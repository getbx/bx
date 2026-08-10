# 维护挂起:让磁盘不再撒谎

## 问题

升级要停下 Core 换二进制,而这件事今天是用**写 `desired=off`** 表达的。于是磁盘上写着
「用户不想要保护」,而事实是**用户想要,只是此刻不能有**。一张欠条
(`/var/lib/bx/upgrade-intent.json`)记着回头把它打开。

**任何忠实的调谐器读到 `desired=off` 都会收敛到 off —— 而那正是 bug 本身。**

这不是调谐器的问题。欠条机制在调谐器出现之前就已经在漏:

- **它有一个活着的 bug。** `macOSDownAction`(`internal/cli/guardian.go:726`)在
  `macOSDownLifecycleDetailed` 报错时提前返回,跳过 `:735` 的销账 —— 而
  `forcedMacOSTeardown` **已经把六步破坏性动作全做完了**(`upgradeplan.go:110-118`
  写明了这一点)。净效果:用户敲 `sudo bx down`,DNS 还原超时,拆除报告失败,
  盘上留下一张陈旧欠条 + `desired=off`。下一次 `app-install`(菜单的 **Repair**
  就带 `--yes` 跑它)读到 `resolveUpgradeDesiredOn == true`,**违背用户明确的关闭请求
  把保护打开**。欠条要防的失效模式,反过来发生了。
- **它的读取偏置与自己的注释相反。** `LoadUpgradeIntent`(`store.go:98-101`)在**任何**
  `os.ReadFile` 错误上返回「没有欠条」,包括 EACCES/EIO,而 `:88-91` 的注释承诺的是
  「存在但坏了 ⇒ 仍算欠条」。只有解析失败享受到了那个 fail-safe。
- **没有人为「升级再也没完成」负责。** 「重启 Guardian 之后、开保护之前」崩掉,机器
  无保护地待着,欠条有效却没有任何东西在读它 —— 它只在**下一次** `app-install` 时被查。

## 设计目标,一句话

**`desired` 只记录用户的意图,永远不被后台操作改写;「此刻不能有保护」由一个正交的、
带原因与过期时间的维护挂起表达。**

做成之后,欠条文件、`?reason=upgrade` 参数(`localapi.go:123`)、请求作用域的标记
(`upgradeintent.go`)、以及 CLI 那份重复销账(`guardian.go:735`)**一起消失**。

## 关键取舍

### 一、挂起单独一个文件,绝不进 `guardian-state.json`

`guardian-state.json` 里是个**裸 JSON 字符串**(`store.go:64-74`),文件内容字面就是
`"on"` 或 `"off"` —— 没有信封、没有 `schema_version`、没有迁移机制。

往里加字段的后果不是「旧版本忽略未知字段」,而是:旧 Guardian 的 `LoadDesired`
**报错** → `recoverLocked`(`manager.go:807-812`)调 `needsAttention(…, "desired_state_read_failed")`
并置 **`recoveryBlocked = true`** → `Manager.Down` 第一句就返回 `errRecoveryIncomplete`,
**永久**。这正是 2026-08-04「用户 71 分钟关不掉保护」那次事故的机制。

而**升级恰恰是新旧两版共存的时刻**:`restartGuardianForUpgrade` 有窗口,回滚过的升级
更是把新格式文件永久留给旧 Guardian。

故:`/var/lib/bx/maintenance-hold.json`,自带 `schema_version`,整文件原子写,
**绝不 read-modify-write**(两个进程写这份状态且没有任何锁,`internal/cli/upgradeintent.go:21`
每次调用都新建一个 `Store`,那个 mutex 连进程内都保护不了什么)。

### 二、`handleUnexpectedExit` 必须认挂起 —— 这是整个改动最承重的一处

两条停机路径的防线**不对称**,这一点决定了改动范围:

| 路径 | 拦住「Core 退出 → 自动重启」的是什么 |
|---|---|
| 干净路径(`Manager.Down`) | `m.current = Process{}`(`manager.go:689`)让 `sameProcessGeneration` 返回 false,在 `:1056` 就返回了,**根本没读到 `desired`** |
| 强制拆除(`forcedMacOSTeardown`) | **只有** `:1067-1075` 那次读盘。它不进 `Manager.Down`,`m.current` 在活着的 Guardian 里原样保留,`deps.stopCore` 从带外杀掉 Core,监视 goroutine 触发,代际检查**通过** |

