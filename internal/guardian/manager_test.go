package guardian

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/getbx/bx/internal/version"
)

func TestManagerUpStartsOneCoreAndPersistsOn(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.runner.startCount(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
	if got, _ := env.store.LoadDesired(); got != DesiredOn {
		t.Fatalf("desired = %q, want %q", got, DesiredOn)
	}
	status := env.manager.Status()
	if status.Protection != ProtectionProtected || status.CorePID == 0 {
		t.Fatalf("status = %+v, want protected Core", status)
	}
}

func TestManagerDownTransitionsBehindBarrier(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"barrier.install", "core.stop", "network.restore", "desired.off", "barrier.remove"}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if got := env.manager.Status(); got.Desired != DesiredOff || got.Protection != ProtectionOff {
		t.Fatalf("status = %+v, want off", got)
	}
	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("repeated Down() error = %v", err)
	}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("repeated Down changed events = %#v", got)
	}
}

func TestManagerAdoptsMatchingHealthyCore(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.existing = Process{PID: 42, Executable: install.BinPath, UID: 0}
	env.health.runtime = healthyRuntime(42)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("unexpected starts = %d", got)
	}
	if got := env.manager.Status(); got.CorePID != 42 || got.Protection != ProtectionProtected {
		t.Fatalf("status = %+v, want adopted protected Core", got)
	}
}

func TestManagerRejectsUnverifiableExistingPID(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.existing = Process{PID: 42, Executable: "/tmp/not-bx", UID: 501}
	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("unverifiable process adopted")
	}
	if got := env.runner.signalCount(); got != 0 {
		t.Fatalf("unrelated process was signalled %d times", got)
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("second Core started beside unverifiable PID: starts=%d", got)
	}
}

func TestManagerRejectsRuntimePIDMismatchWithoutSignalling(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.existing = Process{PID: 42, Executable: install.BinPath, UID: 0}
	env.health.runtime = healthyRuntime(43)
	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("runtime PID mismatch was adopted")
	}
	if got := env.runner.signalCount(); got != 0 {
		t.Fatalf("existing process was signalled %d times", got)
	}
}

func TestManagerUpInspectionFailureDoesNotStartSecondCore(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.existingErr = errors.New("inspect permission denied")
	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("Up succeeded despite ambiguous process inspection")
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("second Core started after inspection failure: starts=%d", got)
	}
	if got := env.manager.Status(); got.Protection != ProtectionNeedsAttention {
		t.Fatalf("status = %+v, want needs_attention", got)
	}
}

func TestManagerUpFailureDoesNotClaimBarrierProtection(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.startErr = errors.New("start failed")
	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("Up succeeded despite start failure")
	}
	if got := env.manager.Status(); got.Protection != ProtectionNeedsAttention {
		t.Fatalf("protection = %q, want %q without an installed barrier", got.Protection, ProtectionNeedsAttention)
	}
}

func TestManagerSerializesMutations(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.blockStart = make(chan struct{})
	firstDone := make(chan error, 1)
	go func() { firstDone <- env.manager.Up(context.Background()) }()
	select {
	case <-env.runner.startEntered:
	case <-time.After(time.Second):
		t.Fatal("Up did not enter Core start")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- env.manager.Down(context.Background()) }()
	select {
	case err := <-secondDone:
		t.Fatalf("Down overlapped Up: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(env.runner.blockStart)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestManagerQueuedExpiredMutationPerformsNoWrites(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.blockStart = make(chan struct{})
	firstDone := make(chan error, 1)
	go func() { firstDone <- env.manager.Up(context.Background()) }()
	select {
	case <-env.runner.startEntered:
	case <-time.After(time.Second):
		t.Fatal("Up did not enter Core start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() { secondDone <- env.manager.Down(ctx) }()
	<-ctx.Done()
	close(env.runner.blockStart)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired queued Down error = %v, want deadline exceeded", err)
	}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, []string{"desired.on", "core.start"}) {
		t.Fatalf("expired queued Down mutated state: events=%#v", got)
	}
	if got, err := env.store.LoadDesired(); err != nil || got != DesiredOn {
		t.Fatalf("desired after expired Down = %q, %v; want on", got, err)
	}
	if got := env.manager.Status(); got.Protection != ProtectionProtected {
		t.Fatalf("status after expired Down = %+v, want protected", got)
	}
}

func TestManagerDownRestoreFailureRecoversProtectedCore(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.restorer.err = errors.New("dns restore failed")
	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("Down succeeded despite restoration failure")
	}
	want := []string{"barrier.install", "core.stop", "network.restore", "core.start", "barrier.remove"}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if got, _ := env.store.LoadDesired(); got != DesiredOn {
		t.Fatalf("desired = %q, want recovery to preserve on", got)
	}
	if got := env.manager.Status(); got.Protection != ProtectionProtected || got.Phase == PhaseNeedsAttention {
		t.Fatalf("status = %+v, want recovered protection", got)
	}
}

func TestManagerDownDoubleFailureKeepsBarrierAndNeedsAttention(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.restorer.err = errors.New("dns restore failed")
	env.runner.startErr = errors.New("Core restart failed")
	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("Down succeeded despite restoration and recovery failures")
	}
	want := []string{"barrier.install", "core.stop", "network.restore", "core.start"}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if got := env.manager.Status(); got.Phase != PhaseNeedsAttention || got.Protection != ProtectionBlocked {
		t.Fatalf("status = %+v, want blocked needs_attention", got)
	}
	if got, _ := env.store.LoadDesired(); got != DesiredOn {
		t.Fatalf("desired = %q, want on", got)
	}
}

