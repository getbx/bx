# Servers 窗口与「Core 起不来」:整枝 review 的逐条(2026-09-12/13)

**这份文件是过程,不是判据。** 从 CLAUDE.md 搬出(2026-09-13)。两支的判据留在
CLAUDE.md 对应小节;**真机验收步骤在 `docs/acceptance-pending.md`**。这里是两轮
整枝 review 各自抓到了什么、怎么抓到的。

---

## Servers 窗口:整枝 review 的修复轮

**十一条,其中「守卫钉住的是缺陷旁边的东西」的第十二次长在漏斗自己身上**:
`presentServers` 收成一个漏斗之后,里面有 `show(` 与 `refreshIfVisible(` **两个**调用点,
而判据是 `strings.Contains(整个函数体, "canEdit: canEdit")` —— **一个调用点替另一个满足了
断言**。实测只把 `show(` 那一处写死成 `core: nil / switchingTo: false / canEdit: true`,
整套 `TestMacMenuServer*` 全绿;而 `show(` 正是用户点「Servers…」那条路,后果是对着只声明
`servers` 的旧 Guardian 画出 `⋯`、Remove… 落进兼容分支 —— 出口 IP 换到他想删的那一台,
菜单还报成功。判据因此下沉到**每一个实参表**(`swiftArgumentIsPlainly`),与同一轮在
`swiftValueReachesViewTree` 里学到的「作用域限定在最内层块」是同一条。

行为上改掉四件,每一件都是用户看得见的:
- **`runningServerName` 对同一主机上的两台不再自信地报第一条。** Core 只报得出主机
  (`RuntimeState` 里没有名字),而 `--with-hysteria2` 出的两条链接、凭据轮换那段过渡都会
  造出这个形状。此前:填实的 `●` 与「bx is actually using X」落在错的那台,实时峰值给错行,
  `recordThroughputOnce` **按错名字永久落盘**。**有歧义也是「说不出」**,返回空串。
- **`Test All` 对只有一台的清单不再什么都不发生**(这一支自己引入的回归):探测结论只长在
  候选行上,而 `otherServerRows` 按定义排除当前那台。现在当前那一块有自己的探测行,
  **复用同一个三态呈现** —— 灰的仍是灰的,红只从实测失败来。
- **两个改清单的动词在拨号之前自己再查一遍能力门**(`main.swift` 两处)。`NSMenu.popUp` 是
  嵌套事件循环,画出 `⋯` 到点下去之间窗口可能已被重画;`GuardianClient.swift` 上那两句
  「只有 serverEditingAvailable 判定支持时才该调用它」此前**没有任何东西**在执行。
  两层是刻意的:一道防线不该只有一层。
- **删除确认框说得出「这一台此刻正在承载你的流量」**(在跑、但配置里已不是 current 的那台
  Remove… 是亮着的,合规),判据仍只有一份,从 `row.isRunningNow` 带过去。

线上/CLI 三件:`servers_edit` 进 `acceptance.RequiredCapabilities`(菜单已真的依赖它 ——
**能力声明是唯一能证明进程真的换了的信号**);`bx server list` 的探测三态改读 `Measured`
而不是从 `Error != ""` 反推(spec §6.1 明令禁止,今天输出恰好对着只是因为产地总带一句
中文原因);**add 与 replace 都在写盘之前校验链接解不解得出主机** —— 此前 add 只在「名字
省略要推导」那一支上顺带校验,replace 一个字都不校验,而写进**当前**那台的一条解析不出的
链接会让下一次重连起不来,同时界面刚承诺「重连后生效」。
`Core not answering — nothing below was measured.` 改成 `— the live readings below are
missing.`:它下面还活着 UDP 那行(来自配置)与吞吐(来自落了盘的历史,**确实量到过**且
带真实年龄),原话被它自己下面那行当场证伪。

**顺带修掉两处生产**产不出**的 fixture**(两次都是「测试输入让待守属性不可见」):
`bx server list` 那三条 `ProbeReport` 省了 `Measured`(`Reachable:true` 而 `Measured:false`),
它们全靠那条反推才绿;两条 add fixture 用的是 `brook://host:9999?password=y` —— 一条
`tunnel.ServerHost` **解不出主机**的链接,能绿正是因为 add 那个校验缺口,而同一个文件
二十行之下就写着正确的形状。

