# Guardian 拥有诊断面与配置面,菜单是纯客户端(2026-09-09)

## 动机:三处「不便」是同一个洞

2026-09-09 从用户视角盘点菜单栏 App,排在最前的三条:

1. **出了问题,菜单把你带进死胡同。** 四处失败弹窗的收尾都是「See /var/log/bx-guard.err.log」
   (`main.swift:1217/1343/1404/1437`,`RulesModel.swift:25`),而那个文件是 `root 0600`
   (`install.SecureGuardianLogs`,2026-08-05 刻意收紧的),普通用户打开就是 Permission denied。
   菜单里的「Open Logs」打开的是 `~/Library/Logs/bx`,那是诊断包的落点,平时是空的。
2. **「Check for Problems」把你扔进终端。** `runDoctor` 是 `openTerminal("… sudo bx doctor …")`:
   弹密码、跑完让你按任意键。菜单里唯一一个会突然打开 Terminal 的项。
3. **换配置要输三次密码。** `replaceConfiguration` 依次 `runPrivileged("bx setup …")`、
   `runPrivileged("bx down")`、`runPrivileged("bx up")`,每次一个授权框,期间断网。

三条的根子是一个:**菜单在 Guardian 已经能做的事上还在绕路。** Guardian 是 root、握着日志路径
(`install.GuardianLogPaths()`)、拨得到 Core 的控制 socket、读得到配置与真实 china 列表;它的
`/v1/servers` **已经**有 `add` 与热切换(`supervisor.SwitchServer`:装 → 验健康 → 提交,不健康自动
回滚,不断网、不要密码)。而菜单还在开终端、弹密码、指向 root 文件。

这不是新方向。`docs/superpowers/specs/2026-08-08-control-plane-architecture-design.md` 定下的目标是
「生命周期归 daemon,CLI 与菜单是瘦客户端;安装/卸载/强制拆除留在 CLI —— 因为它们必须在
daemon 不存在时也能工作」。阶段①让菜单的**读**路径全部走 Guardian(spawn 9→0);本设计把
**诊断**与**配置**两个面也收进去,菜单上只剩下按定义不能收的那几处。

## 目标与非目标

**目标**
- 菜单上任何「看」与「改」的动作,只要 Guardian 在,就经 Guardian 的 owner 门完成:不弹密码、不开
  终端、不指向用户打不开的文件。
- 判据只有一份:`bx doctor` 与 Guardian 的 `/v1/doctor` 调同一个纯函数。
- 换配置复用已有的 servers 面,不新开一条「写配置 + 重启 Core」的路。

**非目标**
- 不动 `bx setup` / `bx doctor` 的命令行行为(`--json` 输出逐字节不变,由守卫钉住)。
- 不做日志脱敏、不做日志轮转(后者另有记档的已知缺口)。
- 不给 Guardian 增加除 `/v1/doctor` 探测之外的任何新出网路径。
- 不动 Windows 托盘与 Linux(Guardian 只在 darwin 跑;linux 的 Guardian 门虽已开但无调用方)。

## §1 边界:什么留在 CLI

留在 CLI、菜单继续 `runPrivileged` / `openTerminal` 的**只有两类**:

| 类 | 动作 | 为什么不能收进 Guardian |
|---|---|---|
| Guardian 不存在时也必须能跑 | install / repair(`runEmbeddedInstaller`)、uninstall、update(`updateBx`)、force-teardown(`toggleEscape` 那条) | 它们要停掉、换掉或删掉 Guardian 自己;2026-08-04 那次 71 分钟事故换来的不变量 |
| 必须非 root | leakcheck(`checkForLeaks`) | `bx leakcheck` 拒绝 root;它是独立的检测产品 |
| 写用户目录、要 chown | Export Diagnostics(诊断包归档,`archiveClientLogsWithReason`) | 一年一次;走终端的代价可接受,收进 Guardian 要它写 uid 501 的目录再改属主,不值 |

**这张表是整个设计的不变量**,由 §7 的白名单守卫钉住:`main.swift` 里每一个 `runPrivileged(` /
`openTerminal(` / `runPrivilegedScriptOffMainThread(` 调用点必须落在白名单函数里,多一处即红。

