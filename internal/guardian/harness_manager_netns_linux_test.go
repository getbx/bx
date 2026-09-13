//go:build integration && linux

// guardian 侧集成台的第二块:在真 netns 里跑**真 Manager**(真 ExecCoreRunner
// spawn 一个真子进程、真 linux 屏障、真 procscan、真 peercred),只有 Core 自己
// 换成替身。
//
// 这是控制面第一次有「打在内核状态上」的生命周期断言:此前 up/down 的接线
// 只由替身背书,而本仓库全部事故都在接线而不在判定。
package guardian

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/netnsguard"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/getbx/bx/internal/version"
	"golang.org/x/sys/unix"
)

// unixKillZero 向内核求证一个 PID 是不是还在。**判据是 ESRCH 而不是「有没有
// 错误」**:EPERM 意味着进程活着但不归我们管,把它读成「已经没了」正是这个
// 仓库为 macOS EIO 那次栽过的形状。
func unixKillZero(pid int) error {
	err := unix.Kill(pid, 0)
	if err == nil {
		return nil // 活着
	}
	if err == unix.EPERM {
		return nil // 活着,只是不归我们管
	}
	return err
}

// installFakeCoreBinary 把测试二进制拷成一个名叫 `bx` 的可执行文件。
//
// **名字是判据的一部分,不是摆设**:looksLikeCore 认的是
// `basename(exe 或 argv[0]) == "bx" && argv[1] == "run" && uid == 0`。
// 拷成别的名字,procscan 就认不出这个 Core —— 而「已有 Core 在跑 ⇒ 拒绝再起
// 一个」那条准入正是靠它,少了它这台子会在最要紧的地方假绿。
func installFakeCoreBinary(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("定位测试二进制: %v", err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatalf("读测试二进制: %v", err)
	}
	path := filepath.Join(t.TempDir(), "bx")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatalf("写假 Core: %v", err)
	}
	return path
}

// newHarnessManager 按**生产的接线**造一个 Manager:平台相关的每一件都经
// lifecyclePlatform 取(与 RunDaemon 同一条路),只有 Core 可执行文件指向替身。
func newHarnessManager(t *testing.T) *Manager {
	t.Helper()
	// 假 Core 的钩子靠环境变量触发,而 ExecCoreRunner 把 os.Environ() 传给子进程。
	t.Setenv("BX_GUARDIAN_FAKE_CORE", "1")

	dir := t.TempDir()
	platform := newLifecyclePlatform()
	runner := newTestCoreRunner(t, installFakeCoreBinary(t), filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = filepath.Join(dir, "core-process.json")
	manager, err := NewManager(ManagerOptions{
		Store: OpenStore(Paths{
			Desired:         filepath.Join(dir, "guardian-state.json"),
			Transaction:     filepath.Join(dir, "transaction.json"),
			Receipt:         filepath.Join(dir, "receipt.json"),
			Staging:         filepath.Join(dir, "staging"),
			Snapshots:       filepath.Join(dir, "snapshots"),
			UpgradeIntent:   filepath.Join(dir, "upgrade-intent.json"),
			MaintenanceHold: filepath.Join(dir, "maintenance-hold.json"),
		}),
		Runner:          runner,
		Health:          HealthChecker{PollInterval: 50 * time.Millisecond},
		Barrier:         platform.NewBarrier(nil),
		DNS:             platform.NewDNSManager(""),
		Legacy:          systemLegacyCoreLifecycle{},
		BarrierContext:  BarrierContext{BlockIPv6: true},
		GatewayProvider: GatewayProviderFunc(platform.DiscoverGateway),
		CoreVersion:     version.Version,
	})
	if err != nil {
		t.Fatalf("造 Manager: %v", err)
	}
	t.Cleanup(func() {
		// 收尾必须无条件把屏障拆干净:一次失败的 Down 会让 netns 里留下
		// pref-120 rule,而后面的测试会在一台「公网本来就不通」的机器上
		// 取基线 —— 那正是让断言变得毫无意义的形状。
		//
		// **带超时,而且不许因为 Down 挂住就跳过拆屏障**:变异实测(扫描恒报
		// 「没有 Core」)之下会真的起出第二个 Core,两个 Core 争同一个控制
		// socket,这里的 Down 就此无限等 —— 于是一个本该立刻转红的变异表现为
		// 整套测试挂到超时。收尾路径不许挂,这条在生产里也是不变量。
		downCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_ = manager.Down(downCtx)
		cancel()
		_ = RemoveBlockingBarrierRoutes(context.Background(), nil)
	})
	return manager
}

func harnessRules(t *testing.T) string {
	t.Helper()
	return netnsguard.MustIP(t, "rule", "show")
}

// Up:真 Manager 起真子进程、等健康、发布 protected,而**屏障不留在系统上**
// (它是过渡期的保护,Core 接手之后就该退场)。
//
// 这一条同时坐实了三块 linux 供货第一次在生产接线上一起工作:procscan
// (准入)、dataPlaneDNSManager(无需接管,漏教一道门 Up 就恒失败)、
// 真屏障(装/释放)。
func TestHarnessManagerUpStartsCoreAndLeavesNoBarrier(t *testing.T) {
	enterGuardianNetns(t)
	manager := newHarnessManager(t)

	if err := manager.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	status := manager.Status()
	if status.Protection != ProtectionProtected {
		t.Fatalf("Protection = %q, want protected(LastError=%q)", status.Protection, status.LastError)
	}
	if status.CorePID <= 0 {
		t.Fatalf("没有记下 Core 的 PID: %+v", status)
	}
	// 向系统求证那个 PID 真的在跑,而不是只信 Manager 的记账。
	if err := unixKillZero(status.CorePID); err != nil {
		t.Fatalf("Manager 说 Core PID 是 %d,而系统说它不在: %v", status.CorePID, err)
	}
	// **procscan 必须认得出它** —— 这条准入判据在 linux 上是本轮新写的,
	// 认不出的后果是「已有 Core 在跑」时照样再起一个(双 Core)。
	cores, err := scanRunningCores(coreScanObserve)
	if err != nil {
		t.Fatalf("扫描运行中的 Core: %v", err)
	}
	if len(cores) != 1 || cores[0].PID != status.CorePID {
		t.Fatalf("procscan 没认出这个 Core:got %+v,want 只有 PID %d", cores, status.CorePID)
	}
	// Core 接手之后屏障必须退场:留着就是把整机公网堵在一个「已受保护」的
	// 状态里,而 status 会说一切正常。
	if rules := harnessRules(t); strings.Contains(rules, linuxBarrierRulePref+":") {
		t.Fatalf("Up 完成后屏障 rule 还在:\n%s", rules)
	}
	if got := routeVerdict(t, "1.1.1.1"); strings.Contains(got, "unreachable") {
		t.Fatalf("Up 完成后公网被屏障堵着:\n%s", got)
	}
}

// Down:Core 真的停掉、屏障不留残渣、desired 落盘为 off。
//
// **「Down 不许撒谎」在这里第一次有内核背书**:此前那条不变量(报成功之前
// 必须确知 Core 已停)只由替身证明,而替身的「停了」是它自己说的。
func TestHarnessManagerDownStopsCoreForReal(t *testing.T) {
	enterGuardianNetns(t)
	manager := newHarnessManager(t)
	if err := manager.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	pid := manager.Status().CorePID

	if err := manager.Down(context.Background()); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if state := manager.Status().Protection; state == ProtectionProtected {
		t.Fatalf("Down 之后仍报 protected")
	}
	// 向系统求证进程真的没了 —— 这正是「不许撒谎」那条不变量的内容。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if unixKillZero(pid) != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if unixKillZero(pid) == nil {
		t.Fatalf("Down 报成功,而 Core PID %d 还活着 —— 停止路径撒了谎", pid)
	}
	if cores, err := scanRunningCores(coreScanObserve); err == nil && len(cores) != 0 {
		t.Fatalf("Down 之后 procscan 仍扫到 Core: %+v", cores)
	}
	// 屏障是 Down 路径的保护,收尾必须自己带走(孤儿屏障 = 整机黑洞)。
	if rules := harnessRules(t); strings.Contains(rules, linuxBarrierRulePref+":") {
		t.Fatalf("Down 之后屏障 rule 残留:\n%s", rules)
	}
}

