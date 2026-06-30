# CLAUDE.md — bx

基于 brook 的 **Linux 透明全局代理**(自研「类 ipio」,单一 Go 静态二进制)。整机 TCP/UDP 经 TUN 自动分流:中国直连、其余走加密隧道,对应用零配置。隧道是**可插拔黑盒子进程**:`brook://` 链接→内嵌 brook(默认),`vless://` 链接→sing-box 的 **VLESS-REALITY**(抗 DPI 伪装);两者其余全自有代码。

- 用户文档见 `README.md`;设计/计划见 `docs/superpowers/specs/` 与 `docs/superpowers/plans/`。
- 模块:`github.com/getbx/bx`,Go 1.26,GitHub `getbx/bx`。
- 平台:**linux/amd64 + linux/arm64**(开箱即用)。macOS 已能编译、待真机验证;Windows 未做。

## 架构(数据面 vs 控制面)

**数据面(平台无关,一行不用动)**:应用 → TUN(gVisor netstack 终结 TCP/UDP)→ `tun.Engine` →
- UDP:53 → `dns` fake-IP 处理器(A 查询返回 `198.18/15` 假 IP,`fakeip.Pool` 记录 域名↔假IP)
- 其余连接 → `dialer.Dialer.Dial`:假 IP 反查回域名 → `route.Router.Decide`(UserDirect/Proxy → china 列表 → 默认)→ 没命中域名就用国内 DNS 解析真实 IP 再按 IP 决策 → Direct(防环直连器)或 Proxy(brook socks5)
- **kill-switch**:隧道不健康时 Proxy 连接直接 Block(fail-closed,不漏真实 IP)
- Router 用 `atomic.Pointer` 热重载(换 china 列表不断流)

**控制面**:`supervisor.Run()`(`run.go`)串起 provision 释放内嵌 brook → 建 Router → **按 server link scheme 选传输**(`transportKind`:`vless://`→reality,其余→brook)起隧道(`tunnel`,socks5 健康检查 + 指数退避重连,**数据面对引擎无感**)→ 开 TUN → 劫持默认路由 → stats unix socket → china 列表自动刷新(经隧道拉)→ 阻塞等信号/死手 → defer 全量还原。

