# 用进程扫描判定 Core 所有权 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `bx up` 不再因为一个 `launching` 标记而永久失败——改为向系统求证「有没有进程在跑我们的 Core」,而不是从自己的记账里推断。

**Architecture:** `internal/guardian` 新增一个只读的进程扫描能力(darwin 实现 + `!darwin` 桩),经 `ExecCoreRunner` 上一个可注入的函数字段接入,替换 `Existing()` 与 `refuseLiveLaunchMarker` 里对 `launching` 标记的无条件拒绝。判据的纯逻辑部分(procargs2 解析、「像不像 Core」)独立成可测函数。

**Tech Stack:** Go 1.26,`golang.org/x/sys/unix`(`SysctlKinfoProcSlice`、`SysctlRaw("kern.procargs2")`)。

设计文档:`docs/superpowers/specs/2026-08-07-core-ownership-by-process-scan-design.md`

## Global Constraints

- **fail-closed 一步不让。** 扫描失败、拿不到进程信息、判据存疑——一律保持 uncertain 并拒绝,绝不放行。「问不出来」不等于「没有」。
- **判据刻意偏向过度匹配。** 多认出一个进程的后果是拒绝启动(安全);漏认一个会起第二个 Core(灾难)。**绝不用「可执行路径 == 当前 Core 路径」做判据**——更新后旧版 Core 的路径不同,会被漏认。
- `spawned` 与 `owned` 两种记录的既有处理**一个字不改**(它们有真实 PID,`inspectProcess` 已能求证)。
- 不碰 `Start()` 其余的 fail-closed 判定、不碰两段式标记、不碰 Linux/Windows。
- **绝不启动 bx / 改路由 / 跑 `bx up`。** 验证只跑编译与单测。
- TDD:先写失败测试→跑红→最小实现→跑绿。中文 conventional commits,结尾带 `Co-Authored-By: Claude …`。在默认分支直接提交。
- 验证:`go build ./... && go vet ./... && go test ./...`;跨平台 `GOOS=linux/windows GOARCH=amd64 go build -o /dev/null ./...`。

---

### Task 1: 判据的纯逻辑(procargs2 解析 + 「像不像 Core」)

**Files:**
- Create: `internal/guardian/procscan_darwin.go`
- Test: `internal/guardian/procscan_darwin_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `func parseProcArgs(raw []byte) (executable string, argv []string, err error)`
  - `func looksLikeCore(executable string, argv []string, uid int) bool`

**为什么先做纯逻辑:** 这两个函数装着本设计**唯一容易出错的判断**。真正的枚举是机械的,而判据错了会直接导致双 Core。

- [ ] **Step 1: 写失败测试**

创建 `internal/guardian/procscan_darwin_test.go`:

```go
//go:build darwin

package guardian

import (
	"encoding/binary"
	"testing"
)

// buildProcArgs 造一份 kern.procargs2 的字节布局:
// [4 字节 argc][可执行路径 NUL][若干填充 NUL][argv[0] NUL][argv[1] NUL]…
func buildProcArgs(executable string, argv []string, padding int) []byte {
	raw := make([]byte, 4)
	binary.NativeEndian.PutUint32(raw, uint32(len(argv)))
	raw = append(raw, []byte(executable)...)
	raw = append(raw, 0)
	for i := 0; i < padding; i++ {
		raw = append(raw, 0)
	}
	for _, arg := range argv {
		raw = append(raw, []byte(arg)...)
		raw = append(raw, 0)
	}
	return raw
}

// buildTruncatedProcArgs 造一份最后一项缺 NUL 结尾的缓冲区(模拟内核缓冲被截断)。
func buildTruncatedProcArgs(executable string, complete []string, truncated string) []byte {
	raw := buildProcArgs(executable, complete, 1)
	binary.NativeEndian.PutUint32(raw[:4], uint32(len(complete)+1))
	return append(raw, []byte(truncated)...)
}

