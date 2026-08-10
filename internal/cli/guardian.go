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
	// downPurposeUpgrade:升级把停保护当作自己的一步。**不写 desired=off**
	// ——用户想要保护,只是此刻不能有;那件事由维护挂起表达。欠条也必须留着,
	// 否则装文件一失败,重试就再也不知道该把保护起回来。
	downPurposeUpgrade
)

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
	client            guardianLifecycleClient
	consoleUID        func() (int, error)
	ensureMenu        func(int) error
	pollInterval      time.Duration
	// The five hooks below make up the `bx down` escape hatch, run by
	// forcedMacOSTeardown whenever the clean Guardian transaction is
	// unavailable or fails. None of them installs or bootstraps anything,
	// and none of them removes /etc/bx, /var/lib/bx or any other file —
	// that is `bx uninstall`'s job.
	//
	// stopCore asks a running Core to cancel its own Run context over its
	// control socket, so Core's defer-based teardown restores the routes it
	// installed. This must happen before forceTeardown: it is the only
	// deterministic way to stop Core. (Relying on `launchctl bootout`'s
	// SIGTERM reaching Core through a shared process group is guesswork —
	// nothing here calls Setpgid/Setsid, Guardian's Shutdown never signals
	// Core, and launchd may follow up with SIGKILL mid-teardown.)
	stopCore func(context.Context) error
	// forceTeardown stops the Guardian launchd service (only).
	forceTeardown func(context.Context) error
	// markDesiredOff persists desired=off. It runs *first*, before Core is
	// stopped: a live Guardian's monitor reacts to Core's exit by reading
	// this very state off the store (Manager.handleUnexpectedExit), and
	// while it still reads On it reinstalls the blocking barrier and
	// restarts Core behind us. With Off already persisted that handler
	// returns immediately. It also keeps Guardian's RunAtLoad+KeepAlive
	// plist from restarting protection at the next boot — the forced path
	// never reaches Manager.Down, which is what normally records it.
	markDesiredOff func() error
	// armMaintenanceHold 武装一次维护挂起(见 guardian.MaintenanceHold)。
	// 升级用它取代「写 desired=off」:磁盘上那句「用户不想要保护」是假的,
	// 而任何忠实的调谐器都会照它把机器收敛到 off。它守的位置与 markDesiredOff
	// 完全一样(在停 Core 之前拦住一个还活着的 Guardian 的重启),只是不撒谎。
	//
	// **写不成时退回 markDesiredOff**,见 recordStopIntent:「既没挂起也没
	// desired=off」是一个新的失效模式,比退回一个会撒谎但安全的状态更糟。
	armMaintenanceHold func(reason string) error
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
		client:         client,
		consoleUID:     consoleUserUID,
		ensureMenu:     ensureMacOSMenuRunning,
		pollInterval:   100 * time.Millisecond,
		stopCore:       shutdownRunningCore,
		forceTeardown:  install.BootoutGuardian,
		markDesiredOff: func() error { return guardian.OpenDefaultStore().SaveDesired(guardian.DesiredOff) },
		armMaintenanceHold: func(reason string) error {
			return guardian.OpenDefaultStore().ArmMaintenanceHold(reason, time.Now())
		},
		clearMaintenanceHold: func() error { return guardian.OpenDefaultStore().ClearMaintenanceHold() },
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
		return fmt.Errorf("请求 Core(PID %d)协作关闭: %w", state.PID, err)
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
	// IntentUnrecorded 非 nil 表示这次停机**既没能武装维护挂起、也没能写下
	// desired=off**(见 recordStopIntent 的退回规则)。保护确实停了,但盘上没有
	// 任何一句话说明「此刻不该有保护」。
	//
	// 它只在**干净路径**上出现:强制路径的 forcedMacOSTeardown 会把同一个错误
	// 折进自己的 failures 一起汇报,而干净路径根本不调它。字段与返回的 error
	// 并存,理由与 Forced/Cause 一样 —— 调用方拿零值去渲染时,
	// downReportLines 会平静地打印「已停止」。
	IntentUnrecorded error
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

