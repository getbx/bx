package guardian

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCoreArgsUsesArgumentVector(t *testing.T) {
	got := coreArgs("/etc/bx/config.yaml", "127.0.0.1:53")
	want := []string{"run", "-c", "/etc/bx/config.yaml", "--listen-dns", "127.0.0.1:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("coreArgs() = %#v, want %#v", got, want)
	}
	got[0] = "changed"
	if next := coreArgs("/etc/bx/config.yaml", "127.0.0.1:53"); next[0] != "run" {
		t.Fatalf("coreArgs returned shared mutable storage: %#v", next)
	}
}

func TestProcessRecordRoundTripUsesRootOnlyMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core-process.json")
	want := processRecord{PID: 42, Executable: "/usr/local/bin/bx", Generation: "darwin:123:456"}
	if err := saveProcessRecord(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadProcessRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("record = %+v, want %+v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %#o, want 0600", got)
	}
}

func TestVerifyInstalledProcessAllowsAtomicExecutableReplacement(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "bx")
	if err := os.WriteFile(installed, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	process := Process{PID: 42, Executable: installed, UID: 0, Generation: "darwin:123:456"}
	replaceExecutableAtomically(t, installed)
	if err := verifyInstalledProcess(process, installed); err != nil {
		t.Fatalf("atomic executable replacement rejected live generation: %v", err)
	}
}

func TestVerifyInstalledProcessRequiresRootPathAndGeneration(t *testing.T) {
	installed := filepath.Join(t.TempDir(), "bx")
	valid := Process{PID: 42, Executable: installed, UID: 0, Generation: "darwin:123:456"}
	if err := verifyInstalledProcess(valid, installed); err != nil {
		t.Fatalf("valid process rejected: %v", err)
	}
	if err := verifyInstalledProcess(Process{PID: 42, Executable: installed, UID: 501, Generation: valid.Generation}, installed); err == nil {
		t.Fatal("non-root process accepted")
	}
	if err := verifyInstalledProcess(Process{PID: 42, Executable: "/tmp/not-bx", UID: 0, Generation: valid.Generation}, installed); err == nil {
		t.Fatal("different executable path accepted")
	}
	if err := verifyInstalledProcess(Process{PID: 42, Executable: installed, UID: 0}, installed); err == nil {
		t.Fatal("missing process generation accepted")
	}
}

