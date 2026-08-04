package guardian

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getbx/bx/internal/install"
)

// TestRunDaemonDoesNotDiscoverGatewayAtStartup is a static regression guard:
// RunDaemon must not require a default gateway before the socket exists.
// Another VPN can legitimately own the default route via a point-to-point
// utun (no gateway) without that blocking the Guardian daemon from starting;
// the gateway is only an operation-time dependency for planning a barrier
// with server-bypass routes (see barrierContextForRuntime in manager.go).
func TestRunDaemonDoesNotDiscoverGatewayAtStartup(t *testing.T) {
	if _, err := os.Stat("daemon_darwin.go"); err == nil {
		data, readErr := os.ReadFile("daemon_darwin.go")
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(data), "discoverDaemonGateway") {
			t.Fatal("discoverDaemonGateway must be gone: gateway is an operation-time dependency")
		}
	}
	data, err := os.ReadFile("daemon.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "discoverDaemonGateway") {
		t.Fatal("RunDaemon must not call discoverDaemonGateway at startup")
	}
}

func TestDaemonNetworkObserverFollowsDesiredState(t *testing.T) {
	var desiredMu sync.Mutex
	desired := DesiredOff
	currentDesired := func() DesiredState {
		desiredMu.Lock()
		defer desiredMu.Unlock()
		return desired
	}
	setDesired := func(next DesiredState) {
		desiredMu.Lock()
		defer desiredMu.Unlock()
		desired = next
	}
	observer := &fakeDaemonNetworkObserver{
		started:  make(chan struct{}, 2),
		canceled: make(chan struct{}, 2),
	}
	socketPath := filepath.Join(shortSocketDir(t), "observer.sock")
	daemon, err := StartDaemon(context.Background(), DaemonOptions{
		SocketPath:             socketPath,
		Handler:                http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		OwnerUID:               uint32(os.Geteuid()),
		networkObserver:        observer,
		networkObserverDesired: currentDesired,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Close() })

	select {
	case <-observer.started:
		t.Fatal("network observer started while desired state was Off")
	case <-time.After(20 * time.Millisecond):
	}

	setDesired(DesiredOn)
	requestDaemonObserverSync(t, socketPath)
	select {
	case <-observer.started:
	case <-time.After(time.Second):
		t.Fatal("network observer did not start after desired state became On")
	}
	select {
	case <-observer.canceled:
		t.Fatal("request completion canceled daemon-owned network observer")
	case <-time.After(20 * time.Millisecond):
	}

	setDesired(DesiredOff)
	requestDaemonObserverSync(t, socketPath)
	select {
	case <-observer.canceled:
	case <-time.After(time.Second):
		t.Fatal("network observer did not stop after desired state became Off")
	}

	setDesired(DesiredOn)
	requestDaemonObserverSync(t, socketPath)
	select {
	case <-observer.started:
	case <-time.After(time.Second):
		t.Fatal("network observer did not restart after desired state returned to On")
	}
}

func TestDaemonNetworkObserverRapidOffOnRestartsAfterDrain(t *testing.T) {
	observer := newControlledDaemonNetworkObserver()
	lifecycle := newDaemonNetworkObserverLifecycle(context.Background(), observer)
	t.Cleanup(func() {
		lifecycle.beginShutdown()
		observer.releaseAll()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = lifecycle.wait(ctx)
	})

	if err := lifecycle.syncDesired(context.Background(), DesiredOn); err != nil {
		t.Fatal(err)
	}
	first := observer.waitForRun(t)

	offDone := make(chan error, 1)
	go func() {
		offDone <- lifecycle.syncDesired(context.Background(), DesiredOff)
	}()
	first.waitForCancellation(t)

	if err := lifecycle.syncDesired(context.Background(), DesiredOn); err != nil {
		t.Fatal(err)
	}
	first.release()
	if err := <-offDone; err != nil {
		t.Fatalf("Off transition: %v", err)
	}
	second := observer.waitForRun(t)
	select {
	case extra := <-observer.started:
		extra.release()
		t.Fatal("rapid Off -> On started more than one replacement observer")
	default:
	}
	second.release()
}