于是:`desired` 停止在升级时变成 `off` 之后,**强制路径失去它唯一的守卫**,一个活着的
Guardian 会在二进制换到一半时装上屏障、fork 一个新 Core。

**故 `manager.go:1075` 那个判断必须同时看挂起。** 这不是锦上添花,是这条改动能不能做的
前提。反过来也一样:只让调谐器认挂起、不动 `handleUnexpectedExit`,等于什么都没修。

### 三、强制拆除也要 purpose,但**绝不能依赖挂起写成功**

`downPurpose` 今天只传到 `cleanGuardianDown`(`:578`)。`forcedMacOSTeardown` 写两次
`desired=off` 且不带任何来由,而它从升级路径**三条分支**可达 —— 包括
`legacyCoreMayBeRunning`(`:464`),它在**探测仅仅失败**时也返回 true(`:550-559`),
且这个分支在 purpose 被查看**之前**求值。

处置:把 purpose 传进去,升级来由时**先武装挂起、再停 Core**(位置就是今天
`persistDesiredOff` 那一步)。但要守住「关闭与恢复路径不得依赖可能已失效的前置条件」:

**挂起写失败时,退回今天的行为 —— 写 `desired=off`。** 不是继续往下、也不是报错拒绝。
退回意味着调谐器会把机器收敛到 off(与今天一模一样,而升级结束会重新写 on),
代价是已知且有界的;而「既没挂起也没 `desired=off`」是一个**新的**失效模式:
活着的 Guardian 在换二进制时重启 Core。**宁可退回一个会撒谎但安全的状态,
也不要一个诚实但没人拦着的状态。**

### 四、用户的显式动作永远压过挂起

`upLocked`(`manager.go:534-539`)与 `Migrate`(`:459-464`)会**径直把 `desired` 写成 On**。
而挂起「不改 `desired`」这条,与它们并不冲突 —— 冲突的是挂起还武装着。

规则:**任何用户发起的 up / down 都清掉挂起。** 理由是欠条那条注释
(`upgradeintent.go:39-40`)给出的原始论证:「用户一旦真的关掉保护,欠条就被销了」。
挂起必须保住同一条性质,否则用户在挂起窗口里明确关掉保护,`desired` 记对了,
而挂起仍然武装着,又要靠额外规则去裁决谁赢。**让显式动作清掉它,这个问题就不存在。**

推论(**吸取欠条那个活 bug 的教训**):**清挂起这一步对拆除的成败必须是无条件的。**
强制拆除即使报告失败,它的六步破坏性动作也已经做完了 —— 那正是欠条今天留下陈旧
记录的原因。清挂起不许躲在 `if err == nil` 后面。

### 五、过期在**读取时**判定,不设定时器

沿用 `internal/toolkeys/store.go:154,182` 那个唯一持久化过期的先例:记 `ExpiresAt`,
读的时候比一次,过期即当作没有。不需要任何定时器,进程重启后照样成立
(`internal/confirm/deadman.go` 那套形状对、生命周期错 —— 它纯在内存里,而挂起
必须活过进程重启)。

**过期之后会发生什么,值得说清楚:** 挂起失效,而 `desired` 一直写着 `on` ——
于是 `bx status` 会显示一条**真实的分歧**(用户要保护、Core 不在),而不是一台
「看起来正确地关着」的机器。**这正是挂起强于欠条的地方**:欠条让机器看起来是关的,
挂起让它看起来是坏的 —— 而它确实是坏的。

过期本身**不会**恢复保护(没有任何东西在轮询,调谐器本期也没有执行权)。它买到的是
「不再压制」,不是「自动修好」。这一点要写进文档,别让人误以为过期是个自愈机制。

时长取 **15 分钟**:要盖住最长的一次可信升级(文件替换 + 重启 Guardian + 起保护;
下载发生在此之前),又要短到崩溃之后当天就能看出不对。常量导出、单点拥有
(照 `ReconcileStaleAfter` 的先例,`reconcile_loop.go:55-63` 写明了不许消费方各自重导)。

### 六、`desired` 与挂起必须同源

调谐器今天读的是**内存**里的 `m.Status().Desired`(`reconcile_loop.go:95`),而内存
**已经会撒谎**:`needsAttention` 会把调用方传进来的常量写进 `status.Desired`
(`manager.go:1396-1399`),好几处传的是字面量 `DesiredOn` 而磁盘写着 off。

