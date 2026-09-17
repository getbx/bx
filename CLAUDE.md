# CLAUDE.md — bx

基于 brook 的 **Linux 透明全局代理**(自研「类 ipio」,单一 Go 静态二进制)。整机 TCP/UDP 经 TUN 自动分流:中国直连、其余走加密隧道,对应用零配置。隧道是**可插拔黑盒子进程**:`brook://` 链接→内嵌 brook(默认),`vless://` 链接→sing-box 的 **VLESS-REALITY**(抗 DPI 伪装);两者其余全自有代码。

- 用户文档见 `README.md`;设计/计划见 `docs/superpowers/specs/` 与 `docs/superpowers/plans/`。
- **过程记录见 `docs/lessons/`**(事故复盘、施工日志、守卫的七种失效写法);
  **待人工验收的清单见 `docs/acceptance-pending.md`** —— 那上面每一条都只有人在机器前
  才能做(要在屏幕上点,或要制造一次真实故障),**agent 不要去跑它,也不要替它下结论**;
  本文件各节的「真机未验」标签是那份清单的索引。
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
  **observer** 显式 nil(不装假观测)。**这份清单写下时门还没开**(当天的中间态是
  「供货完毕、daemon 门未开、netns harness 未接」);**次日全部做完,上面那段已经
  记了** —— `internal/guardian/daemon_linux.go` 的 `requireDaemonPlatform` 直接 `return nil`,
  `harness_{barrier,manager,daemon}_netns_linux_test.go` 三条台子都在,而
  `internal/guardian/lifecycle_linux_test.go` 钉的已经是**反过来那句**
  (`TestLifecyclePlatformLinuxGateIsOpenNowThatEveryPieceIsSupplied`)。
  **这里此前留着一句现在时的「linux 上 Guardian 仍起不来」,而它已经不成立** ——
  本文件罚过很多次的那一类:一句声称某个限制仍然活着的话,比一句普通的陈旧记述更坏,
  它会让下一个人不去核就相信,并连带对旁边那些还成立的话打折扣。焊死语义只对
  darwin/linux 之外保留原话。终局路线见
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

**判据分四段,各自计数,绝不合成一个总数**(`Section`)(**2026-09-14 从三段改成
四段** —— 加了 `reach` 那天这句定义句漏了改,是本文件罚过的「只清点名的那一句、
不清同一句话的其它副本」当场又犯了一遍):`path`(流量去哪儿,bx 或当前隧道
负责,进 `AnomalyCount`)· `identity`(会不会被单独认出来,进 `IdentityCount`)· `surface`
(网站看得到什么,**`Verdict.Info`,没有极性、不进任何计数**)· `reach`(AI 站边缘
可达性,进 `Report.Reach` 那五个并排的计数,详见下文十四条结论那段)。合成一个数
时它永远不为零(普通 Chrome 就是不防指纹),于是被训练成噪声、把真正的泄漏一起
淹掉。`Section` 零值是 `SectionPath`:漏填是多报,反过来是漏报,代价不对称。

**十四条结论**(此前这里写的是「八条」,**漏了头尾两条**,2026-08-24 按 `Outline()` 实测
更正;**2026-09-14 从十条改成十四条** —— 第四段 `reach`(AI 站可达性)接进
`Outline()`/`Judge()`,四个端点各一条结论):
`traffic_carrier` 谁在承载你的流量 · `webrtc_srflx` WebRTC vs 出口 · `ipv6_leak` IPv6 暴露 ·
`dns_path` DNS 路径 · `route_escape` **路由被动过手脚(TunnelVision CVE-2024-3661 /
TunnelCrack ServerIP)** ‖ `local_addresses` 内网地址是否被 mDNS 遮掉 · `timezone_vs_exit`
时钟 vs 出口国 · `language_vs_exit` 语言 vs 出口国 · `fingerprint_defence` 指纹防护 ‖
`browser_surface` 网站看得到什么 ‖ `reach_*`(前缀拼端点 ID)四个 AI 站的边缘可达性 ——
**不是安全问题**,坏消息是「你用不了」不是「你泄漏了」,与另外三段各自计数、绝不合成。
(`‖` 是分段边界:path 5 条、identity 4 条、surface 1 条、**reach 4 条**。)
骨架(`Outline()`)与 `Judge()` 的 ID/顺序/分段**逐项对上**,由守卫钉住;「哪条需要浏览器」由
`Outline().Inputs` 是否为空推导,**不许手抄一份 ID 列表**(`reach_*` 的 Inputs 为 nil ——
探测是普通 HTTP 拨号,不依赖页面 JS 采集)。
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

**第四段从 2026-09-14 起真的在跑**(此前 `ProbeReach` 零生产调用方,四条结论在真机上
恒为「这一轮没有检查」而两个包全绿)。接线是 `internal/cli` 的 `collectLeakCheckFacts`
→ `leakserve.CollectReach`,**单独一跳、单独预算**:探测是四次跨洋 TLS 握手,塞进
`CollectFacts` 那 5 秒预算里会被掐断,而掐断的结果与「这条路真的不通」在屏幕上一模一样;
预算由**探测数**派生(`reachBudget`),不写死秒数。**它给 `bx leakcheck` 新增了出站**:
跑一次会从本机 GET 四个 AI 端点(走当前路径,不绕隧道),故 `announceReachTargets` 在
**第一个请求之前**把地址原样列出来(`--json` 走 stderr)—— 页面那份披露只管浏览器那半,
这一半页面一个字节都不经手。bypass 那条路仍关着(`DefaultProbeBypass=false`,spec §5.1),
**而且没有绑物理网卡的拨号器**:只翻常量不供 `BypassDial`,这一轮会安静地什么都不多跑;
spec §5 那三句比较结论(「直连不行、走当前隧道行」…)也等那一天,不是忘了。
第四段在 CLI 与页面**各有自己的标题**,摘要**另起一行**报五态、**为零也打印** ——
`default`/`|| titles.path` 兜底吞掉新分段在这一支里出现过三次,每一次的后果都是把
「连不上」画在一个写着「你的流量去哪儿」的标题底下,读起来就是一次泄漏。

