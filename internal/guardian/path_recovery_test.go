package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/supervisor"
)

func TestManagerPathRecoveryDeduplicatesSameGenerationAndCoalescesNewestPending(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(true)
	env.manager.corePath = core

	first, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-a"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.State != "accepted" {
		t.Fatalf("first recovery = %+v, want accepted transaction ID", first)
	}
	if got := core.waitForRequest(t); got.Generation != "wifi-a" {
		t.Fatalf("first Core request = %+v", got)
	}

	const duplicates = 16
	results := make(chan RecoverySnapshot, duplicates)
	errs := make(chan error, duplicates)
	var wg sync.WaitGroup
	for range duplicates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot, requestErr := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-a"})
			results <- snapshot
			errs <- requestErr
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for requestErr := range errs {
		if requestErr != nil {
			t.Fatal(requestErr)
		}
	}
	for snapshot := range results {
		if snapshot.ID != first.ID {
			t.Fatalf("duplicate recovery ID = %q, want %q", snapshot.ID, first.ID)
		}
	}
	if got := core.callCount(); got != 1 {
		t.Fatalf("same-generation Core calls = %d, want 1", got)
	}

	second, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-b"})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-c"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || latest.ID == first.ID || latest.ID == second.ID {
		t.Fatalf("recovery IDs were not unique: first=%q second=%q latest=%q", first.ID, second.ID, latest.ID)
	}
	if second.State != "accepted" || latest.State != "accepted" {
		t.Fatalf("pending recoveries = %+v / %+v, want accepted", second, latest)
	}

	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
	if got := core.waitForRequest(t); got.Generation != "wifi-c" {
		t.Fatalf("coalesced Core request = %+v, want newest generation wifi-c", got)
	}
	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded", Attempt: 3}})
	eventually(t, func() bool {
		current := env.manager.CurrentPathRecovery()
		return current.ID == latest.ID && current.State == "succeeded" && current.Attempt == 3
	})
	if got, want := core.requestsSnapshot(), []supervisor.PathRecoveryRequest{
		{Reason: "underlay_changed", Generation: "wifi-a"},
		{Reason: "underlay_changed", Generation: "wifi-c"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Core requests = %#v, want %#v", got, want)
	}
}

func TestManagerPathRecoveryIgnoresRequestsWhileOff(t *testing.T) {
	env := newManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core

	snapshot, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ID == "" || snapshot.State != "ignored" || snapshot.Stage != "off" {
		t.Fatalf("Off recovery = %+v, want ignored transaction", snapshot)
	}
	if got := core.callCount(); got != 0 {
		t.Fatalf("Core calls while Off = %d, want 0", got)
	}
	if got := env.manager.pathRecoveryActiveCount(); got != 0 {
		t.Fatalf("active path recoveries while Off = %d, want 0", got)
	}
}

func TestManagerPathRecoveryCompletedGenerationIsIdempotentAndManualIsRepeatable(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core

	first, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-a"})
	if err != nil {
		t.Fatal(err)
	}
	core.waitForRequest(t)
	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
	eventually(t, func() bool { return env.manager.CurrentPathRecovery().State == "succeeded" })
	completed := env.manager.CurrentPathRecovery()

	duplicate, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(duplicate, completed) || duplicate.ID != first.ID {
		t.Fatalf("completed duplicate = %+v, want original %+v", duplicate, completed)
	}
	if got := env.manager.pathRecoveryActiveCount(); got != 0 {
		t.Fatalf("completed duplicate started %d worker(s)", got)
	}
	if got := core.callCount(); got != 1 {
		t.Fatalf("Core calls after completed duplicate = %d, want 1", got)
	}

	manualOne, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	core.waitForRequest(t)
	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
	eventually(t, func() bool {
		return env.manager.CurrentPathRecovery().ID == manualOne.ID && env.manager.CurrentPathRecovery().State == "succeeded"
	})
	manualTwo, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if manualTwo.ID == manualOne.ID {
		t.Fatalf("generation-less manual recovery reused completed ID %q", manualTwo.ID)
	}
	core.waitForRequest(t)
	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
	eventually(t, func() bool {
		return env.manager.CurrentPathRecovery().ID == manualTwo.ID && env.manager.CurrentPathRecovery().State == "succeeded"
	})
	if got := core.callCount(); got != 3 {
		t.Fatalf("Core calls after repeatable manual requests = %d, want 3", got)
	}
}

func TestManagerPathRecoverySerializesWithLifecycleMutation(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	if err := env.manager.acquireMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			env.manager.releaseMutation()
		}
	}()

	if _, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "manual"}); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-core.entered:
		t.Fatalf("Core recovery overlapped lifecycle mutation: %+v", request)
	case <-time.After(30 * time.Millisecond):
	}

	env.manager.releaseMutation()
	released = true
	core.waitForRequest(t)
	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
	eventually(t, func() bool { return env.manager.CurrentPathRecovery().State == "succeeded" })
}

func TestManagerPathRecoveryShutdownIsDistinctFromUnexpectedCoreRecovery(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	if _, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "manual"}); err != nil {
		t.Fatal(err)
	}
	core.waitForRequest(t)
	eventually(t, func() bool { return env.manager.pathRecoveryActiveCount() == 1 })
	if got := env.manager.recoveryActiveCount(); got != 0 {
		t.Fatalf("unexpected-Core recovery count = %d, want 0", got)
	}

	env.manager.beginRecoveryShutdown()
	if got := env.manager.pathRecoveryActiveCount(); got != 1 {
		t.Fatalf("existing recovery shutdown changed path count to %d", got)
	}
	select {
	case <-core.canceled:
		t.Fatal("existing recovery shutdown canceled path recovery")
	case <-time.After(30 * time.Millisecond):
	}

	env.manager.beginPathRecoveryShutdown()
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := env.manager.waitForPathRecoveries(waitCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-core.canceled:
	default:
		t.Fatal("path recovery shutdown did not cancel Core work")
	}
	if _, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "manual"}); !errors.Is(err, errPathRecoveryShuttingDown) {
		t.Fatalf("request after path shutdown error = %v, want shutdown", err)
	}
}

