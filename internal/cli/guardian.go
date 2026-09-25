package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/getbx/bx/internal/elevate"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/supervisor"
	urfavecli "github.com/urfave/cli/v2"
)

type guardianCommandDeps struct {
	geteuid func() int
	run     func(context.Context, guardian.DaemonOptions) error
}

const (
	guardianReadyTimeout      = 10 * time.Second
	guardianReadyPollInterval = 200 * time.Millisecond
	// guardianMutationClientTimeout is the HTTP client timeout for Guardian
	// mutations (Up/Down/Migrate). It must exceed the server-side mutation
	// budget (guardianMutationTimeout, 60s in internal/guardian/localapi.go)
	// with real margin: since the startup-recovery fence (see
	// Manager.BeginStartupRecovery), a POST here can legitimately queue
	// behind an in-flight startup Recover — up to that same 60s budget —
	// before it even begins. guardian.NewClient's default 30s timeout would
	// fail such a request with "Client.Timeout exceeded while awaiting
	// headers" even though the daemon would have succeeded.
	guardianMutationClientTimeout = 90 * time.Second
	// coreShutdownWait bounds how long the forced teardown waits for a Core
	// that accepted a cooperative shutdown to actually exit, so its own
	// route/DNS restore can finish before Guardian is booted out.
	coreShutdownWait         = 15 * time.Second
	coreShutdownPollInterval = 200 * time.Millisecond
	// dnsRestoreTimeout bounds the forced teardown's DNS restore step. It
	// shells out to several external commands with no timeout of their own
	// (`networksetup` x2, `dscacheutil`, `killall`), and app.Run(os.Args)
	// (main.go) runs under context.Background(), which never expires by
	// itself — so without a bound here a stuck networksetup call (a real
	// failure mode on a broken network stack, which is exactly when this
	// escape hatch gets used) would hang `bx down` forever. 20s sits
	// between guardianReadyTimeout (10s, a single readiness poll) and twice
	// coreShutdownWait (15s, one cooperative RPC): up to four commands run
	// here sequentially, each normally well under a second, so the bound
	// exists to guarantee termination on a hang, not to accommodate
	// expected latency.
	dnsRestoreTimeout = 20 * time.Second
	// legacyProbeTimeout bounds the legacy-Core probe on the stop path. The
	// probe forks `launchctl print` **twice** (one per legacy label), and it
	// now runs *first* — before anything has been stopped. A wedged launchd
	// there would hang `bx down` before it did any useful work, which is the
	// shape of the 2026-08-04 incident (71 minutes unable to turn protection
	// off). Two `launchctl print` calls are sub-second in every healthy case,
	// so this bound exists to guarantee termination, not to allow for latency.
	// Exceeding it surfaces as an error, which legacyCoreMayBeRunning already
	// treats as "cannot determine ⇒ assume a legacy Core exists".
	legacyProbeTimeout = 5 * time.Second
	// legacyBootoutTimeout bounds disarming the legacy launchd job, for the
	// same reason as legacyProbeTimeout: it shells out to launchctl (once to
	// list labels, then once per loaded label) on the stop path, where a hang
	// is the worst possible outcome.
	legacyBootoutTimeout = 10 * time.Second
)

type migrationMetadataDeps struct {
	discoverGateway func(context.Context) (string, error)
	fetchRuntime    func(string) (supervisor.RuntimeState, error)
	loadConfig      func(string) (*config.Config, error)
	lookupIP        func(context.Context, string) ([]netip.Addr, error)
}

type guardianLifecycleClient interface {
	Status(context.Context) (guardian.Status, error)
	Up(context.Context) (guardian.Status, error)
	Down(context.Context) (guardian.Status, error)
	// DownForUpgrade 与 Down 停的是同一件事,区别在于 Guardian 把每一次普通的
	// Down 当作「用户不要保护了」:写 desired=off、销掉维护挂起与升级欠条。
	// 升级自己那次停保护三样都不要(见 guardian/upgradeintent.go)。
	DownForUpgrade(context.Context) (guardian.Status, error)
	Migrate(context.Context, guardian.MigrationRequest) (guardian.Status, error)
}

// downPurpose 说明这次停保护是谁要的 —— 决定盘上留下的是「用户不想要保护」
// 还是「此刻不能有保护」。
//
// 它不是 macOSLifecycleDeps 的一部分:deps 装的是「怎么做」,而这是「为什么
// 做」,同一套 deps 两种用途都成立(app-install 与 bx down 都用
// defaultMacOSLifecycleDeps)。
type downPurpose int

const (
	// downPurposeUser:用户明确要关保护(bx down)。写 desired=off、销掉维护
	// 挂起与旧欠条。
	downPurposeUser downPurpose = iota
	// downPurposeUpgradeUnprotected:升级,而这台机器此刻**本来就不要保护**
	// (desired=off,用户自己关的)。这是升级停保护的唯一来由:保护开着时升级
	// 根本不停保护,而是在屏障下切换 Guardian(upgradeSteps 的 desiredOn 分支;
	// 挂起由屏障那一步直接武装,见 switchbarrier_darwin.go)。
	//
	// **不武装挂起**,而这不是省事:武装出来的那张挂起没有任何东西会去清它 ——
	// upgradeSteps(running, desiredOn=false) 里根本没有「恢复保护」这一步,而销
	// 挂起只发生在用户显式的 up/down/migrate 上。于是接下来 15 分钟里菜单表头写着
	// Paused、bx status 写着「保护此刻被有意压制」,而机器关着只是因为用户想关着;
	// 顺带还会让启动恢复走 HoldArmed 那一支、跳过 desired==off 那支的 restoreDNS,
	// 陈旧的托管 DNS 因此留在系统里。
	//
	// 不武装也是**安全**的:挂起在这条路上要拦的是「活着的 Guardian 把 Core 重启
	// 回来」,而 handleUnexpectedExit 读到 desired != on 本来就直接返回。
	downPurposeUpgradeUnprotected
)

// isUpgrade 报告这次停机是不是升级自己的一步。
//
// 判据写成函数而不是散着比较字面量:downPurposeUser 是 iota == 0,零值恰好是
// 「用户显式关闭」—— 写 desired=off、销挂起。新加第二种升级来由时只改这里,
// 别让它在别处被静静地当成用户的关闭,正是这一期反复抓到的那种漏。
func (p downPurpose) isUpgrade() bool {
	return p == downPurposeUpgradeUnprotected
}

