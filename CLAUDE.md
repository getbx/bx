# CLAUDE.md — bx

基于 brook 的 **Linux 透明全局代理**(自研「类 ipio」,单一 Go 静态二进制)。整机 TCP/UDP 经 TUN 自动分流:中国直连、其余走加密隧道,对应用零配置。隧道是**可插拔黑盒子进程**:`brook://` 链接→内嵌 brook(默认),`vless://` 链接→sing-box 的 **VLESS-REALITY**(抗 DPI 伪装);两者其余全自有代码。

- 用户文档见 `README.md`;设计/计划见 `docs/superpowers/specs/` 与 `docs/superpowers/plans/`。
- **过程记录见 `docs/lessons/`**(事故复盘、施工日志、守卫的七种失效写法);
  **待人工验收的清单见 `docs/acceptance-pending.md`** —— 那上面每一条都只有人在机器前
  才能做(要在屏幕上点,或要制造一次真实故障),**agent 不要去跑它,也不要替它下结论**;
  本文件各节的「真机未验」标签是那份清单的索引。
- **已知没修的问题与待你拍板的决定见 `docs/known-gaps.md`**(待修 / 待决定 / 等数据 / 接受不动);
  修完回来删掉那一条,拿不准还成不成立先去代码里核。
- 模块:`github.com/getbx/bx`,Go 1.26,GitHub `getbx/bx`。
- 平台:**linux/amd64 + linux/arm64**(开箱即用)+ **macOS 真机已跑通**;**Windows 真机 e2e 已验**(2026-07-09,`030-SJWJ-GSR-B` Win10 19044):全量路由劫持**整机出口==VPS**、reality(sing-box)隧道 390ms 健康、DNS-into-TUN fake-IP(`example.com→198.18.0.16`)、WFP 防泄漏装成功、SSH 经 10/8 旁路存活、死手优雅还原干净。

## 子目录里的 CLAUDE.md(2026-09-23 起)

**本文件只放跨领域的判据**;只跟某一块代码有关的判据下沉到那块代码的目录里,
Claude Code 在读到那个子树的文件时才加载它。**登记在这里的每一份都由
`TestRootClaudeMDIndexesEverySubtreeMemory` 反向钉住**(存在却没登记就红),本文件的
大小由 `TestRootClaudeMDStaysWithinBudget` 钉住,**预算只许往下调** —— 撞上它时,
新写的那一节该进哪个子目录,而不是把数字改大。

- `apps/macos/BxMenu/CLAUDE.md` —— 菜单栏 App:菜单本身、图标、转换通知、各扇窗口
  (Routing Rules / Servers / Diagnostics)、菜单侧的 status watch、离屏快照闸门。
  **动 Swift、或动 `internal/cli/macos_menu_*_test.go` 之前先读它。**
- `internal/guardian/CLAUDE.md` —— 控制面 daemon:平台缝与移植顺序、Core 所有权与锁存、Core 起不来时的
  判别与措辞、调谐环与执行白名单、维护挂起、状态 watch、陈旧恢复快照、响应与失败码。
  **动 Guardian,或动它横跨的 `internal/supervisor/tunneldiagnosis.go`、
  `internal/corestartfailure`、`internal/cli/corestartadvice.go` 之前先读它。**
- `internal/supervisor/CLAUDE.md` —— Core 编排:拆除台账、工人登记册、路由就绪位、内核路由
  自愈(scoped 默认路由、直连出口、服务器旁路、旁路跟随 DNS、Linux 的 Tailscale fwmark)、
  按应用看分流的采集侧。
- `internal/leakcheck/CLAUDE.md` —— 泄漏检测的判据(含 `leakserve`/`loopbackgate` 与 AI 站可达性)。
- `internal/rulereview/CLAUDE.md` —— 规则体检的分类与渲染、死规则。
- `internal/doctor/CLAUDE.md` —— 诊断判据(`bx doctor` 与 `/v1/doctor` 共用),流量成败。
- `internal/dialer/CLAUDE.md` —— 分流决策里的 SNI 规则,`bx explain`(含 `pathview`、`dialfail`)。

## 架构(数据面 vs 控制面)

**数据面(平台无关,一行不用动)**:应用 → TUN(gVisor netstack 终结 TCP/UDP)→ `tun.Engine` →
- UDP:53 → `dns` fake-IP 处理器(A 查询返回 `198.18/15` 假 IP,`fakeip.Pool` 记录 域名↔假IP)。**第一跳是 `staticA`**(域名→固定 A,先于 fake-IP 与 `rules`):既供传输服务器防环,也是用户配置 `hosts:` 的落点 —— **bx 在跑时 `/etc/hosts` 对被 bx 接管的应用是失效的**(应用走系统解析器,而系统 DNS 已被接管到 127.0.0.1),故「把某域名钉到某 IP」只能由 bx 自己提供。值限 IPv4 字面量、**加载期**校验(悄悄没生效正是本功能要消灭的困惑);**传输服务器域名不可被覆盖**(覆盖会让隧道静默连错地方),冲突时以服务器为准并在日志与 `bx status` 里明说这条被忽略;AAAA 不受影响(仍 NODATA,逼应用走 v4,不给绕过覆盖的路)。归一化(大小写 + 尾点)由 `config.NormalizeHostName` **单点**提供,supervisor 与 dns 共用 —— 两侧各写一份归一化时,`Torchfun.com.` 与传输服务器的静态 A 会静默并存,这是 review 抓到的两个 Critical。改了要 `bx down && bx up`(不热重载)。设计 `docs/superpowers/specs/2026-08-08-bx-hosts-override-design.md`
- 其余连接 → `dialer.Dialer.Dial`:假 IP 反查回域名 → `route.Router.Decide`(UserDirect/Proxy → china 列表 → 默认)→ 没命中域名就用国内 DNS 解析真实 IP 再按 IP 决策 → Direct(防环直连器)或 Proxy(brook socks5)
- **kill-switch**:隧道不健康时 Proxy 连接直接 Block(fail-closed,不漏真实 IP)
- Router 用 `atomic.Pointer` 热重载(换 china 列表不断流)

**控制面**:`supervisor.Run()`(`run.go`)串起 provision 释放内嵌 brook → 建 Router → **按 server link scheme 选传输**(`transportKind`:`vless://`→reality,其余→brook)起隧道(`tunnel`,socks5 健康检查 + 指数退避重连,**数据面对引擎无感**)→ 开 TUN → 劫持默认路由 → stats unix socket → china 列表自动刷新(经隧道拉)→ 阻塞等信号/死手 → defer 全量还原。

