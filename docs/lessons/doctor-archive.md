# 诊断(doctor) —— 2026-09-23 从根目录 CLAUDE.md 下沉时的原文存档

**这是存档,不是现行判据。** 这些段落的**判据**浓缩进了 `internal/doctor/CLAUDE.md`(那一份才是改代码之前要读的);
这里逐字保留当时的原文,给想知道「当初怎么换来的」的人看。读到与 `internal/doctor/CLAUDE.md` 冲突的地方,以后者为准。

---
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
