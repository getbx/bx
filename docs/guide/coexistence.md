# 与 Docker / Tailscale 共存

**Docker**:`10/8`、`172.16/12`、`192.168/16` 在任何模式下恒直连(Linux 上由 `pref 150` 送主表,
交 `docker0`/`br-*` on-link 投递),所以**宿主访问容器、端口映射、容器间通信永不进隧道**;
TUN 自己的地址也刻意选在 `198.51.100.1/30`(TEST-NET-2)避开 docker 的地址池。

**容器出公网的流量会经 bx 代理** —— Linux host 模式下劫持规则不限定来源,转发流量一并接管。
这是有意保留的行为(容器白捡代理),并由 `TestContainerEgressIsHijackedBecauseTheCatchAllHasNoSourceFilter`
钉住:谁给那条规则加上来源限定,容器就会静默失去代理、从物理网卡裸奔。

**Tailscale**:`100.64/10` 恒直连;Linux 上还会先把它送进 Tailscale 自己的路由表(table 52),
保证你主动连 peer 能通。`tailscale.com` / `ts.net` 不分配 fake-IP。启动时抓一次 DERP 中继地址
加进旁路,让 Tailscale 的中继流量绕开隧道(抓不到用内置兜底表)。

**「租户」还是「竞争者」按行为判,不按产品名。** 同一个产品会随配置翻转:Tailscale 开了
exit node 就抢默认路由,不开只 claim `100.64/10`;WireGuard `AllowedIPs=10/8` 是叠加、
`=0.0.0.0/0` 是抢。判据是可观测的那一条 —— **它在路由表里 claim 了公网空间没有** ——
用户一开一关 exit node,判定当场跟着变,不需要谁去更新一张清单。

那张租户表因此只回答另一个问题:**这个产品需要什么照顾**(中继主机名、自己命名空间的
解析器地址)—— 那部分推不出来,只能查表。

**其它 overlay(ZeroTier 等)**:共存规则集中在 `internal/overlay` 的一张声明式租户表里
(怎么认出它在跑 · 地址空间要不要额外直连 · 中继在哪 · 自己的命名空间交给谁解析),
加一个新 overlay = 加一行数据。ZeroTier 用 RFC1918 地址,已被通用私网规则覆盖、
不需要特例;它的根节点是公网 IP,现在会在检测到它在跑时加进旁路。

**只对在跑的租户生效**:写死的公网 IP 会过期,常开一条旁路等于给陌生地址放行;
而把查询送给一个没起来的解析器只会挂住。

**晚于 bx 启动的 overlay**:bx 会持续检测(每 15 秒),两半的处境不同 ——
中继旁路**自己跟上**(下一次路由重装时生效);DNS split **跟不上**,因为热更新它是
data race(`dns.Server.SetSplit` 无锁,今天安全只因为只在启动时调一次)。所以 bx 会在
`bx status` 里报一条告警,并明说出路是重启 bx。**报出来而不是假装解决了。**

**已知限制**:系统 DNS 是 bx 与 Tailscale 两个写入者在抢,谁后启动谁生效。要真解决
需要调谐环介入,而它现在是**只观察**的 —— 从侧门做掉它等于绕过那期刻意设的闸门,
故另立一期。

DERP 旁路抓不到时(开机自启那一刻网络常常还没好)会先用内置兜底表,并在后台按退避一直重试
到拿到权威答案;失败绝不缩小已有旁路。**新集合在下一次路由重装时生效**(切服务器、Wi-Fi
切换触发的路径恢复、或下次启动)—— bx 不会为此自动重装路由,那会为一次后台改进拓宽
「先拆后装」的直连窗口。

WebRTC、DNS、IPv6、QUIC 等泄漏面和检测边界见 [leak-surfaces.md](leak-surfaces.md)。完整检测敲 `bx leakcheck`（开本地页面，把浏览器那半与本机那半对起来）；脚本/agent 用 `bx leak-check --network --json --expected-ip <proxy-ip>`（不开页面）。macOS 上,`bx leak-check` 也会只读检查 Tailscale/ZeroTier/WARP/WireGuard/OpenVPN/Clash/Surge/mihomo 这类额外通道是否与 bx 正常共存；Tailscale 会额外做 bootstrap 旁路,避免它重连时被 bx 抢走控制面流量。

`bx status` 是运行期面板。macOS 上 daemon 会轻量只读监测 Tailscale 路由、系统代理和已连接的 VPN 服务；如果 bx 启动后又出现其他通道,status 会显示 `Notice` 那一行,菜单栏也可用同一份 JSON 变成需要注意的状态。