// recordsDesiredOff 报告这次停机该不该在盘上写「用户不想要保护」。
//
// 只有用户明确要关(downPurposeUser)。**升级一台本来就不要保护的机器不在其列** ——
// downPurposeUpgradeUnprotected 的前提就是盘上已经是 off,再写一次只有坏处:
// 用户若恰好在这几秒里点了 Turn On,这一笔会把他刚说的话抹掉,而 desired
// 只由用户改。
func (p downPurpose) recordsDesiredOff() bool {
	return p == downPurposeUser
}

type macOSLifecycleDeps struct {
	guardianInstalled func() bool
	writeGuardianUnit func(string) error
	enableGuardian    func() error
	guardianReady     func(context.Context) bool
	legacyInstalled   func() bool
	// legacyLoaded 带 ctx,因为它会 fork 两次 `launchctl print`,而停止路径上
	// 一个卡住的 launchd 就是 2026-08-04 那次 71 分钟事故的形状。调用方负责
	// 给它一个有界的 ctx(见 legacyProbeTimeout)。
	legacyLoaded func(context.Context) (bool, error)
	// bootoutLegacyUnit 解除 legacy Core 的 launchd job。**它不是可选的礼貌
	// 动作**:legacy plist 带无条件的 KeepAlive(install.go 的 launchd 模板),
	// 所以只请求 Core 退出的话,launchd 会立刻(或过了 10s 节流后)把它拉回来
	// —— 「已停止」于是在几秒后重新变成假话。Guardian 自己的迁移用的就是这个
	// 函数(Manager.Migrate → m.legacy.Stop → install.BootoutLegacyCoreUnit)。
	bootoutLegacyUnit func(context.Context) error
	removeLegacyUnit  func() error
	migrationRequest  func(context.Context, string) (guardian.MigrationRequest, error)
	// pendingSwitch / clearPendingSwitch:一次没交接完的屏障下切换(switchhandoff.go)。
	// nil 表示本平台没有这回事。
	pendingSwitch      func() (guardian.MigrationRequest, bool, error)
	clearPendingSwitch func() error
	client             guardianLifecycleClient
	consoleUID         func() (int, error)
	ensureMenu         func(int) error
	pollInterval       time.Duration
	// The five hooks below make up the `bx down` escape hatch, run by
	// forcedMacOSTeardown whenever the clean Guardian transaction is
	// unavailable or fails. None of them installs or bootstraps anything,
	// and none of them removes /etc/bx, /var/lib/bx or any other file —
	// that is `bx uninstall`'s job.
	//
	// stopCore asks a running Core to cancel its own Run context over its
	// control socket, so Core's defer-based teardown restores the routes it
	// installed. This must happen before forceTeardown: it is the only
	// deterministic way to stop Core. (Guardian's Shutdown never signals
	// Core. Plists written before 2026-09-25 let launchd kill Guardian's
	// whole process group on bootout — Core included, verified on a real
	// Mac — but the current plist sets AbandonProcessGroup so Core outlives
	// a Guardian restart; stopOrphanedCore covers the case this step
	// cannot reach.)
	stopCore func(context.Context) error
	// forceTeardown stops the Guardian launchd service (only).
	forceTeardown func(context.Context) error
	// stopOrphanedCore stops the recorded Core if it outlived the Guardian
	// bootout (see forcedMacOSTeardown step 3b).
	stopOrphanedCore func(context.Context) error
	// markDesiredOff persists desired=off. It runs *first*, before Core is
	// stopped: a live Guardian's monitor reacts to Core's exit by reading
	// this very state off the store (Manager.handleUnexpectedExit), and
	// while it still reads On it reinstalls the blocking barrier and
	// restarts Core behind us. With Off already persisted that handler
	// returns immediately. It also keeps Guardian's RunAtLoad+KeepAlive
	// plist from restarting protection at the next boot — the forced path
	// never reaches Manager.Down, which is what normally records it.
	markDesiredOff func() error
	// clearMaintenanceHold 撤销挂起。它跑在用户显式关闭的路上,**对拆除的成败
	// 无条件** —— 强制拆除即使报告失败,六步破坏性动作也已经做完了,躲在
	// `if err == nil` 后面的销账正是欠条今天留下陈旧记录的原因。
	clearMaintenanceHold func() error
	// restoreSystemDNS puts the macOS resolver back to its saved servers.
	// On macOS it is *Guardian* that points the system at 127.0.0.1
	// (guardian/dns.go → install.EnableDNSContext), and only Manager.Down
	// undoes it; Daemon.Shutdown does not, and Core cannot — internal/
	// supervisor does not even import internal/install, it merely listens
	// on 127.0.0.1:53. Skipping this leaves DNS aimed at a listener that
	// just exited, i.e. the "网页打不开" symptom that motivated this whole
	// escape hatch, with routes that look perfectly clean. It runs after
	// forceTeardown because a live Guardian would just take DNS back, and
	// after clearBarrierRoutes: this step shells out to several external
	// commands with no timeout of their own, so forcedMacOSTeardown wraps
	// it in dnsRestoreTimeout and runs it *after* the barrier is cleared —
	// restoring connectivity does not depend on DNS being restored first,
	// and must not be delayed behind a command that can hang.
	restoreSystemDNS func(context.Context) error
	// clearBarrierRoutes deletes the barrier's blocking routes. Booting
	// Guardian out drops its in-memory ownership record while the kernel
	// keeps the /2 reject routes, which outrank Core's /1 split-default and
	// blackhole the whole machine with nothing left able to remove them.
	// This is not a fail-closed regression: it happens only because the
	// user explicitly asked to stop protection. It is the step that
	// actually gets the user back online, so it runs as early as
	// forceTeardown allows — in particular before restoreSystemDNS, whose
	// external commands have no bound of their own and must never be
	// allowed to delay route cleanup. Deleting routes touches no DNS
	// state, so the two steps have no ordering dependency in the other
	// direction either.
	clearBarrierRoutes func(context.Context) error
}

type macOSUpResult struct {
	Status      guardian.Status
	MenuWarning error
}

