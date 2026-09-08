# CLAUDE.md — bx

基于 brook 的 **Linux 透明全局代理**(自研「类 ipio」,单一 Go 静态二进制)。整机 TCP/UDP 经 TUN 自动分流:中国直连、其余走加密隧道,对应用零配置。隧道是**可插拔黑盒子进程**:`brook://` 链接→内嵌 brook(默认),`vless://` 链接→sing-box 的 **VLESS-REALITY**(抗 DPI 伪装);两者其余全自有代码。

- 用户文档见 `README.md`;设计/计划见 `docs/superpowers/specs/` 与 `docs/superpowers/plans/`。
- 模块:`github.com/getbx/bx`,Go 1.26,GitHub `getbx/bx`。
- 平台:**linux/amd64 + linux/arm64**(开箱即用)+ **macOS 真机已跑通**;**Windows 真机 e2e 已验**(2026-07-09,`030-SJWJ-GSR-B` Win10 19044):全量路由劫持**整机出口==VPS**、reality(sing-box)隧道 390ms 健康、DNS-into-TUN fake-IP(`example.com→198.18.0.16`)、WFP 防泄漏装成功、SSH 经 10/8 旁路存活、死手优雅还原干净。

## 架构(数据面 vs 控制面)

**数据面(平台无关,一行不用动)**:应用 → TUN(gVisor netstack 终结 TCP/UDP)→ `tun.Engine` →
- UDP:53 → `dns` fake-IP 处理器(A 查询返回 `198.18/15` 假 IP,`fakeip.Pool` 记录 域名↔假IP)。**第一跳是 `staticA`**(域名→固定 A,先于 fake-IP 与 `rules`):既供传输服务器防环,也是用户配置 `hosts:` 的落点 —— **bx 在跑时 `/etc/hosts` 对被 bx 接管的应用是失效的**(应用走系统解析器,而系统 DNS 已被接管到 127.0.0.1),故「把某域名钉到某 IP」只能由 bx 自己提供。值限 IPv4 字面量、**加载期**校验(悄悄没生效正是本功能要消灭的困惑);**传输服务器域名不可被覆盖**(覆盖会让隧道静默连错地方),冲突时以服务器为准并在日志与 `bx status` 里明说这条被忽略;AAAA 不受影响(仍 NODATA,逼应用走 v4,不给绕过覆盖的路)。归一化(大小写 + 尾点)由 `config.NormalizeHostName` **单点**提供,supervisor 与 dns 共用 —— 两侧各写一份归一化时,`Torchfun.com.` 与传输服务器的静态 A 会静默并存,这是 review 抓到的两个 Critical。改了要 `bx down && bx up`(不热重载)。设计 `docs/superpowers/specs/2026-08-08-bx-hosts-override-design.md`
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
- `paths_<os>.go` — 运行期 socket/pid 路径,落 bx 自有子目录而非共享父目录(linux `/run/bx`、darwin `/var/run/bx`,`internal/secdir` 校验属主与权限)。

```go
type platform interface {
    OpenTUN(name, addr string, mtu uint32) (link stack.LinkEndpoint, tun tunHandle, closeTUN func(), err error)
    DirectDialer() *net.Dialer
    Hijack(tun tunHandle, serverBypass, userBypass []string) (teardown func(), err error)
}
```
**加一个平台 = 加一个 `platform_<os>.go` 实现这 3 个方法 + `paths_<os>.go`,core 不动。** TUN 生命周期(closeTUN)由 Run 用 defer 接管,Hijack 只管路由。

- **Guardian 侧的平台缝自 2026-08-29 起有清单**:`internal/guardian/lifecycle.go` 的
  `lifecyclePlatform`(RequireDaemon/NewBarrier/DiscoverGateway/NewDNSManager/
  NewNetworkObserver/PeerCredentials 六个构造器字段),daemon 组装只经它选平台,反射 `validate()` +
  三平台 CI 腿各一条行为测试钉住「清单无洞且接的是本平台那份」。**`scanRunningCores`
  刻意不在清单里**(注入钩子无参,转发丢 reason= 审计标签,缝留在编译期自由函数);
  `RemoveBlockingBarrierRoutes` 也不在(CLI 逃生口专用,独立于 daemon)。给 Linux
  移植 Guardian 时照 lifecycle.go 的字段清单供货,procscan/peercred/barrier 各加
  `_linux.go`,`requireDaemonPlatform` 最后放开——顺序不许反。**2026-08-30 全部
  供货完毕、门已开**(`daemon_linux.go`),前置是每一块都有 netns 断言背书:
  屏障四条打在 `ip route get` 的**判决**上(装屏障前先取基线,否则「装上之后
  不通」在一台本来就不通的机器上同样成立)、Manager 四条打在真 spawn 的进程上
  (Up 后 procscan 认得出、Down 报成功之前进程真的没了、系统已有 Core 时第二个
  Manager 被拒而第一个毫发无伤)、外加真 `RunDaemon` 起来并答出 `/v1/status`。
  **开门不改变 linux 产品形态**:生产 linux 仍是 systemd 直管 supervisor,
  没有任何东西会去装或拉起 Guardian —— 那句承诺由**没有调用方**保证,不由这道
  门保证。隔离机制在 `internal/netnsguard`(supervisor 与 guardian 共用一份:
  写错的后果是把 tmpfs 盖在宿主真实的 /run 上、删掉宿主 bx 的控制 socket)。
  **两处只有变异才逼得出来的台子缺陷,记住形状**:① 子进程 re-exec 不传
  `-test.timeout` 时继承 10 分钟默认值,比父进程的还长,于是子进程里的死锁
  表现为「父进程超时 + 零输出」;② `CombinedOutput` 要等管道 EOF,而管道被
  **孙进程**(被测编排 spawn 的 Core)继承 —— 子进程死了 EOF 永远不来,父进程
  挂死。改走临时文件(`*os.File` 不建管道、不起拷贝 goroutine)之后,同一个
  变异从「2.5 分钟超时无线索」变成「15 秒干净红 + 断言直指双 Core」。
  **旧供货进度(2026-08-29)**:`procscan_linux.go`(/proc 树,纯 I/O 半无 tag、fixture 三腿
  可测,root 门槛理由换成 hidepid 致盲)· `peercred_linux.go`(SO_PEERCRED)·
  **barrier**(`barrier_iproute.go` 纯计划 + `barrier_linux.go` 执行器:pref-120
  rule + table 90 + **throw 私网 carve**——linux rule 命中即终止查找,darwin 主表
  最长前缀救私网那条语义必须用 throw 亲手移植,/2 覆盖全空间;pref 120>100 保住
  bx 打标出站的结构性逃逸、<150/200 压过劫持;网关经 `supervisor.LinuxDefaultRoute`
  复用 metric 感知解析,不许手抄)· **DNS**(`DNSNotNeeded` 第四态:「本平台无
  此事」≠「该接管没接管」,manager 两道门放行它、菜单 dns_managed 如实 false)·
  **observer** 显式 nil(不装假观测)。**只剩 daemon 门未开 + netns harness 未接,
  linux 上 Guardian 仍起不来**——刻意的中间态,`lifecycle_linux_test` 钉住
  「供货≠开门」。焊死语义只对 darwin/linux 之外保留原话。终局路线见
  `docs/superpowers/specs/2026-08-29-control-plane-endgame-design.md`。

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

**包**:`leakcheck`(**纯判据**,无 I/O,`purity_test.go` 按前缀禁 net/os/exec 并明写例外)·
`leakserve`(一次性 loopback 服务 + 页面 + 本机事实采集)·`loopbackgate`(token + 逐字节比
`Host` + 写操作才要 `Origin`;`Host` 逐字节比对是唯一可靠的 DNS-rebinding 判据)。

**判据分三段,各自计数,绝不合成一个总数**(`Section`):`path`(流量去哪儿,bx 或当前隧道
负责,进 `AnomalyCount`)· `identity`(会不会被单独认出来,进 `IdentityCount`)· `surface`
(网站看得到什么,**`Verdict.Info`,没有极性、不进任何计数**)。合成一个数时它永远不为零
(普通 Chrome 就是不防指纹),于是被训练成噪声、把真正的泄漏一起淹掉。`Section` 零值是
`SectionPath`:漏填是多报,反过来是漏报,代价不对称。

**十条结论**(此前这里写的是「八条」,**漏了头尾两条**,2026-08-24 按 `Outline()` 实测更正):
`traffic_carrier` 谁在承载你的流量 · `webrtc_srflx` WebRTC vs 出口 · `ipv6_leak` IPv6 暴露 ·
`dns_path` DNS 路径 · `route_escape` **路由被动过手脚(TunnelVision CVE-2024-3661 /
TunnelCrack ServerIP)** ‖ `local_addresses` 内网地址是否被 mDNS 遮掉 · `timezone_vs_exit`
时钟 vs 出口国 · `language_vs_exit` 语言 vs 出口国 · `fingerprint_defence` 指纹防护 ‖
`browser_surface` 网站看得到什么。(`‖` 是分段边界:path 5 条、identity 4 条、surface 1 条。)
骨架(`Outline()`)与 `Judge()` 的 ID/顺序/分段**逐项对上**,由守卫钉住;「哪条需要浏览器」由
`Outline().Inputs` 是否为空推导,**不许手抄一份 ID 列表**。
**条数本身也由守卫钉住**(`TestOutlineHasTheDocumentedNumberOfConclusions`)——
加减一条结论时它会红一次,那正是回来把这个数字改对的时刻;而一份说少了的清单会让下一个人
以为某条结论不存在。**另有一条钉住「结论集合不随输入变化」**:页面按骨架先摆行、再按 ID
塞结论,而页面对认不出的 ID 是 `if (!row) return;`(静默丢弃)—— 少一条就是一行永远等不到
结论的空壳,两头都不报错。原来那条守卫只喂全零输入,**而「够是因为实现恰好是无条件的」正是
「测试输入让待守属性不可见」的形状**;变异实测:让 Judge 只在浏览器**到了**时少发一条,
旧守卫全绿、新守卫在两个非零输入上转红。

**几条判断上的取舍,改之前先读**:
- **`WhoOwnsTheRoute` 四态**(bx / 别人的隧道 / 没有隧道 / 没问出来)是一切结论的挂靠点。判据取
  `route -n get 1.1.1.1` 那一跳(**不是 `default`** —— split-default 下 `default` 仍指物理网关)。
  接口名分类是**白名单式**:认得出是隧道→有,认得出是物理→没有,**认不出→不知道**;把物理网卡
  误判成隧道 = 把裸奔的机器说成受保护,是最坏的一种错。隧道前缀表**全仓只有 describe.go 一份**。
- **规则单边**:不知道有没有隧道就**不许判 ok**,但仍可判 bad。没有隧道时 WebRTC 两半一致
  **不是好消息**(那是「你的真实 IP 和你的真实 IP 一致」)。
- **TunnelVision 判据的形状由真机决定**:项目所有者机器上有 106 条公网 `/32` 经物理网关(bx 自己的
  server bypass)。**单主机不报,更宽的公网前缀才报** —— 后者才是攻击要的(目的是截流量不是一台
  主机)。reject/blackhole(标志位 R/B)不算逃逸,bx 自己的屏障就是一组 `/2` reject。CGNAT 豁免
  必须是「**落在** CGNAT 里」不是 `Overlaps` —— 后者会把 `64.0.0.0/2` 这种攻击形状静默放行。
- **bx 自己的 UDP 分流会长得和泄漏一样**:`udp.transport` 指向另一台服务器时 srflx ≠ HTTP 出口
  而零泄漏。判 `NotChecked` 而**不是 ok** —— bx 不知道那台 UDP 服务器的出口,判 ok 是把假阳性换成
  **假阴性**。`udp.mode=direct-realtime` 仍判 bad(真的以真实 IP 直连),只是点名是配置选的。
  豁免**只对 OwnerBX 生效**,拿 bx 的配置解释别人的 VPN 是张冠李戴。
- **国旗只在能证明时给**:不带 geoip,只能靠内嵌 china CIDR 证明「在不在中国大陆」;anycast
  (1.1.1.1/8.8.8.8)让「国家」这个问题本身没有答案。判不出时返回 nil 判据而非恒 false。
- **指纹那条问「有没有在防」,不问「指纹是什么」**:唯一性没有语料库就没有分母,编一个百分比
  比不报更糟。判据是同一次会话画两遍 canvas 是否相同。

**探测名是一条跨语言契约,而它一度只由一句假话「守着」(2026-08-24 补上)**:
`leakcheck.Probe*` 常量与页面里 `probeLanded("srflx", …)` / `fetchEcho(…, "exit_v4")`
那几个**手抄字面量**必须一致。`outline.go` 头上原本写着「两边用同一组常量,免得页面
自己抄一份」—— 而 `pageData` 里根本没有这几个常量,页面确实自己抄了一份。漂移的后果
是静默的:`skeleton()` 按 Go 那份建 `cells`,`probeLanded` 按页面那份查表,对不上时
`(cells[name] || []).forEach` 什么也不做,那一格**永远停在「还在等」**,而两侧测试都绿。
现由 `leakserve.TestPageProbeNamesMatchTheGoConstants` **双向**钉住(页面用的每个名字
都是真常量 + 每个常量都在页面里被用到),三条变异各咬中一个方向。

**端点是用户可见契约**(页面联网前原样显示),换之前过三关守卫:常量钉死 / https / **不在 china
直连列表**。**这个坑踩过三次**:`ifconfig.me`、`api.ipify.org`(文档自己推荐错的)、以及本轮候选里
的 `ipapi.co` 与 `ifconfig.co`(选之前用生产那份 `route.DomainSet` 逐个比出来的)。现用
`ipv4/ipv6.icanhazip.com` + `stun.cloudflare.com` + `www.cloudflare.com/cdn-cgi/trace`(一次请求
同时给出口 IP 与国家)。
**三关 2026-08-24 逐条实测确认都在,而且都不靠记忆**:`TestEndpointsArePinned`(常量
逐个钉死)、同文件里那段 scheme 断言(v4/v6/trace 必须 https —— 明文回声在路上可被
改写,判据就整个失效)、`TestEchoEndpointsAreNotOnTheChinaDirectList`(拿**真实内嵌
列表 + 生产 `DomainSet`**,并带一条「列表里确实有东西能命中」的自检防假绿)。
`internal/cli` 那半由 `TestPublicIPProbeDomainsAreNotChinaDirect` 同法守住。
**同一次复核修掉一条更坏的注释**:`endpoints_test.go` 里写着「同一个坑**今天还活在**
`internal/cli` 的 `collectNetworkProbe` 里」—— 那个说法已经过期(现用 `icanhazip.com` /
`ipinfo.io`)。**一条声称某个 bug 仍然活着的注释,比一条普通的陈旧注释更坏**:它会派
下一个人去修一个不存在的东西,或者让他连带不再相信旁边那些还成立的话。

**检测结果不留存**,页面与 CLI 都明说。

**真机首验(2026-08-31,项目所有者的 Mac,本机那一半全绿)**:`bx leakcheck`
非 root 起服务 → loopback + token 页面加载 → **在联系任何人之前**先列出四个
第三方(与钉死的常量逐条一致)→ 刻意**不点** Run the check、让它自然硬超时,
验的正是风险最高的那条路径。结果逐条对上设计:
- **十条结论一条不少**、三段(path 5 / identity 4 / surface 1)分段正确;
- **三个计数并排、绝不合成**:`0 leak(s) in the traffic path, 0 identifying
  trait(s), 6 not checked` —— 没有出现「没有发现泄漏」那句最坏的假话
  (设计风险四:异常数为 0 完全可能是一条都没检查成);
- **`WhoOwnsTheRoute` 四态在真机上判对**:「carried by bx (utun12)」,白名单式
  接口分类没有把物理网卡误判成隧道;
- **TunnelVision 判据的形状对**:7 条单主机路由走隧道外,如实说明「这是隧道
  联系自己服务器的方式」而**不报逃逸** —— 正是「单主机不报、更宽的公网前缀
  才报」那条判断;
- IPv6「没有到 v6 互联网的路由,故无可泄漏」、DNS「全部解析器经 bx 或本机」
  均正确;超时那 6 条如实说「浏览器那半从未到达,什么都没联系,故无从下结论」。

**仍未验(三项,别读成已验)**:① **浏览器那半**——要人点一下按钮才产生数据,
点下去会向 icanhazip/cloudflare/STUN 发真实探测;② **非 root 门槛**
(`guardLeakCheckPrivileges`)当时没有 sudo 口令,没实跑;③ `--json` 输出。

**页面那半的 JS 从 2026-08-24 起有闸门了(此前一行测试都盖不到)。** 形状照抄
Swift 那半边 —— Go 测试进不去的语言,单独一个运行器 + `verify.sh` 挂闸门 + CI 跑:
`page.html` 里用 `==== BX-PURE-BEGIN/END ====` 划出一段**纯解析**(只做「字符串 →
结构」,判定仍然全在 Go),`scripts/test-page-js.sh` 把它原样抽出来交给 node 跑断言。
覆盖的是整页最承重的两处:ICE candidate → srflx/host 地址(决定 WebRTC 那条结论
看不看得见你的公网出口)与 `/cdn-cgi/trace` 的 body(决定出口 IP 与国家)——
**它们解析错了不会报错,只会让结论悄悄变成「没检查」或者一个错的 IP,而对一个
泄漏检测工具,静默的假阴性是最坏的一种失效。**
顺手补了一处判据:`bxParseCandidate` 现在**校验下标 6 确实是字面量 `typ`**;
少了它,一条形状意外的 candidate 会让下标 7 上那个词被当成类型直接采信,于是一个
不是 srflx 的地址被报成公网出口。地址仍**原样返回、不按 IP 形状过滤** —— mDNS 的
`<uuid>.local` 正是要报告的事实之一,筛掉它等于把一条结论悄悄变成「没检查」。
Go 侧三条守卫钉住 node 自己证明不了的事:区段是纯的(剥注释后按标识符禁
`document`/`setTimeout`/`fetch` 等 —— 危险的不是 DOM(node 里当场 ReferenceError,
吵),是 node 里**恰好也存在**的那些)、**区段里定义的每个函数页面都真的在调**
(一个没人调用而测试盖着的纯函数,与没有测试在输出上完全一样)、闸门真的接进了
`verify.sh` 且**连收尾横幅一起查**(本仓库实测过「脚本提前 exit 0 仍然退 0」)。
没有 node 时 `verify.sh` 显式报 SKIPPED,不安静通过。五条变异各咬中一条。

**闸门装上的第二天就抓到一个真 bug(2026-08-24)**:`fetchEcho` 里空 body 那一支
**早退时跳过了 `probeLanded`**(`.catch` 只对 throw 生效,所以另一支也接不住)——
后果是那个探针格子**永远停在「还在等」的样子**(`data-got` 缺席既不是 `yes` 也
不是 `no`),而报告其实已经发出并渲染完了。修法不是补一行调用,而是把**结论**
(值 / 错误 / 落地与否)整个移进纯区段(`bxEchoOutcome`),让三个分支不可能各写
各的极性。另加一条接线守卫 `TestPageJSNeverAssertsThatAProbeLanded`,判据**刻意
不对称**:第二个实参写字面量 `true` 一律禁(那是没看答案就宣布探针落地,正是这个
bug 的一般形式),字面量 `false` **允许**(只出现在 `.catch` 里,什么都没到达,那不是
对内容的判断);同时必须真的有一处把纯函数算出的 `.landed` 传进去 —— 少了后半句,
把它改回 `probeLanded(probe, true)` 之后 node 那边照样全绿,**被测的极性根本没接到
界面上**。反向变异确认过 catch 里的 `false` 不被误伤。

**四个探针的极性随后全部收进纯区段(同日)**,守卫也从「至少有一处用纯判据」
收紧成「**每一处都是**」—— 前者在三处内联表达式旁边照样绿,而那三处恰恰是没有
任何测试盯着的地方。新增 `bxTraceOutcome`(拦截页会以 200 返回 HTML,解析出来
两项皆空**必须**判没落地,报成落地等于把一次没拿到答案的探测说成拿到了)、
`bxSrflxLanded`(**只看 srflx 不看 host** —— host 到了不代表公网那半到了,把它算成
落地会让「WebRTC 被禁/被挡」显示成已完成而 Go 拿到空列表:**界面说查过了、判据
说没查过**)、`bxSurfaceLanded`(canvas 被指纹防护挡掉是**要报告的事实**,不是这段
没跑成,故两项任一非空即算)。
**同一轮里我自己那两条守卫各有一个 bug,都是收紧判据时才显形的**:①「区段里每个
函数都要被外面调用」逼出坏分解(纯函数互相组合恰恰是对的),改成**可达性**;
② `probeLandedArgs` 把 `function probeLanded(name, ok)` 的**定义行**当成一次调用 ——
`TrimSpace` 已去掉尾空格,`HasSuffix(…, "function ")` 永远不成立,而它此前无害只是
因为旧判据认不出形参名 `ok`。五条变异各咬中一条(含一条反向:纯函数只被区段内部
调用时可达性守卫不许假红)。

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
{attempts, failures, failure_kinds} 表(分类见下文 `internal/dialfail`);内建列表也计数(少了它无从区分「全网在失败」与「只有我这条
规则在失败」)。③ `DecisionCounter` 接口**直接扩而不是做成可选断言** —— 「实现里没有
就静默不计」的计数器与没有这个功能在输出上完全一样(都是 0),而它恰恰是用来发现
「有东西在悄悄失败」的。

**只在真有问题时才占地方**(这是它不被训练成噪声的前提):`失败 代理 0 直连 8113` +
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

**菜单**(`RulesModel.swift` 纯函数 + `main.swift` 只摆放,子菜单 + NSAlert,不做窗口):
失败的规则排最前、健康的一个字不说;失败按 **kind + 名字**对齐(同名规则可同时在
direct 与 proxy 里,语义相反);读不到就说读不到,不摆空列表;改完按应答里的
`requires_restart` 说「已生效」或「要重连」(2026-09-08 起,见下文「菜单侧三件
用户体验」),要重连时给「现在就重连」,**但绝不替他重连**。**测试当场抓到真 bug**:Swift 合成的 `Decodable`
**不用属性默认值**,而 Guardian 对空列表用 `omitempty` —— 「一条 proxy 规则都没有」这种
正常配置会让整个界面解码失败,改手写 `init(from:)`。

**产品决策(项目所有者否掉了我的提议)**:菜单栏**不加**「Direct rules: N unreachable」
常驻红字 —— 它是常态不是事件(会变墙纸)、没有附带动作、且要算出那个数就得定时主动
探测隧道外面(泄漏检测工具定期发不受保护的流量,新增出站 + 假阳性极便宜)。
**被动观测(系统已经知道的事实)优于主动探测**,这是这一整件事的形状。

**真机已验(2026-08-13,项目所有者的 Mac)**:结果计数第一次上真机就**逼出了一个
一直存在的严重 bug** —— 见下条。

## 规则体检(静态那一半,2026-08-17)

**判据只长在一条路上是这个仓库反复出现的形状,这次照做**:`internal/rulereview`
是纯判据包(无 net/os/exec,`purity_test.go` 按 AST 钉住),给定 `direct`/`proxy`
两张表 + `Global` + china `DomainSet`,产出一组分类结论;`bx doctor`(文本与
`--json` 两条路径)与 `bx status` 常驻面板**都调用同一个 `rulereview.Review`**,
判定只有一份。**但 `Input` 的组装写了两遍**:`internal/cli/rulereview.go` 的
`buildRuleReviewInput` 摊平 `Direct`/`Proxy` 两张表并接内建 china 列表;
`internal/supervisor/riskyrules.go` 里是一份内联的组装,**只摊平了 `Direct`**,
`Proxy` 与 `China` 整个没填——`bx status` 只要危险那一类,原样够用(§见下),
但这不是「共用一份」,是两份组装各自服务各自的调用方。

**四类各自计数,永远不合成一个总数**(`Report` 没有 `TotalCount`,与 leakcheck 的
path/identity/surface 三分同一条纪律):`ClassRisky`(公有云/开放子域直连,去匿名化
风险,零值)、`ClassShadowedByUserRule`(被自己同表更宽的一条盖住,模式无关)、
`ClassOverriddenByOppositeKind`(被**另一张表**里更宽的一条压住,**从来没有生效
过** —— `route.Router` 先查 proxy 再查 direct,没有「更具体优先」这回事)、
`ClassShadowedByBuiltinList`(被内建 china 列表覆盖,**依赖 mode**)。

**`ClassOverriddenByOppositeKind` 是比 spec 多出来的一类**:动笔时发现「同表冗余」
的判据罩不住「跨表压制」这种更隐蔽的形状(用户以为一条 direct 规则在工作,其实
从没生效过),补了这一类,由 `internal/supervisor/ruleprecedence_test.go` 用**真
Router**(不是判据自己的推断)背书——两者独立成文,判定分歧会被测试当场抓到。

**两个 mode 陷阱,都是真机撞出来的**:① 门读的是 **`cfg.Global`,不是
`cfg.Mode`**(后者取值只有 `host|router`,与 global/split 无关)——spec 写完当天
拿项目所有者的真实 24 条规则跑,报出 22 条「被 china 列表覆盖」,而他的机器是
global、china 列表整个不生效,那 22 条全在干活,照着删会让 22 个域名改走隧道。
判据没错,错在没读 mode;现在**只压制 `ClassShadowedByBuiltinList` 这一类**,另
三类模式无关、一条都不少。② **proxy 规则命中 china 列表不是冗余,是生效中的
例外**——`Explain` 先查 `UserProxy` 再轮到内建列表,两支措辞刻意相反,说反了就是
叫用户删掉一条正在把流量拉回隧道的规则。「没查」与「查了没有」分得开
(`BuiltinListChecked` 刻意无 `omitempty` + `BuiltinSkipReason`):global 下报
「0 条冗余」是一句自洽的假话,必须报「未检查」。

**`bx status` 只发危险那一类,severity=warn**:冗余与失效是建议、对任何成熟配置
都不为零,放进常驻面板会变墙纸、把真正要紧的这一条一起淹掉(与项目所有者否掉
「Direct rules: N unreachable」常驻红字同一条判断);它们留在 `bx doctor` 里,
那是诊断命令。severity 取 `warn` 不是 `error`——`11338a0` 之后只有 `error` 会把
总状态降级成 `Needs Attention`,一条配置建议不该让工作正常的机器显示需注意,
那正是 Tailscale 共存 advisory 当初犯的错。该告警在 `Run()` 里算好一次传进
`serveControlWithPathRecovery`(第 17 个形参),不在读状态那条路上重算——菜单每
2 秒拉一次,而配置在运行期不变(bx 不热重载)。

**第 2 条(死规则)2026-08-24 做完了,第 4 条(缺失规则)仍未做。** 死规则的硬前置
是跨重启累计的按规则计数,那一整套(持久化 → Core 侧累计 → 控制 socket 发布 →
判据 → doctor 渲染)已经落地,**但整套真机未验**,见本节末尾。

## 死规则:「这条从来没命中过」(2026-08-24,真机未验)

**它是这份体检里唯一一条要跨重启才敢说的话。** 单次运行的「0 次」什么也说明不了 ——
一台刚重连的机器上每条规则都是 0 次。判错的后果是**用户删掉一条天天在工作的规则**,
所以判据是三道门槛,**任何一道问不出来就整类不判**。

**两处「看起来等价、实则会悄悄改判据」的选择,改之前先读**:

- **门槛一是「Core 累计在跑的时长」,不是「距首次见到这条规则过了多久」。** 后者好
  实现得多,但它把 Core **没在跑**的时间也算进去 —— 那段时间任何规则都不可能被命中,
  拿它当分母**等于悄悄降低门槛**。
- **门槛二(全局累计判定数)含内建列表命中。** 它衡量「这台机器有没有真的被用过」;
  只数用户规则会让流量几乎全走内建列表的机器**永远达不到门槛**,于是这一类静默地
  从不生效 —— 那是这个仓库反复出现的失效形状。为此内建那条(`Rule == ""`)在历史
  里**存、参与门槛、但永不产出 finding**,三件事分开。
  同理它**不因跟踪表满而停止计数**:256 上限是实现细节,而表满恰恰是机器最忙的时候,
  那时停止计数会让门槛在最该达到的时候反而更难达到。

**门槛值 14 天 / 20,000 次判定。这两个数没有真机依据支撑「够不够」** —— 取自这台
机器已有的量级,而「一条规则闲置多久算死」本质上是产品判断,要真机跑一段才知道有
没有假阳性。

**几条不许动的细节,每条都有变异验证过的测试**:
- **`Overflowed` 是粘性的。** 跟踪表满过之后,这段历史里可能有规则从没被记过,而它们
  在表里的样子与「记了、从没命中」**一模一样**;后续运行没溢出就清掉标志,会让
  「没有条目」被当成「没命中」—— 这个功能最忌讳的假阳性。判据侧对应的是:**溢出 ⇒
  整类标「没查」**,而不是提高上限(提高它要先知道真机上有没有人接近过 256,今天
  没有这个数据)。
- **内建列表那条不是孤儿。** 它没有对应的配置行,按「不在当前配置里就丢」的字面规则
  会被剪掉,而门槛二要靠它 —— 剪掉它门槛就永远达不到。
- **写盘用 delta 不是总量**,否则累计值随写盘频率虚高(而写盘频率是实现细节,门槛
  20,000 虚高十倍就等于门槛降十倍);**写失败不推进基线**(推进了就等于宣布这段增量
  已落盘,而它没有);**总量回退按重置处理取当前值,绝不产出负数**(负的累计一旦落盘
  就再也纠正不回来,而它会让门槛永远达不到 —— 一个坏值把功能静默关掉)。
- **配置读不出来时退回启动快照,不喂空表** —— `pruneRuleHistory` 对空表会把所有带
  `Rule` 的条目当孤儿剪光,那是把一次瞬时故障变成**永久数据丢失**。
- **周期写 5 分钟 + 关闭时再写一次,不能只靠后者**:launchd 会 SIGKILL Core。收尾那
  次 flush **必须有人等**(否则 Run 返回后进程可能先退出,那次写静默不发生,而它带着
  最长一个完整周期的增量),但**等待有 2 秒上限** —— 停止路径不许因为别的事没做完而
  变慢。
- **比对按归一化形式,并按 Source 把 direct/proxy 分开查。** 历史里那条来自 route 的
  判定记录,与 config 原文可能只差大小写或 `*.` 前缀;对不上就是假阳性。同一条原文可
  同时在两张表里、语义相反,只按原文比会让 proxy 那条的命中把 direct 那条「救活」。
- **累计与本次运行的计数并列发布,绝不合并**(`Report.RuleHistory` 与
  `Snapshot.Rules`)。合成一个数之后,「0 次」到底指哪一个再也表达不出来。
  `DeadCount` 同理与既有四类并列,`Report` 仍然没有 `TotalCount`。
