# Routing Rules 窗口重做:从预设开关变成规则编辑器

日期:2026-09-11
状态:设计已定,待实施计划

## 1. 起点

所有者的原话:「routing rules 感觉页面没有做好,traffic by app 还行吧」。

差距不是打磨不够,是**问题问错了**。这个窗口今天只做三件事:三个预设组的勾选框、
用户手写规则的**只读灰字**、一个 Show Config 按钮。它回答的是「哪几个预设包开着」,
那是一年问一次的问题;而用户真正的问题是「我的规则里哪条有毛病、帮我改掉」。

2026-09-10 真机验收当场兑现了这个差距:所有者的 29 条 direct 规则里有 11 条冗余
(全被他自己的 `*.apple.com` 盖住),而

- 窗口不显示这件事 —— `/v1/rules` **已经把体检 `Review` 发下来了**,而
  `RulesWindow.show(rows:custom:configPath:)` 连这个参数都不收;
- 窗口不给删 —— 那 11 条最后是绕过界面、直接打 Guardian 的 unix socket 删掉的;
- 窗口不给加 —— GUI 里唯一的加法是在**另一个窗口**右键,而 CLAUDE.md 自己写着
  「右键是发现不了的」。

还有两处沉默的缺口:

- **proxy 规则在 GUI 里完全不存在。** `Custom` 是 `preset.Classify(rules.Direct)`
  算出来的,只含 direct。
- **按规则的失败计数只给预设组,不给手写规则。** 而「`*.qq.com` 57% 失败」正是
  这整个功能的起因。

## 2. 定位

这个窗口回答两个问题,不多也不少:

1. 我的规则现在是什么样?
2. 哪一条有毛病 —— 帮我改掉。

第二问是新的,也是重做的理由。bx 已经知道答案(体检在 `/v1/rules` 里,失败归因在
`/v1/status` 里),只是没人把它摆到用户面前。

**权限边界在本期被明确推翻。** 原设计刻意拒绝删除,代码注释写着「菜单没有资格替
用户删他自己写的东西」。这条克制在今天不成立:菜单**已经能加**规则(按应用窗口
右键),而加一条错的 direct 规则会让流量离开隧道,比删一条危险得多。能加不能删这个
不对称本身就站不住。

## 3. 布局

自上而下三段,窗口仍是窗口(不做子菜单)。

**预设组**(基本不变):三个勾选框,on / 半装(mixed) / off,组里有规则成片失败时
行尾红字。**域名不展开** —— 沿用既有判断:「十行域名对普通用户没有意义,而『Steam
相关的走不通』有」。所有者已在设计问答中确认折叠。

**你的规则**(新):一张表,只列**不属于任何预设**的规则,direct 与 proxy 都在。

- 列:问题标 · 模式(等宽)· 类型(direct/proxy)· 说明 · 删除按钮。
- **排序沿用 Checks 页那条纪律**:有问题的在前,顺序为 危险 → 从没生效
  (被另一张表更宽的一条压住)→ 被同表更宽的一条盖住 → 被内建 china 列表覆盖 →
  成片失败;其余按服务端原序。
- **健康的行说明列空着,一个字不写。** 只在真有问题时才占地方 —— 与 Checks 页、
  与「否掉常驻红字」同一条。

**底部**:`Add Rule…`、`Show Config`,以及一行按应答里的 `requires_restart` 说
「已生效」还是「要重连」(nil = 旧 Guardian 没说 = 按要重连,既有判据不动)。

### proxy 规则从哪来:不用改端点

`preset.Classify` 只分类 `rules.Direct`,预设也只定义 direct 域名,所以**所有 proxy
规则按定义都是自定义的**。菜单把 `rulesResponse.Custom`(direct)与
`rulesResponse.Proxy`(全部)合成一张表即可,`/v1/rules` 的读路径一个字节不动。

## 4. 判据全在纯模型里

**这张表的纯模型其实已经写好了,只是从没接上窗口。** `RulesModel.swift` 里的
`RuleRow` 与 `ruleRows(from:failing:)` 已经在做:合并 direct + proxy、挂上失败归因、
失败的排最前、健康的行 `detail` 返回 nil(一切正常时不说话)。**它今天只有测试在调**
—— 又一处「被绿色测试守着的死代码」,而这一处正是本期要的东西。

所以本期是**扩它、接上它**,不是另造一个:

```swift
// 既有,保持:kind / pattern / failure / detail
struct RuleRow: Equatable { … let verdict: RuleVerdict? }
// 扩签名:多吃一份体检,并只留不属于任何预设的规则
func ruleRows(from list: RuleList, failing: [FailingRule], review: RuleReview?) -> [RuleRow]
```

窗口只摆放。与 Checks 页的 `sortedDoctorChecks` / `doctorSummaryLine` 同一个形状,
理由也同一条:**窗口自己算一次分类,就没有任何测试盯着它了** —— 这个仓库为
「判据落进 AppKit 那半」栽过。