func TestParseProcArgsReadsExecutableAndArgv(t *testing.T) {
	raw := buildProcArgs("/Library/Application Support/bx/runtime/dev/bx",
		[]string{"/usr/local/bin/bx", "run", "-c", "/etc/bx/config.yaml"}, 3)

	executable, argv, err := parseProcArgs(raw)
	if err != nil {
		t.Fatalf("parseProcArgs 失败: %v", err)
	}
	if executable != "/Library/Application Support/bx/runtime/dev/bx" {
		t.Errorf("executable = %q", executable)
	}
	if len(argv) != 4 || argv[1] != "run" || argv[3] != "/etc/bx/config.yaml" {
		t.Errorf("argv = %q, want 完整四项", argv)
	}
}

// 截断/畸形的缓冲区必须报错,不能返回一个看起来合理的空结果——
// 那会让上层以为「这个进程不像 Core」,进而漏认。
func TestParseProcArgsRejectsMalformed(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  []byte
	}{
		{"太短", []byte{1, 2}},
		{"只有 argc 没有路径", []byte{1, 0, 0, 0}},
		{"路径没有 NUL 结尾", append([]byte{1, 0, 0, 0}, []byte("/usr/local/bin/bx")...)},
		// 以下两种是「看起来合理的残缺结果」——最危险的一类:静默返回会让一个
		// 真的 Core 被判成「不是 Core」。
		{"argc 说有 2 项,路径之后一个字节都没有",
			append(append([]byte{2, 0, 0, 0}, []byte("/usr/local/bin/bx")...), 0)},
		{"最后一项没有 NUL 结尾(缓冲区被截断)",
			buildTruncatedProcArgs("/usr/local/bin/bx", []string{"bx"}, "ru")},
		{"argc 为 0", append(append([]byte{0, 0, 0, 0}, []byte("/usr/local/bin/bx")...), 0)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := parseProcArgs(tt.raw); err == nil {
				t.Error("畸形输入必须报错")
			}
		})
	}
}

// 判据的核心:认得出 Core,且**不靠具体路径**认。
func TestLooksLikeCoreIgnoresExecutablePath(t *testing.T) {
	// 更新之后旧版 Core 跑在另一个路径下。用「路径 == 当前 Core 路径」做判据
	// 会漏认它,于是起第二个 Core —— 正是 af81632 被回退的那个风险。
	for _, executable := range []string{
		"/Library/Application Support/bx/runtime/dev/bx",
		"/Library/Application Support/bx/runtime/v0.2.7/bx",
		"/usr/local/bin/bx",
	} {
		argv := []string{executable, "run", "-c", "/etc/bx/config.yaml"}
		if !looksLikeCore(executable, argv, 0) {
			t.Errorf("%s 必须被认成 Core —— 判据不能依赖具体路径", executable)
		}
	}
}

func TestLooksLikeCoreRejectsNonCore(t *testing.T) {
	for _, tt := range []struct {
		name       string
		executable string
		argv       []string
		uid        int
	}{
		{"非 root", "/usr/local/bin/bx", []string{"bx", "run", "-c", "x"}, 501},
		{"不是 run 子命令", "/usr/local/bin/bx", []string{"bx", "status", "--json"}, 0},
		{"没有子命令", "/usr/local/bin/bx", []string{"bx"}, 0},
		{"别的程序", "/usr/bin/ssh", []string{"ssh", "run"}, 0},
		{"argv 为空", "/usr/local/bin/bx", nil, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if looksLikeCore(tt.executable, tt.argv, tt.uid) {
				t.Errorf("不该认成 Core:%s %q uid=%d", tt.executable, tt.argv, tt.uid)
			}
		})
	}
}