func TestExecCoreRunnerStartPersistsInspectedGeneration(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := newStartTestProcess(52)
	t.Cleanup(started.release)
	operations := &startTestProcessOperations{
		started: started,
		process: Process{PID: 52, Executable: executable, UID: 0, Generation: "darwin:123:456"},
	}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.ScanRunningCores = noCoresRunning
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.Operations = operations

	process, err := runner.Start(context.Background(), CoreStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if process.Generation != operations.process.Generation {
		t.Fatalf("started generation = %q, want %q", process.Generation, operations.process.Generation)
	}
	record, err := loadProcessRecord(runner.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if record.Generation != operations.process.Generation {
		t.Fatalf("recorded generation = %q, want %q", record.Generation, operations.process.Generation)
	}
	if started.terminationCount() != 0 {
		t.Fatal("healthy started child was terminated")
	}
}

func TestExecCoreRunnerScopesBypassHandoffToAuthorizedStart(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(guardianBypassHandoffEnv, "203.0.113.99/32")

	for _, tt := range []struct {
		name    string
		options CoreStartOptions
		want    string
	}{
		{name: "authorized", options: CoreStartOptions{GuardianBypassHandoff: []string{"198.51.100.10/32"}}, want: guardianBypassHandoffEnv + "=198.51.100.10/32"},
		{name: "ordinary start strips ambient authorization"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			started := newStartTestProcess(52)
			t.Cleanup(started.release)
			operations := &startTestProcessOperations{
				started: started,
				process: Process{PID: 52, Executable: executable, UID: 0, Generation: "darwin:123:456"},
			}
			runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
			runner.ScanRunningCores = noCoresRunning
			runner.StatePath = filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-")+".json")
			runner.Operations = operations

			if _, err := runner.Start(context.Background(), tt.options); err != nil {
				t.Fatal(err)
			}
			got := operations.startEnvironment()
			for _, entry := range got {
				if strings.HasPrefix(entry, guardianBypassHandoffEnv+"=") && entry != tt.want {
					t.Fatalf("handoff environment = %q, want %q", entry, tt.want)
				}
			}
			if tt.want != "" {
				found := false
				for _, entry := range got {
					found = found || entry == tt.want
				}
				if !found {
					t.Fatalf("authorized handoff missing from environment: %#v", got)
				}
			}
			if tt.want == "" {
				for _, entry := range got {
					if strings.HasPrefix(entry, guardianBypassHandoffEnv+"=") {
						t.Fatalf("ordinary start inherited handoff authorization: %#v", got)
					}
				}
			}
		})
	}
}

func TestExecCoreRunnerStartAmbiguousGenerationTerminatesDirectChild(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := newStartTestProcess(52)
	t.Cleanup(started.release)
	operations := &startTestProcessOperations{
		started: started,
		process: Process{PID: 52, Executable: executable, UID: 0},
	}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.ScanRunningCores = noCoresRunning
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.Operations = operations

	if _, err := runner.Start(context.Background(), CoreStartOptions{}); err == nil {
		t.Fatal("Start accepted a child without immutable generation")
	}
	if got := started.terminationCount(); got != 1 {
		t.Fatalf("direct child termination calls = %d, want 1", got)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("ambiguous child invoked bare PID signal seam %d times", got)
	}
}

func TestExecCoreRunnerStartErrorClearsLaunchMarkerForRetry(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := newStartTestProcess(53)
	t.Cleanup(started.release)
	operations := &startTestProcessOperations{
		started:  started,
		startErr: errors.New("exec failed before child creation"),
		process:  Process{PID: 53, Executable: executable, UID: 0, Generation: "darwin:123:457"},
	}
	statePath := filepath.Join(dir, "core-process.json")
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.ScanRunningCores = noCoresRunning
	runner.StatePath = statePath
	runner.Operations = operations

	if _, err := runner.Start(context.Background(), CoreStartOptions{}); err == nil {
		t.Fatal("Start succeeded despite definitive pre-child error")
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("launch marker after definitive pre-child error = %v, want removed", err)
	}
	if existing, err := runner.Existing(context.Background()); err != nil || existing.PID != 0 {
		t.Fatalf("same-runner Existing = %+v, %v; want no owned process", existing, err)
	}

	operations.setStartError(nil)
	reconstructed := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	reconstructed.ScanRunningCores = noCoresRunning
	reconstructed.StatePath = statePath
	reconstructed.Operations = operations
	if _, err := reconstructed.Start(context.Background(), CoreStartOptions{}); err != nil {
		t.Fatalf("reconstructed retry after definitive pre-child error: %v", err)
	}
	if got := operations.startCount(); got != 2 {
		t.Fatalf("starts = %d, want failed initial call plus reconstructed retry", got)
	}
}

func TestExecCoreRunnerWaitClearsOwnedRecordBeforePublishingExit(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := newStartTestProcess(54)
	operations := &startTestProcessOperations{
		started: started,
		process: Process{PID: 54, Executable: executable, UID: 0, Generation: "darwin:123:458"},
	}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.ScanRunningCores = noCoresRunning
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.Operations = operations
	removeStarted := make(chan struct{})
	allowRemove := make(chan struct{})
	runner.RemoveProcessRecord = func(path string) error {
		close(removeStarted)
		<-allowRemove
		return removeProcessRecordFile(path)
	}

	process, err := runner.Start(context.Background(), CoreStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	started.release()
	select {
	case <-removeStarted:
	case <-time.After(time.Second):
		t.Fatal("Wait did not begin owned-record reconciliation")
	}
	select {
	case err := <-process.Exit:
		t.Fatalf("exit published before owned-record reconciliation: %v", err)
	default:
	}
	close(allowRemove)
	if err := <-process.Exit; err != nil {
		t.Fatalf("exit error = %v", err)
	}
	if _, err := os.Stat(runner.StatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned record after published exit = %v, want removed", err)
	}
}

// waitpid 已经返回 —— 这是「进程确定没了」最强的一种证明。握着它却因为删不掉
// 一份 JSON 记录就宣布所有权不确定,与 603b602 当初对 Existing() 的判断直接
// 冲突,而后果是一次文件系统抖动锁死 daemon。
//
// 这条测试原名 ...PublishesUncertainExit,编码的正是被推翻的那个行为。**不删,
// 改断言** —— 它守着的另外两件事(记录不许被静默丢掉、Existing 之后仍能继续)
// 依然有效。
func TestExecCoreRunnerRecordRemovalFailureAfterWaitIsNotOwnershipUncertainty(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := newStartTestProcess(55)
	operations := &startTestProcessOperations{
		started: started,
		process: Process{PID: 55, Executable: executable, UID: 0, Generation: "darwin:123:459"},
	}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.ScanRunningCores = noCoresRunning
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.Operations = operations
	runner.RemoveProcessRecord = func(string) error { return errors.New("record removal failed") }
	// 与 Watch 那条同理:这行日志由 wait goroutine 打,而 log 的输出目标是全局的。
	var logs syncBuffer
	restoreLog := swapGuardianLogOutput(&logs)
	defer restoreLog()

	process, err := runner.Start(context.Background(), CoreStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	started.release()
	if exitErr := <-process.Exit; errors.Is(exitErr, ErrProcessOwnershipUncertain) {
		t.Fatalf("waitpid 已经返回,却因为删不掉一份记录报了所有权不确定: %v", exitErr)
	}
	if !strings.Contains(logs.String(), "guardian_stale_core_record_after_exit") {
		t.Fatalf("清理失败必须留下线索,日志里没有:\n%s", logs.String())
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("owned record removed after failed reconciliation: %v", err)
	}
	// 记录仍在磁盘上(上面的清除失败),但进程已死:Existing 不应把"清不掉陈旧
	// 文件"当成"所有权不确定"——那会卡死 bx up。应视为无既有 Core 继续。
	operations.setInspectError(ErrProcessNotRunning)
	existing, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatalf("Existing after failed reconciliation = %v, want no error (treated as no existing Core)", err)
	}
	if existing.PID != 0 || existing.Uncertain {
		t.Fatalf("Existing after failed reconciliation = %+v, want zero-value Process", existing)
	}
}

func TestExecCoreRunnerPersistenceFailureLeavesDurableUncertainLaunchMarker(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := newUncertainStartTestProcess(52)
	operations := &startTestProcessOperations{
		started: started,
		process: Process{PID: 52, Executable: executable, UID: 0, Generation: "darwin:123:456"},
	}
	statePath := filepath.Join(dir, "core-process.json")
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = statePath
	runner.Operations = operations
	runner.LaunchCleanupTimeout = 10 * time.Millisecond
	runner.SaveProcessRecord = func(path string, record processRecord) error {
		if record.State == processRecordLaunching {
			return saveProcessRecord(path, record)
		}
		return errors.New("normal process record write failed")
	}
	// 这条测试模拟的场景是:Core 已经 fork 出来(PID 52)、但 spawned 记录写不
	// 进去、清理又不确定——盘上只留下一个 launching 标记,而那个 Core 其实还
	// 活着。真实扫描看不见这个测试里的假 Core 对象,所以必须注入一个与场景一致
	// 的扫描(而不是弱化断言):**fork 之前系统里没有 Core,fork 之后才有**。
	// 静态返回「一直有 Core」会让第一次 Start 在 fork 之前就被拒绝,后面模拟的
	// 残留窗口根本不会发生。
	runner.ScanRunningCores = func() ([]Process, error) {
		if operations.startCount() == 0 {
			return nil, nil
		}
		return []Process{{PID: 52, Executable: executable, UID: 0}}, nil
	}

	if _, err := runner.Start(context.Background(), CoreStartOptions{}); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Start error = %v, want uncertain ownership", err)
	}
	if got := started.terminationCount(); got != 1 {
		t.Fatalf("Terminate calls = %d, want 1", got)
	}
	record, err := loadProcessRecord(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != processRecordLaunching {
		t.Fatalf("marker state = %q, want %q", record.State, processRecordLaunching)
	}
	if _, err := runner.Existing(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("same-runner Existing error = %v, want uncertain ownership", err)
	}

	reconstructed := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	reconstructed.StatePath = statePath
	reconstructed.Operations = operations
	reconstructed.ScanRunningCores = runner.ScanRunningCores
	if _, err := reconstructed.Existing(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("reconstructed Existing error = %v, want uncertain ownership", err)
	}
	if got := operations.startCount(); got != 1 {
		t.Fatalf("starts after retry/reconstruction = %d, want 1", got)
	}
}

func TestExecCoreRunnerLateCleanupProofClearsMarkerForSameAndReconstructedRetry(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := newUncertainStartTestProcess(56)
	operations := &startTestProcessOperations{
		started: first,
		process: Process{PID: 56, Executable: executable, UID: 0, Generation: "darwin:123:460"},
	}
	statePath := filepath.Join(dir, "core-process.json")
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.ScanRunningCores = noCoresRunning
	runner.StatePath = statePath
	runner.Operations = operations
	runner.LaunchCleanupTimeout = 10 * time.Millisecond
	runner.SaveProcessRecord = func(path string, record processRecord) error {
		if record.State == processRecordLaunching {
			return saveProcessRecord(path, record)
		}
		return errors.New("normal process record write failed")
	}

	if _, err := runner.Start(context.Background(), CoreStartOptions{}); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("timed-out cleanup Start error = %v, want uncertain ownership", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("launch marker disappeared before Wait proved exit: %v", err)
	}
	first.release()
	eventually(t, func() bool {
		_, err := os.Stat(statePath)
		return errors.Is(err, os.ErrNotExist)
	})
	if existing, err := runner.Existing(context.Background()); err != nil || existing.PID != 0 {
		t.Fatalf("same-runner Existing after late proof = %+v, %v; want no process", existing, err)
	}
	reconstructed := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	reconstructed.ScanRunningCores = noCoresRunning
	reconstructed.StatePath = statePath
	reconstructed.Operations = operations
	if existing, err := reconstructed.Existing(context.Background()); err != nil || existing.PID != 0 {
		t.Fatalf("reconstructed Existing after late proof = %+v, %v; want no process", existing, err)
	}
}

func TestExecCoreRunnerAdoptedWatcherOutlivesInspectionContext(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	generation := "darwin:123:456"
	statePath := filepath.Join(dir, "core-process.json")
	if err := saveProcessRecord(statePath, processRecord{PID: 42, Executable: executable, Generation: generation}); err != nil {
		t.Fatal(err)
	}
	operations := &watchTestProcessOperations{process: Process{PID: 42, Executable: executable, UID: 0, Generation: generation}, alive: true}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.ScanRunningCores = noCoresRunning
	runner.StatePath = statePath
	runner.Operations = operations
	runner.InspectInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	process, err := runner.Existing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	process = runner.Watch(process)
	cancel()
	time.Sleep(20 * time.Millisecond)
	operations.setAlive(false)
	select {
	case <-process.Exit:
	case <-time.After(600 * time.Millisecond):
		t.Fatal("adopted Core exit was not observed after inspection context ended")
	}
}

func TestExecCoreRunnerExistingDoesNotStartWatcherBeforeAcceptance(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	t.Cleanup(func() {
		operations.setAlive(false)
		time.Sleep(3 * runner.InspectInterval)
	})

	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if process.Exit != nil {
		t.Fatal("Existing started an exit watcher before manager acceptance")
	}
	time.Sleep(4 * runner.InspectInterval)
	if got := operations.inspectCount(); got != 1 {
		t.Fatalf("pre-acceptance inspections = %d, want only the Existing inspection", got)
	}
}

func TestExecCoreRunnerAdoptedWatcherSurvivesAtomicExecutableReplacement(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	process = runner.Watch(process)
	replaceExecutableAtomically(t, runner.Executable())
	time.Sleep(4 * runner.InspectInterval)
	select {
	case err := <-process.Exit:
		t.Fatalf("watcher reported false exit after atomic replacement: %v", err)
	default:
	}
	operations.setAlive(false)
	select {
	case <-process.Exit:
	case <-time.After(time.Second):
		t.Fatal("watcher did not report definitive Core exit")
	}
}

func TestExecCoreRunnerExistingInspectionFailureRetainsRecord(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	operations.setInspectError(errors.New("inspect permission denied"))

	if _, err := runner.Existing(context.Background()); err == nil {
		t.Fatal("Existing succeeded despite ambiguous inspection")
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("process record removed after inspection failure: %v", err)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("inspection failure signalled PID %d times", got)
	}
}

func TestExecCoreRunnerExistingDefinitiveExitRemovesRecord(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	operations.setAlive(false)

	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if process.PID != 0 {
		t.Fatalf("Existing returned process %+v after definitive exit", process)
	}
	if _, err := os.Stat(runner.StatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("process record error = %v, want removed", err)
	}
}

// 进程已死却清不掉陈旧记录时,不应等价于"所有权不确定"——那会卡死 bx up。
// 真机事故:core-process.json 指向早已死亡的 PID,导致 up 持续失败(手工删文件后才恢复)。
func TestExecCoreRunnerExistingTreatsUnremovableDeadRecordAsNoCore(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	operations.setAlive(false)
	runner.RemoveProcessRecord = func(string) error { return errors.New("remove record: permission denied") }

	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatalf("死进程的陈旧记录不应让 Existing 报错: %v", err)
	}
	if process.PID != 0 || process.Uncertain {
		t.Errorf("应视为无既有 Core,实际 = %+v", process)
	}
}

// OS 已经确认被观察的 Core 没了(watchExisting 的两个调用点传的都是
// ErrProcessNotRunning 那一族),失败的只是删一个 JSON 文件。握着「安全」的
// 证明却宣布所有权不确定,等于让 /var/lib/bx 上任何一次文件系统抖动锁死 daemon。
//
// 日志接收端用 syncBuffer 而不是裸 bytes.Buffer:这行日志是 watcher goroutine
// 打的,而 log 的输出目标是全局的(hold_log_test.go 里那条注释记的就是这个坑)。
func TestAdoptedWatcherCleanupFailureIsNotOwnershipUncertainty(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	runner.RemoveProcessRecord = func(string) error { return errors.New("record removal failed") }
	var logs syncBuffer
	restore := swapGuardianLogOutput(&logs)
	defer restore()

	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	process = runner.Watch(process)
	operations.setAlive(false)

	select {
	case exitErr := <-process.Exit:
		if errors.Is(exitErr, ErrProcessOwnershipUncertain) {
			t.Fatalf("删不掉一份陈旧记录被报成了所有权不确定: %v", exitErr)
		}
		if !errors.Is(exitErr, ErrProcessNotRunning) {
			t.Fatalf("退出原因丢了: %v", exitErr)
		}
	case <-time.After(time.Second):
		t.Fatal("watcher 没有报告退出")
	}
	if !strings.Contains(logs.String(), "guardian_stale_core_record_after_exit") {
		t.Fatalf("清理失败必须留下线索,日志里没有:\n%s", logs.String())
	}
}

// **越界守卫。** Stop 那几处形状相似,但它们证明的东西不同(那是关闭途中对
// 一份**可能仍属于活进程**的记录的处置),本期一个字不动。这条测试存在的
// 唯一理由是:上一条的修法很容易被写成「把 uncertainOwnership 从这个文件里
// 全删掉」。
func TestStopStillReportsUncertaintyWhenItCannotClearTheRecord(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	runner.RemoveProcessRecord = func(string) error { return errors.New("record removal failed") }
	operations.setInspectError(ErrProcessNotRunning)

	if err := runner.Stop(context.Background(), process); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Stop 清不掉记录时 = %v, want 仍然报所有权不确定(本期不动这一处)", err)
	}
}