func TestDaemonNetworkObserverDesiredBurstsCoalesceToOneFinalRun(t *testing.T) {
	observer := newControlledDaemonNetworkObserver()
	lifecycle := newDaemonNetworkObserverLifecycle(context.Background(), observer)
	t.Cleanup(func() {
		lifecycle.beginShutdown()
		observer.releaseAll()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = lifecycle.wait(ctx)
	})

	if err := lifecycle.syncDesired(context.Background(), DesiredOn); err != nil {
		t.Fatal(err)
	}
	first := observer.waitForRun(t)
	offDone := make(chan error, 1)
	go func() {
		offDone <- lifecycle.syncDesired(context.Background(), DesiredOff)
	}()
	first.waitForCancellation(t)

	for _, desired := range []DesiredState{
		DesiredOn,
		DesiredOff,
		DesiredOn,
		DesiredOff,
		DesiredOn,
		DesiredOff,
		DesiredOn,
	} {
		ctx := context.Background()
		if desired == DesiredOff {
			expired, cancel := context.WithCancel(ctx)
			cancel()
			ctx = expired
		}
		_ = lifecycle.syncDesired(ctx, desired)
	}

	first.release()
	if err := <-offDone; err != nil {
		t.Fatalf("initial Off transition: %v", err)
	}
	replacement := observer.waitForRun(t)
	select {
	case extra := <-observer.started:
		extra.release()
		t.Fatal("desired-state burst started more than one replacement observer")
	default:
	}
	replacement.release()
}

func TestDaemonShutdownDrainsNetworkObserverBeforeSocketRemoval(t *testing.T) {
	release := make(chan struct{})
	observer := &fakeDaemonNetworkObserver{
		started:  make(chan struct{}, 1),
		canceled: make(chan struct{}, 1),
		release:  release,
	}
	socketPath := filepath.Join(shortSocketDir(t), "observer-drain.sock")
	daemon, err := StartDaemon(context.Background(), DaemonOptions{
		SocketPath:             socketPath,
		Handler:                http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		OwnerUID:               uint32(os.Geteuid()),
		networkObserver:        observer,
		networkObserverDesired: func() DesiredState { return DesiredOn },
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-observer.started:
	case <-time.After(time.Second):
		t.Fatal("network observer did not start")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdownDone <- daemon.Shutdown(ctx)
	}()
	select {
	case <-observer.canceled:
	case <-time.After(time.Second):
		t.Fatal("daemon shutdown did not cancel network observer")
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Fatalf("Guardian socket removed before observer drain: %v", err)
	}

	close(release)
	if err := <-shutdownDone; err != nil {
		t.Fatalf("daemon shutdown: %v", err)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Guardian socket still present after observer drain: %v", err)
	}
}

func TestDaemonShutdownTimeoutPreservesSocketAndAllowsCleanupRetry(t *testing.T) {
	release := make(chan struct{})
	observer := &fakeDaemonNetworkObserver{
		started:  make(chan struct{}, 1),
		canceled: make(chan struct{}, 1),
		release:  release,
	}
	socketPath := filepath.Join(shortSocketDir(t), "observer-timeout.sock")
	daemon, err := StartDaemon(context.Background(), DaemonOptions{
		SocketPath:             socketPath,
		Handler:                http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		OwnerUID:               uint32(os.Geteuid()),
		networkObserver:        observer,
		networkObserverDesired: func() DesiredState { return DesiredOn },
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-observer.started:
	case <-time.After(time.Second):
		t.Fatal("network observer did not start")
	}

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	err = daemon.Shutdown(expired)
	var shutdownErr *DaemonShutdownError
	if !errors.As(err, &shutdownErr) {
		t.Fatalf("shutdown error = %T %v, want structured DaemonShutdownError", err, err)
	}
	if shutdownErr.Stage != "network_observer_drain" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %+v, want network_observer_drain/deadline", shutdownErr)
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Fatalf("Guardian socket removed after observer drain timeout: %v", err)
	}
	requestDaemonObserverSync(t, socketPath)

	close(release)
	ctx, retryCancel := context.WithTimeout(context.Background(), time.Second)
	defer retryCancel()
	if err := daemon.Shutdown(ctx); err != nil {
		t.Fatalf("retry shutdown: %v", err)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Guardian socket still present after retry cleanup: %v", err)
	}
}

func requestDaemonObserverSync(t *testing.T, socketPath string) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}}
	t.Cleanup(client.CloseIdleConnections)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://guardian/sync", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