// macOSDownLifecycleFor 是带用途的入口:升级用 downPurposeUpgrade 调它,
// 其余一律经 macOSDownLifecycleDetailed 走 downPurposeUser。
func macOSDownLifecycleFor(ctx context.Context, purpose downPurpose, configPath string, deps macOSLifecycleDeps) (macOSDownResult, error) {
	// **意图必须先于任何破坏性动作落盘,干净路径与强制路径都要。**
	//
	// 干净路径也不例外:走完 Manager.Down 之后 CLI 紧接着重启 Guardian
	// (restartGuardianForUpgrade),新 Guardian 的启动恢复读到 desired=on 就会把
	// Core 起回来 —— 而那时二进制正换到一半。武装在这里,那次启动恢复就会看见
	// 挂起并停手。
	stop := recordStopIntent(deps, purpose)
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
			forcedErr := forcedMacOSTeardown(ctx, stop, deps, nil)
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
			// **干净路径是 stop.err 唯一的读者。** forcedMacOSTeardown 会把它
			// 折进自己的 failures,而这一支根本不调它 —— 不在这里读,那次
			// 「挂起与 desired=off 都没写成」就一个字都不会留下,而这条路会
			// 报成功。见 stopIntentFailure。
			return macOSDownResult{Status: status, IntentUnrecorded: stop.err}, stopIntentFailure(stop)
		}
		if err := forcedMacOSTeardown(ctx, stop, deps, cleanErr); err != nil {
			return macOSDownResult{Forced: true, Cause: cleanErr}, err
		}
		return macOSDownResult{Status: guardian.Status{Protection: guardian.ProtectionOff}, Forced: true, Cause: cleanErr}, nil
	}
	if err := forcedMacOSTeardown(ctx, stop, deps, nil); err != nil {
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
			"解除旧版 Core 的开机自启(launchd job)失败:%w\n"+
				"它带 KeepAlive,可能会自己重新启动。请执行 sudo bx uninstall —— 它会把两个旧 label(com.getbx.bx 与 com.ggshr9.bx)都停掉并删除,而单独 bootout 其中一个可能落空",
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
	if purpose == downPurposeUpgrade {
		// 干净路径也必须带上这个标记,守的是**两件**事:
		//   - 这一跳不许把 desired 改写成 off。用户想要保护,变的只是「此刻
		//     不能有」,而那件事由维护挂起表达(recordStopIntent 刚武装好)。
		//     升级路径上真有两个写入点(这里与 forcedMacOSTeardown),漏一个
		//     等于没修。
		//   - 前一秒才武装的挂起、以及那张升级欠条,都不许被这一跳销掉 ——
		//     Guardian 把普通的 Down 一律当作「用户不要保护了」(2026-08-08 复审 C1)。
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
func forcedMacOSTeardown(ctx context.Context, stop stopIntent, deps macOSLifecycleDeps, cause error) error {
	if deps.forceTeardown == nil {
		return fmt.Errorf("Guardian 无法正常关闭,且强制拆除功能在此平台不可用")
	}
	var failures []error
	// 1. Record the intent BEFORE touching anything. A Guardian that is still
	//    alive treats Core's exit as a crash and, as long as the store still
	//    says On (and no maintenance hold is armed), reinstalls the blocking
	//    barrier and restarts Core (Manager.handleUnexpectedExit) — racing
	//    every step below. Recording first makes that handler a no-op instead.
	//
	//    维护(升级)记的是挂起,用户记的是 desired=off;两者都由
	//    recordStopIntent 在更早的时候做过一次(它必须早于干净路径,见那边的
	//    注释),这里再做一次是为了盖住「一次并发的 Up 抢在前面」。
	//
	//    **两边对失败的处置刻意不对称,这是有意的:** 挂起写失败进 failures
	//    (整条拆除报错,升级随之中止),而 desired=off 写失败只进 desiredErr,
	//    第 6 步再写一次成功就把它清掉。理由是两者补救的余地不同 ——
	//    desired=off 有第二次机会且那次是权威的(Guardian 已被 bootout,没人能
	//    覆盖),而挂起没有第二种表达方式:第 1 步与第 6 步写的是同一个文件,
	//    第 1 步失败几乎必然意味着第 6 步也会失败,报绿等于让升级带着一个
	//    「没人拦着」的窗口继续往下走。fail-closed 的方向。
	desiredErr := stop.err
	if stop.armsHold() {
		if err := armMaintenanceHold(deps, guardian.HoldReasonUpgrade); err != nil {
			failures = append(failures, fmt.Errorf("刷新维护挂起: %w", err))
		}
	} else if err := persistDesiredOff(deps); err != nil {
		if desiredErr == nil {
			desiredErr = err
		}
	} else {
		desiredErr = nil
	}
	// 用户明确要关 ⇒ 销挂起。**这一步对拆除的成败无条件**(见 Manager.Down 的
	// 同款注释):强制拆除即使报告失败,六步破坏性动作也已经做完了,而躲在
	// `if err == nil` 后面的销账正是欠条今天留下陈旧记录的原因。清不掉只是一条
	// 警告 —— ClearMaintenanceHold 只有 ENOENT 幂等,EACCES/EIO 照样回错误,
	// 而「停止」永不许因为一个记账文件而中止剩下的步骤。
	if stop.purpose != downPurposeUpgrade {
		if err := clearMaintenanceHold(deps); err != nil {
			failures = append(failures, fmt.Errorf("清除维护挂起: %w", err))
		}
	}
	// 2. Ask the running Core to stop itself, while it still has a live
	//    path to restore the routes it installed.
	if deps.stopCore != nil {
		if err := deps.stopCore(ctx); err != nil {
			failures = append(failures, fmt.Errorf("协作关闭 Core: %w", err))
		}
	}
	// 3. Stop the Guardian service so it cannot restart Core behind us.
	if err := deps.forceTeardown(ctx); err != nil {
		failures = append(failures, fmt.Errorf("停止 Guardian 服务: %w", err))
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
			failures = append(failures, fmt.Errorf("清理屏障阻断路由: %w", err))
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
			failures = append(failures, fmt.Errorf("还原系统 DNS(否则仍会网页打不开,可手动执行 sudo bx dns off): %w", err))
		}
	}
	// 6. Record the intent once more. Cheap, idempotent, and now
	//    authoritative: with Guardian booted out nothing can overwrite it,
	//    so a concurrent Up that raced step 1 cannot leave On behind.
	//    Either write succeeding is enough — the goal is the intent on disk.
	if stop.armsHold() {
		if err := armMaintenanceHold(deps, guardian.HoldReasonUpgrade); err != nil {
			failures = append(failures, fmt.Errorf("刷新维护挂起: %w", err))
		}
	} else if err := persistDesiredOff(deps); err == nil {
		desiredErr = nil
	} else if desiredErr != nil {
		desiredErr = errors.Join(desiredErr, err)
	}
	if desiredErr != nil {
		failures = append(failures, fmt.Errorf("记录关闭意图(下次开机可能仍会自动启动保护): %w", desiredErr))
	}
	if len(failures) == 0 {
		return nil
	}
	problems := errors.Join(failures...)
	if cause != nil {
		problems = errors.Join(fmt.Errorf("Guardian 关闭事务失败: %w", cause), problems)
	}
	return fmt.Errorf(
		"强制停止未能全部完成:\n%w\n下一步:sudo bx uninstall(停止全部服务并还原网络,保留 /etc/bx 配置);"+
			"或手动执行 sudo launchctl bootout system/com.getbx.bx.guard,再逐条删除阻断路由:\n  sudo %s",
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

// stopIntent 是这次停机在盘上留下的那条记录。
//
// **升级的挂起必须在停 Core 之前武装好,而且干净路径与强制路径都要**:干净路径
// 走完 Manager.Down 之后 CLI 紧接着重启 Guardian(restartGuardianForUpgrade),
// 新 Guardian 的启动恢复读到 desired=on 就会把 Core 起回来 —— 二进制正换到一半。
type stopIntent struct {
	purpose  downPurpose
	fellBack bool  // 挂起写不成,已退回 desired=off
	err      error // 退回之后仍失败(两条都没写成),留给拆除步骤汇报
}

// armsHold 报告拆除步骤该记哪一种意图。
//
// 退回之后**不再回头去武装挂起**:那半张写不成的挂起若在第 6 步侥幸写成,盘上
// 就会同时躺着「用户不想要保护」和「此刻不该有保护」两句话,而调谐器与
// handleUnexpectedExit 各读一句。退回是一次性的决定,不是可以反悔的尝试。
func (s stopIntent) armsHold() bool {
	return s.purpose == downPurposeUpgrade && !s.fellBack
}

// recordStopIntent 在任何破坏性动作之前把这次停机的来由写到盘上。
func recordStopIntent(deps macOSLifecycleDeps, purpose downPurpose) stopIntent {
	if purpose != downPurposeUpgrade {
		return stopIntent{purpose: purpose}
	}
	if err := armMaintenanceHold(deps, guardian.HoldReasonUpgrade); err != nil {
		// **退回今天的行为,而不是报错拒绝、也不是继续往下。**
		// 退回意味着调谐器会把机器收敛到 off(与今天一模一样,升级结束会重新
		// 写 on),代价已知且有界;而「既没挂起也没 desired=off」是一个新的
		// 失效模式:活着的 Guardian 在换二进制时把 Core 重启回来。
		// 宁可退回一个会撒谎但安全的状态,也不要一个诚实但没人拦着的状态。
		return stopIntent{purpose: purpose, fellBack: true, err: persistDesiredOff(deps)}
	}
	return stopIntent{purpose: purpose}
}

// stopIntentFailure 把 recordStopIntent 那次「挂起与 desired=off 都没写成」如实
// 汇报出去。
//
// **它不违反「停止永不依赖别的先成功」:停止此刻已经做完了**(Manager.Down 干净
// 返回、屏障已拆、DNS 已还)。这里拒绝的不是停止,而是**继续升级** —— 盘上既没有
// 挂起也没有 desired=off,正是设计取舍三点名的那个新失效模式:下一步
// restartGuardianForUpgrade 拉起来的新 Guardian 会读到 desired=on 自己把 Core
// 起回来,而那时二进制正换到一半。退回规则的全部意义就是让这个状态不存在;
// 两条都写不成时,唯一还剩的处置是别往下走。
//
// 用户显式的 down 到不了这里:recordStopIntent 对 downPurposeUser 直接返回,
// 从不产生 err。
func stopIntentFailure(stop stopIntent) error {
	if stop.err == nil {
		return nil
	}
	return fmt.Errorf(
		"保护已停止,但未能记录停机意图(维护挂起与 desired=off 都没写成): %w\n"+
			"已中止后续步骤:盘上没有任何一句话说明「此刻不该有保护」,继续换二进制会被自动恢复的保护打断。"+
			"请检查 /var/lib/bx 是否可写后重试",
		stop.err,
	)
}

// armMaintenanceHold 武装挂起。**钩子缺失是失败**,与 persistDesiredOff 相反:
// 那边缺钩子只是少记一笔,这边缺钩子意味着「此刻不该有保护」这件事根本没落盘,
// 而调用方正指望它拦住一个活着的 Guardian。报错让 recordStopIntent 退回去写
// desired=off,那是今天的行为 —— 安全,只是会撒谎。
func armMaintenanceHold(deps macOSLifecycleDeps, reason string) error {
	if deps.armMaintenanceHold == nil {
		return fmt.Errorf("维护挂起在此平台不可用")
	}
	return deps.armMaintenanceHold(reason)
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
		return fmt.Errorf("尚未配置。先运行: sudo bx setup <client-link>")
	}
	stepLine("Guardian", "接管并启动 bx 保护")
	result, err := macOSUpLifecycle(c.Context, configPath, defaultMacOSLifecycleDeps())
	if err != nil {
		return err
	}
	stepDone("Guardian", "bx 已进入 Protected")
	if result.MenuWarning != nil {
		fmt.Fprintf(os.Stderr, "⚠️  bx 已受保护,但菜单栏未启动: %v\n", result.MenuWarning)
	}
	if msg := upVersionMismatchMessage(result.Status.GuardianVersion, result.Status.RuntimeVersion); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	if report, err := readStatusReport(); err == nil {
		printUpSummary(report, result.Status)
		return nil
	}
	fmt.Println("✅ bx 已启动。")
	return nil
}

func macOSDownAction(c *urfavecli.Context) error {
	configPath := defaultConfigPath
	stepLine("Guardian", "停止 bx 保护并恢复网络")
	result, err := macOSDownLifecycleDetailed(c.Context, configPath, defaultMacOSLifecycleDeps())
	if err != nil {
		return err
	}
	// 销掉上一次未完成升级留下的欠条,免得下一次 app-install(菜单的 Repair 还
	// 带 --yes,连问都不问)拿旧欠条把保护打开。
	//
	// 干净路径上这件事已经由 guardian.Manager.Down 做过了(所有「说 off」的路径
	// 都汇合在那里);这里补的是**强制拆除**那条 —— Guardian 不可达时
	// forcedMacOSTeardown 直接 bootout,Manager.Down 从未被调用,而用户同样明确
	// 说了「我不要保护」。重复销账是幂等的。
	forgetUpgradeDebtOnExplicitOff(
		clearUpgradeIntent,
		func(line string) { fmt.Fprintln(os.Stderr, line) },
	)
	// 进度行也得跟着 Guardian 的判断走。曾经这里无条件打
	// 「✓ bx 已停止,网络已恢复」,而下面 downReportLines 紧接着说「没能确认关闭」——
	// 用户在相邻两行里同时读到断言和对它的否认。改一处忘一处,正是这类文案的常见死法。
	switch {
	case result.Forced:
		stepDone("Guardian", "已强制停止 bx")
	case downConfirmedStopped(result):
		stepDone("Guardian", "bx 已停止,网络已恢复")
	default:
		stepDone("Guardian", "已执行停止,但未能确认")
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
			return guardian.Status{}, false, fmt.Errorf("清理 legacy Core 服务: %w", err)
		}
	}
	if err := deps.enableGuardian(); err != nil {
		return guardian.Status{}, false, fmt.Errorf("bootstrap Guardian: %w", err)
	}
	if !deps.guardianReady(ctx) {
		return guardian.Status{}, false, fmt.Errorf(
			"Guardian 服务未能启动(socket %s 未就绪)。最近的守护日志:\n%s\n排查:sudo launchctl print system/com.getbx.bx.guard;完整日志 /var/log/bx-guard.err.log",
			guardian.SocketPath, install.GuardianLogTail(10),
		)
	}
	if !legacyLoaded {
		return guardian.Status{}, false, nil
	}
	status, err := deps.client.Migrate(ctx, request)
	if err != nil {
		return guardian.Status{}, false, fmt.Errorf("migrate legacy Core: %w", err)
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