func TestExecCoreRunnerAdoptedWatcherIgnoresInspectionFailure(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	process = runner.Watch(process)
	operations.setInspectError(errors.New("transient sysctl failure"))
	time.Sleep(4 * runner.InspectInterval)
	select {
	case err := <-process.Exit:
		t.Fatalf("watcher ended on ambiguous inspection: %v", err)
	default:
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("watcher removed process record after transient error: %v", err)
	}

	operations.setInspectError(nil)
	operations.setAlive(false)
	select {
	case <-process.Exit:
	case <-time.After(time.Second):
		t.Fatal("watcher did not report definitive Core exit")
	}
}

func TestExecCoreRunnerStopUsesCooperativeShutdownWithoutSignal(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	shutdownCalls := 0
	runner.ShutdownCore = func(_ context.Context, socketPath string, expectedPID int) error {
		shutdownCalls++
		if socketPath != runner.ControlSocket || expectedPID != process.PID {
			t.Fatalf("shutdown request = (%q, %d), want (%q, %d)", socketPath, expectedPID, runner.ControlSocket, process.PID)
		}
		operations.setAlive(false)
		return nil
	}

	if err := runner.Stop(context.Background(), process); err != nil {
		t.Fatal(err)
	}
	if shutdownCalls != 1 {
		t.Fatalf("cooperative shutdown calls = %d, want 1", shutdownCalls)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("cooperative stop invoked legacy signal seam %d times", got)
	}
	if _, err := os.Stat(runner.StatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("process record error = %v, want removed", err)
	}
}