func TestManagerUnexpectedExitInstallsBarrierAndRestartsOnce(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	process := env.runner.currentProcess()
	env.events.reset()
	env.runner.exit(process.PID, errors.New("unexpected exit"))
	eventually(t, func() bool { return env.runner.startCount() == 2 })
	eventually(t, func() bool { return env.manager.Status().Protection == ProtectionProtected })
	want := []string{"barrier.install", "core.start", "barrier.remove"}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestManagerUnexpectedExitDesiredReadFailureFailsClosed(t *testing.T) {
	tests := []struct {
		name           string
		barrierErr     error
		wantProtection string
	}{
		{name: "barrier retained", wantProtection: ProtectionBlocked},
		{name: "barrier install fails", barrierErr: errors.New("barrier unavailable"), wantProtection: ProtectionNeedsAttention},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newProtectedManagerTestEnv(t)
			process := env.runner.currentProcess()
			env.events.reset()
			env.store.setLoadError(errors.New("desired state unreadable"))
			env.barrier.installErr = tt.barrierErr

			env.runner.exit(process.PID, errors.New("unexpected exit"))
			eventually(t, func() bool {
				return env.manager.Status().LastError == "desired_state_read_failed"
			})

			if got := env.events.snapshot(); !reflect.DeepEqual(got, []string{"barrier.install"}) {
				t.Fatalf("events = %#v, want fail-closed barrier attempt", got)
			}
			if got := env.runner.startCount(); got != 1 {
				t.Fatalf("Core restarted without readable desired state: starts=%d", got)
			}
			if got := env.manager.Status(); got.Desired != DesiredOn || got.Phase != PhaseNeedsAttention || got.Protection != tt.wantProtection {
				t.Fatalf("status = %+v, want desired on needs_attention protection %q", got, tt.wantProtection)
			}
			if got := env.manager.current.PID; got != process.PID {
				t.Fatalf("current PID cleared after ambiguous desired state: got %d, want %d", got, process.PID)
			}
		})
	}
}

func TestManagerHealthyRecoveryReleasesHeldBarrierBeforeProtected(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Manager, context.Context) error
	}{
		{name: "Up", run: (*Manager).Up},
		{name: "Recover", run: (*Manager).Recover},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newProtectedManagerTestEnv(t)
			retainBarrierAfterDesiredReadFailure(t, env)
			env.store.setLoadError(nil)
			env.events.reset()

			healthPassed := false
			env.health.onSuccess = func() { healthPassed = true }
			env.barrier.onRemove = func() {
				if !healthPassed {
					t.Error("held barrier removal started before health passed")
				}
				if status := env.manager.Status(); status.Protection == ProtectionProtected {
					t.Errorf("status was Protected before held barrier removal: %+v", status)
				}
			}

			if err := tt.run(env.manager, context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := env.events.snapshot(); !reflect.DeepEqual(got, []string{"core.start", "barrier.remove"}) {
				t.Fatalf("events = %#v, want health-gated barrier release", got)
			}
			if env.manager.barrierHeld {
				t.Fatal("barrier remains held after healthy recovery")
			}
			if got := env.manager.Status(); got.Protection != ProtectionProtected || got.Phase != PhaseCommitted {
				t.Fatalf("status = %+v, want Protected only after barrier removal", got)
			}
		})
	}
}