挂起若从磁盘读、`desired` 从内存读,一轮之内就能出现两者互不相干的组合。

故:加一个把**两者一次从磁盘读出来**的入口,`handleUnexpectedExit` 与调谐器共用它。
这顺带修掉调谐器读到被 `needsAttention` 污染的 `Desired` 那个既有问题 —— 不是顺手
扩大范围,而是不这么做就没有一致的语义可言。

### 七、调谐器把挂起当**第四道栅栏**

不是「一处待收敛的差异」。挂起说的是「此刻不该有人动手」,与 `recoveryBlocked` /
路径恢复 / 所有权不确定同类:整轮停摆,并说清是被哪一道挡住的
(`held=maintenance_hold`)。

## 迁移:盘上已经有欠条的机器

新版 CLI 不再读 `upgrade-intent.json`。一台**正处在升级中途**的机器跨过这次切换,
会丢掉那张欠条 → 保护不被恢复。

处置:保留**一个版本**的只读兼容 —— 读到 legacy 欠条文件就当作一次已武装的挂起
(用默认过期时间),用完删掉。删除 legacy 读取的时机单独记一笔,不在本期。

`buildDarwinUninstallPlan` 的 `RemovePaths`(`uninstall_plan.go:58-64`)要加上新文件,
否则它活过卸载重装 —— `darwinCoreProcessStatePath` 与 `darwinUpgradeIntentPath`
就是为这条教训加进去的。

## 对外可见性

- **`bx status --json`**:发布挂起(原因 + 到期时刻)。**并且 `observe.Diverge`
  必须知道它** —— `diverge.go:44` 有一条 `intent.Desired == "off"` 的分支,而挂起
  期间 `desired` 保持 `on`、保护却不在,不告诉它就会冒出一条假分歧。
- **菜单(Swift)**:`GuardianStatus.swift:15,52` 把 `desired` 解成**必填**,但未知键
  安全忽略,故加字段对旧菜单兼容。菜单要显示挂起 —— 否则挂起期间它渲染成一个普通的
  「已关闭」外加一个可点的 Turn On。按 `:8-13` 已有的模式:**`hold == nil` 意味着
  「这一版 Guardian 没有挂起这个概念」,不是「没有挂起」**。用
  `CapabilityMaintenanceHold` 声明,与 `CapabilityReconcileReport` 同一机制。

## 本期不做

- **不给调谐器执行权**(那是③b)。
- **不解 `Uncertain` 锁存**(③b 的另一个前置,单独一期)。
- **不做非 darwin 实现**。Guardian 今天只有 darwin 实体。
- **不让过期去恢复保护**。见取舍五 —— 那需要③b 授权 `start_core`,而
  `reconcile.go:99-110` 已经写明 `CoreSocket==False` 不是安全的准入判据。

## 写计划时发现的规格缺陷(逐条修正,以本节为准)

计划作者对照代码逐条核过上面的取舍,报回六条。五条是真的规格缺陷,一条我改判。

**① 「`?reason=upgrade` 参数一起消失」是错的。** 取舍四要求「**用户发起**的 up/down
清掉挂起」,而升级自己的那次 Down 走的是同一个 handler,必须**不**清 —— 两者都需要
「这次 Down 是不是用户要的」这个请求作用域判据。同理,干净路径的
`Manager.Down`(`manager.go:710`)也要靠它才知道该不该写 `SaveDesired(DesiredOff)`。
**修正**:线上判据保留(改名成挂起的词汇),升级窗口里新旧共存的两个方向都退化成
今天的行为。消失的只有欠条文件与它的 OR 语义。
(替代方案「有挂起就不清」已排除:那会让用户在升级窗口里点 Turn Off 失效,
正是取舍四明确拒绝的。)

**② 漏了第三条压制路径:启动恢复。** 取舍二只列了两条停机路径,但
`restartGuardianForUpgrade` **每次升级都把 Guardian 停掉再拉起**,新 Guardian 起手就跑
`recoverLocked` —— `desired` 保持 `on` 之后,它会在二进制刚换完、CLI 还没走到
「恢复保护」时自己把 Core 起来。**修正**:`recoverLocked` 与 `handleUnexpectedExit`
同批改,且那条分支**必须置 `recoveryBlocked = false`** —— 否则挂起会让 `Manager.Down`
永久返回 `errRecoveryIncomplete`,用一条新不变量撞碎 2026-08-04 那条。

