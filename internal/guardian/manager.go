package guardian

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getbx/bx/internal/supervisor"
)

const (
	ProtectionOff            = "off"
	ProtectionStarting       = "starting"
	ProtectionRecovering     = "recovering"
	ProtectionProtected      = "protected"
	ProtectionBlocked        = "blocked"
	ProtectionNeedsAttention = "needs_attention"
)

type barrierProof uint8

// A barrier credential identifies one ownership epoch. Install assigns a new
// credential, while successful release/remove consumes it, so delayed work
// cannot act on a barrier another lifecycle replaced or completed.
type barrierCredential uint64

const (
	barrierAbsent barrierProof = iota
	barrierInstallAttempted
	barrierProven
	barrierReleaseAttempted
	barrierRemovalAttempted
)

type barrierOwnership struct {
	proof      barrierProof
	credential barrierCredential
	context    BarrierContext
}

var errRecoveryIncomplete = errors.New("guardian startup recovery incomplete")

// errMutationBusy marks the "another mutation holds the lock and ctx ran out
// while queueing" failure. It is a sentinel rather than a bare ctx error so
// LocalAPI can name it (guardian_busy) in the 500 response: that path never
// calls needsAttention, so without a name the response carries no code at
// all — and "Guardian is busy" is one of the two shapes a user hits while
// repeatedly retrying sudo bx up. The ctx error stays wrapped so existing
// errors.Is(err, context.DeadlineExceeded) callers keep working.
var errMutationBusy = errors.New("guardian is busy with another operation")

type DesiredStore interface {
	LoadDesired() (DesiredState, error)
	SaveDesired(DesiredState) error
	// LoadIntentSnapshot 一次读出 desired 与挂起。**加进接口而不是做成可选接口**:
	// 可选接口在没实现时会安静地退化成「没有挂起」,而那正是本期要消灭的谎言的
	// 形状;放进接口则由编译器点名每一个实现。
	LoadIntentSnapshot(time.Time) (IntentSnapshot, error)
	// ClearMaintenanceHold 由用户显式的 up/down 调用(设计取舍四)。
	ClearMaintenanceHold() error
}

type CoreRunner interface {
	Existing(context.Context) (Process, error)
	Watch(Process) Process
	Verify(Process) error
	Start(context.Context, CoreStartOptions) (Process, error)
	Stop(context.Context, Process) error
	Executable() string
	SetExecutable(string) error // 必须绝对路径,否则 error;并发安全
}

type CoreStartOptions struct {
	GuardianBypassHandoff []string
}

// coreScanner 由能向系统求证「有没有 Core 在跑」的 runner 实现。
//
// 刻意做成**可选**接口而不是塞进 CoreRunner:实现不了的 runner 落到
// 「没能确认」那一支即可,不该被迫编造一个答案。
type coreScanner interface {
	ScanRunning() ([]Process, error)
}

// observingCoreScanner 是同一次求证的「只观察」入口:判据一字不差,只是普查
// 日志的 reason 不同。可选 —— 实现不了的 runner 落回 coreScanner,答案完全一样,
// 唯一的差别是那行日志分不出是谁扫的。
type observingCoreScanner interface {
	ScanRunningObserved() ([]Process, error)
}

// coreScanSettle 是两次扫描之间的等待。刚收到 SIGTERM、正在跑 defer 的进程
// 既不是僵尸(scanRunningCores 已跳过僵尸)也还没消失,扫到它就断言「还在跑」
// 会把每一次正常关闭都变成告警 —— 而被训练成忽略告警的用户,真出事时也会忽略。
var coreScanSettle = 300 * time.Millisecond

// confirmCoreStopped 在拆除做完之后问系统:还有没有 Core 在跑?
//
// **永不返回 error。** 扫描的任何失败模式都不得让 Down 失败 —— 那是 2026-08-04
// 那条不变量(用户 71 分钟关不掉保护)。它只回答「能不能说 off」,以及为什么不能。
func (m *Manager) confirmCoreStopped() (stopped bool, reason string) {
	// **panic 也算一种失败模式,而这一条比别的更要命。**
	//
	// Down 经 LocalAPI 的 HTTP handler 进来,net/http 会按连接兜住 panic;
	// 但 Recover 跑在 trackStartupRecovery 的 goroutine 里,那里只有
	// `defer close(...)`,没有 recover —— 一次 panic 打死整个 Guardian 进程,
	// 而 launchd 的 KeepAlive 会把它拉起来,再跑 Recover,再 panic:崩溃循环。
	//
	// scanRunningCores 对 sysctl 返回的 kinfo 切片做索引运算,畸形/截断的应答
	// 并非不可能。把 panic 收成「没能确认」,方向与本函数其余部分一致。
	defer func() {
		if r := recover(); r != nil {
			log.Printf("guardian_core_scan_panicked recovered=%v", r)
			stopped, reason = false, "core_scan_failed"
		}
	}()
	scanner, ok := m.runner.(coreScanner)
	if !ok {
		return false, "core_scan_unsupported"
	}
	cores, err := scanner.ScanRunning()
	if err == nil && len(cores) == 0 {
		return true, ""
	}
	// 一次不算 —— 见 coreScanSettle。
	time.Sleep(coreScanSettle)
	cores, err = scanner.ScanRunning()
	switch {
	case err != nil:
		log.Printf("guardian_core_scan_failed_on_down err=%v", err)
		return false, "core_scan_failed"
	case len(cores) > 0:
		log.Printf("guardian_core_still_running_after_down pids=%s", scannedCorePIDs(cores))
		return false, "core_still_running"
	}
	return true, ""
}

type HealthGate interface {
	Wait(context.Context, HealthTarget) (supervisor.RuntimeState, error)
}

type GatewayProvider interface {
	DefaultGateway(context.Context) (string, error)
}

type GatewayProviderFunc func(context.Context) (string, error)

func (f GatewayProviderFunc) DefaultGateway(ctx context.Context) (string, error) {
	return f(ctx)
}

type LegacyCoreLifecycle interface {
	Present(context.Context) (bool, error)
	Stop(context.Context) error
	Remove() error
}

type ManagerOptions struct {
	Store            DesiredStore
	Runner           CoreRunner
	Health           HealthGate
	Barrier          Barrier
	DNS              DNSManager
	Legacy           LegacyCoreLifecycle
	BarrierContext   BarrierContext
	GatewayProvider  GatewayProvider
	CoreVersion      string
	RestartTimeout   time.Duration
	CleanupTimeout   time.Duration
	UpdatePreparer   UpdatePreparer
	GuardianProtocol int
	CorePath         CorePathClient
}

type Manager struct {
	mutation               chan struct{}
	updateOperation        chan struct{}
	statusMu               sync.RWMutex
	store                  DesiredStore
	runner                 CoreRunner
	health                 HealthGate
	barrier                Barrier
	dns                    DNSManager
	dnsStatus              DNSStatus
	legacy                 LegacyCoreLifecycle
	barrierContext         BarrierContext
	gatewayProvider        GatewayProvider
	coreVersion            string
	restartTimeout         time.Duration
	cleanupTimeout         time.Duration
	updates                updateStore
	updatePaths            Paths
	updatePreparer         UpdatePreparer
	guardianProtocol       int
	current                Process
	runtime                supervisor.RuntimeState
	status                 Status
	barrierOwnership       barrierOwnership
	barrierGeneration      barrierCredential
	recoveryBlocked        bool
	recoveryMu             sync.Mutex
	recoveryContext        context.Context
	cancelRecovery         context.CancelFunc
	recoveryAccepting      bool
	recoveryActive         int
	recoveryDrained        chan struct{}
	recoveryClosed         bool
	corePath               CorePathClient
	pathRecoveryMu         sync.Mutex
	pathRecoveryCurrent    RecoverySnapshot
	pathRecoveryPending    *pathRecoveryTransaction
	pathRecoveryCancel     context.CancelFunc
	pathRecoveryNewContext func() (context.Context, context.CancelFunc)
	pathRecoveryRetryWait  func(context.Context, time.Duration) error
	pathRecoverySequence   uint64
	networkGeneration      string
	pathRecoveryAccepting  bool
	pathRecoveryActive     bool
	pathRecoveryFences     int
	pathRecoveryResolveOff bool
	pathRecoveryDrained    chan struct{}
	pathRecoveryClosed     bool
	// reconcileReport 是只观察调谐环最近一轮的判断,由 statusMu 保护。
	//
	// **刻意住在 m.status 外面。** 控制面里有一大批路径是拿一个从零构造的
	// Status 字面量整体替换 m.status 的(upLocked/downLocked/recoverLocked…),
	// 报告只要并进那个结构体,迟早会被其中一条顺手抹掉 —— 而它被抹掉之后
	// 长的样子恰好是「一轮都没跑过」,也就是本任务要消灭的那句谎话。
	//
	// 用 statusMu 而不是 mutation channel:观测路径不许持有那把锁(一整轮观测
	// 在 darwin 上是 6 次 fork 加约 900 次 syscall,攥着它做等于让用户的 up
	// 排在后面)。
	reconcileReport *ReconcileReport
	// lastErrorGeneration backs Status.LastErrorGeneration (see
	// nextLastErrorGeneration/needsAttention). atomic rather than guarded by
	// statusMu because needsAttention can race with concurrent status reads
	// from other goroutines (e.g. the unexpected-exit monitor).
	lastErrorGeneration atomic.Uint64
}