func TestExecCoreRunnerStopLegacyCoreFailsClosedWithoutSignal(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	runner.ShutdownCore = func(context.Context, string, int) error {
		return errors.New("shutdown endpoint returned 404")
	}

	if err := runner.Stop(context.Background(), process); err == nil {
		t.Fatal("legacy Core without shutdown endpoint was treated as stopped")
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("legacy Core invoked signal seam %d times", got)
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("legacy Core process record removed: %v", err)
	}
}

func TestExecCoreRunnerStopAmbiguousInspectionFailsClosedWithoutSignal(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	operations.setProcess(Process{PID: process.PID, Executable: process.Executable, UID: 0})
	shutdownCalls := 0
	runner.ShutdownCore = func(context.Context, string, int) error {
		shutdownCalls++
		return nil
	}

	if err := runner.Stop(context.Background(), process); err == nil {
		t.Fatal("Stop succeeded on ambiguous pre-shutdown identity")
	}
	if shutdownCalls != 0 {
		t.Fatalf("ambiguous identity received %d cooperative shutdown requests", shutdownCalls)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("ambiguous identity invoked signal seam %d times", got)
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("ambiguous process record removed: %v", err)
	}
}

func TestExecCoreRunnerStopDoesNotTreatAtomicReplacementAsExit(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	replaceExecutableAtomically(t, runner.Executable())
	shutdownCalls := 0
	runner.ShutdownCore = func(context.Context, string, int) error {
		shutdownCalls++
		return nil
	}

	err := runner.Stop(context.Background(), process)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop error = %v, want timeout while same generation remains alive", err)
	}
	if shutdownCalls != 1 {
		t.Fatalf("cooperative shutdown calls = %d, want 1", shutdownCalls)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("atomic replacement invoked signal seam %d times", got)
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("live process record removed after atomic replacement: %v", err)
	}
}

