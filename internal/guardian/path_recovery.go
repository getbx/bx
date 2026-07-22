package guardian

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/getbx/bx/internal/supervisor"
)

var (
	errPathRecoveryInvalid      = errors.New("invalid path recovery request")
	errPathRecoveryShuttingDown = errors.New("Guardian path recovery is shutting down")
	recoveryGenerationPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type CorePathClient interface {
	RecoverPath(context.Context, supervisor.PathRecoveryRequest) (supervisor.PathRecoverySnapshot, error)
}

type pathRecoveryTransaction struct {
	request  RecoveryRequest
	snapshot RecoverySnapshot
}

type pathRecoveryTransition uint8

const (
	pathRecoveryTransitionPreserveGenerated pathRecoveryTransition = iota
	pathRecoveryTransitionResolveOff
)

type pathRecoveryLifecycle interface {
	beginPathRecoveryShutdown()
	waitForPathRecoveries(context.Context) error
}

func ValidateRecoveryRequest(request RecoveryRequest) (RecoveryRequest, error) {
	normalized := request
	normalized.Reason = strings.TrimSpace(normalized.Reason)
	normalized.Generation = strings.TrimSpace(normalized.Generation)
	switch normalized.Reason {
	case "manual", "underlay_changed":
	default:
		return RecoveryRequest{}, errPathRecoveryInvalid
	}
	if normalized.Generation != "" && !recoveryGenerationPattern.MatchString(normalized.Generation) {
		return RecoveryRequest{}, errPathRecoveryInvalid
	}
	return normalized, nil
}

func (r *ExecCoreRunner) RecoverPath(ctx context.Context, request supervisor.PathRecoveryRequest) (supervisor.PathRecoverySnapshot, error) {
	controlSocket := r.ControlSocket
	if controlSocket == "" {
		controlSocket = supervisor.SockPath
	}
	return supervisor.RecoverPathControl(ctx, controlSocket, request)
}

func (m *Manager) RequestPathRecovery(request RecoveryRequest) (RecoverySnapshot, error) {
	normalized, err := ValidateRecoveryRequest(request)
	if err != nil {
		return RecoverySnapshot{}, err
	}

	m.pathRecoveryMu.Lock()
	if !m.pathRecoveryAccepting {
		m.pathRecoveryMu.Unlock()
		return RecoverySnapshot{}, errPathRecoveryShuttingDown
	}
	if m.pathRecoveryPending != nil && samePathRecoveryGeneration(m.pathRecoveryPending.request, normalized) {
		snapshot := m.pathRecoveryPending.snapshot
		m.pathRecoveryMu.Unlock()
		return snapshot, nil
	}
	if m.pathRecoveryActive &&
		!(m.pathRecoveryFences > 0 && normalized.Generation == "") &&
		samePathRecoveryGeneration(recoveryRequestFromSnapshot(m.pathRecoveryCurrent), normalized) {
		snapshot := m.pathRecoveryCurrent
		m.pathRecoveryMu.Unlock()
		return snapshot, nil
	}
	if normalized.Generation != "" &&
		m.pathRecoveryCurrent.Generation == normalized.Generation &&
		completedPathRecoveryState(m.pathRecoveryCurrent.State) {
		snapshot := m.pathRecoveryCurrent
		m.pathRecoveryMu.Unlock()
		return snapshot, nil
	}

	transaction := m.newPathRecoveryTransactionLocked(normalized)
	if m.pathRecoveryActive {
		m.pathRecoveryPending = &transaction
		if m.pathRecoveryFences > 0 {
			m.pathRecoveryCurrent = transaction.snapshot
		}
		cancel := m.pathRecoveryCancel
		m.pathRecoveryMu.Unlock()
		if cancel != nil {
			cancel()
		}
		return transaction.snapshot, nil
	}
	if m.pathRecoveryFences > 0 {
		m.pathRecoveryPending = &transaction
		m.pathRecoveryCurrent = transaction.snapshot
		m.pathRecoveryMu.Unlock()
		return transaction.snapshot, nil
	}
	if m.Status().Desired == DesiredOff {
		transaction.snapshot.State = "ignored"
		transaction.snapshot.Stage = "off"
		transaction.snapshot.UpdatedAt = time.Now().UTC()
		m.pathRecoveryCurrent = transaction.snapshot
		m.pathRecoveryMu.Unlock()
		return transaction.snapshot, nil
	}

	operationCtx, cancel := context.WithTimeout(context.Background(), guardianMutationTimeout)
	m.pathRecoveryCurrent = transaction.snapshot
	m.pathRecoveryCancel = cancel
	m.pathRecoveryActive = true
	m.pathRecoveryMu.Unlock()
	go m.runPathRecovery(operationCtx, transaction)
	return transaction.snapshot, nil
}

func (m *Manager) CurrentPathRecovery() RecoverySnapshot {
	m.pathRecoveryMu.Lock()
	defer m.pathRecoveryMu.Unlock()
	return m.pathRecoveryCurrent
}

func (m *Manager) runPathRecovery(operationCtx context.Context, transaction pathRecoveryTransaction) {
	for {
		m.publishRunningPathRecovery(transaction)
		result := m.executePathRecovery(operationCtx, transaction)

		m.pathRecoveryMu.Lock()
		if m.pathRecoveryCurrent.ID == transaction.snapshot.ID {
			m.pathRecoveryCurrent = result
		}
		m.pathRecoveryCancel = nil
		if !m.pathRecoveryAccepting {
			m.pathRecoveryPending = nil
			m.pathRecoveryActive = false
			m.closePathRecoveryDrainedLocked()
			m.pathRecoveryMu.Unlock()
			return
		}
		if m.pathRecoveryFences > 0 {
			m.pathRecoveryActive = false
			if m.pathRecoveryPending != nil {
				m.pathRecoveryCurrent = m.pathRecoveryPending.snapshot
			}
			m.pathRecoveryMu.Unlock()
			return
		}
		if m.pathRecoveryPending == nil {
			m.pathRecoveryActive = false
			m.pathRecoveryResolveOff = false
			m.pathRecoveryMu.Unlock()
			return
		}
		transaction = *m.pathRecoveryPending
		m.pathRecoveryPending = nil
		if m.pathRecoveryResolveOff {
			m.pathRecoveryResolveOff = false
			m.pathRecoveryActive = false
			m.pathRecoveryCurrent = ignoredPathRecoverySnapshot(transaction.snapshot)
			m.pathRecoveryMu.Unlock()
			return
		}
		operationCtx, m.pathRecoveryCancel = context.WithTimeout(context.Background(), guardianMutationTimeout)
		m.pathRecoveryCurrent = transaction.snapshot
		m.pathRecoveryMu.Unlock()
	}
}

func (m *Manager) publishRunningPathRecovery(transaction pathRecoveryTransaction) {
	m.pathRecoveryMu.Lock()
	defer m.pathRecoveryMu.Unlock()
	if m.pathRecoveryCurrent.ID != transaction.snapshot.ID {
		return
	}
	snapshot := transaction.snapshot
	snapshot.State = "running"
	snapshot.Stage = "core_recovery"
	snapshot.UpdatedAt = time.Now().UTC()
	m.pathRecoveryCurrent = snapshot
}

func (m *Manager) executePathRecovery(ctx context.Context, transaction pathRecoveryTransaction) RecoverySnapshot {
	if err := m.acquireMutation(ctx); err != nil {
		return failedPathRecoverySnapshot(transaction.snapshot, supervisor.PathRecoverySnapshot{}, err)
	}
	defer m.releaseMutation()
	if m.Status().Desired == DesiredOff {
		snapshot := transaction.snapshot
		snapshot.State = "ignored"
		snapshot.Stage = "off"
		snapshot.UpdatedAt = time.Now().UTC()
		return snapshot
	}
	if m.corePath == nil {
		return failedPathRecoverySnapshot(transaction.snapshot, supervisor.PathRecoverySnapshot{}, &supervisor.PathRecoveryError{Code: "recovery_unavailable"})
	}
	result, err := m.corePath.RecoverPath(ctx, supervisor.PathRecoveryRequest{
		Reason:     transaction.request.Reason,
		Generation: transaction.request.Generation,
	})
	if err != nil {
		return failedPathRecoverySnapshot(transaction.snapshot, result, err)
	}
	return completedPathRecoverySnapshot(transaction.snapshot, result)
}

func (m *Manager) newPathRecoveryTransactionLocked(request RecoveryRequest) pathRecoveryTransaction {
	m.pathRecoverySequence++
	now := time.Now().UTC()
	return pathRecoveryTransaction{
		request: request,
		snapshot: RecoverySnapshot{
			ID:         "recovery-" + strconv.FormatUint(m.pathRecoverySequence, 10),
			State:      "accepted",
			Stage:      "queued",
			Reason:     request.Reason,
			Generation: request.Generation,
			Attempt:    1,
			StartedAt:  now,
			UpdatedAt:  now,
		},
	}
}

func (m *Manager) beginPathRecoveryTransition(transition pathRecoveryTransition) {
	m.pathRecoveryMu.Lock()
	m.pathRecoveryFences++
	if transition == pathRecoveryTransitionResolveOff {
		m.pathRecoveryResolveOff = true
		m.queueInterruptedPathRecoveryLocked(false)
	} else {
		m.queueInterruptedPathRecoveryLocked(true)
	}
	cancel := m.pathRecoveryCancel
	m.pathRecoveryMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) endPathRecoveryTransition() {
	desired := m.Status().Desired
	m.pathRecoveryMu.Lock()
	if m.pathRecoveryFences > 0 {
		m.pathRecoveryFences--
	}
	if desired == DesiredOff {
		m.pathRecoveryResolveOff = true
	}
	if m.pathRecoveryFences > 0 || !m.pathRecoveryAccepting {
		m.pathRecoveryMu.Unlock()
		return
	}
	if m.pathRecoveryPending == nil {
		if !m.pathRecoveryActive {
			m.pathRecoveryResolveOff = false
		}
		m.pathRecoveryMu.Unlock()
		return
	}
	if m.pathRecoveryActive {
		m.pathRecoveryMu.Unlock()
		return
	}
	transaction := *m.pathRecoveryPending
	m.pathRecoveryPending = nil
	if m.pathRecoveryResolveOff {
		m.pathRecoveryResolveOff = false
		m.pathRecoveryCurrent = ignoredPathRecoverySnapshot(transaction.snapshot)
		m.pathRecoveryMu.Unlock()
		return
	}
	operationCtx, cancel := context.WithTimeout(context.Background(), guardianMutationTimeout)
	m.pathRecoveryCurrent = transaction.snapshot
	m.pathRecoveryCancel = cancel
	m.pathRecoveryActive = true
	m.pathRecoveryMu.Unlock()
	go m.runPathRecovery(operationCtx, transaction)
}

func (m *Manager) queueInterruptedPathRecoveryLocked(generatedOnly bool) {
	if !m.pathRecoveryActive || m.pathRecoveryPending != nil || m.pathRecoveryCurrent.ID == "" {
		return
	}
	if generatedOnly && m.pathRecoveryCurrent.Generation == "" {
		return
	}
	snapshot := m.pathRecoveryCurrent
	snapshot.State = "accepted"
	snapshot.Stage = "queued"
	snapshot.ErrorCode = ""
	snapshot.Detail = ""
	snapshot.UpdatedAt = time.Now().UTC()
	m.pathRecoveryPending = &pathRecoveryTransaction{
		request:  recoveryRequestFromSnapshot(snapshot),
		snapshot: snapshot,
	}
}

func ignoredPathRecoverySnapshot(snapshot RecoverySnapshot) RecoverySnapshot {
	snapshot.State = "ignored"
	snapshot.Stage = "off"
	snapshot.ErrorCode = ""
	snapshot.Detail = ""
	snapshot.UpdatedAt = time.Now().UTC()
	return snapshot
}

func (m *Manager) beginPathRecoveryShutdown() {
	m.pathRecoveryMu.Lock()
	m.pathRecoveryAccepting = false
	m.pathRecoveryPending = nil
	m.pathRecoveryResolveOff = false
	cancel := m.pathRecoveryCancel
	m.closePathRecoveryDrainedLocked()
	m.pathRecoveryMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) waitForPathRecoveries(ctx context.Context) error {
	select {
	case <-m.pathRecoveryDrained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) pathRecoveryActiveCount() int {
	m.pathRecoveryMu.Lock()
	defer m.pathRecoveryMu.Unlock()
	if m.pathRecoveryActive {
		return 1
	}
	return 0
}

func (m *Manager) closePathRecoveryDrainedLocked() {
	if !m.pathRecoveryAccepting && !m.pathRecoveryActive && !m.pathRecoveryClosed {
		close(m.pathRecoveryDrained)
		m.pathRecoveryClosed = true
	}
}

func completedPathRecoverySnapshot(base RecoverySnapshot, result supervisor.PathRecoverySnapshot) RecoverySnapshot {
	snapshot := base
	if result.Attempt > 0 {
		snapshot.Attempt = result.Attempt
	}
	snapshot.Stage = publicPathRecoveryStage(result.Stage)
	if result.State != "succeeded" {
		snapshot.State = "failed"
		if snapshot.Stage == "" {
			snapshot.Stage = "failed"
		}
		snapshot.ErrorCode = stableGuardianPathRecoveryCode(result.ErrorCode)
		if snapshot.ErrorCode == "" {
			snapshot.ErrorCode = "recovery_failed"
		}
	} else {
		snapshot.State = "succeeded"
		if snapshot.Stage == "" {
			snapshot.Stage = "succeeded"
		}
		snapshot.ErrorCode = ""
	}
	snapshot.Detail = ""
	snapshot.UpdatedAt = time.Now().UTC()
	return snapshot
}

func failedPathRecoverySnapshot(base RecoverySnapshot, result supervisor.PathRecoverySnapshot, err error) RecoverySnapshot {
	snapshot := base
	if result.Attempt > 0 {
		snapshot.Attempt = result.Attempt
	}
	snapshot.State = "failed"
	snapshot.Stage = publicPathRecoveryStage(result.Stage)
	if snapshot.Stage == "" {
		snapshot.Stage = "failed"
	}
	snapshot.ErrorCode = guardianPathRecoveryErrorCode(err, result.ErrorCode)
	snapshot.Detail = ""
	snapshot.UpdatedAt = time.Now().UTC()
	return snapshot
}

func guardianPathRecoveryErrorCode(err error, resultCode string) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "recovery_canceled"
	}
	var recoveryErr *supervisor.PathRecoveryError
	if errors.As(err, &recoveryErr) && recoveryErr != nil {
		if code := stableGuardianPathRecoveryCode(recoveryErr.Code); code != "" {
			return code
		}
	}
	if code := stableGuardianPathRecoveryCode(resultCode); code != "" {
		return code
	}
	return "recovery_failed"
}

