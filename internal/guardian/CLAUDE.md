# CLAUDE.md — internal/guardian(控制面 daemon)

本文件只在读到 `internal/guardian/` 下的文件时加载。跨领域的判据留在根目录 `CLAUDE.md`:
**逃生路径不变量**(`sudo bx down` 任何一步失败都落强制拆除)、**故障可观测性**(完整
错误进 Guardian 日志、响应体只带码)、观测层三分、免密授权面(`authorizeOwnerPeer`
只给 `/v1/up`、`/v1/down`、`/v1/rules` 这类)。**这里不重复,改 Guardian 之前两份都要读。**
2026-09-23 从根目录下沉;过程叙述在 `docs/lessons/2026-08-control-plane.md`、
`docs/lessons/2026-09-servers-and-core-start.md`,下沉前的原文逐字存档在
`docs/lessons/guardian-archive.md`。

**贯穿这个包的三条**,每一条都来自真实事故:
- **停止路径不许因为别的事没做完而变慢或失败**(2026-08-04:路径恢复卡在 attempt 178、
  71 分钟里关不掉保护)。任何新加的等待、预算、清理都先问它会不会拖住 down/shutdown。
- **「开不了」永不许升级成「关不掉」。** 所有权不确定的锁存不许升级成 `recoveryBlocked`;
  维护挂起不许写进 `guardian-state.json`(旧 Guardian 读不动 ⇒ `Manager.Down` 永久报错)。
- **双 Core 是最坏结局。** `supervisor/control.go` 在 `net.Listen` 前先 `os.Remove`,第二个
  Core 会静默夺走控制 socket,两个 Core 争路由、先退出的那个用旧快照掀掉另一个的劫持 ⇒
  `bx status` 显绿而流量明文直连。**凡是会起 Core 的路径,准入一律向系统现扫,不信记账。**

## 平台缝:`lifecyclePlatform`(`lifecycle.go`)

daemon 组装只经它选平台:RequireDaemon / NewBarrier / DiscoverGateway / NewDNSManager /
NewNetworkObserver / PeerCredentials 六个构造器字段,反射 `validate()` + 三平台各一条行为
测试钉住「清单无洞且接的是本平台那份」。**`scanRunningCores` 刻意不在清单里**(注入钩子
无参,转发会丢 `reason=` 审计标签,缝留在编译期自由函数);`RemoveBlockingBarrierRoutes`
也不在(CLI 逃生口专用,独立于 daemon)。

- **linux 的门已开**(`daemon_linux.go` 的 `requireDaemonPlatform` 直接 `return nil`,
  `TestLifecyclePlatformLinuxGateIsOpenNowThatEveryPieceIsSupplied`),每一块都有 netns
  台子背书(`harness_{barrier,manager,daemon}_netns_linux_test.go`)。**开门不改变 linux
  产品形态**:生产 linux 仍是 systemd 直管 supervisor,没有任何东西会去装或拉起
  Guardian —— 那句承诺由**没有调用方**保证,不由这道门保证。darwin/linux 之外仍焊死
  (`TestRunDaemonFailsClosedOnUnsupportedPlatform`)。
- **移植顺序不许反**:照字段清单供货(procscan/peercred/barrier 各加 `_<os>.go`),
  **先实现 `scanRunningCores`**,`requireDaemonPlatform` 最后放开。只放门不实现扫描 ⇒
  扫描恒报错 ⇒ 恒 fail-closed ⇒ `bx up` 永远起不来 Core。
- linux 屏障(`barrier_iproute.go` 纯计划 + `barrier_linux.go`):pref-120 rule + table 90 +
  **throw 私网 carve** —— linux rule 命中即终止查找,darwin 主表最长前缀救私网的语义必须
  用 throw 亲手移植;pref 120 > 100 保住 bx 打标出站、< 150/200 压过劫持;网关经
  `supervisor.LinuxDefaultRoute` 复用 metric 感知解析,不许手抄。DNS 用 `DNSNotNeeded`
  第四态(「本平台无此事」≠「该接管没接管」);observer 显式 nil(不装假观测)。
- **netns 台子的两个坑**:子进程 re-exec 不传 `-test.timeout` 时继承 10 分钟默认值,死锁
  表现为「父进程超时 + 零输出」;`CombinedOutput` 要等管道 EOF,而管道被**孙进程**(被测
  编排 spawn 的 Core)继承、永远不来 —— 改走临时文件。隔离机制在 `internal/netnsguard`
  (与 supervisor 共用;写错会把 tmpfs 盖在宿主真实的 /run 上)。

