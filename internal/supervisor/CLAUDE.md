# CLAUDE.md — internal/supervisor(Core 的编排、路由与后台自愈)

本文件只在读到 `internal/supervisor/` 下的文件时加载。平台接缝(`platform` 四个方法)、防环与
kill-switch 不变量在根目录 `CLAUDE.md`,这里不重复。2026-09-23 从根目录下沉,原文逐字存档在
`docs/lessons/supervisor-archive.md`;按应用看分流的施工过程在 `docs/lessons/2026-08-app-traffic.md`。

## Run() 的形状

- **拆除台账**(`teardown.go` 的 `teardownLedger`)取代一串有序 `defer`:defer 是同步的,一步
  挂住后面全不跑,只剩关机 watchdog 强制退出、跳过剩下的还原。台账给出**逐步限时、命名与记录、
  可断言的顺序**。不许动:**LIFO 一字不改**(路由还原排在关 TUN 之前);`defer teardowns.unwind()`
  **落在第一个系统资源之前**(一半 defer 一半台账会悄悄颠倒相对顺序);超时的 goroutine 是
  **放生不是杀掉**,日志措辞按「没在预算内做完」写;单步预算与 `shutdownGrace` 留三倍余量
  (`TestTeardownStepBudgetLeavesRoomBeforeTheShutdownWatchdog`),**watchdog 不许删**。
  FIFO 变异下单测红而 netns 台子照样绿 —— 真正吃这条顺序的是 darwin 的 split-default,
  **别把台子的绿读成顺序有背书**。步数别写死,要数就 grep `teardowns.push`。
- **后台工人登记册**(`workers.go`):`Run` 里的长命 goroutine 一律经 `workers.start` 起
  (具名 + panic 收在自己那一层)。裸 goroutine 的 panic 会当场终止进程、别的 defer 一个都不跑,
  内核里的 ip rule 还在而 TUN 没了 ⇒ 整机断网。守卫 `TestRunLaunchesNoBareGoroutines` 取 **AST**
  (`Run` 函数体里真实的 `GoStmt`),读不出 `func Run` 时响亮失败。**一律 recover-and-continue,
  代价是静默降级**,所以每个工人「死了会降级成什么」必须列全(判据是 grep `workers.start` 的
  次数,今天 9 个):mutation-engine ⇒ 切服务器不工作 · tailscale-bypass ⇒ 旁路停在兜底表 ·
  transport-failover ⇒ 不再自动切备(kill-switch 仍在)· direct-egress-repair ⇒ 直连出口不再自愈 ·
  rule-history ⇒ 历史停止累计 · server-bypass-refollow ⇒ 服务器换 IP 后旁路不跟 ·
  server-bypass-route-repair(darwin)⇒ 休眠后 `/32` 旁路不再自愈、隧道成环 ·
  china-list-refresh ⇒ 列表不再更新 · routes-ready-repair ⇒ 一次拆到一半的换路由之后,就绪位
  又会永久停在 false(见下文)。**加工人时回来补这一行。** `names()`/`panickedNames()`
  **零生产调用方**,既不进 `bx status` 也不进控制 socket,今天唯一的办法是翻日志。
- **按判据切相位,不按行数切**:`buildSplitBrain`(global 一个字节的 china 列表都不读;CLI flag
  压过 `config.lists`)与 `buildSplitRoutes`(顺序即优先级)已经抽出;剩下的粘合留在相位内。
  **「哪里还藏着判据」的客观信号是读源码的守卫指向哪里。**

## 路由就绪位:`RuntimeState.RoutesInstalled`(2026-09-17,修复真机未验)

写点全仓只有三处(启动 `Hijack` 成功、`liveMutator.Rehijack` apply 成功置真,apply/undo
置假),**没有任何东西会根据观测把它设回来**。三个消费方都吃它:路径恢复的 `verify`、
Guardian 的 health 门(`/v1/update`)、`recoverySupersededByCore`。一次**一条路由都没碰过**
的 rehijack 失败(探网关失败)曾把它永久清成 false ⇒ 20 次 `verification_failed`、升不了级,
而机器全程受保护。
- **`ErrRehijackNoChange`**:三个平台把前置检查失败包一层,apply 只在「真的可能动过路由」时才
  清就绪位,失败时**恢复进来时的值、不是无条件置真**。漏包的后果是退回悲观(卡住),不是放宽。