- **doctor 经控制 socket 拿历史,不读文件** —— `/var/lib/bx` 是 `drwx------`,
  非 root 读不到。**计划里「doctor 已经会读控制 socket,沿用那一条路」是错的**:
  它此前根本不读,这条路是新开的(与 `bx status` 用同一个 `FetchStatusReport`)。
- **历史跟着规则走,但要报出跨了几个版本**,让用户对这份累计打折 —— 中间可能有几版
  的计数行为并不一致。

**渲染层按 Class 字面枚举,新加一类只会静默消失** —— 这个仓库为这个形状栽过四次。
故新增 `TestEveryRuleReviewClassHasARenderingPath`:穷举 Class、每一类造一份只含它的
报告、断言 doctor 至少说了一句;它还带一条前置断言确认那张表覆盖到**最后一个** Class,
少一个的话守卫漏掉的恰好是新加的那一个。

**一个测试抓不到、肉眼看输出才抓到的 bug**:`summarizeFindings` 无条件拼
`← CoveredBy`,而死规则没有「被谁盖住」这回事 —— 输出是 `*.a ← 、*.b ← `,一句没写完
的话。既有断言查的是「这一行含规则原文」,而它确实含,于是全绿。**断言「说了什么」与
「说得像句人话」是两件事。**

**真机验收(未做,交给项目所有者)**:
```bash
sudo bx doctor --skip-probe
bx status --json | jq '.rule_history'
```
**头一次装上之后这一类必然显示「没查」(时长与判定数都不够)—— 那是正确的,不是 bug。**
要盯的只有一件事:`rule_history.uptime_seconds` 与 `decisions` 是否**跨重启后继续涨**
而不是每次重启归零 —— 那是这整件事唯一真正要验的东西。

**这一支上「静默丢弃」出现了四次,形状每次相同,值得先读再动这块代码**:
① 多条危险规则各打一条同名 JSON check(`rule_risky_direct_rule`),而那个名字的
设计意图就是稳定查找键 —— 按名字取的消费方静默丢掉除一条之外的全部安全结论;
② hint 指向不存在的 `bx direct remove`(真名 `rm`),而它是这条常驻安全告警唯一
附带的动作;③ `ClassShadowedByBuiltinList` 对 direct 与 proxy 算出两句**相反**的
话(冗余 vs **生效中的例外**)并有测试守着,而渲染层把 `Kind` 与 `Summary` 整个
丢掉 —— 那句 proxy 措辞是**被绿色测试守着的死代码**,一份只有生效中 proxy 例外的
配置会读到与「删掉不改变任何流量」共用标题的一行;④ `bx status` 那半的接线守卫
在测试里自己 `append` 一遍再断言那个局部变量,把 `control.go` 的合并改回
`guard.warnings()` 全仓照样绿。
**共同机制:测试与生产读的不是同一条路** —— 判据层的测试读判据,渲染层没人读;
守卫在测试里重造一遍生产的表达式,于是它守的是自己那一份。四条都已修,④ 的修法
是把内联闭包抽成 `newStatusReporter` 这个可测的缝(路 1「真起 HTTP server」实测
非 root 不可行:`secdir.Ensure` 要 `MkdirAll` 到 `/var/run`)。

**已知缺口(改这块前要知道)**:

> **2026-08-24 逐条复核过一遍,五条里四条已经过时** —— `renderUpSummary` 只显示
> `Warnings[0]`(早已改成遍历且有守卫,变异复核过)、`configWarnings` 那一跳无测试
> (`3d58914` 关上)、`builtinListLines` 的 Kind 静默消失(`60550bf` 修掉)、内建列表
> 用内嵌快照(2026-08-17 的 wrong-reference-object 修复早就让它读 Core 那份、三种
> 结局分得清)。
>
> **一份四分之三是假的缺口清单,比没有清单更糟** —— 它把下一个人送去找不存在的
> bug,并让他对剩下那条也打折扣。这不是记档懒,是**清单没有守卫**:代码有测试盯着,
> 而「关于代码的陈述」没有。**修完就回来划掉**,与「只清点名的那一句、不清同一句话
> 的其它副本」是同一条纪律的两面;拿不准某条还成不成立时,**先去代码里核一遍再动手**,
> 别按清单直接开修。

- `riskyRuleWarnings` 对一条**本身就永不生效**的危险规则(同名同时在 proxy 里)
  仍会发常驻告警。方向是过度告警,刻意接受:漏报的代价是真实 IP 暴露,多报只是
  提醒了一条不生效的规则,**不对称**。
  **这条的机制此前记错了,2026-08-24 探针实测更正**:原文说「因为只填了
  `Input.Direct`,没填 `Proxy`/`China`」,暗示填了就不会告警 —— **假的**。
  `ClassRisky` 是**独立判**的:把 Proxy 也填进 Input,Review 照样产出那条 risky,
  只是**额外**多一条 `overridden_by_opposite_kind`,而后者本来就会被过滤掉。
  也就是说填不填 Proxy,这条告警一个字都不会变。真正的原因是「这条规则危不危险」
  与「这条规则生不生效」是两个独立的问题。由
  `TestRiskyClassIsJudgedIndependentlyOfWhetherTheRuleEverFires` 钉住。

## Guardian 状态 watch(2026-08-17,真机未验,除 `bx status --watch` 外)

起因:菜单栏图标最长要等 **30 秒**才跟上 `bx up`/`bx down` 的真实结果——关闭档
轮询间隔是常量,而 CLI 没有任何通道通知菜单。项目所有者否掉了「把 30 秒改成
3 秒」这类小修小补,换成 `GET /v1/status?wait=<generation>` 长轮询。

**代际号由内容派生,广播只是叫醒**:单一发布点 `statusPublisher`
(`internal/guardian/statuswatch.go`)每次重算 `Status`,取它的**投影** digest
与上一次比,不同才 `generation++`;`generation` **不由任何调用点直接递增**。
广播(`poke`)不携带任何数据,只是「现在就重算,别等下一个兵底拍」——
`close(p.changed)` 换一条新 channel 是 Go 标准的广播手法。这意味着**一次广播
不可能是错的**,漏一个的代价只是慢到下一个兵底拍(3 秒),不是永久错过。

**投影 = 整个 `Status` 减一张排除名单,不是一份白名单**(`statusdigest.go`)。
方向刻意选择「默认参与」:新加一个易变字段会让 watch 疯狂触发——吵、当场看得见;
默认不参与则是菜单静默地不再对新信号反应,只有用户抱怨才会被发现。两种失效
不对称,选吵的那边(与 `Class` 零值取 `ClassRisky`、`leakcheck.Section` 零值取
`SectionPath` 同一条纪律)。排除名单共六条,每条都要写明为什么易变:
`StatusGeneration` 自己(进投影会永久自激)、`Core.LatencyMS`(每次探测都抖)、
`Core.FailingRules[].Attempts`/`.Failures`(每条连接都在涨,只清计数、保留
`Kind`/`Rule`)、`Reconcile.At`(每轮调谐都盖时间戳,不排除会跟着 30 秒–10 分钟
的调谐环触发)、`Reconcile.UnchangedRounds`(与 `At` 同一类东西的两面——循环
又跑了一轮的记账,不是「有什么变了」的信号,2026-08-17 真机 soak 补的一条,
详见下文)、`Recovery.UpdatedAt`(恢复中每次轮询都换,菜单自己的「Connecting
— N 秒」计数器另有本地驱动,不靠它)。守卫是一条反射遍历 `Status` 全部字段的
测试,逐个改动断言「不在排除名单里就必须让投影变」——它抓不到的是「新加的易变
字段」这一半,那只能真机看。

**深拷贝是承重的,不是讲究**:`FailingRules` 是切片,复制 `Status` 只复制切片头;
在「副本」里把元素计数清零改的是同一个底层数组,于是**真正发布出去的**那份
`Status` 计数也变成 0——一个污染它所要度量的东西的 digest,比没有更糟。修法是
`make` 一条新切片再 `copy` 再清零(与 `GuardianCapabilities()` 头上「每次调用都
返回新切片」同一条纪律)。

**代际号比较用 `!=` 而不是 `>`**(服务端 `statuswatch.go` 的 `wait()`、Go CLI
`internal/cli/statuswatch.go`、Swift `main.swift` 的 `runWatchLoop` 三处一致)。
`>` 在 Guardian 重启后会永久挂住:客户端手上是 57,新 Guardian 从 3 开始,
`3 > 57` 恒假。`!=` 立刻返回;唯一剩下的窗口是重启后代际号恰好落在客户端手上
那个数(小计数器,真会发生),那次请求挂到超时——但超时返回的 `Status` 是当前
真相,最坏后果是一次延迟,不是错误数据,故不加 boot id(YAGNI:兜底轮询已覆盖)。

**关机必须唤醒 parked 的 watch,不许让它们拖住 shutdown**:`Daemon.Shutdown`
先对 mutations/recoveries/observer 调 `beginShutdown()`,**再** `server.Shutdown`
(后者会等在跑的 handler 返回,一个挂 25 秒的 watch 会让 Guardian 关机慢 25 秒)。
`statusPublisher.beginShutdown()` close 一个 `shutdown` channel,`wait()` 的
`select` 里带这一支立刻返回当前 `Status`。这条纪律的直接理由是这个项目在
「关机慢」上真的栽过——2026-08-04 那次路径恢复卡在 attempt 178、持续 71 分钟、
用户全程无法关闭保护(见上文「macOS」一节)——watch 只是同一条不变量的新消费方:
**停止路径不许因为别的事没做完而变慢或失败。**

**只有两个广播点:`/v1/up` 与 `/v1/down` 的 mutation handler 落定之后**
(`internal/guardian/localapi.go` 的 `mutationHandler`,两条路由共用同一个
handler 函数)。刻意不在 Core 意外退出、路径恢复迁移那些地方也 poke——那些逻辑
住在 `Manager` 里,要把 publisher 穿进去,换来的只是把 3 秒兵底缩短到 0;
选 up/down 是因为那是**用户正站在旁边等反馈**的两处,也正是这个 bug 的原始现场。

**兜底轮询与 watch 的健康判断无关,而且刻意如此**(`StatusWatch.swift` 的
`menuWatchBackstopSeconds = 60`)。watch 有一类失效是静默的(连接半开、循环
自己死掉),此时没有任何东西会报错,菜单就停在最后一次收到的状态上而看起来
完全正常;一个被 watch 自己的健康判断影响的兜底,在那个判断错的时候恰好也是
坏的——所以它是个常量,watch 健康时也照跑。**它与 Task 6 引入的
`menuPollClosedSeconds`(30 秒)是两件不同的东西**:后者只在这一版 Guardian
**不支持** watch 时作为纯轮询间隔生效(降级路径,行为不变);前者在 watch
**健康**时也照跑,是「watch 已经哑了」的保险,不是取数据的手段——两个常量
必须保持 `backstop > closed`,否则「保险」比「正常降级」还密,`MenuCadenceTests`
钉着这个大小关系而不是任一个具体数值。

**绝不「试着拨一下看看」——能力门控是这条协议能不能安全退化的分水岭**
(`requireStatusWatchCapability`,`internal/cli/statuswatch.go`;菜单侧
`watchIsAvailable`,`StatusWatch.swift`)。旧 Guardian 会忽略它不认识的 `wait`
query 参数、对任何请求都秒回一份没有 `status_generation` 键的普通应答;客户端
无从区分「立刻返回是因为状态真的变了」与「这版根本不支持长轮询,每次都是这样
立刻返回」。**这不是纸面推演,是真机撞上的事故**:`bx status --watch` 顶着一台
这样的旧 Guardian 跑起来时,解出的代际号恒为 0、与客户端起始值 0 恰好相等,
「未变化」分支被命中且没有任何错误可供退避介入——真机实测本机 unix socket
常驻 CPU **26%~46%**、吞吐**上千次/秒**。判据是 `status.Capabilities == nil`
而不是 `len(status.Capabilities) == 0`——`Status.Capabilities` 刻意不带
`omitempty`,前者是「这版从没声明过任何能力」,后者是「声明了、这一项还没
上线」,两者都要拒绝但要分开报,只是同一件事的两种「没有」。门被绕过时还留了
第二道防线:`watchIdleDelay`/`menuWatchIdleDelaySeconds`(1 秒 floor),给
「秒回但代际号没推进」的分支兜底,把最坏情形从满速空转降级成 1Hz 轮询——理论
上能力门控生效之后这道防线再不会在生产里被触发,留着是因为防线不该只有一层。

**还有一处刻意没修的残留,它的形状值得记住**:若某版 Guardian **声明了**
`status_watch` 却在应答里**不带** `status_generation`(能力与应答自相矛盾,按构造
不该发生),菜单会:退出 watch 循环 → 落回轮询 → 下一个轮询拍
`startWatchLoopIfAvailable` 看到能力仍在、于是**又把循环拉起来** → 再撞同一个 nil
分支……以轮询节拍(开 2 秒 / 关 30 秒)无限循环。**没有针对这一种情形的冷却。**
不修的理由有两条:它违反的是这套协议自己的不变量(能力与应答体必须一致),
而且它**严格好于修复前**的行为(修复前是永久 1Hz 空转且没有任何恢复路径)。
记下来是因为:**如果将来真的观察到菜单在按轮询节拍反复进出 watch,那不是菜单的
bug,是某一端在能力声明上撒谎** —— 与上面那道 1 秒 floor 同理,一条正常时永不
触发的路径被触发了,它本身就是信号。

**`shouldSuppressFetch` 与「显式 vs 环境」的不对称**(`StatusWatch.swift`)——
上面「顺手做的清理(Task 6)」把服务器窗口的刷新改成按需拉之后,
`fetchServersOnDemand` 的 `forceShow: true`(用户点「Servers…」)与
`forceShow: false`(环境刷新在窗口已可见时按需重拉)共用同一个
`serversFetchInFlight`,而最初的拦截判据是裸的 `guard !serversFetchInFlight`——
环境刷新设的标志会把紧跟着来的显式打开也拦住:窗口没出现、没有 alert,
**点了没反应**,是那一轮修复自己引入的新回归。两种失败的代价不对称:重叠取数
的代价是一次多余的本机 socket 往返(已判定无害);拦住一次显式动作的代价是
「用户点了菜单项、什么都没发生」。判据因此改为只压环境刷新那一路——
`explicit == true` 永不被拦,只有 `explicit == false` 才可能被已有一次在飞的
取数拦住。规则窗口只有显式这一路,没有这个不对称,继续用原来裸的 guard,
不受影响。

**空闲开销不是处处为零**:每个 parked 的 waiter 自带一个 3 秒兵底,每次醒来都要
重算一遍 `observableStatus`(一次 Core round trip + 两次小的磁盘读),菜单常驻
时约 **20 次/分钟**的重算,对照它取代的 30 秒轮询(约 2 次/分钟)是一个数量级
的上升——「没人 watch 时开销精确为零」这句话只对**没有订阅者**的情形成立,
菜单一开着就不是这个情形。真机 soak 除了数 watch 触发了几次,也该顺手采样
Guardian 的 CPU。

**`bx status --watch`(`internal/cli/statuswatch.go`)是这个功能唯一的只读真机
验证手段**:它让人在不动网络、不重装菜单的前提下,亲眼看到「敲 `bx down`
的那一瞬间 watch 就吐了一份新 `Status`」;菜单那一半的验证要重装 App。

**顺手做的清理(Task 6)**:今天一次刷新曾是 3 次 socket 往返
(`/v1/status`+`/v1/rules`+`/v1/servers`),后两者各读并 YAML 解析一遍
`/etc/bx/config.yaml`,而图标只依赖 `/v1/status`(`menuRowsNow` 一个字都不碰
rules/servers)。轮询时代这只是浪费;**watch 时代刷新从「每 30 秒一次」变成
「每次状态变化都有一次」,带着它反而可能让总开销上升**——于是它从可选变成
承重。现改为按需:`openRulesWindow`/`openServersWindow` 触发
`fetchRulesOnDemand`/`fetchServersOnDemand`,拨号在后台队列、结果回主线程
落定,读不到就照既有逻辑说读不到(保留 `lastRules`/`lastServers` 原样),
**不摆一个空列表**。

**这条改动本身踩过一次回归,已修:服务器窗口的实时更新不能直接删掉。**
改按需拉之前,`applyRefresh` 里 `if let fresh = outcome.servers { … ;
serversWindow.refreshIfVisible(…) }` 是**唯一**一条「环境刷新(轮询/watch)
更新一个已经打开的服务器窗口」的路径——`ServersWindow.swift` 自己没有定时器。
`loadState` 改成恒传 `servers: nil` 之后这一支永远不会执行,后果是打开服务器
窗口不关它就冻在打开那一刻,直到用户关掉重开、或恰好触发
probeServers/checkExitIP/一次切换。**规则窗口不受影响**——`RulesWindowController`
本来就没有这条环境刷新路径,只由 `applyGroupChange` 驱动,所以没给它加同款逻辑。
修法是按**窗口可见性**触发,而不是恢复无条件取数:`applyRefresh` 现在在
`serversWindow.isVisible` 时调 `fetchServersOnDemand(forceShow: false)`——
窗口关着就不拨(这个 task 要保住的收益,没人看时不再每次刷新都解析一遍
config);窗口开着就说明有人正盯着,这时候按需拉一次正是「按需」的本意,不是
违背它,这个 task 要消掉的是「没人看的时候还每 2 秒解析两遍 config」,不是
「有人正盯着的时候也不给他更新」。`forceShow` 区分两种呈现:`true`(用户点了
「Servers…」)用 `show()` 弹出/前置窗口、读不到就用 `NSAlert` 明说;`false`
(环境刷新、窗口已可见)用 `refreshIfVisible` 就地重画,不抢焦点、不弹 alert
(否则每次刷新都 `NSApp.activate` 或弹一次 alert)。**这个改动也把
`fetchServersOnDemand` 的 in-flight 守卫从「可选」变成「必需」**:窗口开着时
它会跟着每一次刷新触发,watch 时代刷新是事件驱动、可能连着来,没有守卫上一次
没回来、下一次又拨的重叠会真的发生(`serversFetchInFlight`,与 `probing`/
`switchInFlight` 同一个模式)。`fetchRulesOnDemand` 只由菜单点击触发,理论上
够不到重叠,仍一并加了 `rulesFetchInFlight`,纯粹是为了与既有的
`probing`/`switchInFlight` 保持同一个模式,不是发现了具体竞态。

**「投影够不够安静」已真机验,而且第一次就没通过**:2026-08-17 项目所有者的
Mac 上挂 `bx status --watch` 跑了 10 分钟只读 soak(保护开着、状态不动),
稳态下本该几乎不吐,实测却 **4 次唤醒**(18:01→18:05→18:06→18:07→18:09)。
截三个连续代际的 `Status` 逐字节 diff,`protection`/`desired` 全程未变;
decisive 的一次(15→16)diff 只剩 `at` 与 `unchanged_rounds` 两个字段(`at`
早已排除、不该单独移动投影),间隔精确对上调谐环 30s→10min 的退避阶梯——
**watch 在「调谐环观测到什么都没变」这件事本身上被重新触发了**。根因是
`ReconcileReport.UnchangedRounds`(它自己就是「连续多少轮没变」的计数器,
每轮调谐都涨,同时也是退避的输入)没有跟着 `Reconcile.At` 一起进排除名单——
两者是同一类东西的两面:**循环又跑了一轮的标记,不是「有什么变了」的信号**,
`recordReconcileRound` 每轮同时盖两个字段,排一个不排另一个就是留了半个洞。
修法(`statusdigest.go`)是把 `UnchangedRounds` 与 `At` 一起清零;`Actions`/
`Held`/`Unobservable`/`CoreScan` 不动——它们是调谐环真正想报的信号(要做
什么/被什么栅栏挡住/观测瞎了哪一项),不能被这次修复连累着一起排除掉,由
`TestReconcileSignalFieldsStillMoveTheDigest` 单独钉住。回归守卫用的是**真实
观测到的场景**而非合成探针:`TestOneReconcileRoundDoesNotMoveTheDigest` 模拟
一次真实 reconcile 轮次(`At` 前进 **且** `UnchangedRounds` 加一,与
`recordReconcileRound` 同款),证明两个字段一起动也不移动投影——单独测
「只改 `UnchangedRounds`」测不出「两处排除互相依赖」这种写法上的回归。

**这次跳过的窟窿,结构上今天仍然存在**:反射守卫
`TestEveryStatusFieldParticipatesInTheDigest` 只走 `Status` **顶层**字段,逼着
「新加一个顶层字段默认参与投影」成立;但 `Core`/`Reconcile`/`Recovery`
**内部**哪些字段易变,靠的是 `TestVolatileNestedFieldsDoNotMoveTheDigest`
里手写的一张 case 列表,没有任何守卫会因为「某个嵌套字段没被这张列表提到」
而报错——`UnchangedRounds` 就是被漏看的那一个,没人为它写过一行判断,直到
真机 soak 把它显形。往后谁在 `ReconcileReport`/`CoreRuntime`/
`RecoverySnapshot` 里加字段,必须自己想清楚它是「真事件」还是「循环又跑了
一轮的记账」,因为没有任何测试会替他问这个问题。**这个窟窿 2026-08-24 已经补上,但补法与当初的草稿不同,而那个不同是要点。**
草稿写的是「不在 case 列表里就必须移动投影」——照着写完才发现**极性反了**:
`UnchangedRounds` 那个 bug 是「参与了投影而不该参与」,在那版判据下**照样
全绿**。它挡得住「有人悄悄排除一个字段」,挡不住「有人加了一个每轮都涨的
字段」,而后者才是真机 soak 抓到的那一种。
成品是 `internal/guardian/statusdigest_nested_test.go` 的**穷举分类**:枚举
`Status` 里深度 ≥2 的每一个叶子(根不写死三个名字,走的是「每一个结构体
字段」,于是将来加第四个嵌套结构自动进范围),每一个必须落进
`nestedDigestExclusions`(改它不该移动投影,**要写理由**)或
`nestedDigestSignals`(改它必须移动投影,只列名字)之一,**少一个就红**。
两张表形状不对称,理由与整个投影设计同源:错误地排除是安静的失效,错误地
当成信号是吵的失效。加字段的人因此被逼着回答那个「没有任何测试会替他问」
的问题。另有反向断言钉住两张表里不许有指向已不存在字段的陈旧条目 ——
陈旧条目看起来与生效中的一模一样而什么也不守,与本文件那份「四分之三是假的
缺口清单」同一个形状。五条变异各咬中一条不同的判据,其中决定性的一条是
**往 `ReconcileReport` 加一个新字段而不分类**,即历史 bug 的原形。

Guardian 关机耗时没有变长(升级路径上量一次)。`main.swift` 的 watch 循环编不进
Swift 测试套件,Go 侧守卫只证明判据没被手抄第二份,菜单那一半的静默性仍未真机
验(重装 App 才能点)。Linux/Windows 不在范围内(Guardian 只在 darwin 跑,
Windows 托盘另有自己的 3 秒 spawn 轮询,不受影响)。设计
`docs/superpowers/specs/2026-08-17-guardian-status-watch-design.md`、计划
`docs/superpowers/plans/2026-08-17-guardian-status-watch.md`。

## 菜单侧三件用户体验:转换通知、右键加规则、规则热生效(2026-09-08,真机未验)

起点是一次「作为用户还缺什么」的盘点,项目所有者点了三件:验掉睡醒/门户那批
未验修复(他自己跑,见各节验收命令)、**失败不再无声**、**诊断能力离开命令行**。
后两件落在菜单里,三处改动各自独立可测:

- **状态转换通知**(`TransitionNotice.swift` 纯状态机 + `main.swift` 投递)。
  kill-switch 拦下流量时用户此前看到的只是网页转圈,唯一信号是 18pt 图标的轮廓。
  项目所有者否掉的是**常驻**红字(常态会变墙纸);这条只在「受保护 → 阻断 /
  隧道断 / 需注意**且持续 ≥ 30 秒**」那一刻响一次、回到受保护再响一条「已恢复」,
  是事件不是墙纸。**不响的每一种都有测试钉住**:30 秒内的抖动(睡醒 Wi-Fi 起落)、
  用户自己开关(那是意图)、从 off 打开后直接失败(他正站在旁边,进度条在说)、
  菜单启动时机器就已是坏的(上一段故事菜单没看见)、starting/recovering 过渡态
  (既不算变好也不算变坏 —— blocked → recovering → blocked 是同一段故障)。
  `tunnel_healthy` 缺席按「没说」不按「不健康」,与 StatusReport 同一条。投递经
  `UNUserNotificationCenter`,**只在 bundle 内启用**(裸 `swift run` 一调就崩),
  同一个 identifier 让「已恢复」顶掉「阻断」。单一漏斗:watch 与轮询都经
  `refresh()` → `applyRefresh` → `observeTransition(outcome.maintenanceReport)`。
- **按应用窗口右键加规则**。规则的粒度是**目的地不是应用**(bx 没有按应用的规则),
  而窗口每行本来就带目的地;候选由纯函数 `ruleCandidates(for:)` 生成:三段以上域名
  给「精确 + `*.父域`」,两段给 `*.host`,IP 原样,归一化与 `config.NormalizeHostName`
  同向。走的是 `GuardianClient.changeRule` —— 那个方法**此前存在但从没被调过**。
  只在 Guardian 声明 `rules` 能力时挂菜单并在窗口底部提示(右键是发现不了的)。
- **规则热生效**。Guardian `applyRuleChange` 写盘成功后叫 Core `/v0/reload`(与
  `bx direct add` 同一条路,不断隧道),成功 ⇒ `requires_restart:false`;Core 没应答
  / 没接线 ⇒ true,**不回滚**(规则已落盘、如实说要重连)。菜单那句「bx applies
  routing rules when it reconnects」此前是常量,现由纯函数 `ruleChangeFollowUp` 按
  应答判(nil = 旧 Guardian 没说 = 按要重连)。规则组开关与右键两条路共用同一个收尾。

**守卫**:三个 Swift 套件(`TransitionNoticeTests`、`AppTrafficModelTests`、
`RulesModelTests`)钉判据;Go 侧 `rules_reload_test.go` 钉 Guardian 的重载与两处接线;
`internal/cli/macos_menu_transition_test.go` 四条钉 main.swift/窗口的接线(喂状态机的
是这一轮应答且结果被投递、通知只在 bundle 内、收尾用服务端答案而非字面量、右键每一跳
都接上)。八条变异各咬中一条 —— **其中一条第一次「仍绿」是变异台子跑错了包**
(guardian 的测试拿 `internal/cli` 去跑),对包重跑即红;又一次「凡变异全绿先查落没落上」。

**真机未验(全部)**:通知会不会真的弹出、授权框长什么样、右键菜单在 NSGridView
的格子上弹不弹得出来、`/v0/reload` 从 Guardian 打过去 Core 是否应答。验收:重装菜单后
① 在按应用窗口里右键一个应用 → 选一条 Always direct → 应弹「已生效」而不是
「Reconnect Now」,`bx explain <那个域名>` 立刻答 DIRECT;② `sudo bx down && sudo bx up`
不该弹通知(用户自己做的);③ 拔掉 VPS 或 `sudo route delete <服务器IP>` 让隧道
断 ≥ 30 秒 → 应弹一条「traffic blocked」,恢复后弹「protected again」并顶掉前一条。

## 嗅出的 SNI 不许压过真 IP 的规则(2026-09-05,真机诊断,修复真机已验)

真机(公司工作站,bx global):`bx direct add 180.158.6.185` 之后 `bx explain 180.158.6.185`
答 DIRECT、计数也记在那条规则下,而 tailscaled 到它的 TLS 照样经隧道从 VPS 出去 ——
家里的 derper 记到的源 IP 是 VPS,tcpdump 里 eno1 上一个发往该 IP 的 TCP 包都没有。
机制:`dialInner` 对 fake-IP 反查不中的连接从首包嗅 SNI/Host,按域名判;域名规则全不中
就 `Proxy/SourceDefault`,**IP 规则从头到尾没被问过**;explain 没有首包,按 IP 判,自然说
DIRECT。修法:嗅出的域名一条规则都没中时,由那个**真 IP** 说了算(`ExplainIP`),且直连
拨的就是这个 IP、不把 SNI 再解析一遍(那会解析到 VPS);域名规则**命中**时仍由域名说了算
(更具体);fake-IP 那条路不受影响(那个 IP 是假的,按它判什么都判不出)。顺手:HTTP Host
里的 IP 字面量不再被当成域名嗅出来。**测试要带 fake 池**:嗅探只在 `d.Fake != nil` 时发生,
`newTestDialer(nil, …)` 走不到那一支 —— 第一版测试正因此假绿。既有的 explain 漂移守卫用
`Dial`(无首包),盖不到这一类,新加的四条在 `sniff_realip_test.go`。

## `bx explain` 的本机视角:这个目标在这台机器上会怎么走(2026-09-05,真机已跑)

`bx explain <目标>` 此前只答「进了 bx 的连接会怎样」,Core 没在跑就报错。现在**先**答
本机视角(`internal/pathview`,纯判据,`purity_test.go` 钉住不做 I/O),再答 Core 那半;
Core 连不上不再是错误,只留一句「bx 没在跑,以上是没有 bx 时的样子」。动机是同一天两次
误判:另一会话看到 `8.8.8.8 → utun9` 就断定 bx 吞了 Tailscale,真相是**普通进程走 bx 的
TUN、绑了网卡的进程走 en0**;休眠成环那次,哨兵地址进了 TUN 而发往服务器的 /32 早已不在,
只有指着那个具体地址问才看得见。**与 observe 的分工**:observe 是仪表盘(无参、固定几项、
只报异常),explain 是听诊器(你指哪它听哪),两者共用同一批原语。
事实采集在 cli(`collectPathFacts`,全部只读:一次系统解析、`LookupRoute`、新导出的
`LookupBoundRoute`(darwin `-ifscope` / linux `oif`,windows 没问)与 `PhysicalDefaultRoute`、
一次 Core 运行时读取);判据在 pathview(`Judge`):目标分九类(假 IP / 回环 / 私网 / CGNAT
/ 链路本地 / 服务器旁路 / 国内 / 公网 / 解析不出),接口归属白名单式(bx 的 TUN / 别的
隧道(`leakcheck.IsTunnelInterface`)/ 物理网卡 / 认不出就说认不出),结论一句 + 证据几行。
**两条措辞是真机逼出来的**:假 IP 目标要告诉绑网卡的程序「拿到的是假 IP、从物理网卡
发出去石沉大海,域名进 `dns.fakeip_filter`/hosts 或直接写 IP」(就是 DERP 域名那次);
CGNAT 目标不带绑网卡那一句(overlay 走自己的隧道,底层那句是噪声)。`--json` 在 Core 应答
上**追加** `machine` 键,顶层字段一个不动(MCP 的 `bx_explain` 直接转发);Core 连不上时
只有 `machine` + `core_unavailable`。这台 Mac 上五类目标(公网 / 服务器旁路 / 假 IP 域名 /
CGNAT / 私网)实跑过,输出与内核一致。