func NewManager(options ManagerOptions) (*Manager, error) {
	switch {
	case options.Store == nil:
		return nil, errors.New("guardian desired store required")
	case options.Runner == nil:
		return nil, errors.New("guardian Core runner required")
	case options.Health == nil:
		return nil, errors.New("guardian health gate required")
	case options.Barrier == nil:
		return nil, errors.New("guardian barrier required")
	case options.DNS == nil:
		return nil, errors.New("guardian DNS manager required")
	case options.CoreVersion == "":
		return nil, errors.New("guardian Core version required")
	}
	restartTimeout := options.RestartTimeout
	if restartTimeout <= 0 {
		restartTimeout = defaultHealthTimeout + 5*time.Second
	}
	cleanupTimeout := options.CleanupTimeout
	if cleanupTimeout <= 0 {
		cleanupTimeout = 15 * time.Second
	}
	updates, _ := options.Store.(updateStore)
	pathsProvider, _ := options.Store.(guardianPathsProvider)
	var updatePaths Paths
	if pathsProvider != nil {
		updatePaths = pathsProvider.guardianPaths()
	}
	updatePreparer := options.UpdatePreparer
	if updatePreparer == nil {
		updatePreparer = macOSUpdatePreparer{}
	}
	guardianProtocol := options.GuardianProtocol
	if guardianProtocol <= 0 {
		guardianProtocol = currentGuardianProtocol
	}
	corePath := options.CorePath
	if corePath == nil {
		corePath, _ = options.Runner.(CorePathClient)
	}
	gatewayProvider := options.GatewayProvider
	if gatewayProvider == nil {
		gateway := options.BarrierContext.Gateway
		gatewayProvider = GatewayProviderFunc(func(context.Context) (string, error) {
			if gateway == "" {
				return "", errors.New("default gateway unavailable")
			}
			return gateway, nil
		})
	}
	recoveryContext, cancelRecovery := context.WithCancel(context.Background())
	m := &Manager{
		mutation:              make(chan struct{}, 1),
		updateOperation:       make(chan struct{}, 1),
		store:                 options.Store,
		runner:                options.Runner,
		health:                options.Health,
		barrier:               options.Barrier,
		dns:                   options.DNS,
		dnsStatus:             DNSStatus{State: DNSUnknown},
		legacy:                options.Legacy,
		barrierContext:        cloneBarrierContext(options.BarrierContext),
		gatewayProvider:       gatewayProvider,
		coreVersion:           options.CoreVersion,
		restartTimeout:        restartTimeout,
		cleanupTimeout:        cleanupTimeout,
		updates:               updates,
		updatePaths:           updatePaths,
		updatePreparer:        updatePreparer,
		guardianProtocol:      guardianProtocol,
		recoveryContext:       recoveryContext,
		cancelRecovery:        cancelRecovery,
		recoveryAccepting:     true,
		recoveryDrained:       make(chan struct{}),
		corePath:              corePath,
		pathRecoveryCurrent:   RecoverySnapshot{State: "idle", Stage: "idle"},
		pathRecoveryAccepting: true,
		pathRecoveryDrained:   make(chan struct{}),
		status: Status{
			SchemaVersion: 1,
			Desired:       DesiredOff,
			Phase:         PhaseIdle,
			Protection:    ProtectionOff,
			DNSState:      DNSUnknown,
		},
	}
	m.pathRecoveryNewContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), guardianMutationTimeout)
	}
	m.pathRecoveryRetryWait = waitForPathRecoveryRetry
	m.mutation <- struct{}{}
	m.updateOperation <- struct{}{}
	return m, nil
}

func (m *Manager) Status() Status {
	m.statusMu.RLock()
	defer m.statusMu.RUnlock()
	status := m.status
	// 报告在组装时才附上,不住在 m.status 里(见 Manager.reconcileReport)。
	status.Reconcile = m.reconcileReport.clone()
	return status
}

// recordReconcileRound 记下调谐环刚跑完的这一轮。
//
// **每一轮都要记,包括被栅栏挡住的、以及什么差异都没有的那些。** soak 要数的
// 正是「有多少轮根本没在工作」,而一份只在有差异时才更新的报告在健康机器上
// 恒为 nil —— 与「循环压根没跑」完全无法区分。
//
// 时间戳在这里盖,而且没有任何一条分支能绕过它:它是「跑过没跑过」唯一的判据。
func (m *Manager) recordReconcileRound(round reconcileRound) {
	report := &ReconcileReport{
		At:              time.Now(),
		Held:            round.decision.Held,
		UnchangedRounds: round.unchanged,
		Unobservable:    append([]string(nil), round.unobservable...),
		CoreScan:        round.scan,
	}
	for _, action := range round.decision.Actions {
		report.Actions = append(report.Actions, string(action))
	}
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	m.reconcileReport = report
}

// measureRunningCores 向系统求证「有多少个进程看起来像 Core」,**只测量,不判断**。
//
// 设计交付的第二样是 looksLikeCore 的真机误报率,而它今天只挂在三条改动路径上
// (Existing / Start / confirmCoreStopped),只观察的循环再跑多久也攒不出证据。
// 这里把同一个原语接进循环,但答案只进报告,不进 decide 的输入。
//
// 三条纪律,与 confirmCoreStopped 同源:
//   - **不持 mutation channel。** m.runner 是构造后不再变的字段,取它不需要任何锁;
//     扫描本身约 900 次 syscall,攥着那把锁去做等于让用户的 up 排在后面。
//   - **「问不出来」不是「一个都没有」。** 失败一律 Measured=false,绝不记 0 ——
//     记 0 会让误报率算出来偏低,正好是这份测量的反面。
//   - **panic 收在这里。** scanRunningCores 对 sysctl 返回的 kinfo 切片做索引运算;
//     一次 panic 若逃出去,循环这一轮就白跑,而循环是常驻的,撞上罕见输入的机会
//     随时间累积。
func (m *Manager) measureRunningCores() (measurement ReconcileCoreScan) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("guardian_reconcile_core_scan_panicked recovered=%v", r)
			measurement = ReconcileCoreScan{Reason: coreScanPanicked}
		}
	}()
	scan := m.observingCoreScan()
	if scan == nil {
		return ReconcileCoreScan{Reason: coreScanUnsupported}
	}
	cores, err := scan()
	if err != nil {
		return ReconcileCoreScan{Reason: coreScanFailed}
	}
	return ReconcileCoreScan{Measured: true, Cores: len(cores)}
}

// observingCoreScan 优先取带「只观察」标签的那条入口,取不到就退回通用的。
// 退回不改变答案,只让那行普查日志分不出是谁扫的 —— 宁可少一个标签,也不能
// 因为 runner 没实现新接口就把「没测」当成「测到 0 个」。
func (m *Manager) observingCoreScan() func() ([]Process, error) {
	if observing, ok := m.runner.(observingCoreScanner); ok {
		return observing.ScanRunningObserved
	}
	if scanner, ok := m.runner.(coreScanner); ok {
		return scanner.ScanRunning
	}
	return nil
}

