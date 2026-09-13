package guardian

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
