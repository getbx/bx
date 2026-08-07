package guardian

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 两段式启动标记:把「还没 fork」与「已 fork 但还没验明身份」拆成两个可区分的
// 磁盘形态。
//
// 此前只有一个 launching 标记(PID 恒为 0),它同时承担这两种含义,于是只能
// 一律判为所有权不确定 —— 结果是真机上一旦残留就永久卡死 bx up,只有
// bx uninstall 能脱身(事故 2026-08-05 / 2026-08-06)。
//
// 拆开之后:
//   spawned  (PID>0) ⇒ 已 fork、身份未验 ⇒ 问 OS:死了自愈,活着仍 fail-closed
//   launching(PID==0)⇒ 无法向 OS 求证这条记录本身,但可以绕开它、直接问系统
//                      「有没有任何进程在跑 bx Core」(ScanRunningCores):
//                      没有 ⇒ 孤儿标记,自愈;有 ⇒ 仍然 fail-closed;
//                      问不出来(扫描失败)⇒ 同样 fail-closed。
//
// 本次真正的收益是把残留窗口从「fork → Inspect → verify → 写 owned」缩短到
// 「fork → 一次写盘」:窗口外的崩溃现在留下带真实 PID、可向 OS 求证的 spawned
// 记录,不再是无从判断的 launching。而 launching 本身也不再无条件卡死——它
// 改向系统求证「有没有在跑我们的 Core 可执行文件的进程」,见
// docs/superpowers/plans/2026-08-05-guardian-launch-marker-deadlock.md。

type spawnMarkerOperations struct {
	mu        sync.Mutex
	started   StartedProcess
	process   Process
	live      map[int]Process
	starts    int
	onInspect func(pid int)
}

func (o *spawnMarkerOperations) Start(string, []string, []string) (StartedProcess, error) {
	o.mu.Lock()
	o.starts++
	o.mu.Unlock()
	return o.started, nil
}

func (o *spawnMarkerOperations) Inspect(pid int) (Process, error) {
	o.mu.Lock()
	hook := o.onInspect
	o.mu.Unlock()
	if hook != nil {
		hook(pid)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if process, ok := o.live[pid]; ok {
		return process, nil
	}
	if o.process.PID == pid {
		return o.process, nil
	}
	return Process{}, ErrProcessNotRunning
}

func (o *spawnMarkerOperations) startCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.starts
}

func newSpawnMarkerRunner(t *testing.T, ops ProcessOperations) (*ExecCoreRunner, string) {
	t.Helper()
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "core-process.json")
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = statePath
	runner.ControlSocket = filepath.Join(dir, "bx.sock")
	runner.Operations = ops
	return runner, executable
}

// Start 必须在 fork 一返回就把子进程 PID 落盘,而不是等到 Inspect/verify 都过了
// 才写。否则这中间任何一刻崩溃,盘上都只剩一个 PID==0 的标记,无从向 OS 求证。
func TestStartRecordsSpawnedPIDBeforeVerification(t *testing.T) {
	started := newStartTestProcess(52)
	t.Cleanup(started.release)
	ops := &spawnMarkerOperations{started: started}
	runner, executable := newSpawnMarkerRunner(t, ops)
	ops.process = Process{PID: 52, Executable: executable, UID: 0, Generation: "darwin:123:456"}

	var seen processRecord
	var seenErr error
	ops.onInspect = func(int) { seen, seenErr = loadProcessRecord(runner.StatePath) }

	if _, err := runner.Start(context.Background(), CoreStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if seenErr != nil {
		t.Fatalf("验明身份时盘上应已有记录,实际读取失败:%v", seenErr)
	}
	if seen.PID != 52 {
		t.Fatalf("fork 后立刻落盘的记录 PID = %d, want 52——否则崩在这里就只剩无 PID 的标记", seen.PID)
	}
}

// spawned 且进程还活着 ⇒ 有一个我们没验明身份的 Core 在跑 ⇒ 必须 fail-closed。
// 这条正是原先由 launching 承担的安全属性,拆分后由 spawned 接管,不得丢失。
func TestExistingRefusesSpawnedMarkerWhoseProcessIsAlive(t *testing.T) {
	ops := &spawnMarkerOperations{live: map[int]Process{7001: {PID: 7001, Executable: "/other/bx", UID: 0, Generation: "darwin:9:9"}}}
	runner, _ := newSpawnMarkerRunner(t, ops)
	if err := saveProcessRecord(runner.StatePath, processRecord{PID: 7001, State: processRecordSpawned}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Existing(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("spawned 标记且进程活着时 Existing = %v, want ErrProcessOwnershipUncertain", err)
	}
}

// spawned 但进程已被 OS 确认死亡 ⇒ 陈旧记录,自愈。
func TestExistingHealsSpawnedMarkerWhoseProcessIsDead(t *testing.T) {
	runner, _ := newSpawnMarkerRunner(t, &spawnMarkerOperations{})
	if err := saveProcessRecord(runner.StatePath, processRecord{PID: 7002, State: processRecordSpawned}); err != nil {
		t.Fatal(err)
	}
	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatalf("已死 spawned 记录不得让 Existing 失败:%v", err)
	}
	if process.PID != 0 {
		t.Fatalf("已死 spawned 记录不该产出既有 Core,实际 = %+v", process)
	}
}

// spawned 且进程已死时,Existing 放行只是第一跳:Start 紧接着看到同一个文件也
// 必须放行,否则用户可见行为与修复前一模一样(60b76f3 的教训:只修 Existing
// 一跳是假绿)。
func TestStartProceedsPastDeadSpawnedMarker(t *testing.T) {
	started := newStartTestProcess(53)
	t.Cleanup(started.release)
	ops := &spawnMarkerOperations{started: started}
	runner, executable := newSpawnMarkerRunner(t, ops)
	ops.process = Process{PID: 53, Executable: executable, UID: 0, Generation: "darwin:7:7"}
	// 7009 不在 live 里 ⇒ Inspect 报 ErrProcessNotRunning ⇒ OS 权威确认已死
	if err := saveProcessRecord(runner.StatePath, processRecord{PID: 7009, State: processRecordSpawned}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Start(context.Background(), CoreStartOptions{}); err != nil {
		t.Fatalf("进程已被 OS 确认死亡的 spawned 记录不得卡死 Start:%v", err)
	}
	if ops.startCount() != 1 {
		t.Fatalf("应当真的启动了 Core,实际启动 %d 次", ops.startCount())
	}
}

// 而 spawned + 活着仍必须拒绝启动:绝不允许起第二个 Core。
func TestStartRefusesSpawnedMarkerWhoseProcessIsAlive(t *testing.T) {
	ops := &spawnMarkerOperations{
		started: newStartTestProcess(6001),
		live:    map[int]Process{7003: {PID: 7003, Executable: "/other/bx", UID: 0, Generation: "darwin:3:3"}},
	}
	runner, _ := newSpawnMarkerRunner(t, ops)
	if err := saveProcessRecord(runner.StatePath, processRecord{PID: 7003, State: processRecordSpawned}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Start(context.Background(), CoreStartOptions{}); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Start = %v, want ErrProcessOwnershipUncertain", err)
	}
	if ops.startCount() != 0 {
		t.Fatalf("拒绝时不得启动 Core,实际启动 %d 次", ops.startCount())
	}
}

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