func defaultMacOSLifecycleDeps() macOSLifecycleDeps {
	// This client issues the actual mutations (Up/Down/Migrate below), so it
	// needs guardianMutationClientTimeout rather than NewClient's default
	// 30s — see that constant's comment.
	client := guardian.NewClientWithTimeout(guardian.SocketPath, guardianMutationClientTimeout)
	return macOSLifecycleDeps{
		guardianInstalled: install.GuardianInstalled,
		writeGuardianUnit: func(configPath string) error {
			return install.WriteGuardianUnit(install.GuardianExecutable(), configPath)
		},
		enableGuardian: install.EnableGuardian,
		guardianReady: func(ctx context.Context) bool {
			return waitGuardianSocket(ctx, guardian.SocketPath, guardianReadyTimeout, guardianReadyPollInterval)
		},
		legacyInstalled:   install.LegacyCoreInstalled,
		legacyLoaded:      install.LegacyCoreLoadedContext,
		bootoutLegacyUnit: install.BootoutLegacyCoreUnit,
		removeLegacyUnit:  install.RemoveLegacyCoreUnit,
		migrationRequest: func(ctx context.Context, configPath string) (guardian.MigrationRequest, error) {
			return legacyMigrationRequest(ctx, configPath, migrationMetadataDeps{})
		},
		pendingSwitch:      loadSwitchHandoff,
		clearPendingSwitch: clearSwitchHandoff,
		client:             client,
		consoleUID:         consoleUserUID,
		ensureMenu:         ensureMacOSMenuRunning,
		pollInterval:       100 * time.Millisecond,
		stopCore:           shutdownRunningCore,
		forceTeardown:      install.BootoutGuardian,
		stopOrphanedCore:   stopOrphanedCore,
		markDesiredOff:     func() error { return guardian.OpenDefaultStore().SaveDesired(guardian.DesiredOff) },
		clearMaintenanceHold: func() error {
			_, err := guardian.OpenDefaultStore().ClearMaintenanceHold()
			return err
		},
		clearBarrierRoutes: func(ctx context.Context) error {
			return guardian.RemoveBlockingBarrierRoutes(ctx, nil)
		},
		// An empty service name means "the network service recorded when
		// DNS was taken over" (install.disableDNSDarwinContextWithRunner
		// falls back to the saved state's service), which is exactly how
		// Guardian itself calls it — guardian/daemon.go builds its DNS
		// manager with NewDNSManager(""). It is a no-op returning nil when
		// no takeover state exists and DNS is not pointed at bx.
		restoreSystemDNS: func(ctx context.Context) error {
			_, err := install.DisableDNSContext(ctx, "")
			return err
		},
	}
}

// stopOrphanedCore 停掉 core-process.json 记下、在 Guardian 退出之后还活着的那个
// Core(guardian.ExecCoreRunner.StopOrphanedCore)。Guardian 的 plist 带
// AbandonProcessGroup 之后,「bootout Guardian 顺带杀掉 Core」不再成立,凡是
// 用户明确要停掉 bx 的路径(强制拆除、卸载)都经它显式收尾。
func stopOrphanedCore(ctx context.Context) error {
	runner := guardian.NewExecCoreRunner(install.GuardianExecutable(), defaultConfigPath, darwinDNSListen)
	stopped, err := runner.StopOrphanedCore(ctx)
	if stopped {
		fmt.Println("✓ Stopped a Core that was still running after Guardian exited")
	}
	return err
}

// shutdownRunningCore asks the Core process that owns the control socket to
// cancel its own Run context, then gives it a bounded moment to exit so its
// defer-based restore can finish before the caller boots Guardian out. This
// is the same cooperative protocol Guardian's own ExecCoreRunner.Stop uses.
// A Core that is unreachable is not an error: there is simply nothing to
// stop, and the remaining teardown steps still have work to do.
func shutdownRunningCore(ctx context.Context) error {
	return shutdownRunningCoreWithin(ctx, coreShutdownWait)
}

func shutdownRunningCoreWithin(ctx context.Context, wait time.Duration) error {
	socketPath := statusSocketPath()
	state, err := supervisor.FetchRuntimeState(socketPath)
	if err != nil || state.PID <= 0 {
		return nil
	}
	if err := supervisor.ShutdownControl(ctx, socketPath, state.PID); err != nil {
		return fmt.Errorf("asking Core (PID %d) to shut down cooperatively: %w", state.PID, err)
	}
	waitCoreSocketClosed(ctx, socketPath, wait)
	return nil
}

