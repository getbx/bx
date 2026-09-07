package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/getbx/bx/internal/observe"
	"github.com/getbx/bx/internal/supervisor"
)

var (
	errPathRecoveryInvalid      = errors.New("invalid path recovery request")
	errPathRecoveryShuttingDown = errors.New("Guardian path recovery is shutting down")
	recoveryGenerationPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

const (
	pathRecoveryRetryInitial = 100 * time.Millisecond
	pathRecoveryRetryMax     = 5 * time.Second

	// maxPathRecoveryAttempts 是同一次路径恢复的重试上限。
	//
	// 退避上限只有 5 秒,若不设次数上限,恢复会以约 25 秒一轮无限重试
	// (真实事故中达到 178 次、71 分钟),每轮新起一个隧道进程,且永远不会
	// 停到用户可处理的状态。达到上限后放弃,让状态落到 needs_attention,
	// 用户可以看到并采取行动(换服务器、关闭保护)。
	maxPathRecoveryAttempts = 20
)

type CorePathClient interface {
	RecoverPath(context.Context, supervisor.PathRecoveryRequest) (supervisor.PathRecoverySnapshot, error)
}

type corePathProgressClient interface {
	RecoverPathObserved(
		context.Context,
		supervisor.PathRecoveryRequest,
		func(supervisor.PathRecoverySnapshot),
	) (supervisor.PathRecoverySnapshot, error)
}

type pathRecoveryTransaction struct {
	request                     RecoveryRequest
	snapshot                    RecoverySnapshot
	needsProtectionCommit       bool
	protectionBarrierCredential barrierCredential
	protectionErrorCode         string
}

type recoveryLogRecord struct {
	RecoveryID string `json:"recovery_id"`
	Generation string `json:"generation"`
	Reason     string `json:"reason"`
	State      string `json:"state"`
	Stage      string `json:"stage"`
	Attempt    int    `json:"attempt"`
	DurationMS int64  `json:"duration_ms"`
	ErrorCode  string `json:"error_code"`
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

func (r *ExecCoreRunner) RecoverPathObserved(
	ctx context.Context,
	request supervisor.PathRecoveryRequest,
	observe func(supervisor.PathRecoverySnapshot),
) (supervisor.PathRecoverySnapshot, error) {
	controlSocket := r.ControlSocket
	if controlSocket == "" {
		controlSocket = supervisor.SockPath
	}
	result := make(chan observedCorePathResult, 1)
	go func() {
		snapshot, err := supervisor.RecoverPathControl(ctx, controlSocket, request)
		result <- observedCorePathResult{snapshot: snapshot, err: err}
	}()

	timer := time.NewTimer(pathRecoveryRetryInitial)
	defer timer.Stop()
	for {
		select {
		case completed := <-result:
			return completed.snapshot, completed.err
		case <-ctx.Done():
			return supervisor.PathRecoverySnapshot{}, ctx.Err()
		case <-timer.C:
			snapshot, err := supervisor.FetchPathRecovery(ctx, controlSocket)
			if err == nil &&
				snapshot.State == "recovering" &&
				snapshot.Reason == request.Reason &&
				snapshot.Generation == request.Generation {
				observe(snapshot)
			}
			timer.Reset(pathRecoveryRetryInitial)
		}
	}
}

type observedCorePathResult struct {
	snapshot supervisor.PathRecoverySnapshot
	err      error
}

func (m *Manager) RequestPathRecovery(request RecoveryRequest) (RecoverySnapshot, error) {
	normalized, err := ValidateRecoveryRequest(request)
	if err != nil {
		return RecoverySnapshot{}, err
	}

	m.pathRecoveryMu.Lock()
	if normalized.Generation != "" {
		m.networkGeneration = normalized.Generation
	}
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
		transferPathRecoveryProtection(m.pathRecoveryPending, &transaction)
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
		transferPathRecoveryProtection(m.pathRecoveryPending, &transaction)
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
		logRecoverySnapshot(transaction.snapshot)
		return transaction.snapshot, nil
	}

	operationCtx, cancel := m.pathRecoveryNewContext()
	m.pathRecoveryCurrent = transaction.snapshot
	m.pathRecoveryCancel = cancel
	m.pathRecoveryActive = true
	m.pathRecoveryMu.Unlock()
	go m.runPathRecovery(operationCtx, cancel, transaction)
	return transaction.snapshot, nil
}

// retireCompletedPathRecovery 让一次**已经结束**的路径恢复不再对外发布。
//
// 用户显式的 up/down 成功之后调用。observableStatus 只要上一次恢复还写着
// failed 就把 Manager 的 Protected 改写成 Blocked,而那份快照此前没有任何东西
// 会清 —— 真机 2026-09-04:开机后一次手动重连在 transport_health 失败,随后
// down/up 都成功、up 的应答是 Protected,`bx status` 与菜单却一直 Blocked、
// 图标裂开,同一份 status 里 observed 明明说屏障不在、隧道健康。
//
// 只清**已结束**的:正在跑或排队中的恢复由它自己发布结局,这里不插手
// (它跑完会按 ID 比对再写回 pathRecoveryCurrent,见 runPathRecovery)。
// networkGeneration 不动:它记的是「最近一次看到的网络」,不是恢复的结局。
func (m *Manager) retireCompletedPathRecovery() {
	m.pathRecoveryMu.Lock()
	defer m.pathRecoveryMu.Unlock()
	if m.pathRecoveryActive || m.pathRecoveryPending != nil {
		return
	}
	if !completedPathRecoveryState(m.pathRecoveryCurrent.State) {
		return
	}
	m.pathRecoveryCurrent = RecoverySnapshot{State: "idle", Stage: "idle"}
}

// retireContradictedPathRecovery 让一份**被内核否定**的失败恢复退场。
//
// 真机 2026-09-07:一小时的 50 秒一拍睡眠/暗唤醒里,recovery-10 在 verify 连败
// 20 次后放弃(退避 100ms→5s,20 次只要 105 秒);机器真正醒来后捕获、屏障、DNS、
// 隧道全部自愈,而那份 failed 快照没有任何东西会清 —— `bx status` 与菜单一直
// Blocked、图标裂开,同一屏 observed 说屏障不在、隧道健康。09-04 的
// retireCompletedPathRecovery 只挂在用户的 up/down 上;用户什么都没做时它永远不跑。
//
// **判据是观测,不是时间**:捕获在我们的 TUN 上、屏障不在内核里、DNS 归 bx、Core
// 应答、隧道健康 —— 这五项正是 Core 那边 verify 要看的东西;它们此刻全为 True,
// 那次 verify 放在现在就会通过。任一项不是 True(False 或 Unknown)都不动:「问不
// 出来」不是「好了」。Manager 自己说的不是 Protected 也不动 —— 屏障真在手里时
// Blocked 是事实,不是陈旧结局;正在跑的恢复由它自己发布结局。
func (m *Manager) retireContradictedPathRecovery(observed observe.ObservedState) {
	if observed.CaptureOK != observe.True ||
		observed.BarrierPresent != observe.False ||
		observed.DNSManaged != observe.True ||
		observed.CoreSocket != observe.True ||
		observed.TunnelHealthy != observe.True {
		return
	}
	m.retireFailedPathRecovery("observed_protected")
}

// retireSupersededPathRecovery 是 observableStatus 那条路的入口:Core 的运行时
// 事实已满足 verify 的每一项(判据在 recoverySupersededByCore,调用方已判过),
// 把那份失败快照从记忆里清掉。
func (m *Manager) retireSupersededPathRecovery() bool {
	return m.retireFailedPathRecovery("core_verified")
}

// retireFailedPathRecovery 让一份已结束的失败恢复退场。两个调用方(调谐环按
// 内核观测、状态组装按 Core 运行时)共用这一份:Manager 自己得说 Protected
// (屏障真在手里时 Blocked 是事实),没有正在跑或排队的恢复,当前快照是 failed。
func (m *Manager) retireFailedPathRecovery(reason string) bool {
	if m.Status().Protection != ProtectionProtected {
		return false
	}
	m.pathRecoveryMu.Lock()
	defer m.pathRecoveryMu.Unlock()
	if m.pathRecoveryActive || m.pathRecoveryPending != nil {
		return false
	}
	current := m.pathRecoveryCurrent
	if current.State != "failed" {
		return false
	}
	m.pathRecoveryCurrent = RecoverySnapshot{State: "idle", Stage: "idle"}
	log.Printf("guardian_path_recovery_retired id=%s stage=%s error_code=%s attempt=%d reason=%s",
		current.ID, current.Stage, current.ErrorCode, current.Attempt, reason)
	return true
}

func (m *Manager) CurrentPathRecovery() RecoverySnapshot {
	m.pathRecoveryMu.Lock()
	defer m.pathRecoveryMu.Unlock()
	return m.pathRecoveryCurrent
}

func (m *Manager) currentPathRecoveryStatus() (RecoverySnapshot, string) {
	m.pathRecoveryMu.Lock()
	defer m.pathRecoveryMu.Unlock()
	return m.pathRecoveryCurrent, m.networkGeneration
}

func (m *Manager) runPathRecovery(operationCtx context.Context, cancel context.CancelFunc, transaction pathRecoveryTransaction) {
attempts:
	for {
		m.publishRunningPathRecovery(transaction)
		result := m.executePathRecovery(operationCtx, &transaction)
		cancel()
		logRecoverySnapshot(result)

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
			transferPathRecoveryProtection(&transaction, m.pathRecoveryPending)
			m.pathRecoveryActive = false
			if m.pathRecoveryPending != nil {
				m.pathRecoveryCurrent = m.pathRecoveryPending.snapshot
			}
			m.pathRecoveryMu.Unlock()
			return
		}
		if m.pathRecoveryPending == nil {
			if shouldRetryPathRecovery(transaction, result, m.Status().Desired) {
				delay := pathRecoveryRetryBackoff(transaction.snapshot.Attempt)
				transaction.snapshot = queuedPathRecoveryRetrySnapshot(result)
				retryCtx, retryCancel := context.WithCancel(context.Background())
				m.pathRecoveryCurrent = transaction.snapshot
				m.pathRecoveryCancel = retryCancel
				wait := m.pathRecoveryRetryWait
				m.pathRecoveryMu.Unlock()

				retryErr := wait(retryCtx, delay)
				retryCancel()

				m.pathRecoveryMu.Lock()
				m.pathRecoveryCancel = nil
				if retryErr == nil &&
					m.pathRecoveryAccepting &&
					m.pathRecoveryFences == 0 &&
					m.pathRecoveryPending == nil &&
					m.Status().Desired == DesiredOn {
					operationCtx, cancel = m.pathRecoveryNewContext()
					m.pathRecoveryCancel = cancel
					m.pathRecoveryCurrent = transaction.snapshot
					m.pathRecoveryMu.Unlock()
					continue attempts
				}
				if retryErr != nil &&
					m.pathRecoveryAccepting &&
					m.pathRecoveryFences == 0 &&
					m.pathRecoveryPending == nil {
					if m.pathRecoveryCurrent.ID == transaction.snapshot.ID {
						m.pathRecoveryCurrent = result
					}
					m.pathRecoveryActive = false
					m.pathRecoveryResolveOff = false
					m.pathRecoveryMu.Unlock()
					return
				}
			} else {
				m.pathRecoveryActive = false
				m.pathRecoveryResolveOff = false
				m.pathRecoveryMu.Unlock()
				return
			}
		}
		if !m.pathRecoveryAccepting {
			m.pathRecoveryPending = nil
			m.pathRecoveryActive = false
			m.closePathRecoveryDrainedLocked()
			m.pathRecoveryMu.Unlock()
			return
		}
		if m.pathRecoveryFences > 0 {
			transferPathRecoveryProtection(&transaction, m.pathRecoveryPending)
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
		transferPathRecoveryProtection(&transaction, m.pathRecoveryPending)
		transaction = *m.pathRecoveryPending
		m.pathRecoveryPending = nil
		if m.pathRecoveryResolveOff {
			m.pathRecoveryResolveOff = false
			m.pathRecoveryActive = false
			m.pathRecoveryCurrent = ignoredPathRecoverySnapshot(transaction.snapshot)
			result := m.pathRecoveryCurrent
			m.pathRecoveryMu.Unlock()
			logRecoverySnapshot(result)
			return
		}
		operationCtx, cancel = m.pathRecoveryNewContext()
		m.pathRecoveryCancel = cancel
		m.pathRecoveryCurrent = transaction.snapshot
		m.pathRecoveryMu.Unlock()
	}
}

func (m *Manager) publishRunningPathRecovery(transaction pathRecoveryTransaction) {
	m.pathRecoveryMu.Lock()
	defer m.pathRecoveryMu.Unlock()
	if m.pathRecoveryFences > 0 || m.pathRecoveryCurrent.ID != transaction.snapshot.ID {
		return
	}
	snapshot := transaction.snapshot
	snapshot.State = "running"
	snapshot.Stage = "core_recovery"
	snapshot.UpdatedAt = time.Now().UTC()
	m.pathRecoveryCurrent = snapshot
}

func (m *Manager) executePathRecovery(ctx context.Context, transaction *pathRecoveryTransaction) RecoverySnapshot {
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
	request := supervisor.PathRecoveryRequest{
		Reason:     transaction.request.Reason,
		Generation: transaction.request.Generation,
	}
	var result supervisor.PathRecoverySnapshot
	var err error
	if core, ok := m.corePath.(corePathProgressClient); ok {
		result, err = core.RecoverPathObserved(ctx, request, func(progress supervisor.PathRecoverySnapshot) {
			m.publishCorePathRecovery(transaction.snapshot.ID, progress)
		})
	} else {
		result, err = m.corePath.RecoverPath(ctx, request)
	}
	if err != nil {
		return failedPathRecoverySnapshot(transaction.snapshot, result, err)
	}
	if result.State == "succeeded" {
		barrierBeforeDNS := m.barrierOwnership
		if err := m.ensureDNSManaged(ctx, m.runtime); err != nil {
			recordPathRecoveryProtectionFailure(transaction, barrierBeforeDNS, m.barrierOwnership, m.Status().LastError)
			result.Stage = "verify"
			return failedPathRecoverySnapshot(transaction.snapshot, result, pathRecoveryDNSError(transaction.protectionErrorCode))
		}
		if transaction.needsProtectionCommit &&
			transaction.protectionBarrierCredential != 0 &&
			!m.pathRecoveryBarrierCredentialCurrent(transaction.protectionBarrierCredential) {
			clearPathRecoveryProtection(transaction)
		}
		if transaction.needsProtectionCommit {
			if err := m.provePathRecoveryBarrier(ctx, transaction); err != nil {
				result.Stage = "verify"
				return failedPathRecoverySnapshot(transaction.snapshot, result, pathRecoveryDNSError(transaction.protectionErrorCode, err))
			}
			if err := m.releaseBarrierToCore(ctx); err != nil {
				transaction.protectionBarrierCredential = m.barrierOwnership.credential
				m.needsAttention(DesiredOn, transaction.protectionErrorCode)
				result.Stage = "verify"
				return failedPathRecoverySnapshot(transaction.snapshot, result, pathRecoveryDNSError(transaction.protectionErrorCode, err))
			}
			transaction.protectionBarrierCredential = 0
			if err := m.setProtectedStatus(PhaseCommitted, m.current.PID, m.runtime.Version, ""); err != nil {
				result.Stage = "verify"
				barrierBeforeFailure := m.barrierOwnership
				activationErr := m.failDNSActivation(ctx, m.runtime, "dns_verification_failed", err)
				recordPathRecoveryProtectionFailure(transaction, barrierBeforeFailure, m.barrierOwnership, "dns_verification_failed")
				return failedPathRecoverySnapshot(transaction.snapshot, result, pathRecoveryDNSError(transaction.protectionErrorCode, activationErr))
			}
			clearPathRecoveryProtection(transaction)
		}
	}
	return completedPathRecoverySnapshot(transaction.snapshot, result)
}

func recordPathRecoveryProtectionFailure(transaction *pathRecoveryTransaction, before, after barrierOwnership, code string) {
	transaction.needsProtectionCommit = true
	transaction.protectionErrorCode = pathRecoveryDNSCode(code)

	if transaction.protectionBarrierCredential != 0 &&
		transaction.protectionBarrierCredential == before.credential {
		transaction.protectionBarrierCredential = after.credential
		return
	}
	if before.proof != barrierAbsent {
		return
	}
	transaction.protectionBarrierCredential = after.credential
}

func (m *Manager) provePathRecoveryBarrier(ctx context.Context, transaction *pathRecoveryTransaction) error {
	credential := transaction.protectionBarrierCredential
	if credential == 0 {
		return errors.New("path recovery does not own the pending protection barrier")
	}
	if !m.pathRecoveryBarrierCredentialCurrent(credential) {
		return errors.New("path recovery protection barrier credential is stale")
	}
	if m.barrierProven() {
		return nil
	}

	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), m.cleanupTimeout)
	err := m.installBarrierForRecovery(cleanupCtx, m.runtime)
	cancelCleanup()
	transaction.protectionBarrierCredential = m.barrierOwnership.credential
	if err != nil {
		m.needsAttention(DesiredOn, transaction.protectionErrorCode)
		return fmt.Errorf("prove path recovery barrier: %w", err)
	}
	return nil
}