## Guardian 的 JSON 响应必须显式带 Content-Length(2026-09-05,真机诊断)

菜单的「规则」窗口报「Guardian returned an invalid response」,而 curl 拿到的是 200 +
合法 JSON。差别在**框架**:`/v1/rules` 的体随 review 一节长过 2KB,`net/http` 对
`json.Encoder` 的流式写入改用 chunked(它只给 handler 返回前攒在 2KB 缓冲里的体补
Content-Length);菜单那份手写的 HTTP 读取器刻意最小、只认 Content-Length,于是
`body.count != contentLength` → invalidResponse。`/v1/status` 只有 900 字节,恰好没撞上。
修在 `writeGuardianJSON`:先整体 marshal、显式带 Content-Length、一次写出,响应的框架
不再由体的大小决定(`TestGuardianJSONResponsesAlwaysCarryContentLength` 用一个 10KB
的体钉住)。**升级之前的机器**菜单规则窗口一直坏,用 `bx direct add` / `bx doctor` 代替。

## 调谐环执行 start_core(阶段③c,2026-09-05,真机未验)

**修的是一条无人区路径**:Core 意外退出时 `handleUnexpectedExit` 装屏障后重启**一次**,
失败(`core_restart_failed`)之后没有任何东西再试,机器停在 Blocked 直到有人敲
`sudo bx up`。现在白名单三项(`restore_dns`/`clear_orphan_barrier`/`start_core`,
`TestExecutableWhitelistIsExactlyOffCleanupPlusStartCore` 钉死)。**准入是槽内现扫
`ScanRunning`,不是 socket**(`decideStartCoreAdmission` 三态:测成 0 个才起;≥1 个
→ `core_process_present`,那是卡住但活着的 Core,起第二个正是 af81632 双 Core 的入口,
本期只显形;没测成 → `core_scan_failed`,「问不出来」不是「没有」)。起 Core 复用
`startCoreLocked`(带屏障 handoff、等健康、成功释放屏障),与 `bx up` 同一条路,
`runner.Start` 既有的 fail-closed 准入一道不拆。**每段故障封顶 5 次**
(`maxReconcileStartCoreAttempts`,段 = socket 首次不应答 → 再次应答或用户 `Up`),
被扫描拦下的不计次;过了发布 `start_core_exhausted`,`bx status` 渲染成「已放弃,
等你 sudo bx up」——**不许渲染成让路**。与 ③b「清理永不放弃」刻意不同:清理幂等,
起进程不是。槽内前置条件从写死的 `desired==off` 改成按动作(`requiredDesired`)。
`stop_core`、重启卡住的 Core、装屏障、解 Uncertain 锁存四样仍不做(spec「不做」)。
旗舰测试 `TestReconcileLoopStartsCoreBackAfterAFailedCrashRestart`(白名单改回两项即红,
变异实测 `start = 2, want 3`)。
**真机验收**(所有者在场):把 data_dir 里的 sing-box 暂时改名,`sudo kill -9 <Core PID>`,
看日志 `core_unexpected_exit` → `core_restart_failed` → 循环 `start_core … execute_failed`
五次 → `start_core_exhausted`;改回名字、`sudo bx up` 归零回绿。
spec `docs/superpowers/specs/2026-09-05-stage3c-start-core-design.md`。

## 调谐环第一批执行权(阶段③b,2026-08-29,真机未验)

**授权面只有 `desired=off` 的两个清理动作**(`restore_dns`/`clear_orphan_barrier`,
白名单在 `internal/guardian/reconcile_execute.go`,内容由
`TestExecutableWhitelistIsExactlyOffCleanupPlusStartCore` 钉死(③c 起含 start_core)—— 穷举守卫只测名单**外**
的动作,名单越大它测得越少,扩名单必须有意识地改到这条测试上)。`stop_core` 观察态
的理由写死:desired=off + socket 应答最常见来源是 **`sudo bx run` 调试路径**,每
30 秒杀一次调试进程的调谐器是敌意软件;`start_core` 照 ③a 原文(双 Core 入口)。
五条执行纪律(spec `2026-08-29-stage3b-cleanup-actions-design.md`):mutation 槽
**try-acquire 不排队**(FIFO 里硬等会把用户的 up 挤过预算)、**槽内复核意图**
(决策与拿到槽之间用户可能刚好 up,`preconditions_changed` 让路)、**一轮至多一个**
(第二个动作是按执行前的陈旧观测提议的)、**失败不放弃靠退避限频**(「连败 N 次
就停」是手写补偿时代的形状 —— 停了残留永久无人管)、**动作全部复用既有原语**
(清屏障 = 逃生口同款 `RemoveBlockingBarrierRoutes`,经 Manager 字段注入 ——
包级函数会让单测真 exec route/ip;DNS = `m.restoreDNS` 带状态发布)。
`Executed` 与 `Actions` 并列进报告绝不合并,statusdigest 嵌套穷举守卫逼它选边
(signals);CLI 渲染「上轮执行 …(成功/失败/让路)」,让路措辞刻意不像故障。
**变异验证自己抓到一条空转断言**:fakeDNSManager 默认不记事件(mutationCallCounts
里那句记档的陷阱),「同轮不执行第二个」的断言没打开 record 就是假绿 —— 变异 3
落上仍全绿才显形;另两个假阴性是编译失败被数成 0 与 zsh 把 `===` 当参数展开,
**凡变异「全绿」先查落没落上**这条老纪律又付了一次学费。
**真机验收(未做)**:关保护后手工 `networksetup` 设 127.0.0.1 或造一条孤儿
pref 路由,看循环在退避窗口内清掉并在 `bx status` 显示「上轮执行」。

## 按应用看分流(2026-08-19,**整套真机未验**)

起因是一次真实排查:项目所有者报「腾讯会议开着 bx 会绕一圈」。根因很浅 ——
白名单里只有 `*.qq.com`(那是微信在用),而腾讯会议整个跑在 `*.tencent.com` /
`*.qcloud.com` 上,global 模式下 china 列表不生效,于是**信令和媒体流全部**绕到
境外 VPS 再回国。**但发现它花了半小时**,最后是靠 `strings /Applications/
TencentMeeting.app` 把域名捞出来比对才确认的 —— 而这半小时里 bx 在数据面上
**每一条连接都看见了**:源端口就在 `stack.TransportEndpointID` 里,判定就在
`route.Reason` 里,两件事都在,只是从来没被放在一起。(**2026-09-01 之后不再如此**:
`bx explain <目标>` 把判定与依据放在了一起,见下文;这个窗口答的仍是「谁在用」,
explain 答的是「这一个为什么」。)

`bx status` 答得出「按**规则**多少次」,答不出「那 1136 次是谁发起的」。而用户
脑子里的单位从来不是规则,是**应用**。

**一个用户自己开关的悬浮窗**(`NSWindow.level = .floating`),三组:
`TUNNEL` / `DIRECT` / `BLOCKED`,**同一个应用可以同时出现在多组**(Chrome 一部分
域名直连、一部分走隧道是常态,压成一行「混合」等于把最有用的那一半扔掉)。
**不进菜单栏常驻** —— 与此前否掉「Direct rules: N unreachable」常驻红字同一条
判断:常态不是事件,会变墙纸。

**包**:`internal/appattr`(**纯判据**,`purity_test.go` 按 AST 禁 net/os/exec/
syscall/x/sys)· `internal/supervisor/appsource_{darwin,other}.go`(平台原语)·
`internal/supervisor/apptraffic.go`(订阅、环形缓冲、按端口字节账)· Core
`/v0/apps` → Guardian `/v1/apps`(`authorizeOwnerPeer`,与 `/v1/rules` 同一道门)
→ 菜单 `AppTrafficModel.swift`(纯函数)+ `AppTrafficWindow.swift`。

### 可行性:`CGO_ENABLED=0` 下拿得到「端口→PID→进程名」

标准做法 libproc 要 cgo,而 bx 是静态单文件,这条不能破。**出路是
`unix.SysctlRaw("net.inet.{tcp,udp}.pcblist_n")`** —— 纯 Go,一次性 spike 实测:
TCP 205/205、UDP 106/106 条 pcb 全带 PID;对 `lsof` **一致 29 / 不一致 1 /
缺失 3**(连跑五次完全稳定);全表读+解析 **451µs~1.5ms**,45 个 PID 解名
**~330µs**。**socket 表非 root 也读得到,但 `kern.procargs2` 读 root 进程要权限**
—— 所以**归因必须住在 Core(root)**,放进菜单(uid 501)会让所有系统守护进程
的名字变成空串。这不是偏好,是实测。

### 两个字节布局陷阱(共同特征:失败得不明显)

① **块按 8 字节对齐,而 `xso_len` 不含尾部填充。** TCP 的 `XSO_TCPCB` 块 len=204,
下一块其实在 +208。不补齐则**整张 TCP 表只解出 1 条,而 UDP 表完全正常**(它没有
那个块)—— 一半好一半坏,最容易被误判成「UDP 能做、TCP 不能」的平台限制。
② **`so_last_pid` 在偏移 68、`so_e_pid` 在 72,不在块尾**(其后还有 `so_gencnt`/
`so_flags`/`so_flags1`/`so_usecount`/`so_retaincnt`/`xso_filter_flags`)。按
「结构体最后两个字段」从块尾往回取,TCP 的 pid 全读成 0。

**fixture 必须来自真机**(已脱敏提交进 `testdata/`):合成数据不会有 `XSO_TCPCB`
块,挡不住陷阱 ①。另配一条**合成数据的偏移隔离测试**(块内每个 4 字节字填不同
哨兵值)—— 真机 fixture 挡布局陷阱,合成数据挡偏移错位,**两条各守一半**,因为
那 8 条 golden 记录在 offset 44/52/56/60/80/96 上恰好也全是 0。

### 几条判断上的取舍

- **`so_e_pid` 必须查活性。** 它是「替谁干活」(把 `nsurlsessiond`/`trustd` 这类
  代劳者归回真正的应用),但**委托方死了之后它是个陈旧值** —— spike 真机撞到
  `trustd` 的 `so_e_pid` 指向一个早已退出的 7961。不查活性就会把流量记在一个
  不存在的应用上,而**那种错误在界面上完全看不出来**。
- **归因的键必须带协议维度**(`appattr.PortKey{Port, UDP}`)。TCP 与 UDP 的端口
  空间**相互独立**,同一个数字可能同时被两边占用;只用 `uint16` 会让 UDP 那轮
  静默覆盖 TCP 的归因 —— 流量记到错的应用名下,**而错的归因产生的信号就是没有
  信号**(界面只显示一个名字,不显示冲突)。
- **端口复用清账只对 TCP 生效。** TCP 一个源端口同时只有一条活连接,复用即旧连接
  已终结;**UDP 一个 socket 服务多个对端**(gVisor 按 5 元组建流,一个应用打 STUN
  + TURN + 多个 peer 就是 N 条流 ⇒ N 次 `Record`),那些流属于同一个 socket、同一个
  应用,**累加才是对的**。无条件清账会让**腾讯会议的媒体流字节系统性偏低而连接数
  完全正常,且无任何报错** —— 恰好命中这个功能最初的用例。
- **字节数是近似值,界面上必须说出来。** 按源端口记,端口复用时旧账可能算到新
  连接头上。窗口底部固定一行 `Byte counts are approximate (ports get reused).`,
  由守卫钉住。
- **字节账是 map + 一把全局 mutex,不是无锁定长表**(`apptraffic.go` 的
  `addBytes`,挂在 `copyOneWay` 的 `onWrite` 上)。spec 初稿写的两张
  `[65536]atomic.Int64` 在归因键补上协议维度之后就不成立了 —— 定长表要开**四张**
  (TCP/UDP × 上/下),订阅期间常驻 2MB 而真实占用通常只有几百个槽;map 是权衡
  后的选择,不是疏忽。**代价如实记在这里**:窗口开着时整机每一次转发写都串行
  经过同一把锁,临界区很短(一次 map 哈希 + 一次加法,无 I/O、无系统调用、键已
  存在时无分配),但它是**全局**锁,争用的数字只有真机能给。**未订阅时字节记账
  连锁都不碰**(第一句 `t.active.Load()` 就返回)——但**这条只对字节记账成立**:
  `Record`/`ConnClosed` 为了维护活连接表现在无条件抢锁,见下面「订阅之前建立的
  连接」一条。
- **`unknown` 在它所属的组里单独成行**,不摊进已知应用、也不丢弃。
  **「unknown 占比高」是可用的故障信号**(2026-08-20 起,见下一条 —— 在那之前
  不是,而当时那句忠告本身把因果说反了)。
- **报告是「最近 60 秒」,不是「自打开窗口以来的累计」(2026-08-20 改)。**
  见下面「unknown 是最大的一行」一节:因果曾经被写反,真机证据把它翻了过来。
- **陈旧提示只陈述观测到的事实,原因只给可能性**(`Protection may be off.`)——
  这条路上 bx 分不清「保护关了 / Guardian 忙 / Core 刚重启」,**断言其中一个就是
  编答案**。与「国旗只在能证明时给」同一条。

### 采集只在有人看的时候发生

窗口开着才订阅,**30 秒 TTL**、靠拉取续期(菜单被强杀时没人退订,而「没人看时
不问内核、不记字节、不攒历史」是这个设计的隐私前提,不能靠对方守规矩)。
**全内存、不落盘、不进日志。**未订阅时**不问内核、不记字节、不攒历史**,由一条
测试逐条钉住(appSource 一次没被调用 + `records`/`bytesUp`/`bytesDn` 保持 nil)
——**不用基准测试,基准不会让 CI 转红**。
**「未订阅时热路径的全部代价是一次 atomic 读」/「没人看的时候开销精确为零」
这两句话在 2026-08-20 之后不再成立,别再照抄**:未订阅时仍要维护一张活连接表,
代价是**每条连接两次全局锁获取 + 四次 map 操作**(建连时 `Record` 读改写一次、
关闭时 `ConnClosed` 读删一次),是每条连接一次、**不是每个包一次**;那把锁与
字节记账是**同一把全局锁**。
隐私上仍然成立 —— 表里只有端口与判定,**没有应用名**(应用名是 `Snapshot` 时
才去问内核的)。

### 订阅之前建立的连接必须看得见(2026-08-20,真机 bug)

`recordApp` **只在建连的那一刻**被调用(`dialer.DialWithInitial`),于是**订阅
之前就已经建好的连接从来没有被 `Record` 过,永远不会出现在窗口里** —— 窗口只
看得见「打开它之后新拨的连接」。真机现象:项目所有者打开窗口只看到 **2 个应用**,
而 `bx status` 同时报 **66 条**活跃连接。**后果最重的是长连接**:会议媒体流、
WebSocket、SSH、Colima 隧道,建连一次跑几小时,**全在盲区**;而这个功能最初的
用例正是「腾讯会议为什么绕一圈」,开会开到一半才打开窗口,它会告诉你腾讯会议
**不在**走隧道,与事实相反。**十二轮审查没抓到它,因为所有测试都是「先订阅、
再造连接」** —— 测试输入让缺陷不可见(与这一支上「守卫钉住的是缺陷旁边的东西」
出现六次同一个形状)。

修法是**活连接表 + 订阅时播种**:`AppTraffic.live` 是 `PortKey →
(path, source, rule, refs)`,`Record` **无条件**写入(未订阅时也写),
`tun.ByteAttributor` 新增的 `ConnClosed(srcPort, udp)` 在引擎 `handleConn` 的
defer 里删除,`Subscribe()` 把当时全部活连接作为记录播进新建的环形缓冲。

四条不许动的细节,每条都有测试钉住:
- **种子只在一次订阅开始时播,续期不播。** 菜单每 5 秒调一次 `Subscribe`,
  每次都播会让同一条连接每 5 秒多算一次 —— 连接数随时间线性膨胀而无一处报错。
- **`ConnClosed` 必须 defer 在拨号之前。** 判定(`recordApp`)发生在 `Dial`
  内部,被 kill-switch Block 的连接**已经进了表**而拨号返回错误;defer 在拨号
  成功之后,这类条目永远没人删 —— 而隧道挂掉时被 Block 的连接恰恰最多。
- **`refs` 记数,不是裸 delete。** UDP 一个源端口上有多条并存的流(gVisor 按
  5 元组建流,一个会议 socket 打 STUN + TURN + 多个 peer),裸 delete 会在第一
  条流结束时把整条 socket 从表里抹掉,种子于是看不见一个正在灌媒体流的会议。
  它也顺手兜住「旧连接的 `ConnClosed` 晚于新连接的 `Record` 到达」这个真实竞态。
- **活连接表的唯一边界就是 `ConnClosed`。** 少了它,泄漏出来的陈旧条目**不会
  涨到 OOM** —— 键是 `PortKey{uint16, bool}`,硬上限 131072 条、约 10–15MB。
  **真正的后果发作得更早也更糟**:陈旧条目攒到几千条之后,一次 `Subscribe` 的
  种子就能把 4096 格的环形缓冲填满并绕圈,**新记录被自己的陈旧种子挤掉** ——
  报告从「正确但残缺」退化成「错的」,而仍然没有任何一处会报错。由 `liveSize()`
  这个白盒窗口 + 「开关 N 轮后表大小回落到 0」钉住,变异验证过。
  (**「单调增长到 OOM」是第一版写下的过度声明,已改。** 这个仓库为「文档说的
  比实际大」纠过三轮,这条差点成为第四轮。)
- **~~已知缺口:种子把一个 socket 上并存的 N 条流压成一条~~(2026-08-24 已补)。**
  此前 `live` 按 `PortKey` 记、同键最后写入者胜,种子每键只发一条。于是一个会议
  socket 同时打 STUN(可能直连)+ TURN(可能走隧道)时:**订阅前**建立的只出现
  在**一个**组里、连接数恒为 1;**订阅后**建立的正确地出现在**两个**组里 ——
  **同一个事实,按窗口打开时机给出不同答案**,而这正是「腾讯会议为什么绕一圈」
  要回答的问题。
  **补法**:活连接表改成**一条流一个条目**(键是不复用的单调 flowID),另有一张
  `map[PortKey]int` 索引供「这个端口还开着吗」那三处 O(1) 查询。当初判定「真修
  不便宜」的理由是 `ConnClosed(port, udp)` 无从知道该减哪一档 —— 而把释放挂到
  拨号返回的 conn 自己身上之后,它拿得到自己那条流的 ID,**那个障碍是被上一条
  改动顺手搬开的**。同一块补齐的还有裁剪那半:活性豁免从「按端口封顶一条」改成
  「按流封顶一条」,否则并存的流在**过期之后**仍会被压成一条(同一个缺陷的另一
  半,只是发作得晚一点)。
  那两条「明确钉住已知错误行为」的测试因此被**翻过来**,其中「目的地」那条还拆
  成了两半 —— 并存(两个目的地都报)与端口复用(只报接手的那一个)在此之前塌成
  同一句话,现在各有各的正确答案;少了「复用」那一半,一个「永远报全部历史目的
  地」的实现照样全绿,而那是把一张「此刻在连什么」的表悄悄变成一份历史记录,
  隐私性质完全不同。五条变异各咬中一组不同的测试。
- **已做(2026-08-24):配平按构造成立。** `ConnClosed` 从 `tun.ByteAttributor`
  挪到 `dialer.AppRecorder` —— **谁记账谁释放**。`DialWithInitial` 变成薄壳,
  判定全在 `dialInner` 里,壳只做一件事:拿到 conn 就包一层(`appTrackedConn`,
  `Close` 时经 `sync.Once` 释放),返回错误就地释放。**单一漏斗是必需的**:
  `dialInner` 有十几个 return,逐个包会漏,而漏掉的那一个正是「记了账没人释放」。
  引擎那条 defer 一并删掉 —— 留着就是双重释放,refs 提前归零会把一条**还开着**
  的连接从表里抹掉。**接口直接扩而不是可选类型断言**(与 `stats.DecisionCounter`
  同一条:「实现里没有就静默不计」的释放者与没有这个功能在输出上完全一样)。
  **搬家换来的代价要记住**:释放现在骑在 `Close` 上,于是「relay 真的会关掉
  upstream」从一个显然的实现细节变成了活连接表正确性的**前提** —— 由
  `TestEngineClosesTheUpstreamConnSoTheDialerCanRelease` 单独钉住(变异验证:
  删掉 `upstream.Close()` 即转红)。包装层**嵌入** `net.Conn` 而不逐个转发方法:
  吞掉一个 `SetReadDeadline` 不会报错,只是连接再也不会因空闲而结束,goroutine
  与 fd 一起泄漏而界面上什么都看不出;热路径 `copyOneWay` 是手写 Read/Write
  循环、没有 `io.Copy`,所以包一层不丢 `ReaderFrom` 快路径 —— 这一点动手前核过。
  五条变异各咬中一条测试:错误路径不释放 / 重复 Close 释放两次 / Close 不释放 /
  包装层吞掉 deadline / relay 不关 upstream。**不要**改成「把 `len(live)` 发布进 `/v0/apps`
  或 `bx status --json`」:那恰好在错的地方可见(`/v0/apps` 只在**有人订阅时**
  可读,而泄漏正是在**没人看的时候**累积),且一个没有阈值的常驻数字会变墙纸
  (与项目所有者否掉「Direct rules: N unreachable」同一条判断)。

窗口开着时另有一个 **5 秒**心跳,因为菜单的兜底轮询是 60 秒、**比 TTL 还长**,
光靠环境刷新会让窗口反复跳回「Not collecting」并清零。心跳**必须活不过它的窗口**:
首拉失败时窗口从未创建 ⇒ `windowWillClose` 永不触发 ⇒ 心跳永不停止,每 5 秒一次
失败拨号 + 一条 Guardian 日志,**永久,直到菜单进程被杀** —— 而它恰好发生在最常见
的探索场景(保护关着时点一下那个菜单项)。

### 观感那一轮(2026-08-20):图标、速率、对齐的列

装上真机之后用户的反馈是「感觉 iStat 做得更好」。**两条没有跟着做的判断**,
别再重新提出来:① **不动外观跟随** —— 那台机器是浅色系统(`AppleInterfaceStyle`
未设置),窗口用的是语义色,**本来就在正确跟随**;iStat 是深色因为它无视系统外观
自画皮肤,那对原生窗口通常是错的。② **不抄圆环/仪表盘** —— 圆环编码的是「占已知
上限的百分比」,而「这个应用走隧道传了多少字节」没有上限,套圆环是假精确。

- **图标**(收益最大):`AppRow.ExecPath` 是一次**刻意的信息面扩大** —— 此前离开
  Core 的只有显示名,现在是**完整可执行路径**(能暴露安装位置、用户名、装了什么)。
  判据:与显示名走同一条路、同一道 owner 门,同一台机器同一个信任边界,项目所有者
  已明确同意;**写下来是为了让它不是「悄悄加的」**,要再扩大发布面之前先回来读这段。
  显示名本来就是从这条路径推出来的(`DisplayName`),从前读完就丢。
  它是**代表值不是全集**(Chrome 的 helper 各有各的路径、全折成一个名字),
  unknown 行恒空。窗口侧取的是 **`.app` 包**而不是包里那个可执行文件
  (`appIconPath`,纯函数、已测)—— 对后者取图标一整列长一个样,等于没有图标;
  **路径为空不画占位**:一格空白的占位图不是「没有图标」,是「这个应用的图标长
  这样」,那是另一句话。
- **速率**(**本段 2026-08-24 按代码重写 —— 原文整段描述的是已经被换掉的做法**):
  服务端按**端口**做差(`appattr.DiffPortRates`,纯函数),采样发生在订阅期间的
  resolver 那一拍里(`sampleRatesLocked`,带 `rateSampleInterval` 下限),结果经
  `bytesUpRate`/`bytesDownRate` 下发。
  **原文写的是「客户端拿相邻两次快照做差」、键是「(组, 应用名)」、判据在纯模型
  `AppTrafficRateTracker` 里 —— 三条都不再成立**,那个类型已删。删它的理由记在
  `AppTrafficModel.swift` 的字段注释里,值得记住:**60 秒滚动窗口下,一行的累计
  字节会因为记录滑出窗口而下降**,客户端把「变小」误读成「计数器复位」于是显示
  破折号,而什么都没出错。服务端做得了这个区分(它拥有那些账),客户端做不了。
  **仍然成立的**:第一次采样只立基线、不报速率(那一格是破折号,不是 0);
  `elapsed <= 0` 返回 nil。`nil` 与「指向 0 的值」是两件事 —— 前者是「不知道」,
  后者是「量出来就是 0」,压成同一个 0 会把「不知道」显示成「闲着」,与
  `observe.Tristate`、`leakcheck.NotChecked` 同一条。
  **变了而值得单记的一条**:上一帧没有这个键、或累计值往回走时,现在**给**速率
  (`delta = 当前值`)而不是不给。在客户端时代那两种情形分不清「端口换了主人」
  与「窗口滑掉了旧记录」,只能弃权;服务端按端口记账,账被删掉再重建就是端口
  换了主人,直接用当前值才是对的。
  **累计值一个都没删** —— 速率答「现在多快」,累计答「一共多少」。
- **列**:`Row.entry` 从一句散文换成 `Entry` 的各个格子,`NSGridView` 八列
  (图标 / App / Conns / Up/s / Down/s / Up / Down / Rule),列标题**只出现一次**。
  **哪几列右对齐由纯模型的 `appTrafficNumericColumns` 说了算**,窗口只遍历它。
- **~~那句缺口小字~~(`appTrafficPreexistingNote`,2026-08-24 已删)**:原文是
  「Connections already open when this window opened may appear in only one
  section.」。**它的整个前提被修掉了** —— 并存的流不再被压成一条,订阅前建立的
  连接会正确地出现在各自的组里,于是这句话变成假话。**一句假的准确性声明比没有
  更糟**:让用户对一份其实更可信的数据打折扣,还把下一个人送去找不存在的 bug
  (与本文件那份「四分之三是假的缺口清单」同一个教训)。
- **同一次改动让另一个一直存在的近似露了出来,「近似值」那句因此改写**:并存的
  多条流共享一份字节账,而 `appattr.Aggregate` 把它整个记给该端口**最近**的那条
  记录(`counted[pk]`)—— 拆成两行之后字节全在其中一行、另一行是 **0**,而合成
  一行时根本看不见。一个显示 0 B 却明明有活连接的行,不说明白会被读成「这条流是
  闲的」。现文案两个理由都说:「Byte counts are approximate: ports get reused, and
  an app listed in two sections may show all its bytes on one side.」措辞仍**只
  陈述观测到的现象、不解释实现**(与「Protection may be off.」同一条)。

**那句小字为什么不是墙纸(有人反对过,已证伪)**:项目所有者否掉「Direct rules:
N unreachable」的**承重理由是它要定期主动探测隧道外面**(泄漏检测工具新增不受保护
的出站),常驻只是附带;而这个窗口上已经有一句同样恒真、同样不可行动的小字**并且
已被接受**(近似值那条),两句同属「对眼前这份数据的准确性声明」,不是系统告警。
**~~待办:给 `AppRow` 加「这一行含订阅前建好的连接」的标记~~ —— 2026-08-24 作废**:
它要标记的那件事已经不存在了。**留下的判断仍然有用**:底部小字说的是整份数据的
性质,行内注记说的是某几行的性质,两者不该混住;将来若真要标记某几行(例如
「这一行的字节全算在另一个组里」),按行内注记做,别往底部再加一句。

**两条布局上承重的细节**:① **凡是会截断的格子必须同时给出看全的办法** —— 规则
原文那一列 `byTruncatingTail`,而这个窗口不横向滚动,少了出路它就**永久不可见**
(上一版那句散文是整行读得到的,那会是净退化);出路是 `toolTip` + 窗口
`.resizable`,两条各有一条守卫。② **挤压顺序必须确定**:规则(250)< 应用名(500)
< 数字列(750);第一版应用名与数字列**同为默认 750**,谁让位由 Auto Layout 任选。

**四条新守卫的判据都是语义位置**:图标真的进了一行的格子、数字列真的被摆成
trailing、速率真的传进 `rows(...)`、缺口提示真的出现在某一次 `addArrangedSubview`
的实参里。其中「没有退回散文」钉的是**一行的格子数 == 列标题数** —— 把七个格子
拼成一句话塞进一个 label,字段名照样全在源码里,**只有这个计数会掉下来**。

### 守卫又被攻破两次(2026-08-20 审查实跑),形状都不新

**① 判据落在实参的「标签」上,而标签恰好就叫那个名字。** 速率接线守卫写的是
`strings.Contains(arg, "rates")`,于是 `rows(rates: [:])` **全绿** —— 标签本身就
满足了判据;`_ = rateTracker.ingest(…)`(丢弃返回值)同样全绿。两者产生的失效
恰好是这个功能唯一一条「只有真机能发现」的:**速率列永远是破折号,而窗口看起来
完全正常**(数据在更新、行在变、没有任何报错)。触发它的**不是对抗性写法,是一次
看起来完全无辜的重构**。判据现在是两条位置断言:每一次 `ingest` 的结果都被赋给
同一个非 `_` 的存储属性,且 `rows(...)` 的实参就是那个属性**光秃秃的一次取值**。
**教训可推广:钉「实参里出现过某个词」时,先看那个词是不是恰好也是形参标签。**

**② 抹白只做了一半 —— 找结构那半做了,内容判定落回原串。**
`blankSwiftStringLiterals` 的头注释自称是「本文件全部结构化扫描器的前置」,而实际
只有数括号那半在抹白副本上跑;随后的 `strings.Index(body, "execPath")`、
`body[gate:use]`、`window[loop:end]` 全部回到原串,于是一行
`let _ = "xPlacement = .trailing"` 架空右对齐守卫、`let _ = "execPath … return nil"`
架空图标的空路径门(后者会让路径为空的行去取一个不存在路径的图标)。**两个绕法
都实跑验证过:修复前绿、修复后红。** 现在整条路只经 `menuAppTrafficWindowCode`
一个入口。**同一个洞在既有的「近似值」守卫上也有,一并补了** —— 这个仓库的老形状
是「同一个根因修一处漏两处」。

**③ 发布面扩大补了一条白名单守卫**(`internal/appattr/publication_test.go`):
全仓扫 `ExecPath`/`exec_path`/`execPath`,不在白名单里就红。理由不是今天的风险
(今天发布面确实只有菜单一条),而是**「发布面扩大靠 review」在这个仓库是已知的
弱环**;危险形状不是有人故意转发,是将来某个人把 `appattr.Report` 整个 `%+v` 进
一行诊断日志。**加进白名单是可以的,悄悄加不行。**

