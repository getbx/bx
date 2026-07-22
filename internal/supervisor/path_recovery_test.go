package supervisor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

type scriptedPathRecoverer struct {
	stages      []string
	entered     chan struct{}
	release     <-chan struct{}
	active      atomic.Int32
	max         atomic.Int32
	blocked     atomic.Bool
	enteredOnce sync.Once
	mu          sync.Mutex
	observed    []string
	err         error
}

func (r *scriptedPathRecoverer) RecoverPath(_ context.Context, _ PathRecoveryRequest, observe func(PathRecoverySnapshot)) (PathRecoverySnapshot, error) {
	active := r.active.Add(1)
	for {
		max := r.max.Load()
		if active <= max || r.max.CompareAndSwap(max, active) {
			break
		}
	}
	defer r.active.Add(-1)
	if r.entered != nil {
		r.enteredOnce.Do(func() { close(r.entered) })
	}
	if r.release != nil && r.blocked.CompareAndSwap(false, true) {
		<-r.release
	}
	for _, stage := range r.stages {
		state := "recovering"
		if stage == "succeeded" {
			state = "succeeded"
		}
		observe(PathRecoverySnapshot{State: state, Stage: stage})
		r.mu.Lock()
		r.observed = append(r.observed, stage)
		r.mu.Unlock()
	}
	return PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}, r.err
}

func TestPathRecoveryPublishesStagesAndSerializesOperations(t *testing.T) {
	release := make(chan struct{})
	recoverer := &scriptedPathRecoverer{
		stages:  []string{"observe", "validate_capture", "rebind_underlay", "transport_health", "verify", "succeeded"},
		entered: make(chan struct{}),
		release: release,
	}
	op := newPathRecoveryOperation(recoverer)

	firstDone := make(chan PathRecoverySnapshot, 1)
	go func() {
		snapshot, _ := op.Recover(context.Background(), PathRecoveryRequest{Reason: "underlay_changed", Generation: "wifi-b"})
		firstDone <- snapshot
	}()
	<-recoverer.entered

	if current := op.Snapshot(); current.State != "recovering" || current.Stage != "observe" {
		t.Fatalf("current snapshot = %+v, want recovering observe", current)
	}

	secondStarted := make(chan struct{})
	secondDone := make(chan PathRecoverySnapshot, 1)
	go func() {
		close(secondStarted)
		snapshot, _ := op.Recover(context.Background(), PathRecoveryRequest{Reason: "manual"})
		secondDone <- snapshot
	}()
	<-secondStarted
	if got := recoverer.max.Load(); got != 1 {
		t.Fatalf("concurrent recoveries = %d, want 1", got)
	}

	close(release)
	first := <-firstDone
	second := <-secondDone
	if first.State != "succeeded" || second.State != "succeeded" {
		t.Fatalf("recovery results = %+v, %+v", first, second)
	}
	recoverer.mu.Lock()
	got := append([]string(nil), recoverer.observed...)
	recoverer.mu.Unlock()
	want := []string{"observe", "validate_capture", "rebind_underlay", "transport_health", "verify", "succeeded"}
	if len(got) < len(want) {
		t.Fatalf("published stages = %v", got)
	}
	for i, stage := range want {
		if got[i] != stage {
			t.Fatalf("published stages = %v, want prefix %v", got, want)
		}
	}
}

func TestPathRecoveryFailureStoresStableCodeWithoutDetail(t *testing.T) {
	secret := "vless://secret-uuid@proxy.example:443?pbk=secret-key"
	recoverer := &scriptedPathRecoverer{
		stages: []string{"observe", "transport_health"},
		err:    &PathRecoveryError{Code: "transport_unavailable", Detail: secret},
	}
	op := newPathRecoveryOperation(recoverer)

	snapshot, err := op.Recover(context.Background(), PathRecoveryRequest{Reason: "manual"})
	if !errors.Is(err, recoverer.err) {
		t.Fatalf("Recover error = %v, want %v", err, recoverer.err)
	}
	if snapshot.State != "blocked" || snapshot.ErrorCode != "transport_unavailable" {
		t.Fatalf("failure snapshot = %+v", snapshot)
	}
	if snapshot.Detail != "" {
		t.Fatalf("stored detail = %q, want empty redacted detail", snapshot.Detail)
	}
}
