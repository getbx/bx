package guardian

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/getbx/bx/internal/supervisor"
)

const (
	ProtectionOff            = "off"
	ProtectionStarting       = "starting"
	ProtectionProtected      = "protected"
	ProtectionBlocked        = "blocked"
	ProtectionNeedsAttention = "needs_attention"
)

type barrierProof uint8

const (
	barrierAbsent barrierProof = iota
	barrierInstallAttempted
	barrierProven
	barrierReleaseAttempted
	barrierRemovalAttempted
)

type barrierOwnership struct {
	proof   barrierProof
	context BarrierContext
}

var errRecoveryIncomplete = errors.New("guardian startup recovery incomplete")

type DesiredStore interface {
	LoadDesired() (DesiredState, error)
	SaveDesired(DesiredState) error
}

type CoreRunner interface {
	Existing(context.Context) (Process, error)
	Watch(Process) Process
	Verify(Process) error
	Start(context.Context, CoreStartOptions) (Process, error)
	Stop(context.Context, Process) error
}

type CoreStartOptions struct {
	GuardianBypassHandoff []string
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

type NetworkRestorer interface {
	Restore(context.Context) error
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
	Restorer         NetworkRestorer
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
	restorer               NetworkRestorer
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
	pathRecoverySequence   uint64
	pathRecoveryAccepting  bool
	pathRecoveryActive     bool
	pathRecoveryFences     int
	pathRecoveryResolveOff bool
	pathRecoveryDrained    chan struct{}
	pathRecoveryClosed     bool
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
	case options.Restorer == nil:
		return nil, errors.New("guardian network restorer required")
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
		restorer:              options.Restorer,
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
		},
	}
	m.mutation <- struct{}{}
	m.updateOperation <- struct{}{}
	return m, nil
}

func (m *Manager) Status() Status {
	m.statusMu.RLock()
	defer m.statusMu.RUnlock()
	return m.status
}