func TestManagerHeldBarrierRemainsWhenRecoveryHealthFails(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	retainBarrierAfterDesiredReadFailure(t, env)
	env.store.setLoadError(nil)
	env.health.err = errors.New("Core unhealthy")
	env.events.reset()

	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("Up succeeded despite recovery health failure")
	}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, []string{"core.start", "core.stop"}) {
		t.Fatalf("events = %#v, want no barrier removal before health", got)
	}
	if !env.manager.barrierHeld {
		t.Fatal("barrier released after recovery health failure")
	}
	if got := env.manager.Status(); got.Protection != ProtectionBlocked || got.Phase != PhaseNeedsAttention || got.LastError != "core_health_failed" {
		t.Fatalf("status = %+v, want blocked core_health_failed", got)
	}
}

func TestManagerHeldBarrierRemovalFailureDoesNotClaimProtected(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	retainBarrierAfterDesiredReadFailure(t, env)
	env.store.setLoadError(nil)
	env.barrier.removeErr = errors.New("barrier remove failed")
	env.events.reset()

	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("Up succeeded despite held barrier removal failure")
	}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, []string{"core.start", "barrier.remove"}) {
		t.Fatalf("events = %#v, want attempted post-health barrier removal", got)
	}
	if !env.manager.barrierHeld {
		t.Fatal("barrier state cleared after removal failure")
	}
	if got := env.manager.Status(); got.Protection != ProtectionBlocked || got.Phase != PhaseNeedsAttention || got.LastError != "barrier_remove_failed" {
		t.Fatalf("status = %+v, want blocked barrier_remove_failed", got)
	}
}

func retainBarrierAfterDesiredReadFailure(t *testing.T, env *managerTestEnv) {
	t.Helper()
	process := env.runner.currentProcess()
	env.store.setLoadError(errors.New("desired state unreadable"))
	env.runner.exit(process.PID, errors.New("unexpected exit"))
	eventually(t, func() bool {
		return env.manager.Status().LastError == "desired_state_read_failed"
	})
	if !env.manager.barrierHeld {
		t.Fatal("fail-closed setup did not retain barrier")
	}
}

func TestManagerPlannedDownDoesNotRestartExitedCore(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.runner.exitOnStop = true
	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if got := env.runner.startCount(); got != 1 {
		t.Fatalf("planned stop restarted Core: starts=%d", got)
	}
}

type managerTestEnv struct {
	manager  *Manager
	store    *recordingDesiredStore
	runner   *fakeCoreRunner
	health   *fakeHealthGate
	barrier  *fakeBarrier
	restorer *fakeNetworkRestorer
	events   *eventLog
}

func newManagerTestEnv(t *testing.T) *managerTestEnv {
	t.Helper()
	events := &eventLog{}
	store := &recordingDesiredStore{Store: OpenStore(Paths{
		Desired:     filepath.Join(t.TempDir(), "guardian-state.json"),
		Transaction: filepath.Join(t.TempDir(), "transaction.json"),
		Receipt:     filepath.Join(t.TempDir(), "receipt.json"),
		Staging:     filepath.Join(t.TempDir(), "staging"),
		Snapshots:   filepath.Join(t.TempDir(), "snapshots"),
	}), events: events}
	runner := newFakeCoreRunner(events)
	health := &fakeHealthGate{}
	barrier := &fakeBarrier{events: events}
	restorer := &fakeNetworkRestorer{events: events}
	manager, err := NewManager(ManagerOptions{
		Store:          store,
		Runner:         runner,
		Health:         health,
		Barrier:        barrier,
		Restorer:       restorer,
		BarrierContext: BarrierContext{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}, BlockIPv6: true},
		CoreVersion:    version.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &managerTestEnv{manager: manager, store: store, runner: runner, health: health, barrier: barrier, restorer: restorer, events: events}
}

func newProtectedManagerTestEnv(t *testing.T) *managerTestEnv {
	t.Helper()
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	env.events.reset()
	return env
}

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func (l *eventLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = nil
}

type recordingDesiredStore struct {
	*Store
	events  *eventLog
	mu      sync.Mutex
	loadErr error
}

func (s *recordingDesiredStore) LoadDesired() (DesiredState, error) {
	s.mu.Lock()
	err := s.loadErr
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return s.Store.LoadDesired()
}

func (s *recordingDesiredStore) setLoadError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadErr = err
}

