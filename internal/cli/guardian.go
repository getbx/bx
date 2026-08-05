package cli

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
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
	Migrate(context.Context, guardian.MigrationRequest) (guardian.Status, error)
}

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
	// forceTeardown is the `bx down` escape hatch: it stops the Guardian
	// launchd service (only — never installs/bootstraps it, never touches
	// /etc/bx or /var/lib/bx) so the daemon process exits and its own
	// defer-based teardown restores routes/DNS. Used when Guardian is
	// unreachable, so `bx down` never depends on Guardian successfully
	// starting up first (see macOSDownLifecycle).
	forceTeardown func(context.Context) error
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
		client:        client,
		consoleUID:    consoleUserUID,
		ensureMenu:    ensureMacOSMenuRunning,
		pollInterval:  100 * time.Millisecond,
		forceTeardown: install.BootoutGuardian,
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
			return deps.run(ctx, guardian.DaemonOptions{ConfigPath: c.String("config"), DNSListen: c.String("listen-dns"), SocketPath: guardian.SocketPath})
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
	status, _, err := macOSDownLifecycleDetailed(ctx, configPath, deps)
	return status, err
}

// macOSDownLifecycleDetailed implements `bx down`'s two paths:
//
//   - Guardian reachable: go through the same clean ownership+Down
//     transaction as before (ensureGuardianOwnership handles legacy-Core
//     handoff bookkeeping, then deps.client.Down commits the shutdown).
//   - Guardian unreachable: this is exactly the moment a user most needs
//     to be able to shut bx down (e.g. Guardian stuck in an infinite
//     recovery retry loop after a network fault — the production
//     incident this fixes). Calling ensureGuardianOwnership here would
//     install/bootstrap Guardian and require it to become ready before
//     `down` could proceed, which is backwards: "stop" must never depend
//     on first successfully "starting" the thing being stopped. Instead
//     fall back to forceTeardown, which only stops the Guardian service;
//     the running core process (if any) notices via its own health/ctx
//     machinery and its defer-based teardown restores routes/DNS.
//
// It never installs or bootstraps Guardian on the unreachable path, and the
// forced path never touches /etc/bx or /var/lib/bx — only `bx uninstall`
// removes files.
func macOSDownLifecycleDetailed(ctx context.Context, configPath string, deps macOSLifecycleDeps) (status guardian.Status, forced bool, err error) {
	if deps.guardianReady != nil && deps.guardianReady(ctx) {
		if _, _, err := ensureGuardianOwnership(ctx, configPath, deps); err != nil {
			return guardian.Status{}, false, err
		}
		status, err = deps.client.Down(ctx)
		return status, false, err
	}
	if deps.forceTeardown == nil {
		return guardian.Status{}, false, fmt.Errorf("Guardian 不可达,且强制拆除功能在此平台不可用")
	}
	if err := deps.forceTeardown(ctx); err != nil {
		return guardian.Status{}, false, fmt.Errorf(
			"Guardian 未响应,强制停止也失败: %w。请尝试 sudo bx uninstall 脱离,或手动执行: sudo launchctl bootout system/com.getbx.bx.guard",
			err,
		)
	}
	return guardian.Status{Protection: guardian.ProtectionOff}, true, nil
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
	_, forced, err := macOSDownLifecycleDetailed(c.Context, configPath, defaultMacOSLifecycleDeps())
	if err != nil {
		return err
	}
	if forced {
		stepDone("Guardian", "Guardian 未响应,已强制停止并还原网络")
		fmt.Println("⚠️  Guardian 未响应,已强制停止服务;网络路由/DNS 将由其进程退出时的自动还原逻辑恢复。")
		return nil
	}
	stepDone("Guardian", "bx 已停止,网络已恢复")
	fmt.Println("✅ bx 已停止并取消开机自启。")
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