- 守卫是**一对**:`TestLiveMutatorRehijackKeepsRoutesReadyWhenNothingWasTouched` +
  `TestLiveMutatorRehijackLeavesRoutesNotReadyAfterFailure`(少后者,「干脆不碰这一位」也能
  全绿)。接线 `TestEveryRehijackPreflightFailureIsTaggedAsNoChange` 要求前置区段里**每一个**
  return 都带标记,**先剥注释再找锚点**(解释性注释里恰好写着锚点,曾让它假绿)。
- **拆到一半才失败的 rehijack 仍会把它清成 false(那是对的),但现在有东西把它设回来**
  (`routes_ready_repair.go`,2026-09-23,真机未验):就绪位为假、又没有待确认的改动时,后台
  **重新完整装一遍路由**(与服务器旁路自愈同一个入口、同一把锁,锁里再查一遍就绪位),就绪位
  **只在真的装成功之后**才回到 true。**刻意没有选「去内核看一眼、看着没问题就置真」** ——
  那是凭观测放行,覆盖不到的那条路由会被一起宣布完好。让路 / 已就绪都返回哨兵错误,**不许报成
  「重装成功」**。**顺序承重**:这个工人在 Hijack 成功之后才起,停止那一步 push 在「restore default
  route」之后(LIFO 下先停,且等循环真的退出)—— 从 `OnControlReady` 里起的话,正常关机时就绪位
  一被置假,它会把刚拆掉的劫持路由装回去(`TestRunStartsTheRoutesReadyRepairAfterHijackAndStopsItBeforeRestore`)。

## 内核路由的自愈(问内核,不信记账)

- **macOS 的 `DirectDialer` 需要一条 scoped 默认路由**(2026-08-13,真机已验)。`IP_BOUND_IF`
  只查该接口的 scoped 表,而单网络服务的 Mac 上 scoped 表里没有 default ⇒ **所有用户 direct
  规则 ENETUNREACH,隧道毫发无伤**(server bypass 恰好是显式 en0 路由)。`Hijack` 顺手装
  `route add -ifscope <dev> default <gw>`(只进 scoped 表,隧道的 `0/1`+`128/1` 照旧压过一切)。
  **这条路由必须「可选」,装失败时不许进待删列表**:多服务的 Mac 上系统自己就有一条,
  "File exists" 若记进待删列表,拆除时会删掉系统那条(`darwinRouteSpec.optional`)。非可选的
  路由失败仍然中止并回滚。**`network is unreachable` 是路由问题,`i/o timeout` 是目标不应答。**
- **直连出口自愈**(`egress_repair.go`):路由会在启动之后丢,且对所有既有信号隐形。重装的触发
  条件是**去问内核那条路由还在不在**,不是「记账里的 underlay 变了」。
- **服务器旁路自愈**(`bypass_route_repair.go`,2026-09-04,真机未验):休眠唤醒后 en0 重新关联,
  macOS 冲掉挂在它网关上的路由(含 bx 的服务器 `/32`),utun 上的 `/1` 照旧活着 ⇒ sing-box 到
  VPS 的连接进了 TUN、被 kill-switch 拦下 ⇒ **成环,静默且自锁**。签名是 1–2ms 的 EOF 刷屏。
  每 30 秒 `route -n get <ip>`(**不带 `-ifscope`**:子进程是普通 socket),接口是我们的 TUN 就经
  `controlServer.reassertRoutes` 重新落实全部路由;有待确认的改动时让路。判据三态
  (`decideServerBypassIntact`):**没有服务器可查 / 问不出来是「不知道」,不是「完好」**。
  循环体与 direct_egress 共用 `watchKernelRoute`。只有 darwin 有探测原语。
