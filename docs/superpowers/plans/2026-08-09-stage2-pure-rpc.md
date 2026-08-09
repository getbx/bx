# 阶段②:停止路径只做停止 + 逃生口命名 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `bx down` 的干净路径只做停止(不查 legacy Core、不做网关发现、不碰 launchd),并给强制拆除一个可以被直接要求的名字 `bx force-teardown`。

**Architecture:** 只改 `internal/cli` 的 macOS 生命周期编排,不动 `internal/guardian` 的行为、不动数据面。强制拆除的六步实现一行不改——本计划只改**谁能调它**和**它的报告怎么被测**。

**Tech Stack:** Go 1.26、`urfave/cli`、既有的 `macOSLifecycleDeps` 注入式 fake。

## Global Constraints

- **强制拆除六步的实现与顺序一个字节不许改。** `macos_lifecycle_test.go:489` 钉住的
  `desired.off|core.shutdown|guardian.forceTeardown|barrier.clear|dns.restore|desired.off`
  必须**一个断言都不改**就通过——那是「重排没有改变行为」的唯一证据。
- **`down` 的自动回落保留。** 干净路径失败仍然落到强制拆除。理由见设计:
  用户在最需要关掉保护的时候,最不可能知道该换个命令。
- **`up` 路径不动。** 在 `up` 上「先把 daemon 装好起来」是正当的;不变量约束的是停止。
- **菜单栏不改。** 它的逃生口经提权 `bx down` 拿到强制拆除能力,而本期 `down` 的回落
  行为不变。但 `force-teardown` 的注释里必须写明这条依赖(见 Task 3)。
- **两次 `desired.off` 写入的相对位置不许动**(`guardian.go:475` 与 `:516`)——
  它们是故意在跟 `Manager.handleUnexpectedExit` 抢时序,没有锁也没有代际号。
- TDD;中文 conventional commits,结尾 `Co-Authored-By: Claude <noreply@anthropic.com>`;直接在 `master` 提交。
- 验证:`go build ./... && go vet ./... && go test ./... -count=1`;交叉编译 linux/darwin/windows × amd64/arm64;`gofumpt -l`(排除 `internal/embedded/assets/` 与 `internal/winfw/`)。
- **本期不新增 netns 集成台断言**:台子跑在 Linux,而本期改的全是 macOS 生命周期代码。
  不要因为「台子绿了」就以为本期安全。
- **agent 不得在宿主上跑 `bx`/`sudo bx`/`launchctl`/`route`/`networksetup`** —— 用户机器正开着 bx 日常使用。

---

## Task 1: 把 `down` 的报告抽成纯函数并补测(**必须先于任何重排**)

**Files:**
- Modify: `internal/cli/guardian.go`(`macOSDownAction` 的报告段 `:611-624`)
- Create: `internal/cli/downreport.go`
- Create: `internal/cli/downreport_test.go`

**Interfaces:**
- Produces: `func downReportLines(result macOSDownResult) (stdout []string, stderr []string)` —— 纯函数,不打印。

**为什么先做这个。** 强制路径打印的那几行是**用户在最坏时刻看到的唯一指引**
(「已执行:…」「请确认网络是否已恢复…若仍不通,执行 sudo bx uninstall」),而它今天
**零覆盖**:唯一盯着 `macOSDownAction` 的是一条查它有没有调 `forgetUpgradeDebtOnExplicitOff`
的源码文本守卫(`upgraderun_test.go:352-361`)。后面两个任务都要动这段代码,
先把它钉住,重构才可能是安全的。

- [ ] **Step 1: 写失败测试**

`internal/cli/downreport_test.go`:

