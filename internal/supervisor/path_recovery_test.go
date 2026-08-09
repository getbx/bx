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

type terminalProgressRecoverer struct {
	published chan struct{}
	release   chan struct{}
}

func (r *terminalProgressRecoverer) RecoverPath(_ context.Context, _ PathRecoveryRequest, observe func(PathRecoverySnapshot)) (PathRecoverySnapshot, error) {
	observe(PathRecoverySnapshot{State: "recovering", Stage: "succeeded"})
	close(r.published)
	<-r.release
	return PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}, nil
}

func TestPathRecoveryNeverPublishesRecoveringSucceeded(t *testing.T) {
	recoverer := &terminalProgressRecoverer{published: make(chan struct{}), release: make(chan struct{})}
	op := newPathRecoveryOperation(recoverer)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = op.Recover(context.Background(), PathRecoveryRequest{Reason: "manual"})
	}()
	<-recoverer.published

	if got := op.Snapshot(); got.State != "succeeded" || got.Stage != "succeeded" {
		t.Fatalf("transient success snapshot = %+v, want succeeded/succeeded", got)
	}
	close(recoverer.release)
	<-done
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

func TestPathRecoveryNetworkUnavailableIsStableWithoutDetail(t *testing.T) {
	secret := "default route secret"
	recoverer := &scriptedPathRecoverer{
		stages: []string{"observe"},
		err:    &PathRecoveryError{Code: "network_unavailable", Detail: secret},
	}
	op := newPathRecoveryOperation(recoverer)

	snapshot, err := op.Recover(context.Background(), PathRecoveryRequest{Reason: "underlay_changed"})
	var recoveryErr *PathRecoveryError
	if !errors.As(err, &recoveryErr) {
		t.Fatalf("Recover error = %v, want PathRecoveryError", err)
	}
	if snapshot.State != "blocked" || snapshot.ErrorCode != "network_unavailable" || snapshot.Detail != "" {
		t.Fatalf("failure snapshot = %+v", snapshot)
	}
}

type fakeLiveUnderlay struct {
	next     UnderlaySnapshot
	errs     map[string]error
	recorder *chronologicalRecoveryRecorder

	mu    sync.Mutex
	calls []string
}

func (u *fakeLiveUnderlay) record(call string) {
	u.mu.Lock()
	u.calls = append(u.calls, call)
	u.mu.Unlock()
	if u.recorder != nil {
		u.recorder.record(call)
	}
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
	err      error
	recorder *chronologicalRecoveryRecorder

	mu    sync.Mutex
	calls []string
}

func (s *fakeLiveTransportSet) Recover(context.Context) error {
	s.mu.Lock()
	s.calls = append(s.calls, "transport_health")
	s.mu.Unlock()
	if s.recorder != nil {
		s.recorder.record("transport_health")
	}
	return s.err
}

type chronologicalRecoveryRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *chronologicalRecoveryRecorder) record(call string) {
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
}

func (r *chronologicalRecoveryRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func TestLivePathRecovererRunsCaptureSafeOrderAndVerifies(t *testing.T) {
	old := mustUnderlaySnapshot(t, "en0", "192.0.2.1", "192.0.2.0/24")
	next := mustUnderlaySnapshot(t, "en1", "198.51.100.1", "198.51.100.0/24")
	recorder := &chronologicalRecoveryRecorder{}
	underlay := &fakeLiveUnderlay{next: next, errs: map[string]error{}, recorder: recorder}
	transports := &fakeLiveTransportSet{recorder: recorder}
	recoverer := &livePathRecoverer{
		underlay:     underlay,
		transports:   transports,
		tun:          tunHandle{Name: "utun7"},
		previous:     old,
		serverBypass: []string{"203.0.113.9/32"},
		userBypass:   []string{"10.0.0.0/8"},
		verify: func(context.Context) error {
			recorder.record("verify")
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
	gotCalls := recorder.snapshot()
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

// bypassCapturingUnderlay 只为看清 Rebind 实际拿到了哪一组 serverBypass。
type bypassCapturingUnderlay struct {
	next      UnderlaySnapshot
	gotBypass []string
}

func (u *bypassCapturingUnderlay) Observe(context.Context) (UnderlaySnapshot, error) {
	return u.next, nil
}
func (u *bypassCapturingUnderlay) ValidateCapture(context.Context, tunHandle) error { return nil }
func (u *bypassCapturingUnderlay) Rebind(_ context.Context, _ tunHandle, _, _ UnderlaySnapshot, serverBypass, _ []string) error {
	u.gotBypass = append([]string(nil), serverBypass...)
	return nil
}

// 路径恢复此前拿的是**启动时冻结的那份** serverBypass。切到一台新服务器之后,
// 一次 Wi-Fi 切换就足以触发自动路径恢复,而它会用旧集合重装旁路 ——
// 刚切过去的那台不在里面 = 成环,且是自动发生的,用户什么都没做。
func TestLivePathRecovererUsesLiveBypassSet(t *testing.T) {
	next := mustUnderlaySnapshot(t, "en1", "198.51.100.1", "198.51.100.0/24")
	underlay := &bypassCapturingUnderlay{next: next}
	store := newBypassStore([]string{"203.0.113.9/32"}, nil, nil)
	recoverer := &livePathRecoverer{
		underlay:     underlay,
		transports:   &fakeLiveTransportSet{recorder: &chronologicalRecoveryRecorder{}},
		tun:          tunHandle{Name: "utun7"},
		previous:     mustUnderlaySnapshot(t, "en0", "192.0.2.1", "192.0.2.0/24"),
		serverBypass: []string{"203.0.113.9/32"}, // 启动时那份,已陈旧
		bypass:       store,
		verify:       func(context.Context) error { return nil },
	}
	// 切到新服务器:刷新把新集合发布进 store。
	store.set([]string{"203.0.113.9/32", "198.51.100.77/32"}, nil, nil)

	if _, err := recoverer.RecoverPath(context.Background(), PathRecoveryRequest{Reason: "underlay_changed"}, nil); err != nil {
		t.Fatal(err)
	}
	if !containsString(underlay.gotBypass, "198.51.100.77/32") {
		t.Fatalf("路径恢复必须用当前的 bypass 集合,不是启动时那份 —— 否则刚切过去的服务器被漏在外面 = 成环, got %v", underlay.gotBypass)
	}
}