func TestExecCoreRunnerGenerationMismatchIsNotStopped(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	operations.setProcess(Process{
		PID:        process.PID,
		Executable: process.Executable,
		UID:        process.UID,
		Generation: "darwin:999:1",
	})
	shutdownCalls := 0
	runner.ShutdownCore = func(context.Context, string, int) error {
		shutdownCalls++
		return nil
	}

	if err := runner.Stop(context.Background(), process); err != nil {
		t.Fatalf("Stop returned error after recorded generation disappeared: %v", err)
	}
	if shutdownCalls != 0 {
		t.Fatalf("reused PID received %d cooperative shutdown requests", shutdownCalls)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("reused PID invoked signal seam %d times", got)
	}
}

func TestExecCoreRunnerExistingGenerationMismatchIsNotAdopted(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	operations.setProcess(Process{
		PID:        process.PID,
		Executable: process.Executable,
		UID:        process.UID,
		Generation: "darwin:999:1",
	})

	existing, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if existing.PID != 0 {
		t.Fatalf("reused PID was adopted: %+v", existing)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("reused PID invoked signal seam %d times", got)
	}
}

func TestExecCoreRunnerExistingAmbiguousGenerationRetainsRecord(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	operations.setProcess(Process{PID: process.PID, Executable: process.Executable, UID: process.UID})

	if _, err := runner.Existing(context.Background()); err == nil {
		t.Fatal("process without generation was adopted")
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("ambiguous process record removed: %v", err)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("ambiguous process invoked signal seam %d times", got)
	}
}