type fakeDaemonNetworkObserver struct {
	started  chan struct{}
	canceled chan struct{}
	release  <-chan struct{}
}

func (o *fakeDaemonNetworkObserver) Run(ctx context.Context) {
	o.started <- struct{}{}
	<-ctx.Done()
	o.canceled <- struct{}{}
	if o.release != nil {
		<-o.release
	}
}

type controlledDaemonObserverRun struct {
	canceled   chan struct{}
	releaseCh  chan struct{}
	releaseOne sync.Once
}

func (r *controlledDaemonObserverRun) waitForCancellation(t *testing.T) {
	t.Helper()
	select {
	case <-r.canceled:
	case <-time.After(time.Second):
		t.Fatal("observer run was not canceled")
	}
}

func (r *controlledDaemonObserverRun) release() {
	r.releaseOne.Do(func() { close(r.releaseCh) })
}

type controlledDaemonNetworkObserver struct {
	mu      sync.Mutex
	started chan *controlledDaemonObserverRun
	runs    []*controlledDaemonObserverRun
}

func newControlledDaemonNetworkObserver() *controlledDaemonNetworkObserver {
	return &controlledDaemonNetworkObserver{
		started: make(chan *controlledDaemonObserverRun, 16),
	}
}

func (o *controlledDaemonNetworkObserver) Run(ctx context.Context) {
	run := &controlledDaemonObserverRun{
		canceled:  make(chan struct{}),
		releaseCh: make(chan struct{}),
	}
	o.mu.Lock()
	o.runs = append(o.runs, run)
	o.mu.Unlock()
	o.started <- run
	<-ctx.Done()
	close(run.canceled)
	<-run.releaseCh
}

func (o *controlledDaemonNetworkObserver) waitForRun(t *testing.T) *controlledDaemonObserverRun {
	t.Helper()
	select {
	case run := <-o.started:
		return run
	case <-time.After(time.Second):
		t.Fatal("observer run did not start")
		return nil
	}
}

func (o *controlledDaemonNetworkObserver) releaseAll() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, run := range o.runs {
		run.release()
	}
}

// TestDaemonServesLocalAPIWhileRecoveryIsInFlight is the regression guard for
// the "false Guardian 未能启动" bug: startRecoveredDaemon must open the
// LocalAPI socket and return WITHOUT waiting for the startup Recover to
// finish. Recover (guardianMutationTimeout budget, up to ~60s for a
// recovering Up with Core health waits) legitimately outlasts the client's
// socket-readiness timeout (guardianReadyTimeout, 10s in cli/guardian.go);
// serving first is what lets a slow-but-healthy recovery still be observed
// as "the socket exists" instead of being misreported as a startup failure.
func TestDaemonServesLocalAPIWhileRecoveryIsInFlight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	events := []string{}
	recoverStarted := make(chan struct{})
	releaseRecover := make(chan struct{})
	controller := &daemonStartupController{
		recover: func(context.Context) error {
			close(recoverStarted)
			<-releaseRecover
			mu.Lock()
			events = append(events, "recover")
			mu.Unlock()
			return nil
		},
	}
	start := func(_ context.Context, options DaemonOptions) (*Daemon, error) {
		mu.Lock()
		events = append(events, "serve")
		mu.Unlock()
		if options.Handler == nil {
			t.Fatal("LocalAPI handler was not installed")
		}
		if options.OwnerUID != 0 {
			t.Fatalf("OwnerUID = %d, want root", options.OwnerUID)
		}
		return &Daemon{}, nil
	}

	returned := make(chan struct{})
	go func() {
		if _, err := startRecoveredDaemon(ctx, DaemonOptions{}, controller, start); err != nil {
			t.Error(err)
		}
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("startRecoveredDaemon blocked on Recover instead of serving first")
	}
	select {
	case <-recoverStarted:
	case <-time.After(time.Second):
		t.Fatal("background Recover never started")
	}

	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()
	if !reflect.DeepEqual(got, []string{"serve"}) {
		t.Fatalf("events while Recover is in flight = %#v, want [serve] only (socket must be observable before recovery completes)", got)
	}

	close(releaseRecover)
	eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return reflect.DeepEqual(events, []string{"serve", "recover"})
	})
}

