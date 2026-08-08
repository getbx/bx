package cli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/supervisor"
)

// 这一组用例把 runUpgrade 的「停保护」那一步接到**真的** guardian.Manager 上。
//
// 之前两轮复审都放过了 C1(「升级欠条被它下一步自己删掉」),原因不是没写测试,
// 而是写的测试全是替身:upgraderun_test.go 的 stopProtection 是个只记一笔的
// 闭包,从不经过 Manager.Down;manager_down_test.go 从不跑 runUpgrade。两边各自
// 全绿,而它们之间那条真实的路 —— 写欠条 → client.DownForUpgrade → unix socket
// → LocalAPI → Manager.Down → 销不销账 —— 没有任何一个用例走过。
//
// 所以这里除了 Core/DNS/屏障这些与欠条无关的系统动作用桩之外,全程用真件:
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
	if runtime.GOOS == "windows" {
		// unix socket + /tmp 路径是这套 harness 的前提;Guardian 本就只在
		// macOS 上运行(daemon.go 的 requireDaemonPlatform)。
		t.Skip("Guardian 的 unix socket harness 不在 Windows 上跑")
	}
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
		Desired:       filepath.Join(stateDir, "guardian-state.json"),
		Transaction:   filepath.Join(stateDir, "transaction.json"),
		Receipt:       filepath.Join(stateDir, "receipt.json"),
		Staging:       filepath.Join(stateDir, "staging"),
		Snapshots:     filepath.Join(stateDir, "snapshots"),
		UpgradeIntent: filepath.Join(stateDir, "upgrade-intent.json"),
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

// 一次中途失败的升级必须把欠条留在盘上 —— 哪怕它自己的第一步就是「停保护」。
//
// 这是 2026-08-08 复审 C1 的回归用例:欠条写下之后紧接着的 UpgradeStopProtection
// 经 Manager.Down 把它删掉,于是重试读到 desired=off、找不到欠条,计划里不再有
// 「恢复保护」,重试「成功」而机器从此不再被保护。
func TestRunUpgradeKeepsTheDebtAcrossARealGuardianDown(t *testing.T) {
	env := newUpgradeE2EEnv(t)
	installFailure := errors.New("bundle 校验失败")

	outcome, err := runUpgrade(upgradeIO{
		guardianRunning: func() (bool, error) { return true, nil },
		loadDesiredOn:   func() bool { return true },
		saveIntent:      env.store.SaveUpgradeIntent,
		clearIntent:     env.store.ClearUpgradeIntent,
		confirm:         func(string) (bool, error) { return true, nil },
		stopProtection: func() (bool, error, error) {
			result, err := macOSDownLifecycleFor(context.Background(), downPurposeUpgrade, "/etc/bx/config.yaml", env.deps)
			return result.Forced || err != nil, result.Cause, err
		},
		installFiles:    func() (installedFiles, error) { return installedFiles{}, installFailure },
		restartGuardian: func() error { return nil },
		startProtection: func() error { return nil },
		log:             func(string) {},
	}, true)

	if err == nil {
		t.Fatal("装文件失败必须报错")
	}
	if outcome.ForcedTeardown {
		t.Fatalf("这次停保护应当走干净路径(Guardian 就在跑),events=%v", env.events)
	}
	// 真的关掉了 —— 否则「欠条还在」这句话没有任何分量。
	if desired, err := env.store.LoadDesired(); err != nil || desired != guardian.DesiredOff {
		t.Fatalf("Manager.Down 应当已把 desired 记成 off,实际 %q(err=%v);测试没走到真正的关闭路径", desired, err)
	}
	pending, present := env.store.LoadUpgradeIntent()
	if !present {
		t.Fatal("升级中途失败后欠条必须还在:它是重试唯一知道「还欠一次恢复保护」的东西")
	}
	if !pending {
		t.Fatalf("欠条应记着 desired_on=true,实际 %v", pending)
	}
	// 重试时读到的答案必须是「要把保护起回来」,即便 Guardian 的 desired 已是 off。
	if !resolveUpgradeDesiredOn(false, pending, present) {
		t.Fatal("留着的欠条必须让重试恢复保护,否则它等于没留")
	}
}

// 而用户**明确**关掉保护时,同一台机器上的旧欠条必须销账。
//
// 与上一条互为镜像:C1 的修法只能让升级那一跳保住欠条,不能顺手把销账整个关掉,
// 否则一次失败的升级之后用户说「就这么关着」,下次菜单 Repair(带 --yes,连问都
// 不问)仍会拿旧欠条把保护打开。
func TestExplicitDownStillClearsTheDebtAfterAFailedUpgrade(t *testing.T) {
	env := newUpgradeE2EEnv(t)
	if err := env.store.SaveUpgradeIntent(true); err != nil {
		t.Fatal(err)
	}

	result, err := macOSDownLifecycleDetailed(context.Background(), "/etc/bx/config.yaml", env.deps)
	if err != nil {
		t.Fatalf("bx down 失败: %v", err)
	}
	if result.Forced {
		t.Fatalf("应当走干净路径,events=%v", env.events)
	}
	if pending, present := env.store.LoadUpgradeIntent(); present {
		t.Fatalf("用户明确关闭保护之后欠条必须销账,实际 pending=%v", pending)
	}
}

// 以下四个桩只负责让 Manager.Down 能跑完 —— Core、健康、屏障、DNS 都与升级欠条
// 无关,而真做这些事需要 root 和真实网络。欠条那一半全程是真的。
type stubCoreRunner struct{}

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