func (m *Manager) Up(ctx context.Context) error {
	if err := m.acquireMutation(ctx); err != nil {
		return err
	}
	defer m.releaseMutation()
	if m.recoveryBlocked {
		return errRecoveryIncomplete
	}
	// 用户显式的动作永远压过挂起(设计取舍四)。放在这里而不是调用方:菜单的
	// Turn On 走 socket,一行 CLI 都不经过。
	//
	// **放在 Up 而不是 upLocked**,理由有两条,方向相反而都必须成立:
	//   - upLocked 有一条「已经 Protected 就直接返回」的早出口,而那同样是一次
	//     用户显式的 on;挂在写 desired 那一段之后就漏了它,留下「保护开着而挂起
	//     还武装着」——此后 Core 一旦退出,handleUnexpectedExit 会 fail-closed
	//     不把它拉回来,保护在用户以为开着的时候悄悄消失。
	//   - upLocked 还被**启动恢复**复用(recoverLocked → upLocked),而每一次升级
	//     都会重启 Guardian、跑一遍启动恢复。销挂起若住在 upLocked 里,挂起就会
	//     在它最该生效的那一刻被自己人销掉。
	m.clearMaintenanceHold()
	return m.upLocked(ctx)
}

func (m *Manager) Migrate(ctx context.Context, request MigrationRequest) error {
	normalized, err := ValidateMigrationRequest(request)
	if err != nil {
		return err
	}
	m.beginPathRecoveryTransition(pathRecoveryTransitionPreserveGenerated)
	defer m.endPathRecoveryTransition()
	if m.legacy == nil {
		return errors.New("legacy Core lifecycle unavailable")
	}
	if err := m.acquireMutation(ctx); err != nil {
		return err
	}
	defer m.releaseMutation()
	if m.recoveryBlocked {
		return errRecoveryIncomplete
	}
	// 与 Up 同一条规则(设计取舍四):Migrate 是 `bx up` 在一台还带 legacy Core
	// 的机器上走的那条路,同样是用户显式说的 on。放在所有早出口之前,理由与
	// Up 那边一字不差。
	m.clearMaintenanceHold()

	if m.current.Uncertain {
		m.needsAttention(DesiredOn, "core_ownership_uncertain")
		return uncertainOwnership(m.current, nil)
	}
	if m.current.PID != 0 && m.Status().Protection == ProtectionProtected {
		if err := m.legacy.Remove(); err != nil {
			m.needsAttention(DesiredOn, "legacy_unit_remove_failed")
			return fmt.Errorf("remove migrated Core unit: %w", err)
		}
		return nil
	}

	desired, err := m.store.LoadDesired()
	if err != nil {
		m.needsAttention(DesiredOn, "desired_state_read_failed")
		return err
	}
	if desired != DesiredOn {
		if err := m.store.SaveDesired(DesiredOn); err != nil {
			m.needsAttention(desired, "desired_state_write_failed")
			return err
		}
	}
	barrierContext := migrationBarrierContext(normalized)
	m.barrierContext = cloneBarrierContext(barrierContext)
	if err := m.installBarrier(ctx, barrierContext); err != nil {
		m.needsAttention(DesiredOn, "barrier_install_failed")
		return fmt.Errorf("install migration barrier: %w", err)
	}
	m.setStatus(Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseBarrierActive, Protection: ProtectionBlocked})

	if err := m.legacy.Stop(ctx); err != nil {
		m.needsAttention(DesiredOn, "legacy_core_stop_failed")
		return fmt.Errorf("stop legacy Core behind barrier: %w", err)
	}
	if err := m.barrier.ReassertBypass(ctx, barrierContext); err != nil {
		m.needsAttention(DesiredOn, "barrier_reassert_failed")
		return fmt.Errorf("reassert migration bypass: %w", err)
	}
	state, err := m.startCoreLockedWithBarrierRelease(ctx, false)
	if err != nil {
		return err
	}
	if err := m.releaseBarrierToCore(ctx); err != nil {
		m.needsAttention(DesiredOn, "barrier_remove_failed")
		return err
	}
	if err := m.legacy.Remove(); err != nil {
		// Reinstalling the barrier here is a real install (see comment on
		// removeBarrier/releaseBarrierToCore) and does need a gateway;
		// degrade to block-only rather than leaving Core to run with no
		// barrier at all if the gateway can't be resolved right now.
		reinstallContext, gatewayErr := m.barrierContextForRuntime(ctx, state)
		if gatewayErr != nil {
			reinstallContext = blockOnlyRecoveryContext(m.contextForRuntime(state))
		}
		reinstallErr := m.retainMigrationBarrier(ctx, reinstallContext)
		m.needsAttention(DesiredOn, "legacy_unit_remove_failed")
		return errors.Join(fmt.Errorf("remove migrated Core unit: %w", err), reinstallErr)
	}
	if err := m.setProtectedStatus(PhaseCommitted, m.current.PID, state.Version, ""); err != nil {
		return m.failDNSActivation(ctx, state, "dns_verification_failed", err)
	}
	return nil
}

func (m *Manager) retainMigrationBarrier(ctx context.Context, barrierContext BarrierContext) error {
	reinstallCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.cleanupTimeout)
	defer cancel()
	err := m.installBarrier(reinstallCtx, barrierContext)
	if err != nil {
		return fmt.Errorf("restore migration barrier after legacy cleanup failure: %w", err)
	}
	return nil
}

func (m *Manager) upLocked(ctx context.Context) error {
	if m.current.Uncertain {
		m.needsAttention(DesiredOn, "core_ownership_uncertain")
		return uncertainOwnership(m.current, nil)
	}
	if m.current.PID != 0 && m.Status().Protection == ProtectionProtected {
		return nil
	}
	if err := m.requireLegacyReleased(ctx); err != nil {
		return err
	}
	desired, err := m.store.LoadDesired()
	if err != nil {
		m.needsAttention(DesiredOn, "desired_state_read_failed")
		return err
	}
	if desired != DesiredOn {
		if err := m.store.SaveDesired(DesiredOn); err != nil {
			m.needsAttention(desired, "desired_state_write_failed")
			return err
		}
	}
	m.setStatus(Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseActivating, Protection: ProtectionStarting})

	existing, err := m.runner.Existing(ctx)
	if err != nil {
		if errors.Is(err, ErrProcessOwnershipUncertain) {
			if process, ok := uncertainProcess(err); ok {
				m.retainUncertain(process)
			}
			m.needsAttention(DesiredOn, "core_ownership_uncertain")
			return err
		}
		m.needsAttention(DesiredOn, "core_state_read_failed")
		return fmt.Errorf("inspect existing Core: %w", err)
	}
	if existing.PID != 0 {
		if err := m.runner.Verify(existing); err != nil {
			m.needsAttention(DesiredOn, "core_identity_unverified")
			return fmt.Errorf("verify existing Core: %w", err)
		}
		state, err := m.waitHealthy(ctx, existing)
		if err != nil {
			m.needsAttention(DesiredOn, "core_adoption_health_failed")
			return fmt.Errorf("adopt existing Core: %w", err)
		}
		if err := m.acceptHealthy(ctx, existing, state, true); err != nil {
			return fmt.Errorf("release held barrier after adopting Core: %w", err)
		}
		return nil
	}

	_, err = m.startCoreLocked(ctx)
	return err
}

// upgradeIntentStore 是可选能力:实现了它的 store 才有升级欠条可销。
// 用可选接口而不是塞进 DesiredStore,是为了不逼所有既有实现(含测试替身)
// 都长出一个它们并不需要的方法。
type upgradeIntentStore interface {
	ClearUpgradeIntent() error
}

func (m *Manager) forgetUpgradeIntent() {
	store, ok := m.store.(upgradeIntentStore)
	if !ok {
		return
	}
	if err := store.ClearUpgradeIntent(); err != nil {
		log.Printf("guardian_upgrade_intent_clear_failed err=%v", err)
	}
}

// clearMaintenanceHold 撤销挂起。**只记日志,绝不让调用方失败**:它跑在
// up/down 这两条路上,而停止不许依赖先成功做成别的事。
//
// 调用方是用户显式的三条路:Up、Migrate(`bx up` 在带 legacy Core 的机器上走的
// 那条)、以及 Down 的 defer(维护自己的那次 Down 除外)。启动恢复**不在其列**
// —— 它与 Up 共用 upLocked,而每次升级都会重启 Guardian 跑一遍启动恢复。
func (m *Manager) clearMaintenanceHold() {
	if err := m.store.ClearMaintenanceHold(); err != nil {
		log.Printf("guardian_maintenance_hold_clear_failed err=%v", err)
	}
}