func (m *Manager) pathRecoveryBarrierCredentialCurrent(credential barrierCredential) bool {
	return credential != 0 &&
		m.barrierOwnership.proof != barrierAbsent &&
		m.barrierOwnership.credential == credential
}

func transferPathRecoveryProtection(from *pathRecoveryTransaction, to *pathRecoveryTransaction) {
	if from == nil || to == nil || !from.needsProtectionCommit {
		return
	}
	to.needsProtectionCommit = true
	if from.protectionBarrierCredential != 0 {
		to.protectionBarrierCredential = from.protectionBarrierCredential
	}
	if to.protectionErrorCode == "" {
		to.protectionErrorCode = from.protectionErrorCode
	}
}

func clearPathRecoveryProtection(transaction *pathRecoveryTransaction) {
	transaction.needsProtectionCommit = false
	transaction.protectionBarrierCredential = 0
	transaction.protectionErrorCode = ""
}

func pathRecoveryDNSCode(code string) string {
	switch code {
	case "dns_takeover_failed", "dns_verification_failed":
		return code
	default:
		return "dns_verification_failed"
	}
}

func pathRecoveryDNSError(code string, causes ...error) error {
	errs := []error{&supervisor.PathRecoveryError{Code: pathRecoveryDNSCode(code)}}
	errs = append(errs, causes...)
	return errors.Join(errs...)
}

