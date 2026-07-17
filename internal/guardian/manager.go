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

type DesiredStore interface {
	LoadDesired() (DesiredState, error)
	SaveDesired(DesiredState) error
}

type CoreRunner interface {
	Existing(context.Context) (Process, error)
	Watch(Process) Process
	Verify(Process) error
	Start(context.Context) (Process, error)
	Stop(context.Context, Process) error
}

type HealthGate interface {
	Wait(context.Context, HealthTarget) (supervisor.RuntimeState, error)
}

type NetworkRestorer interface {
	Restore(context.Context) error
}

type ManagerOptions struct {
	Store          DesiredStore
	Runner         CoreRunner
	Health         HealthGate
	Barrier        Barrier
	Restorer       NetworkRestorer
	BarrierContext BarrierContext
	CoreVersion    string
	RestartTimeout time.Duration
	CleanupTimeout time.Duration
}

type Manager struct {
	mutation          chan struct{}
	statusMu          sync.RWMutex
	store             DesiredStore
	runner            CoreRunner
	health            HealthGate
	barrier           Barrier
	restorer          NetworkRestorer
	barrierContext    BarrierContext
	coreVersion       string
	restartTimeout    time.Duration
	cleanupTimeout    time.Duration
	current           Process
	runtime           supervisor.RuntimeState
	status            Status
	barrierHeld       bool
	recoveryMu        sync.Mutex
	recoveryContext   context.Context
	cancelRecovery    context.CancelFunc
	recoveryAccepting bool
	recoveryActive    int
	recoveryDrained   chan struct{}
	recoveryClosed    bool
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
	recoveryContext, cancelRecovery := context.WithCancel(context.Background())
	m := &Manager{
		mutation:          make(chan struct{}, 1),
		store:             options.Store,
		runner:            options.Runner,
		health:            options.Health,
		barrier:           options.Barrier,
		restorer:          options.Restorer,
		barrierContext:    cloneBarrierContext(options.BarrierContext),
		coreVersion:       options.CoreVersion,
		restartTimeout:    restartTimeout,
		cleanupTimeout:    cleanupTimeout,
		recoveryContext:   recoveryContext,
		cancelRecovery:    cancelRecovery,
		recoveryAccepting: true,
		recoveryDrained:   make(chan struct{}),
		status: Status{
			SchemaVersion: 1,
			Desired:       DesiredOff,
			Phase:         PhaseIdle,
			Protection:    ProtectionOff,
		},
	}
	m.mutation <- struct{}{}
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
	return m.upLocked(ctx)
}