## Core 所有权:向系统现问,不信自己的记账

- **macOS 的「进程不存在」不走 ESRCH**:`sysctl kern.proc.pid` 成功但写回 0 字节,`x/sys`
  转成 **EIO**,于是 `ErrProcessNotRunning` 曾在本平台永不可达,两处专为它写的自愈是死代码
  (后果:`down` 被判成崩溃 → 重启失败 → 永久 500)。**判据不凭 EIO 断言**(它也可能是真
  I/O 失败):只有 `kill(pid,0)` 明确回 **ESRCH** 才判不存在,活着/EPERM/求证失败一律保持
  不透明错误。
- **`looksLikeCore` 刻意不依赖可执行路径**:更新后旧版 Core 跑在 `runtime/<旧版本>/bx`,
  按路径匹配会漏认。判据 `basename(argv[0])=="bx" && argv[1]=="run" && uid==0`;
  **过度匹配的代价是拒绝启动(安全),漏认的代价是灾难**。
- **「问不出来」≠「没有」**:枚举失败、或枚举到进程却一个 `procargs2` 都读不出,一律
  fail-closed(`decideCoreScan`)。
- **两段式启动标记**:fork 一返回就落带子进程 PID 的 `spawned` 记录再去验明;死了自愈,
  **活着仍 fail-closed**(从没验明,既不接管也不当它不存在)。**`launching`(PID==0)
  仍一律 fail-closed**:「PID==0 ⇒ 确定没 fork」不成立 —— `spawned` 那次写盘失败时 fork
  已发生而盘上仍只有 `launching`(`TestExecCoreRunnerPersistenceFailureLeavesDurableUncertainLaunchMarker`)。
  盘上无记录的路径同样要求证,**别叫人手删 `core-process.json`**。
- **所有权不确定的锁存:释放是求证,不是承诺。** `Down` 从不清它(用户「down 再 up」管用
  是因为 CLI 落到强制拆除、把 Guardian 整个 bootout 掉 —— **真正管用的一直是杀掉 daemon**)。
  用户发起的 `Up`/`Migrate` 经 `confirmNoCoreForRelease` 重新求证:**两次扫描都干净、中间
  隔一个沉降窗口**才释放。它与 `Down`/`Recover` 用的 `confirmCoreStopped`(第一次扫干净就
  算数)**偏置刻意相反,把两者「统一」掉就是把双 Core 的门打开**。启动恢复刻意不重新求证
  (`retryDaemonRecovery` 每 5 秒一轮永不放弃);**这套纪律不许搬进任何按时钟驱动的路径**。
- **`ForceStop` 手里没句柄时去问系统**:占主导的「没句柄」是我们自己那个 Core 已经退了
  (wait goroutine 在 `waitpid` 返回就 forget)⇒ `ErrProcessNotRunning` ⇒ 清记录 → nil;
  身份比对复用 `sameProcessIdentity`(不许比 `Stop` 更严,更严就是又一道通向
  `core_ownership_uncertain` 的窄门)。系统说还在或答不上来才拒绝
  (`TestCoreThatDiedOnItsOwnIsNotReportedAsOwnershipUncertain`)。

## Core 起不来时,说出它为什么起不来(2026-09-13,真机未验)

所有者原话:「vps 之前不通,但 bx 不会告诉我是 vps 不通,用户会以为是 bx 自己的问题。」
病因(`dial tcp …: i/o timeout`)从第一秒就在 Core 手里,被逐层剥掉:Core 卡在隧道健康门、
**在建出控制 socket 之前**返回 → Guardian 只知道「socket 20 秒没出现」→ 清理去请 Core
从 `core.sock` 上自己退出,而那个 socket 正是它没建出来的东西 ⇒ 真话 `core_health_failed`
被假话 `core_ownership_uncertain` 顶掉,留下的孤儿又盖住用户的重试。**这条链违反的就是
「停止路径不许依赖别的先成功」。** 横跨四处:本包(清理、宽限、读记录)、
`internal/supervisor/tunneldiagnosis.go`(判别拨号)、`internal/corestartfailure`(记录,
叶子包)、`internal/cli/corestartadvice.go` 与菜单(措辞)。**动其中任何一处先读这一节。**