```go
package cli

import (
	"strings"
	"testing"
)

// 强制路径打印的这几行是用户在最坏时刻(保护关不掉、网络可能不通)看到的唯一指引。
// 它今天零覆盖,而阶段② 要重排这段代码 —— 先钉住,重构才可能是安全的。
func TestDownReportForcedPathTellsTheTruthAndGivesTheNextStep(t *testing.T) {
	out, errLines := downReportLines(macOSDownResult{Forced: true})
	joined := strings.Join(append(append([]string{}, out...), errLines...), "\n")

	// ① 必须说清「走的是强制」,否则用户以为一切正常。
	if !strings.Contains(joined, "强制") {
		t.Errorf("强制路径必须让用户知道走的是强制:\n%s", joined)
	}
	// ② 必须逐条列出做过的动作。用户要凭它判断还差什么。
	for _, action := range []string{"关闭意图", "Core", "Guardian", "屏障", "DNS"} {
		if !strings.Contains(joined, action) {
			t.Errorf("强制路径必须列出做过的动作,缺 %q:\n%s", action, joined)
		}
	}
	// ③ **绝不能断言「网络已还原」。** 强制拆除是尽力而为,六步里任何一步都可能失败
	//    而流程仍然继续 —— 断言已还原就是骗人,而被骗的人此刻可能正断着网。
	if strings.Contains(joined, "网络已恢复") {
		t.Errorf("强制路径不得断言网络已恢复(它做不到这个保证):\n%s", joined)
	}
	// ④ 必须给出下一步。
	if !strings.Contains(joined, "bx uninstall") {
		t.Errorf("强制路径必须给出仍不通时的下一步:\n%s", joined)
	}
}

// 干净路径可以断言已恢复 —— Guardian 的事务成功返回才走到这里。
func TestDownReportCleanPathMayAssertRecovery(t *testing.T) {
	out, _ := downReportLines(macOSDownResult{})
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "已停止") {
		t.Errorf("干净路径要告诉用户已停止:\n%s", joined)
	}
	if strings.Contains(joined, "强制") {
		t.Errorf("干净路径不该出现「强制」字样:\n%s", joined)
	}
}

// 回落原因必须原样带给用户:它是唯一能说明「为什么没能干净停下」的东西。
func TestDownReportCarriesTheCleanPathFailureCause(t *testing.T) {
	_, errLines := downReportLines(macOSDownResult{Forced: true, Cause: errSentinelForReport})
	joined := strings.Join(errLines, "\n")
	if !strings.Contains(joined, errSentinelForReport.Error()) {
		t.Errorf("回落原因必须出现在给用户的输出里:\n%s", joined)
	}
}

// Cause 为 nil 表示 Guardian 压根没应答 —— 那也要说清楚,不能只说「失败了」。
func TestDownReportSaysGuardianWasUnreachableWhenThereIsNoCause(t *testing.T) {
	_, errLines := downReportLines(macOSDownResult{Forced: true})
	joined := strings.Join(errLines, "\n")
	if !strings.Contains(joined, "未响应") {
		t.Errorf("没有 Cause 时要说明 Guardian 未响应:\n%s", joined)
	}
}

var errSentinelForReport = errReportSentinel{}

type errReportSentinel struct{}

func (errReportSentinel) Error() string { return "recovery-incomplete-sentinel" }
```

- [ ] **Step 2: 跑它,确认失败**

Run: `go test ./internal/cli -run TestDownReport -count=1`
Expected: FAIL,`undefined: downReportLines`

- [ ] **Step 3: 抽实现**

`internal/cli/downreport.go`:把 `macOSDownAction` 里 `if result.Forced { … }` 那一段的
**文案逐字搬过来**,返回而不是打印。`macOSDownAction` 改为调它并打印结果。
`stepDone` 的调用留在 `macOSDownAction`(它是进度指示,不是报告)。