func TestStartRecoveredDaemonWiresConfiguredOwnerIntoLocalAPI(t *testing.T) {
	controller := &daemonStartupController{recover: func(context.Context) error { return nil }}
	var handler http.Handler
	start := func(_ context.Context, options DaemonOptions) (*Daemon, error) {
		handler = options.Handler
		if options.OwnerUID != 0 {
			t.Fatalf("socket OwnerUID = %d, want root", options.OwnerUID)
		}
		return &Daemon{}, nil
	}
	if _, err := startRecoveredDaemon(context.Background(), DaemonOptions{LocalAPIOwnerUID: 501}, controller, start); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/recoveries", strings.NewReader(`{"reason":"manual"}`))
	request = request.WithContext(withPeerCredentials(request.Context(), 501, true))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("configured owner recovery status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestLoadGuardianLocalAPIOwnerUIDFromConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: brook://example\nowner_uid: 501\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ownerUID, err := loadGuardianLocalAPIOwnerUID(path)
	if err != nil {
		t.Fatal(err)
	}
	if ownerUID != 501 {
		t.Fatalf("LocalAPI owner UID = %d, want 501", ownerUID)
	}
}

func TestDaemonRetriesRecoveryWhileServingDiagnostics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	attempts := 0
	retried := make(chan struct{})
	controller := &daemonStartupController{recover: func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if attempts == 1 {
			return errors.New("barrier unavailable")
		}
		select {
		case <-retried:
		default:
			close(retried)
		}
		return nil
	}}
	served := make(chan struct{})
	start := func(_ context.Context, options DaemonOptions) (*Daemon, error) {
		if options.Handler == nil {
			t.Fatal("diagnostics handler was not installed")
		}
		close(served)
		return &Daemon{}, nil
	}

	if _, err := startRecoveredDaemon(ctx, DaemonOptions{}, controller, start); err != nil {
		t.Fatal(err)
	}
	select {
	case <-served:
	case <-time.After(time.Second):
		t.Fatal("daemon did not serve diagnostics after recovery failure")
	}
	select {
	case <-retried:
	case <-time.After(2 * time.Second):
		t.Fatal("daemon discarded recovery failure instead of retrying")
	}
}

// TestStartRecoveredDaemonRaisesFenceBeforeServing is the regression guard
// for the fail-closed window fd1fd90 opened: BeginStartupRecovery (which
// raises Manager.recoveryBlocked and the path-recovery fence) must happen
// SYNCHRONOUSLY, before the socket is handed to `start`, not from inside the
// background recovery goroutine. Otherwise a client that wins the race to
// connect the instant the socket exists could win the mutation channel ahead
// of the background Recover and run before the crash-recovery journal was
// ever inspected.
func TestStartRecoveredDaemonRaisesFenceBeforeServing(t *testing.T) {
	controller := &daemonStartupController{recover: func(context.Context) error { return nil }}
	beganBeforeStart := false
	start := func(_ context.Context, options DaemonOptions) (*Daemon, error) {
		beganBeforeStart = controller.beginCallCount() == 1
		return &Daemon{}, nil
	}
	if _, err := startRecoveredDaemon(context.Background(), DaemonOptions{}, controller, start); err != nil {
		t.Fatal(err)
	}
	if !beganBeforeStart {
		t.Fatal("BeginStartupRecovery was not raised before the listener started accepting")
	}
	if got := controller.abortCallCount(); got != 0 {
		t.Fatalf("AbortStartupRecovery called %d times after a successful start, want 0", got)
	}
}