- **强杀只对从没服务过的 Core 安全。** `supervisor.Run` 的顺序是 健康门 ≺ `OpenTUN` ≺
  控制 socket ≺ `Hijack`(grep 函数名核顺序,别信行号):卡在健康门的 Core 没开过 TUN、
  没装过路由、没碰过 DNS。**但「健康门没过」不蕴含「从没服务过」**(UDP 档没就绪、隧道
  抖了、升级时版本对不上的 Core 已经开了 TUN、装了路由,强杀会跳过它的 defer 还原)。
  判据是 `coreEverServed(process, state)` = **它的控制 socket 亲口报过自己的 PID**;
  `cleanupCoreAfterFailedStart`(`manager.go`)是**唯一**决定点
  (`TestACoreThatAnsweredTheControlSocketIsNeverForceKilled`)。`m.runner.ForceStop`
  全包只许一个调用点(`TestTheKillCapabilityItselfHasExactlyOneCallSite`),协作关闭那侧
  是一份带理由的具名白名单。
- **时序陷阱:Guardian 的健康等待 20s == Core 的隧道健康窗口 20s,而后者起步更晚。**
  不加宽限的话,Guardian 放弃的那一刻 Core 才开始判别拨号、一个字节没写就被 SIGKILL ——
  机制在它唯一存在的那个场景里一次都不触发,而且没有东西会转红。宽限
  `coreStartFailureGrace`(`corestartfailure.go`)**由 `supervisor.TunnelDiagnosisTimeout`
  派生**,只在失败路径上跑、答案一到就返回、句柄没了就收手。**余量那 3 秒没有测量依据**;
  空手而归时日志 `guardian_core_start_failure_record_absent reason=… waited=…` 说得出是
  「Core 没写」还是「我们早放弃了」——**要调这个数,先去日志读 `waited`**。
  没选的两条:缩短 Core 的健康窗口(真的缩短隧道能用多久建起来)、拉长 Guardian 的等待。
- **宽限不许花清理那份预算**:`startCoreLocked` 有三个调用点传 `restartTimeout=25s`(崩溃
  重启、调谐环 `start_core`、`Manager.Down` 里 DNS 还原失败后的补偿重启 —— **一条停止
  路径**)。上界取 operationCtx 的 deadline,不自己再算一遍 `reserveCleanup` 那份;余量为零
  时仍读一次、一拍不等(`TestTheStartFailureGraceNeverEatsTheCleanupReserve`、
  `TestZeroGraceStillReadsOnce`)。已知代价:一次失败的 `bx up` 最坏多押住 mutation 槽约 8 秒。
- **判别是一次观测,不读 sing-box 的 stderr。** 健康窗口过后对 `serverHostFromLink` 的
  host:port 直连拨一次(上限 5s),**只在失败路径上发生**(`TestHealthyStartupNeverDials`)。
  五种结局:拨不通(拒绝/超时)⇒ `tunnel_unreachable`;拨得通 ⇒ `tunnel_handshake_failed`
  (两者处置相反,合成一个码会把「reality 的 SNI 证书过大」那次教训埋回去);**UDP-only 传输
  (hysteria2)一次都不拨** ⇒ `…undetermined_udp_transport`(活着的 QUIC 服务器根本不应答
  TCP SYN;判据取传输种类不取端口);**SYN 没离开本机**(ENETUNREACH/EHOSTUNREACH/EACCES/
  EADDRNOTAVAIL/DNS 错误)⇒ `…undetermined_local_dial`(指着 bx 自己的直连器,不指着 VPS);
  其余落笼统码。三个 undetermined 共享**拼接得来的前缀**,消费方不可能把其中之一读成
  「服务器没事」。**超时归 unreachable**(事故本身就是 i/o timeout)。分类全靠哨兵错误,
  一条字符串匹配都没有(`TestStartFailureCodeNeverGuessesFromText`)。Windows 的 errno 是
  另一族,孪生表由 `TestEveryLocalDialFailureHasAWinsockTwin` 守住。
- **判别拨号绑网卡在本机失败后,不绑再试一次**:darwin 的 `IP_BOUND_IF` 只查 scoped 路由表,
  而那条 scoped 默认路由由 `Hijack` 装、排在判别之后。**只在「SYN 没离开本机」那一族上退**,
  拒绝/超时/解析不了是观测到的答案,不许被第二次拨号覆盖。