**传输层(可插拔多传输,2026-06 加)**:`tunnel.Tunnel/Runner/socks5Health` 抽象同构容纳**六种引擎**——`NewBrook`(内嵌 brook 子进程)、`NewReality`(sing-box vless-reality,`reality.go`+`vlesslink.go`,TCP 抗 DPI)、`NewHysteria2`(sing-box hysteria2,`hysteria2.go`+`hysteria2link.go`,QUIC/UDP,丢包高 RTT 链路快)、`NewTrojan`(sing-box trojan,`trojan.go`+`trojanlink.go`,TLS)、`NewShadowsocks`(sing-box shadowsocks,`ss.go`+`sslink.go`,认 SIP002 与 legacy 两种 ss:// 格式)、`NewVmess`(sing-box vmess,`vmess.go`+`vmesslink.go`,v2rayN base64-JSON,认 tcp/ws/grpc/h2 传输 + 可选 TLS,port/aid 字符串或数字都吃)。`transportKind`/`buildTunnel` 按 server link scheme 派发(`vless://`→reality,`hysteria2:///hy2://`→hysteria2,`trojan://`→trojan,`ss://`→shadowsocks,`vmess://`→vmess,其余→brook)。**关键不变量自动继承**:引擎不碰数据面,kill-switch/fail-closed/fakeip 分流零成本沿用;防环靠 `serverHostFromLink`(认 vless/hysteria2/trojan 的 authority host;`ss://`/`vmess://` authority 是 base64 走 `tunnel.SSHost`/`tunnel.VmessHost` 专解)做 server bypass。sing-box **已内嵌**(linux amd64/arm64,自建静态 `with_utls,with_quic`,~28MB),`provision.EnsureSingbox` 优先级 `override > 内嵌 > 下载兜底`,根除自举悖论;`embedCacheKey`=版本+内容 hash,重嵌换 tag 也刷新缓存。**新传输真机 e2e 已验(2026-06-30,VPS 203.0.113.20)**:用 bx 自己的 `parseSSLink`/`parseVmessLink`/`parseTrojanLink`+`singboxConfig` 生成的客户端配置,对真实 sing-box 服务端(ss aes-256-gcm / vmess tcp / trojan TLS 自签 insecure)实跑握手——三者经隧道出口 IP 全 == VPS(api.ipify HTTP + 1.1.1.1/cdn-cgi/trace TLS 双验),**ss/vmess/trojan 协议层全部坐实**;**hysteria2(QUIC/UDP,TLS 自签)亦同法 e2e 已验,出口==VPS**——至此 brook/reality/hysteria2/trojan/ss/vmess **六种全部真机握手背书**。**服务端生成 e2e(2026-06-30,`internal/srvgen` + `bx server install --protocol`)**:bx 生成的 **hysteria2 + reality 服务端**配置均真机跑通,出口==VPS ✅。**reality+hys2 合体(`--with-hysteria2`,一份 sing-box 配两入站)+ reality 多用户 share(`srvgen.AddRealityUser` 加第二 uuid)也真机验过**:2-user reality 服务端跑通,经 **share 用户(第二 uuid,链接 `swapVlessUUID` 换壳)** 连上、出口==VPS——`bx server share` 多用户坐实。**reality 一度全挂、真因是默认 SNI `www.microsoft.com` 证书过大**(~3410B 叶证书,超 reality 借壳中继证书承受 → `processed invalid connection`);**换 `www.cloudflare.com`(~1322B)后:VPS loopback 通、Mudi(真实中国网络,egress 203.0.113.30)→VPS 跨主机也通,且 api.ipify(GFW 直连被挡)经 reality 出口==VPS——reality 跨 GFW 坐实。** 教训坑:① **reality 握手 `processed invalid connection` 先查 SNI 证书大小**(microsoft 必挂),别误归因 sing-box #4023 同机问题或网络 MITM(本次都误判过——reality 同机 loopback 用好 SNI 照样通);默认已固定 cloudflare + 回归守卫(`TestDefaultRealitySNINotMicrosoft`)。② 从「本身已被代理(出口 203.0.113.10)」的机器直连 VPS 高端口,TCP CONNECT 成功但批量数据被双跳 MTU 黑洞(health 绿、curl exit 28)——故 ss/vmess/trojan/hys2 的 e2e 用 **VPS loopback 跑 bx 生成的配置**绕开本地烂路径;但 **reality 不能 loopback 同机测得太干净时也 OK**(本次同机用 cloudflare 通了),真实跨 GFW 复验用 **Mudi 路由器**(干净第三方 arm64 客户端)。③ 验出口用 api.ipify.org/1.1.1.1-trace,别用 china 列表里的 ifconfig.me。VPS 防火墙只放行 22141+9999(测高端口要临时 `ufw allow`,测完删)。

**多传输能力(S1-S5,2026-06-29)**:① **自动容灾**(`failover.go`):config `transports: [link,...]`(有序优先级,reality 主),`failoverPolicy.decide`(滞回+冷静期+全挂不切防抖)+ `transportSwapper.swapTo` 后台 `runFailover` 监健康自动切备,全程 fail-closed(swapper 建新→等健康→SetTransport→停旧;全挂保持当前+Block,不横跳)。② **按类分流**(speed-within-safety):config `udp.transport: hysteria2://…`(仅 mode=proxy),dialer `SetUDPTransport`——UDP/QUIC 走 hysteria(速度)、TCP 走主传输。**专用 UDP 传输挂掉时回落主传输,不 Block**(`113876b`,2026-07-10 刻意反转了原来那条「绝不回落」的不变量:两者去的是同一台 VPS、同一条加密隧道,回落它 ≠ 回落直连、不泄漏,而黑洞 UDP 只会让 hysteria2 一抖整机 UDP 就断);回落记在 `udp_proxy_fallback` 上、`bx status` 的 UDPNotice 会说出来,**主传输也挂才 fail-closed Block**。其 server 也进 bypass+静态 DNS 防环。③ **单 link bundle**(`blink.EncodeMulti/DecodeAll`):`bx blink l1 l2 …` → 一条 `bx://` 装多传输,`bx setup` 一贴配好全部+容灾;envelope `links[]`,单元素退化 legacy 兼容。④ **裸链接直收+提示**:`bx setup vless://…` 直接用,但 `rawLinkRisk` 提示建议 `bx blink` 换壳(命令行/分享面防泄)。

**包速查**:`cli`(命令)·`config`(yaml schema)·`blink`(base64url 换壳 brook/vless/hysteria2 link,多传输 bundle)·`setup`/`install`(开箱+systemd+自装 PATH)·`provision`(内嵌 brook/sing-box+china 释放,sing-box 兜底下载)/`embedded`(内嵌资产)·`supervisor`(编排+路由+传输派发+自动容灾 `failover.go`)·`tunnel`(brook/reality/hysteria2/trojan/shadowsocks/vmess 子进程隧道)·`tun`(gVisor 引擎)·`dialer`(分流+按类 UDP 传输)/`route`/`dns`/`fakeip`·`stats`(面板)。

## 平台抽象(重要:跨平台的接缝)

`supervisor` 已拆成**平台无关 core + 窄接口**(2026-06 重构)。核心原则:**接口按「意图」定义,不按「机制」**——`DirectDialer()`(给我个不绕回隧道的直连器)而非 `SetSOMark`。

- `run.go`(无 build tag)— `Options`、`Run()` 编排、`platform` 接口、serveStats/socks/resolver。**读它看不出 OS。**
- `platform_linux.go`(`//go:build linux`)— `OpenTUN`=fdbased、`DirectDialer`=SO_MARK(fwMark 0x162)、`Hijack`=`ip rule` 策略路由(table 100 + 私网 pref 150 + 全量 pref 200)。
- `platform_darwin.go`(`//go:build darwin`)— `OpenTUN`=utun、`DirectDialer`=`IP_BOUND_IF`、`Hijack`=split-default(`0/1`+`128/1`)。
- `tun/wgbridge.go`(`darwin||windows`)— wireguard `tun.Device` ↔ gVisor `channel.Endpoint` 桥接 + 收发 pump(mac/win 共用;Linux 走 `device_linux.go` fdbased)。
- `paths_<os>.go` — 运行期 socket/pid 路径,落 bx 自有子目录而非共享父目录(linux `/run/bx`、darwin `/var/run/bx`,`internal/secdir` 校验属主与权限)。

```go
type platform interface {
    OpenTUN(name, addr string, mtu uint32) (link stack.LinkEndpoint, tun tunHandle, closeTUN func(), err error)
    DirectDialer() *net.Dialer
    Hijack(tun tunHandle, serverBypass, userBypass []string) (teardown func(), err error)
    // 在**存活**的 TUN 上重落实劫持「路由」(重探网关 + 拆旧装新),绝不删设备。
    RehijackRoutes(tun tunHandle, serverBypass, userBypass []string) error
}
```
**加一个平台 = 加一个 `platform_<os>.go` 实现这四个方法 + `paths_<os>.go`,core 不动。** TUN 生命周期(closeTUN)由 Run 用 defer 接管,Hijack 只管路由。
**`RehijackRoutes` 此前漏在这段代码块外面(2026-09-13 补)** —— 而这段是本文件的**移植说明**:
漏掉它的移植者会在编译期就撞上接口没实现,那还算好的;真正的代价是他不会知道**为什么**
需要第四个方法 —— 换服务器(commit-confirmed 的 Rehijack)、休眠唤醒后 `/32` 旁路被冲掉的
自愈(`internal/supervisor/bypass_route_repair.go`)、`refollowServerBypass` 跟着 DNS 换地址,三条路全经它,
而它们的共同前提是**不能碰 TUN 设备**(拆设备等于把整机流量断在半路)。三平台都实现了。

- **Guardian 侧的平台缝**在 `internal/guardian/lifecycle.go`(`lifecyclePlatform` 六个构造器
  字段)。linux 的门已开但生产 linux 仍是 systemd 直管 supervisor;移植顺序、netns 台子的坑见
  `internal/guardian/CLAUDE.md`。终局路线 `docs/superpowers/specs/2026-08-29-control-plane-endgame-design.md`。

## 防环 / 安全不变量(改动时务必保住)

- **kill-switch 一以贯之**:隧道挂 → Proxy 决策 Block,绝不降级直连漏 IP。
- **私网/docker 恒直连**:`route.DefaultPrivateCIDRs`(10/8、172.16/12、192.168/16、CGNAT、link-local、loopback),不受 global 影响。
- **服务器防环**:brook→服务器的连接经 bypass 路由走原网关(brook 是子进程,靠路由不靠 socket mark)。
- **bx 自身出站防环**:Direct/resolver/socks 拨号都走 `DirectDialer()`(Linux SO_MARK、mac IP_BOUND_IF)。
- **死手定时器** `--test-timeout`(仅 `bx run`):到点自动还原,远程实测保命。
- TUN 默认地址 `198.51.100.1/30`(TEST-NET-2),刻意避开 docker `172.16/12`。

## 命令模型(2 步开箱)

`bx blink brook://…`(admin 生成 `blink://`)→ `sudo ./bx setup blink://…`(自装进 `/usr/local/bin/bx` + 释放 brook + 连通检测 + 写 `/etc/bx/config.yaml` + 装 unit,**不启动**)→ `sudo bx up`(systemd enable+start)→ `bx status`。其它:`down`(停+禁自启)、`run`(前台调试)、`uninstall`。
固定路径:config `/etc/bx/config.yaml`、brook+列表 `/var/lib/bx/`、binary `/usr/local/bin/bx`、socket `/run/bx/core.sock`(bx 自有运行时子目录,不落共享的 `/run` 根,与 darwin `/var/run/bx/` 同构)。

## 泄漏检测(bx 的第二个功能,2026-08-11)

**产品定位由项目所有者定死:「bx 既是检测工具,也是 vpn 就行了」** —— 一个二进制两个功能,
**不建站、不做 WASM、不做引流漏斗**(提过,被否了:需要的人会自己装)。这条决定同时避开了
一个会被墙的目标,也保住「单文件零依赖」那个身份。

**两条命令,受众不同,别再合并**:`bx leakcheck`(人用,开本地一次性页面)与
`bx leak-check`(机器用,非交互 JSON,MCP 与脚本在用,含 Tailscale/ZeroTier/WARP 共存检查与
`--expected-ip` 主动探测)。`webrtc-check` 与 `leak-check --browser` **已删** —— 前者是
leakcheck 的真子集(它跟用户填的期望 IP 比,leakcheck 跟实测出口比),后者带着**第二个对
浏览器开放的本机端口 + 第二份内嵌 HTML 页**:一道安全面有两份实现就有两份要守,而只有一份
会被想起来。MCP 一侧的 browser/browser_confirmed/browser_timeout 三字段与那道确认门也整块
摘掉了 —— 浏览器检查要人在屏幕前点一下,**那从来不适合 agent 代劳**,按构造做不到强于运行期拦。

**`bx leakcheck` 拒绝 root、不读 config、不需要 Guardian、不需要 `bx setup`** —— 一个只想查
自己 Mullvad 的人装完就能用,这是「检测工具」这个定位能成立的前提,改动时别破坏它。

**判据**(四段各自计数、十四条结论、`WhoOwnsTheRoute`、TunnelVision 判据、探测名跨语言
契约、端点三关、AI 站可达性)在 `internal/leakcheck/CLAUDE.md`。**改 `internal/leakserve`、
`internal/loopbackgate` 或 `internal/cli` 里 leakcheck 那几条路径之前先读它**(动那几个包时
它不会自动加载)。

## 真机验收(2026-08-16,项目所有者的 Mac)—— 一次升级把一大批「未验」清掉

`install.sh` 升级路径全程正常(停保护 → 换文件 → 重启 Guardian → 恢复保护),
`go run ./cmd/bx-acceptance` 五项 PASS。**这批东西第一次上真机就没出事**,
而验收器本身在升级**之前**那次跑就已经证明了自己:它当场报出「缺 servers 能力、
/v1/servers 404」—— 跑着的还是升级前那个 Guardian(文件换了、进程没换)。
`能力声明` 是这份报告里唯一能证明进程真的换了的信号,版本号不能。

**同一次验收还抓到一个正在发生的故障**:`*.qq.com 55/68 失败`、
`*.push.apple.com 175/333`,而 `route -n get -ifscope en0 8.8.8.8` 是
`not in table` —— 8-13 那个 DirectDialer 故障的同一签名,**路由是启动之后丢的**
(早上那次跑还是 `直连 446(失败 0)`)。全程 status 显示 Protected、隧道健康:
这个故障对所有既有信号都隐形。`down && up` 之后 scoped 路由回来
(`gateway 192.168.50.2 / en0`),直连恢复 `26/失败 0`。根因已修(见
`internal/supervisor/egress_repair.go`):重装那条路由的触发条件从「记账里的
underlay 变了」改成「去问内核那条路由还在不在」。

**UDP 反事实计数的第一批真机数据**(`bx status --json | jq '.rules[] |
select(.source|startswith("udp_"))'`):`udp_no_change 75`、`udp_proxy 72`、
`udp_proxy_fallback 3`,而 **`udp_would_flip_*` 与 `udp_undecidable` 都是 0**。
两条结论:① 让 china 列表也对 UDP 生效(那个被搁置的选项 B),就目前流量
**一条都不会改变**;② 「大部分 UDP 没有域名可判」这个担心**不成立** ——
fake-IP 反查全部命中。样本约 150 条,要跑几天再定论。

**仍未验**:吞吐历史(需 15 分钟以上运行 + 真实流量)、换服务器的 commit-confirmed
路径(要真切一次、会改出口 IP)、菜单栏那几个窗口(要人在屏幕前点)、
装新 VPS 的表单(要一台空 VPS)、UDP 遵守 direct 规则(要一份升级前的基线)。

## 规则可观测与可编辑(2026-08-13)

一次真实排查暴露的空洞:用户 config 里 `'*.steamstatic.com'` 一类的强制直连规则,
指向的那条直连路已经完全不通(每次 4ms 内失败),而 **bx 就在数据面上、每一次都看见了**
(`dialer.go` 那句 `dial direct failed` 的 debug 日志),然后扔掉。`bx status` 只报
`direct 26186` —— 其中多少失败、被哪条规则逼出去的,一个字都答不上来;用户看到的现象是
「Steam 图片全裂」,于是去怀疑 bx 和 CDN。

**三层,每层单独可测**:① `route.Explain`/`ExplainIP` 返回 `Reason{Source, Rule}`,
`Decide`/`DecideIP` 是它们的**薄壳**(判定只有一份,另有一条测试逐输入比对以防拆开);
`Rule` 是**配置里那一行的原文**(内部把 `*.a.com` 存成 `a.com`,报归一化形式会让用户
去搜一个搜不到的串)。② `stats` 的 `direct_failed`/`proxy_failed` 两个总数 + 按规则的
{attempts, failures, failure_kinds} 表(分类见 `internal/dialer/CLAUDE.md` 的 `internal/dialfail`);内建列表也计数(少了它无从区分「全网在失败」与「只有我这条
规则在失败」)。③ `DecisionCounter` 接口**直接扩而不是做成可选断言** —— 「实现里没有
就静默不计」的计数器与没有这个功能在输出上完全一样(都是 0),而它恰恰是用来发现
「有东西在悄悄失败」的。

**只在真有问题时才占地方**(这是它不被训练成噪声的前提):`Failed  proxy 0  direct 8113` +
点名那一行 + 「改哪个文件、之后要 `bx down && bx up`」。点名门槛同时看绝对数(≥5)与
比例(≥50%):1/1 失败是 100% 但什么也说明不了。**只报用户规则**:内建列表没有哪一行
可点名、用户也改不了。配置路径取自 Core 的 `RuntimeState`(发布**它此刻在用的**那个
文件,与 `DNSUpstream` 同一条纪律),不让叶子包 `stats` 自己猜一份路径常量。

**编辑**:`internal/setup` 的 `AddRule`/`RemoveRule`/`ListRules` 在 **yaml.Node 上做外科
手术**(沿用 `UpdateTransports` 的理由:2026-08-06 那次整份重写把用户手写的 apple/steam
策略连同注释一起冲掉了)。加规则**幂等**、删不存在的**如实报错** —— 不对称是刻意的:
静默成功会让菜单显示「已删除」而配置一个字没变,用户据此重启一次网络、从此不再怀疑
这一步。非法输入在写盘之前挡掉(测试断言盘上文件一个字节没动)。**带 `*` 的值必须加
引号**:不加在 YAML 里是别名语法,写出去读回来直接报解析错误 —— 守卫走生产用的
`config.Parse` 整个读回来,不是「yaml 能解析」。原子替换写盘。

**Guardian `/v1/rules`**(GET/POST,`authorizeOwnerPeer`,与 `/v1/up`、`/v1/down` 同一道门 ——
判据不是「规则更敏感」,恰恰相反,能关掉保护的人已经能做更坏的事,取一致是要点)。
**它不重启任何东西**;应答的 `requires_restart` **刻意无 omitempty**(键缺席读作「这版
没说」)。「没接线」回 501 而不是空列表。新增 `CapabilityRules`。daemon 那段组装抽成
纯函数 `localAPIOptionsFor` 才让接线本身可测 —— **这个仓库全部的事故都在组装根上**。

**菜单**那半(规则窗口、按应答收尾、Swift 解码的 omitempty 陷阱)见
`apps/macos/BxMenu/CLAUDE.md`。

**产品决策(项目所有者否掉了我的提议)**:菜单栏**不加**「Direct rules: N unreachable」
常驻红字 —— 它是常态不是事件(会变墙纸)、没有附带动作、且要算出那个数就得定时主动
探测隧道外面(泄漏检测工具定期发不受保护的流量,新增出站 + 假阳性极便宜)。
**被动观测(系统已经知道的事实)优于主动探测**,这是这一整件事的形状。

**真机已验(2026-08-13,项目所有者的 Mac)**:结果计数第一次上真机就**逼出了一个
一直存在的严重 bug** —— macOS 的 DirectDialer 一直到不了公网,见 `internal/supervisor/CLAUDE.md`「scoped 默认路由」。

## 规则体检与死规则 → `internal/rulereview/CLAUDE.md`

`bx doctor`、`bx status` 常驻告警、Guardian `/v1/rules`、explain、菜单规则窗口都消费
`rulereview.Review`;分类、两个 mode 陷阱、渲染层的静默丢弃、死规则的三道门槛全在那份里。
**改 `internal/cli/rulereview.go`、`internal/supervisor/riskyrules.go`、`internal/rulereviewsrc`
之前先读它。**

## 加规则的风险门与规则热生效(2026-09-08/11)

- **风险判据只有一份**:`policy.DirectRuleHazard`(`internal/policy/policy.go`)判加规则,
  `DirectRisk`(`rulereview` 体检、`policy.Apply` 的 `allow_risk` 门、即 MCP
  `bx_policy_apply`)是它的薄壳。`internal/cli/direct.go` 与 `internal/guardian/rules.go`
  共用它;Guardian 拒绝时回 409 `code=rules_risky_direct`,带 `Force` 才放行。
- **判据是「这条规则覆盖到哪里」,不是「它写成什么样」。** 匹配器是后缀集
  (`route.NewDomainSet` 去掉 `*.` 只存后缀),`bucket.s3.amazonaws.com` 与
  `*.s3.amazonaws.com` 覆盖同一棵子树 —— 「只拦通配、确切主机放行」曾被做过又撤回,
  它在裸写形式上完全不设防。好用由**逃生口**买单(`--force` / 菜单 409 后 Add Anyway),
  不由放松判据买单(`TestEveryOpenPlatformIsHazardousWrittenBareAndReallyCoversStrangers`)。
- **Guardian 写盘成功后叫 Core `/v0/reload`**(与 `bx direct add` 同一条路,不断隧道):
  成功 ⇒ `requires_restart:false`;Core 没应答 / 没接线 ⇒ `true`,**不回滚**(规则已落盘,
  如实说要重连)。守卫 `internal/guardian/rules_reload_test.go`。
- 菜单那半(窗口、右键加规则、按应答收尾)见 `apps/macos/BxMenu/CLAUDE.md`。

## Servers 的线上契约(2026-09-12)

窗口那半见 `apps/macos/BxMenu/CLAUDE.md`;这里是 Guardian 发什么、为什么这么发。
- `/v1/servers` 带 `single_server`(「单服务器配置」≠「清单真的空」)、`current_server`
  (**刻意在清单之外**:塞进清单会让 `serverListEmptyReason` 退回 nil)、Core 报的
  `running`(问不出来就缺席,**绝不与 `current` 合并**:热切先写配置再切,失败那刻两者不同)。
  单服务器配置那台没有名字,「它在不在跑」由 `current_server_running` 说(与 `running` 同一份
  按主机的判据 `hostMatches`;Core 没答话时键缺席)。
  **链接是凭据,从不出门**(`TestServerListNeverShipsTheLinkItself`)。
- 探测 `ProbeReport.measured` **不带 omitempty**(缺席 = 这版没说);Guardian 发码不发中文。
- 切换四种结局各一个码(`arm_failed`/`rolled_back`/`rollback_failed`/`commit_failed`),
  原始错误串不出门;认不出的回落旧常量,消费方必须留「说不出是哪种」的分支。
- **改清单的动词另立能力 `servers_edit`**:只声明 `servers` 的旧 Guardian 收到 `remove`
  会落进兼容分支**切到那一台**,「试着拨一下」的代价是换掉用户的出口国。删当前那台服务端
  409。换链接走 `setup.ReplaceServerLink`(`UpsertServer`/`AddServer` 都会挪 `current`,
  换一条没在用那台的链接会顺手搬走出口)。
  `servers add` 同名回 409;名字可省略,Guardian 用 `setup.LinkHost` 推导(认 `bx://` 换壳)。
- **所有者定死的边界**:不自动容灾、只有用户能切;不按延迟排序、不自动选最快、不分组、
  不导入订阅;**不后台定时探测**(探测走在隧道外面,几台同时握手是很整齐的模式);
  不做每台独立的 rules/dns/udp.mode。

## 仓库里不许有真实基础设施地址与凭据(2026-09-18)

这是公开仓库,而对一个翻墙工具,**「这个仓库 ↔ 这台机器是翻墙出口」这条可被爬取的关联**
比地址本身值钱。项目所有者选了「换掉现有文件 + 加守卫,不改写历史」(force-push 换来的
确定性不高,fork 仍有旧副本);历史里没有真凭据(全量核过)。
- `TestNoRealInfrastructureAddressesInTheRepo` 扫 `git ls-files` 的每个文本文件:公网 IPv4
  要么落在文档保留段/私网/组播/保留段,要么进 `knownPublicAddresses` **并写明理由**。
  **白名单不是黑名单**:加一条的动作本身就是那句要被逼着回答的话 ——「这是第三方服务,
  还是我们自己的机器?」反向断言禁陈旧条目。约定:bx 服务器用 `203.0.113.x`,别的真实
  主机用 `192.0.2.x`。
- **「看起来像真实主机」与「是我们的主机」是两件事**:`192.200.0.101–116` 是 Tailscale
  controlplane 兜底段,生产里由 `{192, 200, 0, byte(i)}` **算出来**,纯文本替换只改了
  测试;ZeroTier root、Tailscale DERP 兜底同理,**功能上必须是真地址**。
- `TestNoUsableServerLinksInTheRepo`:`vless://`/`trojan://`/`hysteria2://` 的 userinfo 必须
  一眼看得出是编的(判据方向是「看得出是假的」),**`bx://` 要解一层再查**(整行粘贴
  `bx server share` 的输出才是最可能的事故)。合成 uuid 约定
  `11111111-2222-3333-4444-555555555555`。**它盖不住 `ss://`/`vmess://`/`brook://`**
  那几种 base64 形式,别读成「凭据这件事全有人管了」。

## 守卫的七种失效写法 → `docs/lessons/guard-antipatterns.md`

**这个仓库最贵的一份方法论。** 七种写法此前散在本文件四个小节里(这一支三节、
按应用看分流一节),合起来一万字而没人会按顺序读完,2026-09-13 集中过去了。清单:
① 钉标识符而性质是关于 JSON 键的 · ② 钉「这东西存在」而性质是「它在某处之后 /
被摆进了视图树 / 真的被用上」 · ③ 钉一个零调用方的壳函数 · ④ 用 `t.Fatalf` 的桩
当依赖(桩一响,真正想验的那条够不着) · ⑤ 断言被满足,但是因为别的理由 ·
⑥ **那条守卫写了、跑了、绿了,而它守的函数一次都没被 `main()` 调到** ·
⑦ **判据是对的,而「把真实输入递给它」的那根线没人守**(钉的是「这次调用发生过」,
不是「到达的值是什么、什么时候到达」)。

**两条可推广的判据**:① 写每一条断言之前,先说出「要让这个缺陷回来,**什么**必须
改变」,再检查这条断言是不是恰好卡住那件事;② **凡是盖测不到的那一半的守卫一律要用
变异证明,不许靠读。** 并且:**凡变异结果是「全绿」或「构建失败」,先查变异本身有没有
落上** —— 它落空过至少七种方式,包括共用脚本被另一个任务覆盖。

## 规则写入路径:归一化与校验只有一半在做(2026-09-12,真机未验)

**两条写入路径此前不同标准,而宽的那条是 agent 也在用的那条。** `internal/setup`
的 `AddRule`/`RemoveRule`(菜单 → Guardian `/v1/rules`)既归一化又校验;
`internal/policy` 的 `apply`(`bx direct/proxy add|rm` 与 MCP 的 `bx_policy_apply`)
两样都不做,只 `ToLower+TrimSpace`。两个后果都实测复现过:

- **对侧删除按字面串比对**,而 bx 的匹配器是后缀集 —— proxy 写着 `*.zoom.us`、
  往 direct 加 `zoom.us`,两条并存、CLI 打勾、MCP 回 `changed: true`,而
  `route.Explain` 先查 proxy 且**没有「更具体优先」**,那条 direct 一次都不会命中。
- **一个字都不校验**:`example.com.` 写得进去,而 `NewDomainSet` 去掉的是**查询**
  的尾点不是**模式**的 —— 它谁也匹配不上,却在 `bx direct ls` 与菜单里长得像一条
  健康规则。同一个输入形状这个仓库付过一次学费(`Torchfun.com.` 的静态 A 冲突,
  两个 Critical,`config.NormalizeHostName` 就是那时长出来的),`rules:` 这个面没跟上。

**判定收敛成一份**:`policy.ParseRulePattern`(归一化走 `config.NormalizeHostName`、
形状走原先住在 setup 的那条正则、最后拿**生产那份 `route.NewDomainSet`** 验一遍
匹配得上)· `policy.RuleCIDR`(网段识别,`supervisor.BuildRouter` 的 `asCIDR` 现在
是它的薄壳)· `policy.CoverageKey`(覆盖键:`zoom.us` 与 `*.zoom.us` 同键)。
`setup.ValidateRulePattern` 变薄壳。**顺带修掉另一条路上的窄校验**:setup 那条
域名正则一直拒 IP 与 CIDR,而 `BuildRouter` 一直支持、菜单右键对 IP 目的地给的
候选(`ruleCandidates` 的「IP 原样」)按构造加不进去 —— **校验器比它守着的那个面
窄,拒的就是合法配置**。

**更窄的 direct 规则被更宽的 proxy 规则盖住时:拒绝,而且刻意不给 `--force`。**
风险名单那道门**可能判错**(用户也许真的独占那台主机),所以它必须留逃生口;
这一道判的不是风险,是 `Explain` 的查找顺序 —— **它不会错**,放行的唯一结果是往
配置里种一条死规则(`rulereview` 事后会把它归成 `ClassOverriddenByOppositeKind`,
但那是体检报告,不是写入时该做的事)。**能判错的门要逃生口,不能判错的门不要。**
出路是真出路:错误里点名挡路的那一行并给出 `bx proxy rm <那一行>`。
**这道门只朝一个方向关**:proxy 排在前面,所以往 proxy 加一条更窄的是**正在生效
的例外**(把一小块流量拉回隧道),同表加更窄的判定也与用户要的一致 —— 两者都不拦,
否则它就变成「总是挡路」的那一类门,而那种门会被绕过或删掉。
删除同样按覆盖键(`rm example.com` 删得掉 `*.example.com`),且**删除不过校验** ——
盘上可能躺着这次修复之前写进去的畸形规则,那正是最需要删掉的东西。

**行为变化,记在这里给下一个人**:① `bx_policy_apply` 与 `bx direct/proxy add`
现在会拒绝畸形写法(空白、引号、URL、`a..b.com`、尾点除外——尾点归一化不打回),
以及被更宽 proxy 规则盖住的 direct 规则;错误带 `policy.ErrCoveredByOppositeMode`
哨兵,MCP 按它给对得上的处置建议(**按错误文本认的话,措辞一改建议就悄悄退回一句
通用的废话**)。② `bx preset apply` 对一份写着更宽 proxy 规则的配置现在**整批拒绝**
而不是静默不写 —— 它此前把 policy 的错误折成 `changed=false`,打印「preset 已经
生效,无改动」,两句话都不真;`editYAMLRuleList` 现在如实返回错误。

**守卫**(`internal/policy/writepath_test.go`,外部测试包才引得到 supervisor):
每条都走 `config.Parse` → `supervisor.BuildRouter` → `route.Explain` 这条生产路径,
不在测试里重算一遍「谁盖住谁」。四条变异各咬中一条。**真机未验**:没有人在真机上
敲过 `bx direct add`。

## 嗅出的 SNI 不许压过真 IP 的规则 → `internal/dialer/CLAUDE.md`

嗅出的域名一条规则都没中时由真 IP 说了算,直连拨的就是这个 IP。细节与测试的坑在那份里。

## 按应用看分流(2026-08-19,整套真机未验)→ `internal/supervisor/CLAUDE.md`

判据在 `internal/supervisor/CLAUDE.md`(采集、活连接表、60 秒窗口、归因时机)与 `apps/macos/BxMenu/CLAUDE.md`
(窗口)。**动 `internal/appattr`、`internal/dialer` 的 `AppRecorder`/`appTrackedConn`、
`internal/tun` 引擎 relay 之前先读前者** —— 动那几个包时它不会自动加载。

## 强制门户(酒店/咖啡店 Wi-Fi)—— 诊断已坐实,修法刻意只做一半(2026-08-23)

现象:酒店与星巴克的 Wi-Fi 下 `bx down && bx up` 是必修课,家里/别处的 Wi-Fi 重连
从来不用。**Guardian 日志坐实**(2026-08-18 16:39–16:42):`recovery-3` 连续 20 次
attempt **全部**停在 `stage=transport_health` / `transport_unavailable`,duration
479s→672s,重试 11 分钟后放弃;直到底层指纹再变一次,`recovery-4` 才 943ms 一次成功。

**根因不是重试不够,是够不着门户。** 强制门户靠劫持 DNS + 拦截 HTTP 重定向把你弹到
登录页;而 bx 开着时 DNS 归 bx、境外域名拿到 fake-IP 走隧道、隧道不健康 ⇒ kill-switch
Block —— **酒店网关根本没看见你的请求**,所以不会重定向,macOS 自己那个
`captive.apple.com` 探测同样被挡。全仓 grep:**bx 里没有任何强制门户的概念**。
家里没有门户,所以从来不发生。

**几条排查时确立、改这块之前要知道的事实**:
- `Rebind` 排在 `transport_health` **之前**,且失败**不回滚**(`r.previous = next` 也
  已经推进)。所以走到 `transport_unavailable` 就说明**路由那半已经修好了**。
- `transportSet.Recover` 失败是 `abort(candidate)` —— 它准备一个**新**隧道,不健康就
  丢掉,**原隧道原封不动还在跑**,而它自带 socks5 健康检查 + 指数退避重连。
  ⇒ 门户一旦被满足,**大概率不需要路径恢复插手**。(**真机未验**,见下。)
- 路径恢复是**纯边沿触发**:`NetworkObserver.checkGeneration` 里
  `current == *previous` 就 return,没有任何电平触发(没有「隧道已经不健康很久了,
  再试一次」)。`maxPathRecoveryAttempts=20` 耗尽后 `shouldRetryPathRecovery` 恒 false。

**「放弃后自愈」刻意没做,理由是证据而不是偏好**:全部历史日志里 **40 次失败
100% 落在 `transport_health`**(另有 2 次 `recovery_canceled`,那一类本就不重试),
**一次都没有在 rebind 之前耗尽过** —— 也就是说「路由没修好才重新武装」这个设计在这
台机器上一次都不会触发。在 71 分钟事故的现场加一段没有证据表明会被用到的代码,是拿
真实风险换假想收益。**要做也只做「耗尽在 rebind 之前」那一支**,别退回无条件重试:
那次事故的机制正是每轮新起一个隧道进程。

**「登录此网络」菜单入口刻意没做。** 它技术上**不**新增能力(`/v1/down` 自 2026-08-07
起对 owner 免密),但:① 它把最危险的动作包装成一件网络范围内的小事,人会在不是门户的
场合也去点;② **自动重新武装是一个可能悄悄违约的承诺**(菜单被杀 / 机器睡眠 / 定时器
被取消),比诚实的 `bx down` 更糟 —— 后者不制造「我还被保护着」这个期待。真正会构成
**新旁路原语**的是「按目的地开口子」(允许明文到 X),那个不做。

**已做的是给信息**(`captiveNetworkHint` in `internal/cli/cli.go` + 菜单
`recoveryFailureReason`):判据窄到只有 `reason=underlay_changed` **且**
`error_code=transport_unavailable` 这一种组合(到处出现的提示会被训练成墙纸);措辞
只给可能性(「常常要」/`often`),不断言这就是门户 —— bx 分不清「门户」与「服务器真
挂了」。**第一步刻意不是「关掉 bx」**:私网恒直连(`route.DefaultPrivateCIDRs`,不受
kill-switch 影响),网关上的门户页在 bx 开着时通常够得着 —— 弹不出来的是**发现机制**,
不是那条路被堵死。down/up 的退路一并保留,顺序由测试钉住。

**那两个「仍在推理」的问题已经在实验室里回答了,不靠去酒店碰运气**(项目所有者
明确否掉了「自己测 + 让用户敲命令行」这两条,两条都对):
- `TestPrivateStaysDirectWhileTheTunnelIsDownButPublicIsBlocked`(`internal/dialer`)
  —— 隧道挂掉时私网仍走直连,**而同一次不健康下公网被 kill-switch 拦下**。
  **对照组是必需的**:只断言「私网通了」证明不了任何事,kill-switch 压根没武装时
  它也通。这条坐实了网关那条路的承重前提。
- `TestProxyResumesTheMomentTunnelHealthReturns` —— kill-switch 是**每次拨号现问
  一遍** `Healthy()`,不是记下来的状态,所以隧道自己重连成功的下一条连接就通。
  **门户登录完之后不需要 down/up。**
**仍然只有真机能答的那一半,要说清楚**:某个具体酒店的门户页,在直接按 IP 打开时
会不会因为引用了公网资源(CDN 上的 JS/CSS)或跳转到公网认证域名而加载不全 ——
那取决于那家酒店,任何实验室都复现不了。所以 down/up 的退路必须保留。

**产品答案是菜单里那个「Open Wi-Fi Sign-In Page」按钮,不是命令行。** 它与上面否掉
的那个「登录此网络」入口是两回事:那个要**关掉保护**,这个只是打开一个 URL ——
没有 fail-open 窗口、没有要守的承诺。(初稿把两个设计混成了一个、用对前者的理由
否掉了后者,这是一次判断错误,记在这里。)安全判断显式住在纯函数 `wifiSignInURL`:
**只接受私网 IPv4**(RFC1918 + CGNAT + link-local),网关是公网 IP 时宁可不给按钮
—— 打开它就是隧道外的一个明文请求;v6 一律不给(v6 是 fail-closed 阻断的)。
取网关那次 spawn 走 `main.swift` 那份**可枚举的进程出口清单**
(`readDefaultRouteOffMainThread`,显式加入并写明理由,**加进来可以、悄悄加不行**)。

## 升级时 launchctl 的竞态(2026-08-13,真机已验)

**升级会把机器停在「保护已关、文件已换、服务没起」(同日,真机已验)**:
`launchctl kickstart -k … : exit status 113: Could not find service`。**根因是竞态,
不是 launchctl 用错了**:`bootout` 返回时服务还没真的从域里消失,紧接着
`EnableGuardian` 问 `Loaded()` 看到一个正在拆除中的服务 → 判 active=true →
计划里**只剩 kickstart、跳过了 bootstrap** → 等它真跑时服务已经没了。修在源头
(bootout 等标签真的消失,约 3 秒上限,**等不到也不报错** —— 停止路径不许因为别的事
没做成而失败);纵深防御是 kickstart 报「找不到服务」时补一次完整加载序列再试
(判据 `launchdLabelAbsent` 早就有,只是这条路上没用上),**只补一次,别的失败一律
如实上报**。真机验收:同一台机器上次升级必落 113,修复后 `重启保护服务 → 恢复保护 →
✓ 升级完成` 干净通过。

**仍未解**:统一安装里菜单栏 LaunchAgent 经 `launchctl asuser 501 launchctl bootstrap
gui/501 …` 仍报 `EIO(5)`。文档此前把这个 EIO 归因到「上一次统一安装残留的 plist」,
本次是从 root 经 `asuser` 走的、仍然失败 —— **那条归因不完整**,待查。

## bx server deploy(2026-08-14,真机端到端已验)

**一条命令把 bx server 装到一台裸 VPS**,跑在管理员自己的机器上(与 `bx server install`
的全部区别:后者要求你已经在那台机器上)。走系统 `ssh`/`scp` —— **bx 完全不经手
凭据**,密码/密钥/agent/known_hosts 全由用户自己的 ssh 客户端处理。

**真机验收(203.0.113.173,全新 Ubuntu 24.04)**:一条命令 → 远端自取并校验
二进制 → 装 reality+hysteria2 → 放行 ufw → 起服务(active+enabled)→ 443 TCP/UDP
双 LISTEN → 打出可直接粘贴的 setup 命令(含 `--udp`)。**再用第二台 VPS
(203.0.113.250)当干净外部视角做端到端**:tcp/443 通而**对照的 12345 不通**、
reality 把未认证探测正确中继到真 cloudflare(http 200)、真实客户端握手 619ms、
**经隧道出口 == 203.0.113.173 而直连出口 == 203.0.113.250** —— 全程用
`run --no-hijack`,VPS#2 零路由改动。

**真机第一次跑打穿三处,都是只有真机才暴露得出来的**:

① **下载方向反了 —— 实测差 490 倍。** 曾按「裸 VPS 连通性未知、本机环境已知」
把二进制放本机下载再 scp。实测 VPS 直下 GitHub **8.36 MB/s**、本机经隧道 **17 KB/s**
(27.6MB 分别是 3 秒与 27 分钟)。那条推理在**可靠性**上成立,在**吞吐**上正好相反 ——
VPS 本来就在目的地那一侧。**但「谁下载」和「谁验证」不必是同一方**:签名清单在
本机取(ed25519 验签),大文件让远端下,**用本机拿到的 sha256 在远端核对**;远端
取不到才回落本机 scp。**校验失败绝不回落** —— 「连不上」换条路对,「拿到的东西不对」
换条路只会掩盖问题。

② **假 fixture。** `bx server install` 打的是可直接复制的一整条命令
`sudo bx setup 'bx://AAA' --udp 'bx://BBB'` —— 链接**带单引号且有两条**,而单测
fixture 用的是裸链接。前面每步都成功,唯独最后取链接失败。漏取第二条会让 UDP
退回主传输、白丢 QUIC 加速。

③ **服务装好了、端口在听,而外面进不来。** Ubuntu 24.04 的 ufw 默认
`deny (incoming)`。`bx server install` 只打了句提示 —— **一条声称「一条命令装好」
的路径把最后一道留给用户读提示,等于没装好**。现在自动放行,**TCP 与 UDP 都放**
(hysteria2 走 QUIC,只开一半会让另一半静默失效而 status 仍显示主隧道健康);
ufw 不在/未启用时安静通过;**改动会打给用户看**(静默改别人的防火墙,他既无从
复核也无从撤销)。

**在本机(开着 bx)构造不出有效的外部可达性测试** —— 这条值得单记,本轮被骗两次:
`curl --resolve <域名>:443:<VPS>` **在 bx 面前不成立**(bx 按域名分流,把请求经隧道
送去了真站,根本没连那台 VPS);改用 `nc -z` 同样无效(经 SOCKS5 时本地握手就算
成功,**必定关闭的 12345/54321 一样「通」**)。**要外部视角就得真有一台外部机器。**

**~~已知缺口~~(2026-08-24 逐条复核,两条都已经不成立)**:

- ~~`applyDeployedLink` 只打印下一步命令,不自动写本机配置~~ —— 给了服务器名时它
  **会**自动写进清单(`addDeployedServer`);只在**写失败 / 没给名字 / 非 root**
  三种情况下才退回「你自己敲一条」。那条退路保留的理由仍然成立并写在代码里:
  **不偷偷提权** —— 写 `/etc/bx` 要 root,而这条命令的其余部分不需要,让一条只做
  ssh 的命令中途弹密码框是坏意外。写失败也不算致命:机器已经装好了、链接就在眼前,
  报清楚原因再退回那条路即可。
- ~~deploy 假定以 root 登录,待补 `--sudo`~~ —— 已做,而且**不是加一个标志,是自动
  探测**:`needsSudo` 读远端 `id -u`(空输出、非数字都如实报错,不猜),非 0 就把
  **整段**脚本包进 sudo。`remoteScript` 的注释点出了要害:简单地在前面加一个
  `sudo ` 只作用于**第一条**命令,后面每条仍是普通用户 —— 失败方式极难查(文件下
  下来了、校验过了,却写不进 `/usr/local/bin`)。没有 TTY 时会预先说明 sudo 若要
  密码会失败。五条测试覆盖(uid 解析 / 整段包裹 / 需要 sudo 时给 `-t` /
  每条远程命令都走 sudo / root 登录不包)。

## AI-native 诊断面(2026-08-31)—— agent 看得见什么

**起点是一次实测而不是设想**:agent 经 `bx_inspect` 拿到的 status 是 Core 的
`stats.Report`,而 `bx status --json` 比它**多 16 个键** —— desired、observed、
divergence、reconcile、protection_state、recovery、dns_state…… **全部是 Guardian
那半**。也就是说这套控制面架构最核心的洞见「意图 / 事实 / 差异」,agent 一个字
都看不到:它答得出「隧道健康、延迟 293ms」,答不出「bx 以为自己开着,而系统说
劫持没生效」。

**根因是 `bx_status` 手挑了六个字段**,之后 Guardian 那半长出十几个键而投影没
跟上 —— 漏掉的字段不会有任何东西报错。所以新工具一律**不再手挑**:

- **`bx_protection`**:原样转发 `bx status --json` 的信封(与 `bx_inspect` 同
  模式),由一条按**返回类型**判定的测试钉住(手写结构体即红)。真 MCP server
  端到端验过:16 个字段全部到达。
- **`bx apps` / `bx_apps`**(`internal/cli/apps.go`):「哪个应用走哪条路」第一次
  离开菜单窗口。**必须采样一个窗口**(默认 6 秒)—— `/v0/apps?subscribe=1` 的
  第一次调用只是订阅,采集从那一刻才开始,拉完就返回必然是空报告,而空报告与
  「真的没有连接」在输出上完全一样。「订阅没成」与「订阅了但确实没有连接」分开报。
  **刻意不带可执行路径**:那是记档在案的信息面扩大,agent 要回答的是「哪个应用
  走哪条路」不是「它装在哪儿」;守卫从**行为**兜(判据打在序列化后的字节上,
  变异验证过),两张发布面白名单**显式加入并写明理由** —— 守卫拦住过我一次,
  而它要的动作正是「想清楚再把自己加进去」。
- **规则体检对非 root 可见**:此前 `config_readable: fail` ⇒ 整个 config 分支
  跳过 ⇒ 体检根本没跑,而 agent 按设计以业主身份免 sudo 跑。修法是**换一条被
  授权的路,不是第二个真相源**:Guardian 的 `/v1/rules` 走 owner 门、读的正是
  同一个文件。退路有**两道门,都是既有测试逼出来的**:只对 `fs.ErrPermission`
  生效(配置**不存在**是「没 setup 过」这个真问题,拿 Guardian 的答案盖住它是
  掩盖故障),且 Guardian 报的 `config_path` 必须与要问的路径相同(否则
  `--config 别的路径` 被一份来自 `/etc/bx/config.yaml` 的答案冒名顶替)。
- **体检本身改由 Guardian 算**(`internal/guardian/rulereview.go`):它有 root,
  读得到配置**与 Core 实际在用的那张 china 列表**,所以给得出完整四类;客户端
  自己算只能给三类(无从知道用户有没有指定自己的列表)。组装下沉
  `internal/rulereviewsrc`,判定仍在 `rulereview.Review` —— 两个消费方共用一份。
  `Class` 因此补了 `UnmarshalJSON`(`MarshalJSON` 的注释原本写着「今天不可达」,
  已更正):认不出的词**不报错**(否则整份报告读不出来)且落到 `ClassRisky`
  (与零值那条刻意的不对称同向)。
  **`nil` 与空报告分开,三处各写一遍** —— 真机当场兑现:跑着的是旧 Guardian
  (不发 review),CLI 报「这一版 Guardian 没有发布规则体检」而不是「你的规则
  都很健康」。

## 诊断:`bx doctor` 与 `/v1/doctor` → `internal/doctor/CLAUDE.md`

`doctor.Judge` 是两个采集方(`internal/cli/doctor_facts.go`、`internal/guardian/doctor.go`)共用的
唯一判据;golden 逐字节、`not_checked` 第四态、关掉保护不是故障、ok 行不带 hint、流量成败与
DirectEgress 那一格都在那份里。**改两个采集方之前先读它。**

## 读源码的守卫:三种处置(2026-08-31)

这一轮退役了三条,**而当时数出来的那两个数(总数与其中 Swift 那部分)已经不能复核,
2026-09-13 起从这里删掉** —— 「什么算『真正读源码』」没有一份写下来的判据(读 `.swift`
的、读自己包里 `.go` 的、AST 纯度守卫、读 `verify.sh` 的,各算不算?),于是那两个数
既复现不出来也重算不了。**一个复核不出来的数字在这份文件里比没有数字更糟**:它看起来
可以引用,而引用它的人得不到任何东西。**方向是确定的:今天比当时多**(2026-08-31 之后
菜单那一块又多出 `macos_menu_ruleswindow_test.go`(09-11)、`macos_menu_doctor_test.go`
(09-09)、`macos_menu_diagscroll_test.go`(09-10)三个新文件,Servers 窗口重做又往既有
那份里加了一批)。真要重数,先写下判据再数,并把判据一起写在这儿。
**结构不变,而结构才是这段话的重点**:这些守卫的绝大多数是 **Swift 菜单守卫**(Go
测试编不了 Swift、`main.swift` 也进不了 Swift 测试 target,读源码是唯一够得着的办法),
其次是**纯度守卫**(按 AST 禁 net/os/exec —— 今天六个:`internal/{appattr,doctor,
leakcheck,pathview,platformcheck,rulereview}/purity_test.go`,**本该存在**,不是变通)。
剩下的 Go 守卫里,多数守的是**否定命题**(「不存在第二条接线」)或**不可调用的函数**
(要出网),结构上无法行为化 —— **它们该留着**。

退役的三条各代表一种处置,值得对照:

| 守卫 | 处置 | 判据 |
|---|---|---|
| `TestRunDaemonDoesNotDiscoverGatewayAtStartup` | **换行为版** | 它只禁一个函数名;变异实测(改走 `platform.DiscoverGateway`)它全绿,而在**没有默认路由的 netns** 里真起 daemon 当场转红 |
| `TestUserSplitRulesArePrependedBeforeOverlayOnes` | **抽纯函数** | 它自己写着「读源码是这里唯一够得着的办法」—— 那是真的,同时是一份待办;判据抽成 `buildSplitRoutes` 后成为行为断言 |
| `TestUDPSourceNamesMatchTheDialer` | **根治** | 它守的是「两份拷贝还一样」;清单下沉 `internal/udpsource` 叶子包后**漂移在构造上不可能**(与 `internal/barriercidr` 同一先例) |

第三种最好 —— **它让守卫失业**,而不是让守卫更聪明。

**一个值得记的细节**:split 那条在两个 for 循环被搬走的那一刻,以「找不到那两个
循环 —— 守卫读不懂现在的代码了」**响亮失败**而不是安静通过。一条读源码的守卫
最坏的失效是锚点漂了还绿着,它在最需要的时候恰好不可达;这一条做对了。

**尚未做、且刻意不在无人监督时开的**:Swift 那一大批的根治办法(把
`BxState`/`resolve()` 搬进能编译进测试套件的 `MenuState.swift`)。**这一半今天仍然
逐字成立**(2026-09-13 复核:`MenuState.swift` 不存在,`resolve()` 仍是
`main.swift` 里嵌在另一个函数体内的一段一百二十多行的定义,靠捕获外层局部变量吃数据);
抽它要把捕获变成显式参数,并让一批读 `main.swift` 的守卫失去锚点、逐条重判 ——
收益是指标,风险是用户天天在用的菜单。

## 日志:一句良性的话刷了 43 万行(2026-09-01,真机诊断,修复真机未验)

`/var/log/bx-guard.err.log` 长到 **100MB 且从无轮转**。逐条查下来,**99% 与
Guardian 无关**:435,128 行是同一句 sing-box 的 `network: missing default
interface`,约 2 次/秒、不停;Guardian 自己那几千行(`guardian_core_scan` 2764、
`network_recovery` 207、`guardian_reconcile_would` 117……)全埋在底下。

**那句话在 bx 的配置下是良性的**:全仓一处 `auto_detect_interface` /
`bind_interface` 都没有 —— bx 从不让 sing-box 去探接口,出站靠系统路由表
(server bypass 那条 /32 走物理网关)。sing-box 的网络管理器无条件启动、探不到
就报一句然后继续,**它报的这件事 bx 压根不用**。所以缺陷整个在 bx 这一侧。

**两个根因,各修一处**:
- `internal/tunnel/stderr.go` 无条件转发子进程的每一行。既有的
  `maxStderrLineLength` 挡不住这个形状 —— 它挡的是「一行很长」,而这里是
  「一行很短、重复很多次」。现按行折叠:窗口内同一行只写一次,**重复次数跟着
  下一次写入(或子进程退出时的 flush)一起报出来**。少了那个数,「刷了 43 万次」
  在日志里与「出现过一次」完全一样,而后者不值得看,前者就是事故本身。环形缓冲
  (健康检查失败时的诊断出口)**不受影响**,压缩的只是日志那一份。
- `internal/guardian/corelog.go`:Core 此前继承 Guardian 的 stdout/stderr,
  两个进程挤进同一个文件。现在 Core 写自己的 `/var/log/bx.log`,bx-guard.* 只
  剩 Guardian 自己的话。**轮转的判据放在每次 spawn,是刻意的** —— Core 只在
  两次 spawn 之间写它,所以永远不需要给一个**正在被写**的 fd 做轮转。

**「给正在被写的 fd 做轮转」在 macOS 上做不干净,别再提 newsyslog**:launchd
持有 Guardian 的 stderr fd,newsyslog 把文件 rename 之后写入会跟着进归档文件,
于是审计线索被悄悄写进一个即将被删掉的文件里。**那比一个大文件糟得多**:大
文件至少还看得见。唯一干净的做法是让写的人自己开文件(dup2 或 `log.SetOutput`)。

**已知缺口(刻意没做)**:Guardian **自己**那份日志仍无轮转,现约 15MB/年
(此前它被 sing-box 那 99% 掩盖着,看不出来)。要修就得在 daemon 启动路径上做
fd 手术,而那是本仓库明写「全部事故都在组装根」的地方,且**无法在不升级真机的
前提下验证**。按「在事故现场加一段没有证据表明会被用到的代码,是拿真实风险换
假想收益」这条,先不做。

**清掉那 100MB 要用截断不是删除**:launchd 与 Guardian 都持着那个 fd,`rm`
一个字节都不会释放(直到 Guardian 重启),而 `sudo : > /var/log/bx-guard.err.log`
就地截断、fd 仍然有效(O_APPEND)。

**分家已真机验(2026-09-01 升级后)**:`bx.log` 末行是当次接管播报(**当时那行还是
中文** `✅ bx 已全局接管`;2026-09-17 起是 `✅ bx has taken over this machine`),
而重启时刻之后 `bx-guard.err.log` 里的 `singbox:` **0 行**,也没有
`guardian_core_log_unavailable`(那条退路没被触发)。**顺带得到一次天然对照**:
err.log 曾被截断过一次,22 小时重新长到 9.4MB(≈10MB/天);升级后两分钟只长
90 字节(≈65KB/天),**约 160 倍**。

**折叠那一半仍未验,而「阵发」这个判断现在有 13 天证据**(2026-09-13 复核):
`bx.log` 里 `missing default interface` 共 **5 次**,分散在 09-10(2)、09-12(3),
**没有任何一次落在同一个折叠窗口内** ⇒ 折叠标记 0 条。所以那句话确实是阵发的、
不是稳态的,而折叠**至今没有素材**。它下次成串发作时 `bx.log` 里会出现带
`[同一行重复 N 次已折叠]` 的行。

**同一次复核把两个外推的数字换成实测**:① **分家已再次确认** —— `bx-guard.err.log`
里 39455 行 `singbox:` **全部在 2026/09/01**(最后一条 23:25:55,正是分家当天),
09-02 起 **0 行**。**注意别拿累计数字当「现在的状态」** —— 光看「有 39455 行」会
把一条已经修好的事判成还在发生。② **Guardian 自己那份日志的增长实测 26.6 KB/天**
(09-02 起 12 天长了 319 KB / 2829 行),年化约 **9.7 MB/年**,比原先外推的
65 KB/天 ≈ 15 MB/年 **小 2.4 倍**;「仍无轮转」这条已知缺口的紧迫性因此比记的低。
今天那个文件 9.7 MB,其中 **96.6% 是 09/01 那一天的历史** —— 要清就用
`sudo : > /var/log/bx-guard.err.log` 截断,**不要 `rm`**(launchd 与 Guardian 都持着
那个 fd,删一个字节都不会释放)。

## `bx explain <目标>`(2026-09-01/05/14)→ `internal/dialer/CLAUDE.md`

判据(问活着的 Core 不重建 Router、合成不重写、Explain 无副作用、失败分类与两句判决、本机视角)
在 `internal/dialer/CLAUDE.md`。**改 `internal/cli/explain.go`、`internal/pathview`、`internal/dialfail` 之前先读它**
(动那几个包时它不会自动加载)。

## 手机那条桥(2026-09-01,真机未验)

**此前是断的,不是不方便**:`bx server share` 打的是 `sudo bx setup 'bx://…'`,
而 `bx://` 是 bx 自己的信封(base64url 包着 `{"v":1,"t":"vless","link":"vless://…"}`)
—— sing-box / Hiddify / v2rayN / NekoBox / Shadowrocket **一个都不认**。真正的
`vless://` 就在 `rec.Link` 里躺着,没有任何一条命令能把它拿出来;想给手机用只能
登 VPS 翻 JSON 手抄。

`bx server share <name> --qr|--format link` 与 `bx server shares <name> --qr|--format link`
(后者**纯读**:不新建用户、不重启 server)。**默认行为一个字节不变。**

- **二维码不是便利,是这条路上正确的载体**:链接自带凭据,打成文本就进了
  scrollback、shell 历史、剪贴板,以及你为了发给自己而经过的那个聊天软件。
  故 `--qr` **绝不同时打印链接原文**(两样都打 = 加了把锁、钥匙插在上面),
  而 `--format link` 必须警告(同既有 `rawLinkRisk`)。
- **quiet zone 是规范的一部分**,反色时必须跟着底色反,否则码外留一圈墨等于把它
  抹掉。**极性**:默认按浅色终端画(深码浅底),深色终端 `--qr-invert`,而这条出路
  必须写在码旁边 —— 一张扫不出来的码与没有这个功能完全一样。
- **不给名字时一个凭据都不打**(一条命令泄漏全部钥匙);**`--json` 与
  `--qr`/`--format link` 是矛盾指令,拒绝而不是静默挑一个**(前者刻意脱敏、后者
  刻意打原文,悄悄挑一个用户不会知道自己拿到的是哪种)。
- 依赖 `rsc.io/qr`(纯 Go、**零传递依赖**)。渲染那半是唯一有判据的部分,抽成纯
  函数单独守。

**未验**:一张码能不能被手机摄像头真的扫出来 —— 渲染的性质(quiet zone/极性/
落点)有守卫,「扫得出来」没有。

## Bx.app 里谁要执行位,判据只有一份(2026-09-18,真机撞到)

**两个写者、两份清单,而窄的那份在常规路径上。** `internal/update` 的 `stageApp`
(Guardian 主持的升级,**保护开着时走它**)只给 `Contents/MacOS/BxMenu` 加执行位;
`internal/cli` 的 `writeMacOSAppTree`(直装,保护关着时才走)还认
`Resources/bx-cli` 与 `Resources/bx-bridge`。

真机后果:一次正常升级之后 `/Applications/Bx.app/Contents/Resources/bx-cli` 是
**0644**,而 `upgradeSwitchCommand` —— 产品自己在 `bx up` 时打印的、用来收掉
Guardian 版本漂移的那条命令 —— 正是直接执行它,于是用户照着敲得到
`command not found`。**一条指向跑不动的命令的提示**是这个仓库反复罚过的那一类,
而这次它出在**修复指引**上,读到它的人正处在「升级只做了一半」的时刻。

**判据下沉成 `updatepkg.MacOSAppFileMode`**,两个写者都用它。**两条守卫各钉一个
写者、都拿那份共用判据当准绳**(`TestStageAppGivesEveryFileTheSharedMode`、
`TestWriteMacOSAppTreeUsesTheSharedFileModes`)—— 任一方不再用它,它那条就红;
合起来「两份清单」在构造上回不来。**断言打在盘上真实的权限位上**,不是打在
「它调用了那个函数」上:后者挡不住「调了、又被下面一行 Chmod 覆盖掉」,而
`stageApp` 里恰好两样都有。第三条 `TestEveryRequiredAppFileHasADeliberateMode`
穷举 `requiredMacOSAppFiles`,逼着往清单里加文件的人回答「它要不要执行位」,
并反向钉住可执行名单里没有陈旧条目。

**同一次真机还暴露出这条修复指引自己的脆弱,`upgradeSwitchCommand` 因此改了形式。**
它原先是 `sudo <bundle>/Contents/Resources/bx-cli app-install`,推理没错(常量上方那段
注释解释得很清楚:裸 `sudo bx app-install` 经 bridge 跑会 `syscall.Exec` 到 runtime,
`os.Executable()` 反推不出包根,必然报 `is not inside a Bx.app bundle`)——但它把这条
指引押在了「安装器给那个文件写对了执行位」上,而上面那个 bug 正好让这个前提塌掉。
**一条修复指引的全部职责就是在降级状态下还能跑**,「安装器把每件事都做对了」是它最
不该依赖的前提。现在是 `sudo bx app-install --app-source /Applications/Bx.app`:显式
传值绕开那一跳反推(旧注释那段推理仍然成立),又不执行 bundle 里的任何东西。
**`unifiedRepairHint` 刻意不跟着改** —— 它用在 `runtime/current` 不完整的场景,而
bridge 正是 exec 到那里,那时 bundle 那份是唯一保证在的二进制;同一个写法在两处有
**相反**的理由,`TestUpgradeSwitchCommandDoesNotDependOnTheBundleExecBit` 与
`TestUnifiedRepairHintStillRunsTheBundleCopy` 各钉一边。
**两条既有守卫(Go 的 `TestUpgradeSwitchCommandCanActuallyRun` 与 Swift 的
StatusReportTests)当场把这次改动拦了下来,这是它们该做的**;判据改成「显式给了就验
那个值,没给就验反推」而不是删掉 —— **换判据不等于放松判据**,把它换成一条只比字符串
的测试才是。

**顺带记两个真机事实,省得下一个人重新挖**:① `/usr/local/bin/bx`(2.6MB)是
**bridge**,真正执行的是 `/Library/Application Support/bx/runtime/current/bx`(96MB);
bundle 里那两个是**安装载荷**,平时不被执行 —— 所以这个 bug 只打穿「直接执行
bundle 那份」这一条路,`bx` 本身一直是好的。② bundle 的 `bx-cli` 与 runtime 里那份
**字节完全相同**(实测同一个 sha256),所以 `sudo bx app-install` 与那条长路径
等价 —— 用户被卡住时这就是出路。

## 升级进度按字节报,不按秒报;下载与换文件分开说(2026-09-18,真机未验)

**一个时钟在下载已经死掉之后照样在涨。** 真机上一个 39MB 的包经隧道下了十几分钟,
而菜单上唯一的数字是 `Downloading and installing… 199s` —— 它结构上答不了用户唯一
想问的那个问题「是不是卡住了」,于是那个问题只能由人来问一遍。**一个朝着已知终点
走的字节数自己就证明自己活着**,这是这次改动的全部判据。

**两段必须分开说,因为它们对用户意味着不同的行为**:下载十几分钟、保护一动不动、
走开完全没事;换文件几秒、屏障装着、网络会停一下。合成一句话等于用同一句同时表示
「随便等」和「别碰」。

- **进度在 `downloadBytes` 上,不在调用点上**(`internal/cli/download_progress.go`)。
  那个函数的三个调用点全是几十 MB 的 release 资产(更新两处 + `bx server deploy`),
  于是**漏接线在构造上不可能** —— 没有人需要「记得传一个 reporter」;清单与签名那
  几百字节走 `downloadBytesContext`,一个字不打。
- **写 stderr 不写 stdout**:`--json` 的契约是 stdout 上只有那份 JSON,而菜单跑的是
  `bx update --json > 日志 2>&1`,两条流进同一个文件 —— 菜单因此**不需要任何新通道**,
  读它本来就在收尾时读的那个文件即可。
- **总量未知时绝不编百分比**(`formatDownloadProgress`):编出来的数会在过半时跳一下,
  而用户无从分辨那是进度还是错的。
- **打多少行由两条判据各管一头**(`shouldEmitProgress`):每多下 1 MiB 一行(下得快时
  细粒度,总行数随包大小有界),每 10 秒至少一行(下得极慢时仍然证明活着 —— 而那
  正是用户会怀疑卡住的时候)。收尾那一行无条件打,否则进度停在 97% 然后画面一跳。
- **菜单那三态是承重的**(`updateStageText`,`installing: Bool?`):阶段由 Guardian 的
  phase 判(下载期间 Guardian 根本没被调用,phase 还停在上一次的终态),而
  **phase 缺席 ⇒ nil ⇒ 一段都不猜**,退回原来那句合并文案。阶段名单只有
  `updatingBanner` 一份,菜单不再抄第二份 —— 漂了的后果是把「正在换文件」显示成
  「正在下载」,用户据此以为可以放心走开。

**跨语言契约**:`⏳ downloaded ` 这个前缀产地在 Go 的 `formatDownloadProgress`,
Swift 的 `lastDownloadProgressLine` 手抄了一份常量。漂掉是**完全静默的** —— 菜单
一行进度都找不到、永远退回秒数,两侧测试全绿。由
`TestMenuReadsTheSameDownloadProgressMarkerTheCLIWrites` 双向钉住(Go 打的每一种
进度行都带这个前缀 **且** 那个常量真的参与解析),与 leakcheck 页面探针名同一条。

**接线那一跳单独守**:`TestDownloadBytesActuallyReportsProgress` 打在真 HTTP 上,
为此 `downloadProgressOut` 是 var(理由同 `downloadStallTimeout`:让测试够得着这条
线)—— 少了它,把那个实参改成 nil 全仓一行不红,而这次改动等于没做。菜单那半由
`TestMacMenuUpdateRowIsFedTheRealStageAndTheRealLog` 钉**到达的值**而不是「调用发生
过」(第七种失效写法),含「日志路径收尾要清掉」——不清则下一次更新开始那一瞬间会
把上一次的陈旧进度显示成当前进度。五条变异各咬中一条。

**刻意不做:把下载与安装拆成两次用户动作。** 「下好了自己挑时间装」听起来体贴,
实际是把一个待办交给用户:一个躺着的包他得记着、会过期(下一个版本出了怎么办)、
而菜单被杀或机器睡过去之后那个承诺会悄悄作废 —— 与否掉「登录此网络」那个入口同
一条理由(**自动重新武装是一个可能悄悄违约的承诺**)。多付的注意力是天天的,换来
的好处(挑一个几秒抖动的时机)一年用一次。

**真机未验**:进度行在菜单上的观感与换行、两段标签的切换时刻、`bx server deploy`
那条路上的进度输出。

## Core 起不来时说出为什么(2026-09-13,真机未验)→ `internal/guardian/CLAUDE.md`

横跨四处:Guardian 的清理与宽限、`internal/supervisor/tunneldiagnosis.go` 的判别拨号、
`internal/corestartfailure` 记录(叶子包)、`internal/cli/corestartadvice.go` 与菜单的措辞。
**动其中任何一处之前先读 `internal/guardian/CLAUDE.md` 那一节** —— 判据全在那里(强杀只对从没服务过的
Core 安全、宽限由 `TunnelDiagnosisTimeout` 派生、五种判别结局、记录只有码、措辞四条)。

## macOS 共存检查:同一份判据别再写两遍(2026-09-18,真机撞到)

`bx status` 与 `bx doctor` 对**同一个事实**说了两句不一样的话:

```
bx status →  Notice  macOS VPN service active: Tailscale
bx doctor →  [WARN]  macOS VPN service connected: 8B24B74E-… "Tailscale"   [VPN:…]
```

根因是 `internal/supervisor/network_guard_darwin.go`(喂 `bx status`)与
`internal/platformcheck/darwin.go`(喂 `bx doctor` 与菜单 Checks 页)里**四个函数
逐字重复**;同日修了前者的措辞而后者没跟上。**两边的测试都绿** —— 因为两边各测各
的那一份。

三个**纯解析**判据下沉成叶子包 `internal/macnetprobe`(`ConnectedNetworkService` /
`SystemProxyEnabled` / `HasTailscaleOverlayRoute`),两处都变薄壳;`darwinAnyProcessDetected`
要 exec,不是解析,留在原地。守卫 `TestTheseJudgementsExistOnlyHere` 用 git grep 断言
**本包之外没有第二份同样的正则**(薄壳可以很多,自己解析的不许有第二个),并带一条
「本包自己那几个正则要真的在」的下限 —— 它当场抓到我漏删的一处残留正则,以及「新包
还没 git add,git grep 看不见」这种守卫自己失明的情形。

**同一轮按真机输出修掉三条用户读不懂的话**:① `rule dead rules` 打的是
「**14 days of cumulative uptime, short of 14 days**」—— `roundDays` 用 `%.0f`
四舍五入,13.6 天被说成 14,而门槛也是 14。改成向下取整:在一道「还不能下结论」
的门上**少说自己的进度是安全方向**。② 三处 hint 写着「answered by tunnel_claims」,
而 `tunnel_claims` 那条 check 在非 root 的报告里根本不出现 —— **一句指向用户看不见
的东西的提示,与指向不存在的命令是同一类**。③ 非 root 跑 `bx doctor`(它**刻意**
允许非 root)会在一份全绿的报告末尾打出
`Diagnostics archive failed: mkdir …: permission denied`,用户读到的是「doctor 失败
了」;现在如实说 `(diagnostics archive skipped: it needs sudo bx doctor)` ——
**「跑不了」与「跑了没过」必须分开**。

**那两条随后也做掉了(同日)**:

- **`Loop` 那行改成稳态沉默**(`reconcileRoundIsQuiet`)。一台健康机器上它每次都长
  一个样(`no divergence (unchanged for N rounds) · scanned 1 Core process(es)`),
  用户读不出该做什么、也无事可做 —— 每次都在的东西会被训练成墙纸,然后把真正要紧
  的那一次一起淹掉。**「安静」的判据是「没有任何可行动的内容」,不是「没出错」**:
  报告发霉 / 被栅栏挡住 / 提议过动作 / 真的执行过 / 有项目没观测到 / Core 进程数
  不是 1 / 没数出来 / `UnchangedRounds == 0`(这一轮与上一轮不同,是一次转变),
  任何一条都要重新开口。守卫两半缺一不可 —— 少了「安静时不说」缺陷原样回来,少了
  那张「每一种都要开口」的表,一个**干脆永远不说**的实现也能全绿,而那会把发霉的
  报告与双 Core 一起藏掉。**`bx status --json` 一个字段都没少**:agent 拿全量,
  人拿信号。
- **`Via` 不再重复 `Server` 刚说过的地址**(`reality@203.0.113.92  UDP→hysteria2@203.0.113.92`
  → `reality  UDP→hysteria2`)。**判据是「与 Server 相同才省」,不是无脑去掉 `@`
  后面的东西** —— `udp.transport` 指向另一台服务器是 bx 支持的真实配置,那时这两个
  host 的差别恰恰是这一行最值钱的信息。

**这一条改动当场演了一遍第三种失效写法**:那个 helper 写好了、单测绿了,而**调用点
根本没改**(一次脚本在写盘前抛了异常,替换只在内存里发生过),于是一个零调用方的壳
函数被一条绿测试盖着,真机输出一个字没变 —— 是拿新二进制去真机上看输出才发现的。
断言因此下沉到 `Render()` 的**输出行**上,而不是停在那个纯函数上。

## `bx --help` 分组:顶上那一屏只放天天用的(2026-09-18)

把 help 树当用户读了一遍:**28 个命令平铺成一列**,而天天敲的 `up`/`down`/`status`
排在第 15、16、26 位,前面挤着 `server`/`invite`/`user` 这些装服务端才用的东西。

现在按 `Category` 分组。**urfave 把没有 Category 的命令渲染在最前、且不带标题** ——
顶上那一组因此是 `up`/`down`/`status`/`update`,其余进
Diagnose / First run / For agents / Routing rules / Server side / Windows only。
分类名**按字典序**排(`commandCategories.Less`),顺序不可控,所以别指望用名字排出
想要的次序;要紧的是日常四条在顶上、相关的聚在一起。

**守卫守的是「新命令不会静默落进顶上那一组」**
(`TestEveryTopLevelCommandDeclaresWhereItBelongs`)—— 漏填 Category 的后果不是
「没分组」,是**它看起来像一条天天要用的命令**;另有反向断言禁止 everyday 表里
留已经不存在的命令。

**两条 leakcheck 的描述从三行压成一行,长解释搬进各自的 `--help`**(`Description`)。
列表里用户只需要回答一个问题:我该打哪一个。**但 `bx leakcheck` 的 Usage 必须保留
「browser page」这几个字** —— 既有守卫 `TestLeakCheckCommandIsRegisteredAlongsideLeakCheck`
钉着它,而我压缩时写成了「opens a page」,当场被它拦下:那条守卫要的性质是「用户
分得出它会开浏览器」,而「a page」在终端里读起来可以是别的东西。

**顺手补上一条射程外的守卫**:`TestNoHelpTextCarriesMarkdown` —— help 文案里不许有
markdown 的 `**`(终端不渲染,用户读到的是字面星号)。这条纪律本仓库立过两次
(corestartadvice 与 leakcheck 各一条),而 **help 文案一直在那两条的射程之外**;
我给 leakcheck 写 Description 时当场又写进去一对,是看终端输出才发现的。

## macOS 首装面:dmg 早就打好了,而它说的是中文(2026-09-18)

所有者原话「首次安装太麻烦了,dmg 应该有打包的?」。**查下来 dmg 与引导都已经在**:
release 里有 `bx-macos-<arch>.dmg`,包内是标准形状(`Bx.app` + `Applications` 软链
+ `README.txt`,`package-macos-dmg.sh` 建),README.md 第 22 行就是「macOS:双击装,
不用开终端」,而首次引导也有(`FirstRun.swift` 的 `firstRunAction`:双击 → 主动问
`Install bx?` → 装完主动问 `Set Up bx…`)。**所以「要不要打包」这件事不是缺口。**

真缺口是别的:**包里那三份东西整片都是中文** —— `README.txt`、`install.sh` 与
`uninstall.sh` 打印的话。而产品(App、CLI、菜单)这一轮已经全改英文,**于是唯一还
说中文的那个面,恰好是给还没用过 bx 的人看的那个**。已翻译;
`TestTheMacOSPackageSpeaksToUsersInEnglish` 钉住三段 heredoc 里**用户可见的行**
(以 `#` 开头的注释不在射程内,与全仓「注释中文、用户可见英文」同一条)。

**dmg 里那份说明改了名字**:`README.txt` → `Open me first - macOS will say bx is unverified.txt`,
并且把 Gatekeeper 那一段**提到了最前面**。理由是那段说明是首装唯一一道过不去的坎,
而没有人会在拖完图标之后去点开一个叫 README 的文件 —— 他卡在系统弹窗前,而说明
就在旁边。那一段现在**先说两个对话框的区别**:「无法验证开发者」可以放行;
「已损坏,应移到废纸篓」**不许放行、也不许照网上说的跑 `xattr`** —— 后者正好关掉
包被改动过的检测,而那是这个产品唯一能给用户的防篡改信号。

### `verify-macos-release.sh` 与打包脚本之间此前无人看管(2026-09-18)

那个脚本逐句钉住发布包里三份文件的内容,而它**只在发版流水线里跑** ——
`scripts/verify.sh` 够不着它。于是「改了生成脚本的文案」与「那些 grep」之间的漂移
是静默的:本机全绿,**打 tag 才红,而那时你正在发版**。

`TestReleaseVerifierAndPackagerAgreeOnEveryLiteral` 把两边对上,不需要真的打包:
正向断言的每一句必须在对应 heredoc 里找得到,反向断言(`! grep`)的每一句必须
找不到。**它第一次跑就抓到两个真东西,都是同一次翻译造成的**:

1. 七条 grep 找的还是中文原句 —— 我把包内文案翻成英文时没动它们(这一条是读脚本
   时发现的,不是测试);
2. 更隐蔽的那条:新写的
   `echo "... and sets bx up (one administrator prompt)."` 里含子串 **`bx up `**,
   而 `verify-macos-release.sh` 有一条 `! grep -qF "bx up "` —— 那是为了保证
   install.sh **绝不调用** `bx up`。**一句纯文案翻译撞上了一条安全断言**,
   而除了这条守卫,没有任何东西会在发版之前说一个字。

同一轮还发现:我第一版重写 README 时**把整个 Notes 段删掉了**(替换区间从 heredoc
开头一直到 `TXT`),而那里面有几条是刻意的安全披露(「全新安装不启动保护、不修改
DNS/路由」「覆盖安装会在你确认后重启保护」)。**翻译一段文字时,先确认你替换的
区间就是你读过的那一段** —— 上面那条守卫现在也钉住了这几句的存在。

**剩下的首装摩擦都不在代码里,写下来省得下一个人再查一遍**:

1. **Gatekeeper**(「无法验证开发者」→ 系统设置 → 隐私与安全性 → 仍要打开)是最大
   的一道,而它只能用 Apple Developer ID 签名 + 公证解决 —— 那是一笔年费与一个账号,
   不是代码。打包脚本里的 ad-hoc 签名(`codesign -s -`)满足不了 Gatekeeper,它防的
   是另一件事(包被改动过会显示「已损坏」,那条路**没有**放行入口)。
2. **鸡生蛋**:dmg 从 GitHub 下载,而 GitHub 正是你还没有 bx 时够不着的地方。
3. **首装必须先有一条 `bx://`**,也就是先有一台服务器 —— 对一个从零开始的人,
   「装客户端」和「有服务器」是两件事,而 dmg 只解决前者。

## 约定

过程(每条守卫的来历、事故复盘、CI flake 的逐条经过)在 `docs/lessons/conventions-archive.md`。

- **文档本身有守卫,别让「关于代码的陈述」无人看管。** 任意目录的 `CLAUDE.md`、`README.md`、
  `docs/` 下除 `superpowers/` 之外的 `.md`、`apps/` 下 Swift 的注释:点名的**文件**必须存在
  (`TestDocumentedFilePathsExist`),点名的**测试**必须存在(`TestEveryTestNameMentionedInProseExists`,
  对前缀宽容)。**`docs/superpowers/{specs,plans}` 刻意不在范围**:区别是时态,计划书点名
  「将要建的东西」,失效是预期的。刻意退场的测试登记进 `retiredTestNames` 并写明被谁接手;
  退场的名字**不许在同一句话里被当成现在时的守卫**(「由 X 钉住」)。守卫够不着要扫的东西时
  必须 `t.Fatal`(安静地扫了零个文件的守卫与没有守卫在输出上一样)。
- **CLAUDE.md 的分家规则**:跨领域判据留根目录;只跟某块代码有关的判据下沉到那块代码目录的
  `CLAUDE.md`(登记在本文件顶上那一节);过程进 `docs/lessons/`;逐条验收步骤进
  `docs/acceptance-pending.md`,但「真机未验」标签留在判据旁边(它是待办不是历史);
  **判据不许只存在于 lessons 里**;下沉时原文逐字存进 `docs/lessons/*-archive.md`。根目录与
  每份子目录都有大小预算(`internal/cli/claudemd_budget_test.go`,根目录的数**只许往下调**)。
  清理的判据是「说的是不是今天的事实」,不是「旧不旧」:**一句声称某个 bug / 限制仍然活着的话,
  比一句普通的陈旧记述更坏**;被明确否掉过的方案与判据的理由不删(删了会被重新提出来)。
- **TDD**:先写失败测试→跑红→最小实现→跑绿→提交。纯逻辑测试免 root(用 `t.TempDir()`,
  不碰真实路由/设备)。
- **绝不并行派两个会写盘的子代理进同一个 checkout。** 路径不相交挡得住 git 冲突,挡不住两件事:
  `verify.sh` 是全树的(会读到对方的半成品)、**`git stash` 是全树操作**(2026-08-17 一方为隔离
  自己把另一方五个未提交文件整批卷走)。只读的 review 代理可以并行;要真并行写,各自一个
  worktree。**一方的报告不是全局事实**,尤其当那一方恰好是肇事者。
- **验证命令**:`bash scripts/verify.sh`(全量)或 `--quick`(改一行时;横幅逐条报出跳过了哪几步)。
  **判据一律是退出码,不是字符串匹配**(`go test … | grep …; git commit` 用 `;` 串联、grep 错的
  失败字样、`grep -c` 数的是行数……同一个根因栽过六次),**别再手敲那一串命令**;漏一道闸门由
  `TestVerifyScriptCoversEveryGate` 钉住。步数别写死。两步值得知道为什么在:**windows 测试
  typecheck**(`go build` 从不编译 `_test.go`,`*_windows_test.go` 里的错只在 CI 红;刻意不写成
  `GOOS=windows go vet ./...`,`internal/tray` 有一条既有告警会让它恒红)、**integration 测试
  typecheck**(`//go:build integration && linux` 的 netns 台子本机一个字都看不见;它只 typecheck
  不跑,「编得过、断言不再成立」仍然只有 CI 那条腿证得了)。
- **一个会偶发红的闸门比没有闸门更糟**,它训练人去重跑。三条可复用的判据:
  ① **活过测试函数的 goroutine 不许碰 `*testing.T` 的任何方法**(`Errorf`/`Helper`/`Log` 都算,
  测试返回后调用会 panic),也不许在测试结束后往 `t.TempDir()` 里写(与 `RemoveAll` 抢目录)——
  正确形状是 goroutine 只把错误递给 channel,由 `t.Cleanup` **先等 goroutine 退出再排空**转成
  `t.Errorf`;② **拿挂钟指定「哪一步该失败」就是在赌调度**(`WithTimeout(40ms)` 想让第 N 步失败,
  在忙机器上会失败在更早一步、红在别的断言上)—— 换成由调用方决定何时到期的 context 替身,
  并在没走到预定那一步时**当场说出来**;把超时调大不算修;③ 守卫钉**修法的机制**而不是那个
  flake(1/375 的失败率跑一遍抓不到)。**本机 `verify.sh` 覆盖不到 Linux 的平台差异**(CI 的 build
  job 跑 Linux),复现用 Colima 的 linux 容器。
  **那条挡过两次发版的 `TestManagerUpdateReservesDeadlineForTargetCleanup` 已修(2026-09-23),
  而根因是产品缺陷不是测试**:新版 Core 的健康等待只给「清理新版」留了预算、没给「回滚」留,
  新版卡满时回滚只剩零头 —— 生产上就是「更新失败之后连回滚也失败」。修法与判据见
  `internal/guardian/CLAUDE.md`「更新的预算」;没有调大任何一个超时。**「偶发红」先去找它在
  分什么预算,别先怀疑 runner。**
- **真机与 CI 互不替代。** 真机验 CI 验不了的(平台语义、真实文件系统、网卡、网络);CI 验真机
  验不了的(**干净 checkout** —— `.gitattributes` 的效果只有它证得了、没有本地遗留物)。真机
  可能比 CI 宽松(那台 Windows 恰好有 `C:\tmp`,写死 `/tmp` 的测试在它上面全绿而 runner 上红 11 条)。
- **提交信息**:中文 conventional commits,结尾带 `Co-Authored-By: Claude …`。在默认分支直接提交(单人项目)。
- **内嵌资产**:`internal/embedded/assets/brook_linux_{amd64,arm64}`(~30MB)+ `singbox_{linux,darwin}_{amd64,arm64}`(linux ~28MB / darwin ~23MB)是提交进仓库的真二进制,按 GOOS/GOARCH 条件 embed(每构建只嵌匹配的那一个;singbox 经 `embedded_singbox_{amd64,arm64,darwin_amd64,darwin_arm64,other}.go`,**linux+darwin 都内嵌(同 brook 平台覆盖,mac 上 reality/hysteria2 也零依赖即跑)**,windows/其他 arch 走 nil 兜底→下载)。CI `embed-brook.yml`/`embed-singbox.yml` 跟上游 release 自动重嵌。换 arch 要补对应二进制。**缓存键掺内容 hash(已实现)**:`provision.embedCacheKey` = 版本 tag + `sha256(内嵌字节)[:12]`,写进 `.brook-version`/`.singbox-version`;同 tag 重嵌不同字节(如 sing-box 从 `with_utls` 加到 `with_utls,with_quic`)也会失效旧缓存、强制重释放,避免用到陈旧二进制。
  - **sing-box 是「自建静态最小构建」不是官方 release 二进制**:官方 linux 包是 glibc **动态链接 + 56MB 全家桶**(含 tailscale/acme/clash/dhcp,reality 全用不上),违背 bx「静态单文件、零依赖」。故从同一 release tag 源码用 `CGO_ENABLED=0 go build -tags with_utls,with_quic`(REALITY 需 utls;**hysteria2/QUIC 需 with_quic**)自建:**静态**(Alpine/musl 也跑,同 brook)、**~28MB**(官方半体积)、同 revision。CI `embed-singbox.yml` 复刻此构建;改时务必保持 `with_utls,with_quic` 与 `CGO_ENABLED=0`。
- **绝不擅自启动 bx / 改路由**:启动是用户的事(需 root、动真实网络)。改完让用户自己 `bx up`。
  **这条约定被 shell 绕过去过一次**(2026-09-13):一个只读排查代理在**双引号**的 grep 模式里带了
  反引号,zsh 把它当命令替换执行了 `bx up`。这个仓库的文档里到处是反引号包着的命令,而搜文档是
  每个代理开工第一件事。**规矩:凡是搜索/匹配用的模式一律单引号**(双引号里要出现反引号就转义)。
- gVisor/wireguard 等库的 API 易随版本变——查 `$(go list -m -f '{{.Dir}}' <module>)` 的真实源码,别凭记忆。

## 跨平台待办
### IPv6

决策已定 = **fail-closed 阻断**(不走隧道),设计见 `docs/superpowers/specs/2026-06-11-bx-ipv6-blackhole-design.md`。**Linux 已实现**:`Hijack` 探测 `/proc/net/if_inet6`,v6 内核启用时装 `-6 unreachable` 默认路由(table 100 + pref 200)把全局 v6 堵死,`route.DefaultPrivateV6CIDRs`(`::1`/`fe80::`/`fc00::`/`ff00::`)走主表 carve-out;v6 禁用则零 `-6` 步骤、不连累 v4。on-link GUA 邻居也已 carve:`Hijack` 动态读 `ip -6 route show` 提取 on-link 全局前缀(2000::/3、有 dev 无 via)补进 pref-150,与私网段一并直连(纯解析 `parseOnLinkV6Prefixes` 免 root 可测)。**darwin 已实现(编译过、待真机)**:`Hijack` 用 `ipv6EnabledDarwin()`(扫 `net.InterfaceAddrs` 有无非 loopback v6)门控,装两个 `/1` 的 `-reject`(`::/1`+`8000::/1`)盖全量全局 v6;靠主表最长前缀让 link-local/ULA/组播/on-link(含 GUA)自动直连,无需显式 carve-out(故 mac 无 Linux 的 GUA 局限)。纯构造 `darwinRouteSpecs` 免 root 单测。**真机待验**:① `-reject` 确切语法(dummy gw `::1`);② 本地 errno 是否 EHOSTUNREACH(决定 v4 回落);③ `IPV6_BOUND_IF` 与 reject 的交互(今无 v6 出站,moot)。

### macOS

代码能编译、review 过(桥接引用计数/生命周期已验证)。**待真机 sudo 验证**:① `Hijack` 的 `route`/`ifconfig` 语义(含 IPv6 `-reject`,见上);② launchd 服务层(`bx up`/`down` 在 mac 的自启)未做。

**逃生路径不变量(2026-08-04,71 分钟事故换来的)**:**关闭与恢复路径不得依赖可能
已失效的前置条件。** `sudo bx down` 的干净路径**任何一步失败**(Guardian 不可达、
接管失败、`client.Down` 报错、客户端 30 秒超时)都落到**强制拆除**;强制拆除跳过
安装/bootstrap,按序做四件事、**任一步失败都继续做完剩下的**:① 经 Core 自己的
`/v0/shutdown` 协作关闭(不指望 `launchctl bootout` 的 SIGTERM 投给 Core ——
代码里根本没有 `Setpgid`/`Setsid`);② `bootout` 停 Guardian;③ `SaveDesired(off)`
(否则 plist 的 `RunAtLoad`+`KeepAlive` 下次开机把坏状态带回来);④ **清屏障阻断路由**
—— bootout 抹掉内存里的 `barrierOwnership`,而内核里的 `/2` reject 路由会留下来打死
整机连通,且此后新 Guardian 认为「无屏障」照样报绿灯,孤儿屏障在 up/down/uninstall
全周期存活。清屏障是**用户显式请求停止保护**的结果,不是隧道不健康时的回落,不削弱
fail-closed。文案只列举做过的动作、**不断言「网络已还原」**。
**`bx force-teardown` 这个命令只活在 spec 里,从来没有被实现** —— 要关掉保护敲的
就是 `sudo bx down`,它自己会落到强制拆除;这段话里绝不许再印一个不存在的命令,
读到它的人正处在「关不掉保护」的时刻,一句 `command not found` 是这份文档能造成的
最坏伤害。

**故障可观测性不变量(2026-08-05,另一次真实事故)**:**失败必须留下可操作线索。**
Guardian 的四个 handler 失败时把**完整错误写进 Guardian 日志**,响应体**只带失败码**
(绝不外传原始错误串 —— 可能含路径/链接/凭据);新鲜度用**递增的代际号**而非值比较
(值比较会把「持续复现的同一失败」误判成陈旧丢弃,而那恰是主用例);**码可能被省略,
宁可不带也不能带错的**。两个例外由**错误本身**命名而不经 `needsAttention`:
`recovery_incomplete` 与 `guardian_busy` —— 它们恰是「启动恢复已失败 / 用户反复
`bx up`」的表现形式,按上面的规则会被省略码,**最需要指引的场景反而无码**。
CLI 对 **500 一律附排查指引**(没有码的 500 恰恰最无从下手),且指引必须点名
`/var/log/bx-guard.err.log` —— `bx logs` 读的是 **Core** 日志,不含 Guardian 失败原因。
Guardian 日志是 **0600 root:wheel**(`install.SecureGuardianLogs`);**2026-08-11
之前安装的机器仍是 0644**,里面有服务器 IP 与 bypass 网段。
**逐条经过见 `docs/lessons/2026-08-control-plane.md`。**

**观测层与不变量基线(2026-08-05,`internal/observe`,纯逻辑+单测,真机未验)**:**意图 / 事实 / 代码 三分**——意图只由用户/agent 显式声明式改动;事实只由调谐环改动(判断与执行分离,见 `internal/guardian/CLAUDE.md`);代码由人经 PR 评审改动。**agent 只声明意图,从不直接驱动动作。** `internal/observe` 是**只读**观测层,向系统现问四件事:劫持是否生效(`route -n get 1.1.1.1`/`129.1.1.1` 的接口 == 我们的 TUN)、屏障是否在位(查 `barriercidr.Blocking()` 那组 `/2` 网段是否被内核以 reject 应答;该清单已下沉到叶子包 `internal/barriercidr`,装屏障的 guardian 与问内核的 observe 共读同一份,`guardian.BlockingBarrierCIDRs` 已删)、DNS 归谁(`install.InspectDNSContext`)、Core 是否活着(`supervisor.FetchRuntimeState`——**控制 socket 在应答本身就是存活观测,不需要 PID 文件**)。**三态 `Tristate` 是关键取舍**:区分「观测到否」与「观测不到」,零值为 `Unknown`;用 `bool` 会把「问不出来」压成 false,而这正是旧架构骗人的方式之一(`RoutesInstalled` 是个只置位不复查的 `atomic.Bool`)。任一项观测失败即记为 `Unknown` 并附原因,**绝不中断其余项、绝不让调用方失败**。`bx status --json` 现**并列发布** `desired`(意图)、既有信念字段、`observed`(事实)、`divergence`(二者之差),**不用观测覆盖信念**——二者的 diff 本身就是最高价值的诊断信号;「status 显绿而流量明文直连」在这个结构下表达不出来,绿是 believed 而 observed 会同时说 `capture_ok: false`。首批四条不变量由 `internal/observe` 的纯函数测试钉住(protected 必须三项皆 True、`desired=off` 不得残留屏障/DNS、不一致必须产出自解释 divergence);**第五条(拆除永不拒绝)是行为性质,observe 够不着**:`invariants_test.go` 里那条 `t.Skip` 写明了今天由 `internal/cli` 的强制入口测试保住,非 darwin 的关闭路径没排查过。**两处相对设计的有意偏离**:① `Deps.TunName` 返回 `(string, error)` 而非 `string`——生产接线里 TUN 名字取自 Core 控制 socket,「socket 静默」若被压成「没有 TUN」就会报出一个自信的 `CaptureOK=False`,正是本包要消灭的谎言;② 非 macOS 的 `InspectDNSContext` 返回 `Supported:false` + **nil error**,wire 层把它转成错误 → `Unknown`,不把「没问过」报成「不归 bx」。观测整轮封顶 5s(`bx status` 是出问题时最先敲的命令,宁可少答一项也不能挂住),Core 运行时状态一次观测内只取一次并缓存。**观测只在 darwin 附上**(`observerForPlatform`):路由/DNS 原语目前只有 macOS 实现,在 Linux/Windows 附观测换不来任何新事实(`tunnel_healthy` 本就来自同一个控制 socket、已在扁平字段里),却让每次 `bx status --json` 恒吐 **5 条**「该项无法观测」divergence(实测,`capture_ok` 还重复两次)——那会把 divergence 训练成用户和 agent 学会忽略的噪声,正好毁掉它唯一的价值。**字段缺席是诚实的「没问」;满屏「无法观测」则是把静态平台限制伪装成每次调用都新发生的差异。**设计 `docs/superpowers/specs/2026-08-05-observation-layer-design.md`、计划 `docs/superpowers/plans/2026-08-05-observation-layer.md`。

**Core 所有权、所有权不确定的锁存、两段式启动标记、macOS 上 ESRCH 走成 EIO 那个坑**
(以及移植 Guardian 必须先实现 `scanRunningCores`)见 `internal/guardian/CLAUDE.md`。


**观测路径真机已验(2026-08-05,本机 macOS,零改动)**:观测全程只读且 `bx status` 是独立 CLI 调用,故**无需重装、无需重启运行中的实例**——直接用新编的二进制问现有那套即可(`/var/run/bx/{core,guardian}.sock` 是 0666,连 sudo 都不要)。实测输出:`desired=on` / 信念 `protected` / 事实 `capture_ok=true(utun11)`、`dns_managed=true(127.0.0.1)`、`barrier_present=false`、`core_socket=true`、`tunnel_healthy=true`,**divergence 为空**——坐实了 `route -n get` 解析、`networksetup` 读 DNS、控制 socket 往返三条真机通路,以及「一致时必须安静」。**仍未验的是故障态**:divergence 能否抓到真实事故,要等真出一次(或人为造一次)才知道。

#### 2026-08 那一轮控制面重做:判据在这里,过程搬走了

完整施工日志(菜单栏三期、架构诊断、集成台、阶段③a、维护挂起、当晚的真机验收)
在 `docs/lessons/2026-08-control-plane.md` —— **那八节原本是这一行里的 27k 字符**,
而它们记的是「当初怎么换来的」,不是「改之前必须知道的」。下面只留后者。

- **菜单栏 LaunchAgent 的 `KeepAlive` 必须是 `{SuccessfulExit: false}`,不是裸 `true`。**
  后者不区分退出码,会让用户点的 `Quit bx` 静默失效(点确认、进程退出、随即被
  launchd 复活,界面上看不出任何异常)。**四处生成器必须字面一致**,第四处
  `InstanceGate.swift` 最易漏也最致命 —— 它每次菜单启动都拿自己那份与盘上 plist
  比对、不一致就覆写,漏写等于每次启动都把装对的改回错的,而本次会话仍用旧配置、
  当场看起来正常。`TestMenuAgentPlistTextRestartsOnlyOnAbnormalExit` 与
  `internal/install/menu_plist_generators_darwin_test.go` 钉住。
- **菜单动作免密的授权面精确到一个函数**:`mutationHandler`(服务 `/v1/up` 与
  `/v1/down`)用 `authorizeOwnerPeer`,`updateHandler`/`migrationHandler` **保持
  root-only**,由 `TestLocalAPIUpdateAndMigrateStayRootOnlyEvenWithOwnerConfigured`
  钉住。**已接受的安全后果**:无 Developer ID 就绑不了权利到 Bx.app(SMJobBless
  要校验双方签名),故该用户下任何进程都能静默开关 bx;缓解是 `guardian_mutation_requested`
  记下 uid 与发起时刻。
  (菜单侧的回落规则见 `apps/macos/BxMenu/CLAUDE.md`。)
- **架构诊断一句话:CLI 不是客户端,是第二个控制面;菜单是第三个。** 那一整轮
  开发里数据面 0 个 bug、控制面全部。目标形态是**生命周期(up/down/status/reconnect)
  归 daemon,CLI 与菜单是瘦客户端**;而**安装/卸载/强制拆除留在 CLI**,因为它们
  必须在 daemon 不存在时也能工作 —— 这条是 2026-08-04 那次 71 分钟事故换来的,
  不可退让。
- **集成台(`internal/supervisor/harness*_netns_linux_test.go`)在 netns 里跑真正的
  `supervisor.Run()`**,五条断言全部打在**内核状态**上;唯一注入的缝是
  `Options.BuildTunnel`。**它的盲区必须记住:只覆盖 Linux 控制面,macOS 主平台不在内**
  ——「真机未验」清单不因它存在而清空。建台时踩的五个坑(线程级 `unshare` 在并发
  goroutine 下按线程生效、隔离守卫只查 net 不查 mnt、隔离参照信的是可伪造的环境变量、
  `ipQuiet` 把错误折进字符串而**反极性断言**下「什么都没找到」就是绿灯、
  before/after 快照在结构上看不见顺序)全在 lessons 那份里。

### Windows

**状态**:第 1/2 步 + 第 3 步(OpenTUN/DirectDialer/Hijack/WFP/Service/setup/DNS-into-TUN)
全做完,**真机 e2e 已验**(2026-07-09,`030-SJWJ-GSR-B` Win10 19044):全量路由劫持整机
出口==VPS、reality 390ms、fake-IP、WFP 防泄漏、SSH 经 10/8 旁路存活、死手还原干净;
Service 生命周期(setup→up→status→down→uninstall)与 hysteria2 UDP 档同批验过。托盘 App
与 Inno 安装包**代码完成、GUI 真机未验**(要人在机器旁点 UAC)。**逐步的经过、每次 e2e
的逐条结果、当时踩的坑全在 `docs/lessons/windows-port.md`**;下面只留改这块之前必须
知道的判据。

**CI 那条腿只跑它证明得了的东西(2026-09-16 定;当天 master 恢复 7/7 全绿,
自 2026-07-08 以来第一次)。** `go test ./...` 在 Windows 上红了两个多月,168 条失败
散在 15 个包里,逐条看下来绝大多数是 **darwin/linux 子系统的测试跑在一台 Windows
主机上**:`/tmp` 硬编码、0600 权限断言、launchd plist 路径、拿宿主 `filepath` 语义去
校验 POSIX 路径串(`filepath.IsAbs("/Applications/Bx.app")` 在 Windows 是 false)。
那些代码在 Windows 上**编得进但跑不到**(Guardian 只在 darwin/linux 起)。同一天在真机上
跑只读命令,一次扫描抓到五条真缺陷(到处让人敲 sudo、拿 NTFS 判 0600、印 systemd 的
服务名、explain 认不出物理网卡、preset 打出字面星号)—— **没有一条会被那 168 个里的
任何一个抓到。红着的腿不是严格,是等于不存在。**
现在 `test-windows` 跑两组:带 `*_windows_test.go` 的包(Windows **独家**能证明的行为)
与带 `purity_test.go` 的纯判据包(按构造与平台无关,在哪儿都该一样)。**清单从
`git ls-files` 现取、不手抄,一个都找不到时响亮失败** —— 与 `verify.sh` 第 14 步同源,
由 `TestWindowsCILegDerivesItsPackageList` 钉住,其中含「全量 `go test ./...` 不许回来」
那一条:它在这个平台上结构性地红,而**恒红的闸门会被下一个人删掉**。
**收窄不等于豁免**:收窄之后仍然抓到了真东西 —— 新腿推上去第一轮就逼出
`control_client_test.go` 写死的 `/tmp`(见「约定」里那条「真机绿不等于没问题」)。

**`.gitattributes` 是全部读源码守卫的前提,不是格式偏好。** 它们按 `"\n"` 定位锚点
(`tailscale_bypass_test.go` 找函数结尾的 `"\n}\n"`),而 Windows 上 git 默认
`core.autocrlf=true`、GitHub 的 windows runner 也是 —— checkout 出来是 CRLF,
那一整类守卫会在**最需要它们的那条腿上**以 `t.Fatal` 恒红。今天
`internal/{appattr,platformcheck,supervisor}` 里就有 8 个读源码的测试文件,
它们没红只是因为锚点恰好没跨行。落地前实测全仓 0 个文件含 CRLF,强制 LF 不改变任何
既有内容;内嵌的真二进制(~150MB)与 `*.syso` **显式**标 `binary` —— `text=auto` 靠
内容探测,而一次探测失误就是把一个可执行文件改坏,代价不对称(rebase 到一次 sing-box
自动升级之上时逐个核过:大小逐字节一致、`binary: set`)。

- **WFP 的权重就是正确性本身,照抄上游会反。** `internal/winfw` 只装三条过滤器:
  `permitSelf(15)` + `permitTun(14)` + `blockDNS(deny 12)`,**刻意不带 `blockAll`** ——
  bx 是分流的,china/direct/bypass 合法走物理,全封会打死整机。`permitTun(14) >
  blockDNS(12)` 让**进 TUN 的 :53 通**(fake-IP 解析靠它)、只封 off-TUN;上游
  `EnableFirewall` 是 `deny 14 > permitTun 12`,那靠 `restrictToDNSServers` 例外放行
  隧道 DNS,而 bx 不依赖任何特定 DNS IP。动态会话(`FLAG_DYNAMIC`)在进程退出/崩溃时
  自动清过滤器,比路由更 fail-safe。
- **哨兵 DNS 必须是「会路由进 TUN」的公网 IPv4**(现取 `1.1.1.1`)。Windows 的系统 DNS
  常指 LAN 路由器,而那既在私网 bypass 里、又被 WFP 封了 off-TUN :53 ⇒ DNS 整个断。
  这条不变量由 `TestTunDNSSentinelRoutesIntoTun` 钉死(在 `0.0.0.0/1` 内、非私网 bypass)。
  物理网卡的 DNS 从不被碰,还原干净。
- **`IP_UNICAST_IF` 的 IPv4 值要字节序反转**(`htonl(index)`,MSDN 怪癖,v6 不用)。
  抽成纯逻辑 `unicastIfV4Value`(`unicastif.go`)单独测 —— 写错不会报错,只会让防环
  绑到错的接口上。
- **wintun.dll 的加载器只搜 exe 目录与 System32。** 今天由 `provision.EnsureWintun`
  在 `windowsPlatform.OpenTUN` 第一句按内容 hash 释放到**当前 exe 所在目录**
  (`.wintun-version` 做缓存键),内嵌为空的 arch 回落到系统已装的那份。**这与「安装时
  复制一份到 BinPath 旁边」不是同一条路** —— 后者只保证那一个目录,而服务与便携 exe
  可能从别处跑;前者问的是「我这个进程的 exe 在哪」,对两者都成立。
- **exe 的 manifest 是 `asInvoker`,绝不 `requireAdministrator`。** 托盘进程非提权常驻
  (只轮询状态、开机自启友好),仅在改系统的动作上 `ShellExecuteW` verb `runas` 拉起
  提权子进程 —— 反过来会让每次开机都弹 UAC。资源由 `winres/winres.json` 经 go-winres
  生成 `.syso` 提交进仓库根。
- **Hijack 之后 `bx run` 一旦隧道健康就直冲全量劫持,没有 OpenTUN-only 的止步点。**
  故真机 bring-up 必须走梯度:`bx debug-tun`(只建适配器、零系统改动)→
  `bx run --no-hijack`(隧道+TUN,网络零改动)→ `bx run --test-timeout 2m`(全量,带死手),
  并在 config 里先 bypass 掉 SSH/RDP 的源网段。
- **`internal/winfw` 是逐字 vendored 的第三方**(WireGuard firewall,MIT),只有
  `dnsleak.go` 是 bx 自己的薄入口。从上游同步时要保住三处适配:包名 `firewall→winfw`、
  非 `_windows` 后缀的文件补 `//go:build windows`、**arch 文件的约束必须显式加
  `windows &&`** —— `types_windows_64.go` 的 `_64` 不是合法 GOARCH,文件名后缀不生效,
  漏了会让 Linux 误编译它。
- **真机观察项(未验)**:server 写主机名(非 IP)时,隧道断线重连要重解析域名,而 WFP
  的 `permitSelf` 只放行 `bx.exe`、子进程 app-id 不同,它的 off-TUN :53 会被封。理论上
  靠哨兵 DNS(在 TUN 内、`permitTun` 放行)+ `staticA` 静态真 IP 兜住,但没实测过;若卡,
  补一条放行子进程 exe app-id 的过滤器。
- **环境注记**:公司网络对 HTTPS 做 TLS MITM(自签根 CA),于是 bx 的 GitHub 下载报
  `x509: unknown authority` —— **那是 bx 的供应链安全在正确工作**,不是故障,别去关它;
  真机测试用 `singbox_bin` 指本地二进制绕开(override 通路已因此真机坐实)。

**`internal/winfw` 是 vendored 第三方**(WireGuard firewall,MIT):除 `dnsleak.go`(bx 薄入口)外逐字复制,仅三处适配——包名 `firewall→winfw`、非 `_windows` 后缀文件补 `//go:build windows`、arch 文件约束补 `windows &&`(`types_windows_64.go` 的 `_64` 非合法 GOARCH,文件名后缀不生效,必须显式加 windows 否则 Linux 误编译)。升级从上游同步时保持这三处。

### REALITY 传输收尾

① **自举悖论已解**——sing-box 改为内嵌(自建静态最小构建,见「约定/内嵌资产」),`bx up` 真零外部依赖,download 仅作无内嵌 arch / 自定义兜底。② **端到端已验(2026-06-28)**:用 bx 自己的 `parseVlessLink`+`singboxConfig` 生成客户端配置,跟真实 sing-box REALITY 服务端(VPS,SNI 借 www.apple.com)握手——出口 IP == VPS、停服务端 → 隧道失败不回落(kill-switch 语义)。**协议层坐实**。③ **真实硬件 e2e 已验(2026-06-29)**:在 GL.iNet Mudi(GL-E5800,**aarch64 + musl OpenWrt**)真机上,内嵌静态 arm64 sing-box 直接执行(`version`/`check` 均过)+ reality 握手到真实服务端成功,经隧道出口 == VPS、而直连同服务被运营商封 → 隧道是唯一通路,无可辩驳。**这坐实了「自建静态而非官方 glibc 动态包」的决策——官方包在 musl 上直接 `not found`,我们的静态构建照跑。** ④ **整机 e2e 已验(2026-06-29,Mudi host 模式 global)**:`bx run` 开 bx0 TUN + 劫持整机路由 + reality 隧道,**整机出口 == VPS、kill-switch 停服务端即 fail-closed、退出 defer 还原干净**(bx0/规则/table 100 全清)。修复期间挖出并修掉一个多 WAN bug:`defaultRoute()` 旧逻辑取最后一条 default、无视 metric,在 Mudi(wlan4 metric20 + SIM metric40 双默认)上错选 SIM(CGNAT 抖)→ 隧道走烂路健康抖动;`parseDefaultRoute` 改按 metric 选首选后,隧道一次健康(410ms)。⑤ **split 模式已验正确(2026-06-29,Mudi)**:china 列表正常加载(`china_domain=12165 china_cidr=6115`)、fake-IP DNS 正常(`ifconfig.me→198.18.0.1`)、foreign 走隧道(icanhazip.com/ipinfo.io 出口==VPS)、china 站直连(运营商 IP)。先前疑似的「漏直连」是**自摆乌龙**:`BX_DEBUG=1` 显示 `dial direct: domain="ifconfig.me"`——**`ifconfig.me`/`ip.sb` 本就在 brook 的 `china_domain.txt` 直连列表里**,bx 照列表正确直连;我拿了 china 列表里的域名当 foreign 出口探测才误判。教训:**验出口/分流别用在 china 列表里的域名,用 **icanhazip.com / ipinfo.io**(两个都逐字核过不在列表里)。**这条教训自己错过一次,2026-08-11 才发现**:原文推荐 `api.ipify.org`,而 `ipify.org` 同样在 `china_domain.txt:6045`,于是那条写错的推荐照着进了 `internal/cli/cli.go` 三处 —— `bx doctor` 的公网 IP 探测在保护开着时走直连,报出用户**真实的 ISP 出口**,把一台工作正常的机器说成在漏,方向正好相反。现在不靠记忆:`TestPublicIPProbeDomainsAreNotChinaDirect` 拿**真实的内嵌列表 + 生产用的同一个 DomainSet** 逐个比对,读不到列表就响亮失败**。（那条 `china_cidr=0` 是 global 模式日志,global 本就跳过 china 列表,非 bug。）⑥ **bx0 MTU 怀疑已证伪(2026-06-29)**:经 bx0 下 9MB(jsdelivr)**完整无损**(http200、字节全量、尾部正常),Mudi 上 apple.com 大 TLS 响应经 bx0+reality 也 200。最初疑似的「MTU」其实是 **api.ipify(Cloudflare)目标特异**——它直连也失败(运营商对 Cloudflare 消费 IP 干扰),与 bx0 无关。gVisor 终结 TCP + 子进程按内核 path-MTU 重分段,bx0 大流量无 MTU 截断问题。**reality 整条线再无已知开放项。****踩坑备忘**:① reality 端口受制于服务端 ufw/云安全组白名单 + 路径对 443 的 DPI 干扰(实测 443 与非白名单高端口 TCP 能连但 TLS 载荷被黑洞)——服务端落已放行高端口(同 brook 9999),勿默认 443;② **OpenWrt/BusyBox `ash` 不支持 `/dev/tcp`**,在路由器上测连通必须用 `curl`/`nc`,否则全是假阴性(曾误判路由器"无 TCP 出网")。
