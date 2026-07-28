package supervisor

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// PathRecoveryRequest identifies the network path change that needs recovery.
// It deliberately carries no transport link or other secret material.
type PathRecoveryRequest struct {
	Reason     string `json:"reason"`
	Generation string `json:"generation,omitempty"`
}

// PathRecoverySnapshot is the non-secret recovery progress contract for local clients.
type PathRecoverySnapshot struct {
	ID         string    `json:"recovery_id"`
	State      string    `json:"state"`
	Stage      string    `json:"stage"`
	Reason     string    `json:"reason"`
	Generation string    `json:"generation,omitempty"`
	Attempt    int       `json:"attempt"`
	ErrorCode  string    `json:"last_error_code,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type pathRecoverer interface {
	RecoverPath(context.Context, PathRecoveryRequest, func(PathRecoverySnapshot)) (PathRecoverySnapshot, error)
}

type livePathRecoverer struct {
	mu           sync.Mutex
	underlay     underlayManager
	transports   transportSet
	tun          tunHandle
	previous     UnderlaySnapshot
	serverBypass []string
	userBypass   []string
	verify       func(context.Context) error
}

func (r *livePathRecoverer) RecoverPath(ctx context.Context, _ PathRecoveryRequest, observe func(PathRecoverySnapshot)) (PathRecoverySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	publish := func(stage string) {
		if observe != nil {
			observe(PathRecoverySnapshot{State: "recovering", Stage: stage})
		}
	}
	publish("observe")
	next, err := r.underlay.Observe(ctx)
	if err != nil {
		return PathRecoverySnapshot{Stage: "observe"}, err
	}

	publish("validate_capture")
	if err := r.underlay.ValidateCapture(ctx, r.tun); err != nil {
		return PathRecoverySnapshot{Stage: "validate_capture"}, err
	}

	publish("rebind_underlay")
	if err := r.underlay.Rebind(ctx, r.tun, r.previous, next, r.serverBypass, r.userBypass); err != nil {
		return PathRecoverySnapshot{Stage: "rebind_underlay"}, err
	}
	r.previous = next

	publish("transport_health")
	if err := r.transports.Recover(ctx); err != nil {
		if transportErrorSlot(err) == udpTransportSlot {
			return PathRecoverySnapshot{
				State:     "needs_attention",
				Stage:     "transport_health",
				ErrorCode: "transport_unavailable",
			}, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return PathRecoverySnapshot{Stage: "transport_health"}, err
		}
		return PathRecoverySnapshot{Stage: "transport_health"}, &PathRecoveryError{
			Code:   "transport_unavailable",
			Detail: err.Error(),
		}
	}

	publish("verify")
	if r.verify != nil {
		if err := r.verify(ctx); err != nil {
			return PathRecoverySnapshot{Stage: "verify"}, &PathRecoveryError{
				Code:   "verification_failed",
				Detail: err.Error(),
			}
		}
	}

	publish("succeeded")
	return PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}, nil
}

// PathRecoveryError describes a stable category without making free-form diagnostics
// part of the LocalAPI contract.
type PathRecoveryError struct {
	Code   string
	Detail string
}

func (e *PathRecoveryError) Error() string { return e.Code + ": " + e.Detail }

type pathRecoveryOperation struct {
	mu        sync.Mutex
	recoverer pathRecoverer
	sequence  atomic.Uint64
	snapshot  atomic.Pointer[PathRecoverySnapshot]
}

func newPathRecoveryOperation(recoverer pathRecoverer) *pathRecoveryOperation {
	return &pathRecoveryOperation{recoverer: recoverer}
}

// Recover serializes recovery mutations while Snapshot remains lock-free for GET clients.
func (o *pathRecoveryOperation) Recover(ctx context.Context, request PathRecoveryRequest) (PathRecoverySnapshot, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := time.Now().UTC()
	snapshot := PathRecoverySnapshot{
		ID:         pathRecoveryID(o.sequence.Add(1)),
		State:      "recovering",
		Stage:      "observe",
		Reason:     request.Reason,
		Generation: request.Generation,
		Attempt:    1,
		StartedAt:  now,
		UpdatedAt:  now,
	}
	o.store(snapshot)

	if o.recoverer == nil {
		snapshot.State = "blocked"
		snapshot.ErrorCode = "recovery_unavailable"
		o.store(snapshot)
		return snapshot, &PathRecoveryError{Code: snapshot.ErrorCode}
	}

	result, err := o.recoverer.RecoverPath(ctx, request, func(update PathRecoverySnapshot) {
		o.store(o.normalize(update, snapshot))
	})
	result = o.normalize(result, snapshot)
	if err != nil {
		result.State = "blocked"
		result.ErrorCode = pathRecoveryErrorCode(err)
		result.Detail = ""
		o.store(result)
		return result, err
	}
	if result.State == "" {
		result.State = "succeeded"
	}
	if result.Stage == "" {
		result.Stage = result.State
	}
	result.ErrorCode = stablePathRecoveryCode(result.ErrorCode)
	result.Detail = ""
	o.store(result)
	return result, nil
}

func (o *pathRecoveryOperation) Snapshot() PathRecoverySnapshot {
	if snapshot := o.snapshot.Load(); snapshot != nil {
		return *snapshot
	}
	return PathRecoverySnapshot{State: "idle"}
}

func (o *pathRecoveryOperation) normalize(update, base PathRecoverySnapshot) PathRecoverySnapshot {
	if update.ID == "" {
		update.ID = base.ID
	}
	if update.Reason == "" {
		update.Reason = base.Reason
	}
	if update.Generation == "" {
		update.Generation = base.Generation
	}
	if update.Attempt == 0 {
		update.Attempt = base.Attempt
	}
	if update.StartedAt.IsZero() {
		update.StartedAt = base.StartedAt
	}
	if update.State == "succeeded" || update.Stage == "succeeded" {
		update.State = "succeeded"
		update.Stage = "succeeded"
		update.ErrorCode = ""
	}
	update.UpdatedAt = time.Now().UTC()
	update.ErrorCode = stablePathRecoveryCode(update.ErrorCode)
	update.Detail = ""
	return update
}

func (o *pathRecoveryOperation) store(snapshot PathRecoverySnapshot) {
	if snapshot.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = time.Now().UTC()
	}
	o.snapshot.Store(&snapshot)
}

func pathRecoveryID(sequence uint64) string {
	return "recovery-" + strconv.FormatUint(sequence, 10)
}

func pathRecoveryErrorCode(err error) string {
	var recoveryErr *PathRecoveryError
	if errors.As(err, &recoveryErr) && recoveryErr != nil {
		if code := stablePathRecoveryCode(recoveryErr.Code); code != "" {
			return code
		}
	}
	return "recovery_failed"
}

func stablePathRecoveryCode(code string) string {
	switch code {
	case "capture_invalid", "capture_missing", "network_unavailable", "recovery_failed", "recovery_unavailable", "transport_unavailable", "underlay_rebind_failed", "underlay_unavailable", "verification_failed":
		return code
	default:
		return ""
	}
}

var _ pathRecoverer = (*livePathRecoverer)(nil)