// waitCoreSocketClosed polls until Core's control socket stops accepting
// connections (it closes as the process exits, after its teardown defers
// have restored routes and DNS). Best effort: a Core that outlives the wait
// is reported by neither this nor the caller, because the remaining forced
// steps still improve the user's situation.
func waitCoreSocketClosed(ctx context.Context, socketPath string, wait time.Duration) {
	deadline := time.Now().Add(wait)
	var dialer net.Dialer
	for time.Now().Before(deadline) {
		conn, err := dialer.DialContext(ctx, "unix", socketPath)
		if err != nil {
			return
		}
		conn.Close()
		timer := time.NewTimer(coreShutdownPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func guardianCommand() *urfavecli.Command {
	return guardianCommandWithDeps(guardianCommandDeps{geteuid: os.Geteuid, run: guardian.RunDaemon})
}

func guardianCommandWithDeps(deps guardianCommandDeps) *urfavecli.Command {
	return &urfavecli.Command{
		Name:   "guardian",
		Usage:  "run the macOS Guardian lifecycle daemon",
		Hidden: true,
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{Name: "config", Value: defaultConfigPath},
			&urfavecli.StringFlag{Name: "listen-dns", Value: darwinDNSListen},
		},
		Action: func(c *urfavecli.Context) error {
			if deps.geteuid == nil || deps.geteuid() != 0 {
				return fmt.Errorf("bx guardian requires root")
			}
			if deps.run == nil {
				return fmt.Errorf("Guardian daemon runner unavailable")
			}
			ctx, cancel := signal.NotifyContext(c.Context, syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			return deps.run(ctx, guardian.DaemonOptions{
				ConfigPath: c.String("config"),
				DNSListen:  c.String("listen-dns"),
				SocketPath: guardian.SocketPath,
				// 菜单不再 spawn `bx update --check --json`:同一件事由 Guardian
				// 代查、经 /v1/update-check 发布。取数函数留在 CLI 侧,因为 release
				// 查询与 manifest 签名校验本来就住在这里。
				UpdateCheck: checkLatestReleaseAvailability,
			})
		},
	}
}

func legacyMigrationRequest(ctx context.Context, configPath string, deps migrationMetadataDeps) (guardian.MigrationRequest, error) {
	discoverGateway := deps.discoverGateway
	if discoverGateway == nil {
		discoverGateway = guardian.DiscoverDefaultGateway
	}
	gateway, err := discoverGateway(ctx)
	if err != nil {
		return guardian.MigrationRequest{}, fmt.Errorf("discover migration gateway: %w", err)
	}

	fetchRuntime := deps.fetchRuntime
	if fetchRuntime == nil {
		fetchRuntime = supervisor.FetchRuntimeState
	}
	if runtimeState, runtimeErr := fetchRuntime(statusSocketPath()); runtimeErr == nil {
		if err := validateLegacyRuntimeHandoff(runtimeState); err != nil {
			return guardian.MigrationRequest{}, err
		}
		return guardian.ValidateMigrationRequest(guardian.MigrationRequest{
			Gateway:      gateway,
			ServerBypass: append([]string(nil), runtimeState.ServerBypass...),
		})
	}

	loadExistingConfig := deps.loadConfig
	if loadExistingConfig == nil {
		loadExistingConfig = loadConfig
	}
	cfg, err := loadExistingConfig(configPath)
	if err != nil || cfg == nil {
		return guardian.MigrationRequest{}, fmt.Errorf("cannot read existing client configuration for migration")
	}
	lookupIP := deps.lookupIP
	if lookupIP == nil {
		lookupIP = lookupMigrationIPs
	}
	links := append([]string(nil), cfg.Transports...)
	if len(links) == 0 && cfg.Server != "" {
		links = append(links, cfg.Server)
	}
	if cfg.UDP.Mode == "proxy" && cfg.UDP.Transport != "" {
		links = append(links, cfg.UDP.Transport)
	}
	var bypasses []string
	for i, link := range links {
		host := serverHostFromLink(link)
		if host == "" {
			return guardian.MigrationRequest{}, fmt.Errorf("configured migration transport %d has no valid server host", i+1)
		}
		addresses, err := lookupIP(ctx, host)
		if err != nil || len(addresses) == 0 {
			return guardian.MigrationRequest{}, fmt.Errorf("configured migration transport %d server cannot be resolved", i+1)
		}
		for _, address := range addresses {
			address = address.Unmap()
			if address.IsValid() {
				bypasses = append(bypasses, netip.PrefixFrom(address, address.BitLen()).String())
			}
		}
	}
	return guardian.ValidateMigrationRequest(guardian.MigrationRequest{Gateway: gateway, ServerBypass: bypasses})
}

func macOSUpLifecycle(ctx context.Context, configPath string, deps macOSLifecycleDeps) (macOSUpResult, error) {
	status, migrated, err := ensureGuardianOwnership(ctx, configPath, deps)
	if err != nil {
		return macOSUpResult{}, err
	}
	if !migrated {
		status, err = deps.client.Up(ctx)
		if err != nil {
			return macOSUpResult{}, err
		}
	}
	status, err = waitGuardianProtected(ctx, status, deps.client, deps.pollInterval)
	if err != nil {
		return macOSUpResult{}, err
	}
	result := macOSUpResult{Status: status}
	uid, err := deps.consoleUID()
	if err != nil {
		result.MenuWarning = err
		return result, nil
	}
	if err := deps.ensureMenu(uid); err != nil {
		result.MenuWarning = err
	}
	return result, nil
}

// macOSDownLifecycle is the plain two-value entry point kept for callers
// (and tests) that don't need to distinguish the forced-teardown path from
// the clean one. macOSDownAction uses macOSDownLifecycleDetailed instead, so
// it can report honestly when it had to fall back.
func macOSDownLifecycle(ctx context.Context, configPath string, deps macOSLifecycleDeps) (guardian.Status, error) {
	result, err := macOSDownLifecycleDetailed(ctx, configPath, deps)
	return result.Status, err
}

// macOSDownResult reports which path `bx down` actually took, so the command
// can describe honestly what it did rather than claiming a clean shutdown it
// did not perform.
type macOSDownResult struct {
	Status guardian.Status
	// Forced is true when the clean Guardian transaction was skipped or
	// abandoned and the escape hatch ran instead.
	Forced bool
	// Cause is the clean-path error that triggered the fallback; nil when
	// Guardian was simply unreachable to begin with.
	Cause error
	// LegacyCore is true when the forced path was chosen because a Core that
	// Guardian does not own may still be running (see legacyCoreMayBeRunning).
	// It exists so the report can say why: without it, "forced with no Cause"
	// renders as "Guardian 未响应", which in this case is simply false —
	// Guardian answered, we chose the heavier path on purpose.
	LegacyCore bool
}

// macOSDownLifecycleDetailed implements `bx down`'s two paths:
//
//   - Guardian reachable and healthy: commit the clean shutdown, and
//     nothing else — deps.client.Down (or DownForUpgrade) is the whole
//     transaction. This is the cleanest outcome and stays the default.
//     It deliberately does no startup work: legacy-Core handoff bookkeeping
//     lives on macOSUpLifecycle, where it belongs (see cleanGuardianDown).
//   - Anything else: run the forced teardown. That covers Guardian being
//     unreachable (installing/bootstrapping it and waiting for it to become
//     ready before `down` could proceed would be backwards: "stop" must
//     never depend on first successfully "starting" the thing being
//     stopped) *and* Guardian answering while refusing to
//     shut down. The latter is not hypothetical: Manager.Down returns
//     errRecoveryIncomplete as its first statement whenever recoveryBlocked
//     is set, which a Guardian restart during a network outage makes
//     permanent — the socket answers, the transaction installs its
//     block-only barrier, and then fails forever. Without a fallback here
//     the user is left cut off with no way out.
//
// It never installs or bootstraps Guardian on the forced path, and that path
// never touches /etc/bx or /var/lib/bx — only `bx uninstall` removes files.
func macOSDownLifecycleDetailed(ctx context.Context, configPath string, deps macOSLifecycleDeps) (macOSDownResult, error) {
	return macOSDownLifecycleFor(ctx, downPurposeUser, configPath, deps)
}

// macOSDownLifecycleFor 是带用途的入口:升级(保护本就关着的机器)用
// downPurposeUpgradeUnprotected 调它,其余一律经 macOSDownLifecycleDetailed 走
// downPurposeUser。
func macOSDownLifecycleFor(ctx context.Context, purpose downPurpose, configPath string, deps macOSLifecycleDeps) (result macOSDownResult, err error) {
	// 用户明确要关保护,而一次屏障下切换没交接完:那道屏障是 CLI 装的,Guardian 不
	// 拥有它,干净的 Down 不会去拆 —— 不拆就是「关掉了保护却还断着网」。拆掉并销掉
	// 记录;两步都对 Down 的成败无条件(与销挂起同一条:停止不许依赖别的先成功)。
	if purpose == downPurposeUser && deps.pendingSwitch != nil {
		if _, pending, _ := deps.pendingSwitch(); pending {
			defer func() {
				if deps.clearBarrierRoutes != nil {
					if clearErr := deps.clearBarrierRoutes(ctx); clearErr != nil {
						err = errors.Join(err, fmt.Errorf("removing the barrier left by an unfinished Guardian switch: %w", clearErr))
					}
				}
				if deps.clearPendingSwitch != nil {
					_ = deps.clearPendingSwitch()
				}
			}()
		}
	}
	if deps.guardianReady != nil && deps.guardianReady(ctx) {
		// 一个便宜的判定,决定走哪条路 —— 不是启动的活。
		if mayRun, known := legacyCoreMayBeRunning(ctx, deps); mayRun {
			// **先解除 job,再进强制拆除。** legacy plist 带无条件 KeepAlive,
			// 只让 Core 退出的话 launchd 会把它拉回来,「已停止」几秒后重新
			// 变成假话。放在 forcedMacOSTeardown **之前**有两个理由:一是必须
			// 早于它的第 2 步(请求 Core 退出),否则中间留一个重启窗口;二是
			// forcedMacOSTeardown 的六步顺序被专门的测试逐字钉住,而那六步是
			// 「Guardian 不可达」那条路也要跑的通用拆除 —— 解除 legacy job 只
			// 属于这条分支,不该混进那个通用序列。
			//
			// best-effort:失败不阻断下面任何一步(「停止」不许依赖先成功做成
			// 别的事),但也**不吞掉** —— job 还armed 着意味着 Core 会回来。
			legacyErr := disarmLegacyCoreUnit(ctx, deps)
			// 解除失败只在**确知 job 加载着**时才是硬失败:那时 job 还armed,
			// Core 一定回来,「已停止」是假话。而探查失败时我们其实不知道有没有
			// legacy unit —— 一次 launchctl 抖动不该让一台干净机器的 bx down
			// 整个失败。那正是「停止不许依赖别的先成功」本身。
			if !known {
				legacyErr = nil
			}
			forcedErr := forcedMacOSTeardown(ctx, purpose, deps, nil)
			if err := errors.Join(legacyErr, forcedErr); err != nil {
				// 结果必须带上 Forced/LegacyCore:调用方拿零值去渲染的话,
				// downReportLines 对零值渲染的正是 "✅ bx 已停止并取消开机自启。"
				// —— 这一期要杀的那句原话,离被打印只差一个忽略 err 的 caller。
				return macOSDownResult{Forced: true, LegacyCore: true}, err
			}
			return macOSDownResult{
				Status:     guardian.Status{Protection: guardian.ProtectionOff},
				Forced:     true,
				LegacyCore: true,
			}, nil
		}
		status, cleanErr := cleanGuardianDown(ctx, purpose, configPath, deps)
		if cleanErr == nil {
			return macOSDownResult{Status: status}, nil
		}
		if err := forcedMacOSTeardown(ctx, purpose, deps, cleanErr); err != nil {
			return macOSDownResult{Forced: true, Cause: cleanErr}, err
		}
		return macOSDownResult{Status: guardian.Status{Protection: guardian.ProtectionOff}, Forced: true, Cause: cleanErr}, nil
	}
	if err := forcedMacOSTeardown(ctx, purpose, deps, nil); err != nil {
		return macOSDownResult{Forced: true}, err
	}
	return macOSDownResult{Status: guardian.Status{Protection: guardian.ProtectionOff}, Forced: true}, nil
}

// disarmLegacyCoreUnit 解除 legacy Core 的 launchd job,使它在被请求退出之后
// **留在**停止状态。没有这一步,停止只是暂时的:legacy plist 的 KeepAlive 是
// 无条件的(不是菜单栏那种 {SuccessfulExit:false}),launchd 会把 Core 拉回来。
//
// 这一步不是这次新发明的语义 —— 改动前的停止路径经 ensureGuardianOwnership →
// Migrate 走到 Manager.Migrate,那里做的正是 m.legacy.Stop(BootoutLegacyCoreUnit)
// 加 m.legacy.Remove。我们不再迁移,但「让它别再自己起来」这条必须留下。
//
// 只解除 job,不删 plist:删文件是 `bx uninstall` 的职责,拆除路径一个文件都不碰。
func disarmLegacyCoreUnit(ctx context.Context, deps macOSLifecycleDeps) error {
	if deps.bootoutLegacyUnit == nil {
		return nil
	}
	if err := runWithTimeout(ctx, legacyBootoutTimeout, deps.bootoutLegacyUnit); err != nil {
		return fmt.Errorf(
			"could not disable the older Core's start-at-boot (launchd job): %w\n"+
				"It has KeepAlive set, so it may restart itself. Run "+elevate.Prefix+"bx uninstall — that stops and removes both legacy labels (com.getbx.bx and com.ggshr9.bx), whereas booting out just one of them can miss",
			err,
		)
	}
	return nil
}

// legacyCoreMayBeRunning 回答一个便宜的问题:此刻有没有可能存在一个 Guardian
// 并不掌管的旧版 Core。它只做一次 `launchctl print`(install.LegacyCoreLoaded),
// 不做网关发现、不读 Core runtime、不解析 config、不查 DNS —— 那些正是这次从停止
// 路径上摘掉的东西。
//
// **为什么停止路径非问不可**:legacy Core 是旧 launchd unit 起的,Guardian 从没
// 为它写过 /var/lib/bx/core-process.json。于是 ExecCoreRunner.Existing 读不到文件、
// 返回 PID 0,Manager.Down 里 `if process.PID != 0 { runner.Stop(...) }` 整个被跳过:
// Guardian 装屏障、写 desired=off、还原 DNS、**报成功**,而那个 Core 连同它的 TUN
// 和路由一动没动。用户被告知「已停止」,保护其实还开着 —— 这比一次多余的强制拆除
// 坏得多。只有强制路径停得下它:stopCore 走 supervisor.ShutdownControl 对着 Core
// 自己的控制 socket 说话,与它由哪个 launchd unit 拉起来无关。
//
// **问不出来时返回 true**:探查失败(或钩子缺失)证明不了「没有」,而安全方向是
// 那条万一有就能停下它的路。走错了的代价是一次本可以更轻的停止变重;反过来赌错的
// 代价是对用户说一句假话。与本仓库别处的三态纪律同形:「无法判定」不许塌缩成「否」。
func legacyCoreMayBeRunning(ctx context.Context, deps macOSLifecycleDeps) (mayRun, known bool) {
	if deps.legacyLoaded == nil {
		return true, false
	}
	probeCtx, cancel := context.WithTimeout(ctx, legacyProbeTimeout)
	defer cancel()
	loaded, err := deps.legacyLoaded(probeCtx)
	if err != nil {
		return true, false
	}
	return loaded, true
}

// cleanGuardianDown 停止只做停止。
//
// 这里**曾经**先调 ensureGuardianOwnership —— 那会在发 /v1/down 之前 shell 出去查
// legacy Core,legacy Core 真在跑时还要做网关发现 + 读 Core /v0/runtime + 解析 config
// + DNS 查询。任何一步报错,一次本可以干净完成的停止就升级成拆屏障、还原 DNS 的重手术。
//
// 而且那个函数的契约是「确保它装好并起着」——在停止路径上调用它本身就是错的,
// 哪怕 guardianEnableCommands 的 active&&ready 快路径让它今天恰好是空操作:
// 那是巧合不是契约,谁把它改成「总是 kickstart 一次」,down 就会在停止前重启守护进程。
//
// legacy Core 的迁移没有丢,它留在 up 上(macOSUpLifecycle),那本来就是它该在的地方。
//
// configPath 因此在这里不再被用到;参数保留,是为了让调用方的形状不随这次收窄而变。
func cleanGuardianDown(ctx context.Context, purpose downPurpose, configPath string, deps macOSLifecycleDeps) (guardian.Status, error) {
	_ = configPath // 见上:停止路径不再读 config,参数只为保持调用方形状不变
	if purpose.isUpgrade() {
		// 干净路径也必须带上这个标记:Guardian 把普通的 Down 一律当作「用户
		// 不要保护了」—— 写 desired=off、销挂起(2026-08-08 复审 C1)。升级不是
		// 用户显式的关闭,而 desired 只由用户改;升级路径上真有两个写入点
		// (这里与 forcedMacOSTeardown),漏一个等于没修。
		return deps.client.DownForUpgrade(ctx)
	}
	return deps.client.Down(ctx)
}

// forcedMacOSTeardown is the escape hatch. Every step is best effort and all
// of them run even when an earlier one fails: bailing out early is what
// leaves the machine unusable (an unremoved barrier blackholes everything,
// and a desired=On state brings the broken protection back at next boot).
// Failures are collected and reported together with the next steps the user
// can take by hand.
func forcedMacOSTeardown(ctx context.Context, purpose downPurpose, deps macOSLifecycleDeps, cause error) error {
	if deps.forceTeardown == nil {
		return fmt.Errorf("Guardian could not shut down cleanly, and forced teardown is not available on this platform")
	}
	var failures []error
	// 1. Record the intent BEFORE touching anything. A Guardian that is still
	//    alive treats Core's exit as a crash and, as long as the store still
	//    says On (and no maintenance hold is armed), reinstalls the blocking
	//    barrier and restarts Core (Manager.handleUnexpectedExit) — racing
	//    every step below. Recording first makes that handler a no-op instead.
	//
	//    desired=off 写失败只记在 desiredErr,不立刻进 failures:第 6 步
	//    (Guardian 已被 bootout,那次写入是权威的)再写一次成功就把它清掉。
	//
	//    **升级一台本来就不要保护的机器时什么都不写**(见
	//    downPurposeUpgradeUnprotected):盘上已经是 off,而 desired 只由用户改。
	//    代价是一个已知的窄窗口 —— 用户恰好在这几秒里点了 Turn On,那个 on 没有
	//    挂起拦着,一个还活着的 Guardian 可能在换二进制期间把 Core 起回来。选它
	//    是因为另一头更糟:替用户写一句他没说过的 off,正是这一期要消灭的谎。
	var desiredErr error
	if purpose.recordsDesiredOff() {
		desiredErr = persistDesiredOff(deps)
	}
	// 用户明确要关 ⇒ 销挂起。**这一步对拆除的成败无条件**(见 Manager.Down 的
	// 同款注释):强制拆除即使报告失败,六步破坏性动作也已经做完了,而躲在
	// `if err == nil` 后面的销账正是欠条今天留下陈旧记录的原因。清不掉只是一条
	// 警告 —— ClearMaintenanceHold 只有 ENOENT 幂等,EACCES/EIO 照样回错误,
	// 而「停止」永不许因为一个记账文件而中止剩下的步骤。
	if !purpose.isUpgrade() {
		if err := clearMaintenanceHold(deps); err != nil {
			failures = append(failures, fmt.Errorf("clearing the maintenance hold: %w", err))
		}
	}
	// 2. Ask the running Core to stop itself, while it still has a live
	//    path to restore the routes it installed.
	if deps.stopCore != nil {
		if err := deps.stopCore(ctx); err != nil {
			failures = append(failures, fmt.Errorf("shutting Core down cooperatively: %w", err))
		}
	}
	// 3. Stop the Guardian service so it cannot restart Core behind us.
	if err := deps.forceTeardown(ctx); err != nil {
		failures = append(failures, fmt.Errorf("stopping the Guardian service: %w", err))
	}
	// 3b. Stop a Core that outlived its Guardian. Guardian's plist carries
	//     AbandonProcessGroup (so a Guardian crash or restart no longer takes
	//     Core — and its routes — down with it), which also means bootout no
	//     longer kills a Core that step 2 could not reach (socket gone, Core
	//     hung). The user asked for protection off: stop it explicitly, the
	//     record-matched Core only, escalating signal by signal.
	if deps.stopOrphanedCore != nil {
		if err := deps.stopOrphanedCore(ctx); err != nil {
			failures = append(failures, fmt.Errorf("stopping a Core left behind by Guardian: %w", err))
		}
	}
	// 4. Remove the barrier's blocking routes. Nothing else can: Guardian
	//    is gone along with its ownership record. This is the step that
	//    actually gets the user back online (it deletes the 8 reject
	//    routes covering the whole public internet), so it runs before DNS
	//    restore rather than after: the two steps do not depend on each
	//    other, and restore's several unbounded external commands (step 5)
	//    must never be allowed to delay it.
	if deps.clearBarrierRoutes != nil {
		if err := deps.clearBarrierRoutes(ctx); err != nil {
			failures = append(failures, fmt.Errorf("removing the barrier blocking routes: %w", err))
		}
	}
	// 5. Put the system resolver back. Guardian owns the DNS takeover and
	//    is the only thing that ever restores it, so this must happen
	//    after it is gone (it would otherwise take DNS back). Bounded by
	//    dnsRestoreTimeout: app.Run(os.Args) runs under context.Background()
	//    (main.go), which never expires on its own, and this step's
	//    underlying `networksetup`/`dscacheutil`/`killall` calls have no
	//    timeout of their own — without a bound a stuck one would hang `bx
	//    down` forever.
	if deps.restoreSystemDNS != nil {
		if err := runWithTimeout(ctx, dnsRestoreTimeout, deps.restoreSystemDNS); err != nil {
			failures = append(failures, fmt.Errorf("restoring system DNS (web pages stay broken without it; you can run "+elevate.Prefix+"bx dns off by hand): %w", err))
		}
	}
	// 6. Record the intent once more. Cheap, idempotent, and now
	//    authoritative: with Guardian booted out nothing can overwrite it,
	//    so a concurrent Up that raced step 1 cannot leave On behind.
	//    Either write succeeding is enough — the goal is the intent on disk.
	if purpose.recordsDesiredOff() {
		if err := persistDesiredOff(deps); err == nil {
			desiredErr = nil
		} else if desiredErr != nil {
			desiredErr = errors.Join(desiredErr, err)
		}
	}
	if desiredErr != nil {
		failures = append(failures, fmt.Errorf("recording the intent to stop (protection may still start itself on the next boot): %w", desiredErr))
	}
	if len(failures) == 0 {
		return nil
	}
	problems := errors.Join(failures...)
	if cause != nil {
		problems = errors.Join(fmt.Errorf("Guardian's shutdown transaction failed: %w", cause), problems)
	}
	return fmt.Errorf(
		"the forced stop did not finish every step:\n%w\nNext: "+elevate.Prefix+"bx uninstall (stops every service and restores the network, keeping /etc/bx); "+
			"or by hand, sudo launchctl bootout system/com.getbx.bx.guard, then remove the blocking routes one by one:\n  sudo %s",
		problems, strings.Join(blockingRouteCleanupHints(), "\n  sudo "),
	)
}

// runWithTimeout calls fn with a context derived from ctx and bounded by
// timeout, so a hook whose own external commands have no deadline (like
// restoreSystemDNS's networksetup/dscacheutil/killall calls) cannot hang the
// caller indefinitely. It relies on fn's own commands honoring context
// cancellation (exec.CommandContext does); it cannot forcibly interrupt a
// callee that ignores ctx.
func runWithTimeout(ctx context.Context, timeout time.Duration, fn func(context.Context) error) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return fn(timeoutCtx)
}

// persistDesiredOff runs the markDesiredOff hook if one is wired. A missing
// hook is not a failure — the caller's remaining steps still have work to do.
func persistDesiredOff(deps macOSLifecycleDeps) error {
	if deps.markDesiredOff == nil {
		return nil
	}
	return deps.markDesiredOff()
}

// clearMaintenanceHold 撤销挂起。缺钩子不是失败:没有挂起这个概念的平台上,
// 也就没有挂起要清 —— 而「停止」永不许因为一个记账文件而中止。
func clearMaintenanceHold(deps macOSLifecycleDeps) error {
	if deps.clearMaintenanceHold == nil {
		return nil
	}
	return deps.clearMaintenanceHold()
}

func blockingRouteCleanupHints() []string {
	commands := guardian.PlanBlockingBarrierCleanup()
	hints := make([]string, 0, len(commands))
	for _, command := range commands {
		hints = append(hints, command.String())
	}
	return hints
}

func macOSUpAction(c *urfavecli.Context) error {
	configPath := defaultConfigPath
	if _, err := os.Stat(configPath); err != nil {
		return fmt.Errorf("not configured yet. First run: " + elevate.Prefix + "bx setup <client-link>")
	}
	stepLine("Guardian", "taking over and starting bx protection")
	result, err := macOSUpLifecycle(c.Context, configPath, defaultMacOSLifecycleDeps())
	if err != nil {
		// Core 起不来时,把「是哪台服务器、发生了什么、你还能切到哪儿」拼在
		// Guardian 那句只有码的话前面 —— 2026-09-12 那天用户读到的是七次
		// core_ownership_uncertain,而真相(VPS 的 443 没有应答)从第一秒就在。
		// 应答体仍然只带码;地址与清单是这一侧自己从配置里读的(spec §5)。
		return annotateCoreStartFailure(err, configPath)
	}
	stepDone("Guardian", "bx is now Protected")
	if result.MenuWarning != nil {
		fmt.Fprintf(os.Stderr, "⚠️  bx is protected, but the menu bar did not start: %v\n", result.MenuWarning)
	}
	if msg := upVersionMismatchMessage(result.Status.GuardianVersion, result.Status.RuntimeVersion); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	if report, err := readStatusReport(); err == nil {
		printUpSummary(report, result.Status)
		return nil
	}
	fmt.Println("✅ bx started.")
	return nil
}

func macOSDownAction(c *urfavecli.Context) error {
	configPath := defaultConfigPath
	stepLine("Guardian", "stopping bx protection and restoring the network")
	result, err := macOSDownLifecycleDetailed(c.Context, configPath, defaultMacOSLifecycleDeps())
	if err != nil {
		return err
	}
	// 进度行也得跟着 Guardian 的判断走。曾经这里无条件打
	// 「✓ bx 已停止,网络已恢复」,而下面 downReportLines 紧接着说「没能确认关闭」——
	// 用户在相邻两行里同时读到断言和对它的否认。改一处忘一处,正是这类文案的常见死法。
	switch {
	case result.Forced:
		stepDone("Guardian", "bx was force-stopped")
	case downConfirmedStopped(result):
		stepDone("Guardian", "bx stopped, network restored")
	default:
		stepDone("Guardian", "the stop ran, but could not be confirmed")
	}
	stdout, stderrLines := downReportLines(result)
	for _, line := range stderrLines {
		fmt.Fprintln(os.Stderr, line)
	}
	for _, line := range stdout {
		fmt.Println(line)
	}
	return nil
}

// waitGuardianSocket polls the Guardian control socket until it accepts a
// connection or timeout elapses, respecting ctx cancellation. It returns
// true as soon as a dial succeeds.
func waitGuardianSocket(ctx context.Context, socketPath string, timeout, interval time.Duration) bool {
	if interval <= 0 {
		interval = guardianReadyPollInterval
	}
	deadline := time.Now().Add(timeout)
	var dialer net.Dialer
	for {
		dialCtx, cancel := context.WithTimeout(ctx, interval)
		conn, err := dialer.DialContext(dialCtx, "unix", socketPath)
		cancel()
		if err == nil {
			conn.Close()
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func ensureGuardianOwnership(ctx context.Context, configPath string, deps macOSLifecycleDeps) (guardian.Status, bool, error) {
	if deps.client == nil || deps.guardianInstalled == nil || deps.writeGuardianUnit == nil ||
		deps.enableGuardian == nil || deps.guardianReady == nil || deps.legacyInstalled == nil ||
		deps.legacyLoaded == nil || deps.removeLegacyUnit == nil || deps.migrationRequest == nil {
		return guardian.Status{}, false, fmt.Errorf("macOS Guardian lifecycle dependencies unavailable")
	}
	if !deps.guardianInstalled() {
		if err := deps.writeGuardianUnit(configPath); err != nil {
			return guardian.Status{}, false, fmt.Errorf("install Guardian: %w", err)
		}
	}
	legacyLoaded, err := deps.legacyLoaded(ctx)
	if err != nil {
		return guardian.Status{}, false, fmt.Errorf("inspect legacy Core: %w", err)
	}
	var request guardian.MigrationRequest
	// 一次没交接完的屏障下切换:屏障可能还在、服务器 /32 可能已被旧 Core 删掉。
	// 普通的 Up 在这里起不来(新 Guardian 不拥有那道屏障,它起的 Core 连不上服务器),
	// 用记下的那份交接请求走 migrate 才是把它做完。判据读不出来就不猜。
	pendingSwitch := false
	if deps.pendingSwitch != nil && !legacyLoaded {
		pending, ok, err := deps.pendingSwitch()
		if err != nil {
			return guardian.Status{}, false, err
		}
		if ok {
			request, pendingSwitch = pending, true
		}
	}
	switch {
	case legacyLoaded:
		// A live legacy Core is running: it must go through the
		// barrier-protected migration transaction (needs the gateway).
		request, err = deps.migrationRequest(ctx, configPath)
		if err != nil {
			return guardian.Status{}, false, fmt.Errorf("validate legacy Core handoff: %w", err)
		}
	case deps.legacyInstalled():
		// Only an orphan plist remains: nothing is running to hand off,
		// so just delete it — don't demand a default gateway or run a
		// migration transaction for it.
		if err := deps.removeLegacyUnit(); err != nil {
			return guardian.Status{}, false, fmt.Errorf("cleaning up the legacy Core service: %w", err)
		}
	}
	if err := deps.enableGuardian(); err != nil {
		return guardian.Status{}, false, fmt.Errorf("bootstrap Guardian: %w", err)
	}
	if !deps.guardianReady(ctx) {
		return guardian.Status{}, false, fmt.Errorf(
			"the Guardian service did not start (socket %s never became ready). Recent guardian log:\n%s\nTo dig in: sudo launchctl print system/com.getbx.bx.guard; full log at /var/log/bx-guard.err.log",
			guardian.SocketPath, install.GuardianLogTail(10),
		)
	}
	if !legacyLoaded && !pendingSwitch {
		return guardian.Status{}, false, nil
	}
	status, err := deps.client.Migrate(ctx, request)
	if err != nil {
		if pendingSwitch {
			return guardian.Status{}, false, fmt.Errorf("finish the unfinished Guardian switch: %w", err)
		}
		return guardian.Status{}, false, fmt.Errorf("migrate legacy Core: %w", err)
	}
	if pendingSwitch && deps.clearPendingSwitch != nil {
		if err := deps.clearPendingSwitch(); err != nil {
			fmt.Fprintf(os.Stderr, "! the Guardian switch finished, but its record %s could not be removed: %v\n", switchHandoffPath, err)
		}
	}
	return status, true, nil
}

func waitGuardianProtected(ctx context.Context, status guardian.Status, client guardianLifecycleClient, interval time.Duration) (guardian.Status, error) {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	for {
		if status.Protection == guardian.ProtectionProtected {
			return status, nil
		}
		if status.Phase == guardian.PhaseNeedsAttention {
			return status, fmt.Errorf("Guardian protection needs attention: %s", status.LastError)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return status, ctx.Err()
		case <-timer.C:
		}
		var err error
		status, err = client.Status(ctx)
		if err != nil {
			return status, err
		}
	}
}

func validateLegacyRuntimeHandoff(state supervisor.RuntimeState) error {
	if state.PID <= 0 || state.TunName == "" || !state.TunnelHealthy || !state.DNSListening || !state.RoutesInstalled {
		return fmt.Errorf("running Core returned incomplete migration metadata")
	}
	if state.UDPRequired && !state.UDPReady {
		return fmt.Errorf("running Core UDP handoff is not ready")
	}
	return nil
}

func lookupMigrationIPs(ctx context.Context, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{address.Unmap()}, nil
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}