**文案一个字都不许改** —— 本任务是「让它可测」,不是「让它更好」。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./... -count=1`
Expected: PASS,且既有测试零改动。

- [ ] **Step 5: 变异验证**

把「请确认网络是否已恢复」那句改成「网络已恢复」→ 测试 ③ 必须红。
把动作枚举那行删掉 → 测试 ② 必须红。两处改回。

- [ ] **Step 6: 提交**

```bash
git add internal/cli/downreport.go internal/cli/downreport_test.go internal/cli/guardian.go
git commit -m "refactor(cli): down 的报告抽成纯函数并补测 —— 它今天零覆盖"
```

---

## Task 2: 停止路径只做停止

**Files:**
- Modify: `internal/cli/guardian.go`(`cleanGuardianDown` `:446-457`)
- Modify: `internal/cli/macos_lifecycle_test.go`(给 fake deps 加调用计数)

**Interfaces:**
- Consumes: Task 1 的 `downReportLines`(不直接用,但本任务的重排受它保护)

**要改的就一行**:`cleanGuardianDown` 删掉 `ensureGuardianOwnership` 那一跳。

**要加的是守卫**,而且必须是**行为断言**,不是源码文本匹配:

- [ ] **Step 1: 给 fake deps 加计数**

`fakeMacOSLifecycleDeps` 已有 `forceTeardownCount` 等计数器。照同样形状,给
`testMacOSLifecycleDeps` 里的 `legacyLoaded`/`migrationRequest`/`removeLegacyUnit`/
`writeGuardianUnit`/`enableGuardian` 各加一个计数器(字段 + 在闭包里 `++`)。

- [ ] **Step 2: 写失败测试**

```go
// 停止就是停止。发 POST /v1/down 之前不该查 legacy Core、不该做网关发现、
// 不该解析 config、不该碰 launchd。
//
// 今天它们都会跑:cleanGuardianDown 第一步是 ensureGuardianOwnership。后果不是
// 假想的 —— 任何一步报错都会让一次本可以干净完成的停止,升级成拆屏障、还原 DNS、
// 写 desired=off 的重手术。而停止不该关心有没有 legacy Core 要迁移:那是 up 的工作。
func TestCleanDownDoesNoStartupWork(t *testing.T) {
	f := newFakeMacOSLifecycleDeps()
	if _, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", f.macOSLifecycleDeps); err != nil {
		t.Fatal(err)
	}
	if f.forcedTeardownCalled() {
		t.Fatalf("这条用例的前提是走干净路径, trace=%s", f.trace())
	}
	for _, probe := range []struct {
		name  string
		count int
	}{
		{"legacyLoaded", f.legacyLoadedCount},
		{"migrationRequest", f.migrationRequestCount},
		{"removeLegacyUnit", f.removeLegacyUnitCount},
		{"writeGuardianUnit", f.writeGuardianUnitCount},
		{"enableGuardian", f.enableGuardianCount},
	} {
		if probe.count != 0 {
			t.Errorf("干净停止路径上不该调用 %s(实际 %d 次)—— 停止不许依赖与停止无关的工作",
				probe.name, probe.count)
		}
	}
}