## §2 `/v1/logs`

- `GET /v1/logs?lines=N`(默认 200,上限 2000),`authorizeOwnerPeer`(与 `/v1/rules`、`/v1/servers`
  同一道门:能开关保护的人已经能做更坏的事,取一致是要点)。
- 应答:
  ```json
  {"logs":[{"name":"guardian","path":"/var/log/bx-guard.err.log","lines":["…"],"unavailable":""},
           {"name":"core","path":"/var/log/bx.log","lines":["…"],"unavailable":""}]}
  ```
  路径来自 `install.GuardianLogPaths()`,**不在 Guardian 里再抄一份路径常量**。读不到的那份
  `unavailable` 非空、`lines` 为空 —— 「没读到」与「日志是空的」分开(与 `Tristate` 同一条)。
- 尾部读取按字节从文件末尾倒读,不把 100MB 的文件整个读进内存(2026-09-01 那个文件真的到过 100MB)。
- 不脱敏:日志只经 owner 门发布给同一信任边界的菜单,与 `/v1/rules` 发布配置路径、`/v1/apps` 发布
  可执行路径同一条纪律。**这是一次发布面的记录**:此前日志只有 root 读得到,现在 owner 也读得到。
  理由与 `authorizeOwnerPeer` 当初放宽 `/v1/down` 一样 —— owner 本来就能 `bx down`。
- 能力声明 `CapabilityLogs = "logs"`,菜单按能力决定画不画入口(**绝不试着拨一下看看**)。

## §3 `internal/doctor`:判据一份,事实两份

**为什么要拆**:今天 `collectClientDoctorWith`(`internal/cli/cli.go:2286`,~160 行)把「去哪拿事实」
与「事实说明什么」揉在一起:读配置、`os.Stat` 权限、`config.Parse`、`FetchStatusReport`、
`guardianRulesForDoctor`(拨 Guardian)、`probeCheck`(拨服务器)、`serviceDoctorChecks`、
`readGuardianStatus`、`collectPlatformChecks`。Guardian 要在进程内做同一件事,唯一不重复判据的办法
是把判据抽出来。

**包形状**(与 `internal/leakcheck`、`internal/rulereview`、`internal/pathview` 同款):

```go
package doctor

// Facts 是判据的全部输入。每一项都是「采到的事实」或「没采到 + 原因」,
// 没有一项是判断。
type Facts struct {
    ConfigPath   string
    Config       FileFact          // 字节 / 读错误(区分 ErrPermission 与不存在)/ mode
    Parsed       *config.Config    // nil = 没解出来(ParseErr 非空)
    ParseErr     string
    Service      []ServiceFact     // 平台服务检查(launchd / systemd)的原始结果
    CoreReport   *stats.Report     // nil = Core socket 没应答(CoreErr)
    CoreErr      string
    Guardian     *guardian.Status  // nil = 没问到(GuardianErr)
    GuardianErr  string
    RuleReview   *rulereview.Report
    RuleReviewSkipped string
    Probe        *ProbeFact        // nil = 没探(SkipProbe 或没有链接)
    Platform     []Check           // 平台检查(darwin 的 collectPlatformChecks)原样带入
    Version      string
}

type Check struct{ Name, Status, Detail, Hint string }   // 与 cli.checkReport 逐字段相同
type Report struct{ OK bool; Kind, Version string; SecretsRedacted, ChangesSystem, ChangesNetwork, RequiresRoot bool; Checks []Check }

func Judge(f Facts) Report
```

- `Judge` 是**纯函数**:`purity_test.go` 按 AST 禁 `net`/`os`/`os/exec`/`syscall`,与 leakcheck 同款。
- **check 的 Name 与顺序不变**。`internal/cli/doctor_check_names_test.go` 已经钉着名字;
  新增一条**逐字节守卫**:对固定的 `Facts` fixture,`Judge` 的 JSON 与迁移前 `collectClientDoctorWith`
  的输出相同(迁移那个 commit 里跑一次生成 golden)。`bx doctor --json` 是 MCP 与脚本在用的契约。
