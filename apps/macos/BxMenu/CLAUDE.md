# CLAUDE.md — BxMenu(macOS 菜单栏 App)

本文件只在读到 `apps/macos/BxMenu/` 下的文件时加载。跨领域的不变量(kill-switch、
防环、Guardian 授权面、守卫的七种失效写法)在根目录 `CLAUDE.md`,**这里不重复**。
2026-09-23 从根目录那份下沉过来;搬的是**判据**,过程叙述在 `docs/lessons/`,
下沉前的原文逐字存档在 `docs/lessons/menu-app-archive.md`。

**菜单那半的测试分三处,改之前知道去哪看**:纯模型在 `Tests/`(Swift 套件);
接线(`main.swift` 编不进 Swift 测试 target)由 `internal/cli/macos_menu_*_test.go`
读源码守着;布局由离屏快照守着(见下文)。**「真机未验」的逐条验收步骤在
`docs/acceptance-pending.md`**,agent 不去跑、也不替它下结论。

## 全菜单通用的几条纪律

- **沉默读作「查过了,没事」。** 这是整个菜单的词汇表,于是「没问出来」绝不许被
  渲染成沉默:`compactMenuRows` 只藏 `.ok` 的诊断行(不是「非 `.bad`」);Core 不应答
  时规则窗口顶上要说出来;体检缺席时也要说出来。**`nil` / 键缺席 = 「这一版没说」**,
  不是「没有」也不是「健康」。
- **不显示服务端没说过的话,也不冒充看懂了。** 服务端发码的地方客户端映射英文,
  认不出的码/类原样带上(`verdictText`),消费方**必须留一个「说不出是哪种」的分支**。
  服务端的错误串菜单**根本不解码**(Servers 窗口的 `error` 键),「不显示中文 / 不外泄
  原始错误」从靠纪律变成按构造做不到。
- **解码不许 `try?`**(`GuardianStatus.swift` 里任何 `init(from:)`,
  `TestMacMenuStatusDecoderNeverSwallowsADecodeError` 类级钉住):缺席 ⇒ nil/默认值,
  **在场而读不动 ⇒ 整份响亮失败**。`try?` 额外买到的只有「在场而读不动也当空」,
  而空在下游读作「一条规则都没在失败」。
- **Swift 合成的 `Decodable` 不用属性默认值**,而 Guardian 对空列表常用 `omitempty` ——
  「一条 proxy 规则都没有」这种正常配置曾让整个规则界面解码失败。凡是服务端可能省略的
  键,手写 `init(from:)` 用 `decodeIfPresent … ?? 默认值`。
- **同一个问题只许一份判据**。Core 是否答话一律走 `answeringCore()`
  (`MenuRows.swift`,`reachable == true`),不许再从 `status.core?.xxx` 直接摸。
- **能力门控,绝不「试着拨一下看看」**。每个动词各有能力门(`rules`、`servers`、
  `servers_edit`、`logs`、`doctor`、`status_watch`);旧 Guardian 会把不认识的动词
  落进兼容分支做出**别的事**(`remove` 会被旧 Guardian 当成切换)。`NSMenu.popUp` 是
  嵌套事件循环,**动词在拨号之前自己再查一遍门**,不信画菜单那一刻的判断。
- **显式动作永不被环境刷新的在飞标志拦**(`shouldSuppressFetch`,`StatusWatch.swift`):
  重叠取数的代价是一次多余的本机 socket 往返,拦住显式动作的代价是「点了没反应」。
  只压 `explicit == false`。
- **菜单里所有定时器必须挂 `.common` 模式**:默认模式的 `Timer` 在 NSMenu 追踪期间
  实测触发 0 次,而菜单开着正是用户在看的时候。类级守卫禁止 `main.swift` 出现裸
  `Timer.scheduledTimer`。
- **shell-out 只许落在白名单那几个函数里**(`TestMacMenuShellOutsStayOnTheAllowlist`);
  加一个可以,悄悄加不行。