**传输层(可插拔多传输,2026-06 加)**:`tunnel.Tunnel/Runner/socks5Health` 抽象同构容纳**六种引擎**——`NewBrook`(内嵌 brook 子进程)、`NewReality`(sing-box vless-reality,`reality.go`+`vlesslink.go`,TCP 抗 DPI)、`NewHysteria2`(sing-box hysteria2,`hysteria2.go`+`hysteria2link.go`,QUIC/UDP,丢包高 RTT 链路快)、`NewTrojan`(sing-box trojan,`trojan.go`+`trojanlink.go`,TLS)、`NewShadowsocks`(sing-box shadowsocks,`ss.go`+`sslink.go`,认 SIP002 与 legacy 两种 ss:// 格式)、`NewVmess`(sing-box vmess,`vmess.go`+`vmesslink.go`,v2rayN base64-JSON,认 tcp/ws/grpc/h2 传输 + 可选 TLS,port/aid 字符串或数字都吃)。`transportKind`/`buildTunnel` 按 server link scheme 派发(`vless://`→reality,`hysteria2:///hy2://`→hysteria2,`trojan://`→trojan,`ss://`→shadowsocks,`vmess://`→vmess,其余→brook)。**关键不变量自动继承**:引擎不碰数据面,kill-switch/fail-closed/fakeip 分流零成本沿用;防环靠 `serverHostFromLink`(认 vless/hysteria2/trojan 的 authority host;`ss://`/`vmess://` authority 是 base64 走 `tunnel.SSHost`/`tunnel.VmessHost` 专解)做 server bypass。sing-box **已内嵌**(linux amd64/arm64,自建静态 `with_utls,with_quic`,~28MB),`provision.EnsureSingbox` 优先级 `override > 内嵌 > 下载兜底`,根除自举悖论;`embedCacheKey`=版本+内容 hash,重嵌换 tag 也刷新缓存。**新传输真机 e2e 已验(2026-06-30,VPS 203.0.113.20)**:用 bx 自己的 `parseSSLink`/`parseVmessLink`/`parseTrojanLink`+`singboxConfig` 生成的客户端配置,对真实 sing-box 服务端(ss aes-256-gcm / vmess tcp / trojan TLS 自签 insecure)实跑握手——三者经隧道出口 IP 全 == VPS(api.ipify HTTP + 1.1.1.1/cdn-cgi/trace TLS 双验),**ss/vmess/trojan 协议层全部坐实**;**hysteria2(QUIC/UDP,TLS 自签)亦同法 e2e 已验,出口==VPS**——至此 brook/reality/hysteria2/trojan/ss/vmess **六种全部真机握手背书**。**服务端生成 e2e(2026-06-30,`internal/srvgen` + `bx server install --protocol`)**:bx 生成的 **hysteria2 + reality 服务端**配置均真机跑通,出口==VPS ✅。**reality+hys2 合体(`--with-hysteria2`,一份 sing-box 配两入站)+ reality 多用户 share(`srvgen.AddRealityUser` 加第二 uuid)也真机验过**:2-user reality 服务端跑通,经 **share 用户(第二 uuid,链接 `swapVlessUUID` 换壳)** 连上、出口==VPS——`bx server share` 多用户坐实。**reality 一度全挂、真因是默认 SNI `www.microsoft.com` 证书过大**(~3410B 叶证书,超 reality 借壳中继证书承受 → `processed invalid connection`);**换 `www.cloudflare.com`(~1322B)后:VPS loopback 通、Mudi(真实中国网络,egress 203.0.113.30)→VPS 跨主机也通,且 api.ipify(GFW 直连被挡)经 reality 出口==VPS——reality 跨 GFW 坐实。** 教训坑:① **reality 握手 `processed invalid connection` 先查 SNI 证书大小**(microsoft 必挂),别误归因 sing-box #4023 同机问题或网络 MITM(本次都误判过——reality 同机 loopback 用好 SNI 照样通);默认已固定 cloudflare + 回归守卫(`TestDefaultRealitySNINotMicrosoft`)。② 从「本身已被代理(出口 203.0.113.10)」的机器直连 VPS 高端口,TCP CONNECT 成功但批量数据被双跳 MTU 黑洞(health 绿、curl exit 28)——故 ss/vmess/trojan/hys2 的 e2e 用 **VPS loopback 跑 bx 生成的配置**绕开本地烂路径;但 **reality 不能 loopback 同机测得太干净时也 OK**(本次同机用 cloudflare 通了),真实跨 GFW 复验用 **Mudi 路由器**(干净第三方 arm64 客户端)。③ 验出口用 api.ipify.org/1.1.1.1-trace,别用 china 列表里的 ifconfig.me。VPS 防火墙只放行 22141+9999(测高端口要临时 `ufw allow`,测完删)。

**多传输能力(S1-S5,2026-06-29)**:① **自动容灾**(`failover.go`):config `transports: [link,...]`(有序优先级,reality 主),`failoverPolicy.decide`(滞回+冷静期+全挂不切防抖)+ `transportSwapper.swapTo` 后台 `runFailover` 监健康自动切备,全程 fail-closed(swapper 建新→等健康→SetTransport→停旧;全挂保持当前+Block,不横跳)。② **按类分流**(speed-within-safety):config `udp.transport: hysteria2://…`(仅 mode=proxy),dialer `SetUDPTransport`——UDP/QUIC 走 hysteria(速度)、TCP 走主传输,**各自独立 fail-closed**(UDP 传输挂→UDP Block,绝不回落)。其 server 也进 bypass+静态 DNS 防环。③ **单 link bundle**(`blink.EncodeMulti/DecodeAll`):`bx blink l1 l2 …` → 一条 `bx://` 装多传输,`bx setup` 一贴配好全部+容灾;envelope `links[]`,单元素退化 legacy 兼容。④ **裸链接直收+提示**:`bx setup vless://…` 直接用,但 `rawLinkRisk` 提示建议 `bx blink` 换壳(命令行/分享面防泄)。

**包速查**:`cli`(命令)·`config`(yaml schema)·`blink`(base64url 换壳 brook/vless/hysteria2 link,多传输 bundle)·`setup`/`install`(开箱+systemd+自装 PATH)·`provision`(内嵌 brook/sing-box+china 释放,sing-box 兜底下载)/`embedded`(内嵌资产)·`supervisor`(编排+路由+传输派发+自动容灾 `failover.go`)·`tunnel`(brook/reality/hysteria2/trojan/shadowsocks/vmess 子进程隧道)·`tun`(gVisor 引擎)·`dialer`(分流+按类 UDP 传输)/`route`/`dns`/`fakeip`·`stats`(面板)。

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
- **内嵌资产**:`internal/embedded/assets/brook_linux_{amd64,arm64}`(~30MB)+ `singbox_{linux,darwin}_{amd64,arm64}`(linux ~28MB / darwin ~23MB)是提交进仓库的真二进制,按 GOOS/GOARCH 条件 embed(每构建只嵌匹配的那一个;singbox 经 `embedded_singbox_{amd64,arm64,darwin_amd64,darwin_arm64,other}.go`,**linux+darwin 都内嵌(同 brook 平台覆盖,mac 上 reality/hysteria2 也零依赖即跑)**,windows/其他 arch 走 nil 兜底→下载)。CI `embed-brook.yml`/`embed-singbox.yml` 跟上游 release 自动重嵌。换 arch 要补对应二进制。**缓存键掺内容 hash(已实现)**:`provision.embedCacheKey` = 版本 tag + `sha256(内嵌字节)[:12]`,写进 `.brook-version`/`.singbox-version`;同 tag 重嵌不同字节(如 sing-box 从 `with_utls` 加到 `with_utls,with_quic`)也会失效旧缓存、强制重释放,避免用到陈旧二进制。
  - **sing-box 是「自建静态最小构建」不是官方 release 二进制**:官方 linux 包是 glibc **动态链接 + 56MB 全家桶**(含 tailscale/acme/clash/dhcp,reality 全用不上),违背 bx「静态单文件、零依赖」。故从同一 release tag 源码用 `CGO_ENABLED=0 go build -tags with_utls,with_quic`(REALITY 需 utls;**hysteria2/QUIC 需 with_quic**)自建:**静态**(Alpine/musl 也跑,同 brook)、**~28MB**(官方半体积)、同 revision。CI `embed-singbox.yml` 复刻此构建;改时务必保持 `with_utls,with_quic` 与 `CGO_ENABLED=0`。
- **绝不擅自启动 bx / 改路由**:启动是用户的事(需 root、动真实网络)。改完让用户自己 `bx up`。
- gVisor/wireguard 等库的 API 易随版本变——查 `$(go list -m -f '{{.Dir}}' <module>)` 的真实源码,别凭记忆。

## 跨平台待办

- **IPv6**:决策已定 = **fail-closed 阻断**(不走隧道),设计见 `docs/superpowers/specs/2026-06-11-bx-ipv6-blackhole-design.md`。**Linux 已实现**:`Hijack` 探测 `/proc/net/if_inet6`,v6 内核启用时装 `-6 unreachable` 默认路由(table 100 + pref 200)把全局 v6 堵死,`route.DefaultPrivateV6CIDRs`(`::1`/`fe80::`/`fc00::`/`ff00::`)走主表 carve-out;v6 禁用则零 `-6` 步骤、不连累 v4。on-link GUA 邻居也已 carve:`Hijack` 动态读 `ip -6 route show` 提取 on-link 全局前缀(2000::/3、有 dev 无 via)补进 pref-150,与私网段一并直连(纯解析 `parseOnLinkV6Prefixes` 免 root 可测)。**darwin 已实现(编译过、待真机)**:`Hijack` 用 `ipv6EnabledDarwin()`(扫 `net.InterfaceAddrs` 有无非 loopback v6)门控,装两个 `/1` 的 `-reject`(`::/1`+`8000::/1`)盖全量全局 v6;靠主表最长前缀让 link-local/ULA/组播/on-link(含 GUA)自动直连,无需显式 carve-out(故 mac 无 Linux 的 GUA 局限)。纯构造 `darwinRouteSpecs` 免 root 单测。**真机待验**:① `-reject` 确切语法(dummy gw `::1`);② 本地 errno 是否 EHOSTUNREACH(决定 v4 回落);③ `IPV6_BOUND_IF` 与 reject 的交互(今无 v6 出站,moot)。
- **macOS**:代码能编译、review 过(桥接引用计数/生命周期已验证)。**待真机 sudo 验证**:① `Hijack` 的 `route`/`ifconfig` 语义(含 IPv6 `-reject`,见上);② launchd 服务层(`bx up`/`down` 在 mac 的自启)未做。
- **Windows**:未做。需 wintun.dll 分发 + 路由(route/WFP)+ `IP_UNICAST_IF` + Windows Service。最重的一档,留到 macOS 跑通后再做。
- **REALITY 传输收尾**:① **自举悖论已解**——sing-box 改为内嵌(自建静态最小构建,见「约定/内嵌资产」),`bx up` 真零外部依赖,download 仅作无内嵌 arch / 自定义兜底。② **端到端已验(2026-06-28)**:用 bx 自己的 `parseVlessLink`+`singboxConfig` 生成客户端配置,跟真实 sing-box REALITY 服务端(VPS,SNI 借 www.apple.com)握手——出口 IP == VPS、停服务端 → 隧道失败不回落(kill-switch 语义)。**协议层坐实**。③ **真实硬件 e2e 已验(2026-06-29)**:在 GL.iNet Mudi(GL-E5800,**aarch64 + musl OpenWrt**)真机上,内嵌静态 arm64 sing-box 直接执行(`version`/`check` 均过)+ reality 握手到真实服务端成功,经隧道出口 == VPS、而直连同服务被运营商封 → 隧道是唯一通路,无可辩驳。**这坐实了「自建静态而非官方 glibc 动态包」的决策——官方包在 musl 上直接 `not found`,我们的静态构建照跑。** ④ **整机 e2e 已验(2026-06-29,Mudi host 模式 global)**:`bx run` 开 bx0 TUN + 劫持整机路由 + reality 隧道,**整机出口 == VPS、kill-switch 停服务端即 fail-closed、退出 defer 还原干净**(bx0/规则/table 100 全清)。修复期间挖出并修掉一个多 WAN bug:`defaultRoute()` 旧逻辑取最后一条 default、无视 metric,在 Mudi(wlan4 metric20 + SIM metric40 双默认)上错选 SIM(CGNAT 抖)→ 隧道走烂路健康抖动;`parseDefaultRoute` 改按 metric 选首选后,隧道一次健康(410ms)。⑤ **split 模式已验正确(2026-06-29,Mudi)**:china 列表正常加载(`china_domain=12165 china_cidr=6115`)、fake-IP DNS 正常(`ifconfig.me→198.18.0.1`)、foreign 走隧道(icanhazip.com/ipinfo.io 出口==VPS)、china 站直连(运营商 IP)。先前疑似的「漏直连」是**自摆乌龙**:`BX_DEBUG=1` 显示 `dial direct: domain="ifconfig.me"`——**`ifconfig.me`/`ip.sb` 本就在 brook 的 `china_domain.txt` 直连列表里**,bx 照列表正确直连;我拿了 china 列表里的域名当 foreign 出口探测才误判。教训:**验出口/分流别用 ifconfig.me、ip.sb 这类(在 china 列表),用 icanhazip.com / ipinfo.io / api.ipify.org**。（那条 `china_cidr=0` 是 global 模式日志,global 本就跳过 china 列表,非 bug。）⑥ **bx0 MTU 怀疑已证伪(2026-06-29)**:经 bx0 下 9MB(jsdelivr)**完整无损**(http200、字节全量、尾部正常),Mudi 上 apple.com 大 TLS 响应经 bx0+reality 也 200。最初疑似的「MTU」其实是 **api.ipify(Cloudflare)目标特异**——它直连也失败(运营商对 Cloudflare 消费 IP 干扰),与 bx0 无关。gVisor 终结 TCP + 子进程按内核 path-MTU 重分段,bx0 大流量无 MTU 截断问题。**reality 整条线再无已知开放项。****踩坑备忘**:① reality 端口受制于服务端 ufw/云安全组白名单 + 路径对 443 的 DPI 干扰(实测 443 与非白名单高端口 TCP 能连但 TLS 载荷被黑洞)——服务端落已放行高端口(同 brook 9999),勿默认 443;② **OpenWrt/BusyBox `ash` 不支持 `/dev/tcp`**,在路由器上测连通必须用 `curl`/`nc`,否则全是假阴性(曾误判路由器"无 TCP 出网")。
