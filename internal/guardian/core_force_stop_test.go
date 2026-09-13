package guardian

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 2026-09-12 真机事故的那一跳。
//
// 一个「没能起来的 Core」按定义就是「没有控制 socket 的 Core」——
// supervisor.Run 里 waitTunnelHealthy 排在开 TUN、劫持路由、开控制 socket
// 之前,隧道不通就 fail-closed 返回,socket 从来没被建出来。而清理这个失败
// Core 的路(cleanupStartedCore → runner.Stop)第一件事就是经那个 socket 请它
// 自己退出:必定失败,于是真话(core_health_failed —— 隧道没起来)被
// core_ownership_uncertain(「系统里可能有第二个 Core」)整个顶掉,用户对着
// 一条 VPS 不通的机器读了七遍关于幻影 Core 的排查指引。
func TestCoreThatNeverBecameHealthyIsKilledInsteadOfAskedNicely(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	operations := newSystemProcessOperations(executable, 300)
	t.Cleanup(operations.releaseAll)

	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.ControlSocket = filepath.Join(dir, "bx.sock")
	runner.Operations = operations
	runner.ScanRunningCores = operations.runningCores
	// 这个 socket 按构造不存在 —— 正是失败本身。
	shutdownRequests := 0
	runner.ShutdownCore = func(context.Context, string, int) error {
		shutdownRequests++
		return errors.New("dial unix core.sock: connect: no such file or directory")
	}

	env := newManagerTestEnv(t)
	env.manager.runner = runner
	env.health.err = errors.New("bx 隧道健康检查超时(20s): restarts=0")

	err := env.manager.Up(context.Background())
	if err == nil {
		t.Fatal("隧道没起来而 Up 报成功了")
	}
	if errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("清理一个从没服务过的 Core 被报成所有权存疑:%v", err)
	}
	if got := env.manager.Status().LastError; got != "core_health_failed" {
		t.Fatalf("LastError = %q, want core_health_failed —— 真话不许被清理失败顶掉", got)
	}
	if shutdownRequests != 0 {
		t.Fatalf("向一个按构造不存在的 socket 请求了 %d 次协作关闭", shutdownRequests)
	}
	if env.manager.current.Uncertain {
		t.Fatal("失败的启动留下了所有权锁存 —— 下一次 bx up 会被它挡住")
	}
	// 收干净:进程真的被杀了,记录也没留下。
	started := operations.process(300)
	if started == nil {
		t.Fatal("测试台子没记下那个被 fork 出来的进程")
	}
	if got := started.terminationCount(); got != 1 {
		t.Fatalf("被丢下的 Core 收到 %d 次 Terminate,want 1 —— 它会自己吊满 20 秒超时,\n"+
			"恰好盖住用户的重试,于是下一次 bx up 指着 bx 自己没清干净的孤儿报所有权存疑", got)
	}
	if _, statErr := os.Stat(runner.StatePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("core-process.json = %v, want 已删除", statErr)
	}
}

// Stop 一个字不动:它收拾的是验明过身份、**正在正常服务**的 Core —— 那种 Core
// 已经开了 TUN、装了路由、接管了 DNS,协作关闭让它自己跑 defer 还原,是对的。
// 这条越界守卫存在的唯一理由是:上一条的修法很容易被写成「把 Stop 也改成杀」。
func TestStopStillAsksTheServingCoreToShutDownItself(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	shutdownRequests := 0
	runner.ShutdownCore = func(_ context.Context, socketPath string, pid int) error {
		shutdownRequests++
		if socketPath != runner.ControlSocket || pid != process.PID {
			t.Errorf("协作关闭请求 = (%q, %d)", socketPath, pid)
		}
		operations.setAlive(false)
		return nil
	}
	if err := runner.Stop(context.Background(), process); err != nil {
		t.Fatal(err)
	}
	if shutdownRequests != 1 {
		t.Fatalf("正常服务的 Core 收到 %d 次协作关闭请求,want 1", shutdownRequests)
	}
}

// ForceStop 只杀**我们自己 fork 出来的那个句柄**,不拿 PID 去杀。
// 拿 PID 杀要先验身份,而验身份与杀之间那一瞬 PID 可能已经被复用;拿句柄杀
// 在构造上没有这个窗口。手里没有句柄时**如实报错**,绝不猜一个 PID 杀下去,
// 也绝不悄悄回落到那条要 socket 的协作关闭。
func TestForceStopRefusesWhenItHasNoHandleForThatCore(t *testing.T) {
	runner, process, _ := newRecordedProcessRunner(t)
	shutdownRequests := 0
	runner.ShutdownCore = func(context.Context, string, int) error {
		shutdownRequests++
		return nil
	}
	err := runner.ForceStop(context.Background(), process)
	if err == nil {
		t.Fatal("对一个不是自己 fork 出来的 Core,ForceStop 报了成功")
	}
	if !strings.Contains(err.Error(), "42") {
		t.Errorf("错误要点名是哪一个 PID:%v", err)
	}
	if shutdownRequests != 0 {
		t.Fatalf("ForceStop 悄悄回落到了协作关闭(%d 次)—— 那条路依赖的正是失败本身", shutdownRequests)
	}
}

