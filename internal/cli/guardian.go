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
	// DownForUpgrade 与 Down 停的是同一件事,区别只在**升级欠条**:
	// Guardian 把每一次成功的 Down 当作「用户不要保护了」并销掉欠条,而升级
	// 自己那次停保护必须保住它(见 guardian/upgradeintent.go)。
	DownForUpgrade(context.Context) (guardian.Status, error)
	Migrate(context.Context, guardian.MigrationRequest) (guardian.Status, error)
}

// downPurpose 说明这次停保护是谁要的 —— 决定 Guardian 要不要销掉升级欠条。
//
// 它不是 macOSLifecycleDeps 的一部分:deps 装的是「怎么做」,而这是「为什么
// 做」,同一套 deps 两种用途都成立(app-install 与 bx down 都用
// defaultMacOSLifecycleDeps)。
type downPurpose int

const (
	// downPurposeUser:用户明确要关保护(bx down)。旧欠条一并销账。
	downPurposeUser downPurpose = iota
	// downPurposeUpgrade:升级把停保护当作自己的一步。欠条必须留着,否则
	// 装文件一失败,重试就再也不知道该把保护起回来。
	downPurposeUpgrade
)

type macOSLifecycleDeps struct {
	guardianInstalled func() bool
	writeGuardianUnit func(string) error
	enableGuardian    func() error
	guardianReady     func(context.Context) bool
	legacyInstalled   func() bool
	legacyLoaded      func() (bool, error)
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
		legacyInstalled:  install.LegacyCoreInstalled,
		legacyLoaded:     install.LegacyCoreLoaded,
		removeLegacyUnit: install.RemoveLegacyCoreUnit,
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
	if deps.guardianReady != nil && deps.guardianReady(ctx) {
		status, cleanErr := cleanGuardianDown(ctx, purpose, configPath, deps)
		if cleanErr == nil {
			return macOSDownResult{Status: status}, nil
		}
		if err := forcedMacOSTeardown(ctx, deps, cleanErr); err != nil {
			return macOSDownResult{}, err
		}
		return macOSDownResult{Status: guardian.Status{Protection: guardian.ProtectionOff}, Forced: true, Cause: cleanErr}, nil
	}
	if err := forcedMacOSTeardown(ctx, deps, nil); err != nil {
		return macOSDownResult{}, err
	}
	return macOSDownResult{Status: guardian.Status{Protection: guardian.ProtectionOff}, Forced: true}, nil
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
		// 升级欠条(「还欠你一次把保护起回来」)必须活过这一跳:它是这次
		// app-install 前一秒才写下的,而 Guardian 会把普通的 Down 当作
		// 「用户不要保护了」把它销掉 —— 那正是 2026-08-08 复审 C1。
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
func forcedMacOSTeardown(ctx context.Context, deps macOSLifecycleDeps, cause error) error {
	if deps.forceTeardown == nil {
		return fmt.Errorf("Guardian 无法正常关闭,且强制拆除功能在此平台不可用")
	}
	var failures []error
	// 1. Record the user's intent BEFORE touching anything. A Guardian that
	//    is still alive treats Core's exit as a crash and, as long as the
	//    store still says On, reinstalls the blocking barrier and restarts
	//    Core (Manager.handleUnexpectedExit) — racing every step below.
	//    Persisting Off first makes that handler a no-op instead.
	desiredErr := persistDesiredOff(deps)
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
	// 6. Persist the intent once more. Cheap, idempotent, and now
	//    authoritative: with Guardian booted out nothing can overwrite it,
	//    so a concurrent Up that raced step 1 cannot leave On behind.
	//    Either write succeeding is enough — the goal is Off on disk.
	if err := persistDesiredOff(deps); err == nil {
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
	if result.Forced {
		stepDone("Guardian", "已强制停止 bx")
	} else {
		stepDone("Guardian", "bx 已停止,网络已恢复")
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
	legacyLoaded, err := deps.legacyLoaded()
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
