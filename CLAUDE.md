# CLAUDE.md — bx

基于 brook 的 **Linux 透明全局代理**(自研「类 ipio」,单一 Go 静态二进制)。整机 TCP/UDP 经 TUN 自动分流:中国直连、其余走 brook 加密隧道,对应用零配置。brook 仅作加密隧道黑盒子进程,其余全自有代码。

- 用户文档见 `README.md`;设计/计划见 `docs/superpowers/specs/` 与 `docs/superpowers/plans/`。
- 模块:`github.com/getbx/bx`,Go 1.26,GitHub `getbx/bx`。
- 平台:**linux/amd64 + linux/arm64**(开箱即用)。macOS 已能编译、待真机验证;Windows 未做。

## 架构(数据面 vs 控制面)

**数据面(平台无关,一行不用动)**:应用 → TUN(gVisor netstack 终结 TCP/UDP)→ `tun.Engine` →
- UDP:53 → `dns` fake-IP 处理器(A 查询返回 `198.18/15` 假 IP,`fakeip.Pool` 记录 域名↔假IP)
- 其余连接 → `dialer.Dialer.Dial`:假 IP 反查回域名 → `route.Router.Decide`(UserDirect/Proxy → china 列表 → 默认)→ 没命中域名就用国内 DNS 解析真实 IP 再按 IP 决策 → Direct(防环直连器)或 Proxy(brook socks5)
- **kill-switch**:隧道不健康时 Proxy 连接直接 Block(fail-closed,不漏真实 IP)
- Router 用 `atomic.Pointer` 热重载(换 china 列表不断流)

**控制面**:`supervisor.Run()`(`run.go`)串起 provision 释放内嵌 brook → 建 Router → 起 brook 隧道(`tunnel`,socks5 健康检查 + 指数退避重连)→ 开 TUN → 劫持默认路由 → stats unix socket → china 列表自动刷新(经隧道拉)→ 阻塞等信号/死手 → defer 全量还原。

**包速查**:`cli`(命令)·`config`(yaml schema)·`blink`(base64url 换壳 brook link)·`setup`/`install`(开箱+systemd+自装 PATH)·`provision`/`embedded`(内嵌 brook+china 释放)·`supervisor`(编排+路由)·`tunnel`(brook 子进程)·`tun`(gVisor 引擎)·`dialer`/`route`/`dns`/`fakeip`(分流)·`stats`(面板)。

## 平台抽象(重要:跨平台的接缝)

`supervisor` 已拆成**平台无关 core + 窄接口**(2026-06 重构)。核心原则:**接口按「意图」定义,不按「机制」**——`DirectDialer()`(给我个不绕回隧道的直连器)而非 `SetSOMark`。

- `run.go`(无 build tag)— `Options`、`Run()` 编排、`platform` 接口、serveStats/socks/resolver。**读它看不出 OS。**
- `platform_linux.go`(`//go:build linux`)— `OpenTUN`=fdbased、`DirectDialer`=SO_MARK(fwMark 0x162)、`Hijack`=`ip rule` 策略路由(table 100 + 私网 pref 150 + 全量 pref 200)。
- `platform_darwin.go`(`//go:build darwin`)— `OpenTUN`=utun、`DirectDialer`=`IP_BOUND_IF`、`Hijack`=split-default(`0/1`+`128/1`)。
- `tun/wgbridge.go`(`darwin||windows`)— wireguard `tun.Device` ↔ gVisor `channel.Endpoint` 桥接 + 收发 pump(mac/win 共用;Linux 走 `device_linux.go` fdbased)。
- `paths_<os>.go` — 运行期 socket/pid 路径(linux `/run`、darwin `/var/run`)。

```go
type platform interface {
    OpenTUN(name, addr string, mtu uint32) (link stack.LinkEndpoint, tun tunHandle, closeTUN func(), err error)
    DirectDialer() *net.Dialer
    Hijack(tun tunHandle, serverBypass, userBypass []string) (teardown func(), err error)
}
```
**加一个平台 = 加一个 `platform_<os>.go` 实现这 3 个方法 + `paths_<os>.go`,core 不动。** TUN 生命周期(closeTUN)由 Run 用 defer 接管,Hijack 只管路由。