### 这一支上最值得记的方法论(比代码值钱)

**「守卫钉住的是缺陷旁边的东西」这个形状,在 11 个 task 里出现了六次**,每次都是
测试用了一个**让待守属性不可见**的输入:golden 测试用空 `owners` map(所有记录塌成
一行 unknown,顺序不可见)· 环形缓冲测试只比总数 · `Aggregate` 测试守不住**生产侧**
填协议标记那半 · 接线测试只测得到 403 那条路径(成功路径绕开了组装根)· 「近似值
小字」守卫退化成「文件里出现过这个标识符」· Swift fixture 喂的是**生产不会发生**的
形状(键缺席 vs `null`,两者恰好走同一分支)。

**根因往往比单条守卫更深。** 最后一轮挖到的:`stripSwiftComments` 刻意保留字符串
内容(对的),而之后**每个**结构化扫描器都在数括号 —— 于是数进了字符串字面量里的
括号。一个根因,同时造成**假绿**(两行能编译的代码就能让小字从窗口消失而守卫全绿)
与**假红**(菜单标签里一个 `}` 让四条测试转红,其中一条还 blame 错了地方)。
**假红更致命** —— 恒红的守卫会被下一个人删掉,那等于没有守卫。修法是
`blankSwiftStringLiterals`(抹内容、**保住每个字节偏移**),并套到全包共用的
`swiftFunctionBody`/`swiftFunctionDefs`;证明「没连累既有守卫」靠的**不是**「全量
verify 全绿」,而是逐字节比对 62 个既有调用点 + 对两条防历史事故的守卫做变异确认
它们**仍然**转红。

**变异测试自身会假阴性,今天发生过四次**(正则不匹配真实写法、参数名猜错、位置式
结构体字面量不是 `Field: value`、结果被自己的 grep 滤掉)。**凡变异结果是「全绿」或
「构建失败」,一律先查变异本身有没有落上。**

**`git checkout <path>` 与 `git stash` 是同一形状的两面**:后者是全树操作、路径保护
挡不住(2026-08-17 卷走同伴五个未提交文件);前者是路径操作、但对**未提交**的工作
同样毫不留情 —— 本轮有实施者用它还原变异,把自己 110 行未提交的接线整个丢掉。
共同点是:**未提交的工作对任何 git 还原操作都没有保护。** 还原变异用 scratchpad
备份 + `cp`。

### 真机未验(全部)

悬浮窗、心跳、陈旧横幅、三组渲染、能力门控 —— **没有人在屏幕前点过**。
**2026-08-20 那一轮(图标/速率/八列表格)同样一次没上过屏幕**,而且它引入的是
本功能里唯一一段**布局**代码(`NSGridView` 八列 + 合并的分组标题行 + 截断的规则
列):列宽会不会把规则挤没、720pt 够不够、图标取不到时那一格长什么样,都只有真机
看得见。速率还有一条只有真机能验:**第一拍必然是破折号**,第二拍起才有数 ——
若真机上它**一直**是破折号,说明 `refreshIfVisible` 那条路没走到(而窗口看起来
完全正常)。窗口那半
AppKit 代码本仓库一行测试都盖不到(只有 `swift build` 证明能编译、一条文本守卫
证明那句小字被摆进了视图树)。真机验收清单:① 三组都在、腾讯会议出现在预期的组里;
② 关掉窗口后 CPU 与拨号回落,30 秒后再开数据从零开始;③ 制造 kill-switch 阻断
确认 BLOCKED 组出现内容;④ `unknown` 占比不高 —— **2026-08-20 起这条在任何时刻
都成立**,不再只限「开窗后 10 秒内」(报告改成 60 秒滚动窗口 + 归因提前到连接还
开着时做;此前那条「之后单调增长是预期行为」的限定,连同它背后写反的因果,已改);
⑤ 每 5 秒重建视图树会不会把滚动位置拉回顶部(已知,未修);
⑥ 窗口开着时采样 Guardian 的 CPU。

设计 `docs/superpowers/specs/2026-08-19-app-traffic-attribution-design.md`、
计划 `docs/superpowers/plans/2026-08-19-app-traffic-attribution.md`。

### unknown 是最大的一行 —— 因果曾被写反(2026-08-20,真机证据)

功能装上生产 Mac 之后,窗口里 **`Unknown app` 是最大的一行:16 条连接,而且是
唯一有真实速率的一行**(959 B/s 上 / 1.8 KB/s 下)。

**速率是决定性证据**:如果 unknown 主要是「累积下来的陈旧记录」,累计值就不会
涨、速率应该是 0。速率不为零 ⇒ **新的 unknown 记录在源源不断进来。**

**主因是「归因发生在读取时」**:`OwnersByPort()` 全仓**只在 `Snapshot()` 里被调
一次**(菜单 5 秒一拍),于是**任何活不过一次刷新的连接,在被归因之前 socket 就
没了** —— 内核里查不到那个端口,结构性地落进 unknown。
**spec 与本文件此前都把因果写反了**(称短连接「只是同一现象的一个瞬时特例,不是
它的主因」),已改;那句话的三处复读也一并清了 —— **这个仓库为「只清点名的那一句、
不清同一句话的其它副本」栽过两次。**

修法两件,缺一不可:
- **滚动窗口 60 秒**(`appattr.ReportWindow`,纯判据 `InReportWindow`,`now` 由
  调用方传)。窗口回答的是「**此刻**谁在连谁」,这是产品决定不是随手取的数。
  **字节账必须跟着裁** —— 只裁记录不裁字节账,那张 map 仍然只涨不落,而 UDP
  刻意不在端口复用时清账,于是一笔早该消失的账会整个算给下一个拿到这个端口的
  应用。**「滚动窗口」滚一半等于没滚。**
- **归因提前**:订阅期间一条 **250ms** 的后台 resolver 把还没解析的记录解出来、
  **存进 `ConnRecord.Owner`**;`Snapshot` 改为优先用记录里存着的归因,只对仍未
  解析的做最后一次现查(并把结果也写回记录,让下一次受益)。连接关掉之后归因
  **仍然在**,这正是这次要修的那件事。

**三条不许动的细节**:
- **resolver 绝不许持着 `t.mu` 调 `OwnersByPort`。** 那是两次 sysctl
  (451µs~1.5ms),而 `t.mu` 是整机每条连接、每次转发写都要过的那把**全局**锁 ——
  持锁去问就是每 250 毫秒把整机的记账阻塞一次。形状固定:**持锁挑出待解析的键
  → 放锁 → 问内核 → 重新持锁按序号写回**。白盒守卫让注入的 `appSource` 在被调用
  时自己去抢 `t.mu`(与 `TestAppTrafficByteAccountingTakesNoLockWhileUnsubscribed`
  同一手法),持锁就死锁、超时即红。
- **写回按序号(`bufferedRecord.seq`,单调递增、永不复用),不按下标。** 放锁
  期间缓冲可能已被裁剪重排,按下标写回会把归因写到别人头上;按序号则「这条记录
  已经不在了」自动退化成「找不到,跳过」。序号住在 supervisor 这一层,不进
  `appattr.ConnRecord` —— 那是纯判据的输入,不该带缓冲的记账。
- **还开着的连接不按时间裁。** 只按时间裁会让一条开了两小时还在灌流的会议媒体流
  在开窗 60 秒后消失 —— 那正是「订阅之前建立的连接看不见」那个真机 bug 换一种
  方式复发。**裁剪的早退判据只看时间**(记录按时间序,最旧的还在窗口内 ⇒ 全在):
  写成「最旧的那条留得住吗」,一条因为还开着而留住的长连接会让它后面所有该裁的
  记录一起逃过裁剪。
- **未订阅时 resolver 一次都不跑,TTL 过期就停。** 「没人看时不问内核、不记字节、
  不攒历史」是这个设计的隐私前提,一个自己滴答的 goroutine 会把它悄悄破掉而界面
  上完全看不出来。测试里默认**关掉**后台 resolver(`newAppTrafficNoResolver`)——
  一个每 250ms 自己去问内核的 goroutine 会让「问了几次」这类断言变成掷骰子,而
  **一个偶发红的闸门比没有闸门更糟**。

**已知代价**:窗口开着时 Core 每秒最多 4 次 `OwnersByPort`(约 0.6% 一个核),
没有待解析记录时那一拍连内核都不问;报告不再答得出「开窗以来一共多少」。
**真机未验** —— 以上全部由单元测试 + 六处变异验证覆盖。

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

## macOS 的 DirectDialer 一直到不了公网(2026-08-13,真机已验)

`DirectDialer` 用 `IP_BOUND_IF` 绑物理网卡防环,而**它只查该接口的 scoped 路由表**。
macOS 只在有**多个活跃网络服务**时才装 per-interface scoped default;单服务(只有
Wi-Fi)的机器上 `default` 的标志是 `GLOBAL`,scoped 表里根本没有它。

真机实证:`route -n get -ifscope en0 8.8.8.8` → **`not in table`**;
`bound-en0 udp 223.5.5.5:53` → **`network is unreachable`**;而
`bound-en0 tcp <VPS>:443` → **通**(bx 自己装了那条 /32 的 en0 路由)。

**后果:所有用户 direct 规则全部 ENETUNREACH,而隧道毫发无伤** —— 因为隧道的
server bypass 恰好是一条显式 en0 路由。**这一直是坏的,只是 bx 从来数不出失败**,
所以没有人知道;结果计数上线的第一分钟就把它显形:`*.qq.com 1291 条,失败 1289`、
`*.icloud.com 6/6`、`*.push.apple.com 6/6`。(排查 Steam 时我曾归因到「用户的规则把
它逼上了一条不通的路」—— 方向对,**根因判浅了一层**:不通的不是那条网络,
是 bx 自己的直连器。)

**修法**:`Hijack` 顺手给物理接口装一条 scoped 默认路由(`route add -ifscope <dev>
default <gw>`)。它**只进 scoped 表、不碰全局表**,隧道的 `0/1`+`128/1` 照旧压过一切 ——
只有显式用了 `IP_BOUND_IF` 的 socket(恰好就是 bx 自己的直连/解析/socks)会用到它。
不削弱 kill-switch:那是 Dialer 里的判定,不是路由属性。

**这条路由必须「可选」,而且装失败时不许进待删列表** —— 后半句是要害:多服务的
Mac 上系统自己就有一条同款,`route add` 以 "File exists" 失败,若记进待删列表,
拆除时就会 `route delete -ifscope en0 default` **删掉系统自己的那条**,打断用户的网络
而 bx 无从恢复。为此 `darwinRouteSpec` 加了 `optional`;非可选的路由(split-default
那些是保护本身)失败仍然中止并回滚。

**真机验收**:`direct/direct_failed` 从 `1322/1308` 变成 `45/0`,十条用户规则全部
零失败(`*.qq.com` 17/0、`*.icloud.com` 11/0、`*.icloud-content.com` 15/0)。
**注意区分两种错误**:`network is unreachable` 是路由问题,`i/o timeout` 是目标不应答 ——
验收时探针里有一个百度 IP 是后者,与本修复无关。

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

**真机验收(142.111.173.173,全新 Ubuntu 24.04)**:一条命令 → 远端自取并校验
二进制 → 装 reality+hysteria2 → 放行 ufw → 起服务(active+enabled)→ 443 TCP/UDP
双 LISTEN → 打出可直接粘贴的 setup 命令(含 `--udp`)。**再用第二台 VPS
(102.208.216.250)当干净外部视角做端到端**:tcp/443 通而**对照的 12345 不通**、
reality 把未认证探测正确中继到真 cloudflare(http 200)、真实客户端握手 619ms、
**经隧道出口 == 142.111.173.173 而直连出口 == 102.208.216.250** —— 全程用
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

## 拆除台账:Run 的还原顺序变成数据(2026-08-30,真机未验)

`Run` 的九处有序 `defer` 改走 `internal/supervisor/teardown.go` 的
`teardownLedger`。**动它的理由不是好看**:defer 是同步的,一步拆除挂住,它
后面的每一步都不会跑,只有关机 watchdog 强制退出兜底 —— 而强制退出会把
**剩下的还原全部跳过**(run.go 那条 watchdog 的注释里早就点名了嫌疑:
`eng.Close`/`tun0.Stop`)。台账给出 defer 给不了的三件:**逐步限时**
(一步挂住只损失那一步)、**命名与记录**(关机时查得出卡在哪,此前只有一份
goroutine dump)、**可断言**(顺序是数据)。

**三条不许动的细节**:
- **LIFO 一字不改**:后获取的先释放 —— 路由还原必须排在关 TUN 之前;
- **`defer teardowns.unwind()` 的位置承重**:落在第一个系统资源之前。混着
  来(一半 defer 一半台账)会把两者的相对顺序悄悄颠倒;迁移前后逐条比对过
  `signal.Stop → 台账(11 步)→ cancel` 与原状一致;
- **超时的 goroutine 是放生不是杀掉**(Go 杀不掉),所以「超时」只等于
  「它没在预算内做完」,不等于「那件事没做成」—— 日志措辞按这个来。
- 单步预算与 `shutdownGrace` 留三倍余量,由
  `TestTeardownStepBudgetLeavesRoomBeforeTheShutdownWatchdog` 钉住:一步挂住
  绝不该把 watchdog 逼出来。watchdog **不许删**,它兜的是台账之外的挂点。

**一条要记住的判据边界**:FIFO 变异下**单测转红而集成台照样绿** ——
顺序性质由单测背书,不是台子。linux 的 `ip rule del` 不依赖 TUN 还在,台子
照不到;真正吃这条顺序的是 darwin 的 split-default。别把台子的绿读成
「顺序有背书」。

## 后台工人登记册:一个 goroutine 的 panic 不再打死整机(2026-08-30,真机未验)

`Run` 里六个长命 goroutine 此前是裸 `go f(ctx)`、**零个 `recover()`**。
裸 goroutine 的 panic 不会被 `Run` 的 defer 接住 —— Go 当场终止进程,而
**别的 goroutine 的 defer 一个都不会跑**:进程没了而内核里的 ip rule /
策略路由**还在**,整机流量指向一个已经不存在的 TUN。现全部经
`internal/supervisor/workers.go` 的 `workerRegistry.start` 启动(具名 +
panic 收在自己那一层)。

**六个工人一律 recover-and-continue,是逐个想过的结论不是图省事**:
mutation-engine 死 ⇒ 切服务器不工作 · tailscale-bypass 死 ⇒ 旁路停在兜底表 ·
transport-failover 死 ⇒ 不再自动切备(**kill-switch 仍在**) ·
direct-egress-repair 死 ⇒ 直连出口不再自愈(2026-08-13 那个 bug 会回来) ·
rule-history 死 ⇒ 历史停止累计 · china-list-refresh 死 ⇒ 列表不再更新。
六件里没有一件值得用「一台受保护的机器断网」来换。**但代价写明了**:炸掉的
工人就此不再跑,那是一次**静默降级** —— 所以它不只打日志,还记成数据
(`panickedNames`),让「少了哪个后台循环」答得出来。

**守卫判据取 AST 不取文本**(`TestRunLaunchesNoBareGoroutines`:`Run` 函数体
内真实的 `GoStmt`)—— 注释里、字符串里、别的函数里的 `go ` 都不算。变异实测:
塞回一个裸 `go mutEng.Run(ctx)` **能编译**(说明它是真实可能的改动)且守卫
转红。读不出 `func Run` 时它**响亮失败**而不是静默放行。

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

## 读源码的守卫:三种处置(2026-08-31)

全仓真正读源码的测试函数 **60 → 57**,而**这个数字本身比想象的诚实得多**:
其中 **38 条是 Swift 菜单守卫**(Go 测试编不了 Swift、`main.swift` 也进不了
Swift 测试 target),**3 条是纯度守卫**(按 AST 禁 net/os/exec,**本该存在**,
不是变通)。剩下十几条 Go 守卫里,多数守的是**否定命题**(「不存在第二条接线」)
或**不可调用的函数**(要出网),结构上无法行为化 —— **它们该留着**。

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

**尚未做、且刻意不在无人监督时开的**:Swift 那 38 条的根治办法(把
`BxState`/`resolve()` 搬进能编译进测试套件的 `MenuState.swift`)。实测
`resolve()` 是**嵌套在另一个函数里的 125 行闭包**,捕获五个外层局部变量;抽它
要把捕获变成显式参数,并让一批读 `main.swift` 的守卫失去锚点、逐条重判 ——
收益是指标,风险是用户天天在用的菜单。

## Run() 拆相位:按判据切,不按行数切(2026-08-31)

`Run()` 764 → 734 行,但**行数是副产品,不是目标**。切的过程中确认了一件事:
**它的 700 多行不是等价的**。相位 3(fake-IP + DNS)里真正的判据(hosts 合并、
split 路由)**已经在独立函数里**,剩下的是纯粘合 —— 把粘合搬进另一个函数只是
把同样的耦合换个地方放(一个 3 进 6 出的函数不比原地代码更清晰)。

**判断「哪里还藏着判据」有个客观信号:读源码的守卫指向哪里。** 守卫是前人留下的
路标 —— 他们想断言某件事,而那件事长在接线里够不着,只好去比字符串。

已抽出两块:`buildSplitBrain`(「global 一个字节的 china 列表都不读」「CLI flag
压过 config.lists」,两条各自对应过真实事故)与 `buildSplitRoutes`(顺序即优先级)。
china 列表与它的两个路径**留在相位内** —— 此前是四个只在二十行内被用到的局部
变量,而组装根的每个局部变量都是一次「它后面还会被谁改」的阅读负担。

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

**分家已真机验(2026-09-01 升级后)**:`bx.log` 末行是当次 `✅ bx 已全局接管`,
而重启时刻之后 `bx-guard.err.log` 里的 `singbox:` **0 行**,也没有
`guardian_core_log_unavailable`(那条退路没被触发)。**顺带得到一次天然对照**:
err.log 曾被截断过一次,22 小时重新长到 9.4MB(≈10MB/天);升级后两分钟只长
90 字节(≈65KB/天),**约 160 倍**。

**折叠那一半仍未验**:`bx.log` 里 `missing default interface` 出现 **0 次** ——
素材还没出现,与此前实测的「90 秒零增长」一致:**那句话是阵发的,不是稳态的**。
它下次发作时 `bx.log` 里会出现带 `[同一行重复 N 次已折叠]` 的行。

## `bx explain <目标>`:判定第一次有了外部出口(2026-09-01,部分真机已验)

**动机是三次病历,三次 bx 都握着答案、三次都没人问得到**:Steam 图片全裂(用户去
怀疑 bx 和 CDN,真因是一条 direct 规则指向的路完全不通,而 bx 每一次都看见了
`dial direct failed` 然后扔掉)· 腾讯会议绕一圈(查半小时,最后靠 `strings` 捞
域名)· `*.qq.com` 57% 失败。**共同形状是请求级的「为什么」**,而 bx 全部 11 个
只读 MCP 工具、`bx status`、`bx doctor` 答的都是系统级的「状态如何」。

`route.Explain(Meta) → (Decision, Reason)` 每秒执行上万次,**此前只被 dialer.go
调用**。这个功能就是给它开一个出口。

**几条改之前要读的判断**:

- **问活着的 Core,不在 CLI 里重建 Router。** bx 不热重载,盘上的配置可能已经和
  跑着的那个不一样;CLI 自建的答案是「bx **应该**做什么」,而用户问的是「bx
  **会**做什么」—— 两者不同的那一刻恰恰最需要这个命令。Core 没在跑就如实说,
  **不退回配置推断**。
- **合成不重写一遍。** `route.Explain` 只给路由判定,实际结局还要叠 kill-switch
  与 UDP 档。三条路选了第三条:`(*Dialer).Explain` 是**同一个 Dialer 实例**上的
  只读兄弟方法,读同一个 Router 指针、同一批 Transport、同一个 `Killswitch`/
  `UDPMode`,并调用**同一个** `killswitchBlocks` —— 不是第二份判据,是同一个对象
  的另一个问法。(把 `dialInner` 的合成整块抽出来风险大于收益:它和 stats、
  recordApp、真拨号绞在一起,是全产品最热的路径。)
- **分支结构仍是分别写的两份**,由 `explain_drift_test.go` 挡住:9 个目标 ×
  健康/不健康 × kill-switch 开关 × 三种 udp.mode = 108 组,断言 Explain **预言的**
  结局与 `dialInner` **真的做出来的**一致。
- **Explain 绝不许有副作用。** `udpRuleOverride` 在不命中时会调
  `countUDPCounterfactual`,走那条路等于拿一条根本没发生的连接污染反事实计数 ——
  而那份计数正是「让 china 列表也对 UDP 生效」那个搁置选项的依据。故剥出纯判据
  `udpRuleOverrideDecision`,并由 `TestExplainRecordsNothing` 钉住。
- **`route.Explain` 从不解析。** 写 spec 时我判断错了(以为未命中域名规则时会用
  国内 DNS 解析再按 IP 判),动手时被代码证伪:它直接 `return Proxy, SourceDefault`,
  注释写明理由是不拿可能被污染的国内 DNS 做 geoip。**所以 `--resolve` 整个不必
  存在。**(顺带发现 `dialInner` 里 `dec == route.NeedResolve` 那一支**今天不可达**。)

**失败分类(`internal/dialfail`,叶子包)**:一个百分比**答不出该不该管** ——
同样是 15%,全是 `unreachable` 就要立刻去查路由(2026-08-13 那个 DirectDialer 故障
的签名),全是 `timeout` 就一个字都不用改。而 err 一直在手边
(`conn, err := d.Direct.DialContext(...)`),此前进一行 debug 日志然后被扔掉。
类别名下沉叶子包的理由同 `internal/udpsource`:它是 dialer 与 stats 之间唯一按
字面对齐的东西。**顺序是判据的一部分**:DNS 排在超时之前(DNS 超时的可行动信息
是解析器不是对端);**nil 返回空串不是 Other**;`canceled` **只给名字、不改它是否
计入失败** —— 先量再决定,反过来做会让改动前后的累计不可比。

**两处「数字看起来在说 A、实际在说 B」,都是真机首用当场发现的**:
- `Rule == ""` 那一档(默认/内建列表)的计数是**一个桶的合计**,不是这个目标的。
  真机实测 `steamstatic.com` 与 `1.1.1.1` 拿到逐字相同的 49/15228/73。不删那两行
  (整体失败率是有用背景),加一句话把它归位;命中具体规则时**不加** —— 多余的
  免责声明会让一个准确的数字显得可疑。
- **「累计」必须说清覆盖多长时间、跨几个版本、表溢出过没有**。三个字段本来就在
  `RuleHistorySnapshot` 里,第一版全丢了。一个跨半年几个版本的 15% 与一天之内的
  15% 是完全不同的两件事,而读的人会默认它是后者。

**真机状态**:`bx explain` 本身**已验**(当场答出 `*.qq.com`:命中 `*.qq.com`、
本次 78 次/15 次失败、累计 2726/425)。**失败分类与累计口径未验** —— 升级后第一件
事就是 `bx explain qq.com`,看那些失败是 `路由不可达` 还是 `对端不应答`:前者是
8-13 那个故障的签名要立刻查,后者一个字都不用改。`bx_explain` MCP 工具也未验
(没有 agent 调过)。设计 `docs/superpowers/specs/2026-09-01-explain-target-design.md`。

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

## 陈旧的恢复结局把 Protected 改写成 Blocked(2026-09-04,真机诊断,修复真机未验)

真机:开机后一次手动重连(`recovery-17`,reason=manual)在 `transport_health`
失败;用户 `bx down` 再 `bx up`,up 的应答是 Protected,而 `bx status` 与菜单
一直 Blocked、图标裂开 —— **同一份 status 里 observed 说 capture=true、
barrier_present=false、tunnel_healthy=true,机器其实受保护**。机制:
`observableStatus`(`localapi.go`)只要**上一次**路径恢复的快照还写着 failed,
就把 Manager 自己的 Protected 改写成 Blocked,而那份快照没有任何东西会在用户
之后成功的 up/down 里清掉。这是「status 是记住的不是推导的」那一类失效。
修法在源头:`Manager.Up`/`Down` 成功后 `retireCompletedPathRecovery` 让**已结束**
的恢复不再对外发布(正在跑的由它自己发布结局,不插手;`networkGeneration` 不动),
与「显式 up/down 无条件清挂起」同一条纪律。**观测层也补了反方向那条**:此前
`Diverge` 只盯「说好其实坏」,对「说坏其实好」一言不发(当时 divergence 为
null);现在 believed=blocked 而 barrier_present=False 会产出一行。Unknown 不报。
**装上修复之前的机器**:那个 Blocked 会一直留到下一次恢复成功或 Guardian 重启;
`sudo bx reconnect` 成功一次即可换掉那份快照。开机那次为什么断开仍未查
(要 Guardian 日志)。
**同一形状第二次上真机(2026-09-07),这回用户什么都没做**:电池上一小时
50 秒一拍的 Maintenance Sleep/DarkWake 里,`recovery-10`(underlay_changed)在
Core 那边的 `verify` 连败 20 次后放弃 —— 退避 100ms→5s 封顶,20 次只要 105 秒,
整段都落在 Wi-Fi 反复起落的窗口里;07:20 真正醒来后捕获、DNS、隧道全自愈
(observed 五项全绿、curl 通、qq.com 直连不再新增失败),而 failed 快照留着,
status/菜单 Blocked 一个多小时。**Core 侧失败从不碰 m.status**(只有 DNS/屏障
那半才 needsAttention),所以这个 Blocked 从来只是一个标签,内核里没有屏障。
09-04 的退场只挂在 up/down 上,用户不动手就永远不跑。修在调谐环
(`retireContradictedPathRecovery`,每轮 `reconcileOnce` 调):观测到捕获在
我们的 TUN、屏障不在、DNS 归 bx、Core 应答、隧道健康 —— 正是 verify 要看的
五项 —— 且 Manager 自己说 Protected、没有正在跑的恢复,就让 failed 快照退场并记
`guardian_path_recovery_retired reason=observed_protected`。任一项 False/Unknown 都不动。
**它一个人不够**:滞后最长一个退避周期(10 分钟),刻意不从恢复代码里叫醒循环
(wakeReconcile 的注释明写只由 up/down 调),而所有者的原话是「能上网,但菜单裂开」
—— 那 10 分钟对用户就是 bx 坏了。故**状态组装那一刻也判**
(`recoverySupersededByCore`,`observableStatus` 里):Guardian 每次答 `/v1/status`
都会问 Core 的运行时事实,`CoreRuntime` 现在多带 `RoutesInstalled`/`DNSListening`/
`UDPRequired`/`UDPReady`(从 RuntimeState 搬来,问不出来保持 false),连同
`TunnelHealthy` 正是 Core 那边 verify 闭包看的那几项;全满足 + Manager 自己说
Protected ⇒ 失败快照是历史:发布 idle、不改写成 Blocked、并把 Manager 记忆里那份
也退场(`reason=core_verified`,与调谐环共用 `retireFailedPathRecovery`)。菜单每 2 秒
问一次,于是下一拍就合拢。Core 问不出来(Reachable=false / 没接 provider)一律不算。
三个消费方(Guardian `observableStatus`、CLI `assembleClientStatusReport`、菜单
`recoveryPresentation`)各自把「有 failed 快照」读成「现在 Blocked」,是同一假设的三份
拷贝;修在源头让三处按构造一致,那三份拷贝没动。为什么那 20 次 verify 失败仍要看
Guardian 日志(`network_recovery` 行带 detail)。

## Linux:Tailscale 的 WireGuard 底层 UDP 绕开劫持(2026-09-04,真机诊断)

**Mac ↔ 公司工作站 300ms 的真因**:工作站(bx global)上 tailscaled 发往对端**公网**
地址的 WireGuard UDP 落进 pref 200 → table 100 → 进 TUN → 经隧道从美国 VPS 出去,
对端看到的源地址对不上,直连永远建不起来,只能走 DERP。真机 `ip route get
180.158.6.185 mark 0x80000 ipproto udp` → `dev bx0 table 100`;同机 `ip route get
195.133.192.92` → `via 10.84.14.1 dev eno1`(server bypass)。**`tailscale netcheck` 的
`UDP: true` 是假安心**:它探的 STUN 就是自建 DERP,而那个 IP 恰好在 bypass 里 —— 于是
STUN 通、WG 不通,此前「公司封 UDP」那条记录就是这个机制造成的误判。macOS 上没有
这个问题(tailscaled 把 socket 绑在物理网卡)。bx 已照顾 Tailscale 三处(DERP 旁路、
100.64/10 → table 52、tailscale.com 不给 fake-IP),这是漏掉的第四处。

修法是一条规则:`ip rule add pref 90 fwmark 0x80000/0xff0000 ipproto udp table main`
(`platform_linux.go` 的 `optionalRouteUpSteps`;blockV6 时 `-6` 同款)。**只认 Tailscale
打的标 + 只认 UDP**:TCP 控制面/DERP 照旧经 bx。**它是可选步骤**:`ipproto` 选择器要
iproute2 ≥ 4.17,busybox 与老 NAS 没有,混进必装步骤会让一台本来能起的机器起不来
(netns 台子跑在 busybox 上,实测 `exit status 1` 后 `up()` 照常、还原干净 ——
**台子只能证明退路,证明不了规则真装上了**;后者用 alpine + 真 iproute2 验过 argv,
再由工作站真机验)。拆除对称 del;rehijack 的 `routeUp` 同样带上。

## 休眠唤醒后隧道成环:服务器旁路路由自愈(2026-09-04,真机诊断,修复真机未验)

**它看起来像「重启后 bx 断开」,其实不是重启**:电源日志 13:20 电量耗尽进入
Low Power Sleep 并休眠到磁盘,17:50:58 按电源键从休眠唤醒;开机时间是 9 月 1 日,
Guardian 进程从 9 月 3 日一直活着。唤醒 4 秒后 Core 日志开始刷
`singbox: open connection … using outbound/vless[reality-out]: EOF`,**1–2 毫秒**
一条 —— VPS 在 300 毫秒之外,真正的 EOF 不可能 2 毫秒回来,那是本机给的。
机制:en0 重新关联时 macOS 把挂在它网关上的路由冲掉了 —— **包括 bx 装的服务器
/32 旁路** —— 而 utun 上的两条 /1 劫持路由不挂在 en0,照旧活着。sing-box 到 VPS
的连接于是进了 TUN,被 bx 当成一条普通公网连接:隧道不健康 → kill-switch 拦下 →
EOF。**bx 的服务器防环靠的是内核路由,不是 socket mark**(子进程),那条路由丢了
就是成环,而成环是静默的、自锁的(隧道起不来是因为它自己的包被拦,而拦它是因为
隧道没起来)。同一个 Wi-Fi、generation 没变,路径恢复的边沿触发不响;
`direct_egress` 那条循环 17:51:22 修好了 scoped 默认路由(日志坐实),对 /32
一无所知;手动重连造出的候选传输走同一条坏路,20 秒也起不来;13 分钟后用户
down/up 才救回来。**bx 里没有一行处理睡眠/唤醒的代码**(grep 过)。

