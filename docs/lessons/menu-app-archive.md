# 菜单栏 App —— 2026-09-23 从根目录 CLAUDE.md 下沉时的原文存档

**这是存档,不是现行判据。** 2026-09-23 根目录 CLAUDE.md 开始按代码目录下沉,这些段落的
**判据**浓缩进了 `apps/macos/BxMenu/CLAUDE.md`(那一份才是改代码之前要读的);这里逐字保留当时的原文,给想知道
「当初怎么换来的」的人看 —— 事故经过、真机数字、变异实测、被否掉的方案的完整理由。

**原文里有少数陈述在下沉那天已经不成立**,核过并在子目录那份里更正的有:Checks 页中英混排(`b4ee7586` 已修)、`RuleFinding.summary` 未被渲染(09-17 起直接渲染)、规则窗口不跟环境刷新(09-11 已接上)。
读到与 `apps/macos/BxMenu/CLAUDE.md` 冲突的地方,以后者为准。

---
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
**覆盖面已经长到七张**(rules 折叠/展开、servers、apptraffic、diagnostics 两页、
deploy)—— 原文写的「今天只覆盖 Routing Rules」早就不成立了,2026-09-18 更正。

**fixture 只许装生产真的会产出的东西,这一条是承重的**(`TestSnapshotFixturesOnlyContainThingsProductionCanProduce`)。
它的产物是一张图,而那张图会被当成「用户看到的样子」的证据 —— 我自己就照着它报过
一个不存在的缺陷。2026-09-18 一次复核当场抓到三处,每一处都会让人看着图得出错的
结论:① `servers.json` 里躺着**项目所有者真实的 VPS 地址**,而这是公开仓库,
而本仓库自己把服务器 IP 当敏感信息(Guardian 日志强制 0600 的理由原文就是「里面有
服务器 IP 与 bypass 网段」);② `rules.json` 的分组 name 写的是 `steam`,而生产里
那一组叫 `gaming`(它的 **Title** 才是 Steam)—— 快照因此渲染出「认不出的组」那条
回落副标题,而真实用户永远看不到那个画面;③ `doctor.json`/`logs.json` 里是几句
**编造的中文**,而那几个面这一轮已全改英文 —— 快照把一个**已经修好**的问题画得
还在。**一个会画出产品不存在状态的快照工具,比没有这个工具更坏:它看起来是证据。**
守卫三条判据(无 CJK / 分组 name 必须是真 preset / 只许文档保留网段与私网地址),
三条变异各咬一条。**它扫的是整个 `Snapshots/`,不只是 `fixtures/`** —— 第一版只扫
JSON,而快照驱动 `Snapshots/main.swift` 里还内联着一份同样的数据(探测结果里的
出口 IP),于是修完 fixture 再跑一次,真实地址**照样画在图上**。那是本仓库列过的
第一种失效写法(守卫钉住了缺陷旁边的东西),这次是在同一个改动里当场自己犯了
一遍 —— 下一张图把它显形。

**这件事随后做到了全仓(2026-09-18,选项②:换掉现有文件 + 加守卫,不改写历史)。**
`TestNoRealInfrastructureAddressesInTheRepo` 扫 `git ls-files` 的每一个文本文件
(只跳过 `internal/embedded/assets/` 那两份真实世界的 china 列表),任何公网 IPv4
要么落在文档保留网段/私网/组播/保留段里,要么必须在 `knownPublicAddresses` 里
**写明理由**。**判据是白名单而不是黑名单**:黑名单只拦得住已经知道的那几个,而下一个
人粘贴的是一个新的;白名单里加一条的动作本身,就是那句要被逼着回答的话——
「这是第三方服务,还是我们自己的机器?」另有反向断言禁止陈旧条目。
一次替换动了 **52 个文件 223 处**(自有 VPS、测试 VPS、家里 derper 的地址),
换成 `203.0.113.x`(bx 服务器)与 `192.0.2.x`(别的真实主机)。

**替换当场换错过一处,值得记**:`192.200.0.101–116` 看起来像"某台机器",实际是
**Tailscale controlplane 兜底段**,而且它在生产里是由 `{192, 200, 0, byte(i)}`
**算出来**的 —— 字面量只出现在测试里,于是纯文本替换把测试改了、生产没动,
`TestTailscaleControlplaneFallbackCIDRs` 当场转红。**"看起来像真实主机"与"是我们的
主机"是两件事**,而区分它们要去读产地;白名单里那两条现在写着它是什么。
ZeroTier root 与 Tailscale DERP 兜底同理 —— 它们**功能上**必须是真地址。