func (m *Manager) Down(ctx context.Context) (err error) {
	m.beginPathRecoveryTransition(pathRecoveryTransitionResolveOff)
	defer m.endPathRecoveryTransition()
	if err = m.acquireMutation(ctx); err != nil {
		return err
	}
	defer m.releaseMutation()
	// 一次 map 读,越早越好:关闭是逃生路径,不得因为任何记账逻辑失败或阻塞。
	//
	// **升级自己的那次停保护**(POST /v1/down?reason=upgrade)与「用户不要保护了」
	// 是两件事:app-install 前一秒才武装了一张维护挂起,紧接着就来停保护。不加
	// 区分的话,挂起在武装的下一步就被自己那次 Down 销掉,而 desired 又被写成
	// off —— 于是磁盘上写着「用户不想要保护」,一个忠实的调谐器会照办。
	maintenance := downIsMaintenance(ctx)
	// 用户明确要关 ⇒ 挂起必须消失,**且与这次 Down 的成败无关**。
	//
	// 钩子挂在这里而不是某个调用方,是因为**每一条**「用户说 off」都汇合于此:
	// 菜单的 Turn Off 走 socket → POST /v1/down → controller.Down;CLI 的干净
	// 路径走 client.Down 到同一个 handler。挂在 macOSDownAction 上只覆盖后者,
	// 菜单那条根本不经过 CLI —— 那正是上一轮漏掉的路径。
	//
	// **无条件**这一点是从欠条那个活 bug 学来的:forcedMacOSTeardown 即使报告
	// 失败,六步破坏性动作也已经做完了,而躲在 `err == nil` 后面的销账于是留下
	// 一条陈旧记录,下一次 app-install(菜单的 Repair 带 --yes)拿它**违背用户
	// 明确的关闭请求**把保护打开。销不掉只记日志(clearMaintenanceHold 是
	// best-effort),绝不让「关闭」因为一个记账文件而失败。
	defer func() {
		if maintenance {
			return
		}
		m.clearMaintenanceHold()
		// 欠条那半边照旧只在成功后销(行为不变),Task 6 会连同它一起删掉。
		if err == nil {
			m.forgetUpgradeIntent()
		}
	}()
	if m.recoveryBlocked {
		return errRecoveryIncomplete
	}

	desired, err := m.store.LoadDesired()
	if err != nil {
		m.needsAttention(DesiredOn, "desired_state_read_failed")
		return err
	}
	if desired == DesiredOff && m.current.PID == 0 {
		if restoreErr := m.restoreDNS(ctx); restoreErr != nil {
			m.needsAttention(DesiredOff, "dns_restore_failed")
			return fmt.Errorf("restore stale managed DNS: %w", restoreErr)
		}
		// 这条提前返回的两个前提(盘上的 desired、内存里的 m.current)**都是记账**,
		// 而记账里没有 legacy 单元起的 Core、没有 sudo bx run 起的 Core,手删过
		// core-process.json 的机器上更是什么都没有 —— 那些正是「记账说没有、系统里
		// 却有」的四种情形,一句「已关闭」在那时就是假话。
		return m.reportStopped("early")
	}

	process := m.current
	runtimeState := m.runtime
	if process.PID == 0 {
		process, err = m.runner.Existing(ctx)
		if err != nil {
			m.needsAttention(desired, "core_state_read_failed")
			return fmt.Errorf("inspect existing Core: %w", err)
		}
		if process.PID != 0 {
			if err := m.runner.Verify(process); err != nil {
				m.needsAttention(desired, "core_identity_unverified")
				return fmt.Errorf("verify existing Core: %w", err)
			}
			runtimeState, err = m.waitHealthy(ctx, process)
			if err != nil {
				m.needsAttention(desired, "core_handoff_failed")
				return fmt.Errorf("obtain Core handoff: %w", err)
			}
		}
	} else if state, healthErr := m.waitHealthy(ctx, process); healthErr == nil {
		runtimeState = state
	}

	// 屏障上下文有两种不可用法,都发生在用户最需要关闭保护的那一刻:
	//   ① 网络故障时解析不到默认网关 —— barrierContextForRuntime 直接报错;
	//   ② Core 从没成功起来过,runtime 里没有 server bypass —— 此时
	//      barrierContextForRuntime **成功**返回,只是产出的 ServerBypass 为空,
	//      要到 installBarrier 里才被 validateBarrierContext 以
	//      "server bypass required" 拒掉。
	// 只挡住 ① 是不够的:真机 2026-08-06 走的正是 ② —— VPS 不通、Core 健康检查
	// 20s 超时、控制 socket 从未出现,于是 bx down 报 500 barrier_install_failed,
	// 用户看到一屏红字后只能 uninstall。关闭路径不得依赖「Core 必须先成功起来过」。
	// 两种情况都降级为 block-only(去掉 bypass、强制阻断 IPv6)继续拆除——那比
	// 带 bypass 的屏障更收紧,不会放松 fail-closed。
	barrierContext, err := m.barrierContextForRuntime(ctx, runtimeState)
	if err != nil || len(barrierContext.ServerBypass) == 0 {
		barrierContext = blockOnlyRecoveryContext(m.contextForRuntime(runtimeState))
	}
	if err := m.installBarrier(ctx, barrierContext); err != nil {
		m.needsAttention(desired, "barrier_install_failed")
		return fmt.Errorf("install down barrier: %w", err)
	}
	m.setStatus(Status{SchemaVersion: 1, Desired: desired, Phase: PhaseBarrierActive, CorePID: process.PID, CoreVersion: runtimeState.Version, Protection: ProtectionBlocked})

	if process.PID != 0 {
		if err := m.runner.Stop(ctx, process); err != nil {
			m.needsAttention(desired, "core_stop_failed")
			return fmt.Errorf("stop Core behind barrier: %w", err)
		}
	}
	m.current = Process{}
	m.runtime = supervisor.RuntimeState{}

	restoreCtx, cancelRestore := m.downRestoreContext(ctx)
	restoreErr := m.restoreDNS(restoreCtx)
	cancelRestore()
	if restoreErr != nil {
		restoreErr := fmt.Errorf("restore managed DNS: %w", restoreErr)
		recoveryCtx, cancelRecovery := context.WithTimeout(ctx, m.restartTimeout)
		defer cancelRecovery()
		// **维护挂起期间绝不把 Core 放回去。**
		//
		// 这条补偿的原意是「DNS 还原失败了,别把用户停在一个奇怪的中间态」,
		// 但在维护窗口里「把 Core 放回去」放回去的是一个正换到一半的二进制。
		// 它是 Guardian 自发起 Core 的**第四条**路,与 handleUnexpectedExit、
		// recoverLocked、以及活着的 Guardian 抢在强制拆除前面那条同类 ——
		// 漏掉它就是漏掉四分之一。
		//
		// 方向上安全:这是**少做一个动作**,不可能让「停止」依赖别的先成功。
		// 而屏障照拆 —— 那是覆盖整个公网的 8 条 /2 reject 路由,留下就是整机
		// 断网,且拆它从不依赖 Core 在跑(正常的 Down 成功路径就是这么做的)。
		if !maintenance {
			if _, recoveryErr := m.startCoreLocked(recoveryCtx); recoveryErr != nil {
				m.needsAttentionUnlessDNSActivationFailure("down_restore_recovery_failed")
				return errors.Join(restoreErr, recoveryErr)
			}
		}
		if err := m.removeBarrier(recoveryCtx); err != nil {
			m.needsAttention(DesiredOn, "barrier_remove_failed")
			return errors.Join(restoreErr, err)
		}
		return restoreErr
	}

	// 维护停机**不改写 desired**:用户的意图没有变,变的只是「此刻不能有」。
	// 那件事由维护挂起表达(CLI 在停机之前已经武装好了)。写下 off 就是在磁盘上
	// 留一句假话,而任何忠实的调谐器都会照它收敛。
	if !maintenance {
		if err := m.store.SaveDesired(DesiredOff); err != nil {
			m.needsAttention(desired, "desired_state_write_failed")
			return fmt.Errorf("persist disabled state behind barrier: %w", err)
		}
	}
	if err := m.removeBarrier(ctx); err != nil {
		m.needsAttention(DesiredOff, "barrier_remove_failed")
		return err
	}
	// 拆除到这里已经全做完了(屏障拆了、DNS 还了、desired 落盘为 off),剩下的
	// 只是「能不能说 off」。Stop 成功不等于系统里没有 Core —— 我们只停得掉自己
	// 记账里那一个。
	return m.reportStopped("main")
}