**③ 迁移垫片按字面实现会丢掉欠条真正携带的信息。** 「读到 legacy 欠条就当作一次
已武装的挂起」只恢复了「压制」这一半;欠条携带的是「**用户本来要保护**」,而此刻盘上的
`desired` 正是那次升级自己写下的 `off`。只武装挂起 = 压制 15 分钟然后什么都不恢复,
恰好丢掉这个文件存在的全部理由。**修正**:迁移做两件事 —— `desired` 复位 `on`
**加**武装挂起。

**④ 「过期后 `bx status` 会显示一条真实的分歧」在今天的 `Diverge` 里不存在。**
`diverge.go` 只有两个分支(`believed=protected` 三项必 True、`intent.Desired=="off"`
残留),没有任何一条会在「desired=on 而保护不在」时说话 —— 我把它当既有行为引用了。
**修正**:新增 `desired=on && 无挂起 && CoreSocket==False` 这条分支。没有它,
取舍五承诺的「挂起让机器看起来是坏的」根本不成立。
(另:我说 `intent.Desired == "off"` 那条会「冒出假分歧」的措辞也不准 —— 挂起期间
`desired` 保持 `on`,那条不触发;真正会触发的是**退回路径**(取舍三写 `desired=off`)
加升级窗口里的残留。)

**⑤ 「读不出意图」没有归宿。** 要求两者同源读,却没说读盘失败时调谐器怎么判。
按 `off` 收敛会去停一个用户要的 Core,按 `on` 收敛会在挂起期间起一个不该起的。
**修正**:第五道栅栏 `intent_unreadable`,与本仓库「Unknown ≠ False」同一条纪律。

**⑥ `Manager.Down` 在 DNS 还原失败时会把 Core 拉回来(`manager.go:697-699`),
这条路不看挂起 —— 计划作者留在范围外,我改判进来。** 那是既有的补偿逻辑
(「还原失败了,把 Core 放回去,别让用户停在一个奇怪的中间态」),而在维护窗口里
「把 Core 放回去」恰恰是最不该做的事:二进制正换到一半。它与 `handleUnexpectedExit`、
`recoverLocked` 是**同一类消费者**,漏掉它就是漏掉第四条自发起 Core 的路。
方向上也安全 —— 让它认挂起是**少做一个动作**,不可能让「停止」依赖别的先成功,
不与逃生路径不变量冲突。并进改停机语义的那个任务。

## 整分支复审之后的修正(2026-08-10,以本节为准)

**① 过渡升级(新 CLI × **旧** Guardian)会把用户的意图弄丢 —— 这是本设计删掉
欠条时漏掉的那一半。** 删欠条的依据是「升级停机不再改写 `desired`」,而那只在
**新** Guardian 服务那次停机时成立;第一次升级面对的一定是旧的那个,它的
`Manager.Down` **无条件** `SaveDesired(DesiredOff)`(`git show
d1abe81:internal/guardian/manager.go`;旧的 `?reason=upgrade` 只保住欠条,从不
抑制那次写入)。于是停机之后盘上是一句**自洽的**假话:`desired=off`、没有挂起、
没有欠条 —— `Diverge` 一个字都不说,而一次崩溃就让重跑读到 off:「恢复保护」从
计划里消失,升级还会报完成 = 永久无保护。

**处置(能力门)**:服务这次停机的 Guardian **没有**声明 `maintenance_hold`
(或问不出来)时,CLI 在停机之后、**装文件之前**把 `desired=on` 写回去。
选它而不是继续写一个版本的欠条,理由有三:欠条的读回逻辑正是被这一期删掉的
(它的 OR 语义带着那个活 bug);写回覆盖的是**整个**窗口(欠条方案只有在新
Guardian 起来做迁移之后才生效);而 `desired` 本来就该记着「用户要保护」——
这一步不是新增一种表示法,是把旧 Guardian 刚抹掉的那句话原样写回。
写不成不产生 error(这一次仍会在末尾起保护),只留一行点名 `sudo bx up`。