- **服务器旁路重新跟随 DNS**(`bypass_refollow.go`,2026-09-04,真机未验):「什么必须绕开隧道」
  曾只在启动时算一次,VPS 换 IP 后 NAS 静默断网一个月。**不另造判定**:主传输连续不健康 ≥ 2
  分钟时替用户按一次「重读配置 → 防环解析 → 发布 → 变了就 rehijack」(`refollowServerBypass`),
  再 `Reconnect` 让子进程重新解析;两次之间 ≥ 5 分钟(否则是 DNS 探针)。**必须在 `cs.mu` 里且
  有待确认改动时让路**(刷新是替换语义,会把还没落盘的新服务器剔出旁路);`Reconnect` 在锁外。
  只对域名链接有意义。接线由 `TestRunWiresTheServerBypassRefollowLoop` 读源码钉住。
- **Linux:Tailscale 的 WireGuard UDP 绕开劫持**(2026-09-04):tailscaled 发往对端公网地址的
  WG UDP 曾落进 pref 200 → table 100 → 经隧道出去,直连永远建不起来、只能走 DERP(`tailscale
  netcheck` 的 `UDP: true` 是假安心 —— 它探的 STUN 恰在 bypass 里)。修法一条规则
  `ip rule add pref 90 fwmark 0x80000/0xff0000 ipproto udp table main`(`optionalRouteUpSteps`,
  blockV6 时 `-6` 同款)。**只认 Tailscale 打的标 + 只认 UDP**;**是可选步骤**(`ipproto` 要
  iproute2 ≥ 4.17,busybox 没有,混进必装会让能起的机器起不来)。netns 台子只能证明退路,证明
  不了规则真装上了。

## 按应用看分流(`apptraffic.go` + `internal/appattr`,2026-08-19,整套真机未验)

起因:腾讯会议绕一圈查了半小时,而 bx 在数据面上每条连接都看见了(源端口在
`stack.TransportEndpointID`,判定在 `route.Reason`),只是从没放在一起。**包**:`internal/appattr`
(纯判据,AST 纯度守卫)· `appsource_{darwin,other}.go`(平台原语)· `apptraffic.go`(订阅、
环形缓冲、字节账)· Core `/v0/apps` → Guardian `/v1/apps`(owner 门)→ 菜单。
**动 `internal/appattr`、`internal/dialer` 的 `AppRecorder`/`appTrackedConn`、`internal/tun`
引擎的 relay 时,这份不会自动加载 —— 先读这一节。**

- **两个字节布局陷阱**(共同特征:失败得不明显):① 块按 8 字节对齐而 `xso_len` 不含尾部填充
  (TCP 的 `XSO_TCPCB` 块 len=204,下一块在 +208;不补齐则 TCP 表只解出 1 条而 UDP 完全正常);
  ② `so_last_pid` 在偏移 68、`so_e_pid` 在 72,**不在块尾**。**fixture 必须来自真机**(已脱敏
  在 `testdata/`),另配一条合成数据的偏移隔离测试 —— 两条各守一半。
- **`so_e_pid`(替谁干活)必须查活性**:委托方死了之后它是陈旧值,不查就把流量记在一个不存在
  的应用上,界面上完全看不出。
- **归因的键带协议维度**(`appattr.PortKey{Port, UDP}`):TCP 与 UDP 端口空间相互独立。
- **端口复用清账只对 TCP 生效**:UDP 一个 socket 服务多个对端(会议打 STUN + TURN + 多个
  peer),累加才对;无条件清账会让会议媒体流字节系统性偏低而无任何报错。
- **字节数是近似值,界面必须说出来**(窗口底部那句,守卫钉住)。字节账是 map + 一把**全局**
  mutex(定长表要开四张、常驻 2MB);临界区很短但争用数字只有真机能给。
- **`unknown` 单独成行**,不摊不丢;**陈旧提示只陈述事实,原因只给可能性**。