func (m *Manager) upLocked(ctx context.Context) error {
	if m.current.Uncertain {
		m.needsAttention(DesiredOn, "core_ownership_uncertain")
		return uncertainOwnership(m.current, nil)
	}
	if m.current.PID != 0 && m.Status().Protection == ProtectionProtected {
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
	m.setStatus(Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseActivating, Protection: ProtectionStarting})

	existing, err := m.runner.Existing(ctx)
	if err != nil {
		if errors.Is(err, ErrProcessOwnershipUncertain) {
			if process, ok := uncertainProcess(err); ok {
				m.current = process
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
		if err := m.acceptHealthy(ctx, existing, state); err != nil {
			return fmt.Errorf("release held barrier after adopting Core: %w", err)
		}
		return nil
	}

	_, err = m.startCoreLocked(ctx)
	return err
}

func (m *Manager) Down(ctx context.Context) error {
	if err := m.acquireMutation(ctx); err != nil {
		return err
	}
	defer m.releaseMutation()

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
	if err := m.barrier.Install(ctx, barrierContext); err != nil {
		m.needsAttention(desired, "barrier_install_failed")
		return fmt.Errorf("install down barrier: %w", err)
	}
	m.barrierHeld = true
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
	if err := m.acquireMutation(ctx); err != nil {
		return err
	}
	defer m.releaseMutation()
	desired, err := m.store.LoadDesired()
	if err != nil {
		m.needsAttention(DesiredOn, "desired_state_read_failed")
		return err
	}
	if desired == DesiredOff {
		m.setStatus(Status{SchemaVersion: 1, Desired: DesiredOff, Phase: PhaseIdle, Protection: ProtectionOff})
		return nil
	}
	return m.upLocked(ctx)
}

func (m *Manager) startCoreLocked(ctx context.Context) (supervisor.RuntimeState, error) {
	process, err := m.runner.Start(ctx)
	if err != nil {
		if errors.Is(err, ErrProcessOwnershipUncertain) {
			if uncertain, ok := uncertainProcess(err); ok {
				m.current = uncertain
			}
			m.needsAttention(DesiredOn, "core_ownership_uncertain")
			return supervisor.RuntimeState{}, fmt.Errorf("start Core: %w", err)
		}
		m.needsAttention(DesiredOn, "core_start_failed")
		return supervisor.RuntimeState{}, fmt.Errorf("start Core: %w", err)
	}
	if err := m.runner.Verify(process); err != nil {
		if cleanupErr := m.cleanupStartedCore(ctx, process); cleanupErr != nil {
			m.current = Process{PID: process.PID, Executable: process.Executable, UID: process.UID, Generation: process.Generation, Uncertain: true}
			m.needsAttention(DesiredOn, "core_ownership_uncertain")
			return supervisor.RuntimeState{}, errors.Join(fmt.Errorf("verify started Core: %w", err), uncertainOwnership(m.current, cleanupErr))
		}
		m.needsAttention(DesiredOn, "core_identity_unverified")
		return supervisor.RuntimeState{}, fmt.Errorf("verify started Core: %w", err)
	}
	state, err := m.waitHealthy(ctx, process)
	if err != nil {
		if cleanupErr := m.cleanupStartedCore(ctx, process); cleanupErr != nil {
			m.current = Process{PID: process.PID, Executable: process.Executable, UID: process.UID, Generation: process.Generation, Uncertain: true}
			m.needsAttention(DesiredOn, "core_ownership_uncertain")
			return supervisor.RuntimeState{}, errors.Join(fmt.Errorf("wait for Core health: %w", err), uncertainOwnership(m.current, cleanupErr))
		}
		m.needsAttention(DesiredOn, "core_health_failed")
		return supervisor.RuntimeState{}, fmt.Errorf("wait for Core health: %w", err)
	}
	if err := m.acceptHealthy(ctx, process, state); err != nil {
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

func (m *Manager) acceptHealthy(ctx context.Context, process Process, state supervisor.RuntimeState) error {
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
	if m.barrierHeld {
		m.setStatus(Status{
			SchemaVersion: 1,
			Desired:       DesiredOn,
			Phase:         PhaseBarrierActive,
			CorePID:       process.PID,
			CoreVersion:   state.Version,
			Protection:    ProtectionBlocked,
		})
		if err := m.removeBarrier(ctx, m.contextForRuntime(state)); err != nil {
			m.needsAttention(DesiredOn, "barrier_remove_failed")
			return err
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

func (m *Manager) handleUnexpectedExit(process Process, _ error) {
	recoveryCtx, done, ok := m.admitRecovery()
	if !ok {
		return
	}
	defer done()
	if err := m.acquireMutation(recoveryCtx); err != nil {
		return
	}
	defer m.releaseMutation()
	if !sameProcessGeneration(m.current, process) {
		return
	}
	desired, err := m.store.LoadDesired()
	if err != nil {
		barrierContext := m.contextForRuntime(m.runtime)
		if !m.barrierHeld {
			if barrierErr := m.barrier.Install(recoveryCtx, barrierContext); barrierErr == nil {
				m.barrierHeld = true
			}
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
	if err := m.barrier.Install(recoveryCtx, barrierContext); err == nil {
		m.barrierHeld = true
	}

	restartCtx, cancel := context.WithTimeout(recoveryCtx, m.restartTimeout)
	defer cancel()
	if _, err := m.startCoreLocked(restartCtx); err != nil {
		m.needsAttention(DesiredOn, "core_restart_failed")
		return
	}
	if m.barrierHeld {
		if err := m.removeBarrier(restartCtx, barrierContext); err != nil {
			m.needsAttention(DesiredOn, "barrier_remove_failed")
		}
	}
}

func (m *Manager) cleanupStartedCore(ctx context.Context, process Process) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.cleanupTimeout)
	defer cancel()
	return m.runner.Stop(cleanupCtx, process)
}

func (m *Manager) admitRecovery() (context.Context, func(), bool) {
	m.recoveryMu.Lock()
	defer m.recoveryMu.Unlock()
	if !m.recoveryAccepting {
		return nil, nil, false
	}
	m.recoveryActive++
	ctx, cancel := context.WithTimeout(m.recoveryContext, m.restartTimeout)
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
	if !m.barrierHeld {
		return nil
	}
	if err := m.barrier.Remove(ctx, barrierContext); err != nil {
		return fmt.Errorf("remove maintenance barrier: %w", err)
	}
	m.barrierHeld = false
	return nil
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
	if m.barrierHeld {
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