修法照抄 `direct_egress`,**去问内核**:`internal/supervisor/bypass_route_repair.go`
每 30 秒对每台服务器 `route -n get <ip>`(不带 `-ifscope`:问的是普通 socket 走
哪条路,子进程正是普通 socket),接口是我们自己的 TUN 就是成环,经控制面那把锁里
的 `controlServer.reassertRoutes`(Rehijack 的 apply)重新落实全部路由;有待确认的
改动时让路。判据三态(`decideServerBypassIntact`):**没有服务器可查 / 问不出来
都是「不知道」,不是「完好」**。循环体与 `direct_egress` 共用一份
(`watchKernelRoute`,措辞做成数据),退避、冷静期、change-only 日志一字不差。
只在 darwin 有探测原语(与 `direct_egress` 同一门槛);非 darwin 恒「问不出来」,
循环不动。**真机验收**:合盖休眠 ≥ 数分钟再唤醒(或 `sudo route delete
<服务器IP>` 模拟),看 `bx.log` 在 30 秒内出现 `server_bypass 断了` →
`server_bypass 已重新落实路由`,且 sing-box 的 EOF 刷屏停止、隧道回绿,
**不需要 down/up**。

## 服务器旁路重新跟随 DNS(2026-09-04,真机未验)

**起因是一次静默了一个月的断网**:VPS 2026-08-06 换 IP,NAS 上的 bx 重连 65638 次、
一直到 09-03 才被人发现。根因是「什么必须绕开隧道」**只在启动时算一次**
(`resolveServerBypass` 全仓唯一调用在 `run.go` 启动处):staticA 把服务器域名
钉在启动那一刻的 IP,旁路路由 pin 的也是它,子进程经系统 DNS(=bx)永远拿旧答案。

**修法不另造判定**:切换服务器那条路早就有「重读配置 → 防环解析 → 发布两半 →
变了就 rehijack」(`newBypassRefresher` + `handleSetServer`),
`internal/supervisor/bypass_refollow.go` 只是在**主传输连续不健康 ≥ 2 分钟**时
替用户按一次那个按钮(`controlServer.refollowServerBypass`),变了就 rehijack 再
`Reconnect` 让子进程重新解析;两次之间 ≥ 5 分钟(每次都问一轮 DNS,不限频就是
一个 DNS 探针)。**必须在 `cs.mu` 里、且有待确认的改动时让路**:刷新是替换语义,
与 `/v0/server` 交错会把还没落盘的新服务器从旁路里剔掉,而它的路由已经装上了
(`TestSetServerSerializesConcurrentBypassRefresh` 守着的那个洞)。`Reconnect` 在
锁外:它要等新隧道健康,最长一个 healthTimeout。**只对域名链接有意义**:IP 字面量
在 `resolveAll` 里短路,换 IP 只能改链接(这台 Mac 就是这种)。接线经
`controlServeOptions.OnControlReady` 回调交出 `controlHooks`(serve 的返回形状被
`requireControlSocket` 钉着),循环从 `run.go` 经 `workers.start` 起;
`TestRunWiresTheServerBypassRefollowLoop` 读源码钉住接线(serve 非 root 起不来,
netns 台子造不出「VPS 换 IP」)。同一天顺手做掉的两条:`bx explain` 对**具名规则
本次 0 次记录**明说「bx 没见过走这条规则的连接」(那次 Tailscale 误判就是被省略的
那一行害的;`Rule==""` 那一档仍然不渲染 0,nil 在那里是「没记名」);
`provision.atomicWrite` 写之前先 statfs 问放不放得下(QTS 的 `/` 是几百 MB
内存盘,默认 data_dir 解 29MB sing-box 必 ENOSPC),放不下或写出 ENOSPC 时错误里
直接点名 `data_dir`,探不出空间则放行(保险不是新前置)。
**真机验收**:换一次服务器 IP(或先改 DNS 记录),看 `bx-guard.err.log`/`bx.log`
在 2–7 分钟内出现 `server_bypass_refollow 已切到服务器的新地址` 且隧道自己回绿。

## 约定

- **CLAUDE.md / README.md 点名的文件必须真的在**(`TestDocumentedFilePathsExist`,
  2026-08-24)。**范围刻意只有这两份,不含 `docs/superpowers/{specs,plans}`** ——
  首次全仓扫描给的结论:文档里共点名 541 个路径、55 个不存在,而**这 55 个无一在
  CLAUDE.md**,全部在 plans 里。计划书是**有日期的意图记录**,它点名的是「将要建
  的文件」;实施走偏或功能后来被删,它的路径失效是预期的,不是谎。把 plans 拉进来
  只会制造 55 条假红,而假红的守卫会被下一个人删掉。
  **同一轮试过、而刻意没做的一条**:「文档里点名的**标识符**是否存在」。噪声太大 ——
  92 个「查不到定义」里绝大多数是 stdlib、Win32/AppKit API、plist 键、域名,以及
  CLAUDE.md 自己明确记述「已删」的东西(`KickControl`/`StatusPanel` 那一类),做不成
  闸门。**但那次扫描本身有产出**:它抓出 CLAUDE.md 的「速率」那一整段描述的是已经
  被换掉的做法(客户端做差、键是 (组,应用名)、判据在 `AppTrafficRateTracker` 里 ——
  三条都不再成立,那个类型已删),已按代码重写。
- **散文里点名的测试必须真的存在**(`TestEveryTestNameMentionedInProseExists`,
  **范围含 CLAUDE.md 本身**,
  `internal/cli/testnamerefs_test.go`,2026-08-24)。这个仓库最常复发的失效不是代码
  错,是**关于代码的陈述**错:注释写着「由 `TestXxx` 钉住」而 `TestXxx` 早已改名或
  删除。下一个人读到那句话,会**据此不再去检查那件事**。首次全仓扫描一次性抓出
  **7 个失效引用**;其中一个点名的测试**压根不存在**,而它描述的那件事(常驻安全
  告警的 hint 必须是真敲得动的命令 —— 它已经错过两次:一次指向不存在的
  `bx direct remove`、一次漏了 `sudo`)在 supervisor 那一侧**确实无人守**,那条守卫
  已按注释描述的样子补上。判据对**前缀**宽容(`TestFoo*` 这类通配写法很常见,
  宁可放过一个也不制造假红);**刻意退场**的测试登记进 `retiredTestNames` 并写明
  被什么接手了,另有反向断言钉住「退场的名字不许又变回真测试」。
  **这条守卫自己犯过它要抓的那个错**:反向断言原先排在前缀匹配之后,于是永远
  不可达 —— 变异实测随手加一个同名空测试整条守卫照样绿。**一条在最需要它时恰好
  不可达的断言,与没有这条断言完全一样,而它看起来更让人放心。**
  **2026-09-02 把 CLAUDE.md 拉进同一条守卫**(不是加第二份):它点名了 41 个测试
  而此前**一个守卫都没有** —— 而它恰恰是下一个人(或下一个 agent)开工前唯一会
  通读的东西。首次全量扫描**是干净的**:5 个查不到的里两个是散文占位符
  (`TestFoo`/`TestXxx`,按构造被正则的最短长度排除,不是碰巧),另外三个正是
  「读源码的守卫:三种处置」那张表里明写已退役的,早登记在 `retiredTestNames`。
  测试**顺带改了名**(原 `…MentionedInAComment…`):一条叫「注释」的守卫会让人
  以为 CLAUDE.md 不在保护范围内,而那正是它自己要消灭的那种陈述。范围刻意只加
  这一份、不含 `docs/superpowers/{specs,plans}`,理由同 `TestDocumentedFilePathsExist`。
- **TDD**:先写失败测试→跑红→最小实现→跑绿→提交。纯逻辑测试免 root(用 `t.TempDir()`,不碰真实路由/设备)。
- **绝不并行派两个会写盘的子代理进同一个 checkout。** 2026-08-17 实测的代价:两个
  实施代理按「路径不相交」并行(一个改 `internal/socks5`,一个改
  `internal/cli`/`internal/rulereview`),结果**一方为隔离自己而 `git stash`,把另一方
  五个进行中的未提交文件整批卷走** —— 受害者看到的现象是「文件回到一个我从未提交过
  的 HEAD,而两个我根本没碰过的文件显示为已修改」,只能从零重做。
  **路径不相交挡得住 git 冲突,挡不住两件事**:① `verify.sh` 是全树的,任何一方跑全量
  都会读到另一方的半成品(已害得一个代理吃过一次假红);② **`git stash` 是全树操作,
  完全不受路径保护**。
  只读的 review 代理可以并行。要真并行写,就得各自一个 worktree。
  **控制器的判断错误也记在这里**:当时依据 stash 那一方「零数据丢失、逐字节还原」的
  报告下了「没出事」的结论 —— 那只在它自己视角内成立,它不知道自己卷走了同伴的工作。
  **一方的报告不是全局事实**,尤其当那一方恰好是肇事者。
- **验证命令**:`bash scripts/verify.sh`(全量 12 步)或 `--quick`(改一行时,跳过 race 与交叉编译)。
  **判据一律是退出码,不是字符串匹配。** 它的存在是因为 2026-08-11 那轮里同一个根因栽了六次:
  `go test … | grep …; git commit` 用 `;` 串联(测试红了照样提交)、变异验证 grep `^failed` 而套件
  打印的是 `FAIL:`(「没转红」被误判成守卫失效)、`head -5` 查 `set -e` 而注释头十几行、`grep -c` 数
  「出现次数」而它数的是行数、替换串带了不存在的前导 tab 而 `str.replace` 匹配不上时不报错。
  **别再手敲那一串命令**;`verify.sh` 自己也验过五个方向都会失败,漏一道闸门由 `TestVerifyScriptCoversEveryGate` 钉住。
  **一个会偶发红的闸门比没有闸门更糟**,因为它训练人去重跑 —— 而重跑正是「判据是
  退出码」这条纪律唯一的解毒方式。2026-08-17 抓到并修掉一个:`internal/socks5` 的
  `TestDialerUDPAssociateRelaysDatagrams` 在 1500 次里失败 4 次,根因是 UDP ASSOCIATE
  的客户端 socket 绑的是**双栈通配** `[::]`,而 relay 是 IPv4 —— 服务端明明写成功了
  (14 字节、err=nil、28µs),客户端两秒收不到。**内核层面为什么会漏投这个跨族环回包
  至今未查清**,但修法不依赖它:一个 SOCKS5 客户端只跟一个 relay 说话,socket 就该绑在
  **relay 所在的地址族**上(`78edafa`,改后 0/1500)。守卫钉的是**修法的机制**而不是那个
  flake ——「IPv4 relay ⇒ 本地址是 IPv4」是确定性的,而 1/375 的失败率跑一遍抓不到。
  **同一个文件里第二个、独立的间歇失败源也已修(2026-08-17)**:`serveTCP`/`serveUDP` 是
  活过测试函数的 goroutine,而它们在里面调 `t.Errorf`(以及 `t.Helper()`,同样是测试
  完成后不该调的 `*testing.T` 方法)—— 测试返回之后再调 `t.Errorf` 会让 Go panic
  (`Log in goroutine after Test… has completed`)。当时只修了被点名的那一处丢弃
  `WriteTo` 错误的地方(经 `t.Cleanup` 排空的 channel);现在把同一套机制推广到两个
  goroutine 里全部诊断点(读握手/版本/方法/请求/地址、写方法回复/写 ASSOCIATE 回复、
  解析/构造 UDP 数据报……一律经 `s.reportf` 排队成 `error`),并加一个 `sync.WaitGroup`
  让 `t.Cleanup` **先等两个 goroutine 真正退出、再排空 channel 逐条 `t.Errorf`**——
  否则会有「goroutine 还没来得及把错误塞进 channel,Cleanup 已经查过一遍」的竞态,
  origin 那版靠 `select+default` 单次不阻塞查询本就吃这个亏。`t.Helper()` 从两个
  goroutine 里整个删掉:它们不再直接调 `t.Errorf`,标记 helper 帧对它们已没有意义。
  **教训是通用的、留着**:任何活过测试函数的 goroutine,一旦持有 `*testing.T` 并调用
  它的任何方法(不止 `Errorf`/`Fatalf`,`Helper`/`Log` 同样算),就是一颗定时炸弹 ——
  正确的形状始终是「goroutine 只把错误递给一个 channel,由测试(或 `t.Cleanup`)
  自己的 goroutine 在还没标记完成时把它转成 `t.Errorf`」,一份机制,别为下一个诊断点
  另开一条路。
  两处 grep 参与判据是**必要**的并已注明:`test-macos-menu.sh` 提前 `exit 0` 时退出码仍是 0(只有收尾
  横幅抓得住),`gofumpt -l` 输出文件名而退出码恒 0。
- **提交信息**:中文 conventional commits,结尾带 `Co-Authored-By: Claude …`。在默认分支直接提交(单人项目)。
- **内嵌资产**:`internal/embedded/assets/brook_linux_{amd64,arm64}`(~30MB)+ `singbox_{linux,darwin}_{amd64,arm64}`(linux ~28MB / darwin ~23MB)是提交进仓库的真二进制,按 GOOS/GOARCH 条件 embed(每构建只嵌匹配的那一个;singbox 经 `embedded_singbox_{amd64,arm64,darwin_amd64,darwin_arm64,other}.go`,**linux+darwin 都内嵌(同 brook 平台覆盖,mac 上 reality/hysteria2 也零依赖即跑)**,windows/其他 arch 走 nil 兜底→下载)。CI `embed-brook.yml`/`embed-singbox.yml` 跟上游 release 自动重嵌。换 arch 要补对应二进制。**缓存键掺内容 hash(已实现)**:`provision.embedCacheKey` = 版本 tag + `sha256(内嵌字节)[:12]`,写进 `.brook-version`/`.singbox-version`;同 tag 重嵌不同字节(如 sing-box 从 `with_utls` 加到 `with_utls,with_quic`)也会失效旧缓存、强制重释放,避免用到陈旧二进制。
  - **sing-box 是「自建静态最小构建」不是官方 release 二进制**:官方 linux 包是 glibc **动态链接 + 56MB 全家桶**(含 tailscale/acme/clash/dhcp,reality 全用不上),违背 bx「静态单文件、零依赖」。故从同一 release tag 源码用 `CGO_ENABLED=0 go build -tags with_utls,with_quic`(REALITY 需 utls;**hysteria2/QUIC 需 with_quic**)自建:**静态**(Alpine/musl 也跑,同 brook)、**~28MB**(官方半体积)、同 revision。CI `embed-singbox.yml` 复刻此构建;改时务必保持 `with_utls,with_quic` 与 `CGO_ENABLED=0`。
- **绝不擅自启动 bx / 改路由**:启动是用户的事(需 root、动真实网络)。改完让用户自己 `bx up`。
- gVisor/wireguard 等库的 API 易随版本变——查 `$(go list -m -f '{{.Dir}}' <module>)` 的真实源码,别凭记忆。

## 跨平台待办