// reportStopped 是 Down 唯一说得出「off」的地方:先向系统求证,证不了就如实说
// 没能确认。
//
// **永不返回 error。** 拆除在调用它之前就已经做完,「没能确认」是一种状态而不是
// 一次失败 —— 让它变成失败就等于让「停止」依赖别的事先成功,那正是 2026-08-04
// 用户 71 分钟关不掉保护的形状。
//
// hop 只进日志,用来区分是提前返回那条路还是完整拆除那条路;真正指认是哪个进程的
// 是 confirmCoreStopped 打的 guardian_core_still_running_after_down pids=…
// —— 判据 looksLikeCore 认的是**任何** root 身份的 `bx run`(包括开发者自己在另一个
// 终端里调试的那个),不把 PID 落进日志的话,用户只会看到一句没头没尾的「需注意」。
func (m *Manager) reportStopped(hop string) error {
	stopped, reason := m.confirmCoreStopped()
	if !stopped {
		log.Printf("guardian_down_unconfirmed hop=%s reason=%s", hop, reason)
		m.needsAttention(DesiredOff, reason)
		return nil
	}
	m.setStatus(Status{SchemaVersion: 1, Desired: DesiredOff, Phase: PhaseIdle, Protection: ProtectionOff})
	return nil
}

// BeginStartupRecovery synchronously raises the same fences Recover raises at
// its own start (recoveryBlocked and the path-recovery transition fence)
// before the Guardian LocalAPI listener begins accepting connections. It
// closes the fail-closed window opened by starting the socket ahead of the
// startup Recover call: without it, a concurrent Up/Down/Migrate/Update could
// win Manager's single-slot mutation channel ahead of the background Recover
// goroutine and run before recoverUpdateLocked has even inspected the update
// journal, skipping the crash-mid-update recovery barrier that machinery
// exists for. Because recoveryBlocked is already true the instant the
// listener opens, that race no longer matters: whichever side wins the
// mutation channel, a premature mutation still observes recoveryBlocked and
// fails closed with errRecoveryIncomplete.
//
// Must be paired with exactly one subsequent RunStartupRecovery call (never a
// direct Recover call) so the path-recovery fence counter stays balanced —
// see beginPathRecoveryTransition/endPathRecoveryTransition. If the daemon
// fails to start before RunStartupRecovery can run, call
// AbortStartupRecovery instead to release the fence without performing any
// recovery work.
func (m *Manager) BeginStartupRecovery() {
	m.recoveryBlocked = true
	m.beginPathRecoveryTransition(pathRecoveryTransitionPreserveGenerated)
}

// RunStartupRecovery performs the deferred body of the startup Recover call
// after BeginStartupRecovery has already raised the path-recovery fence
// synchronously. It releases that same fence (rather than raising a second
// one), keeping the begin/end pair balanced instead of leaking a permanently
// raised fence.
func (m *Manager) RunStartupRecovery(ctx context.Context) error {
	defer m.endPathRecoveryTransition()
	return m.recoverLocked(ctx)
}

// AbortStartupRecovery releases the fence BeginStartupRecovery raised without
// performing any recovery work. It exists solely for the daemon-failed-to-
// start path, where start() errors before RunStartupRecovery can ever run —
// without this, the path-recovery fence would leak, permanently queuing
// recoveries. recoveryBlocked is deliberately left set: no Recover actually
// ran, so nothing downstream should assume the crash journal was inspected.
func (m *Manager) AbortStartupRecovery() {
	m.endPathRecoveryTransition()
}

// Recover restores the persisted desired state without treating daemon shutdown
// as an instruction to stop Core or restore direct networking.
func (m *Manager) Recover(ctx context.Context) error {
	m.beginPathRecoveryTransition(pathRecoveryTransitionPreserveGenerated)
	defer m.endPathRecoveryTransition()
	return m.recoverLocked(ctx)
}

func (m *Manager) recoverLocked(ctx context.Context) error {
	if err := m.acquireMutation(ctx); err != nil {
		return err
	}
	defer m.releaseMutation()
	if err := m.recoverUpdateLocked(ctx); err != nil {
		m.recoveryBlocked = true
		return err
	}
	intent, err := m.store.LoadIntentSnapshot(time.Now())
	if err != nil {
		m.needsAttention(DesiredOn, intentReadFailureCode(err))
		// **只有 desired 那一半读不出来才堵死后续 mutation。** 挂起读不出来时
		// 一样 fail-closed(下面一句 return,不起 Core),但 recoveryBlocked 保持
		// false:一张坏挂起(坏 schema、坏 reason、时钟跳变造出的远期过期)在
		// LoadMaintenanceHold 眼里就是一次读失败,而清掉它的唯一办法是一条显式的
		// up/down —— recoveryBlocked 为真会让 Up 与 Down 双双第一句就返回
		// errRecoveryIncomplete,把那条恢复路径自己掐断(2026-08-04 那次 71 分钟
		// 事故的机制)。
		m.recoveryBlocked = !errors.Is(err, ErrMaintenanceHoldUnreadable)
		return err
	}
	desired := intent.Desired
	// 维护挂起:此刻不该有保护,而这不是一次失败的恢复。
	//
	// **每一次升级都会走到这里** —— restartGuardianForUpgrade 把 daemon 停掉再拉
	// 起来,新 Guardian 起手就跑启动恢复;desired 保持 on 之后,不认挂起就等于在
	// 二进制刚换完、CLI 还没走到「恢复保护」那一步时自己先把 Core 起来了。
	//
	// **注意这不是本函数里第一个可能起 Core 的地方**:上面的 recoverUpdateLocked
	// (`:819`)跑在这道检查**之前**,崩在升级中途的机器会在那里回滚并起一个 Core。
	// 那是**有意不拦**的 —— 它先把快照里的二进制还原回去再启动,起来的不是半换的
	// 版本;拦住它反而会把一次做了一半的 Guardian 自更新永久卡住,比它可能造成的
	// 麻烦更糟。见 TestStartupRecoveryStillFinishesAnInFlightUpdateUnderArmedHold,
	// 别把这条例外「修」成一道栅栏。
	//
	// recoveryBlocked **必须置 false**:它为真时 Manager.Down 第一句就返回
	// errRecoveryIncomplete —— 2026-08-04 那次 71 分钟事故的机制。一次挂起绝不
	// 允许把关闭的路堵死。
	if intent.HoldArmed {
		log.Printf("guardian_startup_recovery_held reason=%s expires_at=%s",
			intent.Hold.Reason, intent.Hold.ExpiresAt.Format(time.RFC3339))
		// 与隔壁 desired==off 那一支同一条纪律:**发 off 之前先问系统**。账本说
		// 「此刻不该有保护」证明不了「现在没有 Core 在跑」—— legacy Core、另一个
		// 终端里的 sudo bx run、以及记录丢失的机器,账本里都看不见。
		//
		// 今天升级停机走的是隔壁那支(它已经求证);Task 4 把它搬到这一支上来,
		// 那条求证不能在搬家途中掉在半路 —— 它当初正是为这条路写的。
		stopped, reason := m.confirmCoreStopped()
		// 求证的结果只决定「能不能说 off」,不决定「要不要堵死关闭的路」:
		// 两种情形都必须留 false(见上)。
		m.recoveryBlocked = false
		if !stopped {
			m.needsAttention(desired, reason)
			return nil
		}
		m.setStatus(Status{SchemaVersion: 1, Desired: desired, Phase: PhaseIdle, Protection: ProtectionOff})
		return nil
	}
	if desired == DesiredOff {
		if restoreErr := m.restoreDNS(ctx); restoreErr != nil {
			m.needsAttention(DesiredOff, "dns_restore_failed")
			m.recoveryBlocked = true
			return fmt.Errorf("restore stale managed DNS during startup recovery: %w", restoreErr)
		}
		// 与 Down 同一条纪律:报 off 之前先问系统。账本说「用户要 off」证明不了
		// 「现在没有 Core 在跑」—— legacy Core、另一个终端里的 sudo bx run、
		// 以及记录丢失的机器,账本里都看不见。
		//
		// **而且这一跳比 Down 那一跳更要紧**:Guardian 每次重启都会走它,于是它会
		// 把上一次 Down 好不容易求证出来的判断**抹掉**,换成一句没求证过的 off。
		stopped, reason := m.confirmCoreStopped()
		if !stopped {
			m.needsAttention(DesiredOff, reason)
			// **recoveryBlocked 必须置 false。** 它为真时 Down 的第一句就返回
			// errRecoveryIncomplete —— 而那正是 2026-08-04 那次「用户 71 分钟
			// 关不掉保护」的机制。「没能确认关掉了」绝不能顺带把关闭的路堵死。
			m.recoveryBlocked = false
			return nil
		}
		m.setStatus(Status{SchemaVersion: 1, Desired: DesiredOff, Phase: PhaseIdle, Protection: ProtectionOff})
		m.recoveryBlocked = false
		return nil
	}
	if err := m.upLocked(ctx); err != nil {
		m.recoveryBlocked = true
		return err
	}
	m.recoveryBlocked = false
	return nil
}