func TestExecCoreRunnerLegacyRecordFailsClosed(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	if err := writeJSONAtomically(runner.StatePath, processRecord{PID: 42, Executable: runner.Executable()}); err != nil {
		t.Fatal(err)
	}

	if _, err := runner.Existing(context.Background()); err == nil {
		t.Fatal("legacy record without generation was adopted")
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("legacy process record removed: %v", err)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("legacy record invoked signal seam %d times", got)
	}
}

func TestExecCoreRunnerStopWaitsForRecordedIdentityToDisappear(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	otherExecutable := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(otherExecutable, []byte("other"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner.ShutdownCore = func(context.Context, string, int) error {
		operations.setProcess(Process{PID: process.PID, Executable: otherExecutable, UID: 501, Generation: "darwin:999:1"})
		return nil
	}

	if err := runner.Stop(context.Background(), process); err != nil {
		t.Fatal(err)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("reused PID invoked signal seam %d times", got)
	}
}

func TestExecCoreRunnerSetExecutable(t *testing.T) {
	runner := NewExecCoreRunner("/a/bx", "/etc/bx/config.yaml", "127.0.0.1:53")
	runner.ScanRunningCores = noCoresRunning
	if runner.Executable() != "/a/bx" {
		t.Fatalf("initial executable = %q", runner.Executable())
	}
	if err := runner.SetExecutable("relative/bx"); err == nil {
		t.Fatal("relative path must be rejected")
	}
	if err := runner.SetExecutable("/b/bx"); err != nil {
		t.Fatal(err)
	}
	if runner.Executable() != "/b/bx" {
		t.Fatalf("swapped executable = %q", runner.Executable())
	}
}

func newRecordedProcessRunner(t *testing.T) (*ExecCoreRunner, Process, *watchTestProcessOperations) {
	t.Helper()
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	process := Process{PID: 42, Executable: executable, UID: 0, Generation: "darwin:123:456"}
	statePath := filepath.Join(dir, "core-process.json")
	if err := saveProcessRecord(statePath, processRecord{PID: process.PID, Executable: executable, Generation: process.Generation}); err != nil {
		t.Fatal(err)
	}
	operations := &watchTestProcessOperations{process: process, alive: true}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.ScanRunningCores = noCoresRunning
	runner.StatePath = statePath
	runner.ControlSocket = filepath.Join(dir, "bx.sock")
	runner.InspectInterval = 5 * time.Millisecond
	runner.StopTimeout = 100 * time.Millisecond
	runner.Operations = operations
	return runner, process, operations
}

func replaceExecutableAtomically(t *testing.T, path string) {
	t.Helper()
	replacement := path + ".replacement"
	if err := os.WriteFile(replacement, []byte("replacement binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
}

type watchTestProcessOperations struct {
	mu          sync.Mutex
	process     Process
	alive       bool
	inspectErr  error
	signals     int
	inspections int
}

type startTestProcess struct {
	pid          int
	wait         chan struct{}
	releaseOnce  sync.Once
	mu           sync.Mutex
	terminations int
}

func newStartTestProcess(pid int) *startTestProcess {
	return &startTestProcess{pid: pid, wait: make(chan struct{})}
}

func (p *startTestProcess) PID() int { return p.pid }

func (p *startTestProcess) Wait() error {
	<-p.wait
	return nil
}

func (p *startTestProcess) Terminate() error {
	p.mu.Lock()
	p.terminations++
	p.mu.Unlock()
	p.release()
	return nil
}

func (p *startTestProcess) release() {
	p.releaseOnce.Do(func() { close(p.wait) })
}

func (p *startTestProcess) terminationCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminations
}

type startTestProcessOperations struct {
	started     StartedProcess
	process     Process
	mu          sync.Mutex
	signals     int
	starts      int
	startErr    error
	inspectErr  error
	environment []string
}

func (o *startTestProcessOperations) Start(_ string, _ []string, environment []string) (StartedProcess, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.starts++
	o.environment = append([]string(nil), environment...)
	if o.startErr != nil {
		return nil, o.startErr
	}
	return o.started, nil
}

func (o *startTestProcessOperations) startEnvironment() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.environment...)
}

func (o *startTestProcessOperations) Inspect(int) (Process, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.inspectErr != nil {
		return Process{}, o.inspectErr
	}
	return o.process, nil
}

func (o *startTestProcessOperations) Signal(int, os.Signal) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.signals++
	return errors.New("unexpected signal")
}