**真机未验:整套,含此前搬进来的那两个按钮**(spec §10)—— 实时延迟是否真的每 2 秒跟着
`/v1/status` 动 · 保护关着时点 `Test All` 每行应是灰色英文 `not measured` 而不是红 ·
真切一次(会改出口 IP):要有可见反馈、四种结局的措辞对得上实际发生的事 · 删一台非当前
的(确认框说清链接会丢)与删当前那台应被拒 · 单服务器配置下四个按钮都在、文案说的是
「这是单服务器配置」· 加一台带 UDP 链接的,`bx status` 应显示 `UDP→hysteria2@…`。
设计 `docs/superpowers/specs/2026-09-12-servers-window-design.md`、计划
`docs/superpowers/plans/2026-09-12-servers-window.md`。

## Core 起不来:判别拨号绑物理网卡,而那张表在真机上可能是空的

判别那次拨号走 `plat.DirectDialer()`(darwin 上是 `IP_BOUND_IF`,防的是绕回隧道成环),
而 **`IP_BOUND_IF` 只查 scoped 路由表** —— 那条 scoped 默认路由由 `Hijack` 装,而
`Hijack` 在 `Run` 里排在这次判别拨号(`awaitTunnelHealthOrDiagnose`)**五百多行之后**
(行号别抄:见上文「两条结构性事实」那条,写死的数字已经漂过一次)。2026-08-13 那种机器状态
(单一活跃网络服务,macOS 根本不建 per-interface default)下每一次判别都在本机
`ENETUNREACH`,于是「VPS 真的挂了」与「VPS 活着而握手失败」**一起塌进 local_dial**,
而那句话此前连 host:port 都没有,还先派用户去查 bx 自己的路由 —— **spec §8 的真机验收
因此验不出它要验的东西**,跑验收的人有充分理由判定这支修复是坏的。(今天所有者机器上
那条 scoped 路由在,所以它是潜伏的,不是活着的。)

修法两半:① **local_dial 那句话也点名 host:port**(bx 明明知道那个地址),守卫做成
**穷举整族**(族由 `supervisor.StartFailureCodes()` 派生,新加一档自动进范围):
`TestEveryTunnelOutcomeNamesTheServerWhenBxKnowsIt` 与 Swift 侧同款。
② **绑网卡那次在本机失败后,不绑再试一次**(`diagnosisDialWithUnboundRetry`)。

**为什么不绑在这个位置是安全的、而且更忠实**:此刻 `plat.OpenTUN` 还没被调到、路由也还
没劫持,普通 socket 走的就是主路由表 —— 而**隧道子进程刚才那 20 秒走的正是同一张表**
(sing-box 既没有 `SO_MARK` 也没有 `IP_BOUND_IF`,server bypass 那条 /32 也要等 Hijack
才装)。也就是说不绑的那一次拨号复现的才是隧道自己那条路径。**那为什么还把绑的那次
留作主路径**:它防的是上一个崩掉的实例留在内核里的陈旧 TUN 与劫持路由。所以**只在
「SYN 没离开本机」这一族失败上**才退到不绑;拒绝 / 超时 / 域名解析不了都是**观测到的
答案**,不许被第二次拨号覆盖(`TestAnAnswerFromTheServerIsNeverSecondGuessedByARetry`)。
两次都在本机失败 ⇒ 照旧 `local_dial`,一个字不变。「本机自己没发出去」的判据下沉成
`failedBeforeTheSYNLeft`,分档与重试**共用一份**。

## Core 起不来:跨进程那条线,两头的测试从不相遇

**生产的写方与生产的读方此前从不在同一个测试里碰面** —— 写那半只在 `internal/cli` 里
对着临时目录测,读那半只在 `internal/guardian` 里对着手工拼出来的 `Record` 测。于是
**三条各一行的改动都能让这个功能整个退回改动前,而三个包全绿**:构造器里那句
`StartFailurePath` 被删、`coreArgs` 收到 `""`、写记录时 `At` 取零值。

**结构性帮凶两条,都已拆掉**:① `startFailurePath()` 是**裸转发**,而兄弟 `statePath()`
有默认兜底 —— 同一行删除对状态文件 **fail-safe**、对这份记录 **fail-silent**;现在它
也落回 `corestartfailure.DefaultPath`(`TestTheStartFailureRecordAlwaysHasAPlaceToLive`),
代价是 `StartFailurePath = ""` 不再是「关掉」的开关,而空路径既然不可能了,两处
`if path == ""` 一并删掉 —— 一段永远不会被执行的分支看起来像还有一道防线。
② `spawnRecordSpy` 拿到了 `args` 却什么都不断言(**守卫就摆在缺陷旁边**),现在它断言
argv 里那个值**就是读的人稍后要去看的那个位置**。往返本身由
`TestTheCoreWritesExactlyWhatTheGuardianReads` 钉住:写下去的字节,读回来必须还是同一
个码 —— 判据刻意不是「调用发生过」。**这是本支第四次「第七种写法」,第二次它会让功能
静默死掉。**