// 经软链或改名调用时,argv[0] 仍然叫 bx —— 偏向过度匹配,认出来。
func TestLooksLikeCoreAcceptsBxViaArgv0(t *testing.T) {
	if !looksLikeCore("/opt/weird/path/bx-renamed", []string{"bx", "run", "-c", "x"}, 0) {
		t.Error("argv[0] 是 bx 就该认 —— 过度匹配的后果是 fail-closed,方向安全")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian/ -run 'TestParseProcArgs|TestLooksLikeCore' -count=1`
Expected: FAIL,`undefined: parseProcArgs` / `undefined: looksLikeCore`

- [ ] **Step 3: 实现**

创建 `internal/guardian/procscan_darwin.go`:

```go
//go:build darwin

package guardian

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
)

// parseProcArgs 解析 macOS 的 kern.procargs2 布局:
//
//	[4 字节 argc][可执行路径 NUL][若干填充 NUL][argv[0] NUL][argv[1] NUL]…
//
// 畸形输入一律报错,绝不返回「看起来合理的空结果」——那会让调用方以为这个进程
// 不像 Core,进而漏认一个真的 Core。
func parseProcArgs(raw []byte) (string, []string, error) {
	if len(raw) < 4 {
		return "", nil, errors.New("procargs2 too short")
	}
	argc := int(binary.NativeEndian.Uint32(raw[:4]))
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end <= 0 {
		return "", nil, errors.New("procargs2 has no executable path")
	}
	executable := string(rest[:end])
	rest = rest[end:]
	for len(rest) > 0 && rest[0] == 0 { // 路径与 argv[0] 之间的填充
		rest = rest[1:]
	}
	if argc < 1 {
		// 每个真实进程至少有 argv[0]。argc 为 0 意味着这份缓冲区没解析成功,
		// 而「没解析成功」绝不能被上层读成「这不是 Core」。
		return "", nil, errors.New("procargs2 reports no arguments")
	}
	argv := make([]string, 0, argc)
	for i := 0; i < argc; i++ {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			// 缓冲区在凑够 argc 项之前就断了(内核缓冲竞态、截断)。
			// 静默返回残缺结果会让一个真的 Core 被判成「不是 Core」——正是本任务
			// 要防的漏认。宁可报错让调用方 fail-closed。
			return "", nil, fmt.Errorf("procargs2 truncated: got %d of %d arguments", i, argc)
		}
		argv = append(argv, string(rest[:end]))
		rest = rest[end+1:]
	}
	return executable, argv, nil
}

// looksLikeCore 判定一个进程是不是 bx 的 Core。
//
// **刻意不依赖具体可执行路径。** 更新之后旧版 Core 跑在 runtime/<旧版本>/bx 下,
// 用「路径 == 当前 Core 路径」做判据会漏认它,于是起第二个 Core——正是 af81632
// 被回退的那个双 Core 风险。
//
// 也刻意偏向过度匹配:多认一个的后果是拒绝启动(安全),漏认一个是灾难。用户手工
// `sudo bx run` 会被认出来,而那本来就是一个 Core,认出来是对的。
func looksLikeCore(executable string, argv []string, uid int) bool {
	if uid != 0 {
		return false
	}
	if len(argv) < 2 || argv[1] != "run" {
		return false
	}
	if filepath.Base(executable) == "bx" {
		return true
	}
	return filepath.Base(argv[0]) == "bx"
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/guardian/ -run 'TestParseProcArgs|TestLooksLikeCore' -count=1 -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
go build ./... && go vet ./... && go test ./internal/guardian/ -count=1
git add internal/guardian/procscan_darwin.go internal/guardian/procscan_darwin_test.go
git commit -m "feat(guardian): Core 进程判据的纯逻辑(procargs2 解析与识别)"
```

---

### Task 2: 扫描能力接入 ExecCoreRunner,替换两处无条件拒绝

**Files:**
- Modify: `internal/guardian/procscan_darwin.go`(加 `scanRunningCores`)
- Create: `internal/guardian/procscan_other.go`(`!darwin` 桩)
- Modify: `internal/guardian/process.go`(新字段 + `Existing()` + `refuseLiveLaunchMarker`)
- Test: `internal/guardian/procscan_darwin_test.go`(追加冒烟)、`internal/guardian/spawn_marker_test.go`(追加行为测试)

**Interfaces:**
- Consumes: Task 1 的 `parseProcArgs` / `looksLikeCore`
- Produces:
  - `func scanRunningCores() ([]Process, error)`(darwin);`!darwin` 返回 `errCoreScanUnsupported`
  - `ExecCoreRunner` 新字段 `ScanRunningCores func() ([]Process, error)`(nil 时用平台默认)

**照既有模式:** `ExecCoreRunner` 已有 `SaveProcessRecord` / `RemoveProcessRecord` / `ShutdownCore` 这类可注入函数字段,新字段照抄这个形状,测试可注入假实现。

- [ ] **Step 1: 写失败测试**

在 `internal/guardian/spawn_marker_test.go` 末尾追加:

```go
// launching 标记 + 系统里没有任何 Core 在跑 ⇒ 那个标记确实是孤儿,可安全自愈。
//
// 这是本期的核心:此前无论如何都判 uncertain,于是一个残留标记让 bx up 永久失败,
// 只能靠 bx uninstall 脱身(真机 2026-08-05 / 2026-08-06 各一次)。
// 判据不再来自我们自己的记账,而是向系统求证——fork 一返回子进程就存在,
// 早于它执行我们的任何代码,所以这个判据在「fork 与写盘之间」那个窗口里仍然有效。
func TestExistingHealsLaunchingMarkerWhenNoCoreRunning(t *testing.T) {
	runner, _ := newSpawnMarkerRunner(t, &spawnMarkerOperations{})
	runner.ScanRunningCores = func() ([]Process, error) { return nil, nil }
	if err := saveProcessRecord(runner.StatePath, processRecord{State: processRecordLaunching}); err != nil {
		t.Fatal(err)
	}
	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatalf("系统里没有 Core 时 launching 标记必须自愈,实际: %v", err)
	}
	if process.PID != 0 {
		t.Fatalf("不该产出既有 Core,实际 = %+v", process)
	}
}

