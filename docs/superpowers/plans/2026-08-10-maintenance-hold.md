# 维护挂起(Maintenance Hold)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把「此刻不能有保护」从**改写 `desired`** 换成一个正交的、带原因与过期时间的维护挂起文件,使磁盘上的 `desired` 永远只记录用户的意图,并让升级欠条(`upgrade-intent.json`)连同它的请求作用域标记一起退休。

**Architecture:** 新增 `/var/lib/bx/maintenance-hold.json`(自带 `schema_version`、整文件原子写、**读取时**判过期,15 分钟)。Guardian 里三个会**自己发起 Core** 的地方(`handleUnexpectedExit`、启动恢复 `recoverLocked`、调谐器 `decide`)改为从**同一次磁盘读**拿 `desired` 与挂起,挂起武装期间一律停手。升级的停机路径(干净路径 `Manager.Down` 与逃生路径 `forcedMacOSTeardown`)不再写 `desired=off`,改为先武装挂起;**挂起写失败时退回写 `desired=off`,拆除照常做完**。用户显式的 up/down 无条件清挂起。挂起随 `Status` 发布,`observe.Diverge`、`bx status` 与 Swift 菜单都认它。

**Tech Stack:** Go 1.26(`internal/guardian`、`internal/cli`、`internal/observe`)+ Swift(`apps/macos/BxMenu`,`swiftc` 直编直跑的测试脚本 `scripts/test-macos-menu.sh`)。无新增第三方依赖。

## Global Constraints

- **TDD**:先写失败测试 → 跑红 → 最小实现 → 跑绿 → 提交。每个任务结尾必须做**变异验证**(把生产改动手工改坏,确认对应测试转红,再改回)。
- **提交信息**:中文 conventional commits,结尾带 `Co-Authored-By: Claude <noreply@anthropic.com>`。
- **直接在默认分支 `master` 上提交**(单人项目)。
- **验证命令**(每个任务的收尾都要跑):
  - `go build ./... && go vet ./... && go test ./... -count=1`
  - `go test -race ./internal/guardian ./internal/cli -count=1`
  - 跨平台交叉编译:linux/darwin/windows × amd64/arm64,例如 `GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...`
  - `git ls-files '*.go' | grep -v 'embedded/assets\|internal/winfw' | xargs gofumpt -l` —— **按打印出来的内容判断**(它即使列出了文件也退出 0,只看退出码等于没跑)。
- **实现者绝不运行** `bx`、`sudo bx`、`launchctl`、`route`、`networksetup`、`ifconfig`。
- **实现者绝不使用** `git stash` / `git reset` / `git checkout -- <path>` / `git clean`。
- **测试必须是行为断言,不是源码文本/AST 匹配。** 本仓库的文本守卫在紧邻的两期里被绕过 8 次与 7 次;上一期 7 条 review finding **全部**是「绿得不对」的守卫(证明了 channel 存在而非 goroutine 跑过、证明了接线存在而非循环体执行过、证明了字段存在而非它活过一次 HTTP 往返、只匹配一个字面量的黑名单、只在一半输入空间上断言的不变量、探针落在 int64 溢出之后、fixture 里字段全是零值)。本计划中**每一条断言都注明了「哪一处生产改动会让它转红」**;写不出那句话的断言不要写。
- **Guardian 只有 darwin 实体,集成 harness 只有 Linux**(`internal/supervisor/harness*_netns_linux_test.go` 跑真 `supervisor.Run()`,但它跑的是数据面/Core 那一侧,**碰不到 Guardian**)。故本期的接线保证**全部来自测试替身与真实 `*Store`(`t.TempDir()`)**,没有任何一条真机验证。计划末尾的「真机验收清单」必须原样写进提交说明,别让「测试全绿」被读成「真机验过」。
- **CI 确实会编译并测试 Swift**:`.github/workflows/ci.yml` 有 `macos-app` job,跑 `swift build --package-path apps/macos/BxMenu` 与 `bash scripts/test-macos-menu.sh`(并断言收尾横幅 `macOS menu tests passed` 打印过)。所以 Swift 编译错误与断言失败**会**让 CI 红。但注意:`Tests/` 下的文件不属于任何 SwiftPM target,只由那个脚本用 `swiftc` 直编 —— **新增测试文件必须同时注册进 `scripts/test-macos-menu.sh`,否则它一次都不会跑,而 CI 照样绿。**

## 任务顺序与理由(先读这一段)

半迁移的树**绝不允许出现「升级既没有挂起、也没有 `desired=off`」的提交** —— 那时一个活着的 Guardian 会在二进制换到一半时装屏障、fork 新 Core(设计取舍二)。顺序据此定死:

1. **Task 1–2 先立守卫,再谈别的。** Task 1 只加文件与它的读写(**没有任何消费方**,零行为变化);Task 2 让 Guardian 那两条「自己发起 Core」的路认挂起 —— 而此刻**没有任何东西会武装挂起**,所以对每一台现存机器同样是零行为变化。守卫先于依赖它的改动落地。
2. **Task 4 是唯一翻转行为的提交**:升级停机改为武装挂起、不再写 `desired=off`。它安全,**只因为 Task 2 已经落地**。
3. **关于「`handleUnexpectedExit` 与 `forcedMacOSTeardown` 是否必须同一个任务」的判断:不必须,拆开更安全。** 判据是「拆开会不会留下一个『强制路径丢了 `desired=off` 而没有别的东西守着』的提交」。按本顺序不会:守卫(Task 2)在**前**,移除(Task 4)在**后**,中间那个提交里强制路径照旧写 `desired=off`,守卫只是多了一条永远不成立的分支。反过来的顺序才是禁止的。
4. **但 Task 4 内部的两处写入点必须同一个提交**:`Manager.Down`(干净路径,`manager.go:710` 的 `SaveDesired(DesiredOff)`)与 `forcedMacOSTeardown`(逃生路径,`guardian.go:603,644` 的两次 `persistDesiredOff`)。设计风险二写得很清楚:「升级路径上真有两个写入点,漏一个等于没修」。分开落地不会造成不安全的中间态(两个守卫任一成立即可),但会造成一个**语义上无法描述**的中间态:同一次「升级停保护」经不同分支在盘上留下不同的意图,没有哪条测试能写出它应该是什么。
5. **Task 5(legacy 欠条迁移)必须早于 Task 6(删欠条代码)**:先让新代码认得旧文件,再删旧路径。
6. **Task 7–9(发布/诊断/UI)在语义确定之后**,它们只读、不改控制流。

---

### Task 1: 维护挂起的存储与生命周期(文件 + 过期 + 卸载清理)

**Files:**
- Create: `internal/guardian/hold.go`
- Create: `internal/guardian/hold_test.go`
- Modify: `internal/guardian/types.go`(`Paths` 加一个字段)
- Modify: `internal/guardian/store.go:28-37`(`OpenDefaultStore` 填默认路径)
- Modify: `internal/cli/uninstall_plan.go:21-43,53-66`
- Modify: `internal/cli/uninstall_plan_test.go`(或就近的既有卸载计划测试文件)

**Interfaces:**
- Produces:
  - `const guardian.MaintenanceHoldDuration = 15 * time.Minute`
  - `const guardian.HoldReasonUpgrade = "upgrade"`、`const guardian.HoldReasonLegacyUpgrade = "legacy_upgrade"`
  - `type guardian.MaintenanceHold struct { SchemaVersion int; Reason string; ExpiresAt time.Time }`
  - `func (s *guardian.Store) ArmMaintenanceHold(reason string, now time.Time) error`
  - `func (s *guardian.Store) LoadMaintenanceHold(now time.Time) (MaintenanceHold, bool, error)`
  - `func (s *guardian.Store) ClearMaintenanceHold() error`
  - `guardian.Paths.MaintenanceHold string`
  - `cli.darwinMaintenanceHoldPath`

- [ ] **Step 1: 写失败测试**

新建 `internal/guardian/hold_test.go`:

```go
package guardian

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func holdPaths(root string) Paths {
	p := testPaths(root)
	p.MaintenanceHold = filepath.Join(root, "maintenance-hold.json")
	return p
}

// 挂起在**读取时**判过期,不靠任何定时器 —— 照 internal/toolkeys/store.go:154,182
// 那个唯一持久化过期的先例。
func TestMaintenanceHoldExpiresAtReadTime(t *testing.T) {
	s := OpenStore(holdPaths(t.TempDir()))
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	if err := s.ArmMaintenanceHold(HoldReasonUpgrade, base); err != nil {
		t.Fatal(err)
	}
	if _, armed, err := s.LoadMaintenanceHold(base.Add(MaintenanceHoldDuration - time.Second)); err != nil || !armed {
		t.Fatalf("过期前应仍武装:armed=%v err=%v", armed, err)
	}
	if _, armed, err := s.LoadMaintenanceHold(base.Add(MaintenanceHoldDuration)); err != nil || armed {
		t.Fatalf("到点即失效:armed=%v err=%v", armed, err)
	}
}

// 挂起必须活过进程重启(internal/confirm/deadman.go 那套纯内存的形状不适用)。
func TestMaintenanceHoldSurvivesProcessRestart(t *testing.T) {
	root := t.TempDir()
	base := time.Now()
	if err := OpenStore(holdPaths(root)).ArmMaintenanceHold(HoldReasonUpgrade, base); err != nil {
		t.Fatal(err)
	}
	hold, armed, err := OpenStore(holdPaths(root)).LoadMaintenanceHold(base.Add(time.Minute))
	if err != nil || !armed {
		t.Fatalf("另一个 Store 实例读不到挂起:armed=%v err=%v", armed, err)
	}
	if hold.Reason != HoldReasonUpgrade {
		t.Fatalf("reason = %q, want %q", hold.Reason, HoldReasonUpgrade)
	}
}

// **整个设计取舍一**:guardian-state.json 里是个裸 JSON 字符串,旧 Guardian 读不懂
// 任何新形状就会 recoveryBlocked=true 永久拒绝 Down(2026-08-04 那次 71 分钟事故的
// 机制)。挂起因此必须住在另一个文件里,而且武装它**一个字节都不能动** desired。
func TestArmingHoldLeavesDesiredFileByteIdentical(t *testing.T) {
	paths := holdPaths(t.TempDir())
	s := OpenStore(paths)
	if err := s.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.Desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(paths.Desired)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || string(after) != `"on"` {
		t.Fatalf("desired 文件被动过:before=%s after=%s", before, after)
	}
}

// 读不动 / 读不懂**不许**塌缩成「没有挂起」—— 那正是 LoadUpgradeIntent
// (store.go:98-101)与它自己注释相反的那个 bug。
func TestUnreadableHoldIsAnErrorNotSilentlyUnarmed(t *testing.T) {
	paths := holdPaths(t.TempDir())
	if err := os.WriteFile(paths.MaintenanceHold, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, armed, err := OpenStore(paths).LoadMaintenanceHold(time.Now()); err == nil || armed {
		t.Fatalf("坏文件必须报错:armed=%v err=%v", armed, err)
	}
}

// 没有过期时刻的挂起会永久压制保护,不许存在。
func TestHoldWithoutExpiryIsRejectedOnRead(t *testing.T) {
	paths := holdPaths(t.TempDir())
	body, _ := json.Marshal(MaintenanceHold{SchemaVersion: 1, Reason: HoldReasonUpgrade})
	if err := os.WriteFile(paths.MaintenanceHold, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, armed, err := OpenStore(paths).LoadMaintenanceHold(time.Now()); err == nil || armed {
		t.Fatalf("无过期时刻的挂起必须报错:armed=%v err=%v", armed, err)
	}
}

// 清挂起是幂等的:文件本来就不在不算失败。它是逃生路径上的一步,不许挑剔。
func TestClearMaintenanceHoldIsIdempotent(t *testing.T) {
	s := OpenStore(holdPaths(t.TempDir()))
	if err := s.ClearMaintenanceHold(); err != nil {
		t.Fatalf("文件不存在时清挂起不该失败: %v", err)
	}
	if err := s.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearMaintenanceHold(); err != nil {
		t.Fatal(err)
	}
	if _, armed, err := s.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("清完还武装着:armed=%v err=%v", armed, err)
	}
}
```

在 `internal/cli/uninstall_plan_test.go` 追加:

```go
// 挂起文件必须随卸载消失,否则它活过卸载重装 —— darwinCoreProcessStatePath 与
// darwinUpgradeIntentPath 就是为这条教训加进去的。
func TestUninstallPlanRemovesMaintenanceHoldButKeepsDataDir(t *testing.T) {
	plan := buildDarwinUninstallPlan(501, "/Users/tester", true)
	if !slices.Contains(plan.RemovePaths, darwinMaintenanceHoldPath) {
		t.Fatalf("RemovePaths 缺挂起文件: %v", plan.RemovePaths)
	}
	if slices.Contains(plan.RemovePaths, darwinDataDirPath) {
		t.Fatalf("绝不整目录删 /var/lib/bx: %v", plan.RemovePaths)
	}
}
```

**每条断言由哪一处生产改动供养:**
- `TestMaintenanceHoldExpiresAtReadTime` ← `LoadMaintenanceHold` 里的 `hold.ExpiresAt.After(now)` 比较。删掉它(恒返回 armed)或改成 `!Before` 边界翻转 → 红。
- `TestMaintenanceHoldSurvivesProcessRestart` ← 落盘。把挂起改成 `Store` 的内存字段 → 红。
- `TestArmingHoldLeavesDesiredFileByteIdentical` ← 挂起走 `paths.MaintenanceHold` 而非 `paths.Desired`。把它塞进 `guardian-state.json` 的信封 → 红。
- `TestUnreadableHoldIsAnErrorNotSilentlyUnarmed` ← `LoadMaintenanceHold` 对 `json.Unmarshal` 错误 `return …, false, err`。改成 `return …, false, nil` → 红。
- `TestHoldWithoutExpiryIsRejectedOnRead` ← `ExpiresAt.IsZero()` 那条校验。删掉 → 红。
- `TestUninstallPlanRemovesMaintenanceHold…` ← `buildDarwinUninstallPlan` 的 `RemovePaths` 里那一行。删掉 → 红。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian -run TestMaintenanceHold -count=1`
Expected: 编译失败,`undefined: MaintenanceHold` / `Paths.MaintenanceHold`。

Run: `go test ./internal/cli -run TestUninstallPlanRemovesMaintenanceHold -count=1`
Expected: 编译失败,`undefined: darwinMaintenanceHoldPath`。

- [ ] **Step 3: 最小实现**

`internal/guardian/types.go` 的 `Paths` 加字段(紧跟 `UpgradeIntent` 之后):

```go
	// MaintenanceHold 记录「此刻不该有保护,但用户想要」。
	//
	// **它绝不进 Desired 那个文件。** guardian-state.json 的内容字面就是 `"on"`
	// 或 `"off"`(裸 JSON 字符串,没有信封、没有 schema_version、没有迁移机制),
	// 往里加字段的后果不是「旧版本忽略未知字段」,而是旧 Guardian 的 LoadDesired
	// 报错 → recoverLocked 置 recoveryBlocked=true → Manager.Down 第一句就返回
	// errRecoveryIncomplete,**永久**。而升级恰恰是新旧两版共存的那一刻。
	MaintenanceHold string