**`ReachState` 是五态,零值 `ReachUndetermined`(问过没问出来)**:`Reachable`/
`Refused`/`Unreachable`/**`Challenged`**。**`Challenged` 与 `Undetermined` 必须分开
(review 抓到的核心判据)**:CF 人机挑战有真凭据(特征串)能主动否掉坏消息「不是你的
出口有问题」,而「认不出」没有凭据、不许替它猜同一句话 —— 合成一态就必然有一半在
撒谎:一个真实的地区封禁页(HTML、不含那几个构造的关键词)会落进 undetermined,若
借用 Challenged 那句话,用户读到的是「不是你的出口有问题」,而这个功能存在的唯一
理由就是回答这个问题。**`JudgeReach` 同时看状态码与 body**:
`generativelanguage.googleapis.com` 的 403 是 API 在正常应答(JSON body),
`chatgpt.com` 的 403 是人机挑战(CF 特征串)—— 同一个码,相反的两件事,只看状态码
会判反。

**端点四个**(anthropic API `/v1/messages`、claude.ai **favicon 路径**、openai API
`/v1/models`、google AI API `/v1beta/models`),**`chatgpt.com` 刻意不在清单里**:
它实测恒为 CF 挑战页,会变成一行永远给不出答案的噪声。**claude.ai 用 favicon 而非
首页**:首页是 403 CF 挑战,favicon 路径不挂防护。

**措辞纪律**:可达只说「bx can reach X」,**绝不说「你可以用 X」**—— 地区限制可能
在登录/调用层,本期只观测到了边缘,bx 无权替对方的产品说话;不可达只说 bx 观测到
什么,**不断言对方服务的状态**(与 `core_tunnel_unreachable` 同一条纪律)—— 本机
自己没网时同样拨不通。

**`DefaultProbeBypass=false`,取舍写在这**:绕过隧道那条路径会从物理网卡直接发
4 个 GET、**暴露真实 IP 给 Anthropic/OpenAI/Google**——是 leakcheck 今天没有的
新行为,决定留给项目所有者;守卫把「翻常量」与「供 `BypassDial`」绑在一起,只翻
常量会安静地什么都不多跑。

**已知边界**:favicon 200 只证明**边缘可达**,不证明能登录能用;`Refused` 的关键词
**没有真机样本、是构造的**;CF 的防护会变(今天 favicon 不挂,明天可能挂),端点
常量带 `ExpectedSignal` 记档,行为变了守卫会红一次。

**以上五段(五态/端点/措辞/bypass 取舍/已知边界)整段真机未验**——下面那条
2026-08-31 的「真机首验」在 reach 接线之前跑的,它的「仍未验」清单里没有 reach。
逐条验收清单见 `docs/acceptance-pending.md` 的 A8。

**真机首验(2026-08-31,项目所有者的 Mac):本机那一半全绿** —— 十条结论一条不少、
三段分段正确、**三个计数并排且绝不合成**(`0 leak(s) / 0 identifying trait(s) /
6 not checked`,没有出现「没有发现泄漏」那句最坏的假话)、`WhoOwnsTheRoute` 判对
(「carried by bx (utun12)」)、TunnelVision 判据形状对(7 条单主机路由走隧道外,
如实说明而**不报逃逸**)。**逐条结果见 `docs/lessons/leakcheck-page-js-gate.md`。**

**仍未验(三项,别读成已验)**:① **浏览器那半**——要人点一下按钮才产生数据,
点下去会向 icanhazip/cloudflare/STUN 发真实探测;② **非 root 门槛**
(`guardLeakCheckPrivileges`)当时没有 sudo 口令,没实跑;③ `--json` 输出。

**页面那半的 JS 有闸门,判据仍全在 Go 里。** `page.html` 用
`==== BX-PURE-BEGIN/END ====` 划出一段**纯解析**(只做「字符串 → 结构」),
`scripts/test-page-js.sh` 把它原样抽出来交给 node 跑断言,挂进 `verify.sh`(没有 node
时显式报 SKIPPED,不安静通过)。覆盖整页最承重的两处:ICE candidate → srflx/host 地址、
`/cdn-cgi/trace` 的 body —— **它们解析错了不会报错,只会让结论悄悄变成「没检查」或者
一个错的 IP,而对一个泄漏检测工具,静默的假阴性是最坏的一种失效。**
Go 侧三条守卫钉住 node 自己证明不了的事:区段是纯的、**区段里定义的每个函数页面都真的
在调**(一个没人调用而测试盖着的纯函数,与没有测试在输出上完全一样)、闸门真的接进了
`verify.sh` 且连收尾横幅一起查。**四个探针的极性全部收进纯区段**,守卫是「**每一处都是**」
而不是「至少有一处」—— 前者在三处内联表达式旁边照样绿,而那三处恰恰没有任何测试盯着。
接线守卫 `TestPageJSNeverAssertsThatAProbeLanded` 的判据**刻意不对称**:第二个实参写
字面量 `true` 一律禁(那是没看答案就宣布探针落地),字面量 `false` **允许**(只出现在
`.catch` 里,什么都没到达)。**逐条经过(闸门装上第二天抓到的那个真 bug、五条变异、
我自己那两条守卫各有一个 bug)见 `docs/lessons/leakcheck-page-js-gate.md`。**

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

## Routing Rules 窗口重做:规则编辑器 + 加规则的风险门(2026-09-11,真机未验)

窗口从「预设开关」变成规则编辑器:一行一条(direct+proxy 都在),有问题的排
最前、健康的不说话,`Add Rule…`、`Remove` + `Undo`。`ruleRows(from:failing:
customOnly:)`(`RulesModel.swift`)此前**早就存在、只有测试在调**,本期是接上
`RulesWindow.swift`/`main.swift` 而不是新造。**删除不弹确认但留 Undo**——为 11
条冗余规则点 11 次确认框是在惩罚正确的行为。
**风险门挪了位置,判据只有一份**:`policy.DirectRuleHazard`
(`internal/policy/policy.go`)判加规则,`DirectRisk`(`internal/rulereview` 体检
与 `policy.Apply` 那道 `allow_risk` 门在用,后者正是 MCP `bx_policy_apply` 走的路)
现在是它的**薄壳** —— 四个消费方同一个判据,漂不开。
`internal/cli/direct.go` 与 `internal/guardian/rules.go` 共用它——**此前 Guardian
一处都不查**,右键能一键加进 CLI 会拒绝的规则,本期堵上(`Force` 字段,409
`code=rules_risky_direct`)。
**判据是「这条规则覆盖到哪里」,不是「它写成什么样」** —— 曾经收窄成「只拦开放
平台上的通配符,确切主机放行」,理由是「那个确切主机攻击者拿不到」;**那句前提
对 bx 是假的**:匹配器是后缀集(`route.NewDomainSet` 去掉 `*.` 只存后缀,`Match`
逐级往父域找),`bucket.s3.amazonaws.com` 与 `*.s3.amazonaws.com` 覆盖的子树一模
一样,`evil.bucket.s3.amazonaws.com` 两种写法都直连出去。收窄因此等于在**裸写**
的形式上完全不设防,而 `TestDirectRuleRiskSilentOnBrandDomains` 还被翻过来断言
`amazonaws.com` 必须放行 —— 把回归钉成了绿的。已撤回:好用由**逃生口**买单
(`--force` / 菜单 409 之后的 Add Anyway),不由放松判据买单。守卫钉的是缺陷本身
(`TestEveryOpenPlatformIsHazardousWrittenBareAndReallyCoversStrangers`:每条裸写
的平台域名都判危险,**且** `route.NewDomainSet` 真的匹配 `evil.<它>`)。
右键候选的过滤在 `appTrafficRuleMenu` 里、**只作用于 direct**:滤在
`ruleCandidates` 里会连 Guardian 明确放行的 proxy 候选一起丢掉(`*.workers.dev`
恰是那份菜单上最安全的一项,两段域名的目的地还会得到空子菜单)。Swift 平台清单由
`TestOpenPlatformListMatchesPolicy`(`internal/cli/macos_menu_hazard_test.go`)钉
住与 Go 逐字相同,并**两种写法各问一遍**,免得下一次收窄又从它眼皮底下过去。

**窗口那半同一轮修掉四条,每条都是「界面悄悄替服务端说了一句它没说过的话」**:
① **体检缺席 ≠ 体检说都健康** —— `list.review` 为 nil(旧 Guardian,或配置读不
出来)时窗口照样摆一排没有副标题的行,而这个窗口的词汇表里「没有副标题」恰恰读作
「查过了、健康」;现由 `ruleWindowCaveatNote` 在表顶说明白,措辞按「nil 是
『这版没说』」那条纪律(`TestMacMenuRulesWindowAnnouncesAnAbsentReview` 连
**摆表只有一个出口、而那个出口自己带上它**一起钉 —— 判据 2026-09-12 从「每一处
都记得」换成了「只有一处」,见下文那条)。
② **服务端写的是中文** —— `rulereview` 与 `deadFindings` 的 `summary` 原样渲染
进一个通篇英文的菜单(`已在内建 china 直连列表里…… ← *.apple.com`);当时由
`ruleVerdictText` 按 `class` 在客户端映射成英文,理由是「`summary` 同时喂着**中文的**
`bx doctor`/`bx status`,再写一份就是同一句判断在两处各写一遍」。
**2026-09-17 那个前提被拆掉了**:`internal/doctor`(35 条)与 `internal/rulereview`
(16 条)的判据文案改成英文,一处产地;`ruleVerdictText` 连同它的 Swift 测试一起
删掉,菜单直接渲染 `summary`。**走英文这条路在这里是减少一份清单,不是增加。**
客户端只剩一件事:服务端没发 `summary` 时把那个 `class` 原样带上(`verdictText`),
绝不冒充看懂了。「每一类说一句属于自己的话」这条不变量**搬到了 Go**
(`TestEveryClassSaysSomethingOfItsOwn`,穷举 Class、走生产的 `Review`)——
**判据搬了家,守卫必须跟着搬**,否则它会随着那次删除一起静默消失。
③ **一行既被分类又在成片失败时,失败那半此前整个丢掉** —— 8113/8113 全失败的规则
只显示「删掉它不改变任何流量」还被画成红的;而 `DomainSet.MatchRule` 逐级往父域找,
**累积失败的恰恰是被盖住的那条更窄的规则**,不是边角情况。
④ **规则窗口从来不跟环境刷新走** —— `RulesWindow` 连 `isVisible` 都没有,失败计数
冻在打开窗口那一刻,而「哪条在失败」正是这个窗口存在的理由(与 2026-08-17 服务器
窗口那次回归同一形状)。现照服务器窗口那份先例接上,并由
`TestMacMenuRulesWindowFollowsAmbientRefreshButNeverSuppressesAnExplicitOpen`
把**不对称**一并钉住:显式打开永不被在飞标志拦(那次「点了没反应」)。
另有 `TestMacMenuRuleClassLiteralsMatchTheGoClassNames` 双向钉住菜单那五个字面量
与 `rulereview.Class.String()` 是同一组词 —— 此前改 `ClassRisky.String()` 会让
去匿名化那一行被画成橙色建议、说明回落成「bx flagged this rule (…)」,**而两个
套件全绿**。**真机未验**:默认窗口大小的表格布局与列宽、`Add Rule…` 三种
结局(接受 / 409 后 Add Anyway / 非法输入)、`Remove`+`Undo`、右键候选过滤,见
`internal/cli/macos_menu_ruleswindow_test.go`。

**收尾时停在台账里、当时没进这份文件的六条(2026-09-12 补记)。** 它们是**已知
并接受**,不是待修的 bug —— 补记的理由与这份文件反复罚过的那件事互为镜像:那边
是一份说谎的清单,这边是一份**根本不存在**的清单,而后者连让人去核一遍的机会都
不给。逐条核过仍然成立:

- **Add Rule 是可重入的**(拨号改异步之后)。第一次还在飞时用户可以再点一次
  `Add Rule…`,而 409 回来时 `askForNewRule` 会在已经开着的那个 modal 上再叠一个。
  **不丢字**:每一轮 `addRuleFromWindow` 自建一份 accessory view,两条流各写各的。
- **`addRuleBack`(Undo)没有在飞守卫** —— 双击就是两次 **带 force** 的 add
  (对照:`removeRuleFromWindow` 有 `ruleRemovalsInFlight`)。无害的**承重理由是
  `setup.AddRule` 幂等**,不是「点两下不会发生」;哪天那个幂等没了,这里就要补守卫。
- **`RuleFinding.summary` 解出来了、一个字都没渲染** —— 窗口改成按 `class` 映射
  英文(`ruleVerdictText`)之后它就没有消费方了。**留着是 wire 契约**(同一份
  `summary` 还喂着中文的 `bx doctor`/`bx status`),不是死字段。
- **窗口开着时每一次环境刷新都拉一遍 `/v1/rules`**,而 Guardian 那一跳会重读配置、
  重建一张约 12k 条的 `route.DomainSet`(`internal/rulereviewsrc`)、再加一次 Core
  往返。与服务器窗口当初同一笔交易(窗口开着就说明有人正盯着)。**没量过。**
- **`ruleWindowCaveatNote` 把一次配置解析失败说成了版本问题** —— 它写的是
  「这一版 bx 没检查这些规则」,而 `review == nil` 也包括「Guardian 读得到文件、
  `config.Parse` 拒了它」(`reviewRulesAt` 两条早退都返回 nil)。**要紧的那一半是
  对的**:它绝不宣称健康。
- **`apps/macos/BxMenu/Sources/BxMenu/AppTrafficModel.swift` 里那句注释仍写作
  `riskyDirect`**,而守卫读的是 `riskyDirectDomains`(`internal/policy/policy.go`
  里两个都存在,前者是后者建出来的 `DomainSet`)。名字陈旧,说的事情属实。

**组的副标题(2026-09-14,真机未验)**:品牌名(Steam / Apple / Tencent)答的是
「这一组叫什么」,而用户站在窗口前想的是**开了会怎样**;展开后的域名是证据不是答案。
这一行此前被刻意拿掉过,**拿掉的两条理由里有一条必须被解决而不是绕开** ——
上一版是勾选框下面缩进一行小字而那行**多半是空的**,于是一屏参差不齐的留白。
故 `ruleGroupSubtitle` **恒非空**(认不出的组回落到「N domains」这句一定成立的话)
且摆在**同一行里**,一组仍然一行。另一条理由原样保留:**绝不回显服务端那句中文
`summary`**(它同时喂着 `bx preset show`),在客户端按组名映射成英文,与
`ruleVerdictText` 按 class 映射同一条。Go 那份预设清单与 Swift 这张表由
`TestMacMenuEveryPresetHasAnEnglishSubtitle` **双向**钉住 —— Go 加一组而菜单没跟上
时新组会静默停在那句回落上,**而回落与「我们想过了、就是没什么可说的」在屏幕上
完全一样**。**三态与「覆盖了你几条自定义规则」不用再做:前者早就是
`groupState` 三态 + 勾选框 mixed 态 + 尾列 `N/M`,后者由自定义列表自己变短说出来了。**

### 同一个窗口的第五条:Core 不应答时失败那半被读成「一条都没在失败」(2026-09-12,真机未验)

**空数组在这里是「没问出来」,不是「没有」。** Go 侧的契约是 `CoreRuntime.Reachable`
为 false 时**其余字段按构造全是零值**(`internal/guardian/types.go`),于是
`failing_rules` 是空的;而规则窗口的五处摆表**一致地**写着
`self.maintenanceReport?.core?.failingRules ?? []`,把它读成「没有规则在失败」。
`/v1/rules` 那一跳照样成功(Guardian 自己读配置、自己算体检,**不需要 Core**),
所以体检那句话也不会出现 —— 合起来:保护关着、Core 崩了或正在重启时,窗口摆出
一排没有副标题的行、按最健康的一档排序,**其中就有那条把用户招来的失败规则**,
而这个窗口自己的约定是「不说话 = 健康」。它不是边角:「Routing Rules…」那一项加在
状态 switch 之外、只由 `rules` 能力门控,保护关着时照样点得开。
**Swift 那侧其实早就知道这条契约**:`CoreRuntime.failingRules` 的注释写明「空
**不是**没问出来 —— 后者由 reachable 表达」,`MenuRows.swift` 也早有
`answeringCore()` 这道 `reachable == true` 的门,只是规则窗口那几处没用它。

- **判据取 `reachable`,不取「数组空不空」**,并且**复用** `answeringCore`
  (它因此不再 private):同一个问题不许有第二份判据,而第二份恰好答反了。
- **一句话报两个半边,不是两条横幅**(`ruleWindowCaveatNote(_:coreAnswering:)`,
  由 `ruleReviewUnavailableNote` 改名而来 —— 只报体检那半的名字会变成假话)。
  理由两条:① 要更正用户的是**同一件事**(「这一行什么都没写」≠「查过了」),
  并排说两遍只会训练他把顶上那块整个跳过去,而这个窗口的全部纪律就是「只在真有
  问题时才占地方」;② 摆表的地方不止一处,两条横幅就是两次机会漏掉其中一条 ——
  正是这次要修的那个形状。Core 答着话且体检收到了 ⇒ 恒 `nil`,**健康的机器上顶上
  一个字都没有**(常驻横幅本身就是缺陷)。
- **摆表收口成一个出口** `presentRules(_:forceShow:)`(此前五处各算一遍组行、
  规则行、顶上那句话)。局部绑定刻意叫 `answering` 不叫 `core` —— 后者会拼成
  `core?.failingRules`,与这个 bug 的原形逐字重合,守卫再也分不开「过了门的」与
  「直接从 status 上摸的」。
- **`ruleRowSeverity` 一个字不改,这是刻意的。** 「排在后面 = 更健康」这句话是由
  **并列关系**说出来的;Core 不应答时失败那半对**每一行**都缺席,没有任何一行因此
  被排到另一行后面 —— 排序退化成配置里的原顺序,它什么也没断言,而体检那半若还在,
  它的几类照旧排到最前。反过来给纯排序再塞一个 `coreAnswering` 参数,就是把
  `reachable` 抄成第二份,换来的东西横幅已经说了。

**守卫两侧**:纯模型 `RulesModelTests.testCoreNotAnsweringIsNotRenderedAsNothingFailing`
钉的是**用户看得见的东西** —— 同一份规则、同样一个空的 failing 数组,「Core 没答话」
那次与「Core 答了、一条都没在失败」那次**必须长得不一样**(拿顶上那句话 + 每一行的
模式与副标题拼成一个串比);接线 `TestMacMenuRulesWindowNeverReadsFailingRulesFromAnUnansweredCore`
钉语义不钉拼法:全文不许再出现 `.core?.failingRules`、`coreAnswering:` 不许是字面量
(写死 true 就是没看答案先宣布问过了,与 leakcheck 那条 `probeLanded(probe, true)`
同形)、`answeringCore` 不许变回 private 也不许有第二份定义。
**真机未验**:窗口顶上那句话的观感与换行。

## Servers 窗口:从一份清单变成「这条隧道现在怎么样,以及我能换到哪儿」(2026-09-12,真机未验)

所有者原话「servers 的页面也是,可以升级下」,与 Routing Rules 那次同形;**这个窗口
此前在本文件里一行记录都没有**。主要理由不是「Guardian 发了而窗口没用」,是它在说
**三句假话**(spec §2.1),三处线上改动各修一句:

- **空列表是死路,给的出路还不存在** —— 按钮带画在 `rows.isEmpty` 的 `return` 之后,
  唯一那句提示 `bx setup --name <name>` 里的 flag 根本不存在。**而这是最常见的情形**:
  `bx setup` 从不写 `servers:`,于是每个正常装好 bx 的人打开它都看到「No servers yet」
  而 bx 正跑着一台。现由线上新加的 `single_server` 把「单服务器配置」与「清单真的是
  空的」分开(`serverListEmptyReason`),按钮带**照画**
  (`TestMacMenuServersWindowKeepsTheButtonsWhenTheListIsEmpty`);`internal/cli/servercmd.go`
  里那份同款死提示一并清掉(`TestServerListEmptyHintNamesACommandThatExists`)。
- **「没能测」被画成「这台服务器坏了」** —— `ProbeReport` 加 `measured`(**不带
  omitempty**:缺席读作「这一版 Guardian 没说」,不是「测过」),Guardian 那两处中文
  产地改发码,菜单**根本不解码** `error` 那个键 —— 不显示服务端的中文从「靠纪律」变成
  按构造做不到;红只从**实测失败**来(`TestMacMenuServerRowRedComesOnlyFromAMeasuredFailure`)。
- **`●` 跟着配置走,不跟着实际在跑的走** —— 热切先写配置再切,失败那一刻窗口正断言你
  的流量从一台它其实没走的机器出去。现**并列**发 Core 报的 `running`(问不出来就缺席,
  绝不与 `current` 合并),吞吐峰值改按「这个峰值是在哪一台上量的」归属**并带真实年龄**
  (此前写死 0 ⇒ 读起来像「刚在这台量到的」);`bx server list` 的 ● 一并改。

切换四种结局各一个码(`arm_failed`/`rolled_back`/`rollback_failed`/`commit_failed`,原始
错误串不出门),认不出的回落旧常量 —— 消费方**必须留一个「说不出是哪种」的分支**。
`⋯` 里两个动词:**删除弹确认、没有 Undo,与 Rules 窗口刻意相反** —— 链接是凭据
(`TestServerListNeverShipsTheLinkItself`),菜单手里从来没有它、删了加不回来,而一个撤
不回的 Undo 比没有更糟;服务器又很少,不存在「为十一条冗余点十一次」那种惩罚。删当前
那台一律拒绝(菜单置灰 + 服务端 409,两道)。换链接走**新加的** `setup.ReplaceServerLink`:
`UpsertServer`/`AddServer` 都会挪 `current`,换一条**没在用**那台的链接会顺手搬走出口 ——
`UpsertServer` 因此**仍是零生产调用方**,spec §7.2 那句「用 UpsertServer」已就地更正。
动词另立能力 **`servers_edit`**:只声明 `servers` 的旧 Guardian 收到 `remove` 会落进兼容
分支**切到那一台**去,而这里「试着拨一下看看」的代价就是把用户的出口国换掉;Add 表单的
UDP 框**不**门控(旧 Guardian 一直处理得对,加门等于在那道门本要保护的机器上删功能)。
**所有者定死的四条边界一字未碰**(spec §8):不自动容灾、**只有用户能切**;不按延迟排序 /
不自动选最快 / 不分组 / 不导入订阅;**不后台定时探测**(探测走在隧道外面,几台同时握手
是一个很整齐的模式,而它们恰好是同一个人的资产)—— 只在用户点时发、且串行;不做每台
独立的 `rules`/`dns`/`udp.mode`。

**两条已知缺口**(此前记的三条里,`servers_edit` 那一条已在整枝修复轮做掉,见下):
① `replace` **清不掉** UDP 链接(Guardian 把空 `udp` 读作「保持不变」;界面已明说
「留空 = 保持这台已有的」,措辞对、缺口真)。② **最常见那种配置(`bx setup` 写的、
根本没有 `servers:` 键)仍然看不到「当前那台」那一块** —— `currentServerPanel` 要清单里
有一条 `current` 的条目,而 Guardian 没有条目可画。不是回归,但 Task 6 的报告与验收清单
把这句说反了。

### 整枝 review 的修复轮(2026-09-13)→ `docs/lessons/2026-09-servers-and-core-start.md`

**十一条,而「守卫钉住的是缺陷旁边的东西」的第十二次长在漏斗自己身上**:
`presentServers` 收成一个漏斗之后里面有 `show(` 与 `refreshIfVisible(` **两个**调用点,
而判据是 `strings.Contains(整个函数体, "canEdit: canEdit")` —— **一个调用点替另一个
满足了断言**。判据因此下沉到**每一个实参表**(`swiftArgumentIsPlainly`)。
行为上改掉四件,每件都是用户看得见的:`runningServerName` 对同一主机上的两台
**有歧义也说「说不出」**(此前自信地报第一条,导致填实的 ● 落在错的那台、吞吐峰值
**按错名字永久落盘**);`Test All` 对只有一台的清单不再什么都不发生;两个改清单的
动词在拨号之前**自己再查一遍能力门**(`NSMenu.popUp` 是嵌套事件循环,画出 `⋯` 到点
下去之间窗口可能已被重画);删除确认框说得出「这一台此刻正在承载你的流量」。
## 菜单窗口第一次有了闸门:离屏快照(2026-09-17)

**「菜单那半 Go 测试一行都盖不到」这句话从今天起不再成立。** 它一直是本文件里
「真机未验」清单最长的那一段,而它成立的前提只是**没人试过**:

- `NSView.cacheDisplay(in:to:)` **不需要窗口上屏**就能把视图树渲染成位图;
- `.prohibited` 激活策略下 `makeKeyAndOrderFront` 实测 `occlusionState = hidden`、
  `app.isActive = false` —— 窗口不画到屏幕上、不抢焦点,**可以在人正常工作时跑**。

`scripts/snapshot-macos-menu.sh` 走的是**真实路径**:真实 wire JSON →
真实 `RuleList` 解码 → 真实 `ruleGroupRows`/`ruleRows` → 真实 `RulesWindowController`
→ 离屏 PNG + 视图树 dump。另写一份渲染代码就是这个仓库最忌讳的「两份清单」。

**两种产物,用途不同,别混**:
- **PNG 给人(和给能读图的 agent)看** —— 布局、截断、对齐、空状态。
  它**不适合当闸门**:像素比对换个系统版本字体一变就全红,而一个会偶发红的闸门
  比没有闸门更糟。
- **视图树 dump 给守卫看** —— 每个控件的 frame 与右边界。「控件超出了内容宽度」
  是确定性判定,不是审美问题。

**判据量的是 alignment rect 不是 frame**:Auto Layout 定位用前者,而 `NSTextField`
的 frame 比它每边大 2pt(焦点环)。按 frame 量会让每个标签都「越界 2pt」,
于是闸门恒红。**内边距由 dump 自己报出来,守卫不写魔法数字** —— 判「行活在内边距
里面」而不是「别超过窗口宽度」,后者会放过下面那个真实缺陷,因为它确实没有超过。

**它抓到的第一个缺陷,也是它存在的理由**:规则窗口的 `Show`/`Hide` 按钮
`maxX = 420`,正好压在窗口右边缘(内边距本该 18),被滚动条盖掉半个、点不到。
根因是**五扇窗口各抄了一份布局组装**,而那份拷贝里的行从不被钉到容器宽度 ——
竖直 stack 的 `.leading` 对齐让每行按**固有宽度**布局,压缩永远不触发,
AppKit 就老老实实把它画到窗口外面,**而且不报错**。

判据收进 `apps/macos/BxMenu/Sources/BxMenu/MenuLayout.swift`(`makeScrollingStack`
+ `pinToEdges` + `addFullWidthRow`),五扇窗口共用一份。**两条守卫都要**:
`TestMacMenuWindowsKeepEveryControlInsideTheContentWidth` 抓**结果**(只覆盖今天
有 fixture 的那扇),`TestMacMenuWindowsUseTheSharedFullWidthRowPrimitive` 抓**成因**
(对每扇 `*Window.swift` 都成立)—— 少了成因那条,新加的窗口会静默地带着同一个
缺陷出生;成因那条当场就抓到了我自己漏掉的第五扇(`DeployWindow.swift`)。

**加一扇窗口的成本是一份 fixture 加十来行**,清单在 `Snapshots/main.swift` 里。
今天只覆盖 Routing Rules —— 五扇共用同一套原语,所以这一扇量到的结果对另外四扇
有**指示性**,但那不等于验过了,**别把它读成验过了**。

**仍然答不了的**:手感、动画、VoiceOver、跨 macOS 版本的控件差异;以及
`NSStatusItem` 的那个菜单本身(它不是窗口,这条路够不着)。快照是静态的。

**CI 上它真的在跑(2026-09-17 第一轮实测)**:GitHub 的 `macos-latest` runner
有可用的 WindowServer,快照四件产物齐全、守卫在那上面执行。脚本仍保留「没有
WindowServer 就明说 SKIPPED 并退 0」那一支 —— 「跑不了」与「跑了没过」必须分开,
而那一支今天在 CI 上走不到,只对本地 headless 会话有意义。
**同一轮还栽了一次**:`macos-app` job 此前从不装 Go,而这条守卫是 Go 测试 ——
本机 verify 全绿(本机当然有 Go),推上去 `go: command not found`。
又一次「一边的绿不替另一边背书」。

### 守卫的七种失效写法 → `docs/lessons/guard-antipatterns.md`

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

## 菜单精简:18 行 → 11 行,子菜单从此可用(2026-09-08,真机未验)

项目所有者原话「bx 菜单感觉有点复杂了」。复杂的根源两个:五行数据里四行是**诊断值**
(DNS / Direct lookups / UDP Relay 正常时天天一个样,按「只在真有问题时才占地方」它们
不该常驻);十个动作**按功能平铺**,天天点的(Turn Off)与一年点一次的(Set Up a New
Server、Uninstall)并排。现在「已连接」是:`Via reality@vps · 293 ms` 一行(判据
`compactMenuRows`,纯函数:Route+Latency 合并、诊断行只在 ✗ 时露面、维护挂起与认不出的
新行原样保留 —— 默认参与显示,吵的失效好过安静的)、版本行、Turn Off / Reconnect、
Routing Rules… / Servers… / Traffic by App… / Check for leaks、`Troubleshoot ▸`
(Check for Problems / Open Logs / Uninstall)、Quit。「Set Up a New Server…」搬进服务器
窗口当按钮(它说的就是服务器这件事)。**「Replace Configuration…」并没有搬进去
(2026-09-12 更正)** —— 窗口里取代它的是「Add Server…」,这是 2026-09-09 那次刻意定的
(`TestMacMenuServersWindowOffersAddServerNotReplace`);它只在没有服务器窗口的旧
Guardian 上留在一级菜单(`replaceConfigurationLivesInMenu`),否则换服务器又只能开终端。
原话让 Servers 窗口那一支的实施者照着加了一个按钮、撞上那条守卫才回滚 —— **一句指着
已被明确否掉的做法的记述,正是本仓库定义的第一类缺陷。**

**子菜单此前不能用,根因顺手修了**:菜单开着时每 2 秒 `removeAllItems()` 重填,展开的
子菜单会被拆掉 —— 本仓库两次因此选窗口不选子菜单。现在 `rebuildMenu` 攒一份草稿、
`defer { commitMenu(menu) }` 落定,`commitMenu` 先比**渲染结果的签名**
(`menuSignature`:标题/富文本/可点/图标名/动作名/子菜单递归),变了才就地换 item;
稳态下菜单开着也不再每 2 秒闪一下。带计秒的状态(Connecting Ns)每秒签名都变、照旧
每拍重建,它们本来就没有子菜单。**「菜单对象始终是同一个」那条不变量没动**
(`TestMacMenuRebuildsMenuInPlace` 改成钉 commitMenu:不换对象、先比签名再清空、
`removeAllItems()` 全文件只许在 commitMenu 里出现一次 —— 别处清空会绕过签名比对)。
守卫 `internal/cli/macos_menu_compact_test.go` 三条(已连接只摆压缩行、Troubleshoot
装全三项且一级菜单不再有它们、Replace Configuration 由能力门控 + 窗口回调接上),
六条变异各咬中一条。**真机未验**:子菜单展开时菜单开着 2 秒一拍是否真的不再拆它、
服务器窗口那两个按钮(`New Server…`/`Add Server…`)、Via 行的观感。

## 菜单第一行改成开关(2026-09-08,真机未验)

项目所有者要的是「滑动开关,符合苹果设计」。控制中心的形态:`[盾] Protection …… ◉━━`,
第二行暗色小字是连接摘要(`reality@vps · 293 ms`,✗ 时系统红;进行中时是
`Connecting… Ns`)。「Turn Off bx」「Start Protection」两个文字项删了 —— 同一个动作在两个
状态里有两个名字,用户要先读一遍才知道现在是开是关;开关本身就是状态。

- **判据纯函数** `protectionSwitch(state:inFlight:)`(`ProtectionSwitch.swift`):只在有东西
  可拨的三个状态显示(connected / warning 开、off 关);没装、没 setup、缺文件不画,保留
  各自的文字动作。**进行中停在目标位置、禁用**(弹回再跳过去是两次动画,禁用是不让连拨)。
  **位置永远来自状态,不来自点击**:失败就弹回,与「Last operation failed」那行一起说清。
- **拨动接回原来的 `startBx` / `turnOffBx`**,确认、免密、逃生口一个字不动。菜单拨完
  不关:开关变灰、进度写在它下面,用户刚拨的地方就是他在看的地方。
- **视图** `ProtectionSwitchRow.swift`(AppKit,一行测试都盖不到)挂在 `NSMenuItem.view`
  上;左边距对齐普通菜单项的图标列与文字列。它的 `signature` 进 `menuSignature`,否则拨完
  菜单不会重画(变异实测)。代价:自定义视图的菜单行没有键盘高亮导航,VoiceOver 读得到。
- 守卫 `internal/cli/macos_menu_switch_test.go` 三条:拨开/拨关接的是原入口且位置由
  `protectionSwitch(state: menuStateKind(), inFlight: toggleInFlight?.action)` 判、四个分支
  都摆了开关行且文字项不再有、签名算全(**这条第一版整文件查 `enabled ?`,变异照样绿**,
  收窄到 `signature = […]` 那一段才咬中)。`TestMacMenuPutsConstructiveActionBeforeDiagnostics`
  的 `.off` 锚点从「Start Protection」改成开关行。
- **真机要看**:开关行与其它项的左对齐、`.small` 尺寸的 NSSwitch 在 32/46pt 行高里的观感、
  拨动后菜单是否真的留着并在它下面显示进度、失败时是否弹回。

**设计 review 那一轮(同日,所有者要求「简洁、现代、有设计感」)又剥掉一层重复与噪声**,
现在是 9 行 2 条分隔线:① **抬头删了** —— 菜单栏图标是身份、开关是状态,「bx / Connected」
两行加一条线与开关说的是同一件事;只有**没有开关的状态**(Setup Required / Not Installed /
Updating)保留一行粗体状态词(`addHeadline`)。`.warning` 的原因写在开关下面那行、标红,
不再是单独的 Status 行。② **常驻版本号删了** —— 它只在有新版时才是信息:更新入口只剩
`addUpdateActionIfAvailable` 一处(强调色,紧贴顶部那组),平时的「装的是哪一版」搬进
Troubleshoot ▸ 里一行(`installedVersionForMenu`,只从状态里已带的版本取、不读盘);
`addVersionRow` 与 `updateShownInVersionRow` 那套「谁先画了谁」的记账一起删了。③ 开关下面
那行**服务器名优先**(`vps · 293 ms`),`reality@vps` 是传输@服务器的内部标识,协议留在
`bx status` / doctor(`menuRows` 的 Route 值改为 server 优先、transport 兜底)。④ 标题统一
Title Case,`Check for leaks ↗` → `Check for Leaks…`(其它开窗口的项都用 `…`,`↗` 不是
AppKit 惯例)。⑤ Quit **不带图标、带 ⌘Q**(`addQuit`):电源符号紧挨着保护开关会被读成
「关掉保护」。⑥ Reconnect 与四扇窗口门合成一组,Troubleshoot ▸ 与 Quit 之间不再有线。
守卫跟着改锚点(`verify_script_test.go` 的 `.warning` 原因与更新入口两条、Quit 存在性、
leak 标题大小写)+ 新增 `TestMacMenuQuitHasNoIconAndUsesCommandQ`;五条变异各红。

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

## 菜单里最后三处「没问出来」被解成好消息(2026-09-12,真机未验)

同一个错误的三个实例,都在**用户看得最多的那块面板**上。这个仓库为反面纪律花过
很多力气(`observe.Tristate`、`Status.Capabilities` 刻意无 `omitempty`、leakcheck
拒绝打印「没有发现泄漏」、速率宁可返回 nil 也不返回 0),而这三处是菜单里剩下的。
**三个都在不同的层(显示压缩 / 信号投影 / 解码),类型也不同**,所以没有抽出共用
的谓词或 helper —— 一个横跨三层的名字下面会是三个互不相干的函数体。抽出来的是
**一条覆盖整类的守卫**(见第三条),它守的是那个文件里今天与将来的每一个字段。

- **`compactMenuRows` 把 unknown 连同 fine 一起藏了**(`MenuRows.swift`)。判据写的是
  `row.mark != .bad`,而它上面几行的规则原文是「诊断行**只在 ✗ 时露面**」—— 被压缩
  的本该是*正常时的噪声*。`.unknown` 从另一头滑了进去,而这个菜单的词汇表里
  **沉默读作「查过了,没事」**。现判据是 `== .ok`。
  **「那会不会变成一行常驻的 Not checked?」不会,而且防线在上一层**:一个可能
  结构性缺席的字段由 `menuRows` **整行不发**(`Direct lookups` 就是这么做的,
  `noUpstream` 那条钉着),所以走到压缩层还带着 `.unknown` 的行,是真的问过了而没
  问出来。将来某一行在真机上恒为未知,该修的是它的**构造处**,不是回来把 unknown
  一起藏掉。代价记在测试里:全 unknown 的输入现在摆三行 Not checked —— 那个输入在
  生产里到不了(`compactMenuRows` 只在 `.connected` 那一支被调,而那一支按构造要求
  Core 答过话、隧道健康),给压缩层再加一条「headline 也未知时别重复」的特例,换来
  的只是一个到不了的画面更好看,而一条特例就是一个真 unknown 的藏身处。
- **`protectionSignal` 把「Guardian 没说」解成健康**(`TransitionNotice.swift`)。
  原文 `tunnelHealthy == false ? .protectedTunnelDown : .protectedHealthy`,头上的注释
  正确地写着「nil 是没说、**不是**不健康」—— 只防住了一头。调用方给的是
  `report.core?.tunnelHealthy`,它把 `core` 整个缺席(旧 Guardian、没接 CoreRuntime
  provider —— **升级窗口里的常态**)与键缺席摊平成同一个 nil。后果不是显示错一行:
  这个函数唯一的消费者是通知,`.protectedHealthy` 会**结束一段故障并弹一条「已恢复」**,
  根据是一个没人发过的字段;而同一份输入 `menuProtectionVerdict` 给的是 attention,
  于是通知与菜单栏图标对同一个瞬间各说各话。现归到 **`.transient`**(那一档的语义
  正是「既不算变好也不算变坏」,状态机对它一个字不说、也不改写上一次的稳态),
  **不是 `.attention`** —— 后者是拿一个缺失的键断言机器坏了,只是方向相反,而它那句
  文案会说「保护没能自己恢复」,同样是一句我们无权说的话。图标那半照旧显示
  attention:常驻指示灯说「问不出来」、事件通知保持沉默,两者不矛盾。
  `reachable == false` 不受影响(Go 侧那时把 `tunnel_healthy` 发成零值 `false`)。
- **`CoreRuntime.failingRules` 的 `try?` 吞掉读不动的报文**(`GuardianStatus.swift`)。
  旁边的注释替它辩护说「缺席 = 空,不是解码失败」—— 那句话是真的,但它描述的是
  **`decodeIfPresent` 自己的**性质;`try?` 额外买到的只有「**在场而读不动**也当空」,
  而空在下游读作「一条规则都没在失败」(接着 09-12 上一条修的那个窗口)。今天是
  潜伏的(`failingRulesFrom` 只发 direct/proxy 两种 kind,四个键都没有 omitempty),
  明天不是:Go 加第三种 kind 或给计数加 omitempty,闭合的 Swift 枚举 / 非可选 Int
  就解不动。现去掉 `try?`,回到这个文件其余十几个字段的同一档:**缺席 ⇒ nil / 默认值,
  在场而类型不对 ⇒ 整份响亮失败**。
  **抽出来的那条类级守卫是 `TestMacMenuStatusDecoderNeverSwallowsADecodeError`**:
  GuardianStatus.swift 的任何 `init(from:)` 里都不许有 `try?`。守的是**类**不是那一个
  字段 —— 下一个人加字段时照抄旁边一行是最自然的动作。判据先 `stripSwiftComments` +
  `blankSwiftStringLiterals`(上面这段解释里就写着 `try?`),读不出 `init(from decoder:`
  或 `decodeIfPresent` 时 `t.Fatal` 响亮失败。

**守卫一律钉「用户看得见的东西」**:未知的诊断行与健康的那一行**在屏幕上必须长得
不一样**(把两次压缩结果拼成串比,不是比某个内部枚举值);nil 的隧道健康**不许弹出
一条断言恢复的通知**,而**紧接着一条相反的断言**钉住真的健康时那条「已恢复」仍要发
——少了它,「干脆永远不响」就能廉价满足前一条;读不动的 `failing_rules` **不许读成
「一条都没在失败」**(判据是「要么抛错、要么非空」,不写死实现选了哪一种)。
四条变异各咬中一条。**`TransitionNoticeTests` 那条投影断言是被翻过来的** —— 它此前
以 `.protectedHealthy` 钉住了缺陷本身,注释写的理由(「不编一句隧道断了」)只覆盖
了另一半,与规则窗口那次翻过来的两条测试同一个形状。

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

## 调谐环执行 start_core(阶段③c,2026-09-05;**2026-09-13 真机已验**)

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
**真机已验(2026-09-13,项目所有者的 Mac)——而且是它自己跑完的,故障源不是改名
sing-box,是 VPS(195.133.192.92)真的不通**:日志里 `start_core` 连着
`execute_failed(wait for Core health: context deadline exceeded)`,封顶之后
**61 条 `outcome=skipped code=start_core_exhausted`**,如实说「我已经放弃了」而
不渲染成让路 —— 这一半原样成立。

**同一份日志暴露了另一半是错的,当天已修(`ec3eaf9`)**:段的定义写的是
「socket 首次不应答 → **再次应答或用户 `Up`**」,而 `resetStartCoreAttempts()` 排在
`upLocked` **成功**之后 —— **唯一需要它的场景恰好不生效**。Core 起得来的时候,
下一轮观测(`CoreSocket == True`)本来就会重置;Core 起不来的时候才需要它,而那
正是 `upLocked` 返回错误、那一行被跳过的时候。真机后果:08:23 用户从菜单按下开关、
`/v1/up` 在 `wait for Core health` 超时失败,此后 **9 小时 16 分钟**里调谐环 61 次
醒来一次都没再试,而 `desired=on`、机器上没有 Core、没有屏障,**流量明文直连**;
VPS 若在这期间恢复,bx 也不会自己回来。修法是把重置挪到 `upLocked` **之前**
(用户按下开关这个动作本身结束一段故障),**封顶那条纪律一个字没动** —— 重置只发生
在按下开关那一刻、不在每一轮,用户按一次 ⇒ 重新试满 5 次 ⇒ 再 exhausted 停住。
守卫 `TestStartCoreCapResetsEvenWhenTheUserUpFails` 钉住 up **失败**那条路径;
兄弟测试 `TestStartCoreCapResetsAfterUserUp` 喂的是**成功**那条,**那个输入让这条
性质完全不可见**。

**仍未验**:把 data_dir 里的 sing-box 改名 + `sudo kill -9 <Core PID>` 那条合成路径
(`core_unexpected_exit` → `core_restart_failed` → 循环五次 → exhausted),以及修复
之后「用户 up 失败 ⇒ 调谐环真的重新试 5 次」。
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

**一个用户自己开关的悬浮窗**(`NSWindow.level = .floating`),三组 —— 界面上的标题是
`Through the tunnel` / `Direct` / `Blocked`(`AppTrafficReport.sectionTitle`,判据在纯
模型里、有 Swift 测试盯着;**这里此前抄的是三个全大写的短名,而屏幕上从来没有出现过
那三个词** —— 下文那份真机验收清单还叫人「确认 BLOCKED 组出现内容」,照着找会找不到),
**同一个应用可以同时出现在多组**(Chrome 一部分
域名直连、一部分走隧道是常态,压成一行「混合」等于把最有用的那一半扔掉)。
**不进菜单栏常驻** —— 与此前否掉「Direct rules: N unreachable」常驻红字同一条
判断:常态不是事件,会变墙纸。

**包**:`internal/appattr`(**纯判据**,`purity_test.go` 按 AST 禁 net/os/exec/
syscall/x/sys)· `internal/supervisor/appsource_{darwin,other}.go`(平台原语)·
`internal/supervisor/apptraffic.go`(订阅、环形缓冲、按端口字节账)· Core
`/v0/apps` → Guardian `/v1/apps`(`authorizeOwnerPeer`,与 `/v1/rules` 同一道门)
→ 菜单 `AppTrafficModel.swift`(纯函数)+ `AppTrafficWindow.swift`。

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

### 观感与可行性:过程搬走了

可行性 spike 的真实数字(`unix.SysctlRaw("net.inet.{tcp,udp}.pcblist_n")` 纯 Go 拿得到
PID、对 `lsof` 一致 29/不一致 1/缺失 3、全表 451µs~1.5ms)、观感那一轮的每一处取舍
(七列的由来、搜索框为什么住在重建的视图树之外、目的地小字的 `+N`、速率从客户端做差
改成服务端按端口做差)、以及守卫在这一支上被攻破的两次,全在
`docs/lessons/2026-08-app-traffic.md`。下面留判据。

**改这块最容易静默出错的三处**:① `appTrafficNumericColumns` 的下标 ——
`53a4f0a` 把图标搬进应用名格、列从八降到七之后每个下标都要往前挪一位,挪错**不会有
任何编译错误**,现象只是「右对齐落在错的列上」;② **速率的 `nil` 与 0 是两件事** ——
前者是「不知道」(第一次采样只立基线),后者是「量出来就是 0」,压成同一个 0 会把
「不知道」显示成「闲着」;③ **搜索框必须在 `ensureWindow()` 里创建一次**,长在每 5 秒
被拆掉重填的那棵树里的话,用户打两个字就会连同焦点一起消失,而窗口看起来完全正常。

### 真机未验(全部)

悬浮窗、心跳、陈旧横幅、三组渲染、能力门控 —— **没有人在屏幕前点过**。
**但「列宽会不会把规则挤没」这一条已经被真机回答了,而且答案是「会」
(2026-09-13 更正:此处此前还挂着它,而它三周前就有答案了)** —— 2026-08-20
那一版八列表格上过屏幕,截图里图标列自己涨到 ~350pt 把后面的列全挤扁,
`53a4f0a`(2026-08-22)因此把图标搬进应用名格、列降到七,并加了搜索框与目的地
小字。**改动本身随后又没上过屏幕**,所以仍未验的是**那一版之后**的观感:七列
在默认窗口宽度下的分配、图标取不到时应用名那一格长什么样、搜索框过滤时三个分组
标题都还在不在、目的地小字的 `+N` 与 toolTip。速率还有一条只有真机能验:
**第一拍必然是破折号**,第二拍起才有数 —— 若真机上它**一直**是破折号,说明
`refreshIfVisible` 那条路没走到(而窗口看起来完全正常)。窗口那半
AppKit 代码本仓库一行测试都盖不到(只有 `swift build` 证明能编译、一条文本守卫
证明那句小字被摆进了视图树)。真机验收清单:① 三组都在、腾讯会议出现在预期的组里;
② 关掉窗口后 CPU 与拨号回落,30 秒后再开数据从零开始;③ 制造 kill-switch 阻断
确认 `Blocked` 组出现内容;④ `unknown` 占比不高 —— **2026-08-20 起这条在任何时刻
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

**四条不许动的细节**(数的是下面的条目 —— 此前这里写「三条」而列了四条):
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

`Run` 里那一串有序 `defer` 改走 `internal/supervisor/teardown.go` 的
`teardownLedger`。**动它的理由不是好看**:defer 是同步的,一步拆除挂住,它
后面的每一步都不会跑,只有关机 watchdog 强制退出兜底 —— 而强制退出会把
**剩下的还原全部跳过**(run.go 那条 watchdog 的注释里早就点名了嫌疑:
`eng.Close`/`tun0.Stop`)。台账给出 defer 给不了的三件:**逐步限时**
(一步挂住只损失那一步)、**命名与记录**(关机时查得出卡在哪,此前只有一份
goroutine dump)、**可断言**(顺序是数据)。

**四条不许动的细节**(数的是下面的条目 —— 此前写「三条」而列了四条,只有把没加粗的
那条第四项不算进去才对得上,而它一样不许动):
- **LIFO 一字不改**:后获取的先释放 —— 路由还原必须排在关 TUN 之前;
- **`defer teardowns.unwind()` 的位置承重**:落在第一个系统资源之前。混着
  来(一半 defer 一半台账)会把两者的相对顺序悄悄颠倒;迁移前后逐条比对过
  `signal.Stop → 台账 → cancel` 与原状一致。**步数这里不写了**:原文写着
  「九处 defer」而同一次迁移落进台账的是 11 处(迁移提交 `60fdfe2` 自己的
  commit message 就同时印着这两个数,十二行之内自相矛盾),今天又长到了 13 处 ——
  **这个数不承重,承重的是 LIFO**;真要数就 `grep -c 'teardowns.push' internal/supervisor/run.go`;
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

`Run` 里那一批长命 goroutine 此前是裸 `go f(ctx)`、**零个 `recover()`**。
裸 goroutine 的 panic 不会被 `Run` 的 defer 接住 —— Go 当场终止进程,而
**别的 goroutine 的 defer 一个都不会跑**:进程没了而内核里的 ip rule /
策略路由**还在**,整机流量指向一个已经不存在的 TUN。现全部经
`internal/supervisor/workers.go` 的 `workerRegistry.start` 启动(具名 +
panic 收在自己那一层)。

**一律 recover-and-continue,是逐个想过的结论不是图省事。这份枚举的全部意义
就是回答「这个工人死了会静默降级成什么」,所以它必须是全的** —— 判据不是记忆,
是 `grep -n 'workers.start' internal/supervisor/run.go`,那里出现几次就该有几条:
mutation-engine 死 ⇒ 切服务器不工作 · tailscale-bypass 死 ⇒ 旁路停在兜底表 ·
transport-failover 死 ⇒ 不再自动切备(**kill-switch 仍在**) ·
direct-egress-repair 死 ⇒ 直连出口不再自愈(2026-08-13 那个 bug 会回来) ·
rule-history 死 ⇒ 历史停止累计 · **server-bypass-refollow 死 ⇒ 服务器换了 IP
之后旁路再也不跟过去**(NAS 上那次静默断网一个月,见下文「服务器旁路重新跟随
DNS」)· **server-bypass-route-repair 死(仅 darwin)⇒ 休眠唤醒后被冲掉的
`/32` 旁路不再自愈,隧道成环**(2026-09-04 那次 13 分钟断网,见下文「休眠唤醒
后隧道成环」)· china-list-refresh 死 ⇒ 列表不再更新。
**后两个是 2026-09-04 加的,而这份枚举当时没跟上,直到 2026-09-13 的审计才补** ——
它们各自有一整节讲「没有它会断成什么样」,却恰恰从这份「少了谁会怎样」的清单里
缺席,是本文件反复出现的那个形状:**新写一节描述自己,顺手让旧的一处计数变成假的。**
这几件里没有一件值得用「一台受保护的机器断网」来换。**但代价写明了**:炸掉的
工人就此不再跑,那是一次**静默降级** —— 它打一行带栈的日志,并把名字记进
`panicked`。**但别把那份记账读成「少了哪个后台循环答得出来」**(这里此前就是
这么写的):`names()`/`panickedNames()` 至今**零生产调用方**,既不进 `bx status`
也不进控制 socket,今天唯一的办法仍然是翻日志。

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

## `/v1/logs` 与 `internal/doctor`:判据只有一份(2026-09-09)

`internal/doctor`(`doctor.go`)是**纯判据包**——`Judge(Facts) Report`,**本包自己
的文件**不 import net/os/exec/syscall(`purity_test.go` 按 AST 钉住;传递依赖不在
守卫范围内 —— `config` 自己就会拖进 net/os,本包用到的只是它的类型),
`internal/cli`(`doctor_facts.go` 只采集)与将来的 `/v1/doctor` 共用它。
`bx doctor --json` 与文本路径现在都是「采集 → `doctor.Judge` → 渲染」,
文本只是同一份 `Report` 的另一种打印(`renderDoctorReport`,返回三段式的
`doctorLineSpec` 而不是 `status|key|value` 串 —— 规则原文与错误文本里真的会带
`|`,按分隔符切回去会把一行切错而不报错),不再是第二份手写判据。**只服务文本
路径的那份孪生判据 `darwinServiceDoctorLines` 已删**(没有调用方,而有测试盖着 ——
那与没有判据在输出上完全一样,却会让下一个人以为文本路径还有第二份判定)。
守卫:`TestClientDoctorIsJudgedByTheDoctorPackage` 逐字钉住
`collectClientDoctorWith` 只有那一句、`collectDoctorFacts` 不含判定用语,
`TestDoctorTextPathRendersTheSharedReport` 钉住文本路径不再自己采集,
`TestJudgeGolden`(`internal/doctor/testdata/judge_golden.json`,长路径与权限退路
各一份)把判决**逐字节**钉住 —— 逐条断言名字与状态挡得住「少了一行」,挡不住
「detail 少了一个字」。`doctor` 不能 `import guardian`,**会成环**(§3 里 guardian
要调本包),DNS 三态常量各写一份,`TestDoctorDNSStateConstantsMatchGuardian`
守跨包不漂。**`/v1/logs`**(`internal/guardian/logs.go`)经 owner 门发布 Guardian
与 Core 日志尾部,路径来自 `install.GuardianLogPaths`,能力声明 `logs`
(`CapabilityLogs`,值本身由 `TestLogsCapabilityIsDeclared` 钉住 —— 菜单
`LogsModel.swift` 按字面量门控,改了值菜单就永久看不见日志页而两侧都不报错)。
菜单失败弹窗现带 **Show Details** 打开这份日志页,取代此前指向 root 0600 文件、
非 root 打不开的路径。**那句文案与那个按钮共用同一道能力门**
(`guardianFetchFailureInfo` 吃 `logsAvailable:`):旧 Guardian 上按钮画不出来,
文案就改说「原因记在 bx 的日志里」而不是许诺一个找不到的按钮。**真机未验**:
Show Details 按钮高亮、Open Logs 打开的日志页渲染。

## `/v1/doctor` 与 Add Server:诊断面搬进 Guardian(2026-09-09)

`/v1/doctor`(`internal/guardian/doctor.go`,能力 `CapabilityDoctor`)在 Guardian 进程内
采集,喂同一个 `doctor.Judge`(与 `internal/cli` 的 `collectDoctorFacts` 同一份判据的
第二个采集方);整轮共享一个 10 秒预算,每个依赖都吃同一个 ctx
(`TestCollectDoctorFactsGivesEveryDepTheSameDeadline`)。`probe` 是**控制面的一次
TCP 往返**不是完整握手,它与 launchctl 查询只在用户显式点击那次 GET(已过 owner 门)
才发生。生产那几个原语自己也吃这份 ctx,由
`TestLiveDoctorDepsForwardTheCtxTheyAreHanded` 按**行为**钉住(对着一个会 accept
但永不应答的 socket,250 毫秒的预算必须在预算内回来)—— 此前那条守卫注入的是测试
自己的闭包,「采集把 ctx 递下去了」与「生产闭包接过它之后照旧用 context.Background」
在它眼里一模一样。平台检查下沉 `internal/platformcheck`(cli/Guardian 共用 `Collect`;
**它不是叶子包** —— 自己引 doctor/leakcheck/supervisor,纪律是**不许反向依赖
guardian/cli/install**,采集包被它的消费方引就成环);
`internal/protectionstate` 同理——darwin 上 leakcheck 测试引 guardian、guardian 引
platformcheck、platformcheck 又用 leakcheck 判据,首尾成环,下沉后「两边常量还一样」
那条字面量守卫**退场**,漂移在构造上不再可能。Diagnostics 窗口现两页(Logs /
Checks),Checks 只由显式点击喂数据(`TestMacMenuDoctorPageIsFedByFetchDoctor` 钉住
`fetchDoctor`/`openDiagnosticsChecks` 两处调用点)。**Add Server** 取代 Replace
Configuration:`servers add` 同名 409、名字可省略时 Guardian 用 `setup.LinkHost`
推导(认 `bx://` 换壳);旧 Guardian 上 Replace Configuration 仍留作降级路。新增
`TestMacMenuShellOutsStayOnTheAllowlist`:shell-out 只许落在 spec §1 那七个函数。**Checks 页真机已拉到过数据**(Guardian 日志
`guardian_doctor_result uid=501 ok=true checks=19 elapsed=188~451ms`,2026-09-10/12 共 11 次
请求)—— 端点、owner 门、19 项检查、耗时都坐实了。**仍未验的是判据本身对不对**:
Checks 页与 `sudo bx doctor --json --skip-probe` 逐条对比(**Guardian 那份
永远会探测**,它没有 `--skip-probe` 这个概念,故 Checks 页比 CLI 那份多一行 `probe`
是预期的,不是漂移)、Add Server 三种结局、两页布局。

**真机验收当场抓到三条缺陷(2026-09-10,已修;前两条是判据,第三条是界面)**:① **关掉保护被说成故障** ——
用户 `bx down` 之后 `guardian_dns` 报 `fail` 并 hint「sudo bx up」,而 DNS 还给系统
正是关闭态该有的样子;新 Checks 页把这条红字顶在最上面、合计写「1 failed」,一台
完全正常的机器被说成坏的(与 Tailscale advisory 当初同一形状)。判据当时只看
state/managed,没有意图这一项。现 `doctor.GuardianFact` 带 `Desired`,`DNSCheck` 吃它:
关着且已还给系统 ⇒ ok;**关着却仍占着 DNS ⇒ warn**(那是调谐环 restore_dns 要处理的
真残留,不许被这次豁免一起判绿);意图问不出来时按「要保护」判(宁可多报,不漏
「DNS 被别人接管」)。② **ok 的行在教人修没坏的东西** —— `ok service_active` 底下挂着
「→ sudo bx up」,两个渲染层都是「hint 非空就画」。现抹在 `Report.AddReport` 这个
**唯一入口**里(`AddCheck` 也走它),不靠十几个产出点各自自觉。golden 新增
`desired_off` 一例把关闭态逐字节钉住,原有两例逐字节未变。③ **重画之后停在旧的滚动
位置** —— 打开 Checks 页第一眼看到的是最末尾几行 OK,合计句与唯一那条 WARN 全在屏幕
外面;一个以「坏的排前」为卖点的页面,第一眼给的恰好是最不重要的一端。同一件事还让
Run again 看起来没反应(健康机器上两份报告逐字相同,重画完画面不动)。现两页渲染完都
调 `scrollToTop`(先 `layoutSubtreeIfNeeded` 再滚 `.zero`,少了前者滚的是按旧内容算出
的坐标),Checks 页另加一行 `doctorCheckedAtLine` 的时间戳(带秒 —— 只到分钟连点两次
仍看不出),守卫 `TestMacMenuDiagnosticsPagesReturnToTheTopAfterRendering` 钉在两页各自
的函数体里,三条变异各咬中一条。

### 已知缺口:Checks 页把服务端的中文原样画进一个英文界面(2026-09-17)

离屏快照第一次把这一页画出来之后看见的:

```
1 failed · 1 warning · 1 not checked          ← 英文
info  rule_dead_rules          未检查:累计运行 12 天,不足 14 天,这一类还不能下结论
info  rule_builtin_list_check  未检查:global 模式下内建 china 列表整个不生效,这一类没有比对
ok    traffic_outcomes         直连 1896(失败 192)· 代理 8496(失败 18)
```

**上面三条是 2026-09-17 从项目所有者机器上 `bx doctor --json` 取的真实输出
(19 条 check 里有 3 条 detail 含中文)。** 这一段最初写的是一个**我编造的**例子
(`FAIL guardian dns 系统 DNS 没有指向 bx`)—— 那是快照 fixture 里我自己造的数据,
不是产品真实产出的话。缺陷本身是真的,例子是假的;**一条用假例子撑着的记述,
下一个人按它去 grep 会一无所获,然后连带不再相信这一整条。**
同一轮我还因此误报过 Traffic by App 的 Rule 列"也有混排" —— 实际那一列只放
用户规则原文(`*.qq.com`),中文是我 fixture 编的,已撤回。

**与规则窗口 2026-09-11 修掉的那条是同一个形状** —— 服务端写的是中文,而菜单
通篇英文。但**那次的修法搬不过来**:规则窗口能修是因为 `rulereview.Class` 是
闭合枚举,客户端按 class 映射成英文即可(`ruleVerdictText`);而 doctor 的
`detail` 是**自由文本**,映射不了。

三条出路,**都是产品决定,不是 bug 修复**,所以一条都没选:
① doctor 改发码 + 英文文案(客户端映射,与规则窗口同构,但要给每条 check 定码);
② 接受中英混排(今天的状态);
③ 让 doctor 的 detail 本身变成英文 —— 但 `bx doctor` 的 CLI 是中文的,
   那会让同一份判据在两个出口说两种语言,是更坏的分裂。

**记在这里是因为它以前看不见。** 这一页的渲染此前没有任何闸门,而
「真机未验」清单里它只是一行字;有了快照之后它变成了一张图上明摆着的东西。

## 流量成败进 Judge,「没查」不许读成「没问题」(2026-09-12,真机未验)

**升级本身会让一整类诊断消失,而且是被一句相反的话顶掉。** 「哪条规则在成片
失败」(2026-08-13 那个签名:`*.qq.com` 1291 条失败 1289、Steam 图片全裂)此前
只长在 `bx doctor` 的**文本**路径上 —— `cli.go` 里那个 for 循环,注释还写明
「不在 --json 契约里」。而菜单的「Check for Problems」自从 Guardian 声明
`doctor` 能力起走的是 `/v1/doctor` → `doctor.Judge`,那条路上没有人采流量成败。
于是 Checks 页对那台正在成片失败的机器一个字都不说,顶上还加粗写着
`0 failed · 0 warnings`;`bx_inspect` 的 `ok` 同源,agent 拿到的是 `true`。

修法两半,**第二半才是真正闭合缺陷的那个**:
- **判据搬进 `internal/doctor/traffic.go`,三个消费方共用一份**:`doctor.Facts`
  多一个 `Traffic *TrafficFact`(数据,不是让 Judge 自己去拿 —— 本包纯度守卫
  按 AST 禁 net/os/exec),`internal/cli` 与 `internal/guardian` 两个采集方各自
  填它,文本路径那一段 fork 删掉。与 `bx status` **仍然同源**
  (`stats.FailingRules`/`UDPNotice`,纯度白名单为此收了 `stats` 与 `tristate`
  两个只做计算的包,理由写在名单里)。多条失败规则合并成**恰好一条** check
  (`traffic_failing_rules`)—— 与 `riskyRuleFinding` 同一条:同名 check 会让按
  名字取的消费方静默丢掉其余结论。
- **第四种状态 `not_checked`**:采集方没填(nil)与问不到(Err)都产出一行,
  措辞不同、都不缺席。`Report` 多一个**与 `ok` 并列**的 `not_checked` 计数
  (刻意无 omitempty),菜单合计句变成 `N failed · M warnings · K not checked`
  (K=0 也照写)。**`Report.OK` 的含义一个字没改**(仍是「没有一条 fail」):
  让「有一项没查」把 OK 打成 false,等于宣布一台用户自己 `bx down` 的机器坏了
  —— Core 没在跑时流量必然查不到,而那正是关闭态该有的样子(2026-09-10
  `guardian_dns` 栽的同一形状)。代价由那个并列的计数抵掉,理由写在字段上。

**守卫钉的是缺陷本身**:`TestJudgeMakesUncheckedTrafficLookDifferentFromHealthyTraffic`
断言「没采到流量事实」的报告与「查了、一切正常」的报告**在渲染得出来的行上**不同
(不是在 Facts 上不同 —— 那是缺陷旁边的东西);`TestGuardianDoctorFactsCarryTraffic`
钉住菜单走的那个采集方真的问了;Swift 侧 `testSummaryLineSaysHowManyWereNotChecked`
钉住合计句。golden 从三例加到四例(新的 `failing_rules` 是唯一一份 traffic 真查出
东西的报告 —— 少了它,这次改动可以整个被撤掉而 golden 不动)。

**它当时留了一格空的,同日补上:Guardian 的 `TrafficFact.DirectEgress` 恒
Unknown。** 那一格正是这份诊断最值钱的一句话 —— 2026-08-13 真机上十条 direct
规则 100% 失败,坏的不是规则,是 bx 自己的直连器(macOS 上那条 scoped 默认路由
不见了);恒 Unknown 时 Checks 页会一本正经地建议用户去改那些**完全正确**的规则。
接法是**用同一份判据**:新的 `observe.DirectEgress(ctx, deps)` 是这一格的单问
入口(与 `Observe` 走同一个 `observeDirectEgress`,只是不跑整轮 —— 那要多两次
路由查询、一次 DNS 查询、一次控制 socket 往返,而 doctor 那一轮只有一份预算),
Guardian 的 `doctorCollectorDeps.directEgress` 接的就是它;**`(reachable, known,
err) → Tristate` 那段映射仍然只有一份**,没有第二个 `supervisor.DirectEgressReachable`
调用点。**nil ⇒ Unknown,不是 True** —— 判成好的就等于让那句错的建议照旧发出去。
观测本身只有 darwin 有原语,别处由 `NotApplicableForPlatform` 声明为不成立、
不去问(问了只会每次留下同一条永久失败)。守卫两条:
`TestGuardianDoctorBlamesTheDirectDialerNotTheRules` 打在**渲染出来的 hint** 上
(观测到 False ⇒ 不许再说「改 rules」;没问出来 ⇒ 不许说「不是你的规则」),
`TestDirectEgressAsksOnlyThatQuestion` 钉住单问入口不顺手问别的、吃调用方那份
ctx、且不成立时不去问;`TestLiveDoctorDepsForwardTheCtxTheyAreHanded` 多一条
子测试,判据是**认不认账**而不是快不快 —— 用自己的钟的实现在这台机器上也是几
毫秒回来,只是会给出一个**确定的**答案,那是它唯一看得见的形状(非 darwin 上
这一条是弱的,记着别当成三条腿都在守)。`bx doctor --json` 的 golden 一个字节
没动:判据层没改,补的是采集。

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

### explain 的两句判决(2026-09-14,真机未验)

**分类到处置之间那一跳此前一直留给读的人自己走。** 失败分类从 2026-09-01 就印着
(`[route unreachable×410 peer did not answer×15]`),而「所以该怎么办」写在 `internal/dialfail`
每个常量的注释里、抽成过一个判据 `LooksLikeOurFault`,**零生产调用方** —— 判据
写下来了、测试盖着,从没有一个字到过屏幕上。现在是 `Blame` 那一行(2026-09-14 落地时它叫「判决」,2026-09-17 随整条 explain 改英文)。

- **`dialfail.Blame` 四态取代了那个 bool,零值是 `BlameUndetermined`。** 两态之下
  `Other`(认不出)与 `Timeout`(确知是对端的问题)返回同一个 false,于是
  「我判不出来」被渲染成「不是 bx 的问题」。新加类别忘了分类时由一条 AST 穷举
  守卫红一次 —— 否则它静默落进零值,**而零值读起来像「想过了判不出来」**。
- **`dialfail.Dominant` 的门槛是严格多数,不是「最多的那一类」。** 40/30/30 里挑
  一个说成主因就是编答案;正好一半更不行 —— 5 个路由不可达 + 5 个对端不应答是
  两句处置完全相反的话。没有主因就不说话,那一行的 `[×N ×N ×N]` 拆分本身已经
  说明「它不是一个原因造成的」。
- **选样本的判据是「哪份答得出这个问题」,不是「哪份有失败」。** 累计那份可能有
  8000 次失败却一个分类都没有(旧版本记的、或这一轮还没落盘),按后者会把唯一
  答得出问题的样本整个扔掉,**而输出与「这一版不下判决」逐字节相同**;选中的那份
  说不出主因时也不回落到更小的那份。读的是哪一份必须印出来。
- **指向对端的那一档绝不断言对方的状态**(本机没网时同样表现为「没收到回应」),
  渲染出来的话里**没有 markdown、没有反引号** —— 用户读到的是字面符号,而这个
  仓库刚被反引号坑过一次(文档里反引号包着的命令被 zsh 当成命令替换执行)。

**第二句是 `Review` 行:这条规则该不该留。** `internal/rulereview` 那四类加死规则
整份早就在(`bx doctor`/`bx status` 都在用),而 explain 此前不调它。
**explain 是预言不是观测,所以五类都够得着** —— 它报「会命中哪一条」,于是一条
从来没命中过的死规则照样会被点名。四条判据:**按 kind 分开查**(同一条原文可同时
在两张表里、语义相反)· **全部结论都说不是第一条**(一条规则可以既危险又被盖住,
取第一条会静默丢掉其余安全结论)· **没有用户规则可点名时一个都不给**(内建/默认
那一档没有哪一行是用户写的)· **`CoveredBy` 只在真有时才拼**(死规则没有「被谁
盖住」这回事)。体检对着 **`RuntimeState.ConfigPath`** 算 —— 拿错输入而判据没错
正是 wrong-reference-object 那类事故;两条取数路与 `bx doctor` 逐字同源,
**只有权限不足**才退到 Guardian 的 `/v1/rules`(配置**不存在**是「还没 setup 过」
这个真问题,拿 Guardian 的答案盖住它是掩盖故障),且要比对 config_path 同不同。
**任何一步问不出来 ⇒ 一个字都不说,绝不渲染成「这条规则没问题」。**
两句都同时进 `--json`(`tcp_rule_findings`/`udp_rule_findings`,顶层既有字段
一个没动)—— 只长在文本路径上的诊断正是 `bx doctor` 那次「被一句 0 failed 顶掉」
的形状。

**真机状态**:`bx explain` 本身**已验**(当场答出 `*.qq.com`:命中 `*.qq.com`、
本次 78 次/15 次失败、累计 2726/425)。**失败分类与累计口径未验** —— 升级后第一件
事就是 `bx explain qq.com`,看那些失败是 `route unreachable` 还是 `peer did not answer`:前者是
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
<服务器IP>` 模拟),看 `bx.log` 在 30 秒内出现 `server_bypass is broken` →
`server_bypass reinstalled the routes`,且 sing-box 的 EOF 刷屏停止、隧道回绿,
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
在 2–7 分钟内出现 `server_bypass_refollow: the server's address changed` 且隧道自己回绿。

## Core 起不来时,说出它为什么起不来(2026-09-13,真机未验)

**所有者原话:「vps 之前不通,但 bx 不会告诉我是 vps 不通,用户会以为是 bx 自己的
问题。」** 2026-09-12 那天他的 VPS(195.133.192.92)ssh 与 ping 都不通,而 `sudo bx up`
连着**七次**答 `core_ownership_uncertain` —— 三百字关于「系统里可能有第二个 Core」的
排查指引,一个字都不沾边。

**真相从第一秒就在 bx 手里**(`dial tcp 195.133.192.92:443: i/o timeout`),它是被逐层
剥掉的,而每一层都有名字:

- Core 卡在等隧道健康 ⇒ `supervisor.Run` 在**建出控制 socket 之前**就返回了
  (fail-closed,这一段本支一个字不改);那句原文只进 root-only 的 `/var/log/bx.log`,
  然后进程没了;
- Guardian 只知道「socket 20 秒没出现」—— 它从不读 Core 的日志,也拿不到它的退出原因;
- **清理去请这个 Core 从 `core.sock` 上自己退出,而那个 socket 按构造正是它没能建出来
  的东西。** `Manager.cleanupStartedCore` → `runner.Stop` 失败即 return、从不回落 kill,
  于是**真话 `core_health_failed` 被假话 `core_ownership_uncertain` 顶掉**;
- 副作用是被丢下的 Core 要等自己那 20 秒才自杀,窗口恰好盖住用户的重试 ——
  `guardian_core_still_running_on_release` 指着的正是 bx 自己造的孤儿,把用户派去杀
  一个 bx 该自己收拾的进程。

**这条链违反的是 2026-08-04 那次 71 分钟事故立下的规矩:停止路径不许依赖别的先成功。**

### 两条结构性事实,动这块之前必须知道

**① `supervisor.Run` 的顺序**(`internal/supervisor/run.go`,按出现先后:
`awaitTunnelHealthOrDiagnose` ≺ `plat.OpenTUN` ≺ 控制 socket
(`serveControlWithPathRecovery`)≺ `plat.Hijack`,健康门与劫持之间隔着五百多行):
**卡在健康门的 Core 没开过 TUN、没装过路由、没碰过 DNS**,身上没有任何东西需要优雅
还原 —— 这是「清理改走强杀」能成立的**承重前提**。
**这里此前写的是四个具体行号,已经删掉**:承重的是**顺序**,而行号每改一次 `Run`
就漂一次(2026-09-13 核过一轮:308/473/792/880 全部已经不对)。要复核就 grep 那四个
函数名,别信任何写死的数字。**同一组数字在 `internal/supervisor/tunneldiagnosis.go`
与它的测试注释里还有副本,那几份也漂了** —— 留在那儿是因为本轮只动文档;谁下次改到
那个文件,顺手把它们也换成函数名。

**② 那个前提被 review 收窄过一次,而收窄才是对的。** 我(控制者)在台账里写的是
「waitHealthy 失败 ⇒ 强杀安全」,实施者拿代码顶了回来:**「健康门没过」并不蕴含
「从没服务过」** —— UDP 档没就绪、socks 探测整个窗口失败、隧道恰好抖了、升级时版本
对不上,都会让一个**已经开了 TUN、装了路由**的 Core 没过健康门,强杀它会跳过它自己的
defer 还原(linux 上 pref 150/200 那两条 ip rule 比 TUN 设备活得还久)。所以判据不是
**调用点**,是 `coreEverServed(process, state)` = **它的控制 socket 亲口报过自己的
PID**;`cleanupCoreAfterFailedStart`(`internal/guardian/manager.go`)是**唯一**决定点,
`update.go` 里 accept 健康失败那一处因此**自动**仍走协作关闭(它本来就服务过),
Verify 那两处传零值 `RuntimeState` ⇒ PID 0 与任何真 PID 都不等 ⇒ 落强杀,而那正是
「它连 socket 都没开出来」的诚实读法。判据本身由
`TestACoreThatAnsweredTheControlSocketIsNeverForceKilled` 钉住,而**能力**由
`TestTheKillCapabilityItselfHasExactlyOneCallSite` 钉住(`m.runner.ForceStop` 全包只许
一个调用点 —— 钉包装函数的名字挡不住内联一次强杀;协作关闭那一侧对称地是一份**带
理由的具名白名单**,每条写明它凭什么不是失败启动的清理)。

**顺带一条窄门,它差点让这批修复从另一扇门把事故放回来**:`ForceStop` 手里没句柄时
原先直接报错,而**占主导的那种「没句柄」恰恰是我们自己那个 Core、它已经退了**
(wait goroutine 在 `waitpid` 一返回就 forget,而死于 provision/config/tun_open/hijack
的 Core 约 2 秒就没了,Guardian 却要等 20 秒)⇒ `retainUncertain` ⇒
`core_ownership_uncertain` 原样回来,**正落在刚教会 bx 说清楚的那四个码上**。现在它
去问系统:`ErrProcessNotRunning` ⇒ 清记录 → nil(与老的 `Stop` 同一条),身份比对复用
同一个 `sameProcessIdentity`(PID 复用时不许比 `Stop` 更严 —— 更严就是又一道通向那句
假话的窄门),系统说它还在、或者答不上来才拒绝。`TestCoreThatDiedOnItsOwnIsNotReportedAsOwnershipUncertain`
逐字重现事故,`TestCoreThatNeverBecameHealthyIsKilledInsteadOfAskedNicely` 钉住主路径。

### 差点让整支修复胎死腹中的时序陷阱(实施者发现,计划里没有)

**Guardian 的健康等待 20s == Core 的隧道健康窗口 20s,而 Guardian 从不给 Core 传
`--health-timeout`,Core 那 20s 还起步更晚**(先 provision、建 router、建隧道)。于是
**Guardian 放弃的那一刻,Core 才刚开始那次判别拨号,记录一个字节都没写 —— 紧接着就被
SIGKILL**。按计划原样写,这批的核心机制在它唯一存在的那个场景(=事故本身)里
**一次都不会触发,而且不会有任何东西转红。**

修法是在健康等待放弃之后再给一段**由 `supervisor.TunnelDiagnosisTimeout` 派生**的有界
宽限(`coreStartFailureGrace`,`internal/guardian/corestartfailure.go`,不是手写秒数):
只在失败路径上跑、答案一到就返回、Core 句柄没了就收手、吃调用方的 ctx。
**没选的两条**:把 Core 的健康窗口调短(那是真的缩短隧道能用多久建起来,一次产品行为
改动 —— 一条 reality 握手在烂链路上慢一点就此起不来)、把 Guardian 的等待调长(同样长
的等待,却连「答案已经到了」都不看)。

**那 3 秒的余量没有任何测量依据,所以要记住它必须覆盖什么**:真正的要求是
`grace ≥ Core 的启动偏移 + TunnelDiagnosisTimeout`,而这 3 秒是留给那个偏移的**全部**
余量 —— 偏移里装着 `buildSplitBrain` 建 12k 域名 / 6k 网段的分流脑、`EnsureSingbox` 核
28MB 内嵌资产的缓存键(重嵌之后第一次是一次真解压)、`EnsureLists`、`buildTunnel` 加
子进程 spawn。偏移超过 3 秒时行为是安全的(空手而归 ⇒ 回落 `core_health_failed`,
不编病因),而且**现在说得出来**:
`guardian_core_start_failure_record_absent reason=handle_gone|grace_expired|deadline waited=…`
—— 少了这行,「Core 从没写」与「我们早放弃了 200 毫秒」在真机上完全分不开,而这条分支
唯一的存在理由就是可诊断性。**要调这个数,先去日志里读那个 `waited`。**

**宽限不许花清理那份预算**(review 抓到):`/v1/up` 的 60s 有余量,而**三个**
`startCoreLocked` 调用点传的是 `restartTimeout=25s` —— 崩溃重启、调谐环 `start_core`、
以及 **`Manager.Down` 里 DNS 还原失败之后那次补偿重启,一条停止路径**。裸拿外层 ctx 去
等会把清理预算从 12.5s 挤到 4.5s ⇒ 清理超时 ⇒ `retainUncertain` ⇒ 又是
`core_ownership_uncertain`。故上界取 operationCtx 的 deadline,**不自己再算一遍
`min(cleanupTimeout, remaining/2)`** —— 那就是第二份判据,而它会与 `reserveCleanup`
漂开;余量为零时**仍然读一次、一拍都不等**(config/provision/tun_open/hijack 那几种
两秒前就写完了)。而且在那三条路上宽限是**纯成本**:Guardian 的耐心 12.5s < Core 写出
「隧道那一族」所需的约 25s。守卫:`TestTheRecordGraceOutlastsTheCoresOwnDiagnosis` /
`TestTheGraceActuallyComesFromTheCoresDiagnosisBudget` /
`TestTheStartFailureGraceNeverEatsTheCleanupReserve` / `TestZeroGraceStillReadsOnce`。
**已知代价**:一次失败的 `bx up` 最坏多押住 mutation 槽约 8 秒。

### 「隧道没起来」必须一分为二 —— 而判据是一次观测,不是读 sing-box 的 stderr

一个笼统的 `tunnel_unreachable` 会把两件处置**完全相反**的事压成一句话:那台机器连不上
(去修 VPS 或换一台)vs TCP 连得上而隧道就是不健康(机器活着,问题在链接/凭据/SNI/
路上的干扰)。**本仓库为第二种付过一次大代价**:reality 一度全挂,真因是默认 SNI
`www.microsoft.com` 的证书过大,而当时先误归因成 sing-box 同机问题、又误归因成网络
MITM(见「reality 传输收尾」教训坑 ①)。**把两种压成一个码,等于把那次教训重新埋回去。**

判别**不读文本**(那正是本仓库反对的形状,而且那份日志是多次 spawn 共用的、分不清哪几
行属于这一次),而是**做一次观测**:健康窗口过后,对 `serverHostFromLink` 给出的
host:port 直连拨一次(`internal/supervisor/tunneldiagnosis.go`,上限
`TunnelDiagnosisTimeout` = 5s)。**这次拨号不新增任何暴露面**(目的地是用户自己的服务器,
bx 刚朝它拨了 20 秒;此刻还没 Hijack,普通 socket 走物理网卡),而且**只在失败路径上
发生** —— 成功启动一次都不拨,由 `TestHealthyStartupNeverDials` 守着那条「不后台定时
探测」的边界。

结局**五种**,而不是三种 —— 后两种是 review 逼出来的,每一种都在真机上能演一次:

- 拨不通(拒绝 / 超时)⇒ `tunnel_unreachable`;拨得通 ⇒ `tunnel_handshake_failed`。
- **这一种传输根本不在 TCP 上听** ⇒ `tunnel_unhealthy_undetermined_udp_transport`,
  **一次号都不拨**。hysteria2 是 QUIC/UDP —— 一台**活着的** hysteria2 服务器
  根本不应答 TCP SYN(那个端口被防火墙过滤时连拒绝都不是、直接超时)⇒ 报「那台机器
  可能挂了」。这是确定性的,不是概率性的。判据取**传输种类**不取端口号,认不出的种类落
  「观测不到」(诚实答案):`TestEveryTransportKindDeclaresWhetherATCPProbeObservesIt` +
  `TestAUDPOnlyTransportIsNeverProbedWithTCPAndFallsToUndetermined`。
- **SYN 根本没离开这台机器**(`ENETUNREACH`/`EHOSTUNREACH`/`EACCES`/`EADDRNOTAVAIL`,
  以及 `*net.DNSError`)⇒ `tunnel_unhealthy_undetermined_local_dial`。**它指着 bx 自己的
  直连器,不指着 VPS** —— 2026-08-13 那次事故的签名(DirectDialer 用 `IP_BOUND_IF` 绑
  物理网卡,而那条 scoped 默认路由由 `Hijack` 装,在 `Run` 里排在这次判别拨号五百多行
  之后)。落在唯一一条职责就是说实话的路上说了假话,代价最大。
  `TestALocalDialFailureIsNotReportedAsTheServerNotAnswering` 的后半段刻意钉住反面:
  **拒绝与超时仍然是 `tunnel_unreachable`** —— 少了它,「凡是拨不通一律判不出来」也能
  满足前半段。Windows 那半的 errno 是另一族(`WSAENETUNREACH` 10051,`Errno.Is` 不跨
  映射),平台孪生表由 `TestEveryLocalDialFailureHasAWinsockTwin` 守住。
- 其余(解不出 host:port / DNS 解析不了 / 父 ctx 被取消)落笼统那个码。

**三个 undetermined 码共享同一个前缀,而且是由拼接得来的**(不是三个各写一遍的字面
量):消费方不可能把其中之一读成「那台服务器没事」,而将来加第四种时它自动进这一族。
**超时归 `tunnel_unreachable` 而不是 undetermined** —— 事故本身的错误就是 i/o timeout,
把最常见的形状判成「说不出」等于把这批要给的答案扔掉;undetermined 留给「我们自己没问
成」。分类全程靠**哨兵错误**(`internal/supervisor/startfailure.go`,`errors.Is`),
**一条字符串匹配都没有**(`TestStartFailureCodeNeverGuessesFromText`);每个哨兵必须有
产地(`TestEveryStartFailureSentinelHasAProductionSite`),否则就是一个永远不会出现的码。

### Core 自报,Guardian 按 PID + 窗口双重匹配着读

Core 在 `bx run` 的错误路径上原子写 `/var/lib/bx/core-start-failure.json`
(`internal/corestartfailure/record.go`,叶子包 —— 写的人在 cli、读的人在 guardian,
这是唯一需要逐字对齐的东西,与 `internal/udpsource`/`internal/barriercidr` 同一先例;
flag 名 `--start-failure-file` 也下沉在那儿,**改名漂移因此在构造上不可能**,而「删掉
声明」由 `TestRunDeclaresTheStartFailureFileFlag` 打在生产那份 `runFlags()` 上)。

- **只有 schema/pid/at/code,一个自由文本字段都没有** —— 按构造漏不出路径、链接、凭据;
  由**序列化出来的字节**钉住,不由字段名白名单钉住(`TestRecordCarriesNothingButACode`、
  `TestTheRecordCarriesNeitherTheLinkNorTheConfigPath`)。细节照旧进 Core 日志。
- **陈旧记录两层防线**:spawn 之前先删(而且用 `Discard` 不用 `Remove` —— SIGKILL 按
  构造就落在 `CreateTemp` 与 `Rename` 之间那段窗口附近,只认最终名字的 `Remove` 一个
  碎片都清不掉,`TestDiscardSweepsTemporariesLeftBehindByAKilledWriter`),读的时候
  `pid` 必须是这一次 fork 的、`at` 必须落在本次健康窗口内、码必须在白名单里。任何一项
  对不上 ⇒ **「这一次没说」**,回落 `core_health_failed`,**绝不猜**
  (`TestARecordThatIsNotThisSpawnsIsNotBelieved`、`TestNoRecordAtAllIsSilenceNotAGuess`)。
  这个仓库为陈旧文件栽过三次(`upgrade-intent.json`、`core-process.json`、那份四分之三
  是假的缺口清单)。
- **读发生在收拾那个 Core 之前**(`TestTheRecordIsReadBeforeTheFailedCoreIsCleanedUp`):
  清理走强杀,顺序反了那次读永远读不到东西**而返回值上看不出任何区别**。
- 手敲的 `sudo bx run` 不传 flag ⇒ 一个字都不写(`TestRunWithoutTheFlagWritesNothing`);
  写盘失败**不改变 Run 的返回错误**(诊断不许把一次故障换成另一次故障)。

### 应答体仍然只带码 —— 这才是它没扩大发布面的原因

「那台服务器是谁」与「你还有哪几台」**两个客户端本来就合法持有**:`bx up` 以 root 跑、
读得到 `/etc/bx/config.yaml`;菜单经 `/v1/servers` 拿到的条目本来就带 host/port。于是
Guardian 只发 `code=core_tunnel_unreachable`,两个客户端各自在本地把那句可行动的话拼
出来(`internal/cli/corestartadvice.go`、纯判据在
`apps/macos/BxMenu/Sources/BxMenu/ToggleController.swift`、映射在 `main.swift`),
**这次改动没有新增一个字节的发布面** —— 而「发布面扩大靠 review」在本仓库是已知的弱环。

**唯一的例外是 review 量出来的**:`bx setup` 写出的那种配置(只有 `server:`、没有
`servers:`)在 `/v1/servers` 的应答里**主机名一个字都没有**(实测过应答体,不是照
brief 假设的「已经带了」),于是菜单那半说不出是哪台服务器。修法是新加
`current_server`(`internal/guardian/servers.go`),**刻意在 `servers` 清单之外** ——
往清单里塞一条会让 `serverListEmptyReason` 从「这是单服务器配置」退回 nil,把服务器
窗口那句刻意区分出来的话吃掉。它走同一个构造器,于是映射守卫顺带盖住它
(`TestSingleServerConfigStillNamesTheCurrentServer`、
`TestMacMenuStartFailureServerMappingCarriesTheEntrysOwnFields`)。

**措辞四条规矩,每条都来自一次真实事故**,由
`TestEveryStartFailureOutcomeReadsDifferently`(判据是**整句话**,不是枚举值 —— 少了它,
三个分支映射到同一句「隧道没起来」照样全绿)与另外几条守着:
① 只说 bx 观测到什么,**绝不断言那台服务器的状态** —— 本机自己没网时同样拨不通,而
一句「那台服务器没有应答」会让用户去重启一台好好的 VPS,所以那句话是「**bx 连不上**
<host:port>」(`TestTheWordingNeverAssertsWhatTheServerIsDoing`);
② 连不上与连得上但没握上的措辞必须**相反**;
③ 三个 undetermined **没有一个**可以被读成「服务器没事」;
④ 只在真有另一台时才说「你还配了另一台」,而且**绝不打印链接**
(`TestTheOtherServerLineOnlyAppearsWhenThereIsOne`、`TestNoRenderedAdviceEverCarriesALink`,
与 `TestServerListNeverShipsTheLinkItself` 同一条)。
**实施者在这里也纠正过我一次**:我说「local_dial 那档既然指着本机,就把『换一台服务器』
那句删掉」—— 它顶回来:`*net.DNSError` 也落这个桶,而那个病因**换一台确实有用**。
矛盾的不是那句出路,是那句**无条件断言**;现在它挂在判别结果上,并点名另一种病因
(`TestTheLocalDialAdviceDoesNotContradictItsOwnSwitchSuggestion`)。同理:端口解不出来
就整条不给 `nc -z`(`TestNoNCCommandIsRenderedWithAnEmptyPort` —— 一条粘贴过去就报错的
命令出现在一句唯一目的就是「照着做」的话里),渲染出来的话里不许有 markdown 的 `**`
(用户读到的是字面上的星号,`TestNoRenderedAdviceCarriesMarkdown` 两侧各一条)。

### 判别拨号绑物理网卡 → 同一份 lessons

判别那次拨号走 `plat.DirectDialer()`(darwin 上是 `IP_BOUND_IF`),而**它只查 scoped
路由表** —— 那条 scoped 默认路由由 `Hijack` 装,而 `Hijack` 排在判别拨号**之后**。
2026-08-13 那种机器状态(单一活跃网络服务)下每次判别都在本机 `ENETUNREACH`,
于是「VPS 真的挂了」与「VPS 活着而握手失败」**一起塌进 local_dial**。修法两半:
① local_dial 那句话也点名 host:port;② **绑网卡那次在本机失败后,不绑再试一次**
(此刻还没 OpenTUN、没劫持,普通 socket 走的就是主路由表 —— 而隧道子进程刚才那
20 秒走的正是同一张表)。**只在「SYN 没离开本机」这一族失败上才退到不绑**:
拒绝 / 超时 / 域名解析不了都是**观测到的答案**,不许被第二次拨号覆盖。

### 跨进程那条线 → 同一份 lessons

**生产的写方与生产的读方此前从不在同一个测试里碰面**,于是三条各一行的改动都能让
这个功能整个退回改动前而三个包全绿。往返现由
`TestTheCoreWritesExactlyWhatTheGuardianReads` 钉住:**写下去的字节读回来必须还是
同一个码**,判据刻意不是「调用发生过」。**这是本支第四次「第七种写法」。**


### 刻意不做

- **不自动切服务器。** 所有者定死的边界(Servers 窗口 spec §8:不自动容灾、只有用户能
  切)。但「你还配了另一台」这句话必须说出来 —— 否则那条边界的代价白付了。
- **不改「Core 先等隧道健康、再开控制 socket」这个顺序。** 反过来能让 `bx status` 在
  启动途中就答得出「正在起、隧道还没通」,但 `core_socket=true` 这个信号被观测层
  (`internal/observe`)、调谐环准入(`decideStartCoreAdmission`)、所有权判定到处在用,
  改它的语义是全仓爆炸半径。**单独立项。**
- **不改 fail-closed**,一个字不动。

### 真机未验(整套)→ `docs/acceptance-pending.md` B2

验收步骤搬到那份清单里了(把 `current` 指向不通的地址、`sudo bx up` 应**一次**就说出
「bx 连不上 <host:port>」)。**五条已知缺口仍留在这里**,因为它们是待办不是步骤:
① `bx up` 那条接线**只在 darwin 生效**(linux 走 systemd 不经 Guardian socket);
② `current_server` 只喂「Core 起不来那句话」,**没接进服务器窗口**;
③ 菜单那半在 `.warning`/`.connected` 之外的状态下拿不到码;
④ **升级那条路上的健康失败仍不读记录**(`startUpdateCore` 的 `new_core_health_failed`)
—— 不顺手做是因为那条路的码空间是另一套,要先定前缀与呈现,**单独立项,别顺手改**;
⑤ **「读不到配置」仍落 `other`** —— 只有 `config.Parse` 失败挂了 `ErrConfig`,
文件不在 / 权限不够是另一种故障(多半是「还没 setup 过」),借 `config_unusable`
就是叫用户去改一个他还没写过的文件。
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
  **范围含 CLAUDE.md 本身 + `apps/` 下的 Swift**,
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
  以为 CLAUDE.md 不在保护范围内,而那正是它自己要消灭的那种陈述。
  **2026-09-12 再扩到 `apps/` 下的 Swift**(仍不含 `docs/superpowers/{specs,plans}`,
  理由同 `TestDocumentedFilePathsExist`:计划书是有日期的意图记录,失效是预期的,
  拉进来只会制造 55 条假红)。起因是一次审计在 `main.swift` 里抓到一条失效引用,
  而守卫**在结构上看不见它**;而菜单恰恰是全仓测试覆盖最薄、读源码守卫最多的
  一块 —— 「由 `TestXxx` 钉住」在那里**最承重、也最不容易被发现失效**。Swift 只
  贡献引用不贡献定义(那边的「测试」是 `@main` 结构体加一串 `expect(...)`,没有
  `Test…` 开头的函数名)。**够不着要扫的源码时必须 `t.Fatal`**:两道下限,走不进
  `apps/` 一道、走进去了却一个 `.swift` 都没见着一道 —— 一条安静地扫了零个文件的
  守卫,与没有这条守卫在输出上完全一样,而它看起来更让人放心。首次全量扫描
  **是干净的**(Swift 里 9 处点名全部指向真实存在的 Go 测试)。
  **同一轮收窄了 `retiredTestNames` 那个逃生口。** 它是给**历史记述**用的
  (「那条已退场,由 X 接手」),而审计发现它正在被一句**现在时**的断言吃着:
  `internal/stats/outcome.go` 写着「由 <某条已退场的测试> 钉住」,名字在名单里,
  于是全绿 —— 名字一旦进名单,就从「必须真的存在」变成了「随便怎么用都行」。
  现在多一道:退场的名字**同一句话里紧跟着**断言词(钉住/钉死/守着/守住/盯着/
  pinned by),而相邻一行又没有任何退场字样,就红。**判定粒度是「一句话」不是
  「一段」,这是变异实测逼出来的**:第一版免责窗口开到 ±3 行,把出事那天的原话
  写回去照样全绿 —— 那一段在事后被改对时补上了「当初那条守卫因此退场」,窗口够宽
  就把新写回去的假话一并赦免了,而**一段同时讲着「它退场了」和「由它钉住」的文字
  恰恰是最不该赦免的那一段**。网仍然刻意窄(动词在名字前面、被折到下一行、同义
  改写、块注释、英文只认一种写法,都看不见),理由是本仓库那条老纪律:一条会误报
  的闸门比没有闸门更糟。真实的 12 处退场记述一条都不红。
- **CLAUDE.md 与 `docs/lessons/` 的分家规则(2026-09-13 定)**:CLAUDE.md 只放**判据**
  ——「改这块之前必须知道什么」「哪条不变量不许动」「什么是已知缺口」;**过程**
  (某次事故的逐轮复盘、某个功能的施工日志、某条守卫当初怎么被攻破的)进
  `docs/lessons/`。**判据不许只存在于 lessons 里**:那边是给「想知道当初怎么换来的」
  的人看的,而 CLAUDE.md 是每次会话都加载、下一个人开工前唯一会通读的那一份。
  定这条规则是因为它长到了 200k 字符,其中一个 markdown 列表项独占 79KB(22%)
  —— **通读不了的东西等于没写**。
  **`docs/lessons/` 与 CLAUDE.md 受同两条守卫保护**(`TestDocumentedFilePathsExist`
  与 `TestEveryTestNameMentionedInProseExists`,2026-09-13 扩的范围),因为搬迁本身
  会制造盲区:那些原文点名的测试与路径,搬出去之后若无人看管,**同样会被读到、却
  不再会被证伪**。它与 `docs/superpowers/{specs,plans}` 的区别是**时态** —— 计划书
  写的是「将要建的东西」,失效是预期的;lessons 写的是已经发生的事实。
  **推论:真机验收的逐条结果进 lessons,CLAUDE.md 只留「已验 / 未验」那一行状态加
  指针。** 否则它会随每一次验收单调增长 —— 2026-09-13 那次拆分刚把它压到 148k,
  补三条实测证据就又吃掉 1.6k。**但「未验」那一半必须留在 CLAUDE.md**:它是待办,
  不是历史。
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
- **验证命令**:`bash scripts/verify.sh`(全量 14 步)或 `--quick`(改一行时,跳过 race 与交叉编译)。
  **2026-09-13 加的第 14 步值得单说**:那圈交叉编译用的 `go build` **从不编译 `_test.go`**,
  而 Windows 那半的行为断言只在 CI 的 windows runner 上跑 —— 实测把一个 `*_windows_test.go`
  里的常量改成不存在的名字,`go vet ./...` 与 `GOOS=windows go build ./...` **两条都通过**,
  推上去才红。现在多一步:只 vet 那些含 windows-tagged 测试的包(vet 会 typecheck 测试文件),
  清单从 `git ls-files` 现取、一个文件都找不到时响亮失败。**刻意不写成 `GOOS=windows go vet ./...`**
  —— `internal/tray` 有一条先于此存在的 unsafe.Pointer 告警,拉进来就是一道恒红的闸门。
  **判据一律是退出码,不是字符串匹配。** 它的存在是因为 2026-08-11 那轮里同一个根因栽了六次:
  `go test … | grep …; git commit` 用 `;` 串联(测试红了照样提交)、变异验证 grep `^failed` 而套件
  打印的是 `FAIL:`(「没转红」被误判成守卫失效)、`head -5` 查 `set -e` 而注释头十几行、`grep -c` 数
  「出现次数」而它数的是行数、替换串带了不存在的前导 tab 而 `str.replace` 匹配不上时不报错。
  **别再手敲那一串命令**;`verify.sh` 自己也验过五个方向都会失败,漏一道闸门由 `TestVerifyScriptCoversEveryGate` 钉住。
  **一个会偶发红的闸门比没有闸门更糟**,因为它训练人去重跑
  **2026-09-14 的 release run 连着红两次,两次是不同的测试、不同的病因,而且
  `scripts/verify.sh` 在本机(macOS)全绿 —— 那一整类平台差异它结构上覆盖不到,
  因为它跑的是这台 Mac,而 CI 的 build job 跑 Linux。两条都记下来:**
  ① `TestManagerUpStartsCoreDespiteUnremovableDeadCoreRecord` —— **确定性的,已修**。
  它 `release` 那个假进程,于是 manager 把它当**意外退出**走 `handleUnexpectedExit`:
  写状态、可能再起一个 Core,而那些全落在 `t.TempDir()` 里,与 TempDir 自己的
  `RemoveAll` 抢同一个目录(`unlinkat …: directory not empty` —— 删完内容正要
  rmdir 时又被写进来)。修法是**不 release**:这个测试到 Up 成功就该结束,再模拟
  一次退出不属于它。**只同步 runner 那条 goroutine 不够(试过),manager 的 monitor
  是另一根。** 形状与 socks5 那次同源(活过测试函数的 goroutine),只是那次碰的是
  `t.Errorf`,这次碰的是文件。**在 Colima 的 linux 容器里复现与验证**(本机复现不出来)。
  ② `TestManagerUpdateReservesDeadlineForTargetCleanup` —— **仍是潜在 flake,未修**。
  整个 `Update` 只给 500ms,而 v2 的健康检查无限阻塞、先吃掉大半,剩给「回滚后等
  v1 健康」的余量在 CI 慢机器上不够(`previous_core_health_failed`);本机与本地
  linux 容器各跑 20~30 次都全过。**正确修法不是把 500ms 调大** —— 要先弄清 `Update`
  内部怎么在健康检查与清理之间分预算(那正是这条测试要证明的东西),否则调大只是
  把同一个竞态推远一点。
 —— 而重跑正是「判据是
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
- **真机绿不等于这条路没问题 —— 真机与 CI 互不替代(2026-09-16 付的学费)。**
  给 Windows 那条腿修三条测试时,测试二进制交叉编译到项目所有者的真机
  (`030-SJWJ-GSR-B`)上跑,七个包全绿,变异对照也做了(修复前红、修复后绿,
  CRLF 那条还当场复现了「找不到函数结尾」)。推上 CI 第一轮却红了 **11 条**:
  `control_client_test.go` 写死 `os.MkdirTemp("/tmp", …)`,而 `/tmp` 在 Windows 上
  解析成**当前盘**的 `\tmp` —— 那台真机恰好有 `C:\tmp`(事后实测确认),runner 上没有。
  **真机比 CI 宽松,于是它在这一族上给了假绿,而我当时已经用它下过「七个包全绿」的结论。**
  分工是确定的,别拿一边的绿去替另一边背书:真机验 CI 验不了的(平台语义、真实文件
  系统、真实网卡、真实网络);CI 验真机验不了的(**干净 checkout** —— `.gitattributes`
  的行尾效果只有它证得了、标准环境、没有任何本地遗留物)。
- **提交信息**:中文 conventional commits,结尾带 `Co-Authored-By: Claude …`。在默认分支直接提交(单人项目)。
- **内嵌资产**:`internal/embedded/assets/brook_linux_{amd64,arm64}`(~30MB)+ `singbox_{linux,darwin}_{amd64,arm64}`(linux ~28MB / darwin ~23MB)是提交进仓库的真二进制,按 GOOS/GOARCH 条件 embed(每构建只嵌匹配的那一个;singbox 经 `embedded_singbox_{amd64,arm64,darwin_amd64,darwin_arm64,other}.go`,**linux+darwin 都内嵌(同 brook 平台覆盖,mac 上 reality/hysteria2 也零依赖即跑)**,windows/其他 arch 走 nil 兜底→下载)。CI `embed-brook.yml`/`embed-singbox.yml` 跟上游 release 自动重嵌。换 arch 要补对应二进制。**缓存键掺内容 hash(已实现)**:`provision.embedCacheKey` = 版本 tag + `sha256(内嵌字节)[:12]`,写进 `.brook-version`/`.singbox-version`;同 tag 重嵌不同字节(如 sing-box 从 `with_utls` 加到 `with_utls,with_quic`)也会失效旧缓存、强制重释放,避免用到陈旧二进制。
  - **sing-box 是「自建静态最小构建」不是官方 release 二进制**:官方 linux 包是 glibc **动态链接 + 56MB 全家桶**(含 tailscale/acme/clash/dhcp,reality 全用不上),违背 bx「静态单文件、零依赖」。故从同一 release tag 源码用 `CGO_ENABLED=0 go build -tags with_utls,with_quic`(REALITY 需 utls;**hysteria2/QUIC 需 with_quic**)自建:**静态**(Alpine/musl 也跑,同 brook)、**~28MB**(官方半体积)、同 revision。CI `embed-singbox.yml` 复刻此构建;改时务必保持 `with_utls,with_quic` 与 `CGO_ENABLED=0`。
- **绝不擅自启动 bx / 改路由**:启动是用户的事(需 root、动真实网络)。改完让用户自己 `bx up`。
  **2026-09-13 真机事故:这条约定被 shell 绕过去了一次,而不是被谁决定绕过去的。** 一个只读
  排查代理在**双引号**的 grep 模式里带了反引号,zsh 把它当命令替换执行,于是真的跑了一次
  `bx up`(Core 没起来、屏障没装、路由与 DNS 未动;只有盘上 `desired` 被翻成 on,因为
  `upLocked` 先写意图再起 Core)。**这个仓库对这个形状格外脆弱:文档里到处是反引号包着的命令**,
  而搜文档是每个代理开工第一件事 —— 一次 `grep -rn "…`bx up`…" CLAUDE.md` 就会真的执行它。
  **规矩:凡是搜索/匹配用的模式一律单引号**(单引号里的反引号不执行,双引号里的会);
  要在双引号里出现反引号就转义。判据不是「代理会不会自觉」——这次自觉的是代理,执行的是 shell。
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

**观测层与不变量基线(2026-08-05,`internal/observe`,纯逻辑+单测,真机未验)**:**意图 / 事实 / 代码 三分**——意图只由用户/agent 显式声明式改动;事实只由 reconcile 改动(本期尚无 reconcile,观测只读);代码由人经 PR 评审改动。**agent 只声明意图,从不直接驱动动作。** `internal/observe` 是**只读**观测层,向系统现问四件事:劫持是否生效(`route -n get 1.1.1.1`/`129.1.1.1` 的接口 == 我们的 TUN)、屏障是否在位(查 `barriercidr.Blocking()` 那组 `/2` 网段是否被内核以 reject 应答;该清单已下沉到叶子包 `internal/barriercidr`,装屏障的 guardian 与问内核的 observe 共读同一份,`guardian.BlockingBarrierCIDRs` 已删)、DNS 归谁(`install.InspectDNSContext`)、Core 是否活着(`supervisor.FetchRuntimeState`——**控制 socket 在应答本身就是存活观测,不需要 PID 文件**)。**三态 `Tristate` 是关键取舍**:区分「观测到否」与「观测不到」,零值为 `Unknown`;用 `bool` 会把「问不出来」压成 false,而这正是旧架构骗人的方式之一(`RoutesInstalled` 是个只置位不复查的 `atomic.Bool`)。任一项观测失败即记为 `Unknown` 并附原因,**绝不中断其余项、绝不让调用方失败**。`bx status --json` 现**并列发布** `desired`(意图)、既有信念字段、`observed`(事实)、`divergence`(二者之差),**不用观测覆盖信念**——二者的 diff 本身就是最高价值的诊断信号;「status 显绿而流量明文直连」在这个结构下表达不出来,绿是 believed 而 observed 会同时说 `capture_ok: false`。首批四条不变量由 `internal/observe` 的纯函数测试钉住(protected 必须三项皆 True、`desired=off` 不得残留屏障/DNS、不一致必须产出自解释 divergence);**第五条(拆除永不拒绝)本期未实现**,以已知失败的测试留在 `invariants_test.go`,让「控制面还没修」成为 CI 里可见的事实而非待办里的一行字。**两处相对设计的有意偏离**:① `Deps.TunName` 返回 `(string, error)` 而非 `string`——生产接线里 TUN 名字取自 Core 控制 socket,「socket 静默」若被压成「没有 TUN」就会报出一个自信的 `CaptureOK=False`,正是本包要消灭的谎言;② 非 macOS 的 `InspectDNSContext` 返回 `Supported:false` + **nil error**,wire 层把它转成错误 → `Unknown`,不把「没问过」报成「不归 bx」。观测整轮封顶 5s(`bx status` 是出问题时最先敲的命令,宁可少答一项也不能挂住),Core 运行时状态一次观测内只取一次并缓存。**观测只在 darwin 附上**(`observerForPlatform`):路由/DNS 原语目前只有 macOS 实现,在 Linux/Windows 附观测换不来任何新事实(`tunnel_healthy` 本就来自同一个控制 socket、已在扁平字段里),却让每次 `bx status --json` 恒吐 **5 条**「该项无法观测」divergence(实测,`capture_ok` 还重复两次)——那会把 divergence 训练成用户和 agent 学会忽略的噪声,正好毁掉它唯一的价值。**字段缺席是诚实的「没问」;满屏「无法观测」则是把静态平台限制伪装成每次调用都新发生的差异。**设计 `docs/superpowers/specs/2026-08-05-observation-layer-design.md`、计划 `docs/superpowers/plans/2026-08-05-observation-layer.md`。

**`ErrProcessNotRunning` 在 macOS 上曾永不可达(2026-08-06,真机事故 + 修复 `77227ba`,真机已验)**:**macOS 的「进程不存在」不走 ESRCH**——内核 `sysctl kern.proc.pid` 调用成功但写回 0 字节,`x/sys` 因 `n != SizeofKinfoProc` 转成 **EIO**(v0.45.0 `syscall_darwin.go:513`;本机探针实证:已死 PID 与从未存在的 PID 都返回 EIO,`errors.Is(err, ESRCH)` 恒 false)。而 `inspectProcess` (`process_darwin.go`)只映射 ESRCH/ENOENT,于是 **`ErrProcessNotRunning` 在本平台从来不会被返回**。**后果远超单次事故:两处专门为此写的修复一直是死代码**——`Existing()` 的「PID 已死就自愈、别卡死 bx up」(`603b602`)与 `Start()` 的「OS 权威确认已死才放行陈旧启动标记」(`60b76f3`)都键控 `ErrProcessNotRunning`,**在 macOS 上从未生效过**;这解释了 8-05 那次为何最终只能手删 `core-process.json`。事故链(Guardian 日志逐行可见,**故障可观测性那一期在此完全兑现**):`bx down` 请 Core 退出 → Core 确实退出 → `Inspect(PID)` 拿到 EIO → 不认识 → `core_stop_failed` → 判 `core_unexpected_exit`(误以为崩溃)→ 去重启 → 写下 `launching` 标记 → `core_restart_failed` → 此后每次 `bx up` 撞 `ExecCoreRunner.Start`(`internal/guardian/process.go`)那道无条件 uncertain → **永久 500,只有 `bx uninstall` 能脱身**。**修法刻意不凭 EIO 断言**:EIO 同样可能来自真实 sysctl I/O 失败,本层不可区分,误判「不存在」会放行第二个 Core(正是 `af81632` 被回退的风险);改为向内核求证——只有 `kill(pid,0)` 明确回 **ESRCH** 才判定进程不存在,活着/EPERM/求证失败一律保持不透明错误,fail-closed 不让步。**真机复测**:同一台机器上修复前 `bx down` 必落强制拆除且此后 `bx up` 永久 500;修复后 `down` 走干净路径(`✓ Guardian bx 已停止,网络已恢复`)、`up` 正常、连续第二次 `up` 幂等无操作、Guardian 日志零 `needs_attention`。**`launching` 的死结随后已解**(见本条末尾「Core 所有权改判据」一段):该标记不再无条件判 uncertain,改为向系统求证有没有进程在跑 Core,没有就自愈清标记——**不再需要手删 `/var/lib/bx/core-process.json`**(手删反而危险:盘上无记录曾是唯一没有 OS 求证的启动路径,Core 正跑着时手删会起第二个 Core;那条路径也已补上求证)。两段式 marker 方案见 `docs/superpowers/plans/2026-08-05-guardian-launch-marker-deadlock.md`。**同批真机首验通过的还有**:① `2963472` Guardian 日志 0600(实测 `.rw------- root`);② `dc35594` 菜单栏经 `launchctl asuser` bootstrap(实测 `gui/501/com.getbx.bx.menu` `state=running`)——此前那次 `EIO(5): Bootstrap failed` 是**上一次统一安装残留的 plist** 被 root 直接 bootstrap 所致,legacy CLI-only 安装根本不含菜单栏(plist 只由 `install.UnifiedInstall` 写,程序体就是 `/Applications/Bx.app`)。**两段式启动标记(`e7e413c`,真机已验)**:`Start()` 原先在 fork 与「验明身份后写 owned」之间只留一个 `PID==0` 的 `launching` 标记,该窗口内崩溃即留下无从判断的记录 → 永久卡死 `bx up`。现 fork 一返回就落一条带子进程 PID 的 **`spawned`** 记录再去 Inspect/verify,窗口缩到「fork → 一次写盘」;窗口外崩溃留下的记录可向 OS 求证——进程死了自愈,**活着仍 fail-closed**(`spawned` 没有 executable/generation,从没验明,既不接管也不当它不存在)。**`launching` 仍一律 fail-closed**:设计文档 option 1 主张「`PID==0` ⇒ 确定没 fork ⇒ 可自愈」,实现时被既有测试 `TestExecCoreRunnerPersistenceFailureLeavesDurableUncertainLaunchMarker` **当场证伪**——`spawned` 那次写盘本身失败(磁盘错误)时 fork 已发生而盘上仍只有 `launching`,自愈就会起第二个 Core,正是 `af81632` 被回退的原因;该判据不成立,已撤回那半边。三条既有安全测试(persistence failure / `Start` 拒绝活标记 / Manager 级阻断)全部**原样通过、一个断言未改**,这是「没削弱保护」的依据。彻底解开 `launching` 需绕过自身簿记向系统求证「有没有进程在跑我们的 Core 可执行文件」(与观测层同一思路)——**已在下一段做掉**。

**Core 所有权:向系统现问,不信自己的记账(2026-08-06/07)**。三条判据必须一起读:
① **macOS 的「进程不存在」不走 ESRCH** —— `sysctl kern.proc.pid` 调用成功但写回 0
字节,`x/sys` 转成 **EIO**,于是 `ErrProcessNotRunning` 在本平台**曾经永不可达**,
两处专为它写的自愈从来是死代码。判据**不凭 EIO 断言**(它也可能是真的 I/O 失败):
只有 `kill(pid,0)` 明确回 **ESRCH** 才判定进程不存在,活着/EPERM/求证失败一律保持
不透明错误。② **`looksLikeCore` 刻意不依赖可执行路径** —— 更新后旧版 Core 跑在
`runtime/<旧版本>/bx`,按路径匹配会漏认它、进而起第二个 Core;判据是
`basename(argv[0])=="bx" && argv[1]=="run" && uid==0`,**过度匹配的代价是拒绝启动
(安全),漏认的代价是灾难**。③ **「问不出来」不等于「没有」** —— 枚举失败、或枚举到
进程却一个 `procargs2` 都读不出,一律 fail-closed(`decideCoreScan` 纯函数钉住)。
**双 Core 是最坏结局**:`supervisor/control.go` 在 `net.Listen` 前先 `os.Remove`,
第二个 Core 会**静默夺走控制 socket**,两个 Core 争 split-default 路由、先退出的那个
用旧快照还原掀掉另一个的劫持 ⇒ `bx status` 显绿而流量明文直连。
**移植警告**:非 darwin/linux 平台上 `scanRunningCores` 恒返回错误 ⇒ 恒 fail-closed
⇒ 连「无记录」这条本该能 fork 的路也被拒,即 `bx up` 起不来 Core。**谁要移植
Guardian,必须先实现 `scanRunningCores`,不能只放开 `requireDaemonPlatform`。**

**所有权不确定有出口,但别把它写成承诺(2026-08-11)**。**`Down` 从不清这个锁存**
(它按 `m.current.PID` 分支,三种锁存形状都到不了那句清除)—— 用户「`down` 再 `up`」
之所以管用,靠的是 CLI 落到强制拆除、**把 Guardian 整个 bootout 掉**,而锁存只活在
那个进程内存里。**真正管用的一直是杀掉 daemon。**(同一句话 2026-09-13 又在
`recovery_incomplete` 那条文案上重演了一次。)**用户发起**的 `Up`/`Migrate` 不再短路,
改为经 `confirmNoCoreForRelease` **重新求证**:**两次扫描都干净、中间隔一个沉降窗口**
才释放;扫到了(哪怕只有一次)/扫不动/求证 panic 一律保持拒绝。**它与 `Down`/`Recover`
用的 `confirmCoreStopped` 偏置刻意相反**(后者第一次扫干净就算数),**把两者「统一」
掉就是把双 Core 的门打开**。**启动恢复刻意不重新求证** —— `retryDaemonRecovery` 每
5 秒重试且永不放弃,把两次扫描 + 沉降搬进按时钟驱动的路径就是一天一万七千轮;
**这套纪律不许搬进任何按时钟驱动的路径**。**锁存不许升级成 `recoveryBlocked`** ——
「开不了」永不许升级成「关不掉」,那正是 71 分钟事故的形状。
**逐条经过见 `docs/lessons/2026-08-control-plane.md`。**

**观测路径真机已验(2026-08-05,本机 macOS,零改动)**:观测全程只读且 `bx status` 是独立 CLI 调用,故**无需重装、无需重启运行中的实例**——直接用新编的二进制问现有那套即可(`/var/run/bx/{core,guardian}.sock` 是 0666,连 sudo 都不要)。实测输出:`desired=on` / 信念 `protected` / 事实 `capture_ok=true(utun11)`、`dns_managed=true(127.0.0.1)`、`barrier_present=false`、`core_socket=true`、`tunnel_healthy=true`,**divergence 为空**——坐实了 `route -n get` 解析、`networksetup` 读 DNS、控制 socket 往返三条真机通路,以及「一致时必须安静」。**仍未验的是故障态**:divergence 能否抓到真实事故,要等真出一次(或人为造一次)才知道;第五条不变量(拆除永不拒绝)本期也未实现,故**不建议为了这一期去重装**——重装要停再起,而那正是把用户锁在断网状态的那条路径尚未修复的地方。

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
- **`Quit Menu`(只关界面、保护继续跑)已删**,它等于给用户一键做出「保护在跑但
  没有任何指示灯」的隐形状态。退出入口由 `rebuildMenu()` 顶层无条件加一次,
  `TestMacMenuQuitActionPresentInEveryState` 按**函数体花括号深度**钉住「无条件」
  —— 只数出现次数抓不到「挪回某个 case 里」。**已知缺口**:恢复浮层与进度浮层在
  按 state 建菜单之前就 `return`,走不到那个无条件退出项。
- **菜单动作免密的授权面精确到一个函数**:`mutationHandler`(服务 `/v1/up` 与
  `/v1/down`)用 `authorizeOwnerPeer`,`updateHandler`/`migrationHandler` **保持
  root-only**,由 `TestLocalAPIUpdateAndMigrateStayRootOnlyEvenWithOwnerConfigured`
  钉住。**已接受的安全后果**:无 Developer ID 就绑不了权利到 Bx.app(SMJobBless
  要校验双方签名),故该用户下任何进程都能静默开关 bx;缓解是 `guardian_mutation_requested`
  记下 uid 与发起时刻。**turnOff 的 socket 失败回落到提权 `bx down`,turnOn 不回落**
  ——没有「强制打开」,且把死 socket 升级成弹密码是骚扰;**全部路径都失败时 Quit
  不退出**(退出会藏掉唯一的指示灯而保护还在跑)。
- **图标状态编码在轮廓,不在颜色**(实心/空心/虚线/沿中线裂开),判据是**去掉动效
  后四形态仍两两可分** —— 开启「减弱动态效果」时只靠呼吸周期区分的两态会完全同形。
  无色两态走 template 让系统上色,且必须用**不透明黑**绘制:template 只取 alpha,
  用 `secondaryLabelColor` 画出来的蒙版峰值只有 0.498,深色菜单栏上那条描边会消失
  ——而那正是「保护没开」最不能看不见的状态。
- **菜单里所有定时器必须挂 `.common` 模式**:默认模式的 `Timer` 在 NSMenu 追踪期间
  实测触发 **0 次**,而菜单开着正是用户在看的时候。类级守卫禁止 `main.swift` 出现
  裸 `Timer.scheduledTimer`。数据行三态里**只有 `bad` 计入 `anomalyCount`**(它驱动
  图标裂不裂)——把「没问出来」算成异常会让图标无缘无故裂开;「未观测」译
  `Not checked` 而非 `Unknown`,后者会把它与「问了但不明」重新混为一谈。
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
- **调谐环的判断与执行分离**:纯函数 `decide` 把「用户要什么」×「系统实际是什么」×
  「三道栅栏」映射成一组**命名的意图**。**没有「装屏障」这个动作是刻意的** ——
  装它要先探默认网关,瞬时失败会降级成无 server bypass 的 block-only = 整机黑洞
  且隧道无自愈通路;手写路径至少绑在一次用户显式请求上,放进按时钟驱动的循环
  就是每拍重掷一次骰子。**所有权不确定是栅栏、不是待收敛的差异** —— 它整个存在
  的意义就是「拒绝」,循环去消除它等于自动推翻一次刻意的 fail-closed。
- **「零值读起来像一切正常」在那一期出现四次**,是这类循环的核心危险:
  `reconcileDecision` 的零值恰好就是一台健康机器的判断,于是「从没跑过一轮」
  「循环体空转」「报告在路上被丢掉」「三项探测全失败」在日志与 `bx status` 里与
  「一切正常」逐字节相同。处置:`ReconcileReport.At` 是「跑过没跑过」的唯一判据,
  `UnobservableItems()` 把观测质量折进变更比较(**变瞎本身就是一次值得打印的变化**),
  panic 收在**每一轮内**(收在 `for` 外的话一次 panic 就永久结束循环,而冻住的报告
  仍读作干净一轮)。
- **维护挂起:`desired` 只记录用户意图,停机用正交的 `/var/lib/bx/maintenance-hold.json`。**
  此前升级用「写 `desired=off`」表达停机,而任何忠实的调谐器读到它都会收敛到 off
  —— 那正是 bug 本身。**绝不能把挂起加进 `guardian-state.json`**:那文件是个裸 JSON
  字符串、没有信封没有版本,旧 Guardian 读不动 ⇒ `recoveryBlocked=true` ⇒
  `Manager.Down` 永久返回 `errRecoveryIncomplete`,正是 71 分钟事故的机制,而升级
  恰恰是新旧共存的时刻。**会自己起 Core 的路径共五条**(干净 `Down`、强制拆除、
  启动恢复、`Down` 的 DNS 补偿重启、`recoverUpdateLocked`),前四条必须认挂起,
  **漏一条就仍有一条把 Core 放回半换二进制的路**;第五条刻意不拦(拦住会把没做完
  的 Guardian 自更新永久搁浅)。**挂起写失败就退回写 `desired=off` 并照常拆除**
  ——宁可退回一个会撒谎但安全的状态,也不要一个诚实但没人拦着的状态。**用户的显式
  up/down 无条件清挂起,且清挂起对拆除的成败无条件**。

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