func TestExecCoreRunnerPathRecoveryUsesConfiguredControlSocket(t *testing.T) {
	socketPath := filepath.Join(shortSocketDir(t), "core.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan supervisor.PathRecoveryRequest, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v0/path-recovery" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		var got supervisor.PathRecoveryRequest
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		received <- got
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"})
	})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-serveDone
	})

	runner := NewExecCoreRunner("/unused/bx", "/unused/config.yaml", "127.0.0.1:53")
	runner.ControlSocket = socketPath
	snapshot, err := runner.RecoverPath(context.Background(), supervisor.PathRecoveryRequest{Reason: "manual", Generation: "wifi-b"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != "succeeded" || snapshot.Stage != "succeeded" {
		t.Fatalf("Core snapshot = %+v", snapshot)
	}
	if got, want := <-received, (supervisor.PathRecoveryRequest{Reason: "manual", Generation: "wifi-b"}); got != want {
		t.Fatalf("Core request = %+v, want %+v", got, want)
	}
}

func TestCompletedPathRecoverySnapshotRequiresExplicitSucceededState(t *testing.T) {
	secret := "vless://user:password@example.test?token=secret"
	tests := []struct {
		name      string
		result    supervisor.PathRecoverySnapshot
		wantState string
		wantStage string
		wantCode  string
	}{
		{
			name:      "explicit success",
			result:    supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded", ErrorCode: "transport_unavailable", Detail: secret},
			wantState: "succeeded",
			wantStage: "succeeded",
		},
		{
			name:      "still recovering",
			result:    supervisor.PathRecoverySnapshot{State: "recovering", Stage: "observe", Detail: secret},
			wantState: "failed",
			wantStage: "observe",
			wantCode:  "recovery_failed",
		},
		{
			name:      "empty state",
			result:    supervisor.PathRecoverySnapshot{Detail: secret},
			wantState: "failed",
			wantStage: "failed",
			wantCode:  "recovery_failed",
		},
		{
			name:      "unknown future state",
			result:    supervisor.PathRecoverySnapshot{State: "future_state", Stage: "future_stage", Detail: secret},
			wantState: "failed",
			wantStage: "failed",
			wantCode:  "recovery_failed",
		},
		{
			name:      "blocked stable failure",
			result:    supervisor.PathRecoverySnapshot{State: "blocked", Stage: "blocked", ErrorCode: "transport_unavailable", Detail: secret},
			wantState: "failed",
			wantStage: "blocked",
			wantCode:  "transport_unavailable",
		},
	}
	base := RecoverySnapshot{ID: "recovery-1", State: "running", Stage: "core_recovery", Reason: "manual", Attempt: 1}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := completedPathRecoverySnapshot(base, tt.result)
			if got.State != tt.wantState || got.Stage != tt.wantStage || got.ErrorCode != tt.wantCode {
				t.Fatalf("snapshot = %+v, want state/stage/code %q/%q/%q", got, tt.wantState, tt.wantStage, tt.wantCode)
			}
			if got.Detail != "" || strings.Contains(fmt.Sprintf("%+v", got), secret) {
				t.Fatalf("snapshot leaked Core detail: %+v", got)
			}
		})
	}
}

type corePathResult struct {
	snapshot supervisor.PathRecoverySnapshot
	err      error
}

type fakeCorePathClient struct {
	mu                 sync.Mutex
	requests           []supervisor.PathRecoveryRequest
	entered            chan supervisor.PathRecoveryRequest
	releases           chan corePathResult
	canceled           chan struct{}
	ignoreCancellation bool
	cancelOnce         sync.Once
}

func newFakeCorePathClient(ignoreCancellation bool) *fakeCorePathClient {
	return &fakeCorePathClient{
		entered:            make(chan supervisor.PathRecoveryRequest, 16),
		releases:           make(chan corePathResult, 16),
		canceled:           make(chan struct{}),
		ignoreCancellation: ignoreCancellation,
	}
}

func (c *fakeCorePathClient) RecoverPath(ctx context.Context, request supervisor.PathRecoveryRequest) (supervisor.PathRecoverySnapshot, error) {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.mu.Unlock()
	c.entered <- request
	if c.ignoreCancellation {
		result := <-c.releases
		return result.snapshot, result.err
	}
	select {
	case result := <-c.releases:
		return result.snapshot, result.err
	case <-ctx.Done():
		c.cancelOnce.Do(func() { close(c.canceled) })
		return supervisor.PathRecoverySnapshot{}, ctx.Err()
	}
}

func (c *fakeCorePathClient) waitForRequest(t *testing.T) supervisor.PathRecoveryRequest {
	t.Helper()
	select {
	case request := <-c.entered:
		return request
	case <-time.After(time.Second):
		t.Fatal("Core path recovery was not called")
		return supervisor.PathRecoveryRequest{}
	}
}

func (c *fakeCorePathClient) release(result corePathResult) {
	c.releases <- result
}

func (c *fakeCorePathClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

func (c *fakeCorePathClient) requestsSnapshot() []supervisor.PathRecoveryRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]supervisor.PathRecoveryRequest(nil), c.requests...)
}