**采集只在有人看的时候发生**(隐私前提):窗口开着才订阅,**30 秒 TTL、靠拉取续期**(菜单被
强杀时没人退订);全内存、不落盘、不进日志;未订阅时不问内核、不记字节、不攒历史,由测试逐条
钉住(不用基准测试,基准不会让 CI 转红)。**但未订阅时仍维护一张活连接表**(每条连接两次全局
锁获取,不是每包一次);表里只有端口与判定,没有应用名。**别再照抄「没人看时开销精确为零」。**

**订阅之前建立的连接必须看得见**(真机 bug:窗口只看到 2 个应用而 `bx status` 报 66 条连接;
开会开到一半才打开窗口,它会说腾讯会议**不在**走隧道)。活连接表 + 订阅时播种:
- **种子只在一次订阅开始时播,续期不播**(菜单每 5 秒续一次,否则连接数线性膨胀)。
- **活连接表一条流一个条目**(单调 flowID 为键,另有 `map[PortKey]int` 索引),否则一个 socket
  上并存的 N 条流被压成一条,「同一个事实按窗口打开时机给出不同答案」。
- **配平按构造成立:谁记账谁释放。** 释放在 `dialer.AppRecorder` 上,`DialWithInitial` 是薄壳、
  判定全在 `dialInner`(单一漏斗 —— 十几个 return 逐个包会漏);拿到 conn 就包 `appTrackedConn`
  (`Close` 时经 `sync.Once` 释放),返回错误就地释放;被 kill-switch Block 的连接也已经进了表。
  **引擎那条 defer 不许加回来**(双重释放会把还开着的连接抹掉)。代价:「relay 真的会关
  upstream」成了表正确性的前提(`TestEngineClosesTheUpstreamConnSoTheDialerCanRelease`)。
  包装层**嵌入** `net.Conn` 不逐个转发(吞掉 `SetReadDeadline` 不报错,只让连接永不空闲结束)。
- **活连接表的唯一边界就是释放**:泄漏不会涨到 OOM(硬上限约 13 万条),真正的后果更早也更糟 ——
  陈旧种子填满 4096 格环形缓冲并绕圈,报告从残缺退化成错的。**不要**把 `len(live)` 发布出去
  (`/v0/apps` 只在有人订阅时可读,而泄漏恰在没人看时累积)。

**报告是「最近 60 秒」,归因发生在连接还开着时**(真机证据:`unknown` 是最大的一行且有真实
速率 ⇒ 新的 unknown 在源源不断进来)。因果曾被写反:主因是**归因发生在读取时**,活不过一次
刷新的连接在被归因之前 socket 就没了。
- 滚动窗口 60 秒(`appattr.ReportWindow`,纯判据 `InReportWindow`)。**字节账必须跟着裁**。
- 订阅期间 250ms 的后台 resolver 把归因存进 `ConnRecord.Owner`;`Snapshot` 优先用存着的。
- **resolver 绝不许持着 `t.mu` 调 `OwnersByPort`**(两次 sysctl,而 `t.mu` 是整机每条连接都要
  过的全局锁):持锁挑键 → 放锁 → 问内核 → 重新持锁**按序号**(`bufferedRecord.seq`)写回,
  不按下标。白盒守卫让注入的 `appSource` 被调用时自己去抢 `t.mu`。
- **还开着的连接不按时间裁**;裁剪的早退判据**只看时间**。
- **未订阅时 resolver 一次都不跑**;测试里默认关掉它(`newAppTrafficNoResolver`)。
- 已知代价:窗口开着时每秒最多 4 次 `OwnersByPort`;报告答不出「开窗以来一共多少」。

**窗口那半**(七列、搜索框、速率 nil vs 0、心跳必须活不过它的窗口)见
`apps/macos/BxMenu/CLAUDE.md`「按应用窗口」与 `docs/lessons/2026-08-app-traffic.md`。
真机验收清单:三组都在、腾讯会议出现在预期的组里;关窗后 CPU 与拨号回落;制造阻断后
`Blocked` 组出现内容;`unknown` 占比不高;每 5 秒重建会不会把滚动位置拉回顶部(已知未修);
窗口开着时 Guardian 的 CPU。