- **记录**:Core 在 `bx run` 的错误路径上原子写 `/var/lib/bx/core-start-failure.json`,
  **只有 schema/pid/at/code,一个自由文本字段都没有**(`TestRecordCarriesNothingButACode`);
  flag 名也在叶子包里。spawn 前用 `Discard` 清(连被 SIGKILL 留下的临时文件一起);读的时候
  **PID 是这次 fork 的 + `at` 落在本次窗口内 + 码在白名单里**,任一项不对 ⇒ 「这一次没说」,
  回落 `core_health_failed`,绝不猜。**读发生在收拾那个 Core 之前**。写方与读方的往返由
  `TestTheCoreWritesExactlyWhatTheGuardianReads` 钉住(此前三条一行的改动都能让整个功能退回
  改动前而三个包全绿)。
- **应答体仍然只带码**:「哪台服务器」「你还有哪几台」客户端本来就合法持有,各自在本地拼
  那句话;唯一新增的是 `/v1/servers` 的 `current_server`(`bx setup` 写的配置在清单里一个
  主机名都没有)。**措辞四条**:只说 bx 观测到什么(「bx 连不上 <host:port>」,绝不断言服务器
  状态);两种结局措辞相反;undetermined 不许读成「服务器没事」;只在真有另一台时才提、
  **绝不打印链接**。local_dial 那档也点名 host:port,并说出 DNS 那种病因(换一台确实有用);
  端口解不出就整条不给 `nc -z`;渲染出的话不许带 markdown 的 `**`。守卫的判据是**整句话**
  不是枚举值(`TestEveryStartFailureOutcomeReadsDifferently`)。不自动切服务器(所有者定死的边界)。
- **Linux 那一半(known-gaps A9,2026-09-23)**:linux 走 systemd、不经 Guardian。unit 的
  ExecStart 带上 `--start-failure-file`(老机器由 `bx update` 用 `install.UpgradeUnitExecStart`
  补上,不重启);Core **启动时先清掉旧记录**(记录只代表最近一次启动 —— 没有 Guardian 在
  spawn 前替它清);`bx status` 在 Core 不应答时,**只在 systemd 说 `activating`/`failed`
  (要跑却起不来)时**读记录、走同一个 `coreStartFailureAdvice`,用户自己停掉的不说;非 root
  读不到 `/var/lib/bx` 时如实说「要 sudo 才看得到原因」。「Full reason」那条指引按平台给:
  linux 是 `journalctl -u bx.service`,不是 `/var/log/bx.log`(`coreLogCommandFor`)。
- **已知缺口**:菜单在
  `.warning`/`.connected` 之外拿不到码;**升级路径的健康失败仍不读记录**(那条路的码空间
  是另一套,单独立项,别顺手改)。「读不到配置」(文件不在 / 权限不够)有自己的码
  `config_unreadable`(`supervisor.ReadConfigFile` 挂哨兵,2026-09-23)—— 不借 `config_unusable`
  (那会叫人去改一个还没写过的文件),也不落 `other`。真机验收在 `docs/acceptance-pending.md` B2。

## 调谐环(判断与执行分离)

- **纯函数 `decide`** 把意图 × 观测 × 三道栅栏映射成一组命名的意图。**没有「装屏障」这个
  动作是刻意的**(装它要先探网关,瞬时失败降级成无 server bypass 的整机黑洞;放进按时钟
  驱动的循环就是每拍重掷一次骰子)。**所有权不确定是栅栏,不是待收敛的差异。**
- **零值读起来像一切正常**:`reconcileDecision` 的零值恰好是健康机器的判断。
  `ReconcileReport.At` 是「跑过没跑过」的唯一判据;`UnobservableItems()` 把观测质量折进
  变更比较(**变瞎本身就是一次变化**);panic 收在**每一轮内**,不收在 `for` 外。
- **可执行白名单三项**:`restore_dns`、`clear_orphan_barrier`(desired=off 的清理)、
  `start_core`(`reconcile_execute.go`,`TestExecutableWhitelistIsExactlyOffCleanupPlusStartCore`
  钉死 —— 穷举守卫只测名单外的动作,扩名单必须有意识地改到它)。**`stop_core` 只观察**:
  desired=off 而 socket 应答最常见的来源是 `sudo bx run` 调试路径。
- **执行纪律**:mutation 槽 **try-acquire 不排队**;**槽内按动作复核前置**(`requiredDesired`,
  决策与拿到槽之间用户可能刚好 up/down ⇒ `preconditions_changed` 让路);**一轮至多一个**;
  **清理失败不放弃、靠退避限频**(停了残留就永久无人管);动作全部复用既有原语(清屏障 =
  逃生口同款 `RemoveBlockingBarrierRoutes`,经 Manager 字段注入)。`Executed` 与 `Actions`
  并列进报告绝不合并;让路措辞刻意不像故障。