`review` 为 nil 与**空** review 必须分开:前者是「这一版 Guardian 不做体检」,后者是
「查过了,你的规则都健康」。压成同一个东西是这个功能最贵的教训,`/v1/rules` 的
`Review` 字段注释里已经写明,菜单侧不许在这里丢掉这个区别。

## 5. 删除与添加

**删除不弹确认框。** 方向都是更安全的那一边:删一条 direct 规则 = 那些流量回到隧道;
删一条 proxy 规则 = 回到默认(global 下仍是隧道)。为 11 条冗余点 11 次确认是在惩罚
正确的行为。

**但删除不许是静默的、不可逆的。** 删完那一行**不消失**,原地变成
`Removed *.foo.com · Undo`,下一次刷新才真正走掉;Undo 就是把同一条加回去。这样
一次误点不会毁掉一条手写规则,而批量清理仍然是连点。

**添加**走一个 sheet:模式输入框 + direct / proxy 二选一。`Add Rule…` 是这个 GUI 里
第一个**可发现**的加规则入口;按应用窗口的右键保留,两条路共用同一个收尾
(`ruleChangeFollowUp`,既有纯函数)。

## 6. 加规则的安全门(本期必须一起做)

### 6.1 今天就存在的缺口

`bx direct add` 会挡住公有云 / 开放子域平台(`aliyuncs.com`、`amazonaws.com`、
`cloudfront.net`、`github.io`、`workers.dev` 等,清单在 `internal/policy`),不加
`--force` 不让加。理由是任何人都能在这些平台上注册子域:把它们加进直连白名单,攻击者
开一个桶就能让你的真实 IP 暴露。

**而 Guardian 的 `applyRuleChange` 完全不查这个。** `setup.AddRule` 只校验类型与模式
语法(`validateKind` + `ValidateRulePattern`),没有一处调 `policy.DirectRisk`。

后果:2026-09-08 上线的「按应用窗口右键 → Always direct」,今天就能**一键**加进一条
CLI 明确拒绝的规则。`ruleCandidates(for:)` 对三段以上域名会生成「精确 + `*.父域`」
两个候选,所以一个连 `x.s3.amazonaws.com` 的应用,右键菜单里就摆着
`*.s3.amazonaws.com`。这不是假设:候选生成器的行为是确定的。

本期加 `Add Rule…` 按钮之后,这条路会被走得多得多,所以必须在同一期堵上。

### 6.2 判据收紧:危险的是通配符,不是平台

所有者在设计问答中提出:检测到云平台时「最好让用户指定到具体的域名,不能那么宽泛」。
这条让判据比现状更准:

- `*.s3.amazonaws.com` **危险** —— 攻击者注册 `evil.s3.amazonaws.com` 即命中。
- `mybucket.s3.amazonaws.com` **安全** —— 那个确切主机攻击者拿不到;直连只对这一个
  主机暴露真实 IP,是一次窄而明确的选择。

而现在的 `policy.DirectRisk(domain)` 把两者一律拦下:`norm` 只做小写与去空白,
真正吃掉通配的是 `route.DomainSet.Match` 的**逐级向上走父域** ——
`*.s3.amazonaws.com` 走到 `amazonaws.com` 命中,`mybucket.s3.amazonaws.com` 也走到
同一处命中,两者对它完全一样。**过宽的门会把人逼去用 `--force`,那正是门死掉的方式。**

新判据放在 `internal/policy`,**一份,两个消费方共用**:

```go
// DirectRuleHazard 判一条 direct 规则会不会重新打开去匿名化洞。
// 危险的是「在任何人都能注册子域的平台上用通配符」,不是平台本身。
func DirectRuleHazard(pattern string) (hazard bool, reason, suggestion string)
```

- 通配符 + 命中开放子域平台 ⇒ hazard,suggestion 说「改成你真正需要的那一个确切主机」。
- 确切主机(无通配符)⇒ 不拦,即使它落在那些平台上。
- 其余 ⇒ 不拦。

### 6.3 三处一起改

**Guardian(POST `/v1/rules`,action=add,kind=direct)**:命中 hazard 返回
**409** + `{"code":"rules_risky_direct"}`。完整理由只进 Guardian 日志,响应体只带
code —— 与本文件其余端点同一条纪律。请求体新增 `force bool`,为 true 时放行;那是
逃生口,不是主路。

**按应用窗口的右键**:**不再提供危险的那个候选**。一键动作不该有确认框,那就不该
把危险选项摆在一键的位置上;`ruleCandidates(for:)` 过滤掉命中 hazard 的通配候选,
只留确切主机。真要那条通配规则,去 `Add Rule…` 里显式过门。