- **IPv6**:决策已定 = **fail-closed 阻断**(不走隧道),设计见 `docs/superpowers/specs/2026-06-11-bx-ipv6-blackhole-design.md`。**Linux 已实现**:`Hijack` 探测 `/proc/net/if_inet6`,v6 内核启用时装 `-6 unreachable` 默认路由(table 100 + pref 200)把全局 v6 堵死,`route.DefaultPrivateV6CIDRs`(`::1`/`fe80::`/`fc00::`/`ff00::`)走主表 carve-out;v6 禁用则零 `-6` 步骤、不连累 v4。on-link GUA 邻居也已 carve:`Hijack` 动态读 `ip -6 route show` 提取 on-link 全局前缀(2000::/3、有 dev 无 via)补进 pref-150,与私网段一并直连(纯解析 `parseOnLinkV6Prefixes` 免 root 可测)。**darwin 已实现(编译过、待真机)**:`Hijack` 用 `ipv6EnabledDarwin()`(扫 `net.InterfaceAddrs` 有无非 loopback v6)门控,装两个 `/1` 的 `-reject`(`::/1`+`8000::/1`)盖全量全局 v6;靠主表最长前缀让 link-local/ULA/组播/on-link(含 GUA)自动直连,无需显式 carve-out(故 mac 无 Linux 的 GUA 局限)。纯构造 `darwinRouteSpecs` 免 root 单测。**真机待验**:① `-reject` 确切语法(dummy gw `::1`);② 本地 errno 是否 EHOSTUNREACH(决定 v4 回落);③ `IPV6_BOUND_IF` 与 reject 的交互(今无 v6 出站,moot)。
- **macOS**:代码能编译、review 过(桥接引用计数/生命周期已验证)。**待真机 sudo 验证**:① `Hijack` 的 `route`/`ifconfig` 语义(含 IPv6 `-reject`,见上);② launchd 服务层(`bx up`/`down` 在 mac 的自启)未做。**逃生路径不变量(2026-08-04,修复真实事故——路径恢复卡在 attempt 178、持续 71 分钟,用户全程无法关闭保护)**:**关闭与恢复路径不得依赖可能已失效的前置条件**——`bx down` 解析不到默认网关时降级为去掉 ServerBypass、强制阻断 IPv6 的 block-only 屏障继续拆除而不报错拒绝(`b4b1c58`);`bx down` 的**干净路径任何一步失败(Guardian 不可达、接管失败、或 `client.Down` 报错)都落到强制拆除**(`eadc589`;`Manager.Down` 开头的 `recoveryBlocked` 在断网期间 Guardian 重启后会永久为真 → socket 可达却恒失败、而屏障已装上,只判"socket 不可达"救不了)。强制拆除跳过安装/bootstrap,按序做四件事、**任一步失败都继续做完剩下的**:① 经 Core 自己的控制面 `/v0/shutdown` 协作关闭(`supervisor.FetchRuntimeState` 取 PID + `ShutdownControl`,与 Guardian runner 同源;**不再指望 `launchctl bootout` 的 SIGTERM 经共享进程组投给 Core**——代码里根本没有 `Setpgid`/`Setsid`,且有 launchd 补 SIGKILL 打断 defer 的竞态),等其 socket 关闭再往下;② `launchctl bootout` 停 Guardian(只停服务不删文件);③ `SaveDesired(DesiredOff)`,否则 plist 的 `RunAtLoad`+`KeepAlive` 下次开机把坏状态带回来;④ **`guardian.RemoveBlockingBarrierRoutes` 清屏障阻断路由**——bootout 抹掉内存里的 `barrierOwnership`,而内核里的 `/2` reject 路由(比 Core 的 `/1` split-default 更长、压过一切)会留下来打死整机连通,且此后新 Guardian 认为"无屏障"照样报绿灯、`removeBarrier` 因 `barrierAbsent` 直接 no-op,孤儿屏障在 up/down/uninstall 全周期存活。清屏障是**用户显式请求停止保护**的结果,不是隧道不健康时的回落,不削弱 fail-closed。文案只列举做过的动作、不断言"网络已还原";路径恢复重试加 `maxPathRecoveryAttempts=20` 上限(约 8 分钟耗尽,不再无限重试,`7693c53`)——重试耗尽后对外状态是 **`Blocked`**(屏障仍在生效,`barrierProven()` 为真),不是 `Needs Attention`。**(2026-09-07 更正:这句只对 DNS/屏障那半的失败成立;Core 侧的 verify/transport 失败不碰 m.status、不装屏障,Blocked 只是 `observableStatus` 按 failed 快照贴的标签,内核可能早已自愈 —— 见「陈旧的恢复结局」一节,调谐环现在会按观测让它退场。)****故障可观测性不变量(2026-08-05,修复真实事故——`sudo bx up` 500,只见 `guardian operation failed`,排查四轮才定位到 `/var/lib/bx/core-process.json` 是前一天事故遗留、指向已死 PID,期间 `/var/log/bx-guard.err.log` 对本次失败一个字都没写)**:**失败必须留下可操作线索**——① Guardian 四个 handler(mutation/update/migration/recoveryRequest)失败时把**完整错误写进 Guardian 日志**(`/var/log/bx-guard.{log,err.log}`),响应体**只带失败码**(如 `code=core_ownership_uncertain`),绝不外传原始错误串(可能含路径/链接/凭据);新鲜度判断用 `needsAttention` 内部**递增的代际号**而非值比较,故连续两次同一失败码仍会正确回传(值比较会把"持续复现的同一失败"误判为陈旧丢弃,而这恰是本功能的主用例)——**码本身可能被省略**,某次失败若没有走过 `needsAttention` 设新码,响应宁可不带 `code` 也不能带错的(`985305e`/`4d08429`/`ef91872`)。**例外(整体复审后补,`a544be2`)**:`recoveryBlocked` 短路(`errRecoveryIncomplete`→`recovery_incomplete`)与 `acquireMutation` 排队超时(哨兵 `errMutationBusy`→`guardian_busy`)由**错误本身**命名,不经 `needsAttention`、也不写 `status.LastError`——这两条恰是「启动恢复已失败 / Guardian 正忙、用户反复 `bx up`」的表现形式,按上面的规则会被省略 code,**最需要指引的场景反而无码**;码描述的是本次失败本身,不是陈旧值,不违反上述原则。**日志的 root-only 前提此前并不成立**:launchd 按 umask 建出的是 `0644`(真机实测,内含服务器 IP 与 116 条 bypass 网段),而「完整错误只进日志」正以此为安全依据;现 `WriteGuardianUnit` 之后调用 `install.SecureGuardianLogs` 把两个 guard 日志创建/收紧为 **0600 root:wheel**(`O_APPEND` 打开,不截断既有内容)(`2963472`)——**注意:已存在的安装在下次装/升级前仍是 0644,本轮未动用户机器上的文件。**② CLI(`internal/guardian/client.go` 的 `guardianHTTPError`)展示该失败码 + 排查指引;码缺失时不输出空 `code=`(`6fb0327`)。**整体复审后修正(`cc09c6d`)**:指引原本以「有 code」为前提,而**没有 code 的 500 恰恰最无从下手**,改为 **500 一律附指引**;且原指引给的 `sudo bx logs` 读的是 **Core** 日志(`/var/log/bx.log`),完整原因写在 **Guardian** 日志里——指引现点名 `sudo tail -50 /var/log/bx-guard.err.log`,`archiveClientLogsWithReason` 诊断包也一并收集 `install.GuardianLogPaths()`(非 root 读不到只留 `.unavailable.txt`,绝不让整个归档失败;事故中「翻诊断包只拿到陈旧 Core 日志」即源于此)。③ `Existing()` 对**OS 权威确认已死**的进程、其陈旧记录清除失败时不再判"所有权不确定"(改记日志 + 当作无既有 Core 继续——清不掉一个陈旧文件不等于所有权存疑,那是给"进程还在但身份不匹配"准备的语义,后者仍 fail-closed 不变);`uninstall` 改为**逐文件**删 `/var/lib/bx/core-process.json`、`/var/lib/bx/guardian-state.json` 这类运行时状态,**绝不整目录删 `/var/lib/bx`**(会连 brook/sing-box 二进制与 china 列表一并删掉),`/etc/bx` 用户配置仍保留(`603b602`)。**整体复审证伪并补齐(`60b76f3`)**:`603b602` 只放宽了 `Existing()` 那一跳,**`Start()` 紧接着以 `durable launch marker already exists` 拒绝同一个文件 → 仍是 `core_ownership_uncertain`,用户可见行为与修改前完全一致**(复审用临时测试实测坐实,原「不再卡死 `bx up`」的断言是错的;原单测只覆盖 `Existing()` 一跳,是假绿)。现 `Start()` 的启动标记检查也向 OS 求证:**记录里的 PID 被权威确认死亡才允许覆写**;fail-closed 一步不让——记录里的进程还活着(身份匹配与否都一样)、标记没有 PID(`launching`,可能有未被记录的 Core 在跑)、求证本身失败,三种情况仍全部拒绝且不启动 Core。端到端由**走真实 `ExecCoreRunner` 的 Manager 级回归测试**坐实(变异验证:去掉修复即复现 `start Core: … durable launch marker already exists`);**真机未验**(本轮不重建用户机器上正在运行的 bx)。④ root 上下文直接 `launchctl bootstrap gui/<uid> …` 装菜单栏 LaunchAgent 在 macOS 上必然 `EIO(5)`(没有用户 GUI session),改经 `launchctl asuser <uid> launchctl bootstrap gui/<uid> …` 先进用户上下文(`dc35594`)——**注意只有 bootstrap 这一步包了 asuser**,同文件的 `bootout`/`kickstart`/`print` 仍是 root 直接操作,这是基于"只有 bootstrap 需身处目标 session"的推断,**真机只坐实了 bootstrap 一步**,其余未验证。 **观测层与不变量基线(2026-08-05,`internal/observe`,纯逻辑+单测,真机未验)**:**意图 / 事实 / 代码 三分**——意图只由用户/agent 显式声明式改动;事实只由 reconcile 改动(本期尚无 reconcile,观测只读);代码由人经 PR 评审改动。**agent 只声明意图,从不直接驱动动作。** `internal/observe` 是**只读**观测层,向系统现问四件事:劫持是否生效(`route -n get 1.1.1.1`/`129.1.1.1` 的接口 == 我们的 TUN)、屏障是否在位(查 `barriercidr.Blocking()` 那组 `/2` 网段是否被内核以 reject 应答;该清单已下沉到叶子包 `internal/barriercidr`,装屏障的 guardian 与问内核的 observe 共读同一份,`guardian.BlockingBarrierCIDRs` 已删)、DNS 归谁(`install.InspectDNSContext`)、Core 是否活着(`supervisor.FetchRuntimeState`——**控制 socket 在应答本身就是存活观测,不需要 PID 文件**)。**三态 `Tristate` 是关键取舍**:区分「观测到否」与「观测不到」,零值为 `Unknown`;用 `bool` 会把「问不出来」压成 false,而这正是旧架构骗人的方式之一(`RoutesInstalled` 是个只置位不复查的 `atomic.Bool`)。任一项观测失败即记为 `Unknown` 并附原因,**绝不中断其余项、绝不让调用方失败**。`bx status --json` 现**并列发布** `desired`(意图)、既有信念字段、`observed`(事实)、`divergence`(二者之差),**不用观测覆盖信念**——二者的 diff 本身就是最高价值的诊断信号;「status 显绿而流量明文直连」在这个结构下表达不出来,绿是 believed 而 observed 会同时说 `capture_ok: false`。首批四条不变量由 `internal/observe` 的纯函数测试钉住(protected 必须三项皆 True、`desired=off` 不得残留屏障/DNS、不一致必须产出自解释 divergence);**第五条(拆除永不拒绝)本期未实现**,以已知失败的测试留在 `invariants_test.go`,让「控制面还没修」成为 CI 里可见的事实而非待办里的一行字。**两处相对设计的有意偏离**:① `Deps.TunName` 返回 `(string, error)` 而非 `string`——生产接线里 TUN 名字取自 Core 控制 socket,「socket 静默」若被压成「没有 TUN」就会报出一个自信的 `CaptureOK=False`,正是本包要消灭的谎言;② 非 macOS 的 `InspectDNSContext` 返回 `Supported:false` + **nil error**,wire 层把它转成错误 → `Unknown`,不把「没问过」报成「不归 bx」。观测整轮封顶 5s(`bx status` 是出问题时最先敲的命令,宁可少答一项也不能挂住),Core 运行时状态一次观测内只取一次并缓存。**观测只在 darwin 附上**(`observerForPlatform`):路由/DNS 原语目前只有 macOS 实现,在 Linux/Windows 附观测换不来任何新事实(`tunnel_healthy` 本就来自同一个控制 socket、已在扁平字段里),却让每次 `bx status --json` 恒吐 **5 条**「该项无法观测」divergence(实测,`capture_ok` 还重复两次)——那会把 divergence 训练成用户和 agent 学会忽略的噪声,正好毁掉它唯一的价值。**字段缺席是诚实的「没问」;满屏「无法观测」则是把静态平台限制伪装成每次调用都新发生的差异。**设计 `docs/superpowers/specs/2026-08-05-observation-layer-design.md`、计划 `docs/superpowers/plans/2026-08-05-observation-layer.md`。**`ErrProcessNotRunning` 在 macOS 上曾永不可达(2026-08-06,真机事故 + 修复 `77227ba`,真机已验)**:**macOS 的「进程不存在」不走 ESRCH**——内核 `sysctl kern.proc.pid` 调用成功但写回 0 字节,`x/sys` 因 `n != SizeofKinfoProc` 转成 **EIO**(v0.45.0 `syscall_darwin.go:513`;本机探针实证:已死 PID 与从未存在的 PID 都返回 EIO,`errors.Is(err, ESRCH)` 恒 false)。而 `inspectProcess` (`process_darwin.go`)只映射 ESRCH/ENOENT,于是 **`ErrProcessNotRunning` 在本平台从来不会被返回**。**后果远超单次事故:两处专门为此写的修复一直是死代码**——`Existing()` 的「PID 已死就自愈、别卡死 bx up」(`603b602`)与 `Start()` 的「OS 权威确认已死才放行陈旧启动标记」(`60b76f3`)都键控 `ErrProcessNotRunning`,**在 macOS 上从未生效过**;这解释了 8-05 那次为何最终只能手删 `core-process.json`。事故链(Guardian 日志逐行可见,**故障可观测性那一期在此完全兑现**):`bx down` 请 Core 退出 → Core 确实退出 → `Inspect(PID)` 拿到 EIO → 不认识 → `core_stop_failed` → 判 `core_unexpected_exit`(误以为崩溃)→ 去重启 → 写下 `launching` 标记 → `core_restart_failed` → 此后每次 `bx up` 撞 `process.go:264` 的无条件 uncertain → **永久 500,只有 `bx uninstall` 能脱身**。**修法刻意不凭 EIO 断言**:EIO 同样可能来自真实 sysctl I/O 失败,本层不可区分,误判「不存在」会放行第二个 Core(正是 `af81632` 被回退的风险);改为向内核求证——只有 `kill(pid,0)` 明确回 **ESRCH** 才判定进程不存在,活着/EPERM/求证失败一律保持不透明错误,fail-closed 不让步。**真机复测**:同一台机器上修复前 `bx down` 必落强制拆除且此后 `bx up` 永久 500;修复后 `down` 走干净路径(`✓ Guardian bx 已停止,网络已恢复`)、`up` 正常、连续第二次 `up` 幂等无操作、Guardian 日志零 `needs_attention`。**`launching` 的死结随后已解**(见本条末尾「Core 所有权改判据」一段):该标记不再无条件判 uncertain,改为向系统求证有没有进程在跑 Core,没有就自愈清标记——**不再需要手删 `/var/lib/bx/core-process.json`**(手删反而危险:盘上无记录曾是唯一没有 OS 求证的启动路径,Core 正跑着时手删会起第二个 Core;那条路径也已补上求证)。两段式 marker 方案见 `docs/superpowers/plans/2026-08-05-guardian-launch-marker-deadlock.md`。**同批真机首验通过的还有**:① `2963472` Guardian 日志 0600(实测 `.rw------- root`);② `dc35594` 菜单栏经 `launchctl asuser` bootstrap(实测 `gui/501/com.getbx.bx.menu` `state=running`)——此前那次 `EIO(5): Bootstrap failed` 是**上一次统一安装残留的 plist** 被 root 直接 bootstrap 所致,legacy CLI-only 安装根本不含菜单栏(plist 只由 `install.UnifiedInstall` 写,程序体就是 `/Applications/Bx.app`)。**两段式启动标记(`e7e413c`,真机已验)**:`Start()` 原先在 fork 与「验明身份后写 owned」之间只留一个 `PID==0` 的 `launching` 标记,该窗口内崩溃即留下无从判断的记录 → 永久卡死 `bx up`。现 fork 一返回就落一条带子进程 PID 的 **`spawned`** 记录再去 Inspect/verify,窗口缩到「fork → 一次写盘」;窗口外崩溃留下的记录可向 OS 求证——进程死了自愈,**活着仍 fail-closed**(`spawned` 没有 executable/generation,从没验明,既不接管也不当它不存在)。**`launching` 仍一律 fail-closed**:设计文档 option 1 主张「`PID==0` ⇒ 确定没 fork ⇒ 可自愈」,实现时被既有测试 `TestExecCoreRunnerPersistenceFailureLeavesDurableUncertainLaunchMarker` **当场证伪**——`spawned` 那次写盘本身失败(磁盘错误)时 fork 已发生而盘上仍只有 `launching`,自愈就会起第二个 Core,正是 `af81632` 被回退的原因;该判据不成立,已撤回那半边。三条既有安全测试(persistence failure / `Start` 拒绝活标记 / Manager 级阻断)全部**原样通过、一个断言未改**,这是「没削弱保护」的依据。彻底解开 `launching` 需绕过自身簿记向系统求证「有没有进程在跑我们的 Core 可执行文件」(与观测层同一思路)——**已在下一段做掉**。**Core 所有权改判据(2026-08-07,进程扫描,`d393171`/`0f0e9d9`/`7778b53`,真机未验)**:上一段留的坑在此解开——`resolveOrphanLaunchMarker`(`Existing()`/`refuseLiveLaunchMarker` 共用,`process.go:222`)不再对 `launching` 无条件判 uncertain,改为向系统现问:枚举全部进程,`looksLikeCore`(`procscan_darwin.go`)判定 `basename(可执行路径或 argv[0])=="bx" && argv[1]=="run" && uid==0`。一个都没扫到 ⇒ 孤儿,自愈清标记;扫到 ⇒ 仍 fail-closed,PID 只报在错误文本里、不进 uncertain 的 `Process` payload(`7778b53` 复审发现塞进去会被 `Manager.retainUncertain` 收进 `m.current`,`Down()` 据此把这个从未验明的第三方 PID 当作已知 Core 发布给用户、并对它调用 `Stop`);`scanRunningCores` 失败(枚举失败,或枚举到进程但 `kern.procargs2` 一个都读不出来)也仍 fail-closed——「问不出来」不等于「没有」。**判据刻意不依赖可执行路径**:更新后旧版 Core 跑在 `runtime/<旧版本>/bx`,按路径匹配会漏认它、进而起第二个 Core——正是 `af81632` 被回退的双 Core 风险换了个入口;过度匹配的代价是拒绝启动(安全),漏认的代价是灾难,`looksLikeCore` 因此故意偏向前者。**判据为何成立**:`fork` 一返回子进程就已经作为进程存在,早于它执行我们的任何代码,所以在「fork 与写盘之间」那个窗口里始终有效——两段式标记(`e7e413c`)只能缩小这个窗口,消灭不了它。与 `internal/observe` 同一条原则:向系统现问事实,不信自己的记账。**平台**:darwin 实现(`procscan_darwin.go`)+ `!darwin` 桩(`procscan_other.go` 的 `errCoreScanUnsupported`,恒返回错误 = 恒 fail-closed)。**移植警告**:桩并非「不改行为」——fork 前的判定改为一律向系统求证之后,非 darwin 平台上连「无记录」这条原本能正常 fork 的路也会被拒,即 `bx up` 起不来 Core。今天不可达(`daemon.go` 的 `requireDaemonPlatform()` 在构造 `ExecCoreRunner` 之前就挡住了别的平台),但谁要移植 Guardian,**必须先实现 `scanRunningCores`,不能只放开 `requireDaemonPlatform`**。**复审(`7778b53`)另证伪了一版假绿**:非 root 身份实跑本机 `scanRunningCores` 测出 874 个进程里 305 个 `procargs2` 读不出、其余 uid 均非 root,而当时实现对此返回 `0 cores, err=nil`——与「如实查过、确实没有 Core」在返回值上完全无法区分;遂补上非 root 调用方、以及枚举到进程但 `readable==0` 两条必须报错的下限。**盘上无记录也要问系统(`b39cc2c`,最终全分支复审补齐)**:`Start()` 此前在「盘上一条记录都没有」时**直接 fork,全程没问过系统一句**——而这正是旧文档推荐的应急手段(手删 `core-process.json`)造出来的状态,且此路径没有第二道防线:`supervisor/control.go` 在 `net.Listen` 前先 `os.Remove(SockPath)`,第二个 Core 会**静默夺走控制 socket**,两个 Core 争 split-default 路由、先退出的那个用自己的旧快照还原掀掉另一个的劫持 → `bx status` 显绿而流量明文直连。现走 `refuseUnrecordedRunningCore`,与 `launching` 同一判据同一条 fail-closed 规则。**崩溃重启路径压在同一条路上**(`handleUnexpectedExit` → `startCoreLocked` → `Start`,退出时 owned 记录已被删掉 ⇒ 无记录):若把刚死的旧 Core 误认成「还有 Core 在跑」,**每次崩溃都会变成永久失联**。处置是**显式跳过 SZOMB 僵尸**(`isZombieProcess`)而不是给重启开后门——真机上宣告死亡本就晚于回收(自有子进程等 `waitpid` 返回;接管来的 Core 要 `kill(pid,0)` 明确回 ESRCH,而僵尸对它返回 0),僵尸过滤是残留窗口的兜底;端到端由**跑真实 `ExecCoreRunner` 的 Manager 级测试**坐实(Core 崩溃后自动重启成功,且前置断言先证明注入的扫描器在 Core 活着时确实报告「有 Core 在跑」,绿灯不是来自空壳桩)。**变异测试(本轮实测)**:把 `resolveOrphanLaunchMarker` 改成无条件自愈使 **6 条**顶层测试转红;把 `refuseUnrecordedRunningCore` 改成 no-op 使 **4 条**转红;两处同时拿掉则 **11 条**转红——(此前记的「6 条」是在只有前一处修复时数的 7 条中的一部分:`TestManagerUpBlocksSameAndReconstructedDaemonAfterUncertainLaunch` 现在改由无记录那道检查兜住,故单独变异第一处时它不再转红。数字随防线增加而变,重要的是**每一处放行都至少有一条测试专门盯着**。)**扫描下限的覆盖也补齐了**:`readable==0`(枚举到进程却一个参数都读不出)那条 fail-closed 下限此前零覆盖——把它改成 `if false && readable == 0` 整套测试仍全绿;现把聚合判定抽成纯函数 `decideCoreScan(enumerated, readable, cores)`(`procscan.go`,syscall 那一半单测造不出来)并由单测钉死。**放行日志可归因**:`guardian_orphan_launch_marker` / `guardian_no_core_record` 带 `hop=`(existing/start)、`state_path=`、标记身份,`scanRunningCores` 另打一行 `guardian_core_scan enumerated=… readable=… cores=…` 普查——它们是「我允许了一个 Core 启动,因为我认为没有别的 Core 在跑」的唯一记录。**已知限制(本轮遗留,2026-08-11 已解,见下条「锁存已有出口」)**:所有权不确定是**锁存**的——`Manager.upLocked`/`Migrate` 在再次调用 `Existing()` 之前就先看 `m.current.Uncertain` 并短路,故某一瞬间扫到的第三方 Core 会变成永久拒绝:那个进程后来消失了,`bx up` 依旧失败且**再也不会重新扫描**。本轮**刻意不动这个锁存**(改 `manager.go` 的生命周期状态机风险大于收益,且这种情况罕见),改为让失败自解释:`internal/guardian/client.go` 的 `guardianCodeHints` 对 `code=core_ownership_uncertain` 附上指引——注意**必须写在 CLI 侧**,Guardian 响应体刻意不外传原始错误串,daemon 那边写的错误文本用户根本看不到。(当时写下的那句指引是「唯一脱身办法是 `sudo bx down` 再 `sudo bx up`(`Down()` 清 `m.current`)」——**那半句从来就不成立**,下条详述。)**真机未验**——以上全部是单元测试覆盖,没有人在真机上人为造孤儿标记验证 `bx up` 是否真的恢复。设计 `docs/superpowers/specs/2026-08-07-core-ownership-by-process-scan-design.md`、计划 `docs/superpowers/plans/2026-08-07-core-ownership-by-process-scan.md`。**真机验证**:升级场景(旧 Guardian 在跑 + 新二进制刚装)`down`→`up` 干净通过、记录如期停在 `{"state":"owned"}` 且 PID 真实存活、Guardian 日志自修复起零 `needs_attention`(最后一条失败停在修复前的 06:58:57)。 **锁存已有出口(2026-08-11)**:上面那条「所有权不确定是锁存的、唯一脱身办法是 `sudo bx down` 再 `sudo bx up`」**两半都不再成立,而且其中一半从来就不成立**。① `Down` **从不读 `Uncertain`**,它按 `m.current.PID` 分支,三种锁存形状(PID 0 + desired=off / PID 0 + desired=on / 带真实 PID)分别停在提前返回、`runner.Existing` 的同一次失败扫描、`runner.Stop` 里那次删记录(与当初造出锁存的是同一次 unlink,**只在持久条件还在时**失败;设计初稿说那条形状必然停在 `verifyInstalledProcess` 的身份校验,实测证伪 —— 那个 process 来自 `m.current`,是验明过身份、带 `Executable`/`UID`/`Generation` 的真进程)—— 全都到不了那句 `m.current = Process{}`,**而且没有任何测试断言过 `Down` 会清它**。用户报告它「有时管用」,靠的是 CLI 的 `bx down` 失败落到强制拆除、把 Guardian 整个 bootout 掉:**真正管用的一直是杀掉 daemon**。现在 `Down` 在**所有**出口(含提前返回、含 `recoveryBlocked` 那条早退、含每一条失败路径)无条件清掉**进门时那一个**锁存(`clearOwnershipLatch` 按值比对身份),但**不清**它自己在 DNS 还原补偿里经 `startCoreLocked` 新造的那个 —— 那会是 fail-open。② **用户发起**的 `Up`/`Migrate` 不再短路,改为经 `recheckOwnershipUncertain` → `confirmNoCoreForRelease` **重新求证**:**两次扫描都干净、中间隔一个沉降窗口(`coreScanSettle` 300ms)**才释放;扫到了(哪怕两次里只有一次)/扫不动/runner 不会扫/求证本身 panic,一律保持拒绝。**判据一个字没变,变的是每次都重新求一遍**;拒绝不付沉降的代价(第一次就扫脏立即返回),反复重试的用户不会被拖慢。**它与 `Down`/`Recover` 用的 `confirmCoreStopped` 是两个函数、偏置刻意相反**:后者第一次扫干净就算数(怕把每次正常关闭都报成告警),对一次假的「全清」并不比裸扫描强;而释放锁存是准入控制,`cleanupFailedStart` 那个残留(已 fork、`Terminate()` 已发、wait 超时,进程既非僵尸也未消失,而 `scanRunningCores` 会跳过 argv 读不出的进程)恰恰就是假的全清 —— **把两者「统一」掉就是把双 Core 的门打开**(`TestRecheckDoesNotReleaseWhenOnlyTheSecondScanIsClean` 与 `TestConfirmCoreStoppedReScansAfterASettleWindow` 在**同一个扫描脚本**下断言相反结论,两条同时绿才证明没被合并)。**`Migrate` 与 `Up` 必须一起改**:两处短路形状一模一样,只改一处就是假绿(`60b76f3` 的教训)。③ **启动恢复刻意不重新求证**:`upLocked` 吃一个必填的 `upOrigin`(`upOriginUser` / `upOriginStartupRecovery`),做成参数而不是从调用链推断,是为了让将来第三个调用点被编译器逼着自己选一边。`retryDaemonRecovery` 每 5 秒重试且永不放弃,把两次扫描 + 沉降搬进按时钟驱动的路径就是一天一万七千轮 —— 设计明写「不许把这套纪律搬进任何按时钟驱动的路径」。它原本最坏的后果(锁存升级成挡住 `Down` 的栅栏)已由下一条拆掉,所以这条短路今天只是「开不了」,不再是「关不掉」;「有限次数地自愈瞬时失败」是留给后续的,要照 `maxPathRecoveryAttempts` 的形状单独做。④ 锁存不再经 `recoverLocked` 升级成 `recoveryBlocked`(判据 `errors.Is(err, ErrProcessOwnershipUncertain)`,与 `hold.go` 对 `ErrMaintenanceHoldUnreadable` 的处置同源):**「开不了」永不许升级成「关不掉」** —— `recoveryBlocked` 是 `Up`/`Migrate`/**`Down`**/两个更新入口的第一句判断,正是 2026-08-04 那次 71 分钟事故的形状。**别的**失败仍然照落栅栏(单独一条反向测试守着)。⑤ 两处「已证明安全」的产地不再产出这条判定 —— `finishExistingWatch`(OS 确认进程消失)与 `Start` 自己那条 wait goroutine(`waitpid` 已返回,证明更硬):删不掉一个 JSON 是**清理失败**,不是所有权存疑(与 `603b602` 对 `Existing()` 的判断同源)。`Stop`/`Existing` 那几处处置的是可能仍属于活进程的记录,**不动**。⑥ 锁存的 cause 现在存在 `m.uncertainCause` 里,与锁存**成对**清除(三处:`clearOwnershipLatch`、`clearUncertaintyAfterProof`、`recheckOwnershipUncertain`,漏一处不会有编译错误);以前短路传的是 nil,于是第二次 `bx up` 看到的反而比第一次**更少**信息:没有 PID、没有产地、没有出路。⑦ **调谐器一侧一个字不改**:循环仍然永不清这个锁存、也不为它扫描(`TestReconcileOnceNeverClearsTheOwnershipUncertainLatch` 原样保留,新增一条钉住「也不去扫」)—— 既有文档反对的是**自动的后台清除者**,不是**用户发起**的重新求证,两者威胁模型不同。**`sudo bx down` 再 `sudo bx up` 仍是一条出路,但要知道它为什么管用**:它清掉的是**记忆**,之后那次 `up` 撞不到锁存、走的是 `runner.Existing`/`Start` 自己的求证(`refuseUnrecordedRunningCore` / `resolveOrphanLaunchMarker` / `refuseLiveLaunchMarker`),而那里**只扫一次** —— 也就是说它是一道**比重新求证更松**的闸门。系统里真有一个 Core 时两条路都该继续被拒,所以**四处用户可见文案都不许写成承诺**(`guardianCodeHints`、daemon 侧 `ownershipUncertainEscapeHint`、升级中止文案、菜单 `toggleFailureHint`;现在说的是「每次 `sudo bx up` 都会重新求证,仍然被拒就去看 Guardian 日志里的 `guardian_core_scan` / `guardian_core_still_running_on_release`」)。**darwin/linux 之外重新求证是 no-op**(`procscan_other.go` 的 `scanRunningCores` 恒返回 `errCoreScanUnsupported` ⇒ `confirmNoCoreForRelease` 恒 false ⇒ 恒拒绝),门仍然焊死;**linux 自 2026-08-29 起有 /proc 实现**(`procscan_linux.go`,为集成台跑真 Guardian 供货),这条门在 linux 上与 darwin 同规则;移植到其余平台必须先实现 `scanRunningCores`。**真机未验** —— 以上全部由单元测试覆盖,没有人在真机上人为造一个锁存(第三方 `sudo bx run` / 孤儿标记)验证 `bx up` 是否真的能自己恢复、以及 `bx down` 之后 `bx up` 是否不再被拒。设计 `docs/superpowers/specs/2026-08-11-ownership-uncertain-exit-design.md`、计划 `docs/superpowers/plans/2026-08-11-ownership-uncertain-exit.md`。**advisory 不再拉低总状态(`11338a0`,真机已验)**:`renderUpSummary` 原先 `len(rep.Warnings)>0` 就一律降级,于是一条 Tailscale 共存 advisory 让完全正常的 bx 显示 `Needs Attention`;现只有 `severity=error` 才降级(`hasBlockingWarning`;现存三条告警 tailscale/system_proxy/packet_tunnel 全为 `warn`),error 级仍降级由独立测试守住。真机实测 `Status Protected` + 告警仍单独占一行。 **观测路径真机已验(2026-08-05,本机 macOS,零改动)**:观测全程只读且 `bx status` 是独立 CLI 调用,故**无需重装、无需重启运行中的实例**——直接用新编的二进制问现有那套即可(`/var/run/bx/{core,guardian}.sock` 是 0666,连 sudo 都不要)。实测输出:`desired=on` / 信念 `protected` / 事实 `capture_ok=true(utun11)`、`dns_managed=true(127.0.0.1)`、`barrier_present=false`、`core_socket=true`、`tunnel_healthy=true`,**divergence 为空**——坐实了 `route -n get` 解析、`networksetup` 读 DNS、控制 socket 往返三条真机通路,以及「一致时必须安静」。**仍未验的是故障态**:divergence 能否抓到真实事故,要等真出一次(或人为造一次)才知道;第五条不变量(拆除永不拒绝)本期也未实现,故**不建议为了这一期去重装**——重装要停再起,而那正是把用户锁在断网状态的那条路径尚未修复的地方。**菜单栏常驻与 Open Status 去重(2026-08-06,UI 层,真机未验)**:菜单栏 LaunchAgent 的 `KeepAlive` 从裸 bool 改成字典形式 `{SuccessfulExit: false}`(**四处**生成器同步改齐:`unified_darwin.go` 的 `MenuAgentPlistText` + `install-macos-menu.sh`/`package-macos-menu.sh` 两个脚本 + **`apps/macos/BxMenu/Sources/BxMenu/InstanceGate.swift` 的 `menuLaunchAgentPlist`**,`35222f2`/`409fd3e`/`07bcb0c`):崩溃或被强退(非零退出)会被 launchd 拉回来,用户主动点 `Quit bx`(`NSApp.terminate`,exit 0)不会。**绝不能写成 `KeepAlive=true`**——那不区分退出码,会让 `Quit bx` 静默失效(点确认框、进程退出、随即被 launchd 复活,界面上看不出任何异常);这个区别不产生编译错误,由 `TestMenuAgentPlistTextRestartsOnlyOnAbnormalExit` 钉住,四处生成器必须保持字面一致,改一处忘改另一处不会被编译器抓到。**第四处是最容易漏也最致命的一处**:`InstanceGate.swift` 那份由 `main.swift` 的 `ensureLoginItemIfCanonical()` 在**每次菜单启动**时拿去与盘上 plist 比对、不一致就覆写,漏写 `KeepAlive` 等于每次启动都把装对的 plist 改回错的(launchd 本次会话仍用旧配置,故当场看起来正常,下次 `bx up` 或下次登录才发作);它一度还被 `InstanceGateTests.swift` 里一条反向断言(`!plist.contains("KeepAlive")`)钉成绿的,与 Go 侧断言方向相反而两边都过。现两侧断言方向一致,shell/Swift 三份生成器另由 `internal/install/menu_plist_generators_darwin_test.go` **读源码逐字比对**守住(设计当初把这两个脚本交给「由 review 保证」,而第四处生成器整个被漏掉正是 review 单独守不住的证据)。**`bx uninstall` 侧的连带后果**:菜单 agent 带 KeepAlive 后,一次失败的 `launchctl bootout gui/<uid>/com.getbx.bx.menu` 不再良性——job 留在 launchd 里而卸载紧接着删掉 `/Applications/Bx.app`,launchd 便每 ~10s 重拉一个已不存在的二进制刷屏 `menu.err.log` 到用户注销,而用户以为卸干净了;故 gui 域 bootout 失败会先按 `menuBootstrapCommand` 同款形式用 `launchctl asuser <uid>` 重试一次(`asuserBootoutFallback`),**重试再失败也只警告继续,卸载绝不中止**——「停止」不许依赖先成功做成别的事。同批(`fe0038b`)删掉了 `Open Status` 面板(`StatusPanel.swift`/`StatusRow`/`StatusSnapshot` 连同 `main.swift` 里的接线一并删),它显示的 5 项信息是一级菜单 7 项的严格子集,多一次点击换来的信息更少;`DNSPresentation`/`dnsPresentation` 不在这次清理范围内,原样保留。`Quit bx` 的确认文案(`quitBxConfirmMessage`,`UpdatePresentation.swift`,`b0f33a4`)现在以 `To start bx again, open Bx.app from Applications.` 收尾——菜单是普通用户唯一的非命令行入口,删完 `Quit Menu` 后这是找回它的唯一说明。`Quit Menu`(只关界面、保护继续跑)整个删除(`a29c34a`):它与"关菜单=关 bx"这条新不变量矛盾,且在 `KeepAlive={SuccessfulExit:false}` 下 `NSApp.terminate` 干净退出不会被拉回——留着它等于给用户一键做出"保护在跑但没有任何指示灯"的隐形状态。**已知后果(2026-08-08 已修,`f1bc1a0`)**:`main.swift` 里 `quitBxActionTitle` 一度只出现在 `.connected`/`.warning` 两个分支的菜单构建里,故 `.off`/`.setupNeeded`/`.missing`/`.notInstalled`/`.updateNeeded` 这几个状态下菜单没有任何退出入口,只能等下次登录随 launchd 清场。**现已改为在 `rebuildMenu()` 顶层无条件加一次**,所有 `BxState` 都有退出入口;`TestMacMenuQuitActionPresentInEveryState`(`internal/cli/cli_test.go`)按**函数体花括号深度**钉住「无条件」——只数出现次数抓不到「挪回某个 case 里」这种回退(挪进去之后次数还是 1),已用变异验证。**同一后果也落在安全恢复覆盖层**(`rebuildMenu()` 里 `recoverySnapshot` 非 nil 的那段:建完 header/Status/Recovery 与一个动作项就 `return`,根本走不到按 `state` 建菜单的 `switch`):恢复**正在进行**时那唯一的动作项还是禁用的 `Troubleshoot: Reconnect`,菜单一个可点的项都没有。此时保护通常开着,但恢复是过渡态(结束即回 `.connected`/`.warning`,退出入口随之回来),且恢复途中让用户关掉菜单本也不可取——记录在案,菜单代码不改。**注意这一条在上面那次修复后仍然成立**:恢复浮层与进度浮层都在按 `state` 建菜单之前 `return`,走不到那个无条件的退出项(进度浮层自带一个,恢复浮层没有)。观测面板(把 `bx status --json` 的 `observed`/`divergence` 做成"信念 vs 事实"对照贴进菜单)本期未做,留待真机攒够 divergence 样本后另立一期。**本条全部为 UI 层改动,真机未验**——没有人重装点过一遍。 **菜单栏开关免密 + 异步化(2026-08-07,阶段①,`c43867b`…`765ac9d`,真机未验)**:菜单原先经 AppleScript `do shell script … with administrator privileges` **同步**跑 `bx up`/`bx down`——既每次弹密码,又阻塞主线程(8-04 那次 down 卡 71 分钟,菜单跟着冻了 71 分钟、一个字都没有)。现改为菜单以普通用户身份连 Guardian socket。**授权改动精确到一个函数**:`mutationHandler`(服务 `/v1/up` 与 `/v1/down`)改用 `authorizeOwnerPeer`(原 `authorizeRecoveryPeer`,判据 `got && (uid==0 || (ownerUID!=0 && uid==ownerUID))`)——该判据**早已存在且已守着 `/v1/recoveries`**,而路径恢复会重装路由、同样是改网络的操作。`updateHandler`/`migrationHandler` **保持 root only**,由 `TestLocalAPIUpdateAndMigrateStayRootOnlyEvenWithOwnerConfigured` 钉住(既有的 update/migrate 测试用 `NewLocalAPI(controller)` ⇒ `OwnerUID=0` ⇒ 抓不到放宽,这条守卫是真覆盖不是重复)。`ownerUID==0` 时判据退化 root-only,故 `TestLocalAPIMutationsRequireRootPeer` 原样通过、未放宽任何断言。**已接受的安全后果**:无 Developer ID 时无法把权利绑定到 Bx.app(SMJobBless 那套要校验双方签名),故**该用户下任何进程都能静默开关 bx**;项目所有者在明确知晓后选择现在就做,待取得证书按 `2026-07-20-macos-unified-app-design.md` Phase 3 收紧。缓解是 `guardian_mutation_requested endpoint=… uid=… at=<RFC3339>`(授权后、`accept()` 前就写,故挂死的事务也留下发起人)+ `guardian_mutation_result …outcome/elapsed`;403 不记为发起,响应体一字未改(原始错误仍只进日志)。**整分支复审抓到三条,都是任务级复审结构上看不见的**:① **失败指引整套是死代码**——Guardian 的码骑在 500 上,而 Swift `decodeGuardianHTTPResponse` 在读 body 前就 `throw .status(500)`,于是 `toggleFailureHint` 永不可达、75 秒超时「为了拿到服务端的码」这个理由也落空;现先切 body 再判状态,码经 `GuardianClientError.status(Int, code: String?)` 到达菜单,**码缺席仍为 nil、绝不编造**。② **Turn Off/Quit 丢了强制拆除逃生口(本分支引入的退化)**——旧路径经 `bx down` → `macOSDownLifecycleDetailed` → `forcedMacOSTeardown`(其注释原文即 "the escape hatch"),socket 调用没有兜底;而 Guardian 死、Core 活时 `bx status` 恰好报 `needs_attention` → 菜单渲染 `.warning` → 该状态**提供 Turn Off 与 Quit,两者都死在 `connect()`**,且 `quitBx` 失败仍 terminate ⇒ 保护在跑而指示灯全无。现 turnOff 的 socket 失败回落到提权 `bx down`(经 `osascript` 在后台队列 spawn,唯一插值是编译期常量 `bxPath`);**turnOn 不回落**(没有「强制打开」,且把死 socket 升级成弹密码是骚扰);**全部路径都失败时 Quit 不退出**——退出会藏掉唯一的指示灯而保护还在跑,正是删 `Quit Menu` 时拒绝的那个隐形状态。③ spec 风险一节承诺的 uid 日志无人实现(即上文的 mitigation)。**一处方法论教训**:复审没信 Swift 测试的 fixture,而是真起 HTTP server 跑 `failureResponseBody` dump 线上字节,发现 fixture 与生产不一致(生产多一个 `\n`、多 `Date:`/`Connection:` 头),再把真实字节喂给解码器验证——生产通过,但**测试比生产弱**。**已知取舍**:turnOff 的逃生口对**任何** socket 失败都触发,包括瞬时 `guardian_busy`(并发 CLI `bx up` 占锁),会把「稍候重试」升级成整套强制拆除——比提示暗示的重,但方向与「停止优先」一致、结果仍是关掉。设计 `docs/superpowers/specs/2026-08-07-macos-menubar-redesign-design.md`、计划 `docs/superpowers/plans/2026-08-07-menubar-phase1-owner-auth.md`(阶段②菜单主体、③检测能力尚未开工)。**真机未验**:免密开关、后台队列弹出的授权框、日志落盘,全部只有单测与源码守卫;`main.swift` 是 AppKit、测试脚本编不了,故其接线只能由 Go 侧读源码文本的守卫测试钉住。 **菜单主体重做(2026-08-08,阶段②,`8939edd`…`0abdb0a`,真机未验)**:**图标状态编码在轮廓,不在颜色**——`MenuIcon.swift` 四形态(实心/空心/**虚线**/**沿中线裂开**)+ 两种呼吸(稳态 4s 只动透明度、过渡态 1.5s),`MenuIconTests` 钉住**去掉动效后四形态仍两两可分**(这条是后补的:初版 `.protected` 与 `.transitioning` 同为实心、只靠周期区分,开启「减弱动态效果」后完全同形,故给过渡态另立 `.dashed`)。**spec 初稿的前提是错的**:曾写「菜单栏图标是 template image,颜色不由我们做主」,而 `compactStatusImage` 一直是 `isTemplate=false` 自填 `systemGreen`——颜色一直能用,选形态编码的真实理由是用户要求 + 形态不随菜单栏高亮反色、不受色觉障碍与小尺寸影响。**无色两态(off/transitioning)改走 template 让系统上色**,且必须用**不透明黑**绘制:template 只取 alpha,沿用 `secondaryLabelColor` 会让蒙版峰值只有 0.498(实测),在深色菜单栏上那条 1.35pt 描边会消失——而那正是「保护没开」最不能看不见的状态。**数据行三态**(`MenuRows.swift`):`ok`/`bad`/`unknown`,**只有 `bad` 计入 `anomalyCount`,而它驱动图标裂不裂**——把「没问出来」算成异常会让图标无缘无故裂开;阶段③才有数据的三行(Exit Location / IPv6 Leak / WebRTC)**在场但标 unknown**,数据到位即点亮,无需重构。**轮询按菜单开合调频**(开 2s / 关 30s)。**整分支复审推翻了这一期最初的核心卖点**:轮询改动一度是**净退化**(5s→30s)——默认模式 `Timer` 在 NSMenu 追踪时实测触发 **0 次**(对照 `.common` 9 次),且 `rebuildMenu` 新建 `NSMenu` 而 AppKit 早已捕获旧对象,两条保鲜机制都够不着用户正在看的菜单;现改为**就地 `removeAllItems()` 重填 + 所有定时器挂 `.common`**,`menuNeedsUpdate` 只从缓存重建、采集移到后台队列。**该异步化引入并已修掉一个假红**:采集窗口最长 5s,期间主线程写入 `recoverySnapshot` 的三处会被陈旧值覆盖,可让已成功的恢复复活成 `Reconnect Failed`;现由 `RecoveryGeneration`(两个载体用 `didSet` 自动 bump,逐处手写会被漏掉)在写回前比对代际,只丢快照那一半。`runBx` 改为**先并发排空 stdout+stderr 再 `waitUntilExit`**——今天输出只有几 KB 远不及 64KB 管道上限,修的是**后果**:死锁会把 `refreshGate` 永久锁死、菜单静默停更。**「退出 bx」恒在,但恒在会造出新陷阱**:`.notInstalled`/`.missing`/`.setupNeeded` 里没有任何东西可关,而阶段①刻意规定「关不掉就不退出」,于是退出在刚变可见的那几个状态里必然失败;现由纯函数 `quitPlan` 判定,这几个状态直接 terminate。**`.off` 按证据一分为二**:`menuProtectionVerdict==.off` 是**信念**(Guardian 在应答,可能陈旧 30s)→ 仍走关闭;`diagnoseStopped` 的 `service_active != ok` 是**刚观测到的事实**(socket 已静默 + launchd job 未加载,一次刷新里两条否定观测)→ 直接 terminate——合并时会在什么都没跑的状态下弹出意外授权框,用户一取消就断言 `"bx Is Still Running / would leave protection running with no indicator at all"`,**一句假话**。**方法论教训两条**:① 复审把四种形态**实际光栅化**测墨量(`.dashed` 与 `.hollow` 有 46–50% 墨迹不同,16pt 可分)、**实测**定时器在 eventTracking 下的触发次数、**量**了 template 蒙版 alpha——都不是从代码推断的;② **同一机制修一处漏两处**:`.common` 模式先只修了刷新定时器,呼吸与「正在断开 N 秒」秒表仍冻结,而那个秒表正是阶段①为 71 分钟事故做的东西,现加了**类级守卫**禁止 `main.swift` 出现 `Timer.scheduledTimer`。**菜单文案统一为英文**(行标签、进度、失败指引),CJK 守卫禁止用户可见串再混入中文(注释不受限);「未观测」译 `Not checked` 而非 `Unknown`——那一态的语义是「没去问」,`Unknown` 会把它与「问了但不明」重新混为一谈。**⚠️ CI 从不跑 `swift build`,也不跑 `scripts/test-macos-menu.sh`**(`ci.yml` 只有 `go vet`/`go test`/`go build`/`bash -n scripts/*.sh`,最后一条只查语法不执行)——**本项目 17 个 Swift 套件与整个 macOS App 的编译,一次都没进过 CI**,Swift 侧写个编译错误 CI 照样绿。故 `internal/cli/cli_test.go` 里读源码文本的守卫是**唯一真正在 CI 里跑的 macOS 保护**,也因此承担了远超设计意图的重量:最后一轮复审在它们身上找出 5 个洞(只查函数存在不查函数体、不钉比较方向、不钉判定式、注释剥离不认块注释)。**用文本匹配去守编译器本该守的东西,天然是漏的**。**已补(`ci.yml` 的 `macos-app` job)**:`swift build --package-path apps/macos/BxMenu` + `bash scripts/test-macos-menu.sh`,两步不可互相替代——前者只编 SwiftPM target(`Sources/BxMenu`),而 `Tests/` 下的文件不属于任何 target、只由脚本用 `swiftc` 直编直跑。另断言收尾横幅 `macOS menu tests passed` 确实打印过:实测「脚本提前 `exit 0`」这一种失败**退出码是 0**,只靠退出码会一路绿灯(与 `integration` job 断言 netns PoC 真跑过同理)。三种失败方式(编译错误/断言失败/脚本被清空)均已变异验证。设计 `docs/superpowers/specs/2026-08-07-macos-menubar-redesign-design.md`、计划 `docs/superpowers/plans/2026-08-07-menubar-phase2-menu-body.md`(阶段③检测能力未开工)。**控制面架构诊断与阶段①(2026-08-08/09,`63f9878` 起共 10 个提交,真机未验)**:一整轮开发(菜单栏重做、升级流程、hosts 覆盖)结束后统计——**数据面 0 个 bug,控制面全部**。根因一句话:**CLI 不是客户端,是第二个控制面;菜单是第三个。**`bx up`/`down`/`app-install` 自己跑 networksetup、bootout launchd、写状态文件、做拆除,Guardian 也做这些(同一系统两个平等的改动者靠约定协调);菜单每几秒 spawn 两个进程、解析 stringly-typed 输出、维护自己的八态状态机,**甚至用 `bx logs --help` 解析帮助文本来判断 CLI 支不支持某 flag**。持久化状态 6 份、改系统状态的文件 `internal/cli` 7 个。这解释了本轮每一类 bug:「升级欠条该谁清」纠缠三轮(CLI 与 Guardian **都合法地**是「关掉保护」的那个人)、「装了新版但跑的还是旧版」(文件由 CLI 换、进程由 launchd 管、没人负责让两者一致)、以及孤儿屏障/孤儿启动标记/DNS 残留/升级欠条——**每一个都是手写的补偿逻辑,补的是同一件缺失的东西:没有调谐循环**。对照 Tailscale/systemd/Kubernetes/wg-quick,共同点两条:**状态只有一个主人;status 是推导出来的,不是记住的**——bx 两条都违反。`internal/observe` 是本项目自己发现的那条洞见(不信记账、去问系统),但它被**并排**装在信念旁边而不是取代它。目标架构:**生命周期(up/down/status/reconnect)归 daemon,CLI 与菜单是瘦客户端;安装/卸载/强制拆除留在 CLI——因为它们必须在 daemon 不存在时也能工作**(这条是 08-04 那次 71 分钟事故换来的,不可退让)。三步走,每步单独可发布可真机验证。**阶段①(菜单变瘦客户端)已完成**:菜单轮询路径 spawn **9→0**,改直连 Guardian socket 取结构化状态;Guardian `/v1/status` 聚合 Core 运行时(新 `CoreRuntime`,带 `Reachable` 把「问不出来」与「答案不好」分开),新增 `/v1/update-check`(`authorizeOwnerPeer`,**本地 socket 上唯一一个能让 root 守护进程发起出网请求的端点**,20s 上限 + 1h 缓存 + 串行化,失败只回 503、绝不退化成 available:false、失败不进缓存),能力由 `Status.Capabilities` 声明(**刻意无 omitempty**——键缺席是「旧版 Guardian」的唯一信号,由反射钉住结构体 tag)。唯一保留的 spawn 在**动作路径**:`ensureCLIUsable` 的 exec 探测(「装了但跑不起来」只有真执行一次才知道)。**本阶段最值得记住的不是改动本身,而是守卫被攻破的次数**:`main.swift` 编不进 Swift 测试套件,里面的逻辑只能靠 Go 侧文本匹配来守,而这类守卫在本阶段被**攻破八次**——固定字节窗口被邻近函数满足、只查存在不查谓词、只钉常量不钉判据、行为一致的重实现、分支映射错、断言被**代码自己的解释性注释**兜绿、`Process.init()`/`Process ()`/`popen()` 三种拼法绕过字面量匹配、以及在兄弟文件里写一句 `typealias CommandRunner = Process` 让整条链的证明失效。教训是同一条:**守卫要禁语义,不要禁拼法**——禁「判据出现在状态推导里」而不是禁它的某个落点;锚在类型上而不是调用的字面写法;实参必须是光秃秃的一次取值(`core.answering ?? false` 以 `core.` 开头、全绿,却把三态压回二值,与直接写 `false` 是同一个事故);并且**守卫读不懂现在的代码时必须 `t.Fatal` 响亮失败**,而不是静默放行。根治办法已记档:把 `BxState`/`resolve()` 搬进 harness 可编译的 `MenuState.swift` 成为纯函数,`main.swift` 只剩 spawn/dial/Bundle/AppKit。**阶段②(`bx up`/`down` 变纯 RPC,逃生口显式改名 `bx force-teardown`)与阶段③(调谐循环,desired 与 observed 持续比对,status 直接发布 observed;届时 upgrade-intent.json、孤儿屏障清理、进程扫描、DNS 残留检查、believed/observed 并列这五条手写补偿可逐个删除)尚未开工**,各自应有独立 spec 与 plan。设计 `docs/superpowers/specs/2026-08-08-control-plane-architecture-design.md`、计划 `docs/superpowers/plans/2026-08-08-menu-thin-client.md`。**控制面架构第二轮:先建集成台(2026-08-09,`fe49c9c`…`405b2f2`,Linux 容器内已验)**:多服务器做到 Task 6 时被叫停 —— 一轮 11 个 finding 里 **6 个可追到设计而非实现**,而两个 Critical 都住在 `Run()` 的六行接线上。量化诊断:**控制面:数据面 = 28904:3088 = 9.3 倍**;`Run()` **578 行**(组装根,单测进不去);真跑系统的集成测试只有 3 个且只测原语。数据面本轮 **0 bug**。**根因是这个代码库没有落脚点**:组装根不可测,于是「逻辑对」与「接线对」是两件事,而所有事故都在后者;补救手段是读源码文本/AST 的守卫,**本轮被绕过 8 次**(拼写而非身份、邻近注释满足、行为等价的重实现、看起来像缓存优化的重赋值……)。第二条同样致命:**失败是静默的**——错的 bypass 集合产生的信号是没有信号(隧道连得上、status 显绿、流量绕圈或泄漏)。**新顺序**:① 建集成台 → ② 阶段② up/down 变纯 RPC → ③ 阶段③ 调谐循环 → ④ 回头做多服务器 Task 7-10。**① 已完成**:`internal/supervisor/harness*_netns_linux_test.go` 在一次性 **net+mount** namespace 里跑**真正的 `supervisor.Run()`**(真 TUN、真策略路由、真控制面、真屏障;只有建隧道经 `Options.BuildTunnel` 注入缝换成假的——本仓库唯一一处「组装根可外部指定」的缝)。**五条断言全部打在内核状态上**:bypass 覆盖每台的两条链接、已知服务器间切换不动任何路由、**新服务器的 bypass 必须在传输换过去之前装好**、解析失败必须拒绝切换并原地不动、屏障开口是**闭集**(只含清单里各台的传输地址)。**每一条都用本轮真实发生过的 bug 变异验证过会红。**开发机 macOS 跑不了 netns,故 `scripts/run-netns-tests.sh` = 宿主交叉编译 + 本机 Colima 的特权 busybox 容器(不拉大镜像,二进制须落 `$HOME`——macOS 的 `/tmp` 不在 colima 共享范围)。**建台过程本身又踩了同一类坑五次,值得记**:① brief 里的**线程级 `unshare` 是错的**(按线程生效而 `Run()` 重度并发,实测 50/50 goroutine 落在外层;真跑那版会劫持外层网关、删掉外层 `core.sock`)→ 改整进程 re-exec;② 隔离守卫**只查 net 不查 mnt**,去掉 `CLONE_NEWNS` 照样 PASS 而把外层 `/run` 用 tmpfs 盖住;③ **隔离参照信的是父进程写的环境变量**,伪造一行即绿 → 改问 `/proc/1/ns/{net,mnt}`(不可伪造)——**与 `internal/observe`、Core 所有权进程扫描同一条原则:不信自己的记账,去问内核**;④ `ipQuiet` 把错误折进字符串,只对正极性断言安全,而屏障那条是**反极性**(「不得含用户 hosts 覆盖」),观测一失败就「什么都没找到」= 绿灯 → 改 `(string, error)` 分开存(**观测不到 ≠ 观测到没有**,即 `Tristate` 那条原则);⑤ **before/after 快照在结构上看不见顺序**——两种顺序终态相同,差别只是成环发生的那个瞬态窗口 → 观察点挪进窗口(假隧道工厂正是在 `swapTo` 途中被调用)。**台子还抓到一个单测抓不到的生产问题**:切到解析不出的服务器时,可操作的拒绝原因在服务端产生后被客户端 3s 超时丢掉(服务端刷新期限 5s),用户只看到「超时」——违反「失败必须留下可操作线索」;今天 `SetServerControl` 无生产调用方故不可达,是给将来接服务器切换 UX 的人留的坑,修法参照 `ReconnectControl`。**CI**:`integration` job 逐个测试名断言那条带 `  | ` 前缀的子进程 `--- PASS`(台子 re-exec 后顶层打印的是 `--- SKIP`,锚 `^` 的写法实测匹配 0 行),`rc==0` 与逐名 grep 缺一不可。被台子接手的 **3 条 AST 接线守卫已退场**;`TestRunWiresPathRecovererToLiveBypassStore` **留下**——`livePathRecoverer` 只在平台实现 `Underlay()`(全仓仅 darwin)时构造,netns 上恒 nil,注释里写明了删它的前提。**台子的盲区必须记住:它只覆盖 Linux 控制面,macOS 主平台不在内**,「真机未验」清单不因它存在而清空。设计 `docs/superpowers/specs/2026-08-09-integration-harness-design.md`、计划 `docs/superpowers/plans/2026-08-09-integration-harness.md`;多服务器 Task 1-6 的代码**不删不回退,就地停**(设计 `docs/superpowers/specs/2026-08-09-multi-server-design.md`)。**阶段② + 「`bx down` 不许撒谎」(2026-08-09,`4d8a558`…`162788a` 共 21 个提交,真机未验)**:**阶段②**把停止路径上的启动工作摘掉——`cleanGuardianDown` 曾先调 `ensureGuardianOwnership`(shell 出去 `launchctl print` 查 legacy Core;legacy 在跑时还要网关发现 + 读 Core `/v0/runtime` + 解析 config + DNS 查询),**任何一步报错都把一次本可干净完成的停止升级成拆屏障、还原 DNS 的重手术**。(更正记录:架构文档原说「`down` 会**静默**落到强制拆除」与「`down` 会安装启动 Guardian」,**两条都被实测证伪**——回落会逐条打印做过的动作并拒绝断言「网络已还原」;而 `guardianEnableCommands(active,ready)` 在两者皆真时返回 nil,故健康机器上那步是空操作。真问题是上面那条,以及「空操作是巧合不是契约」。)**逃生口取得自己的名字 `bx force-teardown`**,但 **`down` 的自动回落刻意保留**——搬走它会削弱不变量而非加强:用户在最需要关掉保护的时候,最不可能知道该换个命令。**「不许撒谎」这条弧是本轮最重要的产出**:`Manager.Down` 此前从**自己的记账**推出「没有 Core」——`Existing()` 读 `/var/lib/bx/core-process.json`,文件里没有就返回 PID 0,于是 `runner.Stop` 整个被跳过、装屏障、写 desired=off、还原 DNS、**报成功**,而那个 Core 连同 TUN 和路由一动没动。**这个推理至少有四种反例**:legacy Core(旧 launchd unit 起的)、`sudo bx run`(项目自己文档化的调试路径)、`core-process.json` 丢失或陈旧(**手删它曾是本项目文档推荐的应急手段**)、Guardian 重装窗口。**CLI 侧修不掉**:探针只覆盖四种里的一种,而菜单栏的 Turn Off 从阶段①起直接 `POST /v1/down`、一行 CLI 都不经过。**判断必须发生在持有事实的那一侧**,而 Guardian 早有那个原语——`scanRunningCores` 按 `basename(argv[0])=="bx" && argv[1]=="run" && uid==0` 判定,**与谁拉起它无关**,当初就是为「向系统现问,不信记账」写的,只是从没在 `Down` 里用过。现在 `Manager.Down` 的**两条**出口 + **每次 Guardian 重启都跑的 `Recover`** 都先求证;求证不过发 `ProtectionNeedsAttention`(已有状态,CLI 与菜单都认),reason 区分 `core_still_running`(确知还在)与 `core_scan_failed`(问不出来)。**两条不变量在「问不出来」时冲突,出口是第三个状态**:「停止永不依赖别的先成功」(71 分钟事故)要求扫描失败**不许**让 `Down` 报错;「绝不在保护还开着时报成功」要求扫不出来**不能**说 off——于是**不失败、也不撒谎**,如实说没能确认。**`Recover` 那一跳有个不能照抄的地方**:它成功时会清 `recoveryBlocked`,而该标志为真时 `Manager.Down` 第一句就返回 `errRecoveryIncomplete`——**正是 71 分钟事故的机制**;新分支若顺手把它留成 true,就是用一条新不变量把老不变量撞碎(测试专门钉住「没能确认之后 `Down` 仍可达」)。**panic 收在 goroutine 源头而不是逐个函数堵**:`Down` 经 LocalAPI 的 HTTP handler 进来、`net/http` 会兜住;而 `Recover` 跑在 `trackStartupRecovery` 的裸 goroutine 里,一次 panic 打死 Guardian、launchd `KeepAlive` 拉起来再 panic——**崩溃循环**;`recoverLocked` 有**三个**入口走到 `scanRunningCores`(`confirmCoreStopped`/`runner.Existing`/`runner.Start`),只堵一个是「修了一个实例、看起来像修了那类问题」。**三个消费方也要跟着改**,否则 Guardian 诚实了而下游按老规矩读:`downReportLines` 从不读 `result.Status`(菜单读 `protection_state` 所以生效、CLI 没有);进度行 `stepDone` 无条件打「✓ 已停止,网络已恢复」与紧接着的「没能确认」两行相邻打架;升级路径按 `Forced || err != nil` 判断,`200-with-needs_attention` 两者都不满足 → 照常换二进制而不受管的 Core 还占着 TUN;菜单 `performToggle` 在 HTTP 200 就判成功而退出决策用的正是它。**「扫不动」不许把送修复的通道堵死**:升级只在**确知**还有 Core 时中止(扫描失败则警告并继续)——否则重跑撞同一道闸门,而菜单 Repair 走的正是 `app-install`。**本轮反复出现的失败形状(比代码值钱,建议每次改动前读一遍)**:① **「这条路能做到 X」听起来显然,但没人去看它是不是真的做得到**——我开的修法「legacy 在跑就走强制拆除,那条路能停掉它」漏了 legacy plist 带 `KeepAlive: true`,请它退出 launchd 立刻拉回来,**比改动前更糟**(原 `Migrate` 路径会 `BootoutLegacyCoreUnit` 解除 KeepAlive 再 `Remove`,是永久处置,我用临时处置换掉了它);② **改动落在旁边而不是那条路上**——`confirmedOff` 只接到排队退出那个罕见分支,常规点击 Quit 走的是另一行,**声称修掉的状态原样还在且无测试会红**;③ **同一个 bug 在语义/机制/数据三个高度各长一次**——那句「已停止」被消灭两轮之后,又**以零值 `macOSDownResult{}` 的形式长回来**(`downReportLines` 对零值渲染的正是它);④ **测试测的是相邻的东西**——查 `err != nil` 而缺陷是「报错时返回了什么」;查字段存在而缝已经死了;查 `bx status` 而那正是刚被判定的死胡同(**守卫在钉住缺陷本身**);⑤ **变异测试自身会假阴性**——插错位置(插在赋值之前)让一条真守卫看起来没用,断言写在 `if failures == 0` 判决**之后**让它的失败不计入退出码(后者我一度误判成「17 个 Swift 套件的失败在 CI 里全看不见」的重大漏洞,**查根因才发现是自己插错**——验证之前不下结论对「发现问题」和「发现自己搞错」同样适用);⑥ **专门为「其它测试全是替身」写的 e2e 会变成空壳**——替身缺一个 `ScanRunning`,升级在第一步就中止、`installFiles` 一次没调到,而它每一条断言照样成立。设计 `docs/superpowers/specs/2026-08-09-stage2-pure-rpc-design.md` 与 `2026-08-09-down-must-not-lie-design.md`。**真机三条验收无人执行**:① 正常 `sudo bx down` 仍显示已停止(**最要紧——误报比漏报更可能毁掉整条修复**);② 另一终端跑着 `sudo bx run` 时 `down` 必须**不**报已停止;③ 同样情形下菜单 Turn Off 也应显示需注意(它读同一个 `protection_state`)。 **阶段③a:只观察的调谐环(2026-08-09/10,`3fe2df8`…`2bacfd0` 共 12 个提交,真机未验)**:控制面第一次有了**判断与执行分离**的结构 —— 纯函数 `decide`(`internal/guardian/reconcile.go`)把「用户要什么」×「系统实际是什么」×「三道栅栏」映射成一组**命名的意图**,一条常驻循环每轮调它、把「我本来会做什么」记进日志与 `bx status`,**一个动作都不执行**。授权留给③b 逐项开,而开之前要先有真机 soak 的数字。**为什么必须先只观察**:Guardian 只在 darwin 跑,集成台是 Linux netns 的,凡本期写「由测试保证」的对循环的**接线**而言都是「由替身保证」;唯一能拿到真实证据的办法就是让它先跑一段只说不做的日子。**动作是命名意图而不是函数指针**:日志读得懂、③b 能按名字逐项审、且 `decide` 因此是纯函数(与 `decideCoreScan`/`failoverPolicy.decide` 同一手法)。**没有「装屏障」这个动作,是刻意的**:装它要先探默认网关,瞬时失败会降级成无 server bypass 的 block-only = 整机黑洞且隧道无自愈通路;手写路径至少绑在一次用户显式请求上,放进按时钟驱动的循环就是每拍重掷一次骰子。**所有权不确定是栅栏、不是待收敛的差异** —— 它整个存在的意义就是「拒绝」,循环去消除它等于自动推翻一次刻意的 fail-closed。**本期最值得记的不是代码,是守卫被攻破的次数**:三个任务、六轮复审,抓到的**每一条 Important 都是「守卫绿得没道理」而生产代码是对的**,且形状高度重复 —— ①「不许有装屏障动作」写成了**只认一个字面名的黑名单**(加个叫 `reassert_barrier` 的动作,整套测试全绿)→ 改成白名单 + 穷举全部 3888 种输入;②「栅栏升起时不得同时产出动作」只写在注释里(把栅栏改成只对 `desired=off` 短路,`decide` 返回 `Held` 外加 `Actions=[start_core]`,整包全绿),且原三条用例全是 `desired=off`,而 `desired=on` 才是危险的那一半;③ 零动作断言守的是 `runReconcileLoopWithPacing`,而**生产唯一调用的是 `runReconcileLoop`**(往后者塞 `restoreDNS` 整包全绿);④「daemon 挂上了循环」的断言只证明 `reconcileLoopDone` 会关闭 —— **goroutine 体空转照样绿**(channel 一返回就关,`cancel()` 落在已关的 channel 上平凡成功),必须配一个「进过循环体」的正信号;⑤ 退避上限断言被 **int64 溢出**架空(去掉上限后 round 20 是 364 天、round≥54 回绕成 0s,而唯一那条探针用 `unchangedRounds=1000`,恰好落在回绕之后,一直为错误的理由通过);⑥ change-only 日志断言里每轮 `ObservedAt` 都是零值,分不清「只比决策」与「连观测一起比」;⑦ 报告在 `Manager.Status()` 里有、CLI 拿到能渲染,**没人证明它走完了 `/v1/status` 那一跳**(`observableStatus` 里插一句 `status.Reconcile = nil`,两个包全绿),而隔壁 `setStatus` 里就有一句刻意的同款赋值。**贯穿始终的教训:守卫钉住的往往是缺陷旁边的东西** —— 证明 channel 存在≠goroutine 跑过,证明接线在≠循环体执行过,证明字段在≠它到得了消费方。**「零值读起来像一切正常」是本期的核心危险,前后出现四次**:`reconcileDecision` 的零值恰好就是**一台健康机器**的判断(循环正是靠这一点让健康机器静默),于是「从没跑过一轮」「循环体空转」「报告在路上被丢掉」「三项探测全失败」在日志与 `bx status` 里与「一切正常」逐字节相同 —— 而 soak 的头号结论正是「0 次提议」。处置:`ReconcileReport.At` 是「跑过没跑过」的唯一判据(没跑过整个字段缺席、CLI 渲染「尚未完成第一轮观测」),`ObservedState.UnobservableItems()` 把观测质量折进变更比较(**变瞎本身就是一次值得打印的变化**),陈旧超过退避上限两倍标为陈旧,panic 收在**每一轮内**(原先 `recover` 在 `for` 外,一次 panic 就永久结束循环,而冻住的报告仍读作干净一轮)。**设计交付的第二样(`looksLikeCore` 的真机误报率,至今从未测量)一度落空**:循环压根不调它 —— `scanRunningCores` 只挂在三条**改动**路径上,循环读的是那些路径**锁存**的 `Uncertain`,而 `actionStartCore` 来自控制 socket 探测(`reconcile.go` 自己写明是另一个、会高估的信号)。现循环每轮**只读地**扫一次,答案只进报告不进 `decide`;`Measured=false` 与 `Cores=0` 严格分开(把「问不出来」记成 0 会让误报率算低,正是这份测量的反面)。**普查日志分标签**(`guardian_core_scan reason=lifecycle|observe`):循环稳态约 144 次/天且永久,而那行普查是**紧邻**放行日志的证据,不分标签会把「我允许了一个 Core 启动,因为我认为没有别的 Core 在跑」这条唯一的审计线索淹掉。**三个前置事实决定了 soak 怎么做**:① `decide` 在 `desired=on` 时**只有一条规则**(`CoreSocket`),而③b 最先要授权的 `restore_dns`/`clear_orphan_barrier` **只存在于 `desired=off` 分支** —— 全程开着保护跑一天对那两项零证据,soak 必须留一段时间把保护关掉(Guardian 在 `KeepAlive` 下继续跑,循环照常滴答);② 日志在 **`/var/log/bx-guard.err.log`**(`log.Printf` 走 stderr),且**变化时才打**,一次瞬时事件留两行(变糟一行、恢复一行),只数 `actions=` 非 `none` 的行;③ 周期退避到 **10 分钟**上限(稳态约 150 轮/天),短于 10 分钟的分歧整轮错过 —— 这个数字是**下界**不是全貌。**顺带解出的一个意外收获**:`held=ownership_uncertain` 现在出现在 `bx status` 里,那个锁存态第一次有了用户可见的窗口(**当时**它的出口写作 `sudo bx down && sudo bx up`;2026-08-11 之后用户发起的 `bx up` 每次都会重新求证,见「锁存已有出口」)。**本期不做也不该做**:不给循环任何执行权;不动升级欠条(删它要先有与 `desired` 正交的「维护挂起」数据模型,单独一期,且排在③b 之前);不解 `Uncertain` 锁存;不删 CLI 侧的孤儿屏障清理(逃生口按定义在 Guardian 已死时工作);不动 Linux/Windows 上 `bx status` 的形态(那两个平台一个观测原语都没有)。设计 `docs/superpowers/specs/2026-08-09-stage3-reconciler-design.md`、计划 `docs/superpowers/plans/2026-08-09-stage3a-observe-only.md`。**真机 soak 是本阶段唯一真正的验收,尚未执行。** **维护挂起:让 `desired` 只记录用户意图(2026-08-10,`d1abe81`…`f175e10` 共 25 个提交,真机未验)**:③b 的第一个前置。升级要停 Core 换二进制,而这件事此前是用**写 `desired=off`** 表达的——磁盘说「用户不想要保护」,而事实是**用户想要、只是此刻不能有**,靠一张欠条(`upgrade-intent.json`)记着回头打开。**任何忠实的调谐器读到 `desired=off` 都会收敛到 off,而那正是 bug 本身。** 现改为正交的 `/var/lib/bx/maintenance-hold.json`:自带 `schema_version`、带原因与 **15 分钟**过期、整文件原子写(两个进程写它且没有任何锁,安全**只**来自原子替换,绝不 read-modify-write)、读取时判过期(沿用 `internal/toolkeys` 那个唯一的持久化过期先例,不设定时器)。**绝不能加进 `guardian-state.json`**——那文件是个**裸 JSON 字符串**,没有信封没有版本,旧 Guardian 读不动 → `recoveryBlocked=true` → `Manager.Down` **永久**返回 `errRecoveryIncomplete`,正是 2026-08-04 那次 71 分钟事故的机制,而升级恰恰是新旧共存的时刻。**这一期真正的难点不是文件格式,是「谁会自己起一个 Core」**:测绘查出**五条**(不是计划最初写的四条)——① 干净 `Manager.Down`(靠 `m.current = Process{}` 让代际检查失败,**根本没读 `desired`**);② 强制拆除(**只有**那一次读盘拦着,`m.current` 在活着的 Guardian 里原样保留);③ **启动恢复**(`restartGuardianForUpgrade` 每次升级都停掉再拉起 Guardian,新 Guardian 起手就跑 `recoverLocked`——这条是写计划时才发现的,而它每次升级都走);④ `Down` 在 DNS 还原失败时的补偿重启;⑤ `recoverUpdateLocked`,**刻意不拦**(它先还原快照二进制再起,拦住会把没做完的 Guardian 自更新永久搁浅)。**「挂起武装 ⇒ Guardian 不起任何 Core」是假的**,别依赖。前四条都必须认挂起,漏一条就仍有一条把 Core 放回半换二进制的路。**`desired` 与挂起必须同源读**(`LoadIntentSnapshot`):内存里的 `Status.Desired` **已经会撒谎**——`needsAttention` 把调用方传的常量写进去,好几处传 `DesiredOn` 而磁盘写着 off;两者不同源,一轮之内就能出现互不相干的组合。「读不出意图」既不是 `on` 也不是 `off`,是第五道栅栏 `intent_unreadable`(与 `Tristate` 零值同一条纪律)。**退回规则**:挂起写失败就退回写 `desired=off` 并照常拆除——不是报错、更不是继续,因为「既没挂起也没 `desired=off`」是一个**新的**失效模式(活着的 Guardian 在换二进制时重启 Core);**宁可退回一个会撒谎但安全的状态,也不要一个诚实但没人拦着的状态**。**用户的显式 up/down 无条件清挂起**,且清挂起**对拆除的成败无条件**——欠条今天就栽在这:`macOSDownAction` 报错时提前返回跳过销账,而强制拆除**六步破坏性动作已经做完了**,于是留下陈旧欠条 + `desired=off`,下一次 `app-install`(菜单 Repair 带 `--yes` 跑它)**违背用户明确的关闭请求把保护打开**。**发布面**:`Status.MaintenanceHold` + `CapabilityMaintenanceHold`(与 `CapabilityReconcileReport` 同机制,`Capabilities` 刻意无 `omitempty` 以区分「声明了但没有」与「这版压根没声明过」)、`observe.Diverge` 认挂起、`bx status` 与菜单栏渲染,**`hold == nil` 意味着「这版 Guardian 没有挂起这个概念」,不是「没有挂起」**。**过期不恢复保护**,它买到的只是「不再压制」——但 `desired` 一直是 `on`,于是 `bx status` 会报一条**真实的分歧**:**欠条让机器看起来是关的,挂起让它看起来是坏的——而它确实是坏的。** **整枝复审抓到的 Critical 值得单记**:**过渡升级(新 CLI × 旧 Guardian)会丢掉用户的意图**——设计的全部论证「停机只武装挂起、一个字节不动 `desired`」只在**新** Guardian 服务那次 `/v1/down` 时成立,而第一次升级时跑着的是旧的,它的 `Down` **无条件** `SaveDesired(DesiredOff)`(`?reason=upgrade` 在那一版只保住欠条、从不抑制那次写),而欠条已在同一分支删掉。于是盘上是一句**自洽的假话**:`desired=off`、无挂起、无欠条——`Diverge` 一个字都不说,`bx status` 看起来完全正常,而重跑时「恢复保护」那一步直接从计划里消失、升级仍报完成。修法是能力门控的重新断言(服务停机的那个 Guardian 没声明 `maintenance_hold` 就在装文件前把 `desired=on` 写回),由走真实 socket 的 e2e 坐实「重跑真的恢复了保护」。**可诊断性是本期的显式交付**(用户原话「做好日志记录等,有问题我们排查」):挂起的一生留痕——`guardian_maintenance_hold_armed/_expired/_cleared by=`、`guardian_startup_recovery_held`、`guardian_core_exit_under_hold`、`guardian_legacy_upgrade_intent_migrated/_stale/_migrate_failed`。**武装与强制拆除的清除按构造到不了 Guardian 日志**(武装在 CLI 进程里、强制拆除第 3 步就 bootout 了 Guardian),故改为从 Guardian 侧**观察**而不是让 CLI **声称**——更强的证据,代价是「若 `ArmMaintenanceHold` 失败,Guardian 从没见过挂起、于是什么都不写」,这条仍是缺口。**方法论上这一期最值得记的**:**计划里预先写好的测试,每个任务都有假绿**——Task 1 三条(坏 JSON 那条抓不到读错误坍塌;过期测试两边都用同一常量、把 15 分钟改成 5 分钟照样绿;卸载断言用常量自比)、Task 6 两条(**根本跑不起来**,`SaveDesired` 要求五个路径全给,在 setup 就死了)、Task 7 一条(**恒红**,判据 `strings.Contains(body,"maintenance_hold")` 而能力名字字面就是它)、Task 8 三条(用 `CoreSocket==False` + `Errors==nil` 这个**生产环境造不出来的输入**,而 `observeCore` 里那个值永远伴随一条 `ObserveError`)。**换成真实 fixture 后,同一个缺陷让两个包四条测试转红——那就是「fixture 真实性是承重的」的证据。** 另有三处「证明方式不对而代码是对的」:三个强制拆除入口的覆盖是**读**出来的(换成零值 `stopIntent{}` 整套全绿,而 `downPurposeUser = iota = 0`——**错误答案恰好是零值**);旗舰 e2e 在接线前一路走**降级**路径还报绿;`writeJSONAtomically` 的原子性测试随 `SaveUpgradeIntent` 一起被删,而它守的是底下六处共用的原语——**删测试时该问的不是「还有没有人引用它」,而是「它证明的那件事现在还有没有人证明」**。**CI 两个结构漏洞同期发现并补上**:① `Tests/` 下的 Swift 文件不属于任何 SwiftPM target,漏登记进 `scripts/test-macos-menu.sh` 就**一次都不跑而 CI 全绿**(实测:套件数 17→16、脚本照样打印收尾横幅并退 0),补了 Go 侧的文件清单守卫(查的是清单不是语义,故文本匹配恰当;读不到脚本时**必须响亮失败**);② **`internal/cli` 自 2026-08-09 起在 ubuntu/windows 上就编不过**(`d8f1d9c`,早于本分支),于是所有无 build tag 的新测试在三条 CI 腿里的两条**从未跑过**。**(2026-08-24 实测:这条已经不成立了 —— `GOOS=linux` 与 `GOOS=windows` 下 `go vet ./internal/cli/` 与 `go test -c` 都通过。什么时候被谁修好的没查,但记档不能继续说一件已经不真的事;仓库级守卫因此可以住在 `internal/cli`,三条 CI 腿都会跑到。)**设计 `docs/superpowers/specs/2026-08-10-maintenance-hold-design.md`(含「写计划时暴露的六条规格缺陷」一节,**以那节为准**)、计划 `docs/superpowers/plans/2026-08-10-maintenance-hold.md`。**真机未验**:整枝复审的结论是「可以装,但要有人看着」;第一次升级要盯的三件事——`sudo tail -f /var/log/bx-guard.err.log` 应依次出现 `_armed reason=upgrade` → `guardian_startup_recovery_held` → `_cleared by=up`(第三条不出现就是保护没恢复,趁 15 分钟没走完立刻查盘上的 `desired`);开始前先 `cat /var/lib/bx/upgrade-intent.json`(存在且不足 24 小时的话,新 Guardian 会在中途把 `desired` 翻成 `on` 并武装一个 `legacy_upgrade` 挂起);升级后比对 `bx status --json` 的 `desired` 与 `cat /var/lib/bx/guardian-state.json`。 **真机验收(2026-08-10 夜,项目所有者的 Mac,`dev-bc5ff62`)**:阶段③a、维护挂起、锁存出口三期堆在一起第一次上真机,**验掉五样、抓到一个真 bug**。装法:`bash scripts/package-macos-release.sh` 出 `dist.noindex/release/bx-macos-arm64/`,再以**普通用户身份**跑 `./install.sh`(不要 sudo,脚本自己请求授权);`sudo /tmp/bx app-install` 会失败,因为 `app-install` 要的是一个完整的 `Bx.app` bundle 而不是裸二进制。**验掉的**:① **菜单栏免密开关**(2026-08-07 那一期标着「真机未验」的东西)—— `guardian_mutation_requested endpoint=/v1/down uid=501` → `outcome=ok`,两个方向都不弹密码框,`authorizeOwnerPeer` 真机可用;② **阶段③a 调谐环** —— 首行 `guardian_reconcile_would actions=none held=none core_scan=1 observed=capture=true …`,**之后一直静默**,change-only 日志按设计工作,退避实测 30→60→120→240 秒;③ **`looksLikeCore` 的误报率**(设计里标了「从未测量」的那个数)—— 保护开着 `cores=1`、关掉 `cores=0`,约 8 个采样全对;④ **普查分标签** `reason=lifecycle|observe` —— 准入那两次是 lifecycle、循环每轮是 observe,当天上午刚改的东西在第一次真机运行就派上用场(不分标签,「我允许了一个 Core 启动」那条审计线索会被循环噪声淹掉);⑤ **`readable/enumerated ≈ 766/767`** —— 扫描的「看得见」假设不是勉强成立,而是完全成立;另外 down 只有**一次**扫描(`confirmCoreStopped` 第一次扫干净就返回,刻意的偏置)、没有 `ownership_uncertain_recheck`(没有锁存就不重新求证)。**抓到并修掉的 bug(`7288e63`)——全新安装之后 Guardian 从来不被拉起来**:`upgradeSteps(guardianRunning=false, …)` 只返回 `[InstallFiles]`,于是安装写了 plist 却从不 bootstrap 它,`guardian.sock` 要等第一次 `sudo bx up` 才存在(实测:装完 22:13:23、菜单 22:13:33 起来、socket 22:14:57 才出现)。**后果是菜单栏第①期的免密开关在全新安装这条路上从没能工作过**,而那一期的全部目的就是它;升级路径一直没这个问题,因为 `restartGuardianForUpgrade` 会 bootout 再 bootstrap —— 三期开发六轮复审都没碰到,**只有真机全新安装能暴露它**。而且它**三处都不留痕**:Guardian 没起来所以没日志、菜单自己不记切换失败、403 按设计也不记(这次还不是 403,是根本连不上)—— 一次失败的菜单开关在真机上完全不可见。修法带前置条件:daemon 启动要读 `/etc/bx/config.yaml` 取 `owner_uid`,读不出就退出,而 plist 带 `KeepAlive=true`,**硬 bootstrap 一个起不来的 Guardian 等于让刚装好的机器每秒重启它一次而安装报告说完成**;而「还没跑过 `bx setup`」正是全新安装的常态,不是边角情况。故 `upgradeSteps` 多一个 `configUsable` 入参,判据与 daemon 那一跳**同源**(同一个文件、同一个解析器),两边漂移就是那个崩溃循环。判错的代价不对称,缺省走保守那边(测试替身没设这个钩子时按不可用处理,而不是 panic)。**仍未验**:维护挂起(要一次真升级才触发)与锁存出口(要人为造一个锁存)。**顺带修掉一条从来没生效过的守卫(`0e16536`)**:`scripts/verify-macos-release.sh` 里 `grep -qF '--yes|-y) …'` 的模式以 `--` 开头,grep 把它当自己的选项、打一屏 usage 后非零退出,于是 `|| fail` 恒真 —— 那条守卫**一次都没有真正比对过内容**,而 verify 也因此从来跑不完。加 `--` 分隔符后 verify 首次通过。
- **Windows**:**第 1/2 步 + 第 3 步的 OpenTUN/DirectDialer/Hijack 已做**——交叉编译通过(`embedded_other.go` brook 兜底、`paths_windows.go`)+ CI 三平台矩阵(ubuntu/macos/windows runner 跑单测,全绿)。真机环境已就绪(SSH 提权可达,`bx.exe` 已执行)。**第 3 步进度**:① **OpenTUN**——wintun `CreateTUN`→`wgbridge`(照抄 darwin,去掉 utun 命名限制,`bx0` 直用;回填适配器 `LUID` 供 Hijack;运行时需签名 `wintun.dll` 同目录);② **DirectDialer**——`GetBestInterfaceEx` 探物理默认网卡 index → `IP_UNICAST_IF`(v4)/`IPV6_UNICAST_IF`(v6)防环,复用共享 `shouldBindToDevice`「仅公网目的地才绑」。头号坑 IPv4 字节序(`htonl(index)`,MSDN 怪癖)抽到 `unicastif.go` 的 `unicastIfV4Value`(纯逻辑 `bits.ReverseBytes32`)TDD 覆盖;③ **Hijack**——用 **winipcfg**(`wireguard/windows/tunnel/winipcfg`,包级依赖实际只 x/sys,GUI 依赖不编译)拿 TUN 的 LUID 配地址 + split-default(`0.0.0.0/1`+`128.0.0.0/1`)劫进 TUN,server/私网/SSH bypass 经**物理默认网关**(`physicalDefaultRoute` 从 `GetIPForwardTable2` 取 metric 最低的 `0.0.0.0/0`)旁路;**IPv6 fail-closed**(宿主有 v6 时把 `::/1`+`8000::/1` 劫进 TUN;域名维度 v6 已由 DNS `AAAA→NODATA` 堵死,此为字面量 v6 纵深防御,best-effort 不连累 v4)。纯路由计划抽到 `windows_routes.go`(`windowsRoutes`,与 API 无关)TDD 覆盖;teardown **逐条 `DeleteRoute` 对称还原**(不 flush 物理 LUID),配 `--test-timeout` 死手复原;④ **WFP 封 off-TUN :53**(防 Windows smart-multihomed DNS 泄漏)——**vendor** `wireguard/windows/tunnel/firewall`(MIT)进 `internal/winfw`,加薄入口 `winfw.BlockDNSLeak(tunLUID,nil)` 只装三条过滤器 `permitSelf(15)`+`permitTun(14)`+`blockDNS(deny 12)`、**刻意不带 `blockAll`**(bx 分流,china/direct/bypass 合法走物理,全封会打死)。**权重是正确性核心**:`permitTun(14) > blockDNS deny(12)` 让**进-TUN 的 :53 通**(fake-IP 解析靠它)、只封 off-TUN;不可照抄上游 `EnableFirewall` 的 `deny 14 > permitTun 12`(那靠 `restrictToDNSServers` 例外放行隧道 DNS,bx 不依赖特定 DNS IP)。动态会话(`FLAG_DYNAMIC`)进程退出/崩溃**自动清过滤器**,比路由更 fail-safe。⑤ **Windows Service**(`bx up/down/uninstall` 自启)——两侧:进程侧 `internal/cli/service_windows.go` 的 `svc.Run` handler(bx.exe 被 SCM 拉起时必须上报 Running/Stopped,否则 SCM 判超时杀;`isWindowsService()` 门控,控制台 `bx run` 调试照常前台;Stop 时 cancel ctx 触发 `supervisor.Run` 的 defer 全量还原,与信号关机同源),管理侧 `internal/install/service_windows.go` 用 `svc/mgr` 建/起/停/删(`install.WriteUnit/Enable/Disable/Uninstall/ExecStartCmd/ServiceState` 加 `case "windows"`,LocalSystem 跑)。服务 `BinaryPathName` 用带引号命令行(路径含空格),`commandLineFields`(纯逻辑 TDD)双向解析(建服务拆 exepath+args、读回取子命令做 up 防呆)。OS-aware 路径:`defaultConfigPath`=`C:\ProgramData\bx\config.yaml`、`install.BinPath`=`C:\Program Files\bx\bx.exe`(build-tagged `paths_{windows,other}.go`)。这五块 windows-only(winipcfg/winfw/svc/syscall)交叉编译 amd64/arm64 + vet 过,**真机待验(务必带 `bx run --test-timeout 2m`)**。⑥ **setup 端到端集成(2026-07-08)**:OS-aware 路径(`config.DefaultDataDir`=`C:\ProgramData\bx`、`defaultConfigPath`、`install.BinPath`,均 build-tagged;`setupAction` 不再硬编码 `/var/lib/bx`)+ **`EnsureBrook` 补下载兜底**(原来无内嵌时把 nil 当内容写出空文件冒充 brook → 隧道莫名崩;现 `override>内嵌>下载`,windows/无内嵌 arch 按 `defaultBrookURL` 从版本派生官方 release 地址或用 config `brook_url`/`brook_sha256`,`downloadBinary` 与 EnsureSingbox 共享,`.brook-src` 缓存键避免每次重下)。**brook:// 的 `setup→up` 在 Windows 已代码打通**。⑦ **sing-box windows zip 下载兜底(2026-07-08)**:`EnsureSingbox` 补齐 windows 下载——url 空则 `defaultSingboxURL` 从版本派生官方 release 地址(**注意 tag 带 `v` 前缀、资产文件名不带**:`.../download/v1.13.14/sing-box-1.13.14-windows-amd64.zip`),url 以 `.zip` 结尾则下载后 `extractSingbox` 解压取 `sing-box.exe`(忽略子目录,`path.Base` 匹配),裸二进制 url 仍直接落盘;sha256 校验的是下载物(zip)本身;`.singbox-src` 缓存键(sha 或 url)避免重下。**至此 vless/reality/hysteria2/trojan/ss/vmess 六种传输在 Windows 均可 setup→up(代码层)**。⑧ **DNS-into-TUN(2026-07-08)**:Hijack 给 TUN 适配器设哨兵 DNS `1.1.1.1`(`winipcfg LUID.SetDNS(AF_INET)`)——否则 Windows 系统 DNS 常指 LAN 路由器(私网 bypass + 被 WFP 封 off-TUN :53)→ DNS 整个断。哨兵是**会路由进 TUN** 的公网 IPv4(在 `0.0.0.0/1`、非私网 bypass;不变量由 `windns.go`+守卫测试 `TestTunDNSSentinelRoutesIntoTun` 钉死),系统 DNS 查询进 TUN 由 fake-IP handler 应答(`engine.go` 拦 UDP:53 到**任意**目的地);TUN 接口 metric 已 0(最优)使系统优先用它,off-TUN DNS 由 WFP `blockDNS` 封、bx 自身 resolver 由 `permitSelf` 放行(三者权重自洽)。teardown `FlushDNS` + 适配器随 `closeTUN` 销毁,物理 NIC DNS 从不被碰、还原干净。⑨ **真机 e2e 已验(2026-07-09,`030-SJWJ-GSR-B` Win10 19044,SSH 提权联调)**:梯度 `debug-tun`(wintun.dll 加载+适配器+干净移除)→ `run --no-hijack`(reality 隧道 433ms 健康、TUN、经隧道刷 china 列表)→ `run`(全量劫持)全过——**整机出口==166.1.190.123(VPS)**、`0.0.0.0/1` 指向 bx0、DNS `example.com→198.18.0.16`(fake-IP,经 TUN)、WFP-DNS=true、SSH 源 10.84.14.37 经 10/8 旁路全程存活、死手 teardown 后残留 bypass/TUN/WFP 全 0、出口回直连。真机暴露并当场修掉 2 个 bug:**① WFP `permitWireGuardService` 靠服务 SID 放行自身,bx 非服务跑→`ERROR_NO_SUCH_GROUP` 整个 WFP 建不起来**(改 `permitAppID` 按 app-id 放行,`winfw/dnsleak.go`);**② run.go 第 0 步无条件下 brook**,reality-only 也白下(改惰性,尤其公司网络 TLS MITM 挡 github 时不卡无关下载)。**环境注记**:公司网络对 HTTPS 做 TLS MITM(自签根 CA)→ bx 的 github 下载 `x509: unknown authority`(**这是 bx 供应链安全在正确工作**,拒绝 MITM 证书);真机测试用 `singbox_bin` 指本地 sing-box.exe 绕开(override 通路真机坐实)。⑩ **Windows Service e2e 已验(2026-07-09)**:`setup`(自动装 bx.exe **+ wintun.dll** 到 `C:\Program Files\bx` + 写 config 到 ProgramData + 建 SCM 服务 DEMAND_START LocalSystem)→ `up`(Enable 设 AUTO_START+Start、svc.Run handler 上报 Running、服务内起隧道+全量劫持、**整机出口==VPS、WFP-DNS=true**)→ `status`(读 `C:\ProgramData\bx\bx.sock` 控制面正常)→ `down`(Stop→svc ctx cancel→`supervisor.Run` defer teardown 还原干净、START_TYPE 转 DISABLED)→ `uninstall`/`sc delete`。真机暴露并修:**① `SelfInstall` 漏装 wintun.dll**——服务以 System32 为 CWD 跑 Program Files\bx.exe,DLL 只在源目录→`Error loading wintun.dll`→服务起了就退;加 `installPlatformSideFiles`(`sidefiles_windows.go`)把 wintun.dll 一并装到 BinPath 同目录。**② 服务无控制台 stderr 丢弃**——加 `runAsWindowsService` 把 log 落 `C:\ProgramData\bx\service.log` + svc handler 记录 run 退出错误(靠它抓到 dll 错误)。**坑**:服务跑时 wintun.dll 必须在 exe 同目录(`bx.exe`+`wintun.dll` 同发布,`SelfInstall` 已自动随行);`bx uninstall` 的 Delete 可能被查询句柄挂 pending(同 review #10),`sc delete` 可强删。**Windows 移植网络/服务/供给/DNS 全层代码完整且真机端到端背书。** ⑪ **hysteria2 UDP 档 e2e 已验(2026-07-09)**:config `udp.transport: hysteria2://…` + `udp.mode: proxy`,`bx status` 显示 `传输 reality@… UDP→hysteria2@…`(TCP=reality/UDP=hysteria2 按类分流);整机劫持下 **NTP(UDP:123)经 dialer→UDP 档→hysteria2(QUIC)→VPS→时间服务器往返成功**(`w32tm` 返回真实偏移)、UDP 阻断 0——sing-box hysteria2(`with_quic`)在 Windows 跑通。⑫ **`bx kick`→`bx restart`(2026-07-09,真机促成)**:真机复现 kick 在 Windows 恒 403(非 Linux 无 peer-cred,`peercred_other.go` fail-closed 拒改动类)+ kick 仅热切隧道(数据面卡住修不了),按需精简——删 kick(命令/控制面 `/v0/kick`/`KickControl`),改一条 `bx restart`=全量重启(`install.Restart`),保留自启;`swapTo` 仍供 `runFailover` 容灾用。⚠️ **注意**:Hijack 实现后 `bx run` 一旦隧道健康就直冲全量路由劫持,无 OpenTUN-only 止步点。故加了两个**安全梯度 bring-up 工具**:`bx debug-tun`(只建 wintun 适配器、不起隧道/不碰路由,隔离验证 `wintun.dll`+wgbridge,零系统改动)与 `bx run --no-hijack`(起隧道+TUN+引擎但跳过 Hijack,验证隧道健康+TUN,系统网络零改动)。真机梯度:`debug-tun` → `run --no-hijack` → `run --test-timeout 2m`(+ config bypass SSH/RDP 源)全量。**真机观察项(code-review 提的 PLAUSIBLE)**:server 是**主机名**(非 IP)时,隧道断线重连要重解析主机名——WFP `permitSelf` 只放行 `bx.exe`,brook/sing-box 子进程 app-id 不同,其 off-TUN :53 会被封;理论上靠哨兵 DNS(TUN 内、`permitTun` 放行)+ `staticA` 静态真 IP 答案兜住(子进程走系统 resolver→哨兵→拿真 IP),但真机需实测重连是否卡死;若卡,补 WFP 放行子进程 exe 的 app-id。**施工图见 `docs/superpowers/specs/2026-07-08-windows-tun-design.md`**。⑬ **托盘 App(子项目②,2026-07-12,代码完成+交叉编译验,真机待验)**:新增 `bx tray` 子命令(windows-only)启动 `fyne.io/systray` 系统托盘,小白点图标即可连/断/设置/看状态,全程不碰命令行——对标 macOS `apps/macos/BxMenu` 的克制,是现有 CLI+服务+控制面之上的**薄 UI 壳**,不重造隧道逻辑。**提权模型**:托盘进程**非提权**常驻(只轮询状态、不弹 UAC,开机自启友好),仅点「连/断/设置/重启」这类改动系统的动作时 `ShellExecuteW` verb `runas`(+`SW_HIDE` 隐藏子进程黑框)拉起**提权** `bx.exe <up|down|setup|restart>` 子进程执行(仅此时弹 UAC);动作前 `MessageBox` 确认。**状态检测全非提权**:`install.ServiceState("is-active","bx")`(只读 SCM)+ config 存在性 + spawn `bx status --json`(解析 `server`/`tunnel_healthy`/`latency_ms`/`transport`)合成 5 态(未安装/未配置/已关闭/保护中/需注意),`detectState` 每 3s 刷图标(绿/灰/红 `.ico` 内嵌)+ tooltip。**设置走剪贴板**:`readClipboardText`(user32 LazyDLL,x/sys 无剪贴板)→ `parseSetupLink` 校验受支持前缀(bx/vless/hysteria2/trojan/ss/vmess/brook/blink),**且拒绝内嵌引号/空白**(真链接是 URL-safe base64,挡 `bx setup "<link>"` 参数注入,纵深防御)→ 提权 setup。**自启** HKCU Run(`golang.org/x/sys/windows/registry`,`sync.Once` 幂等)。**黑框**:`freeConsole()`(kernel32 LazyDLL,x/sys 无)消除 console 子系统 exe 双击时的闪窗。包 `internal/tray`:纯逻辑(`state.go`/`status.go`,无 tag,Linux 单测 7 绿)+ windows-only UI/syscall(`tray_windows.go`/`win_windows.go`/`icons_windows.go`,`//go:build windows`);`fyne.io/systray` 只被 windows-tagged 文件 import(`go list -deps` 证 Linux 从不编译它,零连累)。amd64/arm64 交叉编译 + vet 过。**真机待验(Task 6,030-SJWJ-GSR-B)**:GUI 交互(点托盘、批 UAC)需人在机器旁,非 SSH headless 可驱。设计见 `docs/superpowers/specs/2026-07-12-windows-tray-app-design.md`、计划 `docs/superpowers/plans/2026-07-12-windows-tray-app.md`。⑭ **打包分发(子项目③,2026-07-12,代码完成+dev 验,真机待验)**:收口消费级分发。**(A) exe 资源**:`go-winres`(纯 Go,Linux 可跑)从 `winres/winres.json`(真相源)生成 `rsrc_windows_{amd64,arm64}.syso`(提交进仓库根,Go 链接器按 GOOS/GOARCH 自动链入 windows 构建),给 `bx.exe` 嵌 **manifest(`execution-level: as invoker`——绝不 requireAdministrator,保②非提权托盘 + per-action UAC)+ 图标(`winres/icon.png` 绿盾 bx 标)+ 版本信息**;`go generate ./...`(`generate_windows.go` 承载指令)重生成。**dev 端到端验过**:amd64/arm64 build 链入成功、`go-winres extract` 证资源已嵌、linux/darwin 不受影响。**(B) Inno Setup 安装包** `packaging/windows/bx-setup.iss`:`PrivilegesRequired=admin`(装器自提权)装单文件 `bx.exe`(已全内嵌)到 `{autopf}\bx` + 开始菜单快捷方式(`bx.exe tray`)+ 添加/删除程序 + 装完 `postinstall` 起托盘 + 卸载 `[UninstallRun] bx.exe uninstall`(停删服务);固定 `AppId={45A7EBE8-…}`(永不变);**不装服务**(无链接,交托盘 setup)。`.iss` 无法 Linux 编(`iscc` windows-only),CI/真机验。**(C) release 接入 windows**:`release.yml` build job 加 windows amd64/arm64 交叉编译(go-winres 先 `--product-version/--file-version` 覆盖 tag 版本再编)出 `bx_windows_*.zip`,新增 `installer` job(`windows-latest` + choco 装 Inno + `iscc` 出 `bx-setup.exe`)上传 release;`ci.yml` 早已含 windows 交叉编译 + 测试矩阵(`.syso` 守卫自动覆盖,无需改)。**(D) README** 加 Windows 安装(安装包/便携版/托盘)+ **SmartScreen「仍要运行」提示**(无 code signing,现无证书,留待有证书补 signtool 一步),并纠正旧文档「wintun.dll 必须同目录」(①已内嵌)。**关键决策**:manifest `asInvoker`(相对原分发文档「双击→UAC」的有意修正,与②自洽)、不签名、安装包 amd64-only(便携 exe 覆盖 arm64)。**真机待验(Task 5,与②合并)**:双击 `bx-setup.exe`→SmartScreen→装→开始菜单起托盘(带图标)→exe Properties 显版本→剪贴板设置连接→出口==VPS→添加/删除程序卸载清干净。设计 `docs/superpowers/specs/2026-07-12-windows-installer-packaging-design.md`、计划 `docs/superpowers/plans/2026-07-12-windows-installer-packaging.md`。**至此「Windows 消费级分发+小白可用」三子项目(①内嵌②托盘③打包)代码全完成,合并真机验收待用户在 Win 机器旁跑。**
  - **`internal/winfw` 是 vendored 第三方**(WireGuard firewall,MIT):除 `dnsleak.go`(bx 薄入口)外逐字复制,仅三处适配——包名 `firewall→winfw`、非 `_windows` 后缀文件补 `//go:build windows`、arch 文件约束补 `windows &&`(`types_windows_64.go` 的 `_64` 非合法 GOARCH,文件名后缀不生效,必须显式加 windows 否则 Linux 误编译)。升级从上游同步时保持这三处。