## 菜单本身(9 行 2 条分隔线,2026-09-08,真机未验)

- **第一行是保护开关**(`ProtectionSwitch.swift` 纯判据 + `ProtectionSwitchRow.swift`
  视图)。只在有东西可拨的三态显示(connected/warning 开、off 关);没装/没 setup/缺文件
  保留各自的文字动作与一行粗体状态词(`addHeadline`)。**位置永远来自状态,不来自点击**
  (失败就弹回);进行中停在目标位置并禁用。拨动接回原来的 `startBx`/`turnOffBx`,
  确认、免密、逃生口一个字不动。开关行的 `signature` **必须进 `menuSignature`**,否则
  拨完菜单不会重画。代价:自定义视图的菜单行没有键盘高亮导航。
  守卫 `internal/cli/macos_menu_switch_test.go`。
- **开关下面那行服务器名优先**(`vps · 293 ms`),协议留给 `bx status`/doctor;
  `.warning` 的原因写在这行、标红。
- **诊断行只在 ✗ 时露面**(`compactMenuRows`)——判据是 `== .ok` 才藏。可能结构性缺席
  的字段由 `menuRows` **整行不发**;走到压缩层还带 `.unknown` 的是真的问过而没问出来。
  将来某一行在真机上恒为未知,修它的**构造处**,不是回来把 unknown 一起藏掉。
  认不出的新行原样保留(默认参与显示,吵的失效好过安静的)。
- **子菜单能用的前提是就地提交**:`rebuildMenu` 攒草稿,`defer { commitMenu(menu) }`
  比渲染签名(`menuSignature`,递归到子菜单)变了才换 item。`removeAllItems()` 全文件
  **只许在 commitMenu 里出现一次**,菜单对象始终是同一个(`TestMacMenuRebuildsMenuInPlace`)
  ——别处清空会绕过签名比对,展开的子菜单每 2 秒被拆掉(本仓库两次因此选窗口不选子菜单)。
- **常驻版本号删了**:更新入口只剩 `addUpdateActionIfAvailable` 一处;平时装的哪版在
  Troubleshoot ▸ 里(`installedVersionForMenu`,只取状态里已带的版本、不读盘)。
- **Quit 不带图标、带 ⌘Q**(`TestMacMenuQuitHasNoIconAndUsesCommandQ`):电源符号紧挨
  保护开关会被读成「关掉保护」。**退出入口由 `rebuildMenu()` 顶层无条件加一次**
  (`TestMacMenuQuitActionPresentInEveryState` 按花括号深度钉「无条件」)。
  **`Quit Menu`(只关界面、保护继续跑)已删**:它一键做出「保护在跑但没有指示灯」的
  隐形状态。**已知缺口**:恢复浮层与进度浮层在按 state 建菜单之前就 `return`,走不到
  那个退出项。**全部路径都失败时 Quit 不退出**(退出会藏掉唯一的指示灯)。
- **「Replace Configuration…」没有搬进 Servers 窗口**,窗口里取代它的是
  「Add Existing Server…」(`TestMacMenuServersWindowOffersAddServerNotReplace`);
  它只在没有服务器窗口的旧 Guardian 上留在一级菜单(`replaceConfigurationLivesInMenu`)。
- **turnOff 的 socket 失败回落到提权 `bx down`,turnOn 不回落**:没有「强制打开」,
  把死 socket 升级成弹密码是骚扰。
- **模式(global/split)不进菜单**——被项目所有者否掉过,别再提。它不是开关(切模式要
  重劫持、断网几秒,并静默改变九成流量去向),且模式选择器只给两个点,而真实配置在第三个
  点(global + 自己的例外,正是规则窗口在编辑的东西)。**模式只在规则窗口里说**,见下文。

## 图标(轮廓编码状态)