func (s *recordingDesiredStore) SaveDesired(desired DesiredState) error {
	if err := s.Store.SaveDesired(desired); err != nil {
		return err
	}
	s.events.add("desired." + string(desired))
	return nil
}

type fakeCoreRunner struct {
	mu           sync.Mutex
	events       *eventLog
	existing     Process
	existingErr  error
	current      Process
	exits        map[int]chan error
	nextPID      int
	starts       int
	signals      int
	startErr     error
	stopErr      error
	verifyErr    error
	blockStart   chan struct{}
	startEntered chan struct{}
	exitOnStop   bool
}

func newFakeCoreRunner(events *eventLog) *fakeCoreRunner {
	return &fakeCoreRunner{events: events, exits: make(map[int]chan error), nextPID: 100, startEntered: make(chan struct{}, 1)}
}

func (r *fakeCoreRunner) Existing(context.Context) (Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.existing, r.existingErr
}

func (r *fakeCoreRunner) Verify(process Process) error {
	if r.verifyErr != nil {
		return r.verifyErr
	}
	if process.UID != 0 || process.Executable != install.BinPath {
		return fmt.Errorf("unverifiable Core process")
	}
	return nil
}

func (r *fakeCoreRunner) Start(context.Context) (Process, error) {
	r.events.add("core.start")
	r.mu.Lock()
	r.starts++
	startErr := r.startErr
	block := r.blockStart
	select {
	case r.startEntered <- struct{}{}:
	default:
	}
	if startErr != nil {
		r.mu.Unlock()
		return Process{}, startErr
	}
	r.nextPID++
	exit := make(chan error, 1)
	process := Process{PID: r.nextPID, Executable: install.BinPath, UID: 0, Exit: exit}
	r.current = process
	r.exits[process.PID] = exit
	r.mu.Unlock()
	if block != nil {
		<-block
	}
	return process, nil
}

func (r *fakeCoreRunner) Stop(_ context.Context, process Process) error {
	r.events.add("core.stop")
	r.mu.Lock()
	r.signals++
	err := r.stopErr
	exitOnStop := r.exitOnStop
	exit := r.exits[process.PID]
	r.mu.Unlock()
	if exitOnStop && exit != nil {
		select {
		case exit <- nil:
		default:
		}
	}
	return err
}

func (r *fakeCoreRunner) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

func (r *fakeCoreRunner) signalCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.signals
}

func (r *fakeCoreRunner) currentProcess() Process {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

func (r *fakeCoreRunner) exit(pid int, err error) {
	r.mu.Lock()
	exit := r.exits[pid]
	r.mu.Unlock()
	if exit != nil {
		exit <- err
	}
}

type fakeHealthGate struct {
	mu        sync.Mutex
	runtime   supervisor.RuntimeState
	err       error
	last      HealthTarget
	onSuccess func()
}

func (h *fakeHealthGate) Wait(_ context.Context, target HealthTarget) (supervisor.RuntimeState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = target
	if h.err != nil {
		return supervisor.RuntimeState{}, h.err
	}
	state := h.runtime
	if state.PID == 0 {
		state = healthyRuntime(target.PID)
	}
	if h.onSuccess != nil {
		h.onSuccess()
	}
	return state, nil
}

func healthyRuntime(pid int) supervisor.RuntimeState {
	return supervisor.RuntimeState{
		Version: version.Version, PID: pid, TunName: "utun7", SocksAddr: "127.0.0.1:43210",
		ServerBypass: []string{"198.51.100.10/32"}, TunnelHealthy: true, DNSListening: true, RoutesInstalled: true,
	}
}

type fakeBarrier struct {
	events     *eventLog
	installErr error
	removeErr  error
	onRemove   func()
}

func (b *fakeBarrier) Install(context.Context, BarrierContext) error {
	b.events.add("barrier.install")
	return b.installErr
}

func (b *fakeBarrier) ReassertBypass(context.Context, BarrierContext) error { return nil }

func (b *fakeBarrier) Remove(context.Context, BarrierContext) error {
	b.events.add("barrier.remove")
	if b.onRemove != nil {
		b.onRemove()
	}
	return b.removeErr
}

type fakeNetworkRestorer struct {
	events *eventLog
	err    error
}

func (r *fakeNetworkRestorer) Restore(context.Context) error {
	r.events.add("network.restore")
	return r.err
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}