// 反向:迁移没有丢,它留在 up 上 —— 那本来就是它该在的地方。
func TestUpStillDoesTheStartupWork(t *testing.T) {
	f := newFakeMacOSLifecycleDeps()
	if _, err := macOSUpLifecycle(context.Background(), "/etc/bx/config.yaml", f.macOSLifecycleDeps); err != nil {
		t.Fatal(err)
	}
	if f.legacyLoadedCount == 0 {
		t.Error("up 必须仍然检查 legacy Core —— 把它从 down 上摘掉不等于把它删掉")
	}
}
```

(`macOSUpLifecycle` 的实际签名以 `guardian.go:355` 为准;若参数不同请照实调整,
**不要改生产签名**。)

- [ ] **Step 3: 跑它,确认失败**

Run: `go test ./internal/cli -run 'TestCleanDownDoesNoStartupWork|TestUpStillDoes' -count=1`
Expected: 第一条 FAIL(计数非 0),第二条 PASS。

- [ ] **Step 4: 实现**

```go
func cleanGuardianDown(ctx context.Context, purpose downPurpose, configPath string, deps macOSLifecycleDeps) (guardian.Status, error) {
	// 停止只做停止。
	//
	// 这里**曾经**先调 ensureGuardianOwnership —— 那会在发 /v1/down 之前 shell 出去查
	// legacy Core,legacy Core 真在跑时还要做网关发现 + 读 Core /v0/runtime + 解析 config
	// + DNS 查询。任何一步报错,一次本可以干净完成的停止就升级成拆屏障、还原 DNS 的重手术。
	//
	// 而且那个函数的契约是「确保它装好并起着」——在停止路径上调用它本身就是错的,
	// 哪怕 guardianEnableCommands 的 active&&ready 快路径让它今天恰好是空操作:
	// 那是巧合不是契约,谁把它改成「总是 kickstart 一次」,down 就会在停止前重启守护进程。
	//
	// legacy Core 的迁移没有丢,它留在 up 上(macOSUpLifecycle),那本来就是它该在的地方。
	if purpose == downPurposeUpgrade {
		// 升级欠条必须活过这一跳(2026-08-08 复审 C1)。
		return deps.client.DownForUpgrade(ctx)
	}
	return deps.client.Down(ctx)
}
```

- [ ] **Step 5: 跑全量,确认既有断言零改动**

Run: `go test ./... -count=1`
Expected: PASS。**特别确认 `macos_lifecycle_test.go` 里那组强制路径顺序断言一个都没改。**
若有既有测试因为「不再调用某 hook」而失败,**先判断它断言的是行为还是实现**:
断言行为的要保留并说明冲突,断言实现细节的可以删,但要在报告里逐条列出。

- [ ] **Step 6: 变异验证**

把 `ensureGuardianOwnership` 调回 `cleanGuardianDown` 开头 → `TestCleanDownDoesNoStartupWork`
必须红。改回。

- [ ] **Step 7: 提交**

```bash
git add internal/cli/guardian.go internal/cli/macos_lifecycle_test.go
git commit -m "fix(cli): 停止路径只做停止,不再查 legacy Core 也不碰 launchd"
```

---

## Task 3: `bx force-teardown`

**Files:**
- Modify: `internal/cli/cli.go`(命令注册)
- Modify: `internal/cli/guardian.go`(新增 action)
- Create/Modify: `internal/cli/macos_lifecycle_test.go`

**Interfaces:**
- Produces: `bx force-teardown` 命令;`func macOSForceTeardownAction(c *urfavecli.Context) error`

- [ ] **Step 1: 写失败测试**

```go
// force-teardown 与 down 的强制回落必须是**同一件事**:同样的六步、同样的顺序。
// 两条实现会走漂,而走漂的那一天,用户是在保护关不掉的时候撞上它。
func TestForceTeardownProducesTheSameActionSequenceAsDownFallback(t *testing.T) {
	viaDown := newFakeMacOSLifecycleDeps()
	viaDown.macOSLifecycleDeps.guardianReady = func(context.Context) bool { return false }
	if _, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", viaDown.macOSLifecycleDeps); err != nil {
		t.Fatal(err)
	}

	direct := newFakeMacOSLifecycleDeps()
	if err := forcedMacOSTeardown(context.Background(), direct.macOSLifecycleDeps, nil); err != nil {
		t.Fatal(err)
	}

	if viaDown.trace() != direct.trace() {
		t.Fatalf("两条路必须产生同一串动作\ndown 回落=%s\nforce-teardown=%s", viaDown.trace(), direct.trace())
	}
}