```

`internal/guardian/store.go` 的 `OpenDefaultStore` 里补一行:

```go
		MaintenanceHold: guardianStateDirectory + "/maintenance-hold.json",
```

新建 `internal/guardian/hold.go`:

```go
package guardian

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// MaintenanceHoldDuration 是一次维护挂起的有效期。
//
// 15 分钟:要盖住最长的一次可信升级(替换文件 + 重启 Guardian + 起保护;下载发生
// 在此之前),又要短到崩溃之后当天就能看出不对。
//
// **常量单点拥有,消费方不得各自重导**——照 ReconcileStaleAfter(reconcile_loop.go)
// 的先例:另一个进程里抄一个 15 分钟出来,两个数字迟早分叉。
const MaintenanceHoldDuration = 15 * time.Minute

// 挂起的来由。原样进 JSON 与日志,所以是稳定标识符,不是给人看的句子。
const (
	HoldReasonUpgrade       = "upgrade"
	HoldReasonLegacyUpgrade = "legacy_upgrade"
)

var maintenanceHoldReasonPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// MaintenanceHold 是「此刻不该有保护」这件事本身,与 desired 正交:它带来由与
// 过期时刻,**从不改写 desired**。
type MaintenanceHold struct {
	SchemaVersion int       `json:"schema_version"`
	Reason        string    `json:"reason"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// ArmMaintenanceHold 武装一次挂起。整文件原子替换,**绝不 read-modify-write**:
// 写这份状态的有两个进程(CLI 与 Guardian)且没有任何锁,Store 里那把 mutex
// 连进程内都保护不了什么(internal/cli 每次调用都新建一个 Store)。
func (s *Store) ArmMaintenanceHold(reason string, now time.Time) error {
	if !maintenanceHoldReasonPattern.MatchString(reason) {
		return fmt.Errorf("invalid maintenance hold reason %q", reason)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paths.MaintenanceHold == "" {
		return fmt.Errorf("guardian maintenance hold path required")
	}
	if err := os.MkdirAll(filepath.Dir(s.paths.MaintenanceHold), 0o700); err != nil {
		return fmt.Errorf("create guardian state directory: %w", err)
	}
	return writeJSONAtomically(s.paths.MaintenanceHold, MaintenanceHold{
		SchemaVersion: 1,
		Reason:        reason,
		ExpiresAt:     now.Add(MaintenanceHoldDuration),
	})
}

// LoadMaintenanceHold 返回 (挂起, 是否武装, 错误)。
//
// **三个返回值不是啰嗦:「没有挂起」「挂起过期了」「问不出来」是三件事。**
// LoadUpgradeIntent(store.go:98-101)把它们压成两个 bool,于是 EACCES/EIO 与
// 「文件不在」返回同一个答案 —— 那与它自己 :88-91 的注释正好相反,也正是这份
// 设计要消灭的读取偏置。调用方对 err 一律 fail-closed(不起 Core、不收敛)。
//
// 过期在**读取时**判,不设定时器:进程重启后照样成立。过期的文件刻意不在这里
// 删 —— 读路径不许因为一次 unlink 失败而失败;清理由 bx up/bx down/uninstall 做。
func (s *Store) LoadMaintenanceHold(now time.Time) (MaintenanceHold, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paths.MaintenanceHold == "" {
		return MaintenanceHold{}, false, nil
	}
	b, err := os.ReadFile(s.paths.MaintenanceHold)
	if os.IsNotExist(err) {
		return MaintenanceHold{}, false, nil
	}
	if err != nil {
		return MaintenanceHold{}, false, fmt.Errorf("read maintenance hold: %w", err)
	}
	var hold MaintenanceHold
	if err := json.Unmarshal(b, &hold); err != nil {
		return MaintenanceHold{}, false, fmt.Errorf("decode maintenance hold: %w", err)
	}
	if hold.SchemaVersion != 1 {
		return MaintenanceHold{}, false, fmt.Errorf("unsupported maintenance hold schema %d", hold.SchemaVersion)
	}
	if !maintenanceHoldReasonPattern.MatchString(hold.Reason) {
		return MaintenanceHold{}, false, fmt.Errorf("invalid maintenance hold reason")
	}
	// 没有过期时刻的挂起会永久压制保护。宁可报错(fail-closed 且会在
	// bx status 上现形),也不接受一个没有尽头的压制。
	if hold.ExpiresAt.IsZero() {
		return MaintenanceHold{}, false, fmt.Errorf("maintenance hold has no expiry")
	}
	return hold, hold.ExpiresAt.After(now), nil
}

// ClearMaintenanceHold 撤销挂起。文件本来就不在不算失败(幂等)——它跑在
// 「用户明确要关保护」的路径上,而停止不许依赖先成功做成别的事。
func (s *Store) ClearMaintenanceHold() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paths.MaintenanceHold == "" {
		return nil
	}
	if err := os.Remove(s.paths.MaintenanceHold); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove maintenance hold: %w", err)
	}
	return nil
}
```

`internal/cli/uninstall_plan.go`:常量区(紧跟 `darwinUpgradeIntentPath`)加

```go
	// 维护挂起(guardian.Paths.MaintenanceHold 的默认路径)。卸载必须清掉:一张
	// 陈旧挂起会活过卸载重装,让新装的 Guardian 在 15 分钟内拒绝起 Core。
	darwinMaintenanceHoldPath = darwinDataDirPath + "/maintenance-hold.json"
```

并在 `buildDarwinUninstallPlan` 的 `RemovePaths` 里 `darwinUpgradeIntentPath` 之后加一行 `darwinMaintenanceHoldPath,`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/guardian ./internal/cli -run 'MaintenanceHold|Hold' -count=1`
Expected: PASS

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Expected: PASS(既有测试一条不改就该全绿 —— 本任务没有任何消费方)

- [ ] **Step 5: 变异验证**

逐个手工改坏、跑测试、确认转红、改回:
1. `LoadMaintenanceHold` 里 `hold.ExpiresAt.After(now)` → `true` ⇒ `TestMaintenanceHoldExpiresAtReadTime` 红。
2. `json.Unmarshal` 的错误分支 → `return MaintenanceHold{}, false, nil` ⇒ `TestUnreadableHoldIsAnErrorNotSilentlyUnarmed` 红。
3. 删掉 `hold.ExpiresAt.IsZero()` 那条 ⇒ `TestHoldWithoutExpiryIsRejectedOnRead` 红。
4. `ArmMaintenanceHold` 的目标路径改成 `s.paths.Desired` ⇒ `TestArmingHoldLeavesDesiredFileByteIdentical` 红。
5. `buildDarwinUninstallPlan` 删掉那一行 ⇒ 卸载计划测试红。

- [ ] **Step 6: 提交**

```bash
git add internal/guardian/hold.go internal/guardian/hold_test.go internal/guardian/types.go internal/guardian/store.go internal/cli/uninstall_plan.go internal/cli/uninstall_plan_test.go
git commit -m "$(cat <<'EOF'
feat(guardian): 维护挂起落盘,单独一个文件、读取时判过期

desired 是用户的意图,不该被后台操作改写。挂起单独住 maintenance-hold.json:
自带 schema_version、整文件原子写、15 分钟过期在读取时判定,绝不进
guardian-state.json(那是裸 JSON 字符串,旧 Guardian 读不懂新形状就会永久
recoveryBlocked)。卸载一并清掉,否则它活过卸载重装。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 意图快照(desired 与挂起同源读)+ Guardian 自发起 Core 的两条路尊重挂起

**为什么是同一个任务:** 挂起若从磁盘读、`desired` 从内存读,一轮之内就能出现两者互不相干的组合(设计取舍六;`needsAttention` 会把调用方传进来的常量写进 `status.Desired`,`manager.go:1396-1399`)。而这两条路正是「Guardian 自己决定起一个 Core」的全部入口 —— 守卫必须在任何东西武装挂起**之前**就位。

**Files:**
- Modify: `internal/guardian/hold.go`(加 `IntentSnapshot` 与 `LoadIntentSnapshot`)
- Modify: `internal/guardian/manager.go:56-59`(`DesiredStore` 加两个方法)、`:798-844`(`recoverLocked`)、`:1043-1098`(`handleUnexpectedExit`)
- Modify: `internal/guardian/manager_test.go:2011-2040`(`recordingDesiredStore` 必须**覆盖** `LoadIntentSnapshot`)
- Create: `internal/guardian/hold_manager_test.go`

**Interfaces:**
- Consumes: Task 1 的 `Store.LoadMaintenanceHold` / `Store.ClearMaintenanceHold` / `MaintenanceHold`。
- Produces:
  - `type guardian.IntentSnapshot struct { Desired DesiredState; Hold MaintenanceHold; HoldArmed bool }`
  - `func (s *guardian.Store) LoadIntentSnapshot(now time.Time) (IntentSnapshot, error)`
  - `DesiredStore` 新增 `LoadIntentSnapshot(time.Time) (IntentSnapshot, error)` 与 `ClearMaintenanceHold() error`
  - `func (m *Manager) clearMaintenanceHold()`(best-effort,只记日志)

- [ ] **Step 1: 写失败测试**

新建 `internal/guardian/hold_manager_test.go`:

```go
package guardian

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Core 退出**不是崩溃**,只要维护挂起还武装着:那是升级自己要求它停的。
// 此时既不许装屏障(升级正需要网络可用),也不许把它重启回来 ——
// forcedMacOSTeardown 与一个活着的 Guardian 赛跑,正是设计取舍二那张表。
func TestUnexpectedCoreExitUnderArmedHoldDoesNotRestartCore(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	startsBefore := env.runner.startCount()
	barriersBefore := env.barrier.installCount()
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	env.runner.killCurrent(errors.New("core exited")) // 触发 monitor → handleUnexpectedExit
	env.manager.waitIdle(t)

	if got := env.runner.startCount(); got != startsBefore {
		t.Fatalf("挂起期间 Core 被重启了:start = %d, want %d", got, startsBefore)
	}
	if got := env.barrier.installCount(); got != barriersBefore {
		t.Fatalf("挂起期间装了屏障(%d → %d):升级正需要网络可用", barriersBefore, got)
	}
}

// 过期的挂起不再压制任何东西 —— 半边输入空间的断言等于没断言。
func TestUnexpectedCoreExitWithExpiredHoldRestartsCore(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	startsBefore := env.runner.startCount()
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now().Add(-2*MaintenanceHoldDuration)); err != nil {
		t.Fatal(err)
	}
	env.runner.killCurrent(errors.New("core crashed"))
	env.manager.waitIdle(t)

	if got := env.runner.startCount(); got != startsBefore+1 {
		t.Fatalf("过期挂起仍在压制重启:start = %d, want %d", got, startsBefore+1)
	}
}

// 启动恢复也是「Guardian 自己发起 Core」的一条路,而且**每次升级都会走它**
// (restartGuardianForUpgrade 把 daemon 停掉再拉起来)。它同样必须认挂起。
//
// **并且绝不许把 Down 的路堵死。** recoverLocked 里 recoveryBlocked=true 会让
// Manager.Down 第一句就返回 errRecoveryIncomplete —— 2026-08-04 那次「用户 71
// 分钟关不掉保护」的机制。一次挂起不是恢复失败。
func TestStartupRecoveryUnderArmedHoldSkipsCoreAndKeepsDownReachable(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatalf("挂起不是恢复失败: %v", err)
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("挂起期间启动恢复起了 Core:start = %d", got)
	}
	if err := env.manager.Down(context.Background()); errors.Is(err, errRecoveryIncomplete) {
		t.Fatal("挂起把关闭的路堵死了 —— 停止永不许依赖别的先成功")
	}
}

// 问不出来 ≠ 没有挂起。读盘失败一律 fail-closed:不重启 Core。
func TestIntentSnapshotReadFailureDoesNotRestartCore(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	startsBefore := env.runner.startCount()
	env.store.setLoadError(errors.New("permission denied"))
	env.runner.killCurrent(errors.New("core exited"))
	env.manager.waitIdle(t)

	if got := env.runner.startCount(); got != startsBefore {
		t.Fatalf("读不出意图时仍重启了 Core:start = %d, want %d", got, startsBefore)
	}
	if got := env.manager.Status().LastError; got == "" {
		t.Fatal("读不出意图必须留下失败码")
	}
}
```

> 实现者注意:`env.runner.killCurrent`、`env.runner.startCount`、`env.barrier.installCount`、`env.manager.waitIdle` 若在 `manager_test.go` 里叫别的名字,**用既有的那个**,不要新造平行设施;`newManagerTestEnv` 已在 `internal/guardian/manager_test.go` 里。`env.store` 是 `*recordingDesiredStore`,内嵌 `*Store`,故 `ArmMaintenanceHold` 直接可用 —— 但它的 `Paths` 里还没有 `MaintenanceHold`,**先在 `newManagerTestEnv` 里补上** `MaintenanceHold: filepath.Join(t.TempDir(), "maintenance-hold.json")`。

**每条断言由哪一处生产改动供养:**
- `…UnderArmedHoldDoesNotRestartCore` ← `handleUnexpectedExit` 里新增的 `if intent.HoldArmed { m.current = Process{}; return }`。删掉它 → 红。
- `…WithExpiredHoldRestartsCore` ← 同一分支用的是 `intent.HoldArmed`(经 Task 1 的过期判定)而不是「文件存在」。改成「文件存在即停手」→ 红。
- `…StartupRecovery…KeepsDownReachable` ← `recoverLocked` 里挂起分支的 `m.recoveryBlocked = false` 与 `return nil`。把它写成 `m.recoveryBlocked = true` 或 `return err` → 红。
- `TestIntentSnapshotReadFailureDoesNotRestartCore` ← `handleUnexpectedExit` 读快照失败时的 `return`。改成「读失败就按 desired=on 继续」→ 红。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian -run 'Hold|IntentSnapshot' -count=1`
Expected: 编译失败(`LoadIntentSnapshot` 未定义),补上类型后断言失败(Core 被重启)。

- [ ] **Step 3: 最小实现**

`internal/guardian/hold.go` 追加:

```go
// IntentSnapshot 是**一次**磁盘读拿到的完整意图:用户要什么,以及此刻允不允许动手。
//
// 为什么必须同源:调谐器今天读的是内存里的 m.Status().Desired,而内存**已经会
// 撒谎** —— needsAttention 会把调用方传进来的常量写进 status.Desired
// (manager.go:1396-1399),好几处传的是字面量 DesiredOn 而磁盘写着 off。挂起从
// 磁盘读、desired 从内存读,一轮之内就能出现两者互不相干的组合。
//
// **它是一个瞬间的快照,不是一段区间内的保证**:CLI 在另一个进程里随时可能改写
// 这两个文件,本期不引入锁(整文件原子替换 + 最后写者赢)。
type IntentSnapshot struct {
	Desired   DesiredState
	Hold      MaintenanceHold
	HoldArmed bool
}

func (s *Store) LoadIntentSnapshot(now time.Time) (IntentSnapshot, error) {
	desired, err := s.LoadDesired()
	if err != nil {
		return IntentSnapshot{}, err
	}
	hold, armed, err := s.LoadMaintenanceHold(now)
	if err != nil {
		return IntentSnapshot{}, err
	}
	return IntentSnapshot{Desired: desired, Hold: hold, HoldArmed: armed}, nil
}
```

`internal/guardian/manager.go` 的 `DesiredStore`:

```go
type DesiredStore interface {
	LoadDesired() (DesiredState, error)
	SaveDesired(DesiredState) error
	// LoadIntentSnapshot 一次读出 desired 与挂起。**加进接口而不是做成可选接口**:
	// 可选接口在没实现时会安静地退化成「没有挂起」,而那正是本期要消灭的谎言的
	// 形状;放进接口则由编译器点名每一个实现。
	LoadIntentSnapshot(time.Time) (IntentSnapshot, error)
	// ClearMaintenanceHold 由用户显式的 up/down 调用(设计取舍四)。
	ClearMaintenanceHold() error
}
```

`handleUnexpectedExit` 里把 `manager.go:1067-1078` 换成:

```go
	intent, err := m.store.LoadIntentSnapshot(time.Now())
	if err != nil {
		if !m.barrierProven() {
			_ = m.installBarrierForRecovery(operationCtx, m.runtime)
		}
		m.needsAttention(DesiredOn, "desired_state_read_failed")
		return
	}
	// 维护挂起武装着 ⇒ 这次退出是**别人要求的**,不是崩溃。
	//
	// 既不装屏障也不重启:升级正在换二进制,而屏障是覆盖整个公网的 /2 reject
	// 路由 —— 在一台正要去装文件的机器上装它,等于把修复通道自己掐断。
	// desired 此刻仍然写着 on(这正是本期的全部意义),所以不看挂起就一定会重启。
	if intent.HoldArmed {
		log.Printf("guardian_core_exit_under_hold reason=%s expires_at=%s",
			intent.Hold.Reason, intent.Hold.ExpiresAt.Format(time.RFC3339))
		m.current = Process{}
		return
	}
	if intent.Desired != DesiredOn {
		m.current = Process{}
		return
	}
```

`recoverLocked` 里把 `manager.go:807-813` 的 `desired, err := m.store.LoadDesired()` 换成快照读,并在 `if desired == DesiredOff` **之前**插入挂起分支:

```go
	intent, err := m.store.LoadIntentSnapshot(time.Now())
	if err != nil {
		m.needsAttention(DesiredOn, "desired_state_read_failed")
		m.recoveryBlocked = true
		return err
	}
	desired := intent.Desired
	// 维护挂起:此刻不该有保护,而这不是一次失败的恢复。
	//
	// **每一次升级都会走到这里** —— restartGuardianForUpgrade 把 daemon 停掉再拉
	// 起来,新 Guardian 起手就跑启动恢复;desired 保持 on 之后,不认挂起就等于在
	// 二进制刚换完、CLI 还没走到「恢复保护」那一步时自己先把 Core 起来了。
	//
	// recoveryBlocked **必须置 false**:它为真时 Manager.Down 第一句就返回
	// errRecoveryIncomplete —— 2026-08-04 那次 71 分钟事故的机制。一次挂起绝不
	// 允许把关闭的路堵死。
	if intent.HoldArmed {
		m.setStatus(Status{SchemaVersion: 1, Desired: desired, Phase: PhaseIdle, Protection: ProtectionOff})
		m.recoveryBlocked = false
		log.Printf("guardian_startup_recovery_held reason=%s expires_at=%s",
			intent.Hold.Reason, intent.Hold.ExpiresAt.Format(time.RFC3339))
		return nil
	}
```

Manager 上加(放在 `forgetUpgradeIntent` 旁边):

```go
// clearMaintenanceHold 撤销挂起。**只记日志,绝不让调用方失败**:它跑在
// up/down 这两条路上,而停止不许依赖先成功做成别的事。
func (m *Manager) clearMaintenanceHold() {
	if err := m.store.ClearMaintenanceHold(); err != nil {
		log.Printf("guardian_maintenance_hold_clear_failed err=%v", err)
	}
}
```

`manager_test.go` 的 `recordingDesiredStore` **必须覆盖** `LoadIntentSnapshot`,否则内嵌的 `*Store` 会把注入的读错误绕过去(一个天然的假绿):

```go
// 必须覆盖:内嵌 *Store 的 LoadIntentSnapshot 会直接读盘,把 setLoadError
// 注入的错误整个绕过去 —— 那正是「测试测的是相邻的东西」的经典形状。
func (s *recordingDesiredStore) LoadIntentSnapshot(now time.Time) (IntentSnapshot, error) {
	desired, err := s.LoadDesired() // 走本类型的注入逻辑
	if err != nil {
		return IntentSnapshot{}, err
	}
	hold, armed, err := s.Store.LoadMaintenanceHold(now)
	if err != nil {
		return IntentSnapshot{}, err
	}
	return IntentSnapshot{Desired: desired, Hold: hold, HoldArmed: armed}, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/guardian -count=1` 与 `go test -race ./internal/guardian -count=1`
Expected: PASS(既有测试一条断言都不该改;要改说明改动越界了)

- [ ] **Step 5: 变异验证**

1. 删掉 `handleUnexpectedExit` 的 `intent.HoldArmed` 分支 ⇒ `TestUnexpectedCoreExitUnderArmedHoldDoesNotRestartCore` 红。
2. 把 `recoverLocked` 挂起分支里的 `m.recoveryBlocked = false` 改成 `true` ⇒ `…KeepsDownReachable` 红。
3. 把 `recordingDesiredStore.LoadIntentSnapshot` 删掉(让它退化成内嵌实现)⇒ `TestIntentSnapshotReadFailureDoesNotRestartCore` 红。**这一条要特别做**:它证明那个覆盖不是装饰。
4. 把 `LoadIntentSnapshot` 里 `LoadMaintenanceHold` 的错误 `return nil` 掉 ⇒ 同上转红。

- [ ] **Step 6: 提交**

```bash
git add internal/guardian/hold.go internal/guardian/manager.go internal/guardian/manager_test.go internal/guardian/hold_manager_test.go
git commit -m "$(cat <<'EOF'
feat(guardian): 自发起 Core 的两条路认维护挂起,意图与挂起同源读盘

handleUnexpectedExit 与启动恢复是 Guardian 自己决定起 Core 的全部入口:挂起
武装时都停手,且启动恢复绝不因此置 recoveryBlocked(那会把 Down 永久堵死)。
desired 与挂起由 LoadIntentSnapshot 一次读出——内存里的 Desired 会被
needsAttention 写成与磁盘无关的值,两处分别读就能凑出互不相干的组合。

此刻还没有任何东西会武装挂起,故现存机器行为不变:守卫先于依赖它的改动落地。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 调谐器把挂起当第四道栅栏,并改从磁盘读 Desired

**Files:**
- Modify: `internal/guardian/reconcile.go:55-76,87-150`
- Modify: `internal/guardian/reconcile_loop.go:93-118`
- Modify/Create: `internal/guardian/reconcile_test.go`(既有)、`internal/guardian/reconcile_hold_test.go`(新)

**Interfaces:**
- Consumes: `IntentSnapshot`、`Store.LoadIntentSnapshot`(Task 2)。
- Produces:`reconcileInput.MaintenanceHold bool`、`reconcileInput.IntentUnreadable bool`、`heldMaintenanceHold = "maintenance_hold"`、`heldIntentUnreadable = "intent_unreadable"`。

- [ ] **Step 1: 写失败测试**

新建 `internal/guardian/reconcile_hold_test.go`:

```go
package guardian

import (
	"context"
	"testing"
	"time"

	"github.com/getbx/bx/internal/observe"
)

// 挂起是**栅栏**,不是一处待收敛的差异:整轮停摆,并说清是被哪一道挡住的。
func TestDecideHeldByMaintenanceHold(t *testing.T) {
	in := reconcileInput{
		Desired:         DesiredOff,
		MaintenanceHold: true,
		Observed: observe.ObservedState{
			CoreSocket: observe.True, BarrierPresent: observe.True, DNSManaged: observe.True,
		},
	}
	got := decide(in)
	if got.Held != heldMaintenanceHold {
		t.Fatalf("Held = %q, want %q", got.Held, heldMaintenanceHold)
	}
	if len(got.Actions) != 0 {
		t.Fatalf("被栅栏挡住的一轮不许提议动作: %v", got.Actions)
	}
}

// 「读不出意图」不是「没有挂起」,也不是「desired 是 off」——它有自己的名字。
func TestDecideHeldWhenIntentUnreadable(t *testing.T) {
	got := decide(reconcileInput{IntentUnreadable: true, Observed: observe.ObservedState{CoreSocket: observe.False}})
	if got.Held != heldIntentUnreadable || len(got.Actions) != 0 {
		t.Fatalf("decide = %+v", got)
	}
}

// 栅栏次序必须确定:同时升起时日志里出现哪一个不能随机。
func TestMaintenanceHoldRanksAfterTheThreeExistingFences(t *testing.T) {
	base := reconcileInput{Desired: DesiredOn, MaintenanceHold: true}
	base.RecoveryBlocked = true
	if got := heldBy(base); got != heldRecoveryBlocked {
		t.Fatalf("recovery_blocked 优先:%q", got)
	}
	base.RecoveryBlocked, base.PathRecoveryBusy = false, true
	if got := heldBy(base); got != heldPathRecoveryBusy {
		t.Fatalf("path_recovery 优先:%q", got)
	}
	base.PathRecoveryBusy, base.OwnershipUncertain = false, true
	if got := heldBy(base); got != heldOwnershipUncertain {
		t.Fatalf("ownership 优先:%q", got)
	}
	base.OwnershipUncertain = false
	if got := heldBy(base); got != heldMaintenanceHold {
		t.Fatalf("最后才是挂起:%q", got)
	}
}

// 调谐器读的必须是**磁盘**,不是被 needsAttention 污染过的内存。
func TestReconcileOnceReadsDesiredFromDiskNotMemory(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	// 让内存说 on(needsAttention 把调用方传进来的常量原样写进 status.Desired)
	env.manager.needsAttention(DesiredOn, "core_unexpected_exit")

	got := env.manager.reconcileOnce(context.Background(), observe.ObservedState{
		CoreSocket: observe.True, ObservedAt: time.Now(),
	})
	if len(got.Actions) == 0 || got.Actions[0] != actionStopCore {
		t.Fatalf("盘上是 off、Core 还在跑,应提议 stop_core;实际 = %+v", got)
	}
}

// 挂起武装时,连「读栅栏」这一轮也直接停摆。
func TestReconcileOnceHeldByArmedHold(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	got := env.manager.reconcileOnce(context.Background(), observe.ObservedState{
		CoreSocket: observe.False, ObservedAt: time.Now(),
	})
	if got.Held != heldMaintenanceHold {
		t.Fatalf("Held = %q, want %q(否则挂起期间会提议 start_core)", got.Held, heldMaintenanceHold)
	}
}
```

**每条断言由哪一处生产改动供养:**
- `TestDecideHeldByMaintenanceHold` ← `heldBy` 里新增的 `case in.MaintenanceHold`。删掉 → 变成三条 Actions,红。
- `TestDecideHeldWhenIntentUnreadable` ← `heldBy` 里 `case in.IntentUnreadable`。删掉 → `Held==""`,红。
- `TestMaintenanceHoldRanksAfter…` ← `heldBy` 里 `switch` 的分支次序。把挂起挪到最前 → 红。
- `TestReconcileOnceReadsDesiredFromDiskNotMemory` ← `reconcileOnce` 里 `m.store.LoadIntentSnapshot(...)` 取代 `m.Status().Desired`。改回内存读 → 内存说 on、CoreSocket True ⇒ 无动作,红。
- `TestReconcileOnceHeldByArmedHold` ← `reconcileOnce` 把 `intent.HoldArmed` 填进 `input.MaintenanceHold`。忘了填 → 提议 `start_core`,红。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian -run 'Reconcile|Decide' -count=1`
Expected: 编译失败(`heldMaintenanceHold` 未定义)。

- [ ] **Step 3: 最小实现**

`reconcile.go`:

```go
type reconcileInput struct {
	Desired  DesiredState
	Observed observe.ObservedState
	// 栅栏:任一为真即本轮什么都不做。分开列是为了日志能说清是哪一道。
	RecoveryBlocked    bool
	PathRecoveryBusy   bool
	OwnershipUncertain bool
	// MaintenanceHold 是**第四道**栅栏:有人(升级)明确声明了「此刻不该有人
	// 动手」。它与前三道同类 —— 整轮停摆,而不是一处待收敛的差异。
	MaintenanceHold bool
	// IntentUnreadable:desired 或挂起读不出来。**不许塌缩成任何一个具体答案** ——
	// 按 off 收敛会去停一个用户要的 Core,按 on 收敛会在挂起期间起一个不该起的。
	IntentUnreadable bool
}

const (
	heldRecoveryBlocked    = "recovery_blocked"
	heldPathRecoveryBusy   = "path_recovery_in_flight"
	heldOwnershipUncertain = "ownership_uncertain"
	heldMaintenanceHold    = "maintenance_hold"
	heldIntentUnreadable   = "intent_unreadable"
)

func heldBy(in reconcileInput) string {
	switch {
	case in.RecoveryBlocked:
		return heldRecoveryBlocked
	case in.PathRecoveryBusy:
		return heldPathRecoveryBusy
	case in.OwnershipUncertain:
		return heldOwnershipUncertain
	case in.MaintenanceHold:
		return heldMaintenanceHold
	case in.IntentUnreadable:
		return heldIntentUnreadable
	default:
		return ""
	}
}
```

`reconcile_loop.go` 的 `reconcileOnce`:

```go
func (m *Manager) reconcileOnce(ctx context.Context, observed observe.ObservedState) reconcileDecision {
	input := reconcileInput{
		Observed:         observed,
		PathRecoveryBusy: m.pathRecoveryBusy(),
	}
	// **从磁盘读,不读 m.Status().Desired。** 内存里的 Desired 会被 needsAttention
	// 写成调用方传进来的常量(manager.go:1396-1399),好几处传的是字面量 DesiredOn
	// 而磁盘写着 off;而挂起只在磁盘上。两者必须是同一次读出来的。
	intent, err := m.store.LoadIntentSnapshot(time.Now())
	switch {
	case err != nil:
		input.IntentUnreadable = true
	default:
		input.Desired = intent.Desired
		input.MaintenanceHold = intent.HoldArmed
	}
	if input.PathRecoveryBusy || input.MaintenanceHold || input.IntentUnreadable {
		// 这几道栅栏不用抢 mutation channel 就读得到。先看它们:一轮注定要跳过
		// 的判断没有任何理由排在用户的 up 前面。
		return decide(input)
	}
	blocked, uncertain, acquired := m.readMutationFences(ctx)
	if !acquired {
		return reconcileDecision{Held: heldMutationBusy}
	}
	input.RecoveryBlocked = blocked
	input.OwnershipUncertain = uncertain
	return decide(input)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/guardian -count=1`
Expected: PASS。既有的 `reconcile_test.go` / `reconcile_quality_test.go` 里若有测试直接构造 `Manager` 而 store 没有 `MaintenanceHold` 路径,`LoadIntentSnapshot` 会走 `paths.MaintenanceHold == ""` 的分支返回「没有挂起」,行为不变。

- [ ] **Step 5: 变异验证**

1. `heldBy` 删掉 `case in.MaintenanceHold` ⇒ `TestDecideHeldByMaintenanceHold`、`TestReconcileOnceHeldByArmedHold` 红。
2. `reconcileOnce` 改回 `Desired: m.Status().Desired` ⇒ `TestReconcileOnceReadsDesiredFromDiskNotMemory` 红。
3. `reconcileOnce` 把 `err != nil` 那一支改成「当作 desired=off 继续」⇒ `TestDecideHeldWhenIntentUnreadable` 仍绿但 `TestReconcileOnce…` 系列不覆盖 —— **补一条**:store 注入读错误后 `reconcileOnce` 必须返回 `heldIntentUnreadable`,并确认这条变异让它红。

- [ ] **Step 6: 提交**

```bash
git add internal/guardian/reconcile.go internal/guardian/reconcile_loop.go internal/guardian/reconcile_hold_test.go
git commit -m "$(cat <<'EOF'
feat(guardian): 调谐器把维护挂起当第四道栅栏,Desired 改从磁盘读

挂起说的是「此刻不该有人动手」,与 recoveryBlocked/路径恢复/所有权不确定同类:
整轮停摆并报出是哪一道。顺带修掉调谐器读内存 Desired 那个既有问题——
needsAttention 会把常量写进 status.Desired,与磁盘无关。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: 升级停机改用挂起 —— 两个写入点一起改,退回规则与无条件销挂起

**这是唯一翻转行为的任务。** 它安全,只因为 Task 2/3 已经落地。

**Files:**
- Modify: `internal/guardian/upgradeintent.go`(改名为挂起词汇,**保留** `?reason=upgrade` 这个线上字面量)
- Modify: `internal/guardian/manager.go:591-620`(`Down` 的 defer 与 maintenance 判定)、`:710-713`(跳过 `SaveDesired(DesiredOff)`)、`:518-540`(`upLocked` 清挂起)、`:454-464`(`Migrate` 清挂起)
- Modify: `internal/cli/guardian.go:108-183`(deps 加两个钩子)、`:190-232`(默认接线)、`:461-509`(purpose 贯穿)、`:593-664`(`forcedMacOSTeardown`)
- Create: `internal/cli/hold_teardown_test.go`
- Modify: `internal/guardian/manager_test.go` / `localapi_test.go`(新增用例)

**Interfaces:**
- Consumes: Task 1 的 `Store.ArmMaintenanceHold/ClearMaintenanceHold`、`guardian.HoldReasonUpgrade`;Task 2 的 `Manager.clearMaintenanceHold`。
- Produces:
  - `macOSLifecycleDeps.armMaintenanceHold func(reason string) error`
  - `macOSLifecycleDeps.clearMaintenanceHold func() error`
  - `type stopIntent struct { purpose downPurpose; fellBack bool; err error }`
  - `func recordStopIntent(deps macOSLifecycleDeps, purpose downPurpose) stopIntent`
  - `func forcedMacOSTeardown(ctx context.Context, stop stopIntent, deps macOSLifecycleDeps, cause error) error`(签名变了,三处调用点都要改)
  - `guardian.downClearsMaintenanceHold(ctx) bool`(原 `downClearsUpgradeIntent`)

- [ ] **Step 1: 写失败测试**

新建 `internal/cli/hold_teardown_test.go`:

```go
package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/getbx/bx/internal/guardian"
)

type teardownCalls struct {
	armed        []string
	cleared      int
	desiredOff   int
	stopCore     int
	forceTeardown int
	barrier      int
	dns          int
}

func teardownDeps(calls *teardownCalls, armErr, stopErr, dnsErr error) macOSLifecycleDeps {
	return macOSLifecycleDeps{
		guardianReady: func(context.Context) bool { return false }, // 直奔强制拆除
		armMaintenanceHold: func(reason string) error {
			calls.armed = append(calls.armed, reason)
			return armErr
		},
		clearMaintenanceHold: func() error { calls.cleared++; return nil },
		markDesiredOff:       func() error { calls.desiredOff++; return nil },
		stopCore:             func(context.Context) error { calls.stopCore++; return stopErr },
		forceTeardown:        func(context.Context) error { calls.forceTeardown++; return nil },
		clearBarrierRoutes:   func(context.Context) error { calls.barrier++; return nil },
		restoreSystemDNS:     func(context.Context) error { calls.dns++; return dnsErr },
	}
}

// 升级的停机不再写 desired=off —— 磁盘上那句「用户不想要保护」正是要消灭的谎话。
func TestForcedTeardownForUpgradeArmsHoldInsteadOfWritingDesiredOff(t *testing.T) {
	calls := &teardownCalls{}
	if _, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgrade, "/etc/bx/config.yaml", teardownDeps(calls, nil, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if len(calls.armed) == 0 || calls.armed[0] != guardian.HoldReasonUpgrade {
		t.Fatalf("没有武装挂起: %v", calls.armed)
	}
	if calls.desiredOff != 0 {
		t.Fatalf("升级路径写了 %d 次 desired=off", calls.desiredOff)
	}
}

// **设计取舍三,逃生路径的不变量**:挂起写失败时退回写 desired=off,
// 而且六步破坏性动作照常全做完 —— 停止永不依赖别的先成功。
func TestForcedTeardownFallsBackToDesiredOffWhenHoldWriteFailsAndStillTearsDown(t *testing.T) {
	calls := &teardownCalls{}
	deps := teardownDeps(calls, errors.New("read-only file system"), nil, nil)
	if _, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgrade, "/etc/bx/config.yaml", deps); err != nil {
		t.Fatalf("挂起写失败不该让拆除整个失败: %v", err)
	}
	if calls.desiredOff == 0 {
		t.Fatal("挂起写失败必须退回 desired=off:既没挂起也没 off 是一个新的失效模式")
	}
	if calls.stopCore == 0 || calls.forceTeardown == 0 || calls.barrier == 0 || calls.dns == 0 {
		t.Fatalf("破坏性步骤没做完: %+v", calls)
	}
}

// **欠条那个活 bug 的回归**:用户明确要关,清挂起对拆除的成败必须无条件 ——
// forcedMacOSTeardown 即使报告失败,六步也已经做完了(upgradeplan.go:110-118)。
func TestForcedTeardownForUserClearsHoldEvenWhenStepsFail(t *testing.T) {
	calls := &teardownCalls{}
	deps := teardownDeps(calls, nil, errors.New("core unreachable"), errors.New("dns restore timed out"))
	if _, err := macOSDownLifecycleFor(context.Background(), downPurposeUser, "/etc/bx/config.yaml", deps); err == nil {
		t.Fatal("这一轮本该报告失败")
	}
	if calls.cleared == 0 {
		t.Fatal("拆除报错就跳过销挂起 —— 正是 upgrade-intent.json 今天留下陈旧记录的原因")
	}
	if calls.desiredOff == 0 {
		t.Fatal("用户显式关闭仍要写 desired=off")
	}
}
```

`internal/guardian` 侧(放进 `hold_manager_test.go`):

```go
// 升级自己的那次停保护经 POST /v1/down?reason=upgrade 进来,**不许改写 desired**。
func TestMaintenanceDownLeavesDesiredOnAndKeepsHoldArmed(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Down(withMaintenanceStop(context.Background())); err != nil {
		t.Fatal(err)
	}
	desired, err := env.store.LoadDesired()
	if err != nil || desired != DesiredOn {
		t.Fatalf("升级的停机改写了 desired:%q err=%v", desired, err)
	}
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || !armed {
		t.Fatalf("升级自己的那次 Down 把挂起销了:armed=%v err=%v", armed, err)
	}
}

// 用户显式的 down 清挂起,**且与 Down 的成败无关**。
func TestUserDownClearsHoldEvenWhenDownFails(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	env.dns.failRestore(errors.New("networksetup timed out")) // 让 Down 走失败分支
	_ = env.manager.Down(context.Background())
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("Down 失败就不销挂起:armed=%v err=%v", armed, err)
	}
}

// 用户显式的 up 同样清挂起(设计取舍四:显式动作永远压过挂起)。
func TestUpClearsMaintenanceHold(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("bx up 之后挂起还武装着:armed=%v err=%v", armed, err)
	}
}
```

**每条断言由哪一处生产改动供养:**
- `…ArmsHoldInsteadOfWritingDesiredOff` ← `recordStopIntent` + `forcedMacOSTeardown` 里按 purpose 分岔的第 1/6 步。把 `persistDesiredOff` 留在升级分支 → 红。
- `…FallsBackToDesiredOffWhenHoldWriteFails…` ← `recordStopIntent` 里 arm 失败时的 `persistDesiredOff` 退回,以及「继续往下做完」。改成 `return err` → 红(两个断言各红一半)。
- `…ForUserClearsHoldEvenWhenStepsFail` ← 清挂起那一步排在 `failures` 收集里、不在任何 `if err == nil` 后面。把它挪到 `if len(failures)==0` 里 → 红。
- `TestMaintenanceDownLeavesDesiredOnAndKeepsHoldArmed` ← `Manager.Down` 里 `if !maintenance { SaveDesired(DesiredOff) }` 与 defer 里的 `if !maintenance`。任一去掉 → 红。
- `TestUserDownClearsHoldEvenWhenDownFails` ← defer 里**去掉了** `err == nil` 这个条件。加回来 → 红。
- `TestUpClearsMaintenanceHold` ← `upLocked` 里的 `m.clearMaintenanceHold()`。删掉 → 红。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli -run TestForcedTeardown -count=1`;`go test ./internal/guardian -run 'MaintenanceDown|UserDownClears|UpClears' -count=1`
Expected: 编译失败(`armMaintenanceHold` / `withMaintenanceStop` 未定义)。

- [ ] **Step 3: 最小实现**

**(a) `internal/guardian/upgradeintent.go`** —— 改词汇、**不改线上字面量**;`downClearsUpgradeIntent` 整个删掉,欠条与挂起从此共用一个判据 `downIsMaintenance`(Task 6 会把欠条那半边一起删):

```go
// 维护挂起的销账规则:哪一种 Down 算「用户不要保护了」。
//
// **线上的 `?reason=upgrade` 刻意原样保留。** 设计正文说这个参数会「一起消失」,
// 但取舍三与取舍四都要求它继续存在,只是它现在守的是**两件**事而不是欠条:
//   - 升级自己的那次停保护**不改写 desired**(否则干净路径这个写入点照旧撒谎,
//     而调谐器会忠实地把机器收敛到 off ——「漏一个等于没修」);
//   - 升级自己的那次停保护**不销挂起**(它前一秒才武装)。
// 而用户明确的 off 两件都做。判据必须是请求作用域的:靠「写 → 停 → 再写一遍」
// 补救会留下一个真实的崩溃窗口。
//
// 字面量不改还有第二个理由:升级窗口里新旧两版共存。新 CLI 对旧 Guardian 发
// 未知的 reason,旧 Guardian 会按「用户说 off」处理 —— 也就是今天的行为,安全;
// 换一个新词则两个方向都要额外论证。
const (
	downReasonParam   = "reason"
	downReasonUpgrade = "upgrade"
)

const downForUpgradePath = "/v1/down?" + downReasonParam + "=" + downReasonUpgrade

type maintenanceStopKey struct{}

func withMaintenanceStop(ctx context.Context) context.Context {
	return context.WithValue(ctx, maintenanceStopKey{}, true)
}

// downIsMaintenance 报告这次 Down 是不是维护自己的一步。
// 没有标记就不是 —— 拼错一个查询参数的代价应该是「多销一次挂起」。
func downIsMaintenance(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(maintenanceStopKey{}).(bool)
	return marked
}

func markMaintenanceStop(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get(downReasonParam) == downReasonUpgrade {
			r = r.WithContext(withMaintenanceStop(r.Context()))
		}
		next(w, r)
	}
}
```

`localapi.go:123` 的 `markUpgradeStop` 改成 `markMaintenanceStop`。

**(b) `Manager.Down`(`manager.go:591` 起)**:

```go
func (m *Manager) Down(ctx context.Context) (err error) {
	m.beginPathRecoveryTransition(pathRecoveryTransitionResolveOff)
	defer m.endPathRecoveryTransition()
	if err = m.acquireMutation(ctx); err != nil {
		return err
	}
	defer m.releaseMutation()
	// 一次 map 读,越早越好:关闭是逃生路径,不得因为任何记账逻辑失败或阻塞。
	maintenance := downIsMaintenance(ctx)
	// 用户明确要关 ⇒ 挂起必须消失,**且与这次 Down 的成败无关**。
	//
	// 这一条是从欠条那个活 bug 学来的:forcedMacOSTeardown 即使报告失败,六步
	// 破坏性动作也已经做完了,而躲在 `err == nil` 后面的销账于是留下一条陈旧
	// 记录,下一次 app-install 拿它**违背用户明确的关闭请求**把保护打开。
	defer func() {
		if !maintenance {
			m.clearMaintenanceHold()
			// 欠条那半边照旧只在成功后销(行为不变),Task 6 会连同它一起删掉。
			if err == nil {
				m.forgetUpgradeIntent()
			}
		}
	}()
	...
```

并把 `:710` 改成:

```go
	// 维护停机**不改写 desired**:用户的意图没有变,变的只是「此刻不能有」。
	// 那件事由挂起表达(CLI 在停机之前已经武装好了)。
	if !maintenance {
		if err := m.store.SaveDesired(DesiredOff); err != nil {
			m.needsAttention(desired, "desired_state_write_failed")
			return fmt.Errorf("persist disabled state behind barrier: %w", err)
		}
	}
```

**(c) `upLocked` 与 `Migrate`**:在各自写 `SaveDesired(DesiredOn)` 的那一段之后加一行

```go
	// 用户显式的动作永远压过挂起(设计取舍四)。放在这里而不是调用方:
	// 菜单的 Turn On 走 socket,一行 CLI 都不经过。
	m.clearMaintenanceHold()
```

**(d) `internal/cli/guardian.go`** —— deps 与默认接线:

```go
	// armMaintenanceHold 武装一次维护挂起(见 guardian.MaintenanceHold)。
	// 升级用它取代「写 desired=off」:磁盘上那句「用户不想要保护」是假的。
	armMaintenanceHold func(reason string) error
	// clearMaintenanceHold 撤销挂起。它跑在用户显式关闭的路上,**对拆除的成败
	// 无条件**。
	clearMaintenanceHold func() error
```

```go
		armMaintenanceHold: func(reason string) error {
			return guardian.OpenDefaultStore().ArmMaintenanceHold(reason, time.Now())
		},
		clearMaintenanceHold: func() error { return guardian.OpenDefaultStore().ClearMaintenanceHold() },
```

**(e) purpose 贯穿 + 拆除**:

```go
// stopIntent 是这次停机在盘上留下的那条记录。
//
// **升级的挂起必须在停 Core 之前武装好,而且干净路径与强制路径都要**:干净路径
// 走完 Manager.Down 之后 CLI 紧接着重启 Guardian(restartGuardianForUpgrade),
// 新 Guardian 的启动恢复读到 desired=on 就会把 Core 起回来 —— 二进制正换到一半。
type stopIntent struct {
	purpose  downPurpose
	fellBack bool  // 挂起写不成,已退回 desired=off
	err      error // 退回之后仍失败(两条都没写成),留给拆除步骤汇报
}

func recordStopIntent(deps macOSLifecycleDeps, purpose downPurpose) stopIntent {
	if purpose != downPurposeUpgrade {
		return stopIntent{purpose: purpose}
	}
	if err := armMaintenanceHold(deps, guardian.HoldReasonUpgrade); err != nil {
		// **退回今天的行为,而不是报错拒绝、也不是继续往下。**
		// 退回意味着调谐器会把机器收敛到 off(与今天一模一样,升级结束会重新
		// 写 on),代价已知且有界;而「既没挂起也没 desired=off」是一个新的
		// 失效模式:活着的 Guardian 在换二进制时把 Core 重启回来。
		// 宁可退回一个会撒谎但安全的状态,也不要一个诚实但没人拦着的状态。
		return stopIntent{purpose: purpose, fellBack: true, err: persistDesiredOff(deps)}
	}
	return stopIntent{purpose: purpose}
}

func armMaintenanceHold(deps macOSLifecycleDeps, reason string) error {
	if deps.armMaintenanceHold == nil {
		return fmt.Errorf("维护挂起在此平台不可用")
	}
	return deps.armMaintenanceHold(reason)
}

func clearMaintenanceHold(deps macOSLifecycleDeps) error {
	if deps.clearMaintenanceHold == nil {
		return nil
	}
	return deps.clearMaintenanceHold()
}
```

`macOSDownLifecycleFor` 开头加 `stop := recordStopIntent(deps, purpose)`,三处 `forcedMacOSTeardown(ctx, deps, X)` 改成 `forcedMacOSTeardown(ctx, stop, deps, X)`。

`forcedMacOSTeardown` 的第 1 步与第 6 步:

```go
func forcedMacOSTeardown(ctx context.Context, stop stopIntent, deps macOSLifecycleDeps, cause error) error {
	if deps.forceTeardown == nil {
		return fmt.Errorf("Guardian 无法正常关闭,且强制拆除功能在此平台不可用")
	}
	var failures []error
	// 1. 先把意图记下来,再动任何东西。一个还活着的 Guardian 会把 Core 的退出
	//    当成崩溃并重启它 —— 与下面每一步赛跑。
	//    维护(升级)记的是挂起,用户记的是 desired=off;两者都由 recordStopIntent
	//    在更早的时候做过一次,这里再做一次是为了盖住「一次并发的 Up 抢在前面」。
	desiredErr := stop.err
	switch {
	case stop.purpose == downPurposeUpgrade && !stop.fellBack:
		if err := armMaintenanceHold(deps, guardian.HoldReasonUpgrade); err != nil {
			failures = append(failures, fmt.Errorf("刷新维护挂起: %w", err))
		}
	default:
		if err := persistDesiredOff(deps); err != nil && desiredErr == nil {
			desiredErr = err
		} else if err == nil {
			desiredErr = nil
		}
	}
	// 用户明确要关 ⇒ 销挂起。**这一步对拆除的成败无条件**(见 Manager.Down 的
	// 同款注释):强制拆除即使报告失败,六步破坏性动作也已经做完了。
	if stop.purpose != downPurposeUpgrade {
		if err := clearMaintenanceHold(deps); err != nil {
			failures = append(failures, fmt.Errorf("清除维护挂起: %w", err))
		}
	}
	// 2..5 原样不动
	...
	// 6. 再记一次。Guardian 已经被 bootout,此刻的写入是权威的。
	switch {
	case stop.purpose == downPurposeUpgrade && !stop.fellBack:
		if err := armMaintenanceHold(deps, guardian.HoldReasonUpgrade); err != nil {
			failures = append(failures, fmt.Errorf("刷新维护挂起: %w", err))
		}
	default:
		if err := persistDesiredOff(deps); err == nil {
			desiredErr = nil
		} else if desiredErr != nil {
			desiredErr = errors.Join(desiredErr, err)
		}
	}
	...
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cli ./internal/guardian -count=1`
Expected: PASS。既有的 `upgrade_guardian_e2e_test.go` 与 `guardian_test.go` 里凡是断言「升级路径写了 desired=off」的用例**必须改**,并在提交说明里逐条列出改了什么、为什么(这正是本任务翻转的语义)。

Run: `go test -race ./internal/guardian ./internal/cli -count=1`

- [ ] **Step 5: 变异验证**

1. `recordStopIntent` 升级分支的 arm 失败 → 直接 `return stopIntent{purpose: purpose}`(不退回)⇒ `…FallsBackToDesiredOff…` 红。
2. `recordStopIntent` arm 失败 → `panic`/`return err` 让拆除中止 ⇒ 同一条测试的第二半(破坏性步骤做完)红。
3. 把清挂起挪进 `if len(failures) == 0 { ... }` ⇒ `…ForUserClearsHoldEvenWhenStepsFail` 红。
4. `Manager.Down` 的 defer 加回 `err == nil &&` ⇒ `TestUserDownClearsHoldEvenWhenDownFails` 红。
5. 去掉 `if !maintenance` 包着的 `SaveDesired(DesiredOff)` ⇒ `TestMaintenanceDownLeavesDesiredOn…` 红。
6. `upLocked` 删掉 `m.clearMaintenanceHold()` ⇒ `TestUpClearsMaintenanceHold` 红。

- [ ] **Step 6: 提交**

```bash
git add internal/guardian/upgradeintent.go internal/guardian/localapi.go internal/guardian/manager.go internal/cli/guardian.go internal/cli/hold_teardown_test.go internal/guardian/hold_manager_test.go
git commit -m "$(cat <<'EOF'
feat: 升级停机改用维护挂起,两个写入点一起改

干净路径(Manager.Down)与逃生路径(forcedMacOSTeardown)都不再把「停下来换
二进制」写成 desired=off——升级路径上真有两个写入点,漏一个等于没修。挂起写
失败时退回 desired=off 且拆除照常做完(停止永不依赖别的先成功);用户显式的
up/down 无条件销挂起,不躲在 err == nil 后面(欠条今天留下陈旧记录的原因)。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: legacy 欠条迁移垫片

**Files:**
- Create: `internal/guardian/holdmigrate.go`
- Create: `internal/guardian/holdmigrate_test.go`
- Modify: `internal/guardian/manager.go`(`recoverLocked` 开头调用一次)

**Interfaces:**
- Produces: `func (s *guardian.Store) MigrateLegacyUpgradeIntent(now time.Time) (bool, error)`;Manager 侧 `legacyIntentMigrator` 可选接口 + `sync.Once` 包装。

- [ ] **Step 1: 写失败测试**

```go
package guardian

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 一台**正处在升级中途**的机器跨过这次切换:旧 CLI 写下的欠条必须变成
// 「desired=on + 一次已武装的挂起」,而不是被丢掉。
//
// 只当挂起是不够的:欠条携带的信息是「用户本来要保护」,而盘上的 desired 恰恰
// 是那次失败自己写下的 off。两半都要恢复。
func TestLegacyUpgradeIntentBecomesArmedHoldAndRestoresDesiredOn(t *testing.T) {
	paths := holdPaths(t.TempDir())
	s := OpenStore(paths)
	paths.UpgradeIntent = filepath.Join(filepath.Dir(paths.Desired), "upgrade-intent.json")
	s = OpenStore(paths)
	if err := s.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.UpgradeIntent, []byte(`{"schema_version":1,"desired_on":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	migrated, err := s.MigrateLegacyUpgradeIntent(now)
	if err != nil || !migrated {
		t.Fatalf("migrated=%v err=%v", migrated, err)
	}
	if desired, err := s.LoadDesired(); err != nil || desired != DesiredOn {
		t.Fatalf("欠条携带的意图没恢复:%q err=%v", desired, err)
	}
	if hold, armed, err := s.LoadMaintenanceHold(now); err != nil || !armed || hold.Reason != HoldReasonLegacyUpgrade {
		t.Fatalf("没有武装挂起:hold=%+v armed=%v err=%v", hold, armed, err)
	}
	if _, err := os.Stat(paths.UpgradeIntent); !os.IsNotExist(err) {
		t.Fatalf("legacy 欠条没删掉: %v", err)
	}
}

// 没有 legacy 文件时**一个字节都不许写** —— 否则每次启动恢复都会凭空武装一次
// 挂起,把每一台机器的保护压制 15 分钟。
func TestLegacyMigrationIsANoOpWithoutTheLegacyFile(t *testing.T) {
	paths := holdPaths(t.TempDir())
	paths.UpgradeIntent = filepath.Join(filepath.Dir(paths.Desired), "upgrade-intent.json")
	s := OpenStore(paths)
	migrated, err := s.MigrateLegacyUpgradeIntent(time.Now())
	if err != nil || migrated {
		t.Fatalf("migrated=%v err=%v", migrated, err)
	}
	if _, armed, err := s.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("凭空武装了挂起:armed=%v err=%v", armed, err)
	}
}

// 「存在但坏了 ⇒ 仍算欠条」—— 这个文件只在 desired_on=true 时被写出来,
// 往「多恢复一次保护」偏,不往「永远不再保护」偏。
func TestCorruptLegacyUpgradeIntentStillCountsAsDebt(t *testing.T) {
	paths := holdPaths(t.TempDir())
	paths.UpgradeIntent = filepath.Join(filepath.Dir(paths.Desired), "upgrade-intent.json")
	s := OpenStore(paths)
	if err := s.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.UpgradeIntent, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MigrateLegacyUpgradeIntent(time.Now()); err != nil {
		t.Fatal(err)
	}
	if desired, _ := s.LoadDesired(); desired != DesiredOn {
		t.Fatalf("坏欠条被当成没有欠条:%q", desired)
	}
}

// 迁移必须跑在启动恢复**之前**:反过来的话 recoverLocked 会先按 desired=off
// 把状态定成 off,再由迁移改盘,机器与状态两张皮。
func TestStartupRecoveryRunsLegacyMigrationFirst(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.legacyIntentPath, []byte(`{"schema_version":1,"desired_on":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.manager.Status().Desired; got != DesiredOn {
		t.Fatalf("启动恢复发布的意图 = %q, want on(迁移排在它后面就会是 off)", got)
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("迁移出来的挂起没拦住 Core:start = %d", got)
	}
}
```

> `env.legacyIntentPath` 需要在 `newManagerTestEnv` 里暴露出来(它已经在 `Paths.UpgradeIntent` 里建了一个 tempdir 路径,把它存进 env 即可)。

**每条断言由哪一处生产改动供养:**
- `…BecomesArmedHoldAndRestoresDesiredOn` ← `MigrateLegacyUpgradeIntent` 里的三件事:`SaveDesired(DesiredOn)`、`ArmMaintenanceHold(HoldReasonLegacyUpgrade, now)`、`os.Remove`。**逐个删掉各红一条断言**。
- `…IsANoOpWithoutTheLegacyFile` ← `os.IsNotExist` 那条早返回。改成无条件武装 → 红。
- `…CorruptLegacy…StillCountsAsDebt` ← 解析失败时按 `desiredOn = true` 处理。改成 `return false, nil` → 红。
- `TestStartupRecoveryRunsLegacyMigrationFirst` ← `recoverLocked` 开头(在 `LoadIntentSnapshot` **之前**)那次迁移调用。挪到函数末尾 → 红。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian -run Legacy -count=1` → 编译失败 `MigrateLegacyUpgradeIntent` 未定义。

- [ ] **Step 3: 最小实现**

```go
package guardian

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

// MigrateLegacyUpgradeIntent 把盘上一张旧的升级欠条(upgrade-intent.json)
// 翻译成本期的表示法,然后删掉它。**只保留一个版本的兼容**。
//
// 为什么不能只当作挂起:欠条携带的是「用户本来要保护」,而此刻盘上的 desired
// 正是那次失败自己写下的 off。只武装挂起 = 压制 15 分钟然后什么都没恢复,
// 恰好丢掉这个文件存在的全部理由。所以两半都要:desired 复位 on + 挂起武装。
//
// **顺序是 复位/武装 → 删除**:中间崩掉的话下次启动再跑一遍,结果相同(幂等);
// 反过来先删则一次崩溃就永久丢失。
func (s *Store) MigrateLegacyUpgradeIntent(now time.Time) (bool, error) {
	if s.paths.UpgradeIntent == "" {
		return false, nil
	}
	b, err := os.ReadFile(s.paths.UpgradeIntent)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read legacy upgrade intent: %w", err)
	}
	// 「存在但坏了 ⇒ 仍算欠条」:这个文件只在 desired_on=true 时被写出来。
	desiredOn := true
	var intent UpgradeIntent
	if err := json.Unmarshal(b, &intent); err == nil && intent.SchemaVersion == 1 {
		desiredOn = intent.DesiredOn
	}
	if desiredOn {
		desired, err := s.LoadDesired()
		if err != nil {
			return false, err
		}
		if desired != DesiredOn {
			if err := s.SaveDesired(DesiredOn); err != nil {
				return false, err
			}
		}
		if err := s.ArmMaintenanceHold(HoldReasonLegacyUpgrade, now); err != nil {
			return false, err
		}
	}
	if err := os.Remove(s.paths.UpgradeIntent); err != nil && !os.IsNotExist(err) {
		// 删不掉不是失败:上面两件事已经做成了,而本次进程只跑这一遍
		// (见 migrateLegacyIntentOnce),不会反复刷新过期时间。
		log.Printf("guardian_legacy_upgrade_intent_remove_failed err=%v", err)
	}
	return true, nil
}
```

Manager 侧:

```go
// legacyIntentMigrator 是可选能力:实现了它的 store 才有 legacy 欠条可迁移。
// 用可选接口是安全的 —— 没实现的只有测试替身,而替身的目录里不会有 legacy 文件。
type legacyIntentMigrator interface {
	MigrateLegacyUpgradeIntent(time.Time) (bool, error)
}

// migrateLegacyIntentOnce 每个进程只跑一遍:启动恢复有重试循环
// (daemon.go 的 retryDaemonRecovery),而一个删不掉的欠条会让每一次重试都刷新
// 一遍挂起的过期时间 —— 那就成了永久压制。
func (m *Manager) migrateLegacyIntentOnce() {
	m.legacyIntentOnce.Do(func() {
		store, ok := m.store.(legacyIntentMigrator)
		if !ok {
			return
		}
		migrated, err := store.MigrateLegacyUpgradeIntent(time.Now())
		if err != nil {
			log.Printf("guardian_legacy_upgrade_intent_migrate_failed err=%v", err)
			return
		}
		if migrated {
			log.Printf("guardian_legacy_upgrade_intent_migrated hold_reason=%s", HoldReasonLegacyUpgrade)
		}
	})
}
```

`Manager` 加字段 `legacyIntentOnce sync.Once`;`recoverLocked` 在 `acquireMutation` 之后、`recoverUpdateLocked` 之前调用 `m.migrateLegacyIntentOnce()`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/guardian -count=1` → PASS

- [ ] **Step 5: 变异验证**

1. 删 `SaveDesired(DesiredOn)` ⇒ 第一条测试的 desired 断言红。
2. 删 `ArmMaintenanceHold` ⇒ 第一条的 hold 断言 + `TestStartupRecoveryRunsLegacyMigrationFirst` 的 startCount 断言红。
3. 删 `os.Remove` ⇒ 第一条的 Stat 断言红。
4. `desiredOn := false` 起手 ⇒ `…CorruptLegacy…` 红。
5. 迁移调用挪到 `recoverLocked` 末尾 ⇒ `TestStartupRecoveryRunsLegacyMigrationFirst` 红。

- [ ] **Step 6: 提交**

```bash
git add internal/guardian/holdmigrate.go internal/guardian/holdmigrate_test.go internal/guardian/manager.go internal/guardian/manager_test.go
git commit -m "$(cat <<'EOF'
feat(guardian): 读一次 legacy 升级欠条,翻成 desired=on + 已武装的挂起

正处在升级中途的机器跨过这次切换不能丢掉那张欠条。只当作挂起是不够的:欠条
携带的是「用户本来要保护」,而盘上的 desired 正是那次失败自己写下的 off,两半
都要恢复。每个进程只迁移一次,免得一个删不掉的文件靠重试永久刷新过期时间。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: 退休升级欠条

**Files:**
- Modify: `internal/guardian/store.go:76-132`(删 `LoadUpgradeIntent`/`SaveUpgradeIntent`/`ClearUpgradeIntent`;`UpgradeIntent` 结构体**留下**,迁移垫片要用它解析)
- Modify: `internal/guardian/manager.go:574-589,613-617`(删 `upgradeIntentStore`、`forgetUpgradeIntent` 与 defer 里那半边)
- Modify: `internal/guardian/upgradeintent.go`(删掉 Down defer 里欠条那半边留下的引用;`downIsMaintenance` 与 `markMaintenanceStop` 留下,它们守的是挂起)
- Delete: `internal/cli/upgradeintent.go`
- Modify: `internal/cli/guardian.go:728-738`(删 `forgetUpgradeDebtOnExplicitOff` 调用)
- Modify: `internal/cli/upgraderun.go:23-46,50-59,94-100,165-168`(`upgradeIO` 去掉 `saveIntent`/`clearIntent`,`upgradeOutcome` 去掉 `IntentLeftBehind`)
- Modify: `internal/cli/appinstall_darwin.go:131-161`(`upgradeDesiredOn` 只问 store)
- Modify: 对应测试(`store_test.go:245-300`、`upgraderun_test.go:250-340`、`upgrade_guardian_e2e_test.go`)

**Interfaces:**
- Produces: `func upgradeDesiredOn(ctx context.Context) bool`(签名不变,实现只剩 `guardianDesiredOn`)。

- [ ] **Step 1: 写失败测试**

```go
// 欠条能被删掉的**全部依据**:一次中途失败的升级不再破坏 desired,所以重跑
// app-install 时从磁盘读到的就是用户本来的意图。
func TestUpgradeRetryAfterCrashStillRestoresProtection(t *testing.T) {
	root := t.TempDir()
	store := guardian.OpenStore(guardian.Paths{
		Desired:         filepath.Join(root, "guardian-state.json"),
		MaintenanceHold: filepath.Join(root, "maintenance-hold.json"),
	})
	if err := store.SaveDesired(guardian.DesiredOn); err != nil {
		t.Fatal(err)
	}
	// 第一次升级:停保护(武装挂起、**不动 desired**),然后装文件时崩掉。
	if err := store.ArmMaintenanceHold(guardian.HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	// 重跑:意图必须还是 on。
	desired, err := store.LoadDesired()
	if err != nil || desired != guardian.DesiredOn {
		t.Fatalf("一次失败的升级把用户的意图弄丢了:%q err=%v", desired, err)
	}
}
```

再加一条**编译期**保证之外的行为断言(证明 CLI 不再读那个文件):

```go
// CLI 再也不看 upgrade-intent.json —— 盘上放一张陈旧欠条,升级计划不受影响。
// (它今天会让 resolveUpgradeDesiredOn 违背用户明确的关闭请求把保护打开。)
func TestStaleLegacyIntentFileDoesNotTurnProtectionBackOn(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "upgrade-intent.json"), []byte(`{"schema_version":1,"desired_on":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := guardian.OpenStore(guardian.Paths{Desired: filepath.Join(root, "guardian-state.json")})
	if err := store.SaveDesired(guardian.DesiredOff); err != nil {
		t.Fatal(err)
	}
	desired, err := store.LoadDesired()
	if err != nil || desired != guardian.DesiredOff {
		t.Fatalf("desired = %q err = %v", desired, err)
	}
	steps := upgradeSteps(true, desired == guardian.DesiredOn)
	if stepsContain(steps, UpgradeStartProtection) {
		t.Fatal("陈旧欠条把保护打开了 —— 用户明确说过 off")
	}
}
```

**每条断言由哪一处生产改动供养:**
- `TestUpgradeRetryAfterCrash…` ← Task 4 的「维护停机不写 desired=off」。把它改回去 → 红。**这条测试是删欠条的许可证,先跑绿再删代码。**
- `TestStaleLegacyIntentFileDoesNotTurnProtectionBackOn` ← `upgradeDesiredOn` 不再调 `loadUpgradeIntent`/`resolveUpgradeDesiredOn`。把它们接回来 → 红。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli -run 'UpgradeRetryAfterCrash|StaleLegacyIntent' -count=1`
Expected: 第二条红(今天 `upgradeDesiredOn` 会合成欠条)。

- [ ] **Step 3: 最小实现**

按 Files 清单逐个删除。`upgradeDesiredOn` 变成:

```go
// upgradeDesiredOn 回答「这台机器此刻想不想开着保护」。
//
// 只有一个来源了:Guardian 的 desired store。升级不再改写它(维护挂起接管了
// 「此刻不能有保护」这件事),所以一次中途失败之后重跑读到的就是用户本来的意图 ——
// 这正是升级欠条能被删掉的全部依据。
func upgradeDesiredOn(ctx context.Context) bool {
	return guardianDesiredOn(ctx)
}
```

`runUpgrade` 里删掉 `io.saveIntent(true)` 那一段与结尾的 `io.clearIntent()`;`upgradeFailureMessageWithNetwork` 那条「欠条留在盘上」的注释改写成「desired 未被改写,重跑会读到用户本来的意图;维护挂起 15 分钟后自动失效」。

- [ ] **Step 4: 跑测试确认通过**

Run: `go build ./... && go vet ./... && go test ./... -count=1` → PASS
`grep -rn "UpgradeIntent\|upgrade-intent" --include="*.go" .` 的剩余命中应当**只有** `Paths.UpgradeIntent`、`UpgradeIntent` 结构体、`MigrateLegacyUpgradeIntent` 与卸载计划里的 `darwinUpgradeIntentPath`(它必须留着 —— 旧文件仍要随卸载删掉)。

- [ ] **Step 5: 变异验证**

1. 把 `upgradeDesiredOn` 改回合成欠条 ⇒ `TestStaleLegacyIntentFileDoesNotTurnProtectionBackOn` 红。
2. 把 Task 4 的 `if !maintenance` 去掉(即恢复写 desired=off)⇒ `TestUpgradeRetryAfterCrashStillRestoresProtection` 红。
3. 删掉卸载计划里的 `darwinUpgradeIntentPath` ⇒ 既有卸载测试红(确认它仍在守着)。

- [ ] **Step 6: 提交**

```bash
git add -A
git commit -m "$(cat <<'EOF'
refactor: 删掉升级欠条,只留一个版本的只读迁移

升级不再改写 desired,于是「一次失败之后重跑读到的还是用户本来的意图」这件事
由 desired 自己承担,欠条、resolveUpgradeDesiredOn、CLI 那份重复销账一起退休。
Paths.UpgradeIntent 与卸载路径留着:旧文件仍要被迁移和删除。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Guardian 发布挂起(Status 字段 + 能力声明 + 活过 HTTP 往返)

**Files:**
- Modify: `internal/guardian/types.go:109-140,217-263`
- Modify: `internal/guardian/localapi.go:142-165,167-195`
- Modify: `internal/guardian/manager.go`(加 `MaintenanceHoldStatus()`)
- Create: `internal/guardian/hold_status_test.go`

**Interfaces:**
- Produces:
  - `type guardian.MaintenanceHoldStatus struct { Reason string; ExpiresAt time.Time }`
  - `Status.MaintenanceHold *MaintenanceHoldStatus \`json:"maintenance_hold,omitempty"\``
  - `const guardian.CapabilityMaintenanceHold = "maintenance_hold"`(进 `GuardianCapabilities()`)
  - `func (m *Manager) MaintenanceHoldStatus() *MaintenanceHoldStatus`

- [ ] **Step 1: 写失败测试**

```go
// 字段存在不等于它活过一次 HTTP 往返 —— 上一期就有一条守卫栽在这上面。
// 这里走真的 handler、真的 JSON 编解码。
func TestArmedHoldSurvivesTheStatusHTTPHop(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	NewLocalAPI(env.manager).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.MaintenanceHold == nil || got.MaintenanceHold.Reason != HoldReasonUpgrade {
		t.Fatalf("挂起没活过 HTTP 往返: %s", recorder.Body.String())
	}
	if got.MaintenanceHold.ExpiresAt.IsZero() {
		t.Fatal("到期时刻丢了 —— 消费方无从判断它还算不算数")
	}
	if !slices.Contains(got.Capabilities, CapabilityMaintenanceHold) {
		t.Fatalf("能力没声明: %v", got.Capabilities)
	}
}

// 过期的挂起不发布 —— 键缺席的意思是「没有挂起」,而不是「有一个不算数的」。
func TestExpiredHoldIsNotPublished(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now().Add(-2*MaintenanceHoldDuration)); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	NewLocalAPI(env.manager).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if strings.Contains(recorder.Body.String(), "maintenance_hold") {
		t.Fatalf("发布了一个已过期的挂起: %s", recorder.Body.String())
	}
}

// 变更类响应(POST /v1/up、/v1/down)也要带上——菜单的开关读的正是那份响应。
func TestMutationResponsesCarryMaintenanceHoldField(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	status := statusWithVersions(env.manager, LocalAPIOptions{})
	if status.MaintenanceHold == nil {
		t.Fatal("statusWithVersions 没附挂起:菜单的 Turn Off 只看得到这份响应")
	}
}
```

**每条断言由哪一处生产改动供养:**
- `…SurvivesTheStatusHTTPHop` ← `Status.MaintenanceHold` 的 json tag(不是 `-`)+ `observableStatus` 里 `attachMaintenanceHold` 的调用。任一去掉 → 红。
- `…CapabilityMaintenanceHold` 断言 ← `GuardianCapabilities()` 的返回值。不加 → 红。
- `TestExpiredHoldIsNotPublished` ← `MaintenanceHoldStatus()` 里用 `armed` 而不是「文件在不在」。改成后者 → 红。
- `TestMutationResponsesCarryMaintenanceHoldField` ← `statusWithVersions`(不只是 `observableStatus`)里的那次调用。只在 GET 上附 → 红。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian -run 'Hold.*HTTP|ExpiredHoldIsNot|MutationResponsesCarry' -count=1` → 编译失败。

- [ ] **Step 3: 最小实现**

`types.go`:

```go
// CapabilityMaintenanceHold 表示这一版 Guardian 认识维护挂起,因而
// Status.MaintenanceHold 这个键**是它会填的**。
//
// 与 CapabilityReconcileReport 同一机制、同一理由:消费方要分得开「这一版没有
// 挂起这个概念」(没声明能力)与「有这个概念,此刻没有挂起」(声明了、键缺席)。
// 前者下菜单不该说「保护已关闭」,因为它根本不知道是不是维护窗口。
const CapabilityMaintenanceHold = "maintenance_hold"

func GuardianCapabilities() []string {
	return []string{CapabilityDiagnosticsArchive, CapabilityReconcileReport, CapabilityMaintenanceHold}
}

// MaintenanceHoldStatus 是**正在生效**的那次挂起,随 Status 发布。
// 过期的挂起不出现在这里:键缺席的意思是「此刻没有挂起」。
type MaintenanceHoldStatus struct {
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
}
```

`Status` 加字段:

```go
	// MaintenanceHold 非 nil 表示此刻有一次维护挂起在生效:用户要保护(desired
	// 仍是 on),但此刻不能有。**omitempty 是契约的一部分**:键缺席 = 没有挂起。
	// 「这一版认不认识挂起」由 Capabilities 里的 CapabilityMaintenanceHold 回答。
	MaintenanceHold *MaintenanceHoldStatus `json:"maintenance_hold,omitempty"`
```

`manager.go`:

```go
// MaintenanceHoldStatus 现读磁盘。**不缓存、不进 m.status。**
//
// 不进 m.status:控制面里一大批路径拿从零构造的 Status 字面量整体替换它
// (upLocked/downLocked/recoverLocked…),挂起只要并进去就迟早被顺手抹掉 ——
// 与 reconcileReport 住在外面同一个理由。
// 不缓存:武装它的是**另一个进程**(CLI),缓存等于发布一个可能已经不成立的事实。
func (m *Manager) MaintenanceHoldStatus() *MaintenanceHoldStatus {
	hold, armed, err := m.store.LoadIntentSnapshotHold(time.Now())
	if err != nil || !armed {
		return nil
	}
	return &MaintenanceHoldStatus{Reason: hold.Reason, ExpiresAt: hold.ExpiresAt}
}
```

> 实现注:`DesiredStore` 已有 `LoadIntentSnapshot`,直接用它即可(`snapshot.HoldArmed` / `snapshot.Hold`),**不要**再往接口上加第三个方法;上面的 `LoadIntentSnapshotHold` 只是占位名,实现时写成:
> ```go
> intent, err := m.store.LoadIntentSnapshot(time.Now())
> if err != nil || !intent.HoldArmed { return nil }
> return &MaintenanceHoldStatus{Reason: intent.Hold.Reason, ExpiresAt: intent.Hold.ExpiresAt}
> ```

`localapi.go`:

```go
// maintenanceHoldReporter 由能回答「此刻有没有维护挂起」的 controller 实现。
type maintenanceHoldReporter interface {
	MaintenanceHoldStatus() *MaintenanceHoldStatus
}

// attachMaintenanceHold 在**每一个**回 Status 的响应上附挂起:菜单的开关读的是
// POST /v1/up、/v1/down 的响应,只在 GET 上附等于在最需要它的那一刻恒为空
// (与 applyVersionFields 那条注释同一个教训)。
func attachMaintenanceHold(status *Status, controller Controller) {
	reporter, ok := controller.(maintenanceHoldReporter)
	if !ok {
		return
	}
	status.MaintenanceHold = reporter.MaintenanceHoldStatus()
}
```

在 `observableStatus` 的**两个** return 之前、以及 `statusWithVersions` 里各调一次 `attachMaintenanceHold(&status, controller)`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/guardian -count=1` → PASS。既有 `TestStatusCapabilities…` 一类逐字比对能力数组的测试要跟着加第三项。

- [ ] **Step 5: 变异验证**

1. `Status.MaintenanceHold` 的 tag 改成 `json:"-"` ⇒ HTTP 往返测试红。
2. `observableStatus` 里只在一个 return 前调 `attachMaintenanceHold`(漏掉 `recoveries == nil` 那条早返回)⇒ 用 `fakeController`(无 pathRecovery)构造一条同款测试确认它会红;**这条变异必须做**,那正是「分支映射错」那类假绿的形状。
3. `statusWithVersions` 里不调 ⇒ `TestMutationResponsesCarry…` 红。
4. `MaintenanceHoldStatus()` 忽略 `armed` ⇒ `TestExpiredHoldIsNotPublished` 红。

- [ ] **Step 6: 提交**

```bash
git add internal/guardian/types.go internal/guardian/localapi.go internal/guardian/manager.go internal/guardian/hold_status_test.go
git commit -m "$(cat <<'EOF'
feat(guardian): Status 发布正在生效的维护挂起,并声明 maintenance_hold 能力

每一个回 Status 的响应都附(不只 GET):菜单的开关读的是 up/down 的响应。
过期的挂起不发布——键缺席就是「此刻没有挂起」;「这一版认不认识挂起」由能力
声明回答,与 reconcile_report 同一机制。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: `observe.Diverge` 认挂起,`bx status` 显示它

**Files:**
- Modify: `internal/observe/state.go:91-93`(`Intent`)
- Modify: `internal/observe/diverge.go:19-72`
- Modify: `internal/observe/diverge_test.go`
- Modify: `internal/cli/cli.go:205-244`(`clientStatusReport`)、`:4364-4386`(`attachObservation`)、`:4427-4457`(装配)、`renderClientStatus` 里加一行
- Modify: `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: `guardian.MaintenanceHoldStatus`(Task 7)。
- Produces:`observe.Intent.Hold *observe.HoldIntent`、`type observe.HoldIntent struct { Reason string; ExpiresAt time.Time }`、`clientStatusReport.MaintenanceHold`。

- [ ] **Step 1: 写失败测试**

```go
package observe

// 维护挂起期间,残留是**预期之内**的:升级正在进行。不告诉 Diverge 就会冒出
// 一条假分歧,而 divergence 一旦被训练成噪声,它唯一的价值就没了。
func TestDivergeStaysQuietDuringArmedHold(t *testing.T) {
	now := time.Now()
	got := Diverge(
		Intent{Desired: "off", Hold: &HoldIntent{Reason: "upgrade", ExpiresAt: now.Add(5 * time.Minute)}},
		ObservedState{ObservedAt: now, BarrierPresent: True, DNSManaged: True, CoreSocket: False},
		Believed{Protection: "off"},
	)
	if len(got) != 0 {
		t.Fatalf("挂起期间不该有分歧: %+v", got)
	}
}

// **同一份输入,只把到期时刻挪到过去** —— 挂起失效之后必须重新说话。
// 只在一半输入空间上断言的不变量等于没断言。
func TestDivergeSpeaksUpOnceTheHoldExpires(t *testing.T) {
	now := time.Now()
	got := Diverge(
		Intent{Desired: "off", Hold: &HoldIntent{Reason: "upgrade", ExpiresAt: now.Add(-time.Minute)}},
		ObservedState{ObservedAt: now, BarrierPresent: True, DNSManaged: True, CoreSocket: False},
		Believed{Protection: "off"},
	)
	if len(got) == 0 {
		t.Fatal("过期的挂起还在压制分歧")
	}
}

// 挂起过期之后,「用户要保护而 Core 不在」必须现形 —— 这正是挂起强于欠条的地方:
// 欠条让机器看起来是关的,挂起让它看起来是坏的,而它确实是坏的。
func TestDivergeReportsMissingProtectionWhenDesiredOnAndNoHold(t *testing.T) {
	now := time.Now()
	got := Diverge(
		Intent{Desired: "on"},
		ObservedState{ObservedAt: now, CoreSocket: False},
		Believed{Protection: "off"},
	)
	if !hasField(got, "core_socket") {
		t.Fatalf("desired=on 而 Core 不应答,必须报一条分歧: %+v", got)
	}
}

// 而挂起武装着的时候,同一件事**不是**分歧。
func TestDivergeDoesNotReportMissingProtectionUnderHold(t *testing.T) {
	now := time.Now()
	got := Diverge(
		Intent{Desired: "on", Hold: &HoldIntent{Reason: "upgrade", ExpiresAt: now.Add(time.Minute)}},
		ObservedState{ObservedAt: now, CoreSocket: False},
		Believed{Protection: "off"},
	)
	if hasField(got, "core_socket") {
		t.Fatalf("挂起期间报了假分歧: %+v", got)
	}
}
```

CLI 侧:

```go
// 挂起必须从 Guardian 的 Status 一路传进 Diverge —— 字段存在不等于它被用上了。
func TestClientStatusPassesMaintenanceHoldIntoDiverge(t *testing.T) {
	now := time.Now()
	status := guardian.Status{
		Desired:         guardian.DesiredOn,
		Protection:      guardian.ProtectionOff,
		MaintenanceHold: &guardian.MaintenanceHoldStatus{Reason: "upgrade", ExpiresAt: now.Add(5 * time.Minute)},
	}
	report, err := readClientStatusReportWithObserver(
		func() (stats.Report, error) { return stats.Report{}, errors.New("core unavailable") },
		func() (guardian.Status, error) { return status, nil },
		"darwin",
		func(context.Context) observe.ObservedState {
			return observe.ObservedState{ObservedAt: now, CoreSocket: observe.False}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Divergence) != 0 {
		t.Fatalf("挂起没传进 Diverge,冒出了假分歧: %+v", report.Divergence)
	}
	if report.MaintenanceHold == nil || report.MaintenanceHold.Reason != "upgrade" {
		t.Fatalf("报告里没有挂起: %+v", report.MaintenanceHold)
	}
}

// 渲染要说清「用户要保护、此刻被维护挂起压着」,不能只画一个 Off。
func TestRenderClientStatusMentionsMaintenanceHold(t *testing.T) {
	out := renderClientStatus(clientStatusReport{
		ProtectionState: guardian.ProtectionOff,
		Desired:         "on",
		MaintenanceHold: &guardian.MaintenanceHoldStatus{Reason: "upgrade", ExpiresAt: time.Now().Add(3 * time.Minute)},
	})
	if !strings.Contains(out, "维护挂起") || !strings.Contains(out, "upgrade") {
		t.Fatalf("渲染里看不到挂起:\n%s", out)
	}
}
```

**每条断言由哪一处生产改动供养:**
- 前两条 ← `Diverge` 里 `held := intent.Hold != nil && intent.Hold.ExpiresAt.After(observed.ObservedAt)` 与 `if intent.Desired == "off" && !held`。去掉 `!held` → 第一条红;去掉 `.After(...)`(只判 nil)→ 第二条红。
- 第三/四条 ← 新增的 `desired=on && !held && CoreSocket==False` 分支。删掉 → 第三条红;忘了 `!held` → 第四条红。
- `TestClientStatusPassesMaintenanceHoldIntoDiverge` ← `attachObservation` 里把 `status.MaintenanceHold` 翻成 `observe.Intent.Hold`。传 `nil` → 红。
- `TestRenderClientStatusMentionsMaintenanceHold` ← `renderClientStatus` 里那一行。删掉 → 红。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/observe ./internal/cli -run 'Diverge|MaintenanceHold' -count=1` → 编译失败。

- [ ] **Step 3: 最小实现**

`state.go`:

```go
// Intent 是生命周期层的意图。
type Intent struct {
	Desired string `json:"desired"` // "on" | "off"
	// Hold 是正在生效的维护挂起(升级等)。非 nil 且未过期时,「保护此刻不在」
	// 是**预期之内**的,不是分歧。
	Hold *HoldIntent `json:"maintenance_hold,omitempty"`
}

// HoldIntent 是 guardian.MaintenanceHoldStatus 在本包里的镜像。
// 本包不 import internal/guardian(那会把只读观测层挂到控制面上)。
type HoldIntent struct {
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
}
```

`diverge.go`:

```go
func Diverge(intent Intent, observed ObservedState, believed Believed) []Divergence {
	var out []Divergence

	// 挂起用**观测那一刻**的时间判过期,不用 time.Now():这份判断要与它旁边
	// 那些事实说的是同一个瞬间。
	held := intent.Hold != nil && intent.Hold.ExpiresAt.After(observed.ObservedAt)

	if believed.Protection == "protected" { ... 原样不动 ... }

	if intent.Desired == "off" && !held {
		... 原样不动 ...
	}

	// 用户要保护,而 Core 的控制 socket 不应答 —— **挂起期间这是预期,过期之后
	// 这就是故障**。设计取舍五:过期不会恢复保护,它买到的是「不再压制」,于是
	// 机器不再「看起来正确地关着」,而是如实地看起来坏了。
	if intent.Desired == "on" && !held && observed.CoreSocket == False {
		out = append(out, Divergence{
			Field:    "core_socket",
			Believed: "on",
			Observed: "false",
			Note:     "意图是保护,但 Core 的控制 socket 不应答:此刻没有保护在跑",
		})
	}

	for _, e := range observed.Errors { ... 原样不动 ... }
	return out
}
```

CLI:`clientStatusReport` 加

```go
	// MaintenanceHold 是正在生效的维护挂起。它解释了「desired=on 而保护不在」——
	// 没有它,一台正在升级的机器与一台坏掉的机器在 bx status 上长得一模一样。
	MaintenanceHold *guardian.MaintenanceHoldStatus `json:"maintenance_hold,omitempty"`
```

`assembleClientStatusReportWithCoreForPlatform` 里 `MaintenanceHold: status.MaintenanceHold,`;`attachObservation` 里:

```go
	intent := observe.Intent{Desired: string(status.Desired)}
	if hold := status.MaintenanceHold; hold != nil {
		intent.Hold = &observe.HoldIntent{Reason: hold.Reason, ExpiresAt: hold.ExpiresAt}
	}
	report.Divergence = observe.Diverge(intent, observed, observe.Believed{...})
```

`renderClientStatus`(两个分支都要,与 `writeClientReconcile` 并列)加:

```go
func writeClientMaintenanceHold(b *strings.Builder, report clientStatusReport) {
	hold := report.MaintenanceHold
	if hold == nil {
		return
	}
	fmt.Fprintf(b, "  Hold    维护挂起(%s),%s 后失效 —— 保护此刻被有意压制,desired 仍是 %s\n",
		hold.Reason, time.Until(hold.ExpiresAt).Round(time.Second), report.Desired)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/observe ./internal/cli -count=1` → PASS

- [ ] **Step 5: 变异验证**

1. `held` 去掉 `.After(observed.ObservedAt)` ⇒ `TestDivergeSpeaksUpOnceTheHoldExpires` 红。
2. 新增分支去掉 `!held` ⇒ `TestDivergeDoesNotReportMissingProtectionUnderHold` 红。
3. `attachObservation` 里不填 `intent.Hold` ⇒ `TestClientStatusPassesMaintenanceHoldIntoDiverge` 红。
4. `renderClientStatus` 只在**完整**报告那一支调 `writeClientMaintenanceHold`(漏掉 partial 那一支)⇒ 补一条 partial 报告的渲染测试,确认它会红。

- [ ] **Step 6: 提交**

```bash
git add internal/observe internal/cli
git commit -m "$(cat <<'EOF'
feat(observe): Diverge 认维护挂起,bx status 并列发布它

挂起期间残留是预期之内的,不告诉 Diverge 就会冒出假分歧,而 divergence 一旦被
训练成噪声就没有价值了;挂起过期之后,「用户要保护而 Core 不应答」必须现形——
这正是挂起强于欠条的地方:欠条让机器看起来是关的,挂起让它看起来是坏的。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Swift 菜单显示维护挂起

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/GuardianStatus.swift`
- Create: `apps/macos/BxMenu/Sources/BxMenu/MaintenancePresentation.swift`
- Create: `apps/macos/BxMenu/Tests/MaintenancePresentationTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/MenuRows.swift`
- Modify: `apps/macos/BxMenu/Tests/MenuRowsTests.swift`
- Modify: `scripts/test-macos-menu.sh`(**新套件必须注册,且 `menu-rows` 套件要加上新源文件**)

**Interfaces:**
- Consumes: Task 7 的 JSON 形状 `{"maintenance_hold":{"reason":…,"expires_at":…},"capabilities":[…,"maintenance_hold"]}`。
- Produces:`GuardianStatus.maintenanceHold: MaintenanceHold?`、`struct MaintenanceHold { reason: String?; expiresAt: String? }`、`let maintenanceHoldCapability = "maintenance_hold"`、`func maintenanceRow(status: GuardianStatus?, now: Date) -> MenuRow?`。

- [ ] **Step 1: 写失败测试**

新建 `apps/macos/BxMenu/Tests/MaintenancePresentationTests.swift`(照既有测试文件的风格:顶层 `func run…()` + 手写断言 + 结尾打印):

```swift
import Foundation

var failures = 0

func expect(_ condition: Bool, _ message: String) {
    if !condition {
        failures += 1
        FileHandle.standardError.write("FAIL: \(message)\n".data(using: .utf8)!)
    }
}

func decodeStatus(_ json: String) -> GuardianStatus {
    try! JSONDecoder().decode(GuardianStatus.self, from: json.data(using: .utf8)!)
}

// 挂起在生效 → 菜单必须说出来,否则它渲染成一个普通的「已关闭」外加一个
// 可点的 Turn On,用户完全不知道升级正在进行。
func runArmedHoldShowsRow() {
    let status = decodeStatus("""
    {"desired":"on","phase":"idle","protection_state":"off",
     "capabilities":["maintenance_hold"],
     "maintenance_hold":{"reason":"upgrade","expires_at":"2126-01-01T00:00:00Z"}}
    """)
    let row = maintenanceRow(status: status, now: Date(timeIntervalSince1970: 0))
    expect(row != nil, "挂起在生效时必须有一行")
    expect(row?.value.contains("upgrade") == true, "行里要说清是什么挂起: \(String(describing: row))")
    expect(row?.mark == .unknown, "挂起不是异常,不许计进 anomalyCount 让图标裂开")
}

// 过期的挂起不再说话 —— 半边输入空间的断言等于没断言。
func runExpiredHoldShowsNoRow() {
    let status = decodeStatus("""
    {"desired":"on","phase":"idle","protection_state":"off",
     "capabilities":["maintenance_hold"],
     "maintenance_hold":{"reason":"upgrade","expires_at":"1970-01-01T00:00:00Z"}}
    """)
    expect(maintenanceRow(status: status, now: Date(timeIntervalSince1970: 3600)) == nil,
           "过期的挂起不该再显示")
}

// 旧 Guardian(没声明能力、也没有这个键)**不是**「没有挂起」——它是「不知道」。
// 但也不能凭空造一行,故:不声明能力时不显示。
func runOldGuardianShowsNoRow() {
    let status = decodeStatus("""
    {"desired":"on","phase":"idle","protection_state":"off"}
    """)
    expect(status.capabilities == nil, "旧 Guardian 连 capabilities 键都没有")
    expect(maintenanceRow(status: status, now: Date()) == nil, "没有能力声明时不许编一行出来")
}

// 未知键与缺席键都不许把整份状态解坏(菜单失明比显示不全严重得多)。
func runUnknownFieldsStillDecode() {
    let status = decodeStatus("""
    {"desired":"on","phase":"idle","protection_state":"off","brand_new_key":123,
     "maintenance_hold":{"reason":"upgrade"}}
    """)
    expect(status.maintenanceHold?.reason == "upgrade", "挂起没解出来")
    expect(status.maintenanceHold?.expiresAt == nil, "缺席的 expires_at 应为 nil,不是解码失败")
    expect(maintenanceRow(status: status, now: Date()) == nil, "没有到期时刻就无从判断它还算不算数,不显示")
}

runArmedHoldShowsRow()
runExpiredHoldShowsNoRow()
runOldGuardianShowsNoRow()
runUnknownFieldsStillDecode()
if failures > 0 { exit(1) }
print("maintenance presentation tests passed")
```

`MenuRowsTests.swift` 追加:挂起在生效时 `menuRows` 的第一行是那条挂起行,且 `anomalyCount == 0`。

**每条断言由哪一处生产改动供养:**
- `runArmedHoldShowsRow` ← `maintenanceRow` 的存在与它在 `menuRows` 里的调用。删掉调用 → `MenuRowsTests` 红;删掉函数体的 return → 本测试红。
- `runExpiredHoldShowsNoRow` ← `maintenanceRow` 里 `expires > now` 的比较。删掉 → 红。
- `runOldGuardianShowsNoRow` ← `declaresMaintenanceHold(status.capabilities)` 那道门。删掉 → 红。
- `runUnknownFieldsStillDecode` ← `GuardianStatus.init(from:)` 里 `decodeIfPresent` + `MaintenanceHold` 全可选字段。改成 `decode` → 抛错,红。
- `.mark == .unknown` ← `maintenanceRow` 返回的 mark。改成 `.bad` → 红(且它会让图标裂开)。

- [ ] **Step 2: 跑测试确认失败**

Run: `bash scripts/test-macos-menu.sh`
Expected: 编译失败(`maintenanceRow` 未定义)。**必须先把新套件注册进脚本**,否则它一次都不会跑而脚本照样退 0。

- [ ] **Step 3: 最小实现**

`GuardianStatus.swift`:加 `case maintenanceHold = "maintenance_hold"`、字段 `let maintenanceHold: MaintenanceHold?`、`maintenanceHold = try container.decodeIfPresent(MaintenanceHold.self, forKey: .maintenanceHold)`,以及

```swift
/// Guardian 正在生效的那次维护挂起(`internal/guardian.MaintenanceHoldStatus` 的镜像)。
///
/// **两个字段都可选。** Go 侧今天没给它们 omitempty,但那是**生产者当下的选择**,
/// 不是本结构体能依赖的保证:哪天有人加上,菜单会因为缺一个键而整份 GuardianStatus
/// 解码失败 → 落到 "Status unreadable" —— 2026-08-06 那个失明 bug 换个层级重演。
struct MaintenanceHold: Decodable {
    let reason: String?
    let expiresAt: String?

    enum CodingKeys: String, CodingKey {
        case reason
        case expiresAt = "expires_at"
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        reason = try container.decodeIfPresent(String.self, forKey: .reason)
        expiresAt = try container.decodeIfPresent(String.self, forKey: .expiresAt)
    }
}
```

新建 `MaintenancePresentation.swift`:

```swift
import Foundation

/// 与 Go 侧 `guardian.CapabilityMaintenanceHold` 逐字对应。
let maintenanceHoldCapability = "maintenance_hold"

/// **nil 不是「没有挂起」,是「这一版 Guardian 没有挂起这个概念」。**
/// 二者不分开,升级窗口里的旧 Guardian 会被读成「确定没有维护在进行」。
func declaresMaintenanceHold(_ capabilities: [String]?) -> Bool {
    guard let capabilities else { return false }
    return capabilities.contains(maintenanceHoldCapability)
}

/// 维护挂起那一行。没有挂起、挂起过期、或这一版根本不认识挂起 ⇒ 不显示。
///
/// mark 用 `.unknown` 而不是 `.bad`:挂起不是异常,而 anomalyCount 只数 `.bad`
/// 并且驱动图标裂不裂 —— 一次正常的升级不该让图标裂开。
func maintenanceRow(status: GuardianStatus?, now: Date) -> MenuRow? {
    guard let status, declaresMaintenanceHold(status.capabilities),
          let hold = status.maintenanceHold,
          let raw = hold.expiresAt,
          let expires = ISO8601DateFormatter().date(from: raw),
          expires > now
    else { return nil }
    let reason = hold.reason ?? "maintenance"
    let minutes = max(1, Int(expires.timeIntervalSince(now) / 60))
    return MenuRow(label: "Maintenance",
                   value: "Paused for \(reason) — up to \(minutes) min",
                   mark: .unknown)
}
```

`MenuRows.swift` 的 `menuRows` 签名加 `now: Date = Date()`,函数体第一行:

```swift
    if let hold = maintenanceRow(status: status, now: now) {
        rows.append(hold)
    }
```

`scripts/test-macos-menu.sh`:新增

```bash
run_test maintenance-presentation \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Sources/BxMenu/GuardianStatus.swift" \
  "$MENU/Sources/BxMenu/MenuRows.swift" \
  "$MENU/Sources/BxMenu/MaintenancePresentation.swift" \
  "$MENU/Tests/MaintenancePresentationTests.swift"
```

并在既有的 `run_test menu-rows` 块里补 `"$MENU/Sources/BxMenu/MaintenancePresentation.swift" \`。

- [ ] **Step 4: 跑测试确认通过**

Run: `bash scripts/test-macos-menu.sh`
Expected: 结尾打印 `macOS menu tests passed`,且中途出现 `maintenance presentation tests passed`。

Run(有 Xcode 的机器上): `swift build --package-path apps/macos/BxMenu`
Expected: 成功(`main.swift` 的 `menuRows(status:dns:)` 调用因默认参数不受影响)。

- [ ] **Step 5: 变异验证**

1. 把 `expires > now` 改成 `true` ⇒ `runExpiredHoldShowsNoRow` 红。
2. 去掉 `declaresMaintenanceHold` 那道门 ⇒ `runOldGuardianShowsNoRow` 红。
3. `mark` 改成 `.bad` ⇒ `MenuRowsTests` 的 `anomalyCount == 0` 红。
4. `menuRows` 里删掉那次 append ⇒ `MenuRowsTests` 红(证明这一行真的接进去了,不是只存在于一个没人调的函数里)。
5. **把新套件从 `scripts/test-macos-menu.sh` 里删掉**,确认脚本仍然打印收尾横幅并退 0 —— 这一步是给实现者看的:注册是唯一让它跑起来的东西。做完记得加回去。

- [ ] **Step 6: 提交**

```bash
git add apps/macos/BxMenu scripts/test-macos-menu.sh
git commit -m "$(cat <<'EOF'
feat(menu): 菜单显示维护挂起

挂起期间菜单原本渲染成一个普通的「已关闭」外加一个可点的 Turn On,用户完全
不知道升级正在进行。新增一行说明,mark 用 unknown(挂起不是异常,不许让图标
裂开);能力没声明就什么都不说——那是「不知道」,不是「没有挂起」。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

## 真机验收清单(本计划一条都没做,不许被读成做过)

1. `sudo bx app-install` 全程:停保护那一刻 `/var/lib/bx/guardian-state.json` 仍为 `"on"`,`maintenance-hold.json` 存在且 `reason=upgrade`;升级结束后挂起消失。
2. 升级中途手工 kill CLI:15 分钟内 `bx status` 不报分歧;15 分钟后 `bx status` 报出 `core_socket` 那条真实分歧,且**不会**自己把保护起回来。
3. 挂起窗口里点菜单 Turn Off:`desired` 变 off、挂起消失、菜单显示已关闭。
4. 挂起窗口里 `sudo bx up`:挂起消失、保护起来。
5. 把 `/var/lib/bx` 设成只读再跑升级(模拟挂起写失败):必须退回写 `desired=off`,且拆除照常做完、用户网络恢复。
6. 盘上留一张 legacy `upgrade-intent.json` 再重启 Guardian:文件消失、`desired` 变 on、Core 没被立刻起来。
7. `sudo bx uninstall` 之后 `/var/lib/bx/maintenance-hold.json` 不存在,而 brook/sing-box 二进制与 china 列表还在。