func (m *Manager) startCoreLocked(ctx context.Context) (supervisor.RuntimeState, error) {
	return m.startCoreLockedWithBarrierRelease(ctx, true)
}

func (m *Manager) startCoreLockedWithBarrierRelease(ctx context.Context, releaseBarrier bool) (supervisor.RuntimeState, error) {
	operationCtx, cancelOperation, err := m.reserveCleanup(ctx)
	if err != nil {
		m.needsAttention(DesiredOn, "core_start_failed")
		return supervisor.RuntimeState{}, err
	}
	defer cancelOperation()
	process, err := m.runner.Start(operationCtx, m.coreStartOptions())
	if err != nil {
		if errors.Is(err, ErrProcessOwnershipUncertain) {
			if uncertain, ok := uncertainProcess(err); ok {
				m.retainUncertain(uncertain)
			}
			m.needsAttention(DesiredOn, "core_ownership_uncertain")
			return supervisor.RuntimeState{}, fmt.Errorf("start Core: %w", err)
		}
		m.needsAttention(DesiredOn, "core_start_failed")
		return supervisor.RuntimeState{}, fmt.Errorf("start Core: %w", err)
	}
	if err := m.runner.Verify(process); err != nil {
		if cleanupErr := m.cleanupStartedCore(ctx, process); cleanupErr != nil {
			m.retainUncertain(Process{PID: process.PID, Executable: process.Executable, UID: process.UID, Generation: process.Generation, Exit: process.Exit, Uncertain: true})
			m.needsAttention(DesiredOn, "core_ownership_uncertain")
			return supervisor.RuntimeState{}, errors.Join(fmt.Errorf("verify started Core: %w", err), uncertainOwnership(m.current, cleanupErr))
		}
		m.needsAttention(DesiredOn, "core_identity_unverified")
		return supervisor.RuntimeState{}, fmt.Errorf("verify started Core: %w", err)
	}
	state, err := m.waitHealthy(operationCtx, process)
	if err != nil {
		if cleanupErr := m.cleanupStartedCore(ctx, process); cleanupErr != nil {
			m.retainUncertain(Process{PID: process.PID, Executable: process.Executable, UID: process.UID, Generation: process.Generation, Exit: process.Exit, Uncertain: true})
			m.needsAttention(DesiredOn, "core_ownership_uncertain")
			return supervisor.RuntimeState{}, errors.Join(fmt.Errorf("wait for Core health: %w", err), uncertainOwnership(m.current, cleanupErr))
		}
		m.needsAttention(DesiredOn, "core_health_failed")
		return supervisor.RuntimeState{}, fmt.Errorf("wait for Core health: %w", err)
	}
	if err := m.acceptHealthy(ctx, process, state, releaseBarrier); err != nil {
		return state, fmt.Errorf("complete healthy Core activation: %w", err)
	}
	return state, nil
}

func (m *Manager) waitHealthy(ctx context.Context, process Process) (supervisor.RuntimeState, error) {
	state, err := m.health.Wait(ctx, HealthTarget{Version: m.coreVersion, PID: process.PID})
	if err != nil {
		return state, err
	}
	if state.PID != process.PID {
		return state, fmt.Errorf("runtime PID %d does not match process PID %d", state.PID, process.PID)
	}
	if state.Version != m.coreVersion {
		return state, fmt.Errorf("runtime version %q does not match expected %q", state.Version, m.coreVersion)
	}
	return state, nil
}

func (m *Manager) acceptHealthy(ctx context.Context, process Process, state supervisor.RuntimeState, releaseBarrier bool) error {
	if sameProcessGeneration(m.current, process) && m.current.Exit != nil {
		process.Exit = m.current.Exit
	} else {
		process = m.runner.Watch(process)
		if process.Exit != nil {
			go m.monitor(process)
		}
	}
	m.current = process
	m.runtime = state
	if err := m.ensureDNSManaged(ctx, state); err != nil {
		return err
	}
	if m.barrierOwnership.proof != barrierAbsent {
		if m.barrierOwnership.proof == barrierReleaseAttempted && releaseBarrier {
			if err := m.releaseBarrierToCore(ctx); err != nil {
				m.needsAttention(DesiredOn, "barrier_remove_failed")
				return err
			}
		} else if !m.barrierProven() {
			m.needsAttention(DesiredOn, "barrier_install_unproven")
			return errors.New("maintenance barrier installation is unproven")
		} else {
			m.setStatus(Status{
				SchemaVersion: 1,
				Desired:       DesiredOn,
				Phase:         PhaseBarrierActive,
				CorePID:       process.PID,
				CoreVersion:   state.Version,
				Protection:    ProtectionBlocked,
			})
			if !releaseBarrier {
				return nil
			}
			if err := m.releaseBarrierToCore(ctx); err != nil {
				m.needsAttention(DesiredOn, "barrier_remove_failed")
				return err
			}
		}
	}
	if err := m.setProtectedStatus(PhaseCommitted, process.PID, state.Version, ""); err != nil {
		return m.failDNSActivation(ctx, state, "dns_verification_failed", err)
	}
	return nil
}

func (m *Manager) ensureDNSManaged(ctx context.Context, runtimeState supervisor.RuntimeState) error {
	status, err := m.dns.EnsureManaged(ctx)
	m.cacheDNSStatus(status)
	if err != nil {
		return m.failDNSActivation(ctx, runtimeState, "dns_takeover_failed", fmt.Errorf("take over DNS: %w", err))
	}

	status, err = m.dns.Inspect(ctx)
	m.cacheDNSStatus(status)
	if err != nil {
		return m.failDNSActivation(ctx, runtimeState, "dns_verification_failed", fmt.Errorf("verify managed DNS: %w", err))
	}
	if status.State != DNSManaged {
		return m.failDNSActivation(ctx, runtimeState, "dns_verification_failed", fmt.Errorf("verify managed DNS: state is %q", status.State))
	}
	return nil
}

func (m *Manager) restoreDNS(ctx context.Context) error {
	status, err := m.dns.Restore(ctx)
	m.cacheDNSStatus(status)
	if err != nil {
		return err
	}
	if status.State != DNSUnmanaged {
		return fmt.Errorf("DNS restore left state %q", status.State)
	}
	return nil
}

func (m *Manager) failDNSActivation(ctx context.Context, runtimeState supervisor.RuntimeState, code string, cause error) error {
	var barrierErr error
	if !m.barrierProven() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), m.cleanupTimeout)
		barrierErr = m.installBarrierForRecovery(cleanupCtx, runtimeState)
		cancelCleanup()
	}
	m.needsAttention(DesiredOn, code)
	return errors.Join(cause, barrierErr)
}

func (m *Manager) setProtectedStatus(phase Phase, pid int, version, lastError string) error {
	if m.dnsStatus.State != DNSManaged {
		return fmt.Errorf("cannot publish protected status with DNS state %q", m.dnsStatus.State)
	}
	m.setStatus(Status{
		SchemaVersion: 1,
		Desired:       DesiredOn,
		Phase:         phase,
		CorePID:       pid,
		CoreVersion:   version,
		Protection:    ProtectionProtected,
		LastError:     lastError,
	})
	return nil
}

func (m *Manager) requireLegacyReleased(ctx context.Context) error {
	if m.legacy == nil {
		return nil
	}
	present, err := m.legacy.Present(ctx)
	if err != nil {
		m.needsAttention(DesiredOn, "legacy_core_state_failed")
		return fmt.Errorf("inspect legacy Core ownership: %w", err)
	}
	if present {
		m.needsAttention(DesiredOn, "legacy_core_migration_pending")
		return errors.New("legacy Core ownership requires migration")
	}
	return nil
}

func sameProcessGeneration(a, b Process) bool {
	return a.PID > 0 &&
		a.PID == b.PID &&
		a.Generation != "" &&
		a.Generation == b.Generation
}