// TestStartRecoveredDaemonAbortsFenceWhenStartFails proves the fence raised
// by BeginStartupRecovery does not leak when the socket never opens: without
// AbortStartupRecovery, a failed start would leave the path-recovery fence
// permanently raised on the Manager, queuing every future recovery forever —
// worse than the race the fence exists to close.
func TestStartRecoveredDaemonAbortsFenceWhenStartFails(t *testing.T) {
	controller := &daemonStartupController{recover: func(context.Context) error { return nil }}
	start := func(_ context.Context, options DaemonOptions) (*Daemon, error) {
		return nil, errors.New("bind failed")
	}
	if _, err := startRecoveredDaemon(context.Background(), DaemonOptions{}, controller, start); err == nil {
		t.Fatal("expected the start failure to propagate")
	}
	if got := controller.beginCallCount(); got != 1 {
		t.Fatalf("BeginStartupRecovery called %d times, want 1", got)
	}
	if got := controller.abortCallCount(); got != 1 {
		t.Fatalf("AbortStartupRecovery called %d times, want 1", got)
	}
}

// TestDaemonShutdownWaitsForStartupRecovery is the regression guard for the
// unwaited startup-recovery goroutine: before this fix, nothing waited for
// it, so a SIGTERM arriving during the first seconds of a slow recovery could
// let Shutdown return (and the process exit) mid-startCoreLocked, risking an
// orphaned Core or a path-recovery fence left installed. Shutdown must now
// block until the tracked goroutine finishes (bounded by its own ctx).
func TestDaemonShutdownWaitsForStartupRecovery(t *testing.T) {
	socketPath := filepath.Join(shortSocketDir(t), "startup-recovery.sock")
	daemon, err := StartDaemon(context.Background(), DaemonOptions{
		SocketPath: socketPath,
		Handler:    http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		OwnerUID:   uint32(os.Geteuid()),
	})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var finished atomic.Bool
	daemon.trackStartupRecovery(func() {
		close(started)
		<-release
		finished.Store(true)
	})

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tracked startup recovery never started")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		shutdownDone <- daemon.Shutdown(shutdownCtx)
	}()

	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned (%v) before the startup recovery goroutine finished", err)
	case <-time.After(150 * time.Millisecond):
	}
	if finished.Load() {
		t.Fatal("test bug: recovery finished before Shutdown had a chance to observe it in flight")
	}

	close(release)
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not return after the startup recovery goroutine finished")
	}
	if !finished.Load() {
		t.Fatal("Shutdown returned without the tracked startup recovery goroutine completing")
	}
}

// TestDaemonShutdownDoesNotBlockForeverOnStuckStartupRecovery proves the wait
// added for the previous test is bounded, not infinite: a startup recovery
// goroutine that never returns must not hang Shutdown past its own context
// deadline.
func TestDaemonShutdownDoesNotBlockForeverOnStuckStartupRecovery(t *testing.T) {
	socketPath := filepath.Join(shortSocketDir(t), "startup-recovery-stuck.sock")
	daemon, err := StartDaemon(context.Background(), DaemonOptions{
		SocketPath: socketPath,
		Handler:    http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		OwnerUID:   uint32(os.Geteuid()),
	})
	if err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	daemon.trackStartupRecovery(func() { <-block })

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- daemon.Shutdown(shutdownCtx) }()
	select {
	case err := <-shutdownDone:
		if err == nil {
			t.Fatal("Shutdown succeeded despite the tracked startup recovery never finishing")
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown blocked forever on a stuck startup recovery goroutine")
	}
}

// daemonStartupController is a fake recoveringController for daemon-level
// tests. By default RunStartupRecovery, BeginStartupRecovery and
// AbortStartupRecovery just delegate to recover/begin/abort — most tests only
// care about `recover` (aliased as runStartup when set) and never touch the
// begin/abort hooks, so those default to no-ops.
type daemonStartupController struct {
	mu          sync.Mutex
	recover     func(context.Context) error
	runStartup  func(context.Context) error
	beginCalled int
	abortCalled int
}

