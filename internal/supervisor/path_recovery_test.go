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

type fakeLiveUnderlay struct {
	next UnderlaySnapshot
	errs map[string]error

	mu    sync.Mutex
	calls []string
}

func (u *fakeLiveUnderlay) record(call string) {
	u.mu.Lock()
	u.calls = append(u.calls, call)
	u.mu.Unlock()
}

func (u *fakeLiveUnderlay) Observe(context.Context) (UnderlaySnapshot, error) {
	u.record("observe")
	return u.next, u.errs["observe"]
}

func (u *fakeLiveUnderlay) ValidateCapture(context.Context, tunHandle) error {
	u.record("validate_capture")
	return u.errs["validate_capture"]
}

func (u *fakeLiveUnderlay) Rebind(context.Context, tunHandle, UnderlaySnapshot, UnderlaySnapshot, []string, []string) error {
	u.record("rebind_underlay")
	return u.errs["rebind_underlay"]
}

type fakeLiveTransportSet struct {
	err error

	mu    sync.Mutex
	calls []string
}

func (s *fakeLiveTransportSet) Recover(context.Context) error {
	s.mu.Lock()
	s.calls = append(s.calls, "transport_health")
	s.mu.Unlock()
	return s.err
}

func TestLivePathRecovererRunsCaptureSafeOrderAndVerifies(t *testing.T) {
	old := mustUnderlaySnapshot(t, "en0", "192.0.2.1", "192.0.2.0/24")
	next := mustUnderlaySnapshot(t, "en1", "198.51.100.1", "198.51.100.0/24")
	underlay := &fakeLiveUnderlay{next: next, errs: map[string]error{}}
	transports := &fakeLiveTransportSet{}
	var orderMu sync.Mutex
	var order []string
	recoverer := &livePathRecoverer{
		underlay:     underlay,
		transports:   transports,
		tun:          tunHandle{Name: "utun7"},
		previous:     old,
		serverBypass: []string{"203.0.113.9/32"},
		userBypass:   []string{"10.0.0.0/8"},
		verify: func(context.Context) error {
			orderMu.Lock()
			order = append(order, "verify")
			orderMu.Unlock()
			return nil
		},
	}
	var stages []string

	result, err := recoverer.RecoverPath(context.Background(), PathRecoveryRequest{Reason: "underlay_changed"}, func(snapshot PathRecoverySnapshot) {
		stages = append(stages, snapshot.Stage)
	})
	if err != nil {
		t.Fatalf("RecoverPath = %+v, %v", result, err)
	}
	if result.State != "succeeded" || result.Stage != "succeeded" {
		t.Fatalf("result = %+v", result)
	}
	underlay.mu.Lock()
	underlayCalls := append([]string(nil), underlay.calls...)
	underlay.mu.Unlock()
	transports.mu.Lock()
	transportCalls := append([]string(nil), transports.calls...)
	transports.mu.Unlock()
	orderMu.Lock()
	verifyCalls := append([]string(nil), order...)
	orderMu.Unlock()
	gotCalls := append(append(underlayCalls, transportCalls...), verifyCalls...)
	wantCalls := []string{"observe", "validate_capture", "rebind_underlay", "transport_health", "verify"}
	if !equalStrings(gotCalls, wantCalls) {
		t.Fatalf("recovery calls = %v, want %v", gotCalls, wantCalls)
	}
	wantStages := []string{"observe", "validate_capture", "rebind_underlay", "transport_health", "verify", "succeeded"}
	if !equalStrings(stages, wantStages) {
		t.Fatalf("recovery stages = %v, want %v", stages, wantStages)
	}
	if recoverer.previous.Generation != next.Generation {
		t.Fatalf("previous generation = %q, want %q", recoverer.previous.Generation, next.Generation)
	}
}

func TestLivePathRecovererStopsBeforeTransportsWhenRebindFails(t *testing.T) {
	rebindErr := &PathRecoveryError{Code: "underlay_rebind_failed", Detail: "fake route failure"}
	underlay := &fakeLiveUnderlay{
		next: mustUnderlaySnapshot(t, "en1", "198.51.100.1", "198.51.100.0/24"),
		errs: map[string]error{"rebind_underlay": rebindErr},
	}
	transports := &fakeLiveTransportSet{}
	recoverer := &livePathRecoverer{
		underlay:   underlay,
		transports: transports,
		tun:        tunHandle{Name: "utun7"},
		previous:   mustUnderlaySnapshot(t, "en0", "192.0.2.1", "192.0.2.0/24"),
	}

	_, err := recoverer.RecoverPath(context.Background(), PathRecoveryRequest{}, func(PathRecoverySnapshot) {})
	if !errors.Is(err, rebindErr) {
		t.Fatalf("RecoverPath error = %v, want %v", err, rebindErr)
	}
	transports.mu.Lock()
	defer transports.mu.Unlock()
	if len(transports.calls) != 0 {
		t.Fatalf("transports called after rebind failure: %v", transports.calls)
	}
}

func TestLivePathRecovererMapsUDPOnlyFailureToNeedsAttention(t *testing.T) {
	udpErr := &transportSetError{Slot: udpTransportSlot, Err: errors.New("UDP candidate unhealthy")}
	recoverer := &livePathRecoverer{
		underlay: &fakeLiveUnderlay{
			next: mustUnderlaySnapshot(t, "en1", "198.51.100.1", "198.51.100.0/24"),
			errs: map[string]error{},
		},
		transports: &fakeLiveTransportSet{err: udpErr},
		tun:        tunHandle{Name: "utun7"},
		previous:   mustUnderlaySnapshot(t, "en0", "192.0.2.1", "192.0.2.0/24"),
	}

	result, err := recoverer.RecoverPath(context.Background(), PathRecoveryRequest{}, func(PathRecoverySnapshot) {})
	if err != nil {
		t.Fatalf("RecoverPath error = %v", err)
	}
	if result.State != "needs_attention" || result.ErrorCode != "transport_unavailable" {
		t.Fatalf("UDP failure result = %+v", result)
	}
}

func TestLivePathRecovererMapsMainFailureToBlockedTransportError(t *testing.T) {
	mainErr := &transportSetError{Slot: mainTransportSlot, Err: errors.New("main candidate unhealthy")}
	recoverer := &livePathRecoverer{
		underlay: &fakeLiveUnderlay{
			next: mustUnderlaySnapshot(t, "en1", "198.51.100.1", "198.51.100.0/24"),
			errs: map[string]error{},
		},
		transports: &fakeLiveTransportSet{err: mainErr},
		tun:        tunHandle{Name: "utun7"},
		previous:   mustUnderlaySnapshot(t, "en0", "192.0.2.1", "192.0.2.0/24"),
	}

	_, err := recoverer.RecoverPath(context.Background(), PathRecoveryRequest{}, func(PathRecoverySnapshot) {})
	var recoveryErr *PathRecoveryError
	if !errors.As(err, &recoveryErr) || recoveryErr.Code != "transport_unavailable" {
		t.Fatalf("RecoverPath error = %v, want transport_unavailable", err)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
