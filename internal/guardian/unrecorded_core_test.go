package guardian

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 「盘上一条记录都没有」这条路径此前直接 fork,全程没问过系统一句。
//
// 而这正是本项目文档一度推荐的应急手段(手删 /var/lib/bx/core-process.json)
// 会造出来的状态:Core 正跑着 + 无记录。此路径上没有第二道防线——
// supervisor 的控制面在 net.Listen 之前先 os.Remove(SockPath),第二个 Core 会
// 静默夺走控制 socket;两个 Core 争 split-default 路由,先退出的那个用自己的
// 旧快照还原、掀掉另一个的劫持 —— bx status 显绿而流量明文直连。
func TestStartRefusesToForkWithoutRecordWhenCoreIsRunning(t *testing.T) {
	ops := &spawnMarkerOperations{started: newStartTestProcess(81)}
	runner, _ := newSpawnMarkerRunner(t, ops)
	runner.ScanRunningCores = func() ([]Process, error) {
		return []Process{{PID: 4242, Executable: "/usr/local/bin/bx", UID: 0}}, nil
	}

	_, err := runner.Start(context.Background(), CoreStartOptions{})
	if !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Start = %v, want ErrProcessOwnershipUncertain", err)
	}
	if ops.startCount() != 0 {
		t.Fatalf("拒绝时绝不能 fork,实际启动 %d 次", ops.startCount())
	}
	if !strings.Contains(err.Error(), "4242") {
		t.Errorf("错误必须点出扫到的 PID,实际 = %v", err)
	}
	// 与 launching 那条同一条边界:扫到的第三方 PID 只进错误文本,不进 payload。
	// 混进去会被 Manager.retainUncertain 收进 m.current,Down() 据此对一个从未
	// 验明的陌生进程调用 Stop,并把它发布进用户可见状态。
	process, ok := uncertainProcess(err)
	if !ok {
		t.Fatalf("期望 error 带 uncertain Process payload,实际 = %v", err)
	}
	if process.PID != 0 {
		t.Fatalf("uncertain Process 不得携带扫描到的第三方 PID,实际 = %d", process.PID)
	}
}

// 「问不出来」不等于「没有」——扫描失败同样拒绝。
func TestStartRefusesToForkWithoutRecordWhenScanFails(t *testing.T) {
	ops := &spawnMarkerOperations{started: newStartTestProcess(82)}
	runner, _ := newSpawnMarkerRunner(t, ops)
	runner.ScanRunningCores = func() ([]Process, error) { return nil, errors.New("sysctl failed") }

	if _, err := runner.Start(context.Background(), CoreStartOptions{}); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Start = %v, want ErrProcessOwnershipUncertain", err)
	}
	if ops.startCount() != 0 {
		t.Fatalf("扫描失败时不得 fork,实际启动 %d 次", ops.startCount())
	}
}

// 而系统里确实没有 Core 时,无记录仍然照常启动——这条检查不得把正常开机堵死。
func TestStartForksWithoutRecordWhenNoCoreRunning(t *testing.T) {
	started := newStartTestProcess(83)
	t.Cleanup(started.release)
	ops := &spawnMarkerOperations{started: started}
	runner, executable := newSpawnMarkerRunner(t, ops)
	ops.process = Process{PID: 83, Executable: executable, UID: 0, Generation: "darwin:8:3"}
	runner.ScanRunningCores = noCoresRunning

	if _, err := runner.Start(context.Background(), CoreStartOptions{}); err != nil {
		t.Fatalf("系统里没有 Core 时不得拒绝启动:%v", err)
	}
	if ops.startCount() != 1 {
		t.Fatalf("应当真的启动了 Core,实际启动 %d 次", ops.startCount())
	}
}

