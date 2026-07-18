package guardian

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/supervisor"
	updatepkg "github.com/getbx/bx/internal/update"
)

const (
	currentGuardianProtocol         = 1
	maxUpdatePackageBytes           = 128 << 20
	updateRecoveryDescriptorVersion = 1
	updateRecoveryDescriptorName    = "guardian-recovery.json"
)

var (
	updateTransactionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	updateVersionPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\-]{0,127}$`)
	syncRecoveryRoot           = func(root *os.Root) error {
		directory, err := root.Open(".")
		if err != nil {
			return err
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		return errors.Join(syncErr, closeErr)
	}
)

type UpdateRequest struct {
	TransactionID string `json:"transaction_id"`
	FromVersion   string `json:"from_version"`
	ToVersion     string `json:"to_version"`
	AssetSHA256   string `json:"asset_sha256"`
	PackagePath   string `json:"package_path"`
	AppPath       string `json:"app_path,omitempty"`
	AppUID        int    `json:"app_uid,omitempty"`
	AppGID        int    `json:"app_gid,omitempty"`
}

type PreparedUpdate interface {
	SnapshotPath() string
	RequiredGuardianProtocol() int
	BindBarrierContext(BarrierContext) error
	Activate() error
	Restore() error
	Commit() error
}

type UpdatePreparer interface {
	Prepare(context.Context, UpdateRequest, []byte, Paths) (PreparedUpdate, error)
	Recover(context.Context, Transaction, Paths) (PreparedUpdate, BarrierContext, error)
	RecoveryBarrierContext(context.Context, Paths) (BarrierContext, error)
}

type updateStore interface {
	LoadTransaction() (*Transaction, error)
	LoadReceipt() (*Receipt, error)
	SaveTransaction(Transaction) error
	ClearTransaction() error
	SaveReceipt(Receipt) error
}

type guardianPathsProvider interface {
	guardianPaths() Paths
}

func (s *Store) guardianPaths() Paths {
	return s.paths
}

type updateError struct {
	code string
}

func (e updateError) Error() string { return e.code }

func newUpdateError(code string) error {
	return updateError{code: code}
}

func ValidateUpdateRequest(request UpdateRequest) (UpdateRequest, error) {
	normalized := request
	normalized.TransactionID = strings.TrimSpace(normalized.TransactionID)
	normalized.FromVersion = strings.TrimSpace(normalized.FromVersion)
	normalized.ToVersion = strings.TrimSpace(normalized.ToVersion)
	normalized.AssetSHA256 = strings.ToLower(strings.TrimSpace(normalized.AssetSHA256))
	if !updateTransactionIDPattern.MatchString(normalized.TransactionID) {
		return UpdateRequest{}, newUpdateError("update_metadata_invalid")
	}
	if !updateVersionPattern.MatchString(normalized.FromVersion) || !updateVersionPattern.MatchString(normalized.ToVersion) {
		return UpdateRequest{}, newUpdateError("update_metadata_invalid")
	}
	if len(normalized.AssetSHA256) != sha256.Size*2 {
		return UpdateRequest{}, newUpdateError("update_metadata_invalid")
	}
	if _, err := hex.DecodeString(normalized.AssetSHA256); err != nil {
		return UpdateRequest{}, newUpdateError("update_metadata_invalid")
	}
	if !filepath.IsAbs(normalized.PackagePath) || filepath.Clean(normalized.PackagePath) != normalized.PackagePath {
		return UpdateRequest{}, newUpdateError("update_metadata_invalid")
	}
	if normalized.AppUID < 0 || normalized.AppGID < 0 {
		return UpdateRequest{}, newUpdateError("update_metadata_invalid")
	}
	return normalized, nil
}

func (m *Manager) Update(ctx context.Context, request UpdateRequest) (UpdateResult, error) {
	normalized, err := ValidateUpdateRequest(request)
	if err != nil {
		return UpdateResult{}, err
	}
	if err := m.acquireUpdateOperation(ctx); err != nil {
		return UpdateResult{}, err
	}
	defer m.releaseUpdateOperation()

	if err := m.acquireMutation(ctx); err != nil {
		return UpdateResult{}, err
	}
	if m.recoveryBlocked {
		m.releaseMutation()
		return UpdateResult{}, errRecoveryIncomplete
	}
	status := m.Status()
	if m.updates == nil || m.updatePreparer == nil || m.updatePaths.Staging == "" || m.updatePaths.Snapshots == "" {
		m.releaseMutation()
		return m.updateResult(normalized, status.Phase, false, false), newUpdateError("update_unavailable")
	}
	if !m.validUpdateSourceLocked(normalized, status) {
		m.releaseMutation()
		return m.updateResult(normalized, status.Phase, false, false), newUpdateError("update_state_invalid")
	}
	m.releaseMutation()

	packageData, err := readVerifiedUpdatePackage(normalized, m.updatePaths)
	if err != nil {
		return m.updateResult(normalized, status.Phase, false, false), err
	}
	prepared, err := m.updatePreparer.Prepare(ctx, normalized, packageData, m.updatePaths)
	if err != nil {
		return m.updateResult(normalized, status.Phase, false, false), newUpdateError("update_prepare_failed")
	}
	if prepared.RequiredGuardianProtocol() > m.guardianProtocol {
		_ = prepared.Commit()
		return m.updateResult(normalized, status.Phase, false, false), newUpdateError("guardian_protocol_unsupported")
	}

	if err := m.acquireMutation(ctx); err != nil {
		_ = prepared.Commit()
		return m.updateResult(normalized, m.Status().Phase, false, false), err
	}
	if m.recoveryBlocked {
		m.releaseMutation()
		_ = prepared.Commit()
		return UpdateResult{}, errRecoveryIncomplete
	}
	status = m.Status()
	if !m.validUpdateSourceLocked(normalized, status) {
		m.releaseMutation()
		_ = prepared.Commit()
		return m.updateResult(normalized, status.Phase, false, false), newUpdateError("update_state_invalid")
	}
	if err := m.runner.Verify(m.current); err != nil {
		m.releaseMutation()
		_ = prepared.Commit()
		return m.updateResult(normalized, status.Phase, false, false), newUpdateError("update_source_identity_failed")
	}
	liveRuntime, err := m.health.Wait(ctx, HealthTarget{Version: normalized.FromVersion, PID: m.current.PID})
	if err != nil || liveRuntime.PID != m.current.PID || liveRuntime.Version != normalized.FromVersion {
		m.releaseMutation()
		_ = prepared.Commit()
		return m.updateResult(normalized, status.Phase, false, false), newUpdateError("update_runtime_refresh_failed")
	}
	gateway, err := m.gatewayProvider.DefaultGateway(ctx)
	if err != nil {
		m.releaseMutation()
		_ = prepared.Commit()
		return m.updateResult(normalized, status.Phase, false, false), newUpdateError("update_gateway_discovery_failed")
	}
	barrierContext := cloneBarrierContext(m.barrierContext)
	barrierContext.Gateway = gateway
	barrierContext.ServerBypass = append([]string(nil), liveRuntime.ServerBypass...)
	if _, _, _, err := PlanBarrier(barrierContext); err != nil {
		m.releaseMutation()
		_ = prepared.Commit()
		return m.updateResult(normalized, status.Phase, false, false), newUpdateError("update_handoff_invalid")
	}
	if err := prepared.BindBarrierContext(barrierContext); err != nil {
		m.releaseMutation()
		_ = prepared.Commit()
		return m.updateResult(normalized, status.Phase, false, false), newUpdateError("update_recovery_metadata_failed")
	}
	m.runtime = liveRuntime
	defer m.releaseMutation()
	return m.updatePreparedLocked(ctx, normalized, prepared, barrierContext)
}

func (m *Manager) validUpdateSourceLocked(request UpdateRequest, status Status) bool {
	return status.Desired == DesiredOn && status.Protection == ProtectionProtected &&
		m.current.PID != 0 && m.runtime.Version == request.FromVersion
}

func (m *Manager) acquireUpdateOperation(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.updateOperation:
	}
	if err := ctx.Err(); err != nil {
		m.releaseUpdateOperation()
		return err
	}
	return nil
}

func (m *Manager) releaseUpdateOperation() {
	m.updateOperation <- struct{}{}
}

func (m *Manager) recoverUpdateLocked(ctx context.Context) error {
	if m.updates == nil || m.updatePreparer == nil {
		return nil
	}
	transaction, err := m.updates.LoadTransaction()
	if err != nil {
		barrierContext, contextErr := m.updatePreparer.RecoveryBarrierContext(ctx, m.updatePaths)
		if contextErr != nil {
			barrierContext = cloneBarrierContext(m.barrierContext)
		}
		return m.failMalformedUpdateRecovery(ctx, barrierContext)
	}
	if transaction == nil {
		return nil
	}
	if !validPersistedUpdate(*transaction, m.updatePaths) {
		barrierContext, contextErr := m.updatePreparer.RecoveryBarrierContext(ctx, m.updatePaths)
		if contextErr != nil {
			barrierContext = cloneBarrierContext(m.barrierContext)
		}
		return m.failMalformedUpdateRecovery(ctx, barrierContext)
	}
	terminalReceiptMatches := m.matchingTerminalReceipt(transaction)
	prepared, barrierContext, recoverErr := m.updatePreparer.Recover(ctx, *transaction, m.updatePaths)
	if terminalReceiptMatches {
		switch {
		case recoverErr == nil:
			if err := prepared.Commit(); err != nil {
				return m.failCleanupOnlyRecovery(ctx, barrierContext, "update_cleanup_failed")
			}
			if err := m.clearReceiptBackedTransaction(transaction); err != nil {
				return m.failCleanupOnlyRecovery(ctx, barrierContext, "update_journal_cleanup_failed")
			}
			return nil
		case updateTransactionStagingGone(*transaction, m.updatePaths):
			if err := m.clearReceiptBackedTransaction(transaction); err != nil {
				return m.failCleanupOnlyRecovery(ctx, barrierContext, "update_journal_cleanup_failed")
			}
			return nil
		}
	}
	if recoverErr != nil {
		if barrierContext.Gateway == "" {
			barrierContext, _ = m.updatePreparer.RecoveryBarrierContext(ctx, m.updatePaths)
		}
		if err := m.installRecoveryBarrier(ctx, barrierContext); err != nil {
			return err
		}
		m.needsAttention(DesiredOn, "update_recovery_metadata_failed")
		return newUpdateError("update_recovery_metadata_failed")
	}

	switch transaction.Phase {
	case PhasePrepared:
		if transaction.BarrierInstallIntent {
			return m.recoverPreparedBarrierIntent(ctx, transaction, prepared, barrierContext)
		}
		if prepared.RequiredGuardianProtocol() > m.guardianProtocol {
			if err := prepared.Commit(); err != nil {
				return m.failCleanupOnlyRecovery(ctx, barrierContext, "update_cleanup_failed")
			}
			if err := m.updates.ClearTransaction(); err != nil {
				return m.failCleanupOnlyRecovery(ctx, barrierContext, "update_journal_cleanup_failed")
			}
			m.coreVersion = transaction.FromVersion
			return nil
		}
		if err := prepared.Commit(); err != nil {
			return m.failCleanupOnlyRecovery(ctx, barrierContext, "update_cleanup_failed")
		}
		if err := m.updates.ClearTransaction(); err != nil {
			return m.failCleanupOnlyRecovery(ctx, barrierContext, "update_journal_cleanup_failed")
		}
		m.coreVersion = transaction.FromVersion
		return nil
	case PhaseBarrierActive, PhaseActivating, PhaseRollingBack:
		if err := m.installRecoveryBarrier(ctx, barrierContext); err != nil {
			return err
		}
		if err := m.barrier.ReassertBypass(ctx, barrierContext); err != nil {
			m.needsAttention(DesiredOn, "barrier_reassert_failed")
			return newUpdateError("barrier_reassert_failed")
		}
		if prepared.RequiredGuardianProtocol() > m.guardianProtocol {
			m.needsAttention(DesiredOn, "guardian_protocol_unsupported")
			return newUpdateError("guardian_protocol_unsupported")
		}
		return m.recoverUpdateRollback(ctx, transaction, prepared, barrierContext)
	case PhaseCommitted:
		if err := m.installRecoveryBarrier(ctx, barrierContext); err != nil {
			return err
		}
		if err := m.barrier.ReassertBypass(ctx, barrierContext); err != nil {
			m.needsAttention(DesiredOn, "barrier_reassert_failed")
			return newUpdateError("barrier_reassert_failed")
		}
		if prepared.RequiredGuardianProtocol() > m.guardianProtocol {
			m.needsAttention(DesiredOn, "guardian_protocol_unsupported")
			return newUpdateError("guardian_protocol_unsupported")
		}
		return m.recoverCommittedUpdate(ctx, transaction, prepared, barrierContext)
	case PhaseRolledBack:
		if err := m.installRecoveryBarrier(ctx, barrierContext); err != nil {
			return err
		}
		if err := m.barrier.ReassertBypass(ctx, barrierContext); err != nil {
			m.needsAttention(DesiredOn, "barrier_reassert_failed")
			return newUpdateError("barrier_reassert_failed")
		}
		return m.recoverTerminalUpdate(ctx, transaction, prepared, barrierContext, transaction.FromVersion)
	case PhaseNeedsAttention:
		if err := m.installRecoveryBarrier(ctx, barrierContext); err != nil {
			return err
		}
		m.needsAttention(DesiredOn, safeUpdateCode(transaction.LastError))
		return newUpdateError("update_needs_attention")
	default:
		return m.failMalformedUpdateRecovery(ctx, barrierContext)
	}
}

func (m *Manager) failCleanupOnlyRecovery(ctx context.Context, barrierContext BarrierContext, code string) error {
	if err := m.installRecoveryBarrier(ctx, barrierContext); err != nil {
		return err
	}
	m.needsAttention(DesiredOn, code)
	return newUpdateError(code)
}

func (m *Manager) recoverPreparedBarrierIntent(ctx context.Context, transaction *Transaction, prepared PreparedUpdate, barrierContext BarrierContext) error {
	if err := m.installRecoveryBarrier(ctx, barrierContext); err != nil {
		return err
	}
	if err := m.barrier.ReassertBypass(ctx, barrierContext); err != nil {
		return m.failRecoveredUpdate(transaction, "barrier_reassert_failed")
	}
	process, state, err := m.adoptOrStartRecoveredCore(ctx, transaction.FromVersion)
	if err != nil {
		return m.failRecoveredUpdate(transaction, "previous_core_health_failed")
	}
	if err := m.acceptHealthy(ctx, process, state, false); err != nil {
		return m.failRecoveredUpdate(transaction, "previous_core_accept_failed")
	}
	m.coreVersion = transaction.FromVersion
	transaction.BarrierInstallIntent = false
	if err := m.saveUpdatePhase(transaction, PhaseRolledBack, "barrier_install_recovered"); err != nil {
		return m.failRecoveredUpdate(transaction, "update_journal_failed")
	}
	if err := m.releaseBarrierToCore(ctx, barrierContext); err != nil {
		m.needsAttention(DesiredOn, "barrier_remove_failed")
		return newUpdateError("barrier_remove_failed")
	}
	m.setStatus(Status{
		SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseRolledBack,
		CorePID: process.PID, CoreVersion: transaction.FromVersion,
		Protection: ProtectionProtected, LastError: "barrier_install_recovered",
	})
	return m.finishUpdate(transaction, prepared, PhaseRolledBack)
}

func (m *Manager) matchingTerminalReceipt(transaction *Transaction) bool {
	if transaction.Phase != PhaseCommitted && transaction.Phase != PhaseRolledBack {
		return false
	}
	receipt, err := m.updates.LoadReceipt()
	if err != nil || receipt == nil || !receiptMatchesTransaction(*receipt, *transaction) {
		return false
	}
	return true
}

func (m *Manager) clearReceiptBackedTransaction(transaction *Transaction) error {
	if err := m.updates.ClearTransaction(); err != nil {
		return newUpdateError("update_journal_cleanup_failed")
	}
	if transaction.Phase == PhaseCommitted {
		m.coreVersion = transaction.ToVersion
	} else {
		m.coreVersion = transaction.FromVersion
	}
	return nil
}

func receiptMatchesTransaction(receipt Receipt, transaction Transaction) bool {
	return receipt.TransactionID == transaction.ID &&
		receipt.FromVersion == transaction.FromVersion &&
		receipt.ToVersion == transaction.ToVersion &&
		receipt.AssetDigest == transaction.AssetDigest &&
		receipt.Outcome == transaction.Phase &&
		!receipt.CompletedAt.Before(transaction.UpdatedAt)
}

func updateTransactionStagingGone(transaction Transaction, paths Paths) bool {
	transactionStaging := filepath.Join(paths.Staging, transaction.ID)
	if filepath.Dir(transactionStaging) != paths.Staging || filepath.Base(transactionStaging) != transaction.ID {
		return false
	}
	_, err := os.Lstat(transactionStaging)
	return errors.Is(err, os.ErrNotExist)
}

func validPersistedUpdate(transaction Transaction, paths Paths) bool {
	if !updateTransactionIDPattern.MatchString(transaction.ID) ||
		!updateVersionPattern.MatchString(transaction.FromVersion) ||
		!updateVersionPattern.MatchString(transaction.ToVersion) ||
		len(transaction.AssetDigest) != sha256.Size*2 || transaction.AssetDigest != strings.ToLower(transaction.AssetDigest) ||
		transaction.SnapshotPath != filepath.Join(paths.Snapshots, transaction.ID) ||
		transaction.StartedAt.IsZero() || transaction.UpdatedAt.IsZero() || transaction.UpdatedAt.Before(transaction.StartedAt) ||
		(transaction.LastError != "" && !safeLastErrorPattern.MatchString(transaction.LastError)) {
		return false
	}
	_, err := hex.DecodeString(transaction.AssetDigest)
	return err == nil
}

func (m *Manager) failMalformedUpdateRecovery(ctx context.Context, barrierContext BarrierContext) error {
	if _, _, _, err := PlanBarrier(barrierContext); err != nil {
		barrierContext = blockOnlyRecoveryContext(barrierContext)
	}
	if err := m.installBarrier(ctx, barrierContext); err != nil {
		m.needsAttention(DesiredOn, "update_journal_malformed")
		return newUpdateError("update_journal_malformed")
	}
	m.needsAttention(DesiredOn, "update_journal_malformed")
	return newUpdateError("update_journal_malformed")
}

func (m *Manager) installRecoveryBarrier(ctx context.Context, barrierContext BarrierContext) error {
	if _, _, _, err := PlanBarrier(barrierContext); err != nil {
		barrierContext = blockOnlyRecoveryContext(barrierContext)
	}
	if err := m.installBarrier(ctx, barrierContext); err != nil {
		m.needsAttention(DesiredOn, "barrier_install_failed")
		return newUpdateError("barrier_install_failed")
	}
	m.setStatus(Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseBarrierActive, Protection: ProtectionBlocked})
	return nil
}

func blockOnlyRecoveryContext(barrierContext BarrierContext) BarrierContext {
	barrierContext.ServerBypass = nil
	barrierContext.BlockIPv6 = true
	barrierContext.blockOnly = true
	return barrierContext
}

func (m *Manager) recoverUpdateRollback(ctx context.Context, transaction *Transaction, prepared PreparedUpdate, barrierContext BarrierContext) error {
	existing, err := m.runner.Existing(ctx)
	if err != nil {
		return m.failRecoveredUpdate(transaction, "core_state_read_failed")
	}
	if existing.PID != 0 {
		if err := m.runner.Verify(existing); err != nil {
			return m.failRecoveredUpdate(transaction, "core_identity_unverified")
		}
		if err := m.runner.Stop(ctx, existing); err != nil {
			return m.failRecoveredUpdate(transaction, "core_stop_failed")
		}
	}
	if err := m.saveUpdatePhase(transaction, PhaseRollingBack, "update_recovered"); err != nil {
		return m.failRecoveredUpdate(transaction, "update_journal_failed")
	}
	if err := prepared.Restore(); err != nil {
		return m.failRecoveredUpdate(transaction, "update_restore_failed")
	}
	if err := m.barrier.ReassertBypass(ctx, barrierContext); err != nil {
		return m.failRecoveredUpdate(transaction, "barrier_reassert_failed")
	}
	m.coreVersion = transaction.FromVersion
	process, state, err := m.startUpdateCore(ctx, transaction.FromVersion)
	if err != nil {
		return m.failRecoveredUpdate(transaction, "previous_core_health_failed")
	}
	if err := m.acceptHealthy(ctx, process, state, false); err != nil {
		return m.failRecoveredUpdate(transaction, "previous_core_accept_failed")
	}
	if err := m.saveUpdatePhase(transaction, PhaseRolledBack, "update_recovered"); err != nil {
		return m.failRecoveredUpdate(transaction, "update_journal_failed")
	}
	if err := m.releaseBarrierToCore(ctx, barrierContext); err != nil {
		m.needsAttention(DesiredOn, "barrier_remove_failed")
		return newUpdateError("barrier_remove_failed")
	}
	m.setStatus(Status{
		SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseRolledBack,
		CorePID: process.PID, CoreVersion: transaction.FromVersion,
		Protection: ProtectionProtected, LastError: "update_recovered",
	})
	if err := m.finishUpdate(transaction, prepared, PhaseRolledBack); err != nil {
		return err
	}
	return nil
}

func (m *Manager) recoverCommittedUpdate(ctx context.Context, transaction *Transaction, prepared PreparedUpdate, barrierContext BarrierContext) error {
	return m.recoverTerminalUpdate(ctx, transaction, prepared, barrierContext, transaction.ToVersion)
}

func (m *Manager) recoverTerminalUpdate(ctx context.Context, transaction *Transaction, prepared PreparedUpdate, barrierContext BarrierContext, version string) error {
	process, state, err := m.adoptOrStartRecoveredCore(ctx, version)
	if err != nil {
		return m.failRecoveredUpdate(transaction, "recovered_core_health_failed")
	}
	if err := m.acceptHealthy(ctx, process, state, false); err != nil {
		return m.failRecoveredUpdate(transaction, "recovered_core_accept_failed")
	}
	m.coreVersion = version
	if err := m.releaseBarrierToCore(ctx, barrierContext); err != nil {
		m.needsAttention(DesiredOn, "barrier_remove_failed")
		return newUpdateError("barrier_remove_failed")
	}
	m.setStatus(Status{
		SchemaVersion: 1, Desired: DesiredOn, Phase: transaction.Phase,
		CorePID: process.PID, CoreVersion: version, Protection: ProtectionProtected,
		LastError: transaction.LastError,
	})
	return m.finishUpdate(transaction, prepared, transaction.Phase)
}

func (m *Manager) adoptOrStartRecoveredCore(ctx context.Context, version string) (Process, supervisor.RuntimeState, error) {
	existing, err := m.runner.Existing(ctx)
	if err != nil {
		return Process{}, supervisor.RuntimeState{}, err
	}
	if existing.PID == 0 {
		return m.startUpdateCore(ctx, version)
	}
	if err := m.runner.Verify(existing); err != nil {
		return Process{}, supervisor.RuntimeState{}, err
	}
	state, err := m.health.Wait(ctx, HealthTarget{Version: version, PID: existing.PID})
	if err != nil || state.PID != existing.PID || state.Version != version {
		return Process{}, supervisor.RuntimeState{}, newUpdateError("recovered_core_health_failed")
	}
	return existing, state, nil
}

func (m *Manager) failRecoveredUpdate(transaction *Transaction, code string) error {
	code = safeUpdateCode(code)
	_ = m.saveUpdatePhase(transaction, PhaseNeedsAttention, code)
	m.needsAttention(DesiredOn, code)
	return newUpdateError(code)
}

func (m *Manager) updatePreparedLocked(ctx context.Context, request UpdateRequest, prepared PreparedUpdate, barrierContext BarrierContext) (UpdateResult, error) {
	now := time.Now().UTC()
	transaction := Transaction{
		ID: request.TransactionID, FromVersion: request.FromVersion, ToVersion: request.ToVersion,
		Phase: PhasePrepared, AssetDigest: request.AssetSHA256, SnapshotPath: prepared.SnapshotPath(),
		StartedAt: now, UpdatedAt: now,
	}
	if err := m.updates.SaveTransaction(transaction); err != nil {
		_ = prepared.Commit()
		return m.updateResult(request, m.Status().Phase, false, false), newUpdateError("update_journal_failed")
	}
	transaction.BarrierInstallIntent = true
	if err := m.updates.SaveTransaction(transaction); err != nil {
		_ = prepared.Commit()
		return m.updateResult(request, m.Status().Phase, false, false), newUpdateError("update_journal_failed")
	}
	if err := m.installBarrier(ctx, barrierContext); err != nil {
		return m.failUpdate(transaction, request, "barrier_install_failed", false, false)
	}
	transaction.BarrierInstallIntent = false
	if err := m.saveUpdatePhase(&transaction, PhaseBarrierActive, ""); err != nil {
		m.needsAttention(DesiredOn, "update_journal_failed")
		return m.updateResult(request, PhaseNeedsAttention, false, false), newUpdateError("update_journal_failed")
	}
	m.setStatus(Status{
		SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseBarrierActive,
		CorePID: m.current.PID, CoreVersion: request.FromVersion, Protection: ProtectionBlocked,
	})

	old := m.current
	if err := m.runner.Stop(ctx, old); err != nil {
		return m.failUpdate(transaction, request, "old_core_stop_failed", false, false)
	}
	m.current = Process{}
	m.runtime = supervisor.RuntimeState{}
	if err := m.barrier.ReassertBypass(ctx, barrierContext); err != nil {
		return m.rollbackUpdate(ctx, &transaction, request, prepared, barrierContext, "barrier_reassert_failed")
	}
	if err := m.saveUpdatePhase(&transaction, PhaseActivating, ""); err != nil {
		return m.rollbackUpdate(ctx, &transaction, request, prepared, barrierContext, "update_journal_failed")
	}
	if err := prepared.Activate(); err != nil {
		return m.rollbackUpdate(ctx, &transaction, request, prepared, barrierContext, "update_activate_failed")
	}

	process, runtimeState, startErr := m.startUpdateCore(ctx, request.ToVersion)
	if startErr != nil {
		if isUpdateErrorCode(startErr, "core_ownership_uncertain") {
			return m.failUpdate(transaction, request, "core_ownership_uncertain", false, false)
		}
		return m.rollbackUpdate(ctx, &transaction, request, prepared, barrierContext, startErr.Error())
	}
	if err := m.acceptHealthy(ctx, process, runtimeState, false); err != nil {
		return m.rollbackUpdate(ctx, &transaction, request, prepared, barrierContext, "new_core_accept_failed")
	}
	m.coreVersion = request.ToVersion
	if err := m.saveUpdatePhase(&transaction, PhaseCommitted, ""); err != nil {
		m.needsAttention(DesiredOn, "update_journal_failed")
		return m.updateResult(request, PhaseNeedsAttention, true, false), newUpdateError("update_journal_failed")
	}
	if err := m.releaseBarrierToCore(ctx, barrierContext); err != nil {
		m.needsAttention(DesiredOn, "barrier_remove_failed")
		return m.updateResult(request, PhaseNeedsAttention, true, false), newUpdateError("barrier_remove_failed")
	}
	m.setStatus(Status{
		SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted,
		CorePID: process.PID, CoreVersion: request.ToVersion, Protection: ProtectionProtected,
	})
	result := m.updateResult(request, PhaseCommitted, true, false)
	if err := m.finishUpdate(&transaction, prepared, PhaseCommitted); err != nil {
		return result, err
	}
	return result, nil
}

func (m *Manager) startUpdateCore(ctx context.Context, version string) (Process, supervisor.RuntimeState, error) {
	operationCtx, cancelOperation, err := m.reserveCleanup(ctx)
	if err != nil {
		return Process{}, supervisor.RuntimeState{}, newUpdateError("new_core_start_failed")
	}
	defer cancelOperation()
	process, err := m.runner.Start(operationCtx, m.coreStartOptions())
	if err != nil {
		if errors.Is(err, ErrProcessOwnershipUncertain) {
			if uncertain, ok := uncertainProcess(err); ok {
				m.retainUncertain(uncertain)
			}
			return Process{}, supervisor.RuntimeState{}, newUpdateError("core_ownership_uncertain")
		}
		return Process{}, supervisor.RuntimeState{}, newUpdateError("new_core_start_failed")
	}
	if err := m.runner.Verify(process); err != nil {
		if stopErr := m.cleanupStartedCore(ctx, process); stopErr != nil {
			m.retainUncertain(Process{
				PID: process.PID, Executable: process.Executable, UID: process.UID,
				Generation: process.Generation, Exit: process.Exit, Resolution: process.Resolution,
			})
			return Process{}, supervisor.RuntimeState{}, newUpdateError("core_ownership_uncertain")
		}
		return Process{}, supervisor.RuntimeState{}, newUpdateError("new_core_identity_failed")
	}
	state, err := m.health.Wait(operationCtx, HealthTarget{Version: version, PID: process.PID})
	if err != nil || state.PID != process.PID || state.Version != version {
		if stopErr := m.cleanupStartedCore(ctx, process); stopErr != nil {
			m.retainUncertain(Process{
				PID: process.PID, Executable: process.Executable, UID: process.UID,
				Generation: process.Generation, Exit: process.Exit, Resolution: process.Resolution,
			})
			return Process{}, supervisor.RuntimeState{}, newUpdateError("core_ownership_uncertain")
		}
		return Process{}, supervisor.RuntimeState{}, newUpdateError("new_core_health_failed")
	}
	return process, state, nil
}

func (m *Manager) rollbackUpdate(
	ctx context.Context,
	transaction *Transaction,
	request UpdateRequest,
	prepared PreparedUpdate,
	barrierContext BarrierContext,
	cause string,
) (UpdateResult, error) {
	if err := m.saveUpdatePhase(transaction, PhaseRollingBack, safeUpdateCode(cause)); err != nil {
		return m.failUpdate(*transaction, request, "update_journal_failed", false, false)
	}
	if err := prepared.Restore(); err != nil {
		return m.failUpdate(*transaction, request, "update_restore_failed", false, false)
	}
	if err := m.barrier.ReassertBypass(ctx, barrierContext); err != nil {
		return m.failUpdate(*transaction, request, "barrier_reassert_failed", false, false)
	}
	m.coreVersion = request.FromVersion
	process, state, err := m.startUpdateCore(ctx, request.FromVersion)
	if err != nil {
		return m.failUpdate(*transaction, request, "previous_core_health_failed", false, false)
	}
	if err := m.acceptHealthy(ctx, process, state, false); err != nil {
		return m.failUpdate(*transaction, request, "previous_core_accept_failed", false, false)
	}
	if err := m.saveUpdatePhase(transaction, PhaseRolledBack, safeUpdateCode(cause)); err != nil {
		return m.failUpdate(*transaction, request, "update_journal_failed", false, false)
	}
	if err := m.releaseBarrierToCore(ctx, barrierContext); err != nil {
		m.needsAttention(DesiredOn, "barrier_remove_failed")
		return m.updateResult(request, PhaseNeedsAttention, false, true), newUpdateError("barrier_remove_failed")
	}
	m.setStatus(Status{
		SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseRolledBack,
		CorePID: process.PID, CoreVersion: request.FromVersion,
		Protection: ProtectionProtected, LastError: safeUpdateCode(cause),
	})
	result := m.updateResult(request, PhaseRolledBack, false, true)
	if err := m.finishUpdate(transaction, prepared, PhaseRolledBack); err != nil {
		return result, err
	}
	return result, nil
}

func (m *Manager) failUpdate(transaction Transaction, request UpdateRequest, code string, activated, rolledBack bool) (UpdateResult, error) {
	code = safeUpdateCode(code)
	_ = m.saveUpdatePhase(&transaction, PhaseNeedsAttention, code)
	m.needsAttention(DesiredOn, code)
	return m.updateResult(request, PhaseNeedsAttention, activated, rolledBack), newUpdateError(code)
}

func (m *Manager) saveUpdatePhase(transaction *Transaction, phase Phase, lastError string) error {
	transaction.Phase = phase
	transaction.UpdatedAt = time.Now().UTC()
	transaction.LastError = safeUpdateCode(lastError)
	return m.updates.SaveTransaction(*transaction)
}

func (m *Manager) finishUpdate(transaction *Transaction, prepared PreparedUpdate, outcome Phase) error {
	receipt := Receipt{
		TransactionID: transaction.ID, FromVersion: transaction.FromVersion, ToVersion: transaction.ToVersion,
		AssetDigest: transaction.AssetDigest, Outcome: outcome, CompletedAt: time.Now().UTC(),
	}
	if err := m.updates.SaveReceipt(receipt); err != nil {
		return newUpdateError("update_receipt_failed")
	}
	if err := prepared.Commit(); err != nil {
		return newUpdateError("update_cleanup_failed")
	}
	if err := m.updates.ClearTransaction(); err != nil {
		return newUpdateError("update_journal_cleanup_failed")
	}
	return nil
}

func (m *Manager) updateResult(request UpdateRequest, phase Phase, activated, rolledBack bool) UpdateResult {
	return UpdateResult{
		FromVersion: request.FromVersion, ToVersion: request.ToVersion, Phase: phase,
		CoreActivated: activated, RolledBack: rolledBack, ProtectionState: m.Status().Protection,
	}
}

func safeUpdateCode(code string) string {
	if code == "" {
		return ""
	}
	if safeLastErrorPattern.MatchString(code) {
		return code
	}
	return "update_failed"
}

func isUpdateErrorCode(err error, code string) bool {
	var updateErr updateError
	return errors.As(err, &updateErr) && updateErr.code == code
}

func readVerifiedUpdatePackage(request UpdateRequest, paths Paths) ([]byte, error) {
	transactionRoot := filepath.Join(paths.Staging, request.TransactionID)
	relative, err := filepath.Rel(transactionRoot, request.PackagePath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, newUpdateError("update_package_path_invalid")
	}
	if err := requirePrivateOwnedDirectory(paths.Staging); err != nil {
		return nil, newUpdateError("update_package_path_invalid")
	}
	if err := rejectUpdatePathSymlinks(transactionRoot, relative); err != nil {
		return nil, newUpdateError("update_package_path_invalid")
	}
	file, err := os.Open(request.PackagePath)
	if err != nil {
		return nil, newUpdateError("update_package_read_failed")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o077 != 0 || !ownedByGuardian(opened) {
		return nil, newUpdateError("update_package_path_invalid")
	}
	current, err := os.Lstat(request.PackagePath)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return nil, newUpdateError("update_package_path_invalid")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxUpdatePackageBytes+1))
	if err != nil || len(data) > maxUpdatePackageBytes {
		return nil, newUpdateError("update_package_read_failed")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != request.AssetSHA256 {
		return nil, newUpdateError("update_package_digest_mismatch")
	}
	return data, nil
}

func rejectUpdatePathSymlinks(root, relative string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || rootInfo.Mode().Perm()&0o077 != 0 || !ownedByGuardian(rootInfo) {
		return errors.New("invalid transaction staging directory")
	}
	current := root
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errors.New("invalid package path")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || !ownedByGuardian(info) {
			return errors.New("symlinked package path")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return errors.New("non-directory package path")
		}
	}
	return nil
}

func requirePrivateOwnedDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || !ownedByGuardian(info) {
		return errors.New("invalid Guardian-owned directory")
	}
	return nil
}

func ownedByGuardian(info os.FileInfo) bool {
	uid, ok := fileOwnerUID(info)
	return ok && uid == uint32(os.Geteuid())
}

type macOSUpdatePreparer struct{}

func (macOSUpdatePreparer) Prepare(_ context.Context, request UpdateRequest, packageData []byte, paths Paths) (PreparedUpdate, error) {
	requiredProtocol, err := requiredGuardianProtocol(packageData, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	payload, err := updatepkg.ExtractMacOSPackage(packageData, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	transactionRoot := filepath.Join(paths.Staging, request.TransactionID)
	if err := os.RemoveAll(transactionRoot); err != nil {
		return nil, err
	}
	appPath := request.AppPath
	if appPath == "" {
		appPath = "/Applications/Bx.app"
	}
	prepared, err := updatepkg.PrepareMacOSInstall(updatepkg.InstallOptions{
		CLIDestination: install.BinPath,
		AppDestination: appPath,
		AppUID:         request.AppUID,
		AppGID:         request.AppGID,
		SnapshotDir:    filepath.Join(paths.Snapshots, request.TransactionID),
		StagingDir:     transactionRoot,
	}, payload)
	if err != nil {
		return nil, err
	}
	descriptor, err := buildUpdateRecoveryDescriptorTemplate(request, paths, appPath, requiredProtocol)
	if err != nil {
		_ = prepared.Commit()
		return nil, err
	}
	return &preparedMacOSUpdate{
		PreparedInstall:  prepared,
		requiredProtocol: requiredProtocol,
		paths:            paths,
		descriptor:       descriptor,
	}, nil
}

func (macOSUpdatePreparer) Recover(_ context.Context, transaction Transaction, paths Paths) (PreparedUpdate, BarrierContext, error) {
	return readRecoveredMacOSUpdate(transaction, paths, productionUpdatePaths(paths))
}

func (macOSUpdatePreparer) RecoveryBarrierContext(_ context.Context, paths Paths) (BarrierContext, error) {
	entries, err := os.ReadDir(paths.Staging)
	if err != nil {
		return BarrierContext{}, newUpdateError("update_recovery_metadata_failed")
	}
	var found *updateRecoveryDescriptor
	for _, entry := range entries {
		if !entry.IsDir() || !updateTransactionIDPattern.MatchString(entry.Name()) {
			continue
		}
		descriptor, err := readUpdateRecoveryDescriptor(filepath.Join(paths.Staging, entry.Name(), updateRecoveryDescriptorName))
		if err != nil {
			return BarrierContext{}, newUpdateError("update_recovery_metadata_ambiguous")
		}
		if err := validateUpdateRecoveryDescriptor(descriptor, paths, productionUpdatePaths(paths)); err != nil {
			return BarrierContext{}, newUpdateError("update_recovery_metadata_ambiguous")
		}
		if found != nil {
			return BarrierContext{}, newUpdateError("update_recovery_metadata_ambiguous")
		}
		copy := descriptor
		found = &copy
	}
	if found == nil {
		return BarrierContext{}, newUpdateError("update_recovery_metadata_failed")
	}
	return cloneBarrierContext(found.BarrierContext), nil
}

type preparedMacOSUpdate struct {
	*updatepkg.PreparedInstall
	requiredProtocol int
	paths            Paths
	descriptor       updateRecoveryDescriptor
	barrierBound     bool
}

func (p *preparedMacOSUpdate) RequiredGuardianProtocol() int { return p.requiredProtocol }

func (p *preparedMacOSUpdate) BindBarrierContext(barrierContext BarrierContext) error {
	if p.barrierBound {
		return newUpdateError("update_recovery_metadata_failed")
	}
	descriptor := p.descriptor
	descriptor.BarrierContext = cloneBarrierContext(barrierContext)
	if err := validateUpdateRecoveryDescriptor(descriptor, p.paths, true); err != nil {
		return err
	}
	if err := writeUpdateRecoveryDescriptor(descriptor); err != nil {
		return err
	}
	p.descriptor = descriptor
	p.barrierBound = true
	return nil
}

type artifactFingerprint struct {
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
}

type updateRecoveryDescriptor struct {
	SchemaVersion    int                 `json:"schema_version,omitempty"`
	GuardianProtocol int                 `json:"guardian_protocol,omitempty"`
	TransactionID    string              `json:"transaction_id"`
	FromVersion      string              `json:"from_version"`
	ToVersion        string              `json:"to_version"`
	AssetDigest      string              `json:"asset_digest"`
	CLIPath          string              `json:"cli_path"`
	AppPath          string              `json:"app_path"`
	AppUID           int                 `json:"app_uid"`
	AppGID           int                 `json:"app_gid"`
	SnapshotPath     string              `json:"snapshot_path"`
	StagingPath      string              `json:"staging_path"`
	BarrierContext   BarrierContext      `json:"barrier_context"`
	HadCLI           bool                `json:"had_cli"`
	HadApp           bool                `json:"had_app"`
	OldCLI           artifactFingerprint `json:"old_cli"`
	NewCLI           artifactFingerprint `json:"new_cli"`
	OldApp           artifactFingerprint `json:"old_app"`
	NewApp           artifactFingerprint `json:"new_app"`
}

func buildUpdateRecoveryDescriptorTemplate(request UpdateRequest, paths Paths, appPath string, protocol int) (updateRecoveryDescriptor, error) {
	transactionID := request.TransactionID
	snapshotPath := filepath.Join(paths.Snapshots, transactionID)
	stagingPath := filepath.Join(paths.Staging, transactionID)
	cliStage := filepath.Join(filepath.Dir(install.BinPath), ".bx-update-"+transactionID)
	appStage := filepath.Join(filepath.Dir(appPath), ".Bx.app.update-"+transactionID)
	descriptor := updateRecoveryDescriptor{
		SchemaVersion: updateRecoveryDescriptorVersion, GuardianProtocol: protocol,
		TransactionID: transactionID, FromVersion: request.FromVersion, ToVersion: request.ToVersion,
		AssetDigest: request.AssetSHA256, CLIPath: install.BinPath, AppPath: appPath,
		AppUID: request.AppUID, AppGID: request.AppGID, SnapshotPath: snapshotPath,
		StagingPath: stagingPath,
	}
	var err error
	if descriptor.OldCLI, descriptor.HadCLI, err = fingerprintOptionalArtifact(filepath.Join(snapshotPath, "bx")); err != nil {
		return updateRecoveryDescriptor{}, newUpdateError("update_recovery_metadata_failed")
	}
	if descriptor.NewCLI, err = fingerprintArtifact(cliStage); err != nil {
		return updateRecoveryDescriptor{}, newUpdateError("update_recovery_metadata_failed")
	}
	if descriptor.OldApp, descriptor.HadApp, err = fingerprintOptionalArtifact(filepath.Join(snapshotPath, "Bx.app")); err != nil {
		return updateRecoveryDescriptor{}, newUpdateError("update_recovery_metadata_failed")
	}
	if descriptor.NewApp, err = fingerprintArtifact(appStage); err != nil {
		return updateRecoveryDescriptor{}, newUpdateError("update_recovery_metadata_failed")
	}
	if err := validateUpdateRecoveryDescriptorStatic(descriptor, paths, true); err != nil {
		return updateRecoveryDescriptor{}, err
	}
	return descriptor, nil
}

func writeUpdateRecoveryDescriptor(descriptor updateRecoveryDescriptor) error {
	if err := os.MkdirAll(descriptor.StagingPath, 0o700); err != nil {
		return newUpdateError("update_recovery_metadata_failed")
	}
	if err := os.Chmod(descriptor.StagingPath, 0o700); err != nil {
		return newUpdateError("update_recovery_metadata_failed")
	}
	if err := writeJSONAtomically(filepath.Join(descriptor.StagingPath, updateRecoveryDescriptorName), descriptor); err != nil {
		return newUpdateError("update_recovery_metadata_failed")
	}
	return nil
}

func readRecoveredMacOSUpdate(transaction Transaction, paths Paths, requireSystemDestinations bool) (PreparedUpdate, BarrierContext, error) {
	descriptorPath := filepath.Join(paths.Staging, transaction.ID, updateRecoveryDescriptorName)
	descriptor, err := readUpdateRecoveryDescriptor(descriptorPath)
	if err != nil {
		return nil, BarrierContext{}, err
	}
	if err := validateUpdateRecoveryDescriptor(descriptor, paths, requireSystemDestinations); err != nil {
		return nil, BarrierContext{}, err
	}
	if descriptor.TransactionID != transaction.ID || descriptor.FromVersion != transaction.FromVersion ||
		descriptor.ToVersion != transaction.ToVersion || descriptor.AssetDigest != transaction.AssetDigest ||
		descriptor.SnapshotPath != transaction.SnapshotPath {
		return nil, BarrierContext{}, newUpdateError("update_recovery_metadata_mismatch")
	}
	prepared := &recoveredMacOSUpdate{descriptor: descriptor}
	return prepared, cloneBarrierContext(descriptor.BarrierContext), nil
}

func readUpdateRecoveryDescriptor(path string) (updateRecoveryDescriptor, error) {
	if err := requirePrivateOwnedDirectory(filepath.Dir(path)); err != nil {
		return updateRecoveryDescriptor{}, newUpdateError("update_recovery_metadata_failed")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 ||
		info.Size() > 64<<10 || !ownedByGuardian(info) {
		return updateRecoveryDescriptor{}, newUpdateError("update_recovery_metadata_failed")
	}
	file, err := os.Open(path)
	if err != nil {
		return updateRecoveryDescriptor{}, newUpdateError("update_recovery_metadata_failed")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return updateRecoveryDescriptor{}, newUpdateError("update_recovery_metadata_failed")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	var descriptor updateRecoveryDescriptor
	if err := decoder.Decode(&descriptor); err != nil {
		return updateRecoveryDescriptor{}, newUpdateError("update_recovery_metadata_failed")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return updateRecoveryDescriptor{}, newUpdateError("update_recovery_metadata_failed")
	}
	if descriptor.SchemaVersion == 0 {
		descriptor.GuardianProtocol = currentGuardianProtocol
	}
	return descriptor, nil
}

func validateUpdateRecoveryDescriptor(descriptor updateRecoveryDescriptor, paths Paths, requireSystemDestinations bool) error {
	if err := validateUpdateRecoveryDescriptorStatic(descriptor, paths, requireSystemDestinations); err != nil {
		return err
	}
	if _, _, _, err := PlanBarrier(descriptor.BarrierContext); err != nil {
		return newUpdateError("update_recovery_metadata_failed")
	}
	return nil
}

func validateUpdateRecoveryDescriptorStatic(descriptor updateRecoveryDescriptor, paths Paths, requireSystemDestinations bool) error {
	if descriptor.SchemaVersion != 0 && descriptor.SchemaVersion != updateRecoveryDescriptorVersion {
		return newUpdateError("update_recovery_version_unsupported")
	}
	if descriptor.GuardianProtocol <= 0 || !updateTransactionIDPattern.MatchString(descriptor.TransactionID) ||
		!updateVersionPattern.MatchString(descriptor.FromVersion) || !updateVersionPattern.MatchString(descriptor.ToVersion) ||
		len(descriptor.AssetDigest) != sha256.Size*2 || descriptor.AppUID < 0 || descriptor.AppGID < 0 {
		return newUpdateError("update_recovery_metadata_failed")
	}
	if _, err := hex.DecodeString(descriptor.AssetDigest); err != nil {
		return newUpdateError("update_recovery_metadata_failed")
	}
	wantSnapshot := filepath.Join(paths.Snapshots, descriptor.TransactionID)
	wantStaging := filepath.Join(paths.Staging, descriptor.TransactionID)
	if descriptor.SnapshotPath != wantSnapshot || descriptor.StagingPath != wantStaging ||
		!filepath.IsAbs(descriptor.CLIPath) || !filepath.IsAbs(descriptor.AppPath) || filepath.Base(descriptor.AppPath) != "Bx.app" {
		return newUpdateError("update_recovery_metadata_failed")
	}
	if requireSystemDestinations && descriptor.CLIPath != install.BinPath {
		return newUpdateError("update_recovery_metadata_failed")
	}
	for _, fingerprint := range []artifactFingerprint{descriptor.NewCLI, descriptor.NewApp} {
		if !fingerprint.valid() {
			return newUpdateError("update_recovery_metadata_failed")
		}
	}
	if descriptor.HadCLI && !descriptor.OldCLI.valid() {
		return newUpdateError("update_recovery_metadata_failed")
	}
	if descriptor.HadApp && !descriptor.OldApp.valid() {
		return newUpdateError("update_recovery_metadata_failed")
	}
	return nil
}

func productionUpdatePaths(paths Paths) bool {
	return paths.Staging == guardianUpdateDirectory+"/staging" && paths.Snapshots == guardianUpdateDirectory+"/snapshots"
}

func (f artifactFingerprint) valid() bool {
	if f.Kind != "file" && f.Kind != "directory" {
		return false
	}
	if len(f.SHA256) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(f.SHA256)
	return err == nil
}

func fingerprintOptionalArtifact(path string) (artifactFingerprint, bool, error) {
	fingerprint, err := fingerprintArtifact(path)
	if errors.Is(err, os.ErrNotExist) {
		return artifactFingerprint{}, false, nil
	}
	return fingerprint, err == nil, err
}

func fingerprintArtifact(path string) (artifactFingerprint, error) {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return artifactFingerprint{}, err
	}
	defer parent.Close()
	return fingerprintArtifactAt(parent, filepath.Base(path))
}

func fingerprintArtifactAt(root *os.Root, name string) (artifactFingerprint, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return artifactFingerprint{}, err
	}
	kind := ""
	switch {
	case info.Mode().IsRegular():
		kind = "file"
	case info.IsDir():
		kind = "directory"
	default:
		return artifactFingerprint{}, newUpdateError("update_artifact_unsafe")
	}
	hash := sha256.New()
	rootFS := root.FS()
	err = fs.WalkDir(rootFS, name, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entryInfo, err := root.Lstat(path)
		if err != nil {
			return err
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 || (!entryInfo.IsDir() && !entryInfo.Mode().IsRegular()) {
			return newUpdateError("update_artifact_unsafe")
		}
		io.WriteString(hash, strings.TrimPrefix(path, name))
		io.WriteString(hash, "\x00"+strconv.FormatUint(uint64(entryInfo.Mode().Perm()), 8)+"\x00")
		if entryInfo.IsDir() {
			io.WriteString(hash, "d\x00")
			return nil
		}
		io.WriteString(hash, "f\x00"+strconv.FormatInt(entryInfo.Size(), 10)+"\x00")
		file, err := root.Open(path)
		if err != nil {
			return err
		}
		opened, statErr := file.Stat()
		if statErr == nil && !os.SameFile(entryInfo, opened) {
			statErr = newUpdateError("update_artifact_changed")
		}
		if statErr == nil {
			_, statErr = io.Copy(hash, file)
		}
		closeErr := file.Close()
		return errors.Join(statErr, closeErr)
	})
	if err != nil {
		return artifactFingerprint{}, err
	}
	return artifactFingerprint{Kind: kind, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

type recoveredMacOSUpdate struct {
	descriptor updateRecoveryDescriptor
}

func (p *recoveredMacOSUpdate) SnapshotPath() string { return p.descriptor.SnapshotPath }
func (p *recoveredMacOSUpdate) RequiredGuardianProtocol() int {
	return p.descriptor.GuardianProtocol
}
func (*recoveredMacOSUpdate) BindBarrierContext(BarrierContext) error {
	return newUpdateError("update_recovery_metadata_failed")
}
func (*recoveredMacOSUpdate) Activate() error { return newUpdateError("update_reactivation_forbidden") }

func (p *recoveredMacOSUpdate) Restore() error {
	if err := p.verifySnapshot(); err != nil {
		return newUpdateError("update_restore_failed")
	}
	if err := p.restoreCLI(); err != nil {
		return newUpdateError("update_restore_failed")
	}
	if err := p.restoreApp(); err != nil {
		return newUpdateError("update_restore_failed")
	}
	return nil
}

func (p *recoveredMacOSUpdate) verifySnapshot() error {
	d := p.descriptor
	if err := requirePrivateOwnedDirectory(d.SnapshotPath); err != nil {
		return err
	}
	snapshotRoot, err := os.OpenRoot(d.SnapshotPath)
	if err != nil {
		return err
	}
	defer snapshotRoot.Close()
	for _, artifact := range []struct {
		name string
		had  bool
		want artifactFingerprint
	}{
		{name: "bx", had: d.HadCLI, want: d.OldCLI},
		{name: "Bx.app", had: d.HadApp, want: d.OldApp},
	} {
		got, exists, err := optionalFingerprintAt(snapshotRoot, artifact.name)
		if err != nil || exists != artifact.had || (artifact.had && got != artifact.want) {
			return newUpdateError("update_snapshot_invalid")
		}
	}
	return nil
}

func (p *recoveredMacOSUpdate) restoreCLI() error {
	d := p.descriptor
	cliRoot, err := os.OpenRoot(filepath.Dir(d.CLIPath))
	if err != nil {
		return err
	}
	defer cliRoot.Close()
	current, exists, err := optionalFingerprintAt(cliRoot, filepath.Base(d.CLIPath))
	if err != nil {
		return err
	}
	if exists && current != d.NewCLI && (!d.HadCLI || current != d.OldCLI) {
		return newUpdateError("update_artifact_substituted")
	}
	discardName := ".bx-update-" + d.TransactionID + ".discard-recovery"
	if d.HadCLI && exists && current == d.OldCLI {
		return nil
	}
	if exists {
		discardExists, err := optionalKnownFingerprintAt(cliRoot, discardName, d.NewCLI)
		if err != nil {
			return err
		}
		if discardExists {
			return newUpdateError("update_recovery_residue_present")
		}
		if err := moveKnownAside(cliRoot, filepath.Base(d.CLIPath), discardName, d.NewCLI); err != nil {
			return err
		}
		exists = false
	}
	if !d.HadCLI {
		return nil
	}
	restoreNames := []string{
		".bx-update-" + d.TransactionID + ".restore-recovery",
		".bx-update-" + d.TransactionID + ".restore",
	}
	for _, restoreName := range restoreNames {
		restoreExists, err := optionalKnownFingerprintAt(cliRoot, restoreName, d.OldCLI)
		if err != nil {
			return err
		}
		if !restoreExists {
			continue
		}
		if err := cliRoot.Rename(restoreName, filepath.Base(d.CLIPath)); err != nil {
			return err
		}
		return requireKnownFingerprintAt(cliRoot, filepath.Base(d.CLIPath), d.OldCLI)
	}
	snapshotRoot, err := os.OpenRoot(d.SnapshotPath)
	if err != nil {
		return err
	}
	defer snapshotRoot.Close()
	restoreName := restoreNames[0]
	if err := copyFileBetweenRoots(snapshotRoot, "bx", cliRoot, restoreName); err != nil {
		return err
	}
	if err := cliRoot.Rename(restoreName, filepath.Base(d.CLIPath)); err != nil {
		return err
	}
	return requireKnownFingerprintAt(cliRoot, filepath.Base(d.CLIPath), d.OldCLI)
}

func (p *recoveredMacOSUpdate) restoreApp() error {
	d := p.descriptor
	appRoot, err := os.OpenRoot(filepath.Dir(d.AppPath))
	if err != nil {
		return err
	}
	defer appRoot.Close()
	appName := filepath.Base(d.AppPath)
	current, exists, err := optionalFingerprintAt(appRoot, appName)
	if err != nil {
		return err
	}
	if exists && current != d.NewApp && (!d.HadApp || current != d.OldApp) {
		return newUpdateError("update_artifact_substituted")
	}
	discardName := ".Bx.app.discard-" + d.TransactionID
	if d.HadApp && exists && current == d.OldApp {
		return nil
	}
	if exists {
		discardExists, err := optionalKnownFingerprintAt(appRoot, discardName, d.NewApp)
		if err != nil {
			return err
		}
		if discardExists {
			return newUpdateError("update_recovery_residue_present")
		}
		if err := moveKnownAside(appRoot, appName, discardName, d.NewApp); err != nil {
			return err
		}
		exists = false
	}
	if !d.HadApp {
		return nil
	}
	restoreNames := []string{
		".Bx.app.restore-" + d.TransactionID,
		".Bx.app.previous-" + d.TransactionID,
	}
	for _, restoreName := range restoreNames {
		restoreExists, err := optionalKnownFingerprintAt(appRoot, restoreName, d.OldApp)
		if err != nil {
			return err
		}
		if !restoreExists {
			continue
		}
		if err := appRoot.Rename(restoreName, appName); err != nil {
			return err
		}
		return requireKnownFingerprintAt(appRoot, appName, d.OldApp)
	}
	snapshotRoot, err := os.OpenRoot(d.SnapshotPath)
	if err != nil {
		return err
	}
	defer snapshotRoot.Close()
	restoreName := restoreNames[0]
	if err := copyTreeBetweenRoots(snapshotRoot, "Bx.app", appRoot, restoreName, d.AppUID, d.AppGID); err != nil {
		return err
	}
	if err := appRoot.Rename(restoreName, appName); err != nil {
		return err
	}
	return requireKnownFingerprintAt(appRoot, appName, d.OldApp)
}

func (p *recoveredMacOSUpdate) Commit() error {
	d := p.descriptor
	cliRoot, err := os.OpenRoot(filepath.Dir(d.CLIPath))
	if err != nil {
		return newUpdateError("update_cleanup_failed")
	}
	defer cliRoot.Close()
	for _, item := range []struct {
		name string
		want artifactFingerprint
	}{
		{".bx-update-" + d.TransactionID, d.NewCLI},
		{".bx-update-" + d.TransactionID + ".restore", d.OldCLI},
		{".bx-update-" + d.TransactionID + ".restore-recovery", d.OldCLI},
		{".bx-update-" + d.TransactionID + ".discard-recovery", d.NewCLI},
	} {
		if err := removeKnownAt(cliRoot, item.name, item.want); err != nil {
			return newUpdateError("update_cleanup_failed")
		}
	}
	appRoot, err := os.OpenRoot(filepath.Dir(d.AppPath))
	if err != nil {
		return newUpdateError("update_cleanup_failed")
	}
	defer appRoot.Close()
	for _, item := range []struct {
		name string
		want artifactFingerprint
	}{
		{".Bx.app.update-" + d.TransactionID, d.NewApp},
		{".Bx.app.previous-" + d.TransactionID, d.OldApp},
		{".Bx.app.restore-" + d.TransactionID, d.OldApp},
		{".Bx.app.discard-" + d.TransactionID, d.NewApp},
	} {
		if err := removeKnownAt(appRoot, item.name, item.want); err != nil {
			return newUpdateError("update_cleanup_failed")
		}
	}
	if err := removeRootOwnedTransactionDirectory(d.SnapshotPath, filepath.Dir(d.SnapshotPath), d.TransactionID); err != nil {
		return newUpdateError("update_cleanup_failed")
	}
	if err := removeRootOwnedTransactionDirectory(d.StagingPath, filepath.Dir(d.StagingPath), d.TransactionID); err != nil {
		return newUpdateError("update_cleanup_failed")
	}
	return nil
}

func optionalFingerprintAt(root *os.Root, name string) (artifactFingerprint, bool, error) {
	fingerprint, err := fingerprintArtifactAt(root, name)
	if errors.Is(err, os.ErrNotExist) {
		return artifactFingerprint{}, false, nil
	}
	return fingerprint, err == nil, err
}

func optionalKnownFingerprintAt(root *os.Root, name string, want artifactFingerprint) (bool, error) {
	got, exists, err := optionalFingerprintAt(root, name)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	if !want.valid() || got != want {
		return false, newUpdateError("update_artifact_substituted")
	}
	return true, nil
}

func requireKnownFingerprintAt(root *os.Root, name string, want artifactFingerprint) error {
	exists, err := optionalKnownFingerprintAt(root, name, want)
	if err != nil {
		return err
	}
	if !exists {
		return newUpdateError("update_artifact_missing")
	}
	return nil
}

func moveKnownAside(root *os.Root, name, discard string, want artifactFingerprint) error {
	if _, err := root.Lstat(discard); err == nil {
		return newUpdateError("update_recovery_residue_present")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := root.Rename(name, discard); err != nil {
		return err
	}
	moved, err := fingerprintArtifactAt(root, discard)
	if err != nil || moved != want {
		_ = root.Rename(discard, name)
		return newUpdateError("update_artifact_substituted")
	}
	return nil
}

func removeKnownAt(root *os.Root, name string, want artifactFingerprint) error {
	if !want.valid() {
		_, originalErr := root.Lstat(name)
		_, cleanupErr := root.Lstat(name + ".guardian-cleanup")
		if errors.Is(originalErr, os.ErrNotExist) && errors.Is(cleanupErr, os.ErrNotExist) {
			return nil
		}
		return newUpdateError("update_cleanup_identity_missing")
	}
	cleanupName := name + ".guardian-cleanup"
	cleanupExists, err := optionalKnownFingerprintAt(root, cleanupName, want)
	if err != nil {
		return err
	}
	originalExists, err := optionalKnownFingerprintAt(root, name, want)
	if err != nil {
		return err
	}
	if cleanupExists {
		if originalExists {
			return newUpdateError("update_cleanup_residue_present")
		}
		if err := root.RemoveAll(cleanupName); err != nil {
			return err
		}
		return syncRecoveryRoot(root)
	}
	if !originalExists {
		return nil
	}
	if err := root.Rename(name, cleanupName); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := syncRecoveryRoot(root); err != nil {
		return err
	}
	moved, err := fingerprintArtifactAt(root, cleanupName)
	if err != nil || moved != want {
		_ = root.Rename(cleanupName, name)
		return newUpdateError("update_artifact_substituted")
	}
	if err := root.RemoveAll(cleanupName); err != nil {
		return err
	}
	return syncRecoveryRoot(root)
}

func copyFileBetweenRoots(source *os.Root, sourceName string, destination *os.Root, destinationName string) error {
	if _, err := destination.Lstat(destinationName); err == nil {
		return newUpdateError("update_recovery_residue_present")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	info, err := source.Lstat(sourceName)
	if err != nil || !info.Mode().IsRegular() {
		return newUpdateError("update_snapshot_invalid")
	}
	input, err := source.Open(sourceName)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := destination.OpenFile(destinationName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	if copyErr == nil {
		copyErr = output.Chmod(info.Mode().Perm())
	}
	if copyErr == nil {
		copyErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		_ = destination.Remove(destinationName)
	}
	return errors.Join(copyErr, closeErr)
}

func copyTreeBetweenRoots(source *os.Root, sourceName string, destination *os.Root, destinationName string, uid, gid int) error {
	if _, err := destination.Lstat(destinationName); err == nil {
		return newUpdateError("update_recovery_residue_present")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := copyTreeContents(source, sourceName, destination, destinationName, uid, gid); err != nil {
		_ = destination.RemoveAll(destinationName)
		return err
	}
	return nil
}

func copyTreeContents(source *os.Root, sourceName string, destination *os.Root, destinationName string, uid, gid int) error {
	info, err := source.Lstat(sourceName)
	if err != nil || !info.IsDir() {
		return newUpdateError("update_snapshot_invalid")
	}
	if err := destination.Mkdir(destinationName, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := fs.ReadDir(source.FS(), sourceName)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		sourceChild := filepath.Join(sourceName, entry.Name())
		destinationChild := filepath.Join(destinationName, entry.Name())
		childInfo, err := source.Lstat(sourceChild)
		if err != nil {
			return err
		}
		switch {
		case childInfo.IsDir():
			if err := copyTreeContents(source, sourceChild, destination, destinationChild, uid, gid); err != nil {
				return err
			}
		case childInfo.Mode().IsRegular():
			if err := copyFileBetweenRoots(source, sourceChild, destination, destinationChild); err != nil {
				return err
			}
			file, err := destination.Open(destinationChild)
			if err != nil {
				return err
			}
			chownErr := file.Chown(uid, gid)
			closeErr := file.Close()
			if err := errors.Join(chownErr, closeErr); err != nil {
				return err
			}
		default:
			return newUpdateError("update_snapshot_invalid")
		}
	}
	directory, err := destination.Open(destinationName)
	if err != nil {
		return err
	}
	chownErr := directory.Chown(uid, gid)
	closeErr := directory.Close()
	return errors.Join(chownErr, closeErr)
}

func removeRootOwnedTransactionDirectory(path, parent, transactionID string) error {
	if filepath.Base(path) != transactionID || filepath.Dir(path) != parent || !updateTransactionIDPattern.MatchString(transactionID) {
		return newUpdateError("update_cleanup_path_invalid")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.RemoveAll(transactionID); err != nil {
		return err
	}
	return syncRecoveryRoot(root)
}

func requiredGuardianProtocol(packageData []byte, arch string) (int, error) {
	reader, err := gzip.NewReader(bytes.NewReader(packageData))
	if err != nil {
		return 0, err
	}
	defer reader.Close()
	want := "bx-macos-" + arch + "/guardian-update.json"
	tReader := tar.NewReader(reader)
	required := currentGuardianProtocol
	found := false
	for {
		header, err := tReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		if header.Name != want {
			continue
		}
		if found || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Size <= 0 || header.Size > 4096 {
			return 0, newUpdateError("guardian_protocol_metadata_invalid")
		}
		found = true
		var metadata struct {
			GuardianProtocol int `json:"guardian_protocol"`
		}
		decoder := json.NewDecoder(io.LimitReader(tReader, header.Size))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&metadata); err != nil || metadata.GuardianProtocol <= 0 {
			return 0, newUpdateError("guardian_protocol_metadata_invalid")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return 0, newUpdateError("guardian_protocol_metadata_invalid")
		}
		required = metadata.GuardianProtocol
	}
	return required, nil
}