**地址那条只拦 IP,而真正不能有的是凭据** —— `TestNoUsableServerLinksInTheRepo`
(同日补):`vless://`/`trojan://`/`hysteria2://` 的 userinfo 位必须一眼看得出是编的,
**并且 `bx://` 要解一层再查里面那条**(`bx server share` 打出来的正是这个形状,而
**整行粘贴**才是最可能的事故,不是手打)。判据方向刻意是「看得出是假的」而不是
「看起来像真的」:一个谁也没见过的新 uuid 长什么样,这里无从知道;而编的有明显
特征(说人话的词、重复的十六进制组、短到不可能是凭据)。认不出就红,由人回来改成
合成值。首次扫描抓到两个随机形状的 uuid(都在测试里、配着占位主机)——**它们当初
是不是某台真机的,判断不出来,而「判断不出」正是这条守卫要消灭的状态**;已按仓库
既有的合成约定(`11111111-2222-3333-4444-555555555555`)换掉。
**它盖不住 `ss://`/`vmess://`/`brook://`** 那几种把凭据编进 base64 的形式,别读成
「凭据这件事已经全有人管了」。

**历史里没有真凭据**(2026-09-18 全量核过:所有 `vless://` 都是合成 uuid 加假 pbk),
所以那次泄漏只是"关联",不强制换服务器。

**风险在哪、为什么不改写历史**:不在"IP 被知道"(那台机器本来就在 443 上对外听,
REALITY 的设计就是让它看起来像普通 TLS 站)—— 在"这个公开仓库 ↔ 这台机器是翻墙
出口"这条**可被爬取的关联**。对一个翻墙工具,这条关联比地址本身值钱。改现有文件
拿不掉已经进历史的那些,守卫管的是**停止扩散**;改写历史要 force-push 且任何 fork
仍有旧副本,换来的确定性不高 —— 项目所有者据此选了②。

**此前记的原文(已由上面这段取代)**:全仓 10 个文件里有它(生产 Go 源码与 Swift 源码里的
注释、Swift 测试、两份 spec、一份 plan、CLAUDE.md 自己)。绝大多数是记述真机事故
时顺手贴的。**守卫只管 `Snapshots/`**(那是唯一会被渲染成"用户看到的样子"的地方);
其余是不是要清、要不要换一台服务器,是项目所有者的决定 —— 而**编辑现有文件并不
能把它从 git 历史里拿掉**。对一个翻墙工具,风险不在"IP 被知道"(它本来就在 443
上对外听),在"这个 GitHub 仓库 ↔ 这台机器是翻墙出口"这条可被爬取的关联。

**「这句话被截断了」也是确定性判定,和「控件越界」同一类,所以它属于 dump 不属于
PNG。** dump 现在给**不能换行**的标签打 `truncated needs=N`(会换行的字段超出边框
只说明它折了行,那是对的 —— 第一版没分这两者,日志页当场假阳性),`checkTree`
见到就红。它抓到的第一个缺陷:规则窗口默认 420pt,而三条预设副标题**全部**在句子
中间断掉(需要 ~347pt,实得 218~269pt)——**而那句话正是 2026-09-14 定下来回答
「开了会怎样」的东西**,也就是说那个设计在默认宽度下 100% 失效,且没有任何东西会
报错:AppKit 画一个省略号,截图看起来很正常。窗口因此加宽到 580;**选加宽而不是
折成两行**,因为「一组仍然一行」是同一次决定的另一半,而当初拒绝两行的理由(那行
多半是空的)已经随「恒非空」消失了。

**同一轮还按图改了两处别的**:① Servers 窗口里 `New Server…` 与 `Add Server…`
并排站着,名字近义而动作完全不同(前者 ssh 进一台空 VPS 装 bx server,后者只是把
已有链接加进清单),**点错前者的代价是对着一台陌生机器跑 ssh** —— 改成
`Set Up a New VPS…` / `Add Existing Server…`,而守卫从「钉那一个措辞」改成
「钉那条出口存在、两个标题必须一个说 new 一个说 existing」。② Traffic by App 的
窗口 720pt 而内容只用到 ~140,应用名与第一列数字之间空着约 260pt,眼睛要横跨一片
空白去对数字;收到 560。**同时给 fixture 加了一行「现实的最长情形」**(长应用名 +
33 字符的目的地)—— 没有它,按 fixture 选出来的宽度对真实数据不作数,而这份快照
存在的全部意义就是「用户会看到什么」。

**仍然答不了的**:手感、动画、VoiceOver、跨 macOS 版本的控件差异;以及
`NSStatusItem` 的那个菜单本身(它不是窗口,这条路够不着)。快照是静态的。