**状态编码在轮廓,不在颜色**(实心/空心/虚线/沿中线裂开),判据是**去掉动效后四形态
仍两两可分**(开「减弱动态效果」时只靠呼吸周期区分的两态会同形)。无色两态走 template
让系统上色,且必须用**不透明黑**绘制:template 只取 alpha,用 `secondaryLabelColor` 画出
的蒙版峰值只有 0.498,深色菜单栏上「保护没开」那条描边会消失。数据行三态里**只有
`bad` 计入 `anomalyCount`**(它驱动图标裂不裂);「未观测」译 `Not checked` 不译
`Unknown`。**不加常驻红字**(「Direct rules: N unreachable」被所有者否掉:常态会变墙纸)。

## 状态转换通知(`TransitionNotice.swift`,2026-09-08,真机未验)

只在「受保护 → 阻断 / 隧道断 / 需注意**且持续 ≥ 30 秒**」那一刻响一次,回到受保护再响
「已恢复」(同一个 identifier 顶掉前一条)。**不响的每一种都有测试钉住**:30 秒内抖动、
用户自己开关、从 off 打开后直接失败、菜单启动时就已是坏的、starting/recovering 过渡态
(blocked → recovering → blocked 是同一段故障)。
- **`protectionSignal` 对「Guardian 没说」归 `.transient`**,不是 `.protectedHealthy`
  (会凭一个没人发过的字段弹「已恢复」)也不是 `.attention`(拿缺失的键断言机器坏了)。
  `report.core?.tunnelHealthy` 把 core 整个缺席(升级窗口的常态)与键缺席摊平成同一个
  nil,两者都是「没说」。图标那半照旧显示 attention,两者不矛盾。
- 投递经 `UNUserNotificationCenter`,**只在 bundle 内启用**(裸 `swift run` 一调就崩)。
- 单一漏斗:watch 与轮询都经 `refresh()` → `applyRefresh` → `observeTransition(…)`。
- 守卫 `internal/cli/macos_menu_transition_test.go` 与 `TransitionNoticeTests`
  (含「真的健康时『已恢复』仍要发」这一条反向断言,少了它「永远不响」就能满足前一条)。

## Status watch 在菜单这一侧(协议判据在 `internal/guardian/CLAUDE.md`「状态 watch」)

- **兜底轮询是常量,watch 健康时也照跑**(`menuWatchBackstopSeconds = 60`):watch 有一类
  失效是静默的(半开、循环自己死掉),被 watch 自己的健康判断影响的兜底,在那个判断错的
  时候恰好也是坏的。它与 `menuPollClosedSeconds`(旧 Guardian 不支持 watch 时的降级轮询
  间隔)是两件事,必须保持 `backstop > closed`(`MenuCadenceTests` 钉大小关系)。
- **1 秒 floor**(`menuWatchIdleDelaySeconds`)兜「秒回但代际号没推进」,最坏情形从满速
  空转降成 1Hz。
- **刻意没修的残留**:Guardian 声明了 `status_watch` 却不带 `status_generation` 时,菜单会
  按轮询节拍反复进出 watch 循环,没有冷却。**真的观察到这个现象,那不是菜单的 bug,是
  某一端在能力声明上撒谎。**
- **刷新只拉 `/v1/status`**;`/v1/rules`/`/v1/servers` 各要读并解析一遍 config,改为按需
  (`fetchRulesOnDemand`/`fetchServersOnDemand`)。**但窗口开着时环境刷新要跟着重拉**
  (按 `isVisible` 触发、`forceShow: false` 用 `refreshIfVisible` 就地重画、不抢焦点不弹
  alert)——Servers 与 Rules 两扇都是;两扇窗口都没有自己的定时器,删掉这条就会冻在打开
  那一刻(2026-08-17 Servers 窗口栽过一次,Rules 窗口 2026-09-11 补上)。
  `serversFetchInFlight`/`rulesFetchInFlight` 是必需的,不是讲究。

## Routing Rules 窗口(规则编辑器,2026-09-11,真机未验)

