# Guardian —— 2026-09-23 从根目录 CLAUDE.md 下沉时的原文存档

**这是存档,不是现行判据。** 2026-09-23 根目录 CLAUDE.md 开始按代码目录下沉,这些段落的
**判据**浓缩进了 `internal/guardian/CLAUDE.md`(那一份才是改代码之前要读的);这里逐字保留当时的原文,给想知道
「当初怎么换来的」的人看 —— 事故经过、真机数字、变异实测、被否掉的方案的完整理由。

**原文里有少数陈述在下沉那天已经不成立**,核过并在子目录那份里更正的有:「Guardian 只在 darwin 跑」(linux 的门已开,只是生产 linux 没有调用方)、「本期尚无 reconcile」、「第五条不变量以已知失败的测试留着」(现在是写明理由的 `t.Skip`)。
读到与 `internal/guardian/CLAUDE.md` 冲突的地方,以后者为准。

---
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

**菜单那一侧**(兜底轮询常量、1 秒 floor、能力自相矛盾时的残留循环、显式 vs 环境
的不对称、窗口开着时跟着环境刷新重拉)见 `apps/macos/BxMenu/CLAUDE.md`。

**空闲开销不是处处为零**:每个 parked 的 waiter 自带一个 3 秒兵底,每次醒来都要
重算一遍 `observableStatus`(一次 Core round trip + 两次小的磁盘读),菜单常驻
时约 **20 次/分钟**的重算,对照它取代的 30 秒轮询(约 2 次/分钟)是一个数量级
的上升——「没人 watch 时开销精确为零」这句话只对**没有订阅者**的情形成立,
菜单一开着就不是这个情形。真机 soak 除了数 watch 触发了几次,也该顺手采样
Guardian 的 CPU。

**`bx status --watch`(`internal/cli/statuswatch.go`)是这个功能唯一的只读真机
验证手段**:它让人在不动网络、不重装菜单的前提下,亲眼看到「敲 `bx down`
的那一瞬间 watch 就吐了一份新 `Status`」;菜单那一半的验证要重装 App。

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
sing-box,是 VPS(203.0.113.92)真的不通**:日志里 `start_core` 连着
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


## `/v1/update` 的失败码从来没到过客户端,病因也被整个丢掉(2026-09-17,真机诊断)

2026-08-05 立的故障可观测性不变量是「**完整错误写进 Guardian 日志,响应体只带
失败码**」。`/v1/update` 这条路上**两样都没有**,而它正是用户最需要线索的那一刻:

- `Manager.Update` 在健康门那里写的是 `newUpdateError("update_runtime_refresh_failed")`
  —— `m.health.Wait` **已经算出**了一句精确的话(真机上是「core health check timed
  out after 20s: core routes are not installed」),被整个丢掉。Guardian 日志那行
  因此是 `guardian_mutation_failed err=update_runtime_refresh_failed`,只有码。
  同一处还有另外几个码(prepare / gateway_discovery / recovery_metadata)一样丢 err。
- `failureCodeForError` **只认两个哨兵**(`errRecoveryIncomplete`/`errMutationBusy`),
  `updateError` 落进 default;而 `Update` 从不走 `needsAttention`,于是 `after.LastError`
  那条兜底也是空的 —— 响应体里**一个码都没有**,菜单上只有「guardian operation
  failed」加三百字通用排查。`guardianCodeHints` 那套机制在这条路上从未被触发过。

**修法把两条路分开**:`updateError` 带上 cause,`Error()` 把它拼进去(handler 打的
是 `%v`,那份日志是 0600 root:wheel),而响应体走 `failureCodeForError` **只拿 code**
—— 发布面一寸没扩,原始错误串一个字都不出 socket。

**健康门那三种失败方式此前在输出上完全一样**(Wait 报错 / PID 对不上 / 版本对不上),
后两种连 err 都没有,所以「把 err 带上」修不到它们:`runtimeRefreshCause` 让这道门
自己说出是哪一种 —— PID 或版本对不上说明**答话的那个 Core 不是 Guardian 以为的那个**,
与「Core 报的运行时事实里有一项不满足」是两类问题。

**守卫**:`TestUpdateHealthGateFailureKeepsItsCauseForTheLogAndItsCodeForTheBody`
(病因在 `%v` 里 **且** 码在 `failureCodeForError` 里,两条刻意不合并)与
`TestUpdateHealthGateSaysWhichOfItsThreeConditionsFailed`(喂一个 PID 对不上的
运行时,断言那个 PID 被点名 —— 少了它,只把 err 带上也能满足前一条)。
`newUpdateError`(无 cause)原样只返回码,既有那批逐字比对 `err.Error()` 的测试
一条没动。


## Core 起不来时,说出它为什么起不来(2026-09-13,真机未验)

**所有者原话:「vps 之前不通,但 bx 不会告诉我是 vps 不通,用户会以为是 bx 自己的
问题。」** 2026-09-12 那天他的 VPS(203.0.113.92)ssh 与 ping 都不通,而 `sudo bx up`
连着**七次**答 `core_ownership_uncertain` —— 三百字关于「系统里可能有第二个 Core」的
排查指引,一个字都不沾边。

**真相从第一秒就在 bx 手里**(`dial tcp 203.0.113.92:443: i/o timeout`),它是被逐层
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