// 拒绝时必须告诉用户怎么脱身。
//
// 这里断言的是「错误文本带着 ownershipUncertainEscapeHint」这条接线,不是那句话
// 的字面内容 —— 后者由 TestOwnershipUncertainEscapeHintDescribesReVerification
// 单独钉住。原来这条测试查的是硬编码的 "sudo bx down",而 2026-08-11 之后那已经
// 不是出路了(用户发起的 up/migrate 每次都会重新求证);拿常量本身来比,改文案
// 时这条测试跟着走,而「接线断了」照样红。
func TestUnrecordedCoreRefusalNamesTheEscape(t *testing.T) {
	ops := &spawnMarkerOperations{started: newStartTestProcess(84)}
	runner, _ := newSpawnMarkerRunner(t, ops)
	runner.ScanRunningCores = func() ([]Process, error) {
		return []Process{{PID: 4242, Executable: "/usr/local/bin/bx", UID: 0}}, nil
	}
	_, err := runner.Start(context.Background(), CoreStartOptions{})
	if err == nil || !strings.Contains(err.Error(), ownershipUncertainEscapeHint) {
		t.Fatalf("错误必须点名脱身办法,实际 = %v", err)
	}
}

// systemProcessOperations 把「进程操作」与「系统里现在有哪些进程」绑成一体,
// 让注入的扫描器能反映真实语义:**进程一旦被观测到死亡,就从系统里消失**。
//
// 这正是崩溃重启路径成立的前提,而它在真机上有两重保证:
//   - 我们自己 fork 的 Core:exit 通道是在 started.Wait()(waitpid)返回之后
//     才发信号的,那时子进程已被回收,不在进程表里;
//   - 接管来的 Core:watchExisting 只有在 inspectProcess 返回 ErrProcessNotRunning
//     时才宣告死亡,而 darwin 那条判据要求 kill(pid,0) 明确回 ESRCH ——
//     僵尸对 kill(pid,0) 返回 0,所以宣告死亡时它连僵尸都已经不是了;
//   - 万一还有残留窗口,scanRunningCores 显式跳过 SZOMB(见 isZombieProcess)。
type systemProcessOperations struct {
	mu         sync.Mutex
	executable string
	nextPID    int
	starts     int
	live       map[int]Process
	processes  map[int]*startTestProcess
}

func newSystemProcessOperations(executable string, firstPID int) *systemProcessOperations {
	return &systemProcessOperations{
		executable: executable,
		nextPID:    firstPID,
		live:       map[int]Process{},
		processes:  map[int]*startTestProcess{},
	}
}

func (o *systemProcessOperations) Start(string, []string, []string) (StartedProcess, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	pid := o.nextPID
	o.nextPID++
	o.starts++
	process := newStartTestProcess(pid)
	o.processes[pid] = process
	o.live[pid] = Process{PID: pid, Executable: o.executable, UID: 0, Generation: fmt.Sprintf("darwin:900:%d", pid)}
	return process, nil
}

func (o *systemProcessOperations) Inspect(pid int) (Process, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if process, ok := o.live[pid]; ok {
		return process, nil
	}
	return Process{}, ErrProcessNotRunning
}

// runningCores 是注入给 runner 的扫描器:如实报告这台"系统"里活着的 Core。
func (o *systemProcessOperations) runningCores() ([]Process, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	cores := make([]Process, 0, len(o.live))
	for _, process := range o.live {
		cores = append(cores, process)
	}
	return cores, nil
}

// kill 模拟 Core 崩溃:先从系统里摘掉(等价于已被回收),再放行 Wait。
func (o *systemProcessOperations) kill(pid int) {
	o.mu.Lock()
	process := o.processes[pid]
	delete(o.live, pid)
	o.mu.Unlock()
	if process != nil {
		process.release()
	}
}

func (o *systemProcessOperations) startCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.starts
}

func (o *systemProcessOperations) releaseAll() {
	o.mu.Lock()
	processes := make([]*startTestProcess, 0, len(o.processes))
	for _, process := range o.processes {
		processes = append(processes, process)
	}
	o.mu.Unlock()
	for _, process := range processes {
		process.release()
	}
}