func (m *Manager) Up(ctx context.Context) error {
	if err := m.acquireMutation(ctx); err != nil {
		return err
	}
	defer m.releaseMutation()
	if m.recoveryBlocked {
		return errRecoveryIncomplete
	}
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
	barrierContext = m.contextForRuntime(state)
	if err := m.releaseBarrierToCore(ctx, barrierContext); err != nil {
		m.needsAttention(DesiredOn, "barrier_remove_failed")
		return err
	}
	if err := m.legacy.Remove(); err != nil {
		reinstallErr := m.retainMigrationBarrier(ctx, barrierContext)
		m.needsAttention(DesiredOn, "legacy_unit_remove_failed")
		return errors.Join(fmt.Errorf("remove migrated Core unit: %w", err), reinstallErr)
	}
	m.setStatus(Status{
		SchemaVersion: 1,
		Desired:       DesiredOn,
		Phase:         PhaseCommitted,
		CorePID:       m.current.PID,
		CoreVersion:   state.Version,
		Protection:    ProtectionProtected,
	})
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

func (m *Manager) Down(ctx context.Context) error {
	m.beginPathRecoveryTransition(pathRecoveryTransitionResolveOff)
	defer m.endPathRecoveryTransition()
	if err := m.acquireMutation(ctx); err != nil {
		return err
	}
	defer m.releaseMutation()
	if m.recoveryBlocked {
		return errRecoveryIncomplete
	}

	desired, err := m.store.LoadDesired()
	if err != nil {
		m.needsAttention(DesiredOn, "desired_state_read_failed")
		return err
	}
	if desired == DesiredOff && m.current.PID == 0 {
		m.setStatus(Status{SchemaVersion: 1, Desired: DesiredOff, Phase: PhaseIdle, Protection: ProtectionOff})
		return nil
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

	barrierContext := m.contextForRuntime(runtimeState)
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
	restoreErr := m.restorer.Restore(restoreCtx)
	cancelRestore()
	if restoreErr != nil {
		restoreErr := fmt.Errorf("restore managed network: %w", restoreErr)
		recoveryCtx, cancelRecovery := context.WithTimeout(ctx, m.restartTimeout)
		defer cancelRecovery()
		if _, recoveryErr := m.startCoreLocked(recoveryCtx); recoveryErr != nil {
			m.needsAttention(DesiredOn, "down_restore_recovery_failed")
			return errors.Join(restoreErr, recoveryErr)
		}
		if err := m.removeBarrier(recoveryCtx, barrierContext); err != nil {
			m.needsAttention(DesiredOn, "barrier_remove_failed")
			return errors.Join(restoreErr, err)
		}
		return restoreErr
	}

	if err := m.store.SaveDesired(DesiredOff); err != nil {
		m.needsAttention(desired, "desired_state_write_failed")
		return fmt.Errorf("persist disabled state behind barrier: %w", err)
	}
	if err := m.removeBarrier(ctx, barrierContext); err != nil {
		m.needsAttention(DesiredOff, "barrier_remove_failed")
		return err
	}
	m.setStatus(Status{SchemaVersion: 1, Desired: DesiredOff, Phase: PhaseIdle, Protection: ProtectionOff})
	return nil
}

// Recover restores the persisted desired state without treating daemon shutdown
// as an instruction to stop Core or restore direct networking.
func (m *Manager) Recover(ctx context.Context) error {
	m.beginPathRecoveryTransition(pathRecoveryTransitionPreserveGenerated)
	defer m.endPathRecoveryTransition()
	if err := m.acquireMutation(ctx); err != nil {
		return err
	}
	defer m.releaseMutation()
	if err := m.recoverUpdateLocked(ctx); err != nil {
		m.recoveryBlocked = true
		return err
	}
	desired, err := m.store.LoadDesired()
	if err != nil {
		m.needsAttention(DesiredOn, "desired_state_read_failed")
		m.recoveryBlocked = true
		return err
	}
	if desired == DesiredOff {
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
	if m.barrierOwnership.proof != barrierAbsent {
		if m.barrierOwnership.proof == barrierReleaseAttempted && releaseBarrier {
			if err := m.releaseBarrierToCore(ctx, m.contextForRuntime(state)); err != nil {
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
			if err := m.releaseBarrierToCore(ctx, m.contextForRuntime(state)); err != nil {
				m.needsAttention(DesiredOn, "barrier_remove_failed")
				return err
			}
		}
	}
	m.setStatus(Status{
		SchemaVersion: 1,
		Desired:       DesiredOn,
		Phase:         PhaseCommitted,
		CorePID:       process.PID,
		CoreVersion:   state.Version,
		Protection:    ProtectionProtected,
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
		barrierContext := m.contextForRuntime(m.runtime)
		if !m.barrierProven() {
			_ = m.installBarrier(operationCtx, barrierContext)
		}
		process.Uncertain = true
		m.current = process
		m.needsAttention(DesiredOn, "core_ownership_uncertain")
		return
	}
	desired, err := m.store.LoadDesired()
	if err != nil {
		barrierContext := m.contextForRuntime(m.runtime)
		if !m.barrierProven() {
			_ = m.installBarrier(operationCtx, barrierContext)
		}
		m.needsAttention(DesiredOn, "desired_state_read_failed")
		return
	}
	if desired != DesiredOn {
		m.current = Process{}
		return
	}

	barrierContext := m.contextForRuntime(m.runtime)
	m.current = Process{}
	m.needsAttention(DesiredOn, "core_unexpected_exit")
	_ = m.installBarrier(operationCtx, barrierContext)

	if _, err := m.startCoreLocked(operationCtx); err != nil {
		m.needsAttention(DesiredOn, "core_restart_failed")
		return
	}
	if m.barrierOwnership.proof != barrierAbsent {
		if err := m.removeBarrier(operationCtx, barrierContext); err != nil {
			m.needsAttention(DesiredOn, "barrier_remove_failed")
		}
	}
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
		return ctx.Err()
	case <-m.mutation:
	}
	if err := ctx.Err(); err != nil {
		m.releaseMutation()
		return err
	}
	return nil
}

func (m *Manager) releaseMutation() {
	m.mutation <- struct{}{}
}

func (m *Manager) removeBarrier(ctx context.Context, barrierContext BarrierContext) error {
	if m.barrierOwnership.proof == barrierAbsent {
		return nil
	}
	ownedContext := cloneBarrierContext(m.barrierOwnership.context)
	if err := m.barrier.Remove(ctx, ownedContext); err != nil {
		m.barrierOwnership.proof = barrierRemovalAttempted
		return fmt.Errorf("remove maintenance barrier: %w", err)
	}
	m.barrierOwnership = barrierOwnership{}
	return nil
}

func (m *Manager) releaseBarrierToCore(ctx context.Context, barrierContext BarrierContext) error {
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
	m.barrierOwnership = barrierOwnership{}
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
	m.barrierOwnership = barrierOwnership{proof: barrierInstallAttempted, context: owned}
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
	m.setStatus(status)
}

func (m *Manager) setStatus(status Status) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	m.status = status
}

func cloneBarrierContext(in BarrierContext) BarrierContext {
	in.ServerBypass = append([]string(nil), in.ServerBypass...)
	return in
}