**CI 上它真的在跑(2026-09-17 第一轮实测)**:GitHub 的 `macos-latest` runner
有可用的 WindowServer,快照四件产物齐全、守卫在那上面执行。脚本仍保留「没有
WindowServer 就明说 SKIPPED 并退 0」那一支 —— 「跑不了」与「跑了没过」必须分开,
而那一支今天在 CI 上走不到,只对本地 headless 会话有意义。
**同一轮还栽了一次**:`macos-app` job 此前从不装 Go,而这条守卫是 Go 测试 ——
本机 verify 全绿(本机当然有 Go),推上去 `go: command not found`。
又一次「一边的绿不替另一边背书」。


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


## 模式不进菜单,但必须说在规则窗口里(2026-09-18)

所有者问「bx menu 中需要给用户 global 这种模式选择么」。**结论是不加**,而最强的
论据是他自己的配置:`mode: global` + **17 条亲手挑的 direct 规则**(腾讯 8、Apple 3、
家里 derper 两个 IP…)+ 1 条 proxy。模式选择器只提供两个点(「全都走隧道」/「信一份
一万两千条、你没写过的清单」),而他在的是第三个点——**global 加自己的例外**,
那正是 Routing Rules 窗口在编辑的东西。切到 split 会把 17 条刻意的选择换成一万两千条
别人的判断,这不是一个开关该做的决定。

**而且它根本不是开关**:`reloadRouter` 那行注释写死了「global 用启动值(改
mode/global 需重劫持,不在此列)」—— 切模式要重启 Core、断网几秒。一个会断网、
并且静默改变九成流量去向的菜单控件,正是本仓库否掉过的形状(「登录此网络」入口)。

**真缺口是反方向的:模式改变的是那份规则列表的含义,而 GUI 里没有任何地方说你在
哪个模式。** global 下那几条 direct 规则**就是**全部的直连集合(删一条,那部分流量
立刻改走隧道);split 下它们是叠在内建 china 列表之上的**例外**,大半可能本来就被
盖住。同一份列表,两种意思 —— 而这个混淆**真实造成过一次错判**:体检曾把 22 条正在
工作的规则报成「被 china 列表覆盖」,而那台机器是 global、那份列表整个不生效。

所以:**`/v1/rules` 新增 `global`(指针,nil = 这一版没说),规则窗口的
「Your own rules (N)」后面接一句它意味着什么;菜单栏一个字不加** —— 模式几乎从不
改变,放进菜单就是墙纸,而**只有在你盯着规则列表的时候它才改变你读到的东西的含义**。

**不从 `review.builtin_skip_reason` 那句话里认字**(它确实含「in global mode…」)——
按文本认的后果是措辞一改,界面悄悄退回一句通用的废话;`configIsGlobal` 单独读一遍
配置,并把「读不出来」与「不是 global」分开返回。问不出来时标题**只报条数**,
绝不猜 —— 猜错的那一半正好把话说反。

守卫两边:`RulesModelTests` 钉纯判据(三种在屏幕上两两不同、nil 时不许出现任何一种
断言),`TestMacMenuRulesWindowHeadingIsFedTheServersMode` 钉接线(到达的值,不是
「那个函数被调用过」)。两条变异各咬一边。**快照驱动的签名也跟着变了,而那是
`verify.sh` 当场报出来的** —— 加一个参数就会让它编不过,这条链是接着的。


**菜单**(`RulesModel.swift` 纯函数 + `main.swift` 只摆放,子菜单 + NSAlert,不做窗口):
失败的规则排最前、健康的一个字不说;失败按 **kind + 名字**对齐(同名规则可同时在
direct 与 proxy 里,语义相反);读不到就说读不到,不摆空列表;改完按应答里的
`requires_restart` 说「已生效」或「要重连」(2026-09-08 起,见下文「菜单侧三件
用户体验」),要重连时给「现在就重连」,**但绝不替他重连**。**测试当场抓到真 bug**:Swift 合成的 `Decodable`
**不用属性默认值**,而 Guardian 对空列表用 `omitempty` —— 「一条 proxy 规则都没有」这种
正常配置会让整个界面解码失败,改手写 `init(from:)`。

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


- **`Quit Menu`(只关界面、保护继续跑)已删**,它等于给用户一键做出「保护在跑但
  没有任何指示灯」的隐形状态。退出入口由 `rebuildMenu()` 顶层无条件加一次,
  `TestMacMenuQuitActionPresentInEveryState` 按**函数体花括号深度**钉住「无条件」
  —— 只数出现次数抓不到「挪回某个 case 里」。**已知缺口**:恢复浮层与进度浮层在
  按 state 建菜单之前就 `return`,走不到那个无条件退出项。

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