// C1:占主导的那种「手里没有句柄」恰恰是**我们自己那个 Core、而它已经退了**。
//
// Start 的 wait goroutine 在 waitpid 一返回就 forgetStartedCore(process.go 里
// close(exited) 前那一句)。死于 provision / config / tun_open / hijack 的 Core
// 两秒就没了,而 Guardian 的健康等待要满 20 秒才超时 —— 走到清理这一步时,句柄
// 早在十八秒前就被摘掉了。在那里如实报错的后果不是「诚实」,是
// retainUncertain + core_ownership_uncertain:**用户又一次读到那段关于幻影第二个
// Core 的排查指引**,而且恰好落在 Task 2 刚教会 bx 说清楚的那四个码上。
// 老的 Stop 一直处理得对:Inspect → ErrProcessNotRunning → 清记录 → nil。
func TestForceStopAcceptsOurOwnCoreThatAlreadyExited(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	shutdownRequests := 0
	runner.ShutdownCore = func(context.Context, string, int) error {
		shutdownRequests++
		return nil
	}
	// 进程已经退了(这正是 waitHealthy 超时那一刻的常态),句柄也早被摘掉。
	operations.setAlive(false)

	if err := runner.ForceStop(context.Background(), process); err != nil {
		t.Fatalf("我们自己那个已经退掉的 Core 被 ForceStop 报成失败:%v\n"+
			"—— 这条错误会被翻成 core_ownership_uncertain,把「隧道没起来」那句真话顶掉", err)
	}
	if shutdownRequests != 0 {
		t.Fatalf("ForceStop 回落到了协作关闭(%d 次)", shutdownRequests)
	}
	if _, statErr := os.Stat(runner.StatePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("core-process.json = %v, want 已删除 —— 留着它下一次 bx up 会撞上一条指着死 PID 的陈旧记录", statErr)
	}
}

// 反向:系统**答不上来**它还在不在,仍然拒绝。「问不出来」不是「没有」,
// 这半边一个字都没松 —— 否则双 Core 那道门就从这里被打开了。
func TestForceStopStillRefusesWhenTheSystemCannotSayWhetherItIsGone(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	operations.setInspectError(errors.New("sysctl kern.proc.pid: input/output error"))
	if err := runner.ForceStop(context.Background(), process); err == nil {
		t.Fatal("系统答不上来,ForceStop 却报了成功 —— 那是把「问不出来」当成「没有」")
	}
}

// Minor 4:**手里没句柄时的判据要与 Stop 一字相同** —— 不然它比 Stop 更严,
// 而更严的那一头恰好通向同一句假话。
//
// Core 退出与走到清理之间约十八秒(健康等待 20s,而死于 provision/config/
// tun_open/hijack 的 Core 两秒就没了);PID 在这个窗口里被回收再分配是真会发生
// 的事。那时 Stop 会按 sameProcessIdentity 判「身份不符 ⇒ 我们的 Core 已经走了」
// ⇒ 清记录、返回 nil;而 ForceStop 若只看「这个号上有没有进程」,就会答
// 「系统说它还在」⇒ retainUncertain ⇒ **core_ownership_uncertain**,C1 刚消灭掉
// 的那句假话从一扇更窄的门原样回来。
//
// 方向本身是 fail-closed(不是安全洞),但判据只能有一份,而这里那一份答反了。
func TestForceStopTreatsAReusedPIDTheSameWayStopDoes(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	shutdownRequests := 0
	runner.ShutdownCore = func(context.Context, string, int) error {
		shutdownRequests++
		return nil
	}
	// 这个号上现在跑着别人:PID 一样,但代际(与可执行路径)对不上 ——
	// 与 Stop 里 sameProcessIdentity 判「不是同一个」的输入逐字相同。
	operations.setProcess(Process{
		PID: process.PID, Executable: process.Executable, UID: process.UID,
		Generation: "darwin:999:999",
	})

	// 前置自检:同一份输入喂给 Stop,它判「走了,没事」。少了这一句,下面那条
	// 断言可能只是在描述一个两边都错的世界。
	if err := runner.Stop(context.Background(), process); err != nil {
		t.Fatalf("前置不成立:Stop 对同一份输入报了失败:%v", err)
	}
	if shutdownRequests != 0 {
		t.Fatalf("前置不成立:Stop 对一个身份不符的 PID 发了 %d 次协作关闭请求", shutdownRequests)
	}

	runner, process, operations = newRecordedProcessRunner(t)
	operations.setProcess(Process{
		PID: process.PID, Executable: process.Executable, UID: process.UID,
		Generation: "darwin:999:999",
	})
	if err := runner.ForceStop(context.Background(), process); err != nil {
		t.Fatalf("PID 被复用之后 ForceStop 报了失败:%v\n"+
			"—— 它会被翻成 core_ownership_uncertain,而 Stop 对同一份输入判的是「我们的 Core 已经走了」。\n"+
			"同一个问题不许有第二份答案,尤其当第二份答的是那句刚被消灭的假话", err)
	}
	if _, statErr := os.Stat(runner.StatePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("core-process.json = %v, want 已删除 —— 那条记录指着一个已经属于别人的 PID", statErr)
	}
}