**`Add Rule…` sheet**:收到 409 时**不关闭 sheet**,把用户输入原样留在输入框里,
显示理由与建议(「这个平台上任何人都能注册子域;改成你真正需要的那一个确切主机」),
并提供次要动作 `Add Anyway`(带 `force: true`)。主路是让他把模式改窄,不是让他点
「仍然添加」。

**CLI 对齐**:`bx direct add` 改用同一个 `DirectRuleHazard`。**这是一次有意的行为
变更**:此前被拦的确切主机(如 `mybucket.s3.amazonaws.com`)从此可以直接加,不再需要
`--force`;通配那一半的拦截不变,提示改为带上「改窄」的建议。两条路一份判据 ——
两份判据是这个仓库反复栽的那个坑。

## 7. 刷新模型

按规则的失败计数是活数据,所以窗口可见时要跟着环境刷新走,沿用服务器窗口那条
先例与它踩过的回归:

- 显式打开(用户点菜单)→ `show()`,读不到就明说,**不摆空列表**;
- 环境刷新(轮询 / watch)且窗口可见 → `refreshIfVisible`,就地重画,不抢焦点、
  不弹 alert;窗口关着就不拨。
- in-flight 守卫**只压环境刷新那一路**。`explicit == true` 永不被拦 —— 拦住一次
  显式动作的代价是「用户点了菜单项、什么都没发生」,那是 2026-08-17 已经付过一次
  学费的事。

## 8. 不做的事

- **不扩 `/v1/rules` 的读路径。** 不发每条规则的完整命中/失败计数(成熟配置上它永远
  不为零,会被训练成噪声),也不发跨重启的累计命中历史(死规则那条结论 doctor 已经
  在算,搬过来就是同一份判据两个消费方)。窗口只用今天已有的:体检 `Review` + 已经
  超过门槛的失败行。
- **不展开预设组。** 折叠,不做可展开的第三态 —— 多一层展开/收起的状态要维护要测试,
  换来的是普通用户第一眼看到四十行域名。
- **不动 `bx direct` / `bx proxy` 的其余行为**,不动 `/v1/rules` 的组开关语义。
- **不把 doctor 的 `rule_*` 行从 Checks 页移走。** 它们是 `bx doctor --json` 的契约,
  agent 按名字取;同一份判据两处渲染是对的,两份判据才是错的。

## 9. 守卫

- 纯模型:`ruleRows` 的排序与分类、`review` nil 与空的区分、direct/proxy 合并 ——
  Swift 套件(`RulesModelTests`)。
- `DirectRuleHazard`:通配 vs 确切、命中 vs 未命中、大小写与尾点归一 —— Go 单测。
- Guardian:add 命中 hazard 返回 409 且**盘上一个字节不动**;`force: true` 放行;
  响应体不含 pattern 或任何链接文本(与 `servers_name_exists` 同款,日志里也不许有)。
- 接线(读 `main.swift` / `RulesWindow.swift` 源码,本仓库对 AppKit 唯一够得着的
  办法):每一行的删除按钮接的是 `changeRule(action: remove)`;`Add Rule…` 接的是
  带 force 的重试路径;右键候选真的过滤掉了 hazard;窗口可见时才环境刷新。
- 既有守卫重新锚定但**不许改弱**,逐条点名:`TestMacMenuRulesAndServersFetchFailuresOfferShowDetails`
  (读不到时走同一个失败漏斗)、`TestDirectRuleRiskFlagsOpenCloud` 与
  `TestDirectRuleRiskSilentOnBrandDomains`(判据收紧后要改成通配 vs 确切两组,
  **不是删掉**)、`TestEveryRuleReviewClassHasARenderingPath`(渲染层按 Class 字面
  枚举,新加一类会静默消失 —— 本仓库为这个形状栽过四次;窗口这一侧要有同款穷举)。

## 10. 分期

一期做完,不拆:窗口的价值在于三件事一起到位(看得见问题、删得掉、加得上),
少任何一件它仍然是今天那个半成品。

**真机验收**(装好后由所有者跑):

1. 打开 Routing Rules:预设三行在顶上;下面一张表列出你手写的 18 条,direct 与
   proxy 都在;健康的行右边空着。
2. 制造一条冗余(`sudo bx direct add '*.gc.apple.com'`),窗口里那一行应带「被
   `*.apple.com` 盖住,删掉不改变任何流量」,点删除 → 变成 `Removed · Undo`。
3. `Add Rule…` 输入 `*.s3.amazonaws.com` → 不关闭 sheet,给出理由与「改成确切主机」
   的建议;改成 `bucket.s3.amazonaws.com` → 加得进去。
4. 在按应用窗口右键一个连公有云的应用 → 候选里**没有**那条 `*.…amazonaws.com`。
5. `sudo bx direct add 'bucket.s3.amazonaws.com'` 不再需要 `--force`;
   `sudo bx direct add '*.s3.amazonaws.com'` 仍被拦并给出改窄建议。