// force-teardown 不做 socket 预检:用户明知 Guardian 卡死时,不该被迫先等 10 秒。
func TestForceTeardownSkipsTheGuardianReadyProbe(t *testing.T) {
	f := newFakeMacOSLifecycleDeps()
	probed := 0
	f.macOSLifecycleDeps.guardianReady = func(context.Context) bool { probed++; return true }
	if err := forcedMacOSTeardown(context.Background(), f.macOSLifecycleDeps, nil); err != nil {
		t.Fatal(err)
	}
	if probed != 0 {
		t.Errorf("force-teardown 不该探 Guardian socket(探了 %d 次)——它存在的理由就是绕过那条路", probed)
	}
}
```

命令注册的守卫照 `internal/cli/cli_test.go` 里既有的 `findCommand` 写法,断言
`bx force-teardown` 存在且**不是 Hidden**(它要能被用户查到)。

- [ ] **Step 2: 跑它,确认失败**

- [ ] **Step 3: 实现**

`macOSForceTeardownAction`:直接调 `forcedMacOSTeardown(ctx, defaultMacOSLifecycleDeps(), nil)`,
然后用 Task 1 的 `downReportLines(macOSDownResult{Forced: true})` 打印**同一份报告**。

命令注册(`cli.go`):

```go
{
	Name:  "force-teardown",
	Usage: "强制停止 bx 并拆除它装的一切(Guardian 卡死时用;不保证网络已还原)",
	Action: forceTeardownAction,
},
```

**注释里必须写明对菜单栏的依赖**:

```go
// macOSForceTeardownAction 让强制拆除**可以被直接要求**,而不是只能作为 `bx down`
// 干净路径失败后的副作用。
//
// ⚠️ `bx down` 的自动回落**不能**因为有了这个命令就删掉:用户在最需要关掉保护的
// 时候,恰恰最不可能知道该换个命令。而且菜单栏的逃生口今天是提权跑 `bx down` 拿到
// 这个能力的(守卫见 cli_test.go 对 privilegedTurnOffScript 的断言)—— 谁要删回落,
// 必须在**同一个提交**里把菜单指到这里,否则 Guardian 死时菜单会死在 connect() 上,
// 而那正是它需要逃生口的那一刻。
```

- [ ] **Step 4: 跑全量**

- [ ] **Step 5: 变异验证**

在 `macOSForceTeardownAction` 里插一句 `deps.guardianReady(ctx)` →
`TestForceTeardownSkipsTheGuardianReadyProbe` 必须红。
把它改成走 `macOSDownLifecycleDetailed` → 上面那条「同一串动作」的测试仍绿(它本来就该绿),
但预检那条必须红 —— 若两条都绿,说明测试没覆盖到差异,如实报告。

- [ ] **Step 6: 提交**

```bash
git add internal/cli/cli.go internal/cli/guardian.go internal/cli/macos_lifecycle_test.go
git commit -m "feat(cli): bx force-teardown —— 逃生口有了自己的名字"
```

---

## Task 4(可丢):迁移请求的构造搬进 Guardian

Task 2 之后,`migrationRequest` 只在 `up` 上跑,**已经不在停止路径上了**,
所以本任务的价值从「修不变量」降为「CLI 少做一件 Guardian 自己能做的事」。

**如果前三个任务有任何一个出了岔子,直接丢掉本任务** —— 阶段② 的目标已经达成。

**Files:** `internal/guardian/localapi.go`(`migrationHandler`)、`internal/cli/guardian.go`(`:291-352`、`:735-750`)

Guardian 两个输入都已接好:`GatewayProviderFunc(DiscoverDefaultGateway)`(`daemon.go:369`)
与 Core 控制 socket 客户端(`daemon.go:340`)。`/v1/migrate` 自己推导即可,CLI 只发空请求。

**风险**:`/v1/migrate` 是 root-only 且由 `TestLocalAPIUpdateAndMigrateStayRootOnlyEvenWithOwnerConfigured`
钉着 —— 改它的入参不许放宽授权。**这条要单独断言。**

---

## 完成后应当为真的判据

- `bx down` 的干净路径上,`legacyLoaded`/`migrationRequest`/`removeLegacyUnit`/
  `writeGuardianUnit`/`enableGuardian` 调用次数**全为 0**(行为断言)。
- `up` 上它们仍被调到(迁移没丢)。
- 强制路径六步顺序的既有断言**一个未改**。
- `bx force-teardown` 与 `down` 的回落产生**相同的动作序列**。
- 强制路径的用户可见报告有测试,且**测试禁止它断言「网络已还原」**。

## 真机验收(用户执行,不由 agent 跑)

1. `sudo bx down` 正常路径 → 仍打印 `✅ bx 已停止并取消开机自启。`
2. `sudo bx up` → 正常;若机器上还有 legacy Core,迁移仍然发生(看 Guardian 日志)。
3. `sudo bx force-teardown` → 打印与强制回落**同样**的动作清单,且不声称网络已还原。
4. 菜单栏 Turn Off 仍工作(本期未动它,但它经 `bx down`,值得看一眼)。