// launching 标记 + 系统里真有一个 Core ⇒ 仍然 fail-closed,且必须把 PID 说出来。
func TestExistingRefusesLaunchingMarkerWhenCoreIsRunning(t *testing.T) {
	runner, _ := newSpawnMarkerRunner(t, &spawnMarkerOperations{})
	runner.ScanRunningCores = func() ([]Process, error) {
		return []Process{{PID: 4242, Executable: "/usr/local/bin/bx", UID: 0}}, nil
	}
	if err := saveProcessRecord(runner.StatePath, processRecord{State: processRecordLaunching}); err != nil {
		t.Fatal(err)
	}
	_, err := runner.Existing(context.Background())
	if !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("系统里有 Core 时必须 fail-closed,实际 = %v", err)
	}
	if !strings.Contains(err.Error(), "4242") {
		t.Errorf("错误必须点出那个 PID(用户要靠它判断怎么处置),实际 = %v", err)
	}
}

// 扫描本身失败 ⇒ 保持 uncertain。「问不出来」不等于「没有」。
func TestExistingRefusesLaunchingMarkerWhenScanFails(t *testing.T) {
	runner, _ := newSpawnMarkerRunner(t, &spawnMarkerOperations{})
	runner.ScanRunningCores = func() ([]Process, error) { return nil, errors.New("sysctl failed") }
	if err := saveProcessRecord(runner.StatePath, processRecord{State: processRecordLaunching}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Existing(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("扫描失败时必须保持 fail-closed,实际 = %v", err)
	}
}

// Existing 放行只是第一跳:Start 紧接着看到同一个文件也必须放行,
// 否则用户可见行为与修复前一模一样(60b76f3 的教训)。
func TestStartProceedsPastLaunchingMarkerWhenNoCoreRunning(t *testing.T) {
	started := newStartTestProcess(71)
	t.Cleanup(started.release)
	ops := &spawnMarkerOperations{started: started}
	runner, executable := newSpawnMarkerRunner(t, ops)
	ops.process = Process{PID: 71, Executable: executable, UID: 0, Generation: "darwin:5:5"}
	runner.ScanRunningCores = func() ([]Process, error) { return nil, nil }
	if err := saveProcessRecord(runner.StatePath, processRecord{State: processRecordLaunching}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Start(context.Background(), CoreStartOptions{}); err != nil {
		t.Fatalf("孤儿 launching 标记不得卡死 Start:%v", err)
	}
	if ops.startCount() != 1 {
		t.Fatalf("应当真的启动了 Core,实际启动 %d 次", ops.startCount())
	}
}

// 而系统里有 Core 时,Start 仍必须拒绝——绝不允许起第二个。
func TestStartRefusesLaunchingMarkerWhenCoreIsRunning(t *testing.T) {
	ops := &spawnMarkerOperations{started: newStartTestProcess(72)}
	runner, _ := newSpawnMarkerRunner(t, ops)
	runner.ScanRunningCores = func() ([]Process, error) {
		return []Process{{PID: 4242, Executable: "/usr/local/bin/bx", UID: 0}}, nil
	}
	if err := saveProcessRecord(runner.StatePath, processRecord{State: processRecordLaunching}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Start(context.Background(), CoreStartOptions{}); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Start = %v, want ErrProcessOwnershipUncertain", err)
	}
	if ops.startCount() != 0 {
		t.Fatalf("拒绝时不得启动 Core,实际启动 %d 次", ops.startCount())
	}
}
```

在 `internal/guardian/procscan_darwin_test.go` 末尾追加冒烟测试:

```go
// 真实扫描必须能在本机跑通且不 panic。它至少应该看到本测试进程之外的东西,
// 但不断言具体内容——那取决于机器状态。这条只证明 syscall 路径是活的。
func TestScanRunningCoresDoesNotFailOnRealSystem(t *testing.T) {
	if _, err := scanRunningCores(); err != nil {
		t.Fatalf("在真实系统上扫描不应失败: %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian/ -run 'TestExistingHealsLaunching|TestExistingRefusesLaunching|TestStartProceedsPastLaunching|TestStartRefusesLaunching|TestScanRunningCores' -count=1`
Expected: FAIL,`runner.ScanRunningCores undefined` / `undefined: scanRunningCores`

- [ ] **Step 3: 实现扫描(darwin)**

在 `internal/guardian/procscan_darwin.go` 末尾追加:

```go
// scanRunningCores 枚举系统里所有在跑 bx Core 的进程。
//
// 单个进程读不出信息(权限、刚退出)就跳过它,不让整次扫描失败——但**整体
// sysctl 失败必须报错**,由调用方保持 fail-closed。
func scanRunningCores() ([]Process, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("enumerate processes: %w", err)
	}
	var cores []Process
	for i := range procs {
		pid := int(procs[i].Proc.P_pid)
		if pid <= 0 {
			continue
		}
		raw, err := unix.SysctlRaw("kern.procargs2", pid)
		if err != nil {
			continue // 权限不足或进程刚退出:跳过这一个
		}
		executable, argv, err := parseProcArgs(raw)
		if err != nil {
			continue
		}
		uid := int(procs[i].Eproc.Ucred.Uid)
		if !looksLikeCore(executable, argv, uid) {
			continue
		}
		cores = append(cores, Process{PID: pid, Executable: executable, UID: uid})
	}
	return cores, nil
}
```

同文件顶部的 import 补 `"fmt"` 与 `"golang.org/x/sys/unix"`。

创建 `internal/guardian/procscan_other.go`:

```go
//go:build !darwin

package guardian

import "errors"

// errCoreScanUnsupported 让非 darwin 平台保持既有的无条件 fail-closed:
// 这套标记机制在实践中只有 macOS 用得到(Guardian 只有 darwin 实体),
// 在别处放宽没有收益,只有风险。
var errCoreScanUnsupported = errors.New("core process scan is only implemented on darwin")

func scanRunningCores() ([]Process, error) { return nil, errCoreScanUnsupported }
```

- [ ] **Step 4: 接进 ExecCoreRunner**

在 `internal/guardian/process.go` 的 `ExecCoreRunner` 结构体里,`RemoveProcessRecord` 那一行**之后**追加:

```go
	// ScanRunningCores 向系统求证「有没有进程在跑 Core」。nil 时用平台默认实现。
	// 抽成可注入字段是为了让 launching 标记的处置能被单测覆盖。
	ScanRunningCores func() ([]Process, error)
```

在同文件里加一个辅助方法(放在 `refuseLiveLaunchMarker` 之前):

```go
// resolveOrphanLaunchMarker 判定一个 launching 标记是不是孤儿。
//
// launching 标记的 PID 恒为 0,无法向 OS 求证——此前因此一律判 uncertain,
// 于是一个残留标记就让 bx up 永久失败,只能靠 bx uninstall 脱身(真机
// 2026-08-05 / 2026-08-06 各一次)。
//
// 现在改为绕开自己的记账、直接问系统。这个判据在「fork 与写盘之间」那个窗口里
// 仍然有效:fork 一返回子进程就已经作为进程存在,早于它执行我们的任何代码。
//
// 返回的 error 非 nil 表示必须继续 fail-closed。
func (r *ExecCoreRunner) resolveOrphanLaunchMarker(record processRecord) error {
	scan := r.ScanRunningCores
	if scan == nil {
		scan = scanRunningCores
	}
	cores, err := scan()
	if err != nil {
		// 问不出来不等于没有。
		return uncertainOwnership(
			Process{PID: record.PID, Executable: record.Executable, Generation: record.Generation, Uncertain: true},
			fmt.Errorf("durable launch marker with no PID, and scanning for running Cores failed: %w", err))
	}
	if len(cores) > 0 {
		pids := make([]string, 0, len(cores))
		for _, core := range cores {
			pids = append(pids, strconv.Itoa(core.PID))
		}
		return uncertainOwnership(
			Process{PID: cores[0].PID, Executable: cores[0].Executable, Uncertain: true},
			fmt.Errorf("durable launch marker with no PID, and Core appears to be running (PID %s)", strings.Join(pids, ", ")))
	}
	log.Printf("guardian_orphan_launch_marker no_core_running=true (self-healing)")
	return nil
}
```

`process.go` 的 import 需要 `"strconv"`(`strings`、`log`、`fmt` 已在)。

- [ ] **Step 5: 替换 Existing() 里的无条件拒绝**

在 `internal/guardian/process.go` 的 `Existing()` 里,把 `case processRecordLaunching:` 那一支整体替换:

```go
	case processRecordLaunching:
		// PID 为 0,无法向 OS 求证这条记录本身——改为绕开记账问系统:
		// 系统里没有任何 Core 在跑 ⇒ 这个标记是孤儿,清掉当作无既有 Core。
		if err := r.resolveOrphanLaunchMarker(record); err != nil {
			return Process{}, err
		}
		if clearErr := r.removeStaleRecord(record); clearErr != nil {
			log.Printf("guardian_stale_launch_marker clear_failed=%v", clearErr)
		}
		return Process{}, nil
```

- [ ] **Step 6: 替换 refuseLiveLaunchMarker 里的无条件拒绝**

在同文件 `refuseLiveLaunchMarker` 里,把

```go
	if record.PID <= 0 {
		return uncertain(errors.New("durable launch marker already exists"))
	}
```

替换成

```go
	if record.PID <= 0 {
		// 与 Existing() 同一判据:Existing 放行而 Start 拒绝,等于没修
		// (60b76f3 的教训:只修一跳是假绿)。
		return r.resolveOrphanLaunchMarker(record)
	}
```

同时把该函数上方注释里「a marker with no PID … still refuses」那句改成描述新行为:PID 为 0 时改为向系统求证,系统里没有 Core 才放行。

- [ ] **Step 7: 处理三条会变红的既有测试**

本期**故意改变了 `launching` 标记的语义**,所以下面三条钉旧语义的测试必然变红。
它们各自守着真实的安全属性,**处理方式是把断言搬到新判据上,不是删掉或放宽**。

先跑一次看红:`go test ./internal/guardian/ -count=1`

**(a) `spawn_marker_test.go` 的 `TestExistingStillRefusesLaunchingMarker`**
它钉的是「launching 一律 fail-closed」,而本期正是要改这一条。Task 2 Step 1 新增的
三条测试(无 Core→自愈 / 有 Core→拒绝 / 扫描失败→拒绝)已经完整覆盖了它的意图,
且更精确。**删除这一条**,并在文件顶部那段说明里把「launching 仍然必须 fail-closed」
改成描述新判据。

**(b) `process_test.go` 的 `TestExecCoreRunnerStartStillRefusesLiveLaunchMarker`**
它的表里有一例「launching 标记没有 PID,无从向 OS 求证」。现在**问得出来了**。
把该表项改成注入一个「系统里有 Core」的扫描:

```go
		{
			name:   "launching 标记且系统里有 Core 在跑",
			record: processRecord{State: processRecordLaunching},
			live:   map[int]Process{},
			scan:   func() ([]Process, error) { return []Process{{PID: 4242, Executable: "/usr/local/bin/bx", UID: 0}}, nil },
		},
```

并在该测试的 runner 构造处加上 `runner.ScanRunningCores = tt.scan`(表结构体加一个
`scan func() ([]Process, error)` 字段;其余表项该字段为 nil,行为不变)。

**(c) `process_test.go` 的 `TestExecCoreRunnerPersistenceFailureLeavesDurableUncertainLaunchMarker`**
这条最要紧,**绝不能弱化**。它守的是:Core 已经 fork 出来、但记录写不进去、清理
结果又不确定时,不得声称自己没有 Core。它现在会红,是因为它启动的是一个**假**
Core 对象,而真实扫描看不见假对象——测试的保真度而非意图出了问题。

修法:给它注入一个「系统里有 Core」的扫描,与它模拟的场景一致:

```go
	runner.ScanRunningCores = func() ([]Process, error) {
		return []Process{{PID: 52, Executable: executable, UID: 0}}, nil
	}
```

断言一个字不改。这样它守的属性不变,而且比原来更贴近真实——原来它是靠「无条件
拒绝」偶然成立的,现在是靠「系统里确实有一个 Core」成立的。

**如果改完它仍然红,停下来报告,不许调整它的断言。**

- [ ] **Step 7b: 跑测试确认全绿**

```bash
go test ./internal/guardian/ -count=1
go build ./... && go vet ./...
```
Expected: 全绿

- [ ] **Step 8: 跨平台与提交**

```bash
GOOS=linux   GOARCH=amd64 go build -o /dev/null ./...
GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
go test ./... -count=1
git add internal/guardian/
git commit -m "fix(guardian): 孤儿 launching 标记改为向系统求证,不再永久卡死 bx up"
```

---

### Task 3: 文档与全量验证

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: 在 macOS 段落补记**

写明:

- `launching` 标记(`PID==0`)的处置改为**向系统求证**:枚举进程,看有没有
  `basename==bx && argv[1]=="run" && uid==0` 的进程。没有 ⇒ 孤儿,自愈;有 ⇒ 仍
  fail-closed 并**报出 PID**;扫描失败 ⇒ 保持 fail-closed(「问不出来」不等于「没有」)。
- **判据刻意不依赖可执行路径**:更新后旧版 Core 跑在 `runtime/<旧版本>/bx`,
  用路径匹配会漏认它并起第二个 Core——正是 `af81632` 被回退的那个风险换了个入口。
  过度匹配的后果是拒绝启动(安全),漏认是灾难。
- 为什么这个判据成立:**fork 一返回子进程就已存在**,早于它执行我们的任何代码,
  所以它在「fork 与写盘之间」那个窗口里仍然有效——而两段式标记(`e7e413c`)只能
  缩小窗口,消灭不了它。
- 与 `internal/observe` 同一条原则:**向系统现问事实,不信自己的记账**。
- 平台:darwin 实现 + `!darwin` 桩(桩保持既有 fail-closed)。
- **真机未验**。

- [ ] **Step 2: 全量验证**

```bash
go build ./... && go vet ./... && go test ./... -count=1
go test -race ./internal/guardian -count=1
GOOS=linux   GOARCH=amd64 go build -o /dev/null ./...
GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
GOOS=darwin  GOARCH=arm64 go build -o /dev/null ./...
bash scripts/test-macos-menu.sh
git diff --check
```
Expected: 全部 rc=0

- [ ] **Step 3: 提交并停在重装之前**

```bash
git add CLAUDE.md
git commit -m "docs: 记录 Core 所有权改为向系统求证"
```

**不要重装、不要启动 bx、不要碰用户机器上运行中的实例。** 向用户报告验证结果,
并说明真机验收怎么做:

```bash
# 人为造一个孤儿 launching 标记,验证 bx up 不再永久失败
sudo bx down
printf '{"pid":0,"executable":"","generation":"","state":"launching"}' | sudo tee /var/lib/bx/core-process.json
sudo bx up          # 修复前:500 core_ownership_uncertain;修复后:正常起来
sudo tail -5 /var/log/bx-guard.err.log | grep orphan_launch_marker
```