func (o *startTestProcessOperations) signalCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.signals
}

func (o *startTestProcessOperations) startCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.starts
}

func (o *startTestProcessOperations) setStartError(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.startErr = err
}

func (o *startTestProcessOperations) setInspectError(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.inspectErr = err
}

func (o *startTestProcessOperations) setStarted(process StartedProcess, inspected Process) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.started = process
	o.process = inspected
}

type uncertainStartTestProcess struct {
	*startTestProcess
}

func newUncertainStartTestProcess(pid int) *uncertainStartTestProcess {
	return &uncertainStartTestProcess{startTestProcess: newStartTestProcess(pid)}
}

func (p *uncertainStartTestProcess) Terminate() error {
	p.mu.Lock()
	p.terminations++
	p.mu.Unlock()
	return errors.New("terminate failed")
}

func (*watchTestProcessOperations) Start(string, []string, []string) (StartedProcess, error) {
	return nil, errors.New("unexpected start")
}

func (o *watchTestProcessOperations) Inspect(int) (Process, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.inspections++
	if o.inspectErr != nil {
		return Process{}, o.inspectErr
	}
	if !o.alive {
		return Process{}, ErrProcessNotRunning
	}
	return o.process, nil
}

func (o *watchTestProcessOperations) inspectCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.inspections
}

func (o *watchTestProcessOperations) Signal(int, os.Signal) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.signals++
	return errors.New("unexpected signal")
}

func (o *watchTestProcessOperations) setAlive(alive bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.alive = alive
}

func (o *watchTestProcessOperations) setInspectError(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.inspectErr = err
}

func (o *watchTestProcessOperations) setProcess(process Process) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.process = process
}

func (o *watchTestProcessOperations) signalCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.signals
}

// pidAwareProcessOperations 按 PID 分别回答:陈旧记录里那个 PID 已死,新起的
// 进程活着。watchTestProcessOperations 无视 PID,测不出这个区分。
type pidAwareProcessOperations struct {
	mu       sync.Mutex
	dead     map[int]bool
	live     map[int]Process
	started  StartedProcess
	starts   int
	startErr error
}