- **事实采集两份,各服务各的调用方**:
  - `internal/cli`:`collectDoctorFacts(configPath, target, timeout, skipProbe)` —— 现有代码搬家,
    仍然拨 Guardian 的 `/v1/rules` 取规则体检(那条被授权的退路不变)。
  - `internal/guardian`:`collectDoctorFacts(ctx)` —— 进程内:直接读配置(root)、`reviewRulesAt`
    (它已经会算完整四类)、`fetchCoreRuntime` 同源的 Core 报告、自己的 `Status`、
    `liveServerProbe`(**root 出网**,先例是 `/v1/servers` 的 probe,同一道门、同一个理由:用户
    显式点了一下)、`collectPlatformChecks`(搬到能被两边引的位置)。
- 两份采集喂进**同一个** `Judge`,各有一条接线守卫(CLI 的 `doctorAction` 与 Guardian 的
  `/v1/doctor` handler 函数体里都必须出现 `doctor.Judge(`,且没有第二个产出 `Check` 的地方)。

`/v1/doctor`:`GET`,owner 门,整轮封顶 10 秒(探测 5 秒 + 其余;`bx status` 的观测是 5 秒,
doctor 多一次探测),超时的项如实 `warn` + 「timed out」,**绝不让整个应答失败**。能力声明
`CapabilityDoctor = "doctor"`。

## §4 换配置 = 加服务器并切换

- ServersWindow 加「Add Server…」:一个 sheet,贴链接(剪贴板里有合法链接时预填,判据复用
  `clipboardCandidateLink`)、起名字(默认取 `setup.LinkHost`),然后:
  1. `POST /v1/servers {"action":"add","name":…,"link":…,"udp":…}` 落盘(已有,`addServerEntry`);
  2. `POST /v1/servers {"name":…}` 切换(已有,`applyServerSwitch` → `SwitchServer`:装 → 验健康 →
     提交,不健康自动回滚、原样留在旧那台);
  3. 结果就地显示在窗口里(与 `switchResponse.Applied/Detail` 同款);切换失败时**新服务器仍在清单里**,
     用户能看到它、能再试,这与「Guardian 诚实报失败、不回滚写盘」(2026-09-08 规则热重载)同一条。
- 名字冲突:`setup.AddServer` 对已存在的名字怎么处理,以它为准(幂等或报错都行,但**菜单不得
  静默成功**)。
- 菜单里「Replace Configuration…」删除。**旧 Guardian 没有 servers 能力时**保留今天那条提权路
  (`replaceConfigurationLivesInMenu` 已经在做这个判断,这次它的语义变成「Guardian 不会 add,只能
  走 CLI」),这是降级不是主路。
- `bx setup` 命令行不动:它服务的是没有 Guardian 的机器与首次安装。
- 项目所有者 2026-09-09 定的语义:**新链接成为清单里的一台并切换过去,旧的留着可随时切回**,
  不是覆盖。

## §5 失败弹窗:Show Details 代替一条 root 路径

四处「See /var/log/bx-guard.err.log」与 `RulesModel.swift` 的 `guardianFetchFailureInfo` 那句,改成
弹窗带「Show Details」按钮 → 打开 Diagnostics 窗口的日志页,**并把失败码(`guardianFailureCode`)
高亮到日志里那几行**(Guardian 日志的形状是 `guardian_xxx_failed … err=…`,按码查找)。
`toggleFailureHint` 的失败码 → 指引映射不动;变的只是「完整原因去哪看」:由 Guardian 发布,
不让用户去找一个打不开的文件。

**措辞纪律不变**:弹窗只说发生了什么与下一步,不断言原因。

## §6 Diagnostics 窗口

Troubleshoot ▸ 里「Check for Problems」改为打开 Diagnostics 窗口(不再开终端);「Open Logs」
改为打开同一窗口的日志页。窗口两页:

- **Checks**:`/v1/doctor` 的清单,坏的排前(fail > warn > info > ok),每行 name / status / detail /
  hint;顶部一句合计(`N failed · M warnings`,**不合成一个总数**,与 leakcheck 三段计数同一条)。
  「Run again」按钮重拉。