## 防环 / 安全不变量(改动时务必保住)

- **kill-switch 一以贯之**:隧道挂 → Proxy 决策 Block,绝不降级直连漏 IP。
- **私网/docker 恒直连**:`route.DefaultPrivateCIDRs`(10/8、172.16/12、192.168/16、CGNAT、link-local、loopback),不受 global 影响。
- **服务器防环**:brook→服务器的连接经 bypass 路由走原网关(brook 是子进程,靠路由不靠 socket mark)。
- **bx 自身出站防环**:Direct/resolver/socks 拨号都走 `DirectDialer()`(Linux SO_MARK、mac IP_BOUND_IF)。
- **死手定时器** `--test-timeout`(仅 `bx run`):到点自动还原,远程实测保命。
- TUN 默认地址 `198.51.100.1/30`(TEST-NET-2),刻意避开 docker `172.16/12`。

## 命令模型(2 步开箱)

`bx blink brook://…`(admin 生成 `blink://`)→ `sudo ./bx setup blink://…`(自装进 `/usr/local/bin/bx` + 释放 brook + 连通检测 + 写 `/etc/bx/config.yaml` + 装 unit,**不启动**)→ `sudo bx up`(systemd enable+start)→ `bx status`。其它:`down`(停+禁自启)、`run`(前台调试)、`uninstall`。
固定路径:config `/etc/bx/config.yaml`、brook+列表 `/var/lib/bx/`、binary `/usr/local/bin/bx`、socket `/run/bx.sock`。

## 约定

- **TDD**:先写失败测试→跑红→最小实现→跑绿→提交。纯逻辑测试免 root(用 `t.TempDir()`,不碰真实路由/设备)。
- **验证命令**:`go build ./... && go vet ./... && go test ./...`;跨平台 `GOOS=darwin/GOARCH=arm64 go build -o /dev/null ./...`。
- **提交信息**:中文 conventional commits,结尾带 `Co-Authored-By: Claude …`。在默认分支直接提交(单人项目)。
- **内嵌资产**:`internal/embedded/assets/brook_linux_{amd64,arm64}` 是提交进仓库的真二进制(~30MB),按 GOARCH 用 `embedded_<arch>.go` 条件 embed;CI `.github/workflows/embed-brook.yml` 跟上游 brook release 自动重嵌。换 arch 要补对应 brook。
- **绝不擅自启动 bx / 改路由**:启动是用户的事(需 root、动真实网络)。改完让用户自己 `bx up`。
- gVisor/wireguard 等库的 API 易随版本变——查 `$(go list -m -f '{{.Dir}}' <module>)` 的真实源码,别凭记忆。

## 跨平台待办

- **IPv6**:决策已定 = **fail-closed 阻断**(不走隧道),设计见 `docs/superpowers/specs/2026-06-11-bx-ipv6-blackhole-design.md`。**Linux 已实现**:`Hijack` 探测 `/proc/net/if_inet6`,v6 内核启用时装 `-6 unreachable` 默认路由(table 100 + pref 200)把全局 v6 堵死,`route.DefaultPrivateV6CIDRs`(`::1`/`fe80::`/`fc00::`/`ff00::`)走主表 carve-out;v6 禁用则零 `-6` 步骤、不连累 v4。已知局限:on-link GUA 邻居被连带阻断(follow-up:动态读 `scope link` 路由补 carve-out)。**darwin 未实现**(待真机验 `route -inet6` 语义)。
- **macOS**:代码能编译、review 过(桥接引用计数/生命周期已验证)。**待真机 sudo 验证**:① `Hijack` 的 `route`/`ifconfig` 语义;② IPv6 阻断的 darwin 侧实现(见上);③ launchd 服务层(`bx up`/`down` 在 mac 的自启)未做。
- **Windows**:未做。需 wintun.dll 分发 + 路由(route/WFP)+ `IP_UNICAST_IF` + Windows Service。最重的一档,留到 macOS 跑通后再做。