// 崩溃重启路径必须仍然能起来。
//
// handleUnexpectedExit → startCoreLocked → runner.Start 走的正是「盘上没有记录」
// 那条路(退出时 removeRecordIfGeneration 已把 owned 记录删掉),所以新加的扫描
// 检查直接压在自动重启上:一旦它把刚死的旧 Core 误认成「还有 Core 在跑」,
// 每一次 Core 崩溃都会变成永久失联,而不是自动恢复。
func TestManagerRestartsCoreAfterUnexpectedExitDespiteUnrecordedCoreScan(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	operations := newSystemProcessOperations(executable, 100)

	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.ControlSocket = filepath.Join(dir, "bx.sock")
	runner.Operations = operations
	runner.ScanRunningCores = operations.runningCores

	env := newManagerTestEnv(t)
	env.manager.runner = runner
	// 收尾顺序要紧:先把期望状态落成 off(否则放行 Wait 会再触发一轮自动重启,
	// 后台 goroutine 与 t.TempDir 的 RemoveAll 抢同一个目录),再放行进程,
	// 最后等后台把 owned 记录清干净。
	t.Cleanup(func() {
		if err := env.store.SaveDesired(DesiredOff); err != nil {
			t.Errorf("cleanup SaveDesired: %v", err)
		}
		operations.releaseAll()
		eventually(t, func() bool {
			_, err := os.Stat(runner.StatePath)
			return errors.Is(err, os.ErrNotExist)
		})
	})
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatalf("initial Up: %v", err)
	}
	if got := env.manager.current.PID; got != 100 {
		t.Fatalf("Core PID = %d, want 100", got)
	}

	// 前提检查:这个扫描器不是空壳。Core 活着时它确实报告「有 Core 在跑」——
	// 否则下面那条"重启仍然成功"证明不了任何东西。
	if cores, err := runner.ScanRunningCores(); err != nil || len(cores) != 1 || cores[0].PID != 100 {
		t.Fatalf("扫描器必须能看见活着的 Core,实际 cores=%+v err=%v", cores, err)
	}

	operations.kill(100)

	eventually(t, func() bool { return operations.startCount() == 2 })
	eventually(t, func() bool { return env.manager.Status().Protection == ProtectionProtected })
	if got := env.manager.current.PID; got != 101 {
		t.Fatalf("重启后的 Core PID = %d, want 101", got)
	}
	if got := env.manager.Status().LastError; got == "core_ownership_uncertain" {
		t.Fatal("崩溃重启被所有权判定卡住了——每次崩溃都会变成永久失联")
	}
}

// 反面:如果那个"已退出"的 Core 在系统里仍然可见(未被回收、或判据把僵尸
// 也算成在跑),重启就必须拒绝——这条不是缺陷,是 fail-closed 的正确方向,
// 也说明上面那条测试的绿灯来自「死了就消失」而不是来自检查被绕过。
func TestManagerRefusesRestartWhileAnotherCoreRemainsVisible(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	operations := newSystemProcessOperations(executable, 200)
	t.Cleanup(operations.releaseAll)

	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.ControlSocket = filepath.Join(dir, "bx.sock")
	runner.Operations = operations
	// 扫描器谎称永远还有一个 Core 在跑(模拟未被回收的残留)。
	runner.ScanRunningCores = func() ([]Process, error) {
		return []Process{{PID: 200, Executable: executable, UID: 0}}, nil
	}

	env := newManagerTestEnv(t)
	env.manager.runner = runner
	if _, err := env.manager.runner.Start(context.Background(), CoreStartOptions{}); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Start = %v, want ErrProcessOwnershipUncertain", err)
	}
	if operations.startCount() != 0 {
		t.Fatalf("拒绝时不得 fork,实际 %d 次", operations.startCount())
	}
}