风险门的判据(`policy.DirectRuleHazard`,覆盖面而非写法)在根目录 CLAUDE.md;这里是窗口。
- 一行一条(direct+proxy),**有问题的排最前、健康的不说话**。摆表**只有一个出口**
  `presentRules(_:forceShow:)`;局部绑定刻意叫 `answering` 不叫 `core`(后者会拼出与
  bug 原形逐字相同的 `core?.failingRules`,守卫再也分不开)。
- **删除不弹确认但留 Undo**(为 11 条冗余点 11 次确认框是在惩罚正确的行为)。与
  Servers 窗口刻意相反。
- **表顶那句话报两个半边**(`ruleWindowCaveatNote(_:coreAnswering:)`):体检缺席
  (`review == nil`)与 Core 不应答(空的 `failing_rules` 在那时是「没问出来」,Go 侧
  `Reachable=false` 时其余字段按构造全是零值)。一句不是两条横幅;健康机器上恒 `nil`。
  `coreAnswering:` 不许是字面量(`TestMacMenuRulesWindowNeverReadsFailingRulesFromAnUnansweredCore`,
  `TestMacMenuRulesWindowAnnouncesAnAbsentReview`)。
  **已知**:它把「配置解析失败」也说成「这一版 bx 没检查」(两条早退都返回 nil);
  要紧的那一半对——它绝不宣称健康。
- **`ruleRowSeverity` 刻意不吃 `coreAnswering`**:Core 不应答时失败那半对每一行都缺席,
  排序退化成原顺序,什么也没断言;再塞参数就是把 `reachable` 抄第二份。
- **一行既被分类又在成片失败时两半都要报**:`DomainSet.MatchRule` 逐级往父域找,
  累积失败的恰恰是被盖住的那条更窄的规则。
- **体检文案直接渲染服务端的英文 `summary`**(2026-09-17 起,一处产地),没发 `summary`
  时 `verdictText` 原样带 class。菜单的 class 字面量与 `rulereview.Class.String()` 双向
  钉住(`TestMacMenuRuleClassLiteralsMatchTheGoClassNames`)——改了名,去匿名化那一行会
  被画成橙色建议而两个套件全绿。
- **组副标题恒非空**(`ruleGroupSubtitle`,认不出回落「N domains」)且与勾选框**同一行**;
  **绝不回显服务端的 preset `summary`**(那份至今是中文,同时喂着 `bx preset show`),按
  组名映射英文,Go 预设清单与 Swift 表双向钉住(`TestMacMenuEveryPresetHasAnEnglishSubtitle`)
  ——回落与「想过了、没什么可说」在屏幕上完全一样。窗口因此默认 580pt 宽(420 时三条
  副标题全被截断,快照抓到的)。
- **标题说出当前模式**(`/v1/rules` 的 `global`,指针):global 下 direct 规则**就是**全部
  直连集合,split 下是叠在 china 列表上的例外——同一份列表两种意思,体检曾因此错判 22 条。
  **不从 `builtin_skip_reason` 认字**;问不出来时标题只报条数,绝不猜
  (`TestMacMenuRulesWindowHeadingIsFedTheServersMode`)。
- **右键加规则**(按应用窗口):粒度是**目的地不是应用**;候选 `ruleCandidates(for:)`
  (三段以上给「精确 + `*.父域`」,两段给 `*.host`,IP 原样)。**风险过滤只作用于 direct、
  放在 `appTrafficRuleMenu` 里**——滤在 `ruleCandidates` 会连安全的 proxy 候选一起丢。
  Swift 的开放平台清单与 Go 逐字相同、两种写法各问一遍(`TestOpenPlatformListMatchesPolicy`)。
  只在 `rules` 能力在时挂菜单并在窗口底部提示(右键发现不了)。
- **收尾用服务端的答案**:`ruleChangeFollowUp` 按应答里的 `requires_restart` 说「已生效」
  或「要重连」(nil = 旧 Guardian 没说 = 按要重连);要重连时给按钮,**绝不替他重连**。
