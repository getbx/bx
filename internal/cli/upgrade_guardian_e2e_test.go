//go:build darwin

// darwin-only:这里跑的是 macOS 的生命周期编排(macOSUpLifecycle /
// macOSDownLifecycleFor)与 Guardian 的 unix socket,与 macos_lifecycle_test.go
// 同一个约束 —— 它提供的 testMacOSLifecycleDeps 本身就是 darwin-tagged。
// 少了这行,ubuntu/windows runner 上的 go test ./... 会直接编译失败。

package cli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/supervisor"
)

// 这一组用例把 runUpgrade 的「停保护」那一步接到**真的** guardian.Manager 上。
//
// 之前两轮复审都放过了 C1(「升级前记下的意图被它下一步自己删掉」),原因不是
// 没写测试,而是写的测试全是替身:upgraderun_test.go 的 stopProtection 是个只记
// 一笔的闭包,从不经过 Manager.Down;manager_down_test.go 从不跑 runUpgrade。
// 两边各自全绿,而它们之间那条真实的路 —— 武装挂起 → client.DownForUpgrade →
// unix socket → LocalAPI → Manager.Down → 销不销挂起、改不改 desired ——
// 没有任何一个用例走过。
//
// 所以这里除了 Core/DNS/屏障这些与意图无关的系统动作用桩之外,全程用真件:
// 真 *guardian.Store(真文件)、真 Manager、真 LocalAPI、真 unix socket、真
// guardian.Client、真 runUpgrade、真 macOSDownLifecycleFor。

// upgradeE2EEnv 是一台「Guardian 正跑着、保护开着」的机器。
type upgradeE2EEnv struct {
	store  *guardian.Store
	client *guardian.Client
	deps   macOSLifecycleDeps
	events []string
}