- **`start_core`(③c,2026-09-13 真机已验)**:修 Core 崩溃后重启**一次**失败就再没人试的
  无人区。**准入是槽内现扫 `ScanRunning`,不是 socket**(`decideStartCoreAdmission` 三态:
  0 个才起;≥1 ⇒ `core_process_present`,只显形;没测成 ⇒ `core_scan_failed`)。复用
  `startCoreLocked`。**每段故障封顶 5 次**(`maxReconcileStartCoreAttempts`;起进程不幂等,
  与「清理永不放弃」刻意不同),过了发布 `start_core_exhausted`,渲染成「已放弃,等你
  sudo bx up」,**不许渲染成让路**。**段的重置排在 `upLocked` 之前**:用户按下开关这个动作
  本身结束一段故障 —— 排在成功之后时,唯一需要它的场景(up 失败)恰好不生效,真机上
  `desired=on` 而明文直连了 9 小时 16 分钟(`TestStartCoreCapResetsEvenWhenTheUserUpFails`)。
  仍不做:`stop_core`、重启卡住的 Core、装屏障、解 Uncertain 锁存。
- **真机未验**:③b 的两个清理动作;③c 的「改名 sing-box + kill -9」合成路径与「用户 up
  失败 ⇒ 真的重新试 5 次」。

## 维护挂起(升级期间的停机)

**`desired` 只记录用户意图,停机用正交的 `/var/lib/bx/maintenance-hold.json`。** 用「写
`desired=off`」表达停机,任何忠实的调谐器都会收敛到 off —— 那正是 bug 本身。**绝不能把挂起
加进 `guardian-state.json`**(裸 JSON 字符串、无信封无版本,旧 Guardian 读不动 ⇒
`recoveryBlocked` ⇒ `Manager.Down` 永久报错,而升级恰是新旧共存的时刻)。会自己起 Core
的路径共五条(干净 `Down`、强制拆除、启动恢复、`Down` 的 DNS 补偿重启、
`recoverUpdateLocked`),**前四条必须认挂起**,第五条刻意不拦(拦住会把没做完的自更新永久
搁浅)。**挂起写失败就退回写 `desired=off` 并照常拆除**。用户显式 up/down 无条件清挂起,
且清挂起对拆除成败无条件。

## 状态 watch:`GET /v1/status?wait=<generation>`(2026-08-17)

- **代际号由内容派生,广播只是叫醒**:单一发布点 `statusPublisher`(`statuswatch.go`)重算
  `Status`、比投影 digest,不同才 `generation++`;**不许任何调用点直接递增**。`poke` 不带数据,
  所以一次广播不可能是错的,漏一个只是慢到下一个 3 秒兵底。广播点只有 `/v1/up`、`/v1/down`
  落定之后(用户正站在旁边等反馈的两处)。
- **投影 = 整个 `Status` 减排除名单,不是白名单**(`statusdigest.go`):默认参与 ⇒ 新易变字段
  让 watch 吵、当场看得见;默认不参与 ⇒ 菜单静默地不再对新信号反应。排除名单每条写明为什么
  易变:`StatusGeneration`、`Core.LatencyMS`、`FailingRules[]` 的计数(只清计数、保留
  Kind/Rule)、`Reconcile.At` 与 `Reconcile.UnchangedRounds`(同一类东西的两面:循环又跑了
  一轮的记账;真机 soak 抓到只排了前者)、`Recovery.UpdatedAt`。
- **嵌套字段必须穷举分类**(`statusdigest_nested_test.go`):`Status` 深度 ≥2 的每一个叶子要么
  进 `nestedDigestExclusions`(写理由),要么进 `nestedDigestSignals`,少一个就红;反向断言
  禁陈旧条目。**往 `ReconcileReport`/`CoreRuntime`/`RecoverySnapshot` 加字段的人会被这条
  逼着回答「它是真事件,还是循环又跑了一轮的记账」。** 调谐环真正的信号(`Actions`/`Held`/
  `Unobservable`/`CoreScan`/`Executed`)必须移动投影(`TestReconcileSignalFieldsStillMoveTheDigest`、
  `TestOneReconcileRoundDoesNotMoveTheDigest`)。