**② 升级一台本来就不要保护的机器,不再武装挂起。** `recordStopIntent` 曾无条件
武装,而 `upgradeSteps(running, desiredOn=false)` 里没有「恢复保护」这一步,销挂起
又只发生在用户显式的 up/down/migrate 上 —— 那张挂起没有任何东西会去清它。新增
`downPurposeUpgradeUnprotected`:照常走 `DownForUpgrade`,但既不武装挂起也不写
`desired=off`(盘上已经是 off,而 `desired` 只由用户改)。**已知取舍**:用户恰好
在这几秒里点 Turn On 的窄窗口不再有挂起拦着;另一头是替用户写一句他没说过的 off。

**③ `desired` 与挂起必须由 `/v1/status` 一起发布。** 调谐器、`handleUnexpectedExit`、
`recoverLocked` 都改成了 `LoadIntentSnapshot`,唯独 `statusOf` 没有 —— 实测能拿到
「内存说 on、磁盘说 off、挂起来自磁盘」的混源应答,正是取舍⑥禁止的组合。现由
`PublishedIntent()` 一次读盘同时给出两半;读盘失败保留内存里那份信念(磁盘的沉默
不是「用户不想要保护」),而 `/v1/status` 绝不因此失败。

**④ 挂起清不掉时 `Up`/`Migrate` 拒绝打开保护。** 「读得出来、却删不掉」此前只记
一行日志,`bx up` 照常报 Protected 而挂起还武装着 —— 此后 Core 一退出,
`handleUnexpectedExit` 走 `HoldArmed` 那一支**既不重启也不装屏障**,保护在用户
以为开着的时候退回明文直连。新码 `maintenance_hold_clear_failed`,CLI 侧有出路。
**方向**:这里拒绝的是**启动**;`Down` 的销挂起仍是 best-effort、只记日志。

**⑤ 反方向(旧 CLI × 新 Guardian)的措辞更正。** 原先说盘上「两句话都没有、今天
无害」——**不对**。旧 CLI 会写升级欠条,而新 Guardian 的启动恢复(升级本身必然
触发)会迁移它:武装一张 15 分钟的 `legacy_upgrade` 挂起 + `desired` 复位 on。
顺利跑完的升级不受影响;**死在末尾那次 `bx up` 之前**的升级,则把「开机自愈」
换成了「一次开机被压制」,15 分钟后自行结束。

**⑥ 崩溃后的 `desired` 语义变了,记录在案。** `forcedMacOSTeardown` 在
`armsHold()` 下不再写 `desired=off`,于是**装文件期间断电**留下的是 `desired=on`
(此前是 `off`)。方向是对的(用户确实想要保护),后果是那台机器下次开机会自己
把保护起回来 —— 而二进制可能只换了一半。实际风险很低:`installAppBundle` 用
stage+rename,常见情形下要么全换要么没换;`runtime` 目录按版本落盘,
`current` 符号链接同样是原子替换。**这一条没有专门的防护,只有记录。**

**⑦ 一张读不出来的挂起会让启动恢复永远重试(5 秒一轮),这是刻意的。**
`LoadMaintenanceHold` 对坏 schema / 坏 reason / 远期过期一律报错,`recoverLocked`
因此 fail-closed,而那个文件不会自己变好。安静地放弃等于让机器停在「用户要保护、
却没有保护」上不出声。**要防的只是被误读成崩溃循环**:崩溃循环里 PID 会变、启动
横幅重复出现;这一条是同一个进程复读同一个码(`intent_unreadable`),而那个码在
CLI 侧有自己的出路。理由写在 `retryDaemonRecovery` 的注释里。

## 风险

**最大的一条:改的是逃生路径。** `forcedMacOSTeardown` 与 `handleUnexpectedExit` 都受
「关闭与恢复路径不得依赖可能已失效的前置条件」这条不变量管辖(2026-08-04 那次事故
换来的)。取舍三的退回规则就是为它写的,必须有测试专门钉住:**挂起写失败时,拆除
照常做完,且退回写 `desired=off`。**

**第二条:升级路径上真有两个写入点,漏一个等于没修。** 强制拆除那条正是「事情已经
不对劲时才会跑」的路 —— 而那时最不能再多一个失效模式。

**第三条:一致性只在两处读盘之间成立。** 挂起与 `desired` 一次读出来,但 CLI 在
另一个进程里随时可能改写。今天靠整文件原子替换 + 最后写者赢;本期不引入锁,
但要写明「一次读到的是一个瞬间的快照,不是一段区间内的保证」。