func stableGuardianPathRecoveryCode(code string) string {
	switch code {
	case "capture_invalid", "capture_missing", "recovery_canceled", "recovery_failed", "recovery_unavailable", "transport_unavailable", "underlay_rebind_failed", "underlay_unavailable", "verification_failed":
		return code
	default:
		return ""
	}
}

func publicPathRecoveryStage(stage string) string {
	switch stage {
	case "observe", "validate_capture", "rebind_underlay", "transport_health", "commit", "verify", "succeeded", "blocked", "failed":
		return stage
	default:
		return ""
	}
}

func redactRecoverySnapshot(snapshot RecoverySnapshot) RecoverySnapshot {
	snapshot.Detail = ""
	snapshot.ErrorCode = stableGuardianPathRecoveryCode(snapshot.ErrorCode)
	if snapshot.State == "failed" && snapshot.ErrorCode == "" {
		snapshot.ErrorCode = "recovery_failed"
	}
	return snapshot
}

func samePathRecoveryGeneration(a, b RecoveryRequest) bool {
	return a.Generation == b.Generation
}

func recoveryRequestFromSnapshot(snapshot RecoverySnapshot) RecoveryRequest {
	return RecoveryRequest{Reason: snapshot.Reason, Generation: snapshot.Generation}
}

func completedPathRecoveryState(state string) bool {
	return state == "succeeded" || state == "failed"
}