// 反向:身份**对得上**(系统说那个 Core 真的还在)时仍然拒绝 —— 接管来的 Core、
// 上一任 Guardian 留下的 Core,一寸都不许松。少了这一条,「凡是没句柄一律放行」
// 也能满足上面那条,而那正是双 Core 那道门。
func TestForceStopStillRefusesWhenTheRecordedCoreIsGenuinelyStillRunning(t *testing.T) {
	runner, process, _ := newRecordedProcessRunner(t)
	if err := runner.ForceStop(context.Background(), process); err == nil {
		t.Fatal("身份对得上、系统说它还在,ForceStop 却报了成功 —— 双 Core 那道门从这里被打开")
	}
	if _, statErr := os.Stat(runner.StatePath); statErr != nil {
		t.Fatalf("拒绝了却把记录删了:%v", statErr)
	}
}

// 第三种:**身份比不出来**(拿不到代际 / 可执行路径)。它是「问不出来」,
// 不是「不是我们的」—— 与 Inspect 失败那一支同一条极性,也与 Stop 一字相同。
//
// 变异实测:把比对失败折成 same=false(于是走清记录 + nil)之后,上面两条
// **全绿** —— 那正是「守卫钉住的是缺陷旁边的东西」,一个把「没问出来」当成
// 「没有」的分支从这里溜进来,而它的出口恰好是 fail-open。
func TestForceStopRefusesWhenTheIdentityCannotBeCompared(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	// 代际读不出来 —— sameProcessIdentity 对这种输入报错,不报「不是同一个」。
	operations.setProcess(Process{PID: process.PID, Executable: process.Executable, UID: process.UID})
	if err := runner.ForceStop(context.Background(), process); err == nil {
		t.Fatal("身份比不出来,ForceStop 却报了成功 ——「问不出来」不是「没有」")
	}
	if _, statErr := os.Stat(runner.StatePath); statErr != nil {
		t.Fatalf("没能判断就把记录删了:%v", statErr)
	}
}

// C1 的整机形状:Core 自己两秒就死了(provision / config / tun_open / hijack),
// 而健康等待要满 20 秒。到清理那一刻句柄早就没了 —— 这条路必须仍然答出
// core_health_failed,不许退回 core_ownership_uncertain。
//
// 旗舰那条(TestCoreThatNeverBecameHealthyIsKilledInsteadOfAskedNicely)全程
// 让假进程活着,所以看不见这一种:**测试输入让待守的属性不可见**。
func TestCoreThatDiedOnItsOwnIsNotReportedAsOwnershipUncertain(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	operations := newSystemProcessOperations(executable, 300)
	t.Cleanup(operations.releaseAll)

	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.ControlSocket = filepath.Join(dir, "bx.sock")
	runner.Operations = operations
	runner.ScanRunningCores = operations.runningCores
	shutdownRequests := 0
	runner.ShutdownCore = func(context.Context, string, int) error {
		shutdownRequests++
		return errors.New("dial unix core.sock: connect: no such file or directory")
	}

	env := newManagerTestEnv(t)
	env.manager.runner = runner
	env.health.err = errors.New("bx 隧道健康检查超时(20s): restarts=0")
	// Core 在健康等待还没返回之前就自己没了 —— 等回来时句柄早被 wait
	// goroutine 摘掉了(process.go 里 forgetStartedCore 那一句)。
	env.health.onWait = func() {
		operations.kill(300)
		waitForForgottenHandle(t, runner, Process{PID: 300, Generation: "darwin:900:300"})
	}

	err := env.manager.Up(context.Background())
	if err == nil {
		t.Fatal("Core 死了而 Up 报成功了")
	}
	if errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("一个自己死掉的 Core 被报成所有权存疑:%v\n"+
			"—— 这正是这一支要消灭的那句假话,只是从另一扇门回来了", err)
	}
	if got := env.manager.Status().LastError; got != "core_health_failed" {
		t.Fatalf("LastError = %q, want core_health_failed", got)
	}
	if env.manager.current.Uncertain {
		t.Fatal("留下了所有权锁存 —— 下一次 bx up 会被它挡住")
	}
	if shutdownRequests != 0 {
		t.Fatalf("向一个按构造不存在的 socket 请求了 %d 次协作关闭", shutdownRequests)
	}
}

// waitForForgottenHandle 等 Start 那个 wait goroutine 真的把句柄摘掉 ——
// 不等的话这条测试要守的那个状态(句柄已经没了)只是偶尔出现。
func waitForForgottenHandle(t *testing.T, runner *ExecCoreRunner, process Process) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := runner.startedCoreHandle(process); !ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("句柄一直没被摘掉 —— 这条测试要守的状态根本没出现")
}