// 准入控制:系统里已经有一个 Core 在跑时,Up 必须拒绝而不是再起一个。
//
// 这是 af81632 那个双 Core 风险的 linux 首验 —— 两个 Core 争路由、先退出的
// 那个用旧快照还原掀掉另一个的劫持,status 显绿而流量明文直连。
func TestHarnessManagerRefusesSecondCore(t *testing.T) {
	enterGuardianNetns(t)
	first := newHarnessManager(t)
	if err := first.Up(context.Background()); err != nil {
		t.Fatalf("第一个 Up: %v", err)
	}

	// 第二个 Manager 有自己的记账(全新 StatePath),盘上什么都没有 ——
	// 它唯一能发现「已经有 Core 在跑」的途径就是向系统求证。
	//
	// **带超时不是讲究,是让失败形式可用**:变异实测(把 linux 扫描改成恒报
	// 「没有 Core」)之下,第二个 Core 真的被起起来,两个 Core 争同一个控制
	// socket(supervisor 在 Listen 前先 os.Remove),这条测试**挂死**而不是
	// 报错 —— 一个会挂 90 秒的闸门在 CI 里与红灯同样糟。有了超时,同一个变异
	// 变成一次干净且立刻的失败。
	second := newHarnessManager(t)
	upCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := second.Up(upCtx)
	if err == nil {
		t.Fatal("系统里已有 Core 在跑,第二次 Up 却成功了 —— 双 Core 的门开着")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "core") {
		t.Fatalf("拒绝原因读不出与 Core 的关系: %v", err)
	}
	// 第一个 Core 必须**毫发无伤**:准入拒绝不是「谁先谁后」的争抢。
	if status := first.Status(); status.CorePID <= 0 || unixKillZero(status.CorePID) != nil {
		t.Fatalf("被拒绝的那次 Up 影响到了在跑的 Core: %+v", status)
	}
}

// 控制 socket 是 Core 存活观测的唯一权威(observe 那一层的判据),这里确认
// 它在真 netns 里真的答得上话 —— 假 Core 与生产观测的接缝对得上。
func TestHarnessManagerCoreRuntimeIsObservable(t *testing.T) {
	enterGuardianNetns(t)
	manager := newHarnessManager(t)
	if err := manager.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	state, err := supervisor.FetchRuntimeState(supervisor.SockPath)
	if err != nil {
		t.Fatalf("读 Core 运行时状态: %v", err)
	}
	if state.PID != manager.Status().CorePID {
		t.Fatalf("控制面报 PID %d,Manager 记着 %d", state.PID, manager.Status().CorePID)
	}
	if !state.TunnelHealthy {
		t.Fatal("控制面报隧道不健康 —— 健康检查本不该放行")
	}
}