- **已知并接受**:`Add Rule…` 可重入(409 时 modal 会叠,不丢字);Undo(`addRuleBack`)
  没有在飞守卫,**无害的承重理由是 `setup.AddRule` 幂等**——那个幂等没了就要补守卫;
  窗口开着时每次环境刷新都会让 Guardian 重建一次约 12k 条的 `DomainSet`,没量过。

## Servers 窗口(2026-09-12,真机未验)

Guardian 侧的线上字段(`single_server`、`measured`、`running`、`current_server`、
切换四种结局码、`servers_edit`)在根目录 CLAUDE.md;这里是窗口。
- **空列表不是死路**:`bx setup` 从不写 `servers:`,「单服务器配置」与「清单真的空」由
  `serverListEmptyReason` 分开,按钮带照画(`TestMacMenuServersWindowKeepsTheButtonsWhenTheListIsEmpty`)。
- **红只从实测失败来**(`TestMacMenuServerRowRedComesOnlyFromAMeasuredFailure`),
  `measured` 缺席 = 这版没说。
- **● 跟着 Core 报的 `running` 走**;`runningServerName` 对同一主机上的两台**有歧义就说
  「说不出」**(此前自信地报第一条,吞吐峰值按错名字永久落盘)。
- **删除弹确认、没有 Undo**(链接是凭据,菜单手里从来没有它,删了加不回来);删当前那台
  一律拒绝(置灰 + 服务端 409);确认框说得出「这一台此刻正在承载你的流量」。
- **两个按钮是 `Set Up a New VPS…` / `Add Existing Server…`**:前者会 ssh 进一台机器,
  点错的代价不对称;守卫钉「一个说 new、一个说 existing」而不是钉措辞。
- **每一个实参表都要单独钉**(`swiftArgumentIsPlainly`):`presentServers` 里 `show(` 与
  `refreshIfVisible(` 两个调用点,整函数体 `Contains` 会让一个替另一个满足断言。
- **所有者定死的边界**:不自动容灾、只有用户能切;不按延迟排序/不自动选最快/不分组/
  不导入订阅;**不后台定时探测**(只在用户点时发、且串行);不做每台独立的 rules/dns/udp。
- **已知缺口**:`replace` 清不掉 UDP 链接(界面已明说「留空 = 保持」);`bx setup` 写出的
  那种配置仍看不到「当前那台」那一块(`currentServerPanel` 要清单里有条目;`current_server`
  只喂了 Core 起不来那句话,没接进窗口)。

## 按应用窗口(Traffic by App,真机未验)

采集那半(订阅、TTL、活连接表、60 秒窗口、归因时机)的判据在 `internal/supervisor/CLAUDE.md`。
- **心跳 5 秒,且必须活不过它的窗口**:兜底轮询 60 秒比订阅 TTL(30 秒)还长,光靠环境刷新
  窗口会反复跳回「Not collecting」。首拉失败时窗口从未创建 ⇒ `windowWillClose` 永不触发 ⇒
  心跳永不停止(每 5 秒一次失败拨号 + 一条 Guardian 日志),而那恰是最常见的探索场景
  (保护关着时点一下这个菜单项)。
- **改这块最容易静默出错的三处**:① `appTrafficNumericColumns` 的下标(列从八降到七之后每个
  下标都要挪,挪错没有编译错误,只是右对齐落在错的列上);② **速率的 `nil` 与 0 是两件事**
  (第一次采样只立基线 = 不知道;压成 0 会显示成「闲着」);③ **搜索框必须在 `ensureWindow()`
  里创建一次**,长在每 5 秒被拆掉重填的树里,用户打两个字就连同焦点一起消失。
- 三组标题是 `Through the tunnel` / `Direct` / `Blocked`(`AppTrafficReport.sectionTitle`);
  同一个应用可以同时出现在多组(压成一行「混合」等于扔掉最有用的那一半)。不进菜单栏常驻。
