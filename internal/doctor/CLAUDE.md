# CLAUDE.md — internal/doctor(诊断的判据)

本文件只在读到 `internal/doctor/` 下的文件时加载。2026-09-23 从根目录下沉,原文逐字存档在
`docs/lessons/doctor-archive.md`。**两个采集方在别的包里**:`internal/cli/doctor_facts.go`
(`bx doctor`,文本与 `--json`)与 `internal/guardian/doctor.go`(`/v1/doctor`,菜单 Checks 页、
`bx_inspect`)。**改这两个采集方时这份不会自动加载 —— 先读它。** 菜单那一页见
`apps/macos/BxMenu/CLAUDE.md`「Diagnostics 窗口」。

## 判据只有一份

- `doctor.Judge(Facts) Report` 是纯函数:**本包自己的文件**不 import net/os/exec/syscall
  (`purity_test.go` 按 AST;传递依赖不在范围内 —— `config` 自己就拖进 net/os;白名单收了
  `stats` 与 `tristate` 两个只做计算的包,理由写在名单里)。**要什么事实就加进 `Facts`,
  不许让 Judge 自己去拿。**
- **两条路径都是「采集 → Judge → 渲染」**:文本只是同一份 `Report` 的另一种打印
  (`renderDoctorReport` 返回三段式 `doctorLineSpec`,不是 `status|key|value` 串 —— 规则原文里
  真的会带 `|`)。只服务文本路径的孪生判据已删(没有调用方而有测试盖着,等于骗人)。守卫
  `TestClientDoctorIsJudgedByTheDoctorPackage`、`TestDoctorTextPathRendersTheSharedReport`。
- **`TestJudgeGolden`**(`testdata/judge_golden.json`)把判决**逐字节**钉住 —— 逐条断言名字与
  状态挡不住「detail 少了一个字」。**加一种新状态时要加一例 golden**,否则那次改动整个撤掉
  golden 也不动(`failing_rules`、`desired_off` 两例就是这么来的)。
- **本包不能 `import guardian`**(guardian 要调本包,会成环):DNS 三态常量各写一份,
  `TestDoctorDNSStateConstantsMatchGuardian` 守跨包不漂。平台检查在 `internal/platformcheck`
  (cli/Guardian 共用 `Collect`;它不是叶子包,纪律是**不许反向依赖 guardian/cli/install**)。
  `internal/protectionstate` 同理下沉,漂移在构造上不可能。

## 判据上的几条纪律(每条都是真机上撞出来的)

- **关掉保护不是故障**:`GuardianFact.Desired` 进 `DNSCheck` —— 关着且 DNS 已还给系统 ⇒ ok;
  **关着却仍占着 DNS ⇒ warn**(调谐环 `restore_dns` 要处理的真残留,不许被豁免一起判绿);
  意图问不出来时按「要保护」判(宁可多报)。此前一台用户自己 `bx down` 的机器被报成
  `1 failed`(与 Tailscale advisory 当初同一形状)。
- **ok 的行不许带 hint**:抹在 `Report.AddReport` 这个**唯一入口**里(`AddCheck` 也走它),
  不靠十几个产出点各自自觉。
- **`not_checked` 是第四种状态**:采集方没填(nil)与问不到(Err)都产出一行、措辞不同、都不
  缺席。`Report` 有一个**与 `ok` 并列**的 `not_checked` 计数(刻意无 omitempty)。**`Report.OK`
  的含义没改**(仍是「没有一条 fail」):让「有一项没查」把 OK 打成 false,等于宣布一台关着
  保护的机器坏了(Core 不在时流量必然查不到)。
- **多条同类结论合并成恰好一条 check**(`traffic_failing_rules` 与 `riskyRuleFinding` 同理):
  同名 check 会让按名字取的消费方静默丢掉其余结论。
- **文案是英文**(2026-09-17 起,菜单与 CLI 同一处产地)。

## 流量成败(`traffic.go`,2026-09-12,真机未验)

「哪条规则在成片失败」此前只长在 `bx doctor` 的文本路径上;菜单改走 `/v1/doctor` 之后,那台正在
成片失败的机器上 Checks 页写着 `0 failed · 0 warnings`、`bx_inspect` 的 `ok` 是 `true`。
- `Facts.Traffic *TrafficFact`,两个采集方各自填;与 `bx status` 同源(`stats.FailingRules`/
  `UDPNotice`)。守卫打在**渲染出来的行上**:没采到流量的报告与「查了、一切正常」的报告必须
  长得不一样(`TestJudgeMakesUncheckedTrafficLookDifferentFromHealthyTraffic`);菜单走的采集方
  真的问了(`TestGuardianDoctorFactsCarryTraffic`)。
- **`TrafficFact.DirectEgress` 是这份诊断最值钱的一格**:坏的往往不是规则,是 bx 自己的直连器
  (macOS 的 scoped 默认路由不见了)。Guardian 接的是 `observe.DirectEgress(ctx, deps)` 这个单问
  入口(与 `Observe` 共用 `observeDirectEgress`,`(reachable, known, err) → Tristate` 的映射只有
  一份)。**nil ⇒ Unknown,不是 True**。hint 按它说话:观测到 False ⇒ 不许再叫人改 rules;没问
  出来 ⇒ 不许说「不是你的规则」(`TestGuardianDoctorBlamesTheDirectDialerNotTheRules`)。只有
  darwin 有原语,别处由 `NotApplicableForPlatform` 声明、不去问。

## Guardian 那个采集方

- 整轮共享一个 10 秒预算,每个依赖吃同一个 ctx(`TestCollectDoctorFactsGivesEveryDepTheSameDeadline`);
  **生产原语自己也必须认这份 ctx**,由 `TestLiveDoctorDepsForwardTheCtxTheyAreHanded` 按**行为**
  钉住(对着一个 accept 却永不应答的 socket)—— 只注入测试闭包的守卫分不清「采集把 ctx 递下去
  了」与「生产闭包接过去照旧用 `context.Background`」。非 darwin 上 DirectEgress 那条子测试是弱的。
- `probe` 是控制面的一次 TCP 往返,只在用户显式点击那次 GET(已过 owner 门)才发生。**Guardian
  那份永远会探测**,所以 Checks 页比 `bx doctor --skip-probe` 多一行 `probe` 是预期,不是漂移。
- **真机状态**:端点、owner 门、19 项检查、耗时已坐实(Guardian 日志 `guardian_doctor_result`);
  **判据本身对不对仍未验**(Checks 页与 `sudo bx doctor --json --skip-probe` 逐条对比)。