- **Logs**:`/v1/logs` 两份日志各一个分段,等宽字体,底部「Export Diagnostics…」走 §1 表里那条终端
  路(改名自 Run Doctor 的归档那半)。

纯模型 `DiagnosticsModel.swift`(解码、排序、合计句、失败码定位)进 Swift 套件;窗口只摆。
能力门控:`doctor`/`logs` 任一能力缺席就不画对应页,入口按能力显示。

## §7 守卫

- `internal/doctor/purity_test.go`:`Judge` 及其所到之处不引 net/os/exec。
- 逐字节 golden:固定 `Facts` → `Judge` JSON 与迁移前 `collectClientDoctorWith` 输出相同。
- 接线:`doctorAction`(CLI)与 `/v1/doctor` handler 都调 `doctor.Judge(`;`NewLocalAPI` 真的挂上
  `/v1/doctor`、`/v1/logs` 且门是 `authorizeOwnerPeer`(照 `TestNewLocalAPIWiresAppsEndpoint` 的形状:
  403 那条 + 成功那条都要,成功那条绕不开组装根)。
- `/v1/logs` 的路径来自 `install.GuardianLogPaths()`(不许手抄;`TestGuardianLogsServeTheInstalledPaths`)。
- **菜单 shell-out 白名单**(`internal/cli/macos_menu_shellout_allowlist_test.go`):扫 `main.swift`
  全部 `runPrivileged(` / `openTerminal(` / `runPrivilegedScriptOffMainThread(` 调用点,按所在函数名
  对照白名单 `{beginSetup, runEmbeddedInstaller, uninstallBx, updateBx, exportDiagnostics,
  performToggle(逃生口), replaceConfiguration(旧 Guardian 降级)}`,不在名单内即红;名单本身
  带理由,**加进来可以,悄悄加不行**(与 `destPublicationAllowlist` 同款)。
- 四处 root 路径字面量从 `main.swift` / `RulesModel.swift` 消失(守卫扫字面量 `/var/log/bx-guard`)。
- 能力:`CapabilityLogs`、`CapabilityDoctor` 进 `GuardianCapabilities()`,菜单侧 `logsAvailable` /
  `doctorAvailable` 纯函数与既有 `rulesEditingAvailable` 同款。

## §8 分期(每期单独可发布、单独真机验收)

1. **`/v1/logs` + Show Details**。最小、收益最直接:出问题的那一刻用户能看见原因。验收:
   造一次失败(例如 `bx direct add` 一个非法模式),弹窗点 Show Details 看到 Guardian 日志里那行。
2. **`internal/doctor` 抽取,`bx doctor` 改调它**。行为不变(golden 守着),纯重构。验收:
   `bx doctor --json` 升级前后 diff 为空。
3. **`/v1/doctor` + Diagnostics 窗口**。验收:Check for Problems 不再开终端;清单与 `bx doctor`
   一致(同一台机器两边各跑一次,check 名与状态逐条对上)。
4. **Add Server + 删 Replace**。验收:贴一个新链接 → 不弹密码、不断网、Servers 窗口里多一台且
   已切换;贴一个坏链接 → 回滚到原来那台、新的一台留在清单里并标失败。

顺序刻意:①③ 让「出问题时」的体验先好起来;② 是 ③ 的前置;④ 独立,最后做,因为它改的是
产品语义(见 §4)。

## 风险与已知代价

- **`/v1/doctor` 的探测是 root 出网。** 先例 `/v1/servers` probe 同一道门;仍然是用户显式点了
  一下才发生,循环里绝不自动跑(与「被动观测优于主动探测」同一条判断)。
- **Guardian 那份事实采集与 CLI 那份会漂。** 判据共享挡不住「采到的事实不一样」;§8 ③ 的验收
  (两边逐条对上)是唯一的真机证据,记进 CLAUDE.md 的真机未验清单。
- **`/v1/logs` 让 owner 读到日志。** 发布面扩大,理由见 §2;日志里有服务器 IP 与 bypass 网段,
  与 `bx status --json` 已经发布的内容同一量级。
- Diagnostics 窗口那半 AppKit 代码一行测试都盖不到(与本仓库其它窗口相同),只有 Go 侧接线
  守卫与 Swift 纯模型套件。