func (*daemonStartupController) Status() Status                      { return Status{} }
func (*daemonStartupController) Up(context.Context) error            { return nil }
func (*daemonStartupController) Down(context.Context) error          { return nil }
func (c *daemonStartupController) Recover(ctx context.Context) error { return c.recover(ctx) }

func (c *daemonStartupController) BeginStartupRecovery() {
	c.mu.Lock()
	c.beginCalled++
	c.mu.Unlock()
}

func (c *daemonStartupController) RunStartupRecovery(ctx context.Context) error {
	if c.runStartup != nil {
		return c.runStartup(ctx)
	}
	return c.recover(ctx)
}

func (c *daemonStartupController) AbortStartupRecovery() {
	c.mu.Lock()
	c.abortCalled++
	c.mu.Unlock()
}

func (c *daemonStartupController) beginCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.beginCalled
}

func (c *daemonStartupController) abortCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.abortCalled
}

func (*daemonStartupController) RequestPathRecovery(request RecoveryRequest) (RecoverySnapshot, error) {
	return RecoverySnapshot{ID: "recovery-1", State: "accepted", Stage: "queued", Reason: request.Reason, Generation: request.Generation}, nil
}

func (*daemonStartupController) CurrentPathRecovery() RecoverySnapshot {
	return RecoverySnapshot{State: "idle", Stage: "idle"}
}

var _ http.Handler = NewLocalAPI(&daemonStartupController{recover: func(context.Context) error { return nil }})

func TestSystemDNSManagerPropagatesCancellationToAutoDetectedRestore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	dns, ok := systemDNSManager().(dnsManager)
	if !ok {
		t.Fatalf("system DNS manager type = %T, want dnsManager", systemDNSManager())
	}
	dns.restore = func(got context.Context, service string) (install.DNSStatus, error) {
		called = true
		if service != "" {
			t.Fatalf("service = %q, want auto-detect", service)
		}
		return install.DNSStatus{}, got.Err()
	}

	_, err := dns.Restore(ctx)
	if !called {
		t.Fatal("DNS restore was not called")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Restore error = %v, want context canceled", err)
	}
}

func TestSystemLegacyCoreLifecycleForwardsStopAndRemove(t *testing.T) {
	ctx := context.Background()
	var inspected, stopped, removed bool
	lifecycle := systemLegacyCoreLifecycle{
		present: func(got context.Context) (bool, error) {
			inspected = got == ctx
			return true, nil
		},
		stop: func(got context.Context) error {
			stopped = got == ctx
			return nil
		},
		remove: func() error {
			removed = true
			return nil
		},
	}
	present, err := lifecycle.Present(ctx)
	if err != nil || !present {
		t.Fatalf("Present = %v, %v", present, err)
	}
	if err := lifecycle.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Remove(); err != nil {
		t.Fatal(err)
	}
	if !inspected || !stopped || !removed {
		t.Fatalf("legacy lifecycle calls = present:%v stop:%v remove:%v", inspected, stopped, removed)
	}
}

func TestCoreExecutablePinsResolvedSelf(t *testing.T) {
	got := coreExecutable(
		func() (string, error) { return "/Library/Application Support/bx/runtime/current/bx", nil },
		func(path string) (string, error) { return "/Library/Application Support/bx/runtime/1.2.3/bx", nil },
	)
	if got != "/Library/Application Support/bx/runtime/1.2.3/bx" {
		t.Fatalf("coreExecutable = %q", got)
	}
}

func TestCoreExecutableFallsBackToBinPath(t *testing.T) {
	got := coreExecutable(
		func() (string, error) { return "", errors.New("no self") },
		func(path string) (string, error) { return path, nil },
	)
	if got != install.BinPath {
		t.Fatalf("fallback = %q", got)
	}
}

func TestCoreExecutableKeepsPathWhenResolveFails(t *testing.T) {
	got := coreExecutable(
		func() (string, error) { return "/usr/local/bin/bx", nil },
		func(path string) (string, error) { return "", errors.New("eval failed") },
	)
	if got != "/usr/local/bin/bx" {
		t.Fatalf("got %q", got)
	}
}