func (m *Manager) publishCorePathRecovery(id string, progress supervisor.PathRecoverySnapshot) {
	stage := publicPathRecoveryStage(progress.Stage)
	if stage == "" {
		return
	}
	m.pathRecoveryMu.Lock()
	defer m.pathRecoveryMu.Unlock()
	if m.pathRecoveryFences > 0 || m.pathRecoveryCurrent.ID != id {
		return
	}
	snapshot := m.pathRecoveryCurrent
	snapshot.State = "running"
	snapshot.Stage = stage
	if progress.Attempt > snapshot.Attempt {
		snapshot.Attempt = progress.Attempt
	}
	snapshot.UpdatedAt = time.Now().UTC()
	m.pathRecoveryCurrent = snapshot
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
		m.queueInterruptedPathRecoveryLocked(false)
	} else {
		m.queueInterruptedPathRecoveryLocked(true)
		if m.pathRecoveryPending != nil {
			m.pathRecoveryCurrent = m.pathRecoveryPending.snapshot
		}
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
	if m.pathRecoveryFences == 0 {
		m.pathRecoveryResolveOff = desired == DesiredOff
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
	operationCtx, cancel := m.pathRecoveryNewContext()
	m.pathRecoveryCurrent = transaction.snapshot
	m.pathRecoveryCancel = cancel
	m.pathRecoveryActive = true
	m.pathRecoveryMu.Unlock()
	go m.runPathRecovery(operationCtx, cancel, transaction)
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
	if result.Attempt > snapshot.Attempt {
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
	if result.Attempt > snapshot.Attempt {
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
	case "capture_invalid", "capture_missing", "dns_takeover_failed", "dns_verification_failed", "network_unavailable", "recovery_canceled", "recovery_failed", "recovery_unavailable", "transport_unavailable", "underlay_rebind_failed", "underlay_unavailable", "verification_failed":
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

func recoveryLogRecordFor(snapshot RecoverySnapshot) recoveryLogRecord {
	snapshot = redactRecoverySnapshot(snapshot)
	duration := snapshot.UpdatedAt.Sub(snapshot.StartedAt)
	if duration < 0 {
		duration = 0
	}
	return recoveryLogRecord{
		RecoveryID: snapshot.ID,
		Generation: snapshot.Generation,
		Reason:     snapshot.Reason,
		State:      snapshot.State,
		Stage:      snapshot.Stage,
		Attempt:    snapshot.Attempt,
		DurationMS: duration.Milliseconds(),
		ErrorCode:  snapshot.ErrorCode,
	}
}

func logRecoverySnapshot(snapshot RecoverySnapshot) {
	record, err := json.Marshal(recoveryLogRecordFor(snapshot))
	if err == nil {
		log.Printf("network_recovery %s", record)
	}
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

func shouldRetryPathRecovery(transaction pathRecoveryTransaction, result RecoverySnapshot, desired DesiredState) bool {
	if desired != DesiredOn ||
		transaction.request.Reason != "underlay_changed" ||
		transaction.request.Generation == "" ||
		result.State != "failed" ||
		transaction.snapshot.Attempt >= maxPathRecoveryAttempts {
		return false
	}
	switch result.ErrorCode {
	case "capture_invalid", "capture_missing", "recovery_canceled", "recovery_unavailable", "underlay_unavailable":
		return false
	default:
		return true
	}
}

func queuedPathRecoveryRetrySnapshot(result RecoverySnapshot) RecoverySnapshot {
	result.State = "accepted"
	result.Stage = "queued"
	result.Attempt++
	result.Detail = ""
	result.UpdatedAt = time.Now().UTC()
	return result
}

func nextPathRecoveryRetryBackoff(current time.Duration) time.Duration {
	if current >= pathRecoveryRetryMax {
		return pathRecoveryRetryMax
	}
	next := current * 2
	if next < current || next > pathRecoveryRetryMax {
		return pathRecoveryRetryMax
	}
	return next
}

func pathRecoveryRetryBackoff(attempt int) time.Duration {
	delay := pathRecoveryRetryInitial
	for completed := 1; completed < attempt; completed++ {
		delay = nextPathRecoveryRetryBackoff(delay)
	}
	return delay
}

func waitForPathRecoveryRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