func (m *Manager) monitor(process Process) {
	err, ok := <-process.Exit
	if !ok {
		err = errors.New("Core exit channel closed")
	}
	m.handleUnexpectedExit(process, err)
}

func (m *Manager) handleUnexpectedExit(process Process, exitErr error) {
	recoveryCtx, done, ok := m.admitRecovery()
	if !ok {
		return
	}
	defer done()
	if err := m.acquireMutation(recoveryCtx); err != nil {
		return
	}
	defer m.releaseMutation()
	operationCtx, cancelOperation := context.WithTimeout(recoveryCtx, m.restartTimeout)
	defer cancelOperation()
	if !sameProcessGeneration(m.current, process) {
		return
	}
	if errors.Is(exitErr, ErrProcessOwnershipUncertain) {
		if !m.barrierProven() {
			_ = m.installBarrierForRecovery(operationCtx, m.runtime)
		}
		process.Uncertain = true
		m.current = process
		m.needsAttention(DesiredOn, "core_ownership_uncertain")
		return
	}
	intent, err := m.store.LoadIntentSnapshot(time.Now())
	if err != nil {
		// 「问不出来」不等于「没有挂起」:两半都读不出来时一律 fail-closed —— 装屏障、
		// 不重启。屏障在维护窗口里是有害的(见下),但这条分支恰恰是不知道自己在不在
		// 维护窗口里,而漏掉屏障的代价是真实 IP 泄漏。
		if !m.barrierProven() {
			_ = m.installBarrierForRecovery(operationCtx, m.runtime)
		}
		m.needsAttention(DesiredOn, intentReadFailureCode(err))
		return
	}
	// 维护挂起武装着 ⇒ 这次退出是**别人要求的**,不是崩溃。
	//
	// 既不装屏障也不重启:升级正在换二进制,而屏障是覆盖整个公网的 /2 reject
	// 路由 —— 在一台正要去装文件的机器上装它,等于把修复通道自己掐断。
	// desired 此刻仍然写着 on(这正是本期的全部意义),所以不看挂起就一定会重启。
	if intent.HoldArmed {
		log.Printf("guardian_core_exit_under_hold reason=%s expires_at=%s",
			intent.Hold.Reason, intent.Hold.ExpiresAt.Format(time.RFC3339))
		m.current = Process{}
		return
	}
	if intent.Desired != DesiredOn {
		m.current = Process{}
		return
	}

	runtimeState := m.runtime
	m.current = Process{}
	installErr := m.installBarrierForRecovery(operationCtx, runtimeState)
	if installErr != nil {
		m.needsAttention(DesiredOn, "barrier_install_failed")
	} else {
		m.needsAttention(DesiredOn, "core_unexpected_exit")
	}

	if _, err := m.startCoreLocked(operationCtx); err != nil {
		m.needsAttentionUnlessDNSActivationFailure("core_restart_failed")
		return
	}
	if m.barrierOwnership.proof != barrierAbsent {
		if err := m.removeBarrier(operationCtx); err != nil {
			m.needsAttention(DesiredOn, "barrier_remove_failed")
		}
	}
}

// installBarrierForRecovery installs a barrier ahead of an unexpected-exit
// restart or an ownership-uncertain hold. If the bypass-carrying context
// cannot be resolved (e.g. the default gateway is unavailable because
// another VPN owns the default route via a point-to-point tunnel), it
// degrades to a block-only barrier — public IPv4/IPv6 blackholed, no
// bypasses — rather than leaving Core to come back with no barrier at all.
// Fail-closed over fail-open.
func (m *Manager) installBarrierForRecovery(ctx context.Context, state supervisor.RuntimeState) error {
	barrierContext, err := m.barrierContextForRuntime(ctx, state)
	if err != nil {
		barrierContext = blockOnlyRecoveryContext(m.contextForRuntime(state))
	}
	return m.installBarrier(ctx, barrierContext)
}

func (m *Manager) cleanupStartedCore(ctx context.Context, process Process) error {
	cleanupCtx, cancel := context.WithTimeout(ctx, m.cleanupTimeout)
	defer cancel()
	return m.runner.Stop(cleanupCtx, process)
}

func (m *Manager) reserveCleanup(ctx context.Context) (context.Context, context.CancelFunc, error) {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		operationCtx, cancel := context.WithCancel(withPostForkCleanupContext(ctx, ctx))
		return operationCtx, cancel, nil
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, nil, fmt.Errorf("accepted mutation deadline leaves no Core cleanup budget: %w", context.DeadlineExceeded)
	}
	cleanupBudget := m.cleanupTimeout
	if half := remaining / 2; cleanupBudget > half {
		cleanupBudget = half
	}
	operationDeadline := deadline.Add(-cleanupBudget)
	operationCtx, cancel := context.WithDeadline(withPostForkCleanupContext(ctx, ctx), operationDeadline)
	return operationCtx, cancel, nil
}

func (m *Manager) retainUncertain(process Process) {
	process.Uncertain = true
	m.current = process
	if process.Resolution != nil {
		go func() {
			if err, ok := <-process.Resolution; ok && err == nil {
				m.clearUncertaintyAfterProof(process)
			}
		}()
	}
	if process.Exit != nil {
		go func() {
			if err, ok := <-process.Exit; ok && !errors.Is(err, ErrProcessOwnershipUncertain) {
				m.clearUncertaintyAfterProof(process)
			}
		}()
	}
}

func (m *Manager) clearUncertaintyAfterProof(process Process) {
	<-m.mutation
	defer m.releaseMutation()
	if !m.current.Uncertain || !sameUncertainProcess(m.current, process) {
		return
	}
	m.current = Process{}
	status := m.Status()
	if status.LastError == "core_ownership_uncertain" {
		status.CorePID = 0
		status.CoreVersion = ""
		status.LastError = ""
		m.setStatus(status)
	}
}

func sameUncertainProcess(current, resolved Process) bool {
	if current.Resolution != nil && current.Resolution == resolved.Resolution {
		return true
	}
	return current.Exit != nil && current.Exit == resolved.Exit
}

func (m *Manager) admitRecovery() (context.Context, func(), bool) {
	m.recoveryMu.Lock()
	defer m.recoveryMu.Unlock()
	if !m.recoveryAccepting {
		return nil, nil, false
	}
	m.recoveryActive++
	ctx, cancel := context.WithCancel(m.recoveryContext)
	return ctx, func() {
		cancel()
		m.finishRecovery()
	}, true
}

func (m *Manager) finishRecovery() {
	m.recoveryMu.Lock()
	defer m.recoveryMu.Unlock()
	m.recoveryActive--
	m.closeRecoveryDrainedLocked()
}

func (m *Manager) beginRecoveryShutdown() {
	m.recoveryMu.Lock()
	defer m.recoveryMu.Unlock()
	m.recoveryAccepting = false
	m.cancelRecovery()
	m.closeRecoveryDrainedLocked()
}

func (m *Manager) waitForRecoveries(ctx context.Context) error {
	select {
	case <-m.recoveryDrained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) recoveryActiveCount() int {
	m.recoveryMu.Lock()
	defer m.recoveryMu.Unlock()
	return m.recoveryActive
}

func (m *Manager) closeRecoveryDrainedLocked() {
	if !m.recoveryAccepting && m.recoveryActive == 0 && !m.recoveryClosed {
		close(m.recoveryDrained)
		m.recoveryClosed = true
	}
}

func (m *Manager) acquireMutation(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", errMutationBusy, ctx.Err())
	case <-m.mutation:
	}
	if err := ctx.Err(); err != nil {
		m.releaseMutation()
		return fmt.Errorf("%w: %w", errMutationBusy, err)
	}
	return nil
}

func (m *Manager) releaseMutation() {
	m.mutation <- struct{}{}
}

// removeBarrier and releaseBarrierToCore both act only on
// m.barrierOwnership.context — the context recorded when the barrier was
// installed (recordBarrierAttempt already folded any gateway resolved at
// install time into it). They deliberately take no BarrierContext parameter:
// a caller resolving one just to hand it to a function that ignores it is
// dead work that can fail for no reason (see barrierContextForRuntime's
// gateway discovery being called on a release path — a transient `route -n
// get default` failure had no business turning a healthy release into a
// Blocked machine). Only the two real *install* sites — Down and
// installBarrierForRecovery — need to resolve a gateway, because they are
// about to hand a fresh BarrierContext to Install/installBarrier.
func (m *Manager) removeBarrier(ctx context.Context) error {
	if m.barrierOwnership.proof == barrierAbsent {
		return nil
	}
	ownedContext := cloneBarrierContext(m.barrierOwnership.context)
	if err := m.barrier.Remove(ctx, ownedContext); err != nil {
		m.barrierOwnership.proof = barrierRemovalAttempted
		return fmt.Errorf("remove maintenance barrier: %w", err)
	}
	m.consumeBarrierOwnership()
	return nil
}