func (o *pidAwareProcessOperations) Start(string, []string, []string) (StartedProcess, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.starts++
	if o.startErr != nil {
		return nil, o.startErr
	}
	return o.started, nil
}

func (o *pidAwareProcessOperations) Inspect(pid int) (Process, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.dead[pid] {
		return Process{}, ErrProcessNotRunning
	}
	if process, ok := o.live[pid]; ok {
		return process, nil
	}
	return Process{}, ErrProcessNotRunning
}

func (o *pidAwareProcessOperations) startCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.starts
}

// Existing() 放行陈旧死记录只是第一跳:Start() 紧接着看到同一个文件还在,
// 就以 "durable launch marker already exists" 拒绝 → 又是
// core_ownership_uncertain,用户可见行为与修复前一模一样。这条测试覆盖
// Existing→Start 这一对(此前只测了 Existing 一跳,是假绿)。
func TestExecCoreRunnerStartOverwritesOSConfirmedDeadLaunchMarker(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "core-process.json")
	if err := saveProcessRecord(statePath, processRecord{PID: 5129, Executable: executable, Generation: "darwin:1785895536:393862", State: processRecordOwned}); err != nil {
		t.Fatal(err)
	}
	started := newStartTestProcess(6001)
	defer started.release()
	operations := &pidAwareProcessOperations{
		dead:    map[int]bool{5129: true},
		live:    map[int]Process{6001: {PID: 6001, Executable: executable, UID: 0, Generation: "darwin:1785999999:1"}},
		started: started,
	}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.ScanRunningCores = noCoresRunning
	runner.StatePath = statePath
	runner.ControlSocket = filepath.Join(dir, "bx.sock")
	runner.Operations = operations
	// 真机事故形态:记录清不掉(权限/只读),文件留在磁盘上。
	runner.RemoveProcessRecord = func(string) error { return errors.New("remove record: permission denied") }

	existing, err := runner.Existing(context.Background())
	if err != nil || existing.PID != 0 {
		t.Fatalf("Existing = (%+v, %v), want zero-value Process 与 nil error", existing, err)
	}
	process, err := runner.Start(context.Background(), CoreStartOptions{})
	if err != nil {
		t.Fatalf("Start 仍被陈旧启动标记挡住(用户可见行为与修复前相同): %v", err)
	}
	if process.PID != 6001 {
		t.Fatalf("Start 返回 PID = %d, want 6001", process.PID)
	}
	if operations.startCount() != 1 {
		t.Fatalf("Core 启动次数 = %d, want 1", operations.startCount())
	}
}

// fail-closed 不得被削弱:记录里的进程还活着(身份是否匹配都一样)时,
// Start 仍必须拒绝——那是"另一个 Core 可能正在跑"的真实所有权存疑。
func TestExecCoreRunnerStartStillRefusesLiveLaunchMarker(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "core-process.json")
	tests := []struct {
		name   string
		record processRecord
		live   map[int]Process
		scan   func() ([]Process, error)
	}{
		{
			name:   "记录里的进程还活着",
			record: processRecord{PID: 5129, Executable: executable, Generation: "darwin:1:1", State: processRecordOwned},
			live:   map[int]Process{5129: {PID: 5129, Executable: "/other/bx", UID: 0, Generation: "darwin:9:9"}},
		},
		{
			name:   "launching 标记且系统里有 Core 在跑",
			record: processRecord{State: processRecordLaunching},
			live:   map[int]Process{},
			scan:   func() ([]Process, error) { return []Process{{PID: 4242, Executable: "/usr/local/bin/bx", UID: 0}}, nil },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := saveProcessRecord(statePath, tt.record); err != nil {
				t.Fatal(err)
			}
			operations := &pidAwareProcessOperations{live: tt.live, started: newStartTestProcess(6001)}
			runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
			runner.ScanRunningCores = noCoresRunning
			runner.StatePath = statePath
			runner.ControlSocket = filepath.Join(dir, "bx.sock")
			runner.Operations = operations
			runner.ScanRunningCores = tt.scan
			if _, err := runner.Start(context.Background(), CoreStartOptions{}); !errors.Is(err, ErrProcessOwnershipUncertain) {
				t.Fatalf("Start = %v, want ErrProcessOwnershipUncertain", err)
			}
			if operations.startCount() != 0 {
				t.Fatalf("拒绝时不得启动 Core,实际启动 %d 次", operations.startCount())
			}
		})
	}
}