// serveGuardian 把给定的 handler 挂到一个真的 unix socket 上,并回一个真的
// guardian.Client —— CLI 与 Guardian 之间那条线因此全程是真的(客户端拼的 URL、
// handler 的路由与鉴权、状态的 JSON 往返),没有任何一环被替身跳过。
func serveGuardian(t *testing.T, handler http.Handler) *guardian.Client {
	t.Helper()
	// sun_path 有 104 字节上限,t.TempDir() 的名字太长。
	socketDir, err := os.MkdirTemp("/tmp", "bxcli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "guard.sock")
	daemon, err := guardian.StartDaemon(context.Background(), guardian.DaemonOptions{
		SocketPath:      socketPath,
		Handler:         handler,
		OwnerUID:        uint32(os.Geteuid()),
		PeerCredentials: func(net.Conn) (uint32, bool) { return 0, true },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Close() })
	return guardian.NewClient(socketPath)
}

func newUpgradeE2EEnv(t *testing.T) *upgradeE2EEnv {
	t.Helper()
	stateDir := t.TempDir()
	store := guardian.OpenStore(guardian.Paths{
		Desired:     filepath.Join(stateDir, "guardian-state.json"),
		Transaction: filepath.Join(stateDir, "transaction.json"),
		Receipt:     filepath.Join(stateDir, "receipt.json"),
		Staging:     filepath.Join(stateDir, "staging"),
		Snapshots:   filepath.Join(stateDir, "snapshots"),
		// legacy 欠条路径:没有任何东西再写它,但启动恢复第一件事是迁移它,而
		// 缺路径时 Store 报错(holdmigrate.go 的取舍)。留着省下一次红鲱鱼。
		UpgradeIntent: filepath.Join(stateDir, "upgrade-intent.json"),
		// 挂起路径必须填:这里正是**升级路径**的旗舰 e2e(真 Manager、真 socket),
		// 而升级停机就在这条路上武装挂起 —— 缺路径会表现为一句
		// 「maintenance hold unreadable: guardian maintenance hold path required」,
		// 与被测逻辑毫无关系。
		MaintenanceHold: filepath.Join(stateDir, "maintenance-hold.json"),
	})
	// 保护开着:Down 因此会走完整路径(装屏障 → 还原 DNS → 记 off → 撤屏障),
	// 而不是「desired 已是 off」的短路分支。
	if err := store.SaveDesired(guardian.DesiredOn); err != nil {
		t.Fatal(err)
	}

	manager, err := guardian.NewManager(guardian.ManagerOptions{
		Store:          store,
		Runner:         stubCoreRunner{},
		Health:         stubHealthGate{},
		Barrier:        stubBarrier{},
		DNS:            stubDNSManager{},
		BarrierContext: guardian.BarrierContext{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}},
		CoreVersion:    "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	env := &upgradeE2EEnv{store: store, client: serveGuardian(t, guardian.NewLocalAPI(manager))}
	env.deps = testMacOSLifecycleDeps(&env.events, env.client)
	// 挂起的两个钩子接到**同一个真 Store** 上。不接的话 armMaintenanceHold 会
	// 报「此平台不可用」,recordStopIntent 于是退回写 desired=off —— 这条旗舰
	// e2e 就会在盘上看到 off、照样绿,而它自称验的正是「升级不再写 off」。
	// (那是本仓库反复出现的那个形状:替身缺一个钩子,测试测的是退化路径。)
	env.deps.armMaintenanceHold = func(reason string) error {
		return store.ArmMaintenanceHold(reason, time.Now())
	}
	env.deps.clearMaintenanceHold = store.ClearMaintenanceHold
	return env
}

// 版本不一致的提示必须在 `bx up` 实际走的那条路上真的能打印出来。
//
// 2026-08-08 复审 C2:提示读的是 result.Status.GuardianVersion/RuntimeVersion,
// 而这两个字段此前只有 GET /v1/status 填;`bx up` 拿的是 POST /v1/up 的响应,
// 且 Up 一报 Protected,waitGuardianProtected 立刻返回、根本不会再发 GET ——
// 于是在这条提示唯一该出现的场合(Guardian=dev、runtime=phase2)它恒为空。
// 原来的守卫用例只 grep 源码里有没有这次调用与字段名,值是空的照样绿。
// 这个用例走真 socket、真 LocalAPI、真 Client,比的是**到达比较点时的值**。
func TestUpLifecycleSeesGuardianAndRuntimeVersions(t *testing.T) {
	client := serveGuardian(t, guardian.NewLocalAPI(
		protectedStubController{},
		guardian.LocalAPIOptions{
			GuardianVersion: "dev",
			RuntimeVersion:  func() string { return "phase2" },
		},
	))
	var events []string
	result, err := macOSUpLifecycle(context.Background(), "/etc/bx/config.yaml", testMacOSLifecycleDeps(&events, client))
	if err != nil {
		t.Fatalf("bx up 失败: %v", err)
	}
	if result.Status.GuardianVersion != "dev" || result.Status.RuntimeVersion != "phase2" {
		t.Fatalf("比较点上的版本 = (%q, %q),两者都必须非空,否则提示永远打印不出来",
			result.Status.GuardianVersion, result.Status.RuntimeVersion)
	}
	msg := upVersionMismatchMessage(result.Status.GuardianVersion, result.Status.RuntimeVersion)
	if msg == "" {
		t.Fatal("Guardian 跑 dev、盘上装 phase2,必须提示版本不一致")
	}
	if !strings.Contains(msg, "dev") || !strings.Contains(msg, "phase2") {
		t.Fatalf("提示必须点名两个版本,实际 = %q", msg)
	}
}

// protectedStubController 是一台「已经受保护」的 Guardian。
//
// 它只提供 Protection 这一位,版本字段刻意留空 —— 那两个字段该由 LocalAPI 的
// options 填上,而那正是本用例要验的生产代码。
type protectedStubController struct{}

func (protectedStubController) Status() guardian.Status {
	return guardian.Status{SchemaVersion: 1, Desired: guardian.DesiredOn, Protection: guardian.ProtectionProtected}
}
func (protectedStubController) Up(context.Context) error   { return nil }
func (protectedStubController) Down(context.Context) error { return nil }

// 一次中途失败的升级必须把「用户本来要保护」留在盘上 —— 哪怕它自己的第一步
// 就是「停保护」。
//
// 这是 2026-08-08 复审 C1 的回归用例,只是承载它的东西换了:以前是升级欠条,
// 今天是 desired 本身没被改写 + 一张武装着的维护挂起。**这条测试也是删掉欠条的
// 许可证** —— 它证明重跑 app-install 读到的仍是 on。
func TestRunUpgradeKeepsTheIntentAcrossARealGuardianDown(t *testing.T) {
	env := newUpgradeE2EEnv(t)
	installFailure := errors.New("bundle 校验失败")

	installAttempted := false
	outcome, err := runUpgrade(upgradeIO{
		guardianRunning: func() (bool, error) { return true, nil },
		loadDesiredOn:   func() bool { return true },
		confirm:         func(string) (bool, error) { return true, nil },
		stopProtection: func() (macOSDownResult, error) {
			return macOSDownLifecycleFor(context.Background(), downPurposeUpgrade, "/etc/bx/config.yaml", env.deps)
		},
		installFiles: func() (installedFiles, error) {
			installAttempted = true
			return installedFiles{}, installFailure
		},
		restartGuardian: func() error { return nil },
		startProtection: func() error { return nil },
		log:             func(string) {},
	}, true)

	if err == nil {
		t.Fatal("装文件失败必须报错")
	}
	// **这条测试的全部价值在于它真的走到了装文件那一步。** 少了这句断言,
	// 一个在更早处中止的升级(例如新加的「保护未确认」闸门)会让下面每一条断言
	// 都照样成立 —— 报错、ForcedTeardown 为假、desired 仍是 on、欠条还在 ——
	// 而它自称测试的「升级中途失败」根本没发生。复审用探针实测过这个空壳状态。
	if !installAttempted {
		t.Fatal("升级应当走到装文件那一步才失败;它在更早处就中止了,这条测试没测到它自称测的东西")
	}
	if outcome.ForcedTeardown {
		t.Fatalf("这次停保护应当走干净路径(Guardian 就在跑),events=%v", env.events)
	}
	// **升级的停机不再写 desired=off。** 用户想要保护,只是此刻不能有 ——
	// 磁盘上那句「用户不想要保护」正是这一期要消灭的谎话,而任何忠实的调谐器
	// 都会照它把机器收敛到 off。
	if desired, err := env.store.LoadDesired(); err != nil || desired != guardian.DesiredOn {
		t.Fatalf("升级的停机改写了 desired:%q(err=%v)", desired, err)
	}
	// 「此刻不能有保护」这件事由挂起表达,而且它必须活过这一跳:升级紧接着要
	// 重启 Guardian,新 Guardian 的启动恢复读到 desired=on 就会把 Core 起回来,
	// 而那时二进制正换到一半。拦住它的只有这张挂起。
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || !armed {
		t.Fatalf("升级停机之后挂起必须还武装着:armed=%v err=%v", armed, err)
	}
	// 真的关掉了 —— 否则上面两句话都没有分量。
	if outcome.Down.Status.Protection != guardian.ProtectionOff {
		t.Fatalf("Manager.Down 应当已报告保护关闭,实际 %+v", outcome.Down.Status)
	}
	// **这就是删掉欠条的许可证**:重跑 app-install 时 loadDesiredOn 读的是同一个
	// store,而它必须说「要把保护起回来」。欠条曾经是唯一能给出这个答案的东西。
	if !desiredOnFrom(context.Background(), env.store, "/nonexistent/guardian.sock") {
		t.Fatal("重跑升级会读到 off —— 一次失败的升级把用户的意图弄丢了")
	}
}

// 而用户**明确**关掉保护时,同一台机器上那张挂起必须销掉。
//
// 与上一条互为镜像:C1 的修法只能让升级那一跳保住挂起,不能顺手把销挂起整个
// 关掉,否则一次失败的升级之后用户说「就这么关着」,那张挂起会在接下来 15 分钟
// 里让 Guardian 拒绝起 Core —— 而用户下一步很可能正是 sudo bx up。
func TestExplicitDownStillClearsTheHoldAfterAFailedUpgrade(t *testing.T) {
	env := newUpgradeE2EEnv(t)
	if err := env.store.ArmMaintenanceHold(guardian.HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}

	result, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", env.deps)
	if err != nil {
		t.Fatalf("bx down 失败: %v", err)
	}
	if result.Forced {
		t.Fatalf("应当走干净路径,events=%v", env.events)
	}
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("用户明确关闭保护之后挂起必须销掉:armed=%v err=%v", armed, err)
	}
}

// 以下四个桩只负责让 Manager.Down 能跑完 —— Core、健康、屏障、DNS 都与「意图
// 落在盘上什么样」无关,而真做这些事需要 root 和真实网络。意图那一半全程是真的。
type stubCoreRunner struct{}

// ScanRunning 让这个替身像一台**干净机器**。
//
// 少了它,Manager.Down 会答 200/core_scan_unsupported,而新加的升级闸门会在第一步
// 就中止 —— 于是这条「用真组件跑一次中途失败」的回归测试变成空壳:installFiles
// 一次都不会被调到,而它的每一条断言(报错、ForcedTeardown、desired=off、欠条还在)
// **照样成立**。复审用探针实测过。
func (stubCoreRunner) ScanRunning() ([]guardian.Process, error) { return nil, nil }

func (stubCoreRunner) Existing(context.Context) (guardian.Process, error) {
	return guardian.Process{}, nil
}
func (stubCoreRunner) Watch(p guardian.Process) guardian.Process { return p }
func (stubCoreRunner) Verify(guardian.Process) error             { return nil }

func (stubCoreRunner) Start(context.Context, guardian.CoreStartOptions) (guardian.Process, error) {
	return guardian.Process{}, errors.New("stub Core runner cannot start Core")
}
func (stubCoreRunner) Stop(context.Context, guardian.Process) error { return nil }
func (stubCoreRunner) Executable() string                           { return "/nonexistent/bx" }
func (stubCoreRunner) SetExecutable(string) error                   { return nil }

type stubHealthGate struct{}

func (stubHealthGate) Wait(context.Context, guardian.HealthTarget) (supervisor.RuntimeState, error) {
	return supervisor.RuntimeState{}, errors.New("stub health gate")
}

type stubBarrier struct{}

func (stubBarrier) Install(context.Context, guardian.BarrierContext) error           { return nil }
func (stubBarrier) ReassertBypass(context.Context, guardian.BarrierContext) error    { return nil }
func (stubBarrier) Release(context.Context, guardian.BarrierContext, []string) error { return nil }
func (stubBarrier) Remove(context.Context, guardian.BarrierContext) error            { return nil }

type stubDNSManager struct{}

func (stubDNSManager) EnsureManaged(context.Context) (guardian.DNSStatus, error) {
	return guardian.DNSStatus{State: guardian.DNSManaged}, nil
}

func (stubDNSManager) Inspect(context.Context) (guardian.DNSStatus, error) {
	return guardian.DNSStatus{State: guardian.DNSManaged}, nil
}

func (stubDNSManager) Restore(context.Context) (guardian.DNSStatus, error) {
	return guardian.DNSStatus{State: guardian.DNSUnmanaged}, nil
}