func (m *Manager) releaseBarrierToCore(ctx context.Context) error {
	if m.barrierOwnership.proof == barrierAbsent {
		return nil
	}
	if !m.barrierProven() && m.barrierOwnership.proof != barrierReleaseAttempted {
		return errors.New("release maintenance barrier before installation was proven")
	}
	ownedContext := cloneBarrierContext(m.barrierOwnership.context)
	if err := m.barrier.Release(ctx, ownedContext, m.runtime.ServerBypass); err != nil {
		m.barrierOwnership.proof = barrierReleaseAttempted
		return fmt.Errorf("release maintenance barrier to Core: %w", err)
	}
	m.consumeBarrierOwnership()
	return nil
}

func (m *Manager) installBarrier(ctx context.Context, barrierContext BarrierContext) error {
	m.recordBarrierAttempt(barrierContext)
	if err := m.barrier.Install(ctx, barrierContext); err != nil {
		return err
	}
	m.barrierOwnership.proof = barrierProven
	return nil
}

func (m *Manager) recordBarrierAttempt(barrierContext BarrierContext) {
	owned := cloneBarrierContext(m.barrierOwnership.context)
	if m.barrierOwnership.proof == barrierAbsent {
		owned = cloneBarrierContext(barrierContext)
	} else {
		if barrierContext.Gateway != "" {
			owned.Gateway = barrierContext.Gateway
		}
		owned.BlockIPv6 = owned.BlockIPv6 || barrierContext.BlockIPv6
		owned.blockOnly = owned.blockOnly && barrierContext.blockOnly
		seen := make(map[string]struct{}, len(owned.ServerBypass)+len(barrierContext.ServerBypass))
		merged := make([]string, 0, len(owned.ServerBypass)+len(barrierContext.ServerBypass))
		for _, bypass := range append(append([]string(nil), owned.ServerBypass...), barrierContext.ServerBypass...) {
			if _, ok := seen[bypass]; ok {
				continue
			}
			seen[bypass] = struct{}{}
			merged = append(merged, bypass)
		}
		owned.ServerBypass = merged
		if len(owned.ServerBypass) != 0 {
			owned.blockOnly = false
		}
	}
	m.barrierOwnership = barrierOwnership{
		proof:      barrierInstallAttempted,
		credential: m.nextBarrierCredential(),
		context:    owned,
	}
}

func (m *Manager) consumeBarrierOwnership() {
	m.nextBarrierCredential()
	m.barrierOwnership = barrierOwnership{}
}

func (m *Manager) nextBarrierCredential() barrierCredential {
	m.barrierGeneration++
	if m.barrierGeneration == 0 {
		m.barrierGeneration++
	}
	return m.barrierGeneration
}

func (m *Manager) barrierProven() bool {
	return m.barrierOwnership.proof == barrierProven
}

func (m *Manager) coreStartOptions() CoreStartOptions {
	if !m.barrierProven() {
		return CoreStartOptions{}
	}
	return CoreStartOptions{GuardianBypassHandoff: append([]string(nil), m.barrierOwnership.context.ServerBypass...)}
}

func (m *Manager) downRestoreContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := m.restartTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		reserve := m.restartTimeout
		if half := remaining / 2; reserve > half {
			reserve = half
		}
		if available := remaining - reserve; timeout > available {
			timeout = available
		}
	}
	return context.WithTimeout(ctx, timeout)
}

func (m *Manager) contextForRuntime(state supervisor.RuntimeState) BarrierContext {
	barrierContext := cloneBarrierContext(m.barrierContext)
	if len(state.ServerBypass) != 0 {
		barrierContext.ServerBypass = append([]string(nil), state.ServerBypass...)
	}
	return barrierContext
}

// barrierContextForRuntime resolves the barrier context that should be used to
// actually install/reassert/release a barrier for the given runtime state. The
// default gateway is only discovered here, lazily, the moment a barrier with
// server-bypass routes is really about to be planned — daemon startup and any
// other path that never installs a bypass barrier (status reads, blockOnly
// recovery) must not depend on it, since another VPN can legitimately own the
// default route (point-to-point utun, no gateway) without that blocking Guardian.
func (m *Manager) barrierContextForRuntime(ctx context.Context, state supervisor.RuntimeState) (BarrierContext, error) {
	barrierContext := m.contextForRuntime(state)
	if barrierContext.Gateway == "" && len(barrierContext.ServerBypass) > 0 {
		gateway, err := m.gatewayProvider.DefaultGateway(ctx)
		if err != nil {
			return BarrierContext{}, fmt.Errorf("resolve default gateway for barrier: %w", err)
		}
		barrierContext.Gateway = gateway
	}
	return barrierContext, nil
}

func (m *Manager) needsAttention(desired DesiredState, code string) {
	status := m.Status()
	status.SchemaVersion = 1
	status.Desired = desired
	status.Phase = PhaseNeedsAttention
	status.Protection = ProtectionNeedsAttention
	if m.barrierProven() {
		status.Protection = ProtectionBlocked
	}
	status.LastError = code
	// 代际号必须每次都真的往前走——即使是连续两次同一个失败码。「持续、重复的
	// 同一失败」正是回传失败码这一功能存在的理由;如果 LocalAPI 靠比较
	// LastError 的值来判断"这次失败是否真的设置过它",连续同因失败会被误判为
	// 陈旧而丢弃。代际号只看"needsAttention 是否真的跑过一次",与具体码值无关。
	status.LastErrorGeneration = m.nextLastErrorGeneration()
	m.setStatus(status)
	// 失败码只存在内存里等于没有:真机事故中 Guardian 已经知道原因,
	// 却既不落日志也不外传,排查耗时四轮。
	log.Printf("guardian_needs_attention code=%s desired=%s protection=%s", code, desired, status.Protection)
}

// nextLastErrorGeneration returns a fresh, strictly-increasing generation
// number for LastErrorGeneration. Backed by an atomic counter (rather than
// statusMu) so it stays safe to call from needsAttention regardless of
// which goroutine is racing to publish the next status (e.g. the
// unexpected-exit monitor runs on its own goroutine).
func (m *Manager) nextLastErrorGeneration() uint64 {
	return m.lastErrorGeneration.Add(1)
}

func (m *Manager) needsAttentionUnlessDNSActivationFailure(code string) {
	switch m.Status().LastError {
	case "dns_takeover_failed", "dns_verification_failed":
		return
	default:
		m.needsAttention(DesiredOn, code)
	}
}

func (m *Manager) cacheDNSStatus(status DNSStatus) {
	m.dnsStatus = DNSStatus{
		State:   normalizedDNSState(status.State),
		Service: status.Service,
	}
}

func (m *Manager) setStatus(status Status) {
	// 调谐报告不住在 m.status 里(见 Manager.reconcileReport)。这里显式清掉,
	// 免得某条「先 m.Status() 取一份、改几个字段再写回」的路径(needsAttention
	// 就是)把一份快照塞进 m.status,与真正的那一份并存 —— 那样 m.status 里就
	// 会有一份永远不再更新、却看起来完全正常的陈旧报告。
	status.Reconcile = nil
	status.DNSState = m.dnsStatus.State
	status.DNSManaged = m.dnsStatus.State == DNSManaged
	status.DNSService = m.dnsStatus.Service
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	// setStatus replaces m.status wholesale, and the vast majority of call
	// sites construct a fresh Status{...} literal that never mentions
	// LastErrorGeneration (so it's the zero value). Only needsAttention ever
	// sets a real, nonzero generation. Preserve the previous one across every
	// other status transition so the counter stays a reliable "did
	// needsAttention run" signal instead of getting silently wiped by the
	// next unrelated setStatus call.
	if status.LastErrorGeneration == 0 {
		status.LastErrorGeneration = m.status.LastErrorGeneration
	}
	m.status = status
}

func cloneBarrierContext(in BarrierContext) BarrierContext {
	in.ServerBypass = append([]string(nil), in.ServerBypass...)
	return in
}