- **真机要看**:七列在默认宽度下的分配、图标取不到时那一格、搜索时三个分组标题还在不在、
  目的地小字的 `+N`;**速率第一拍必然是破折号,若一直是破折号说明 `refreshIfVisible` 没走到**。

## Diagnostics 窗口(Logs / Checks)

- Checks 页只由显式点击喂数据(`TestMacMenuDoctorPageIsFedByFetchDoctor`);合计句是
  `N failed · M warnings · K not checked`,**K=0 也照写**(未检查是独立状态)。
- 两页渲染完都 `scrollToTop`(先 `layoutSubtreeIfNeeded` 再滚 `.zero`),Checks 页带秒级
  时间戳——否则第一眼看到的是末尾几行 OK,Run again 看起来没反应
  (`TestMacMenuDiagnosticsPagesReturnToTheTopAfterRendering`)。
- 失败弹窗的「Show Details」与那句文案**共用 `logs` 能力门**(`guardianFetchFailureInfo`
  吃 `logsAvailable:`):旧 Guardian 上按钮画不出来,文案就不许许诺它。
- **Checks 页的 `probe` 行比 `bx doctor --skip-probe` 多一行是预期**,不是漂移。

## 离屏快照:菜单窗口的闸门(2026-09-17)

`scripts/snapshot-macos-menu.sh` 走**真实路径**(真实 wire JSON → 真实解码 → 真实窗口
控制器 → 离屏 PNG + 视图树 dump)。`NSView.cacheDisplay` 不需要上屏,`.prohibited`
激活策略下不抢焦点,**可以在人正常工作时跑**。CI 的 `macos-latest` 上真的在跑;没有
WindowServer 时明说 SKIPPED 退 0(「跑不了」≠「跑了没过」)。
- **PNG 给人看,dump 给守卫看**。像素比对不当闸门(换系统版本字体一变就全红)。
  「控件越界」与「不能换行的标签被截断(`truncated needs=N`)」是确定性判定,属于 dump。
  会换行的字段超出边框只是折了行,不算。
- **量 alignment rect 不量 frame**(`NSTextField` 的 frame 每边大 2pt);内边距由 dump
  报出来,判「行活在内边距里」,守卫不写魔法数字。
- **布局组装只有一份**:`MenuLayout.swift`(`makeScrollingStack`/`pinToEdges`/
  `addFullWidthRow`)。竖直 stack 的 `.leading` 对齐让行按固有宽度布局、压缩永不触发,
  AppKit 就把控件画到窗口外而不报错。两条守卫都要:
  `TestMacMenuWindowsKeepEveryControlInsideTheContentWidth`(结果,有 fixture 的窗口)与
  `TestMacMenuWindowsUseTheSharedFullWidthRowPrimitive`(成因,每一扇 `*Window.swift`)。
- **fixture 只许装生产真的会产出的东西**(`TestSnapshotFixturesOnlyContainThingsProductionCanProduce`,
  扫整个 `Snapshots/` 含驱动里的内联数据):无 CJK、分组 name 必须是真 preset、只许文档
  保留网段与私网地址。**一个会画出产品不存在状态的快照工具比没有更坏:它看起来是证据。**
  fixture 里要有一行「现实的最长情形」,否则按它选的宽度对真实数据不作数。
- 加一扇窗口 = 一份 fixture + `Snapshots/main.swift` 里十来行。今天七张。
- **答不了的**:手感、动画、VoiceOver、跨 macOS 版本的控件差异,以及 `NSStatusItem`
  那个菜单本身(不是窗口,够不着)。

## 真机未验(索引;逐条步骤在 `docs/acceptance-pending.md`)

通知是否弹出与授权框;右键菜单在 NSGridView 格子上能否弹出;开关行对齐、`.small`
NSSwitch 观感、拨动后菜单是否留着并显示进度、失败是否弹回;子菜单展开时 2 秒一拍是否
真的不再拆它;规则窗口 `Add Rule…` 三种结局、`Remove`+`Undo`、表顶那句话的换行;
Servers 窗口整套;Show Details / Logs 页渲染。