- **REALITY 传输收尾**:① **自举悖论已解**——sing-box 改为内嵌(自建静态最小构建,见「约定/内嵌资产」),`bx up` 真零外部依赖,download 仅作无内嵌 arch / 自定义兜底。② **端到端已验(2026-06-28)**:用 bx 自己的 `parseVlessLink`+`singboxConfig` 生成客户端配置,跟真实 sing-box REALITY 服务端(VPS,SNI 借 www.apple.com)握手——出口 IP == VPS、停服务端 → 隧道失败不回落(kill-switch 语义)。**协议层坐实**。③ **真实硬件 e2e 已验(2026-06-29)**:在 GL.iNet Mudi(GL-E5800,**aarch64 + musl OpenWrt**)真机上,内嵌静态 arm64 sing-box 直接执行(`version`/`check` 均过)+ reality 握手到真实服务端成功,经隧道出口 == VPS、而直连同服务被运营商封 → 隧道是唯一通路,无可辩驳。**这坐实了「自建静态而非官方 glibc 动态包」的决策——官方包在 musl 上直接 `not found`,我们的静态构建照跑。** ④ **整机 e2e 已验(2026-06-29,Mudi host 模式 global)**:`bx run` 开 bx0 TUN + 劫持整机路由 + reality 隧道,**整机出口 == VPS、kill-switch 停服务端即 fail-closed、退出 defer 还原干净**(bx0/规则/table 100 全清)。修复期间挖出并修掉一个多 WAN bug:`defaultRoute()` 旧逻辑取最后一条 default、无视 metric,在 Mudi(wlan4 metric20 + SIM metric40 双默认)上错选 SIM(CGNAT 抖)→ 隧道走烂路健康抖动;`parseDefaultRoute` 改按 metric 选首选后,隧道一次健康(410ms)。⑤ **split 模式已验正确(2026-06-29,Mudi)**:china 列表正常加载(`china_domain=12165 china_cidr=6115`)、fake-IP DNS 正常(`ifconfig.me→198.18.0.1`)、foreign 走隧道(icanhazip.com/ipinfo.io 出口==VPS)、china 站直连(运营商 IP)。先前疑似的「漏直连」是**自摆乌龙**:`BX_DEBUG=1` 显示 `dial direct: domain="ifconfig.me"`——**`ifconfig.me`/`ip.sb` 本就在 brook 的 `china_domain.txt` 直连列表里**,bx 照列表正确直连;我拿了 china 列表里的域名当 foreign 出口探测才误判。教训:**验出口/分流别用在 china 列表里的域名,用 **icanhazip.com / ipinfo.io**(两个都逐字核过不在列表里)。**这条教训自己错过一次,2026-08-11 才发现**:原文推荐 `api.ipify.org`,而 `ipify.org` 同样在 `china_domain.txt:6045`,于是那条写错的推荐照着进了 `internal/cli/cli.go` 三处 —— `bx doctor` 的公网 IP 探测在保护开着时走直连,报出用户**真实的 ISP 出口**,把一台工作正常的机器说成在漏,方向正好相反。现在不靠记忆:`TestPublicIPProbeDomainsAreNotChinaDirect` 拿**真实的内嵌列表 + 生产用的同一个 DomainSet** 逐个比对,读不到列表就响亮失败**。（那条 `china_cidr=0` 是 global 模式日志,global 本就跳过 china 列表,非 bug。）⑥ **bx0 MTU 怀疑已证伪(2026-06-29)**:经 bx0 下 9MB(jsdelivr)**完整无损**(http200、字节全量、尾部正常),Mudi 上 apple.com 大 TLS 响应经 bx0+reality 也 200。最初疑似的「MTU」其实是 **api.ipify(Cloudflare)目标特异**——它直连也失败(运营商对 Cloudflare 消费 IP 干扰),与 bx0 无关。gVisor 终结 TCP + 子进程按内核 path-MTU 重分段,bx0 大流量无 MTU 截断问题。**reality 整条线再无已知开放项。****踩坑备忘**:① reality 端口受制于服务端 ufw/云安全组白名单 + 路径对 443 的 DPI 干扰(实测 443 与非白名单高端口 TCP 能连但 TLS 载荷被黑洞)——服务端落已放行高端口(同 brook 9999),勿默认 443;② **OpenWrt/BusyBox `ash` 不支持 `/dev/tcp`**,在路由器上测连通必须用 `curl`/`nc`,否则全是假阴性(曾误判路由器"无 TCP 出网")。