- **深拷贝是承重的**:`FailingRules` 是切片,在副本里清零会改掉真正发布出去的那份。
- **代际号比较用 `!=` 不用 `>`**(服务端 `wait()`、`internal/cli/statuswatch.go`、菜单
  `runWatchLoop` 三处一致):Guardian 重启后计数器从小数重来,`>` 会永久挂住。
- **关机先唤醒 parked 的 watch**:`Daemon.Shutdown` 先 `beginShutdown()` 再 `server.Shutdown`,
  否则一个挂 25 秒的 watch 让关机慢 25 秒。
- **能力门控**:旧 Guardian 忽略 `wait`、秒回无 `status_generation` 的应答,客户端分不清
  「变了」与「不支持」—— 真机撞过 26%~46% CPU、上千次/秒。判据 `status.Capabilities == nil`
  (从没声明过)与「声明了、没有这项」分开报;另有 1 秒 floor 兜底。
- **空闲开销不是零**:每个 parked waiter 自带 3 秒兵底,菜单开着时约 20 次/分钟重算
  `observableStatus`。菜单那一侧见 `apps/macos/BxMenu/CLAUDE.md`。生产上 Guardian 只在
  darwin 跑,Windows 托盘另有自己的轮询。**真机未验**:菜单那半的静默性。

## 陈旧的恢复快照不许把 Protected 改写成 Blocked(2026-09-04/07)

`observableStatus`(`localapi.go`)曾经只要上一次路径恢复的快照写着 failed,就把 Manager 的
Protected 改写成 Blocked —— 而**Core 侧的 verify 失败从不碰 `m.status`、从不装屏障**,那个
Blocked 只是一个标签。真机两次:一次在用户 down/up 之后,一次用户什么都没做(睡眠抖动里 20 次
verify 在 105 秒内耗尽,醒来全部自愈,菜单裂开一个多小时)。**「status 是记住的而不是推导的」
那一类失效**,三处让快照退场:
- `Manager.Up`/`Down` 成功后 `retireCompletedPathRecovery`(**已结束**的才退场;正在跑的由它
  自己发布结局);
- 调谐环每轮 `retireContradictedPathRecovery`:观测到 verify 要看的五项(捕获在我们的 TUN、
  屏障不在、DNS 归 bx、Core 应答、隧道健康)全满足,且 Manager 说 Protected、无在跑的恢复;
- **状态组装那一刻** `recoverySupersededByCore`:`CoreRuntime` 带 `RoutesInstalled`/
  `DNSListening`/`UDPRequired`/`UDPReady`/`TunnelHealthy`(问不出来保持 false),全满足 +
  Manager 说 Protected ⇒ 发布 idle 并退场。**调谐环一个人不够**(退避最长 10 分钟,刻意不从
  恢复代码叫醒它)。Core 问不出来一律不算。
观测层也补了反方向:believed=blocked 而 `barrier_present=False` 会产出一行 divergence。
CLI `assembleClientStatusReport` 与菜单 `recoveryPresentation` 各自那份「有 failed 快照 ⇒ Blocked」
的拷贝没动 —— 修在源头,三处按构造一致。**真机未验。**

## 响应与失败码

- **JSON 响应必须显式带 Content-Length**(`writeGuardianJSON`:先整体 marshal 再一次写出)。
  体过 2KB 时 `net/http` 对流式写入改用 chunked,而菜单手写的 HTTP 读取器只认
  Content-Length(`TestGuardianJSONResponsesAlwaysCarryContentLength`)。
- **`/v1/update` 的错误带病因、响应体只带码**:`updateError` 带 cause、`Error()` 拼进去
  (进 0600 的 Guardian 日志),响应体经 `failureCodeForError` 只拿 code。健康门三种失败
  (Wait 报错 / PID 对不上 / 版本对不上)由 `runtimeRefreshCause` 说出是哪一种 —— 后两者说明
  **答话的 Core 不是 Guardian 以为的那个**
  (`TestUpdateHealthGateFailureKeepsItsCauseForTheLogAndItsCodeForTheBody`、
  `TestUpdateHealthGateSaysWhichOfItsThreeConditionsFailed`)。
- `failureCodeForError` 除了 `updateError` 只认两个由错误本身命名的哨兵:
  `recovery_incomplete`、`guardian_busy`(它们恰是最需要指引、按常规规则又会被省略码的场景)。
- 能力值(`CapabilityRules`/`CapabilityLogs`/…)是跨语言契约:菜单按字面量门控,改了值菜单
  就永久看不见那一页而两侧都不报错(`TestLogsCapabilityIsDeclared` 那一类)。
