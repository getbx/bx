package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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

func TestManagerPathRecoveryFailedUpdatePreservesGeneratedRecoveryAndSameGenerationIdentity(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	env.manager.updates = nil
	if err := env.manager.acquireUpdateOperation(context.Background()); err != nil {
		t.Fatal(err)
	}
	updateOperationHeld := true
	defer func() {
		if updateOperationHeld {
			env.manager.releaseUpdateOperation()
		}
	}()

	first, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-a"})
	if err != nil {
		t.Fatal(err)
	}
	core.waitForRequest(t)

	updateDone := make(chan error, 1)
	go func() {
		_, updateErr := env.manager.Update(context.Background(), UpdateRequest{
			TransactionID: "tx-path-recovery",
			FromVersion:   "v1",
			ToVersion:     "v2",
			AssetSHA256:   strings.Repeat("0", 64),
			PackagePath:   filepath.Join(t.TempDir(), "package.tar.gz"),
		})
		updateDone <- updateErr
	}()
	select {
	case <-core.canceled:
	case <-time.After(time.Second):
		t.Fatal("Update did not cancel active path recovery")
	}
	eventually(t, func() bool { return env.manager.pathRecoveryActiveCount() == 0 })

	duplicate, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-a"})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != first.ID || duplicate.State != "accepted" || duplicate.Stage != "queued" {
		t.Fatalf("same generation during Update = %+v, want queued replay with ID %q", duplicate, first.ID)
	}

	env.manager.releaseUpdateOperation()
	updateOperationHeld = false
	if err := <-updateDone; err == nil || err.Error() != "update_unavailable" {
		t.Fatalf("Update error = %v, want update_unavailable", err)
	}
	if got := core.waitForRequest(t); got.Generation != "wifi-a" {
		t.Fatalf("replayed Core request = %+v, want wifi-a", got)
	}
	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
	eventually(t, func() bool {
		current := env.manager.CurrentPathRecovery()
		return current.ID == first.ID && current.State == "succeeded"
	})
	if got := core.callCount(); got != 2 {
		t.Fatalf("Core calls after failed Update = %d, want interrupted attempt plus replay", got)
	}
}

func TestManagerPathRecoveryPublishesQueuedPendingDuringOnPreservingTransitions(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(*managerTestEnv)
		run        func(*managerTestEnv) error
		wantError  string
		supersedes bool
	}{
		{
			name: "update",
			prepare: func(env *managerTestEnv) {
				env.manager.updates = nil
			},
			run: func(env *managerTestEnv) error {
				_, err := env.manager.Update(context.Background(), unavailablePathRecoveryUpdateRequest("tx-current-update"))
				return err
			},
			wantError:  "update_unavailable",
			supersedes: true,
		},
		{
			name: "migrate",
			run: func(env *managerTestEnv) error {
				return env.manager.Migrate(context.Background(), MigrationRequest{
					Gateway:      "192.0.2.1",
					ServerBypass: []string{"198.51.100.10/32"},
				})
			},
		},
		{
			name: "startup recover",
			run: func(env *managerTestEnv) error {
				return env.manager.Recover(context.Background())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newProtectedManagerTestEnv(t)
			if tt.prepare != nil {
				tt.prepare(env)
			}
			core := newFakeCorePathClient(false)
			env.manager.corePath = core
			handler := NewLocalAPI(env.manager, LocalAPIOptions{OwnerUID: 501})
			if err := env.manager.acquireMutation(context.Background()); err != nil {
				t.Fatal(err)
			}
			mutationHeld := true
			defer func() {
				if mutationHeld {
					env.manager.releaseMutation()
				}
			}()

			first, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-a"})
			if err != nil {
				t.Fatal(err)
			}
			transitionDone := make(chan error, 1)
			go func() { transitionDone <- tt.run(env) }()
			eventually(t, func() bool { return env.manager.pathRecoveryActiveCount() == 0 })

			current := currentPathRecoveryFromAPI(t, handler)
			if current.ID != first.ID || current.Generation != "wifi-a" || current.State != "accepted" || current.Stage != "queued" || current.ErrorCode != "" {
				t.Fatalf("GET current while %s is held = %+v, want queued recovery %q", tt.name, current, first.ID)
			}

			expected := first
			if tt.supersedes {
				newer, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-b"})
				if err != nil {
					t.Fatal(err)
				}
				expected = newer
				current = currentPathRecoveryFromAPI(t, handler)
				if current.ID != newer.ID || current.Generation != "wifi-b" || current.State != "accepted" || current.Stage != "queued" || current.ErrorCode != "" {
					t.Fatalf("GET current after supersession = %+v, want queued recovery %q", current, newer.ID)
				}
			}

			env.manager.releaseMutation()
			mutationHeld = false
			select {
			case err := <-transitionDone:
				if tt.wantError == "" && err != nil {
					t.Fatal(err)
				}
				if tt.wantError != "" && (err == nil || err.Error() != tt.wantError) {
					t.Fatalf("%s error = %v, want %s", tt.name, err, tt.wantError)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s transition did not finish", tt.name)
			}
			if got := core.waitForRequest(t); got.Generation != expected.Generation {
				t.Fatalf("Core request after %s = %+v, want generation %q", tt.name, got, expected.Generation)
			}
			core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
			eventually(t, func() bool {
				current := env.manager.CurrentPathRecovery()
				return current.ID == expected.ID && current.State == "succeeded"
			})
		})
	}
}

func TestManagerPathRecoveryExplicitManualDuringCanceledActiveWindowGetsFreshQueuedWork(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(true)
	env.manager.corePath = core
	env.manager.updates = nil
	handler := NewLocalAPI(env.manager, LocalAPIOptions{OwnerUID: 501})
	if err := env.manager.acquireUpdateOperation(context.Background()); err != nil {
		t.Fatal(err)
	}
	updateOperationHeld := true
	defer func() {
		if updateOperationHeld {
			env.manager.releaseUpdateOperation()
		}
	}()

	first, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	core.waitForRequest(t)
	updateDone := make(chan error, 1)
	go func() {
		_, updateErr := env.manager.Update(context.Background(), unavailablePathRecoveryUpdateRequest("tx-manual-window"))
		updateDone <- updateErr
	}()
	eventually(t, func() bool {
		env.manager.pathRecoveryMu.Lock()
		defer env.manager.pathRecoveryMu.Unlock()
		return env.manager.pathRecoveryFences > 0
	})

	explicit, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.ID == first.ID || explicit.State != "accepted" || explicit.Stage != "queued" {
		t.Fatalf("manual request during canceled-active window = %+v, want fresh queued ID after %q", explicit, first.ID)
	}
	current := currentPathRecoveryFromAPI(t, handler)
	if current.ID != explicit.ID || current.State != "accepted" || current.Stage != "queued" {
		t.Fatalf("GET current manual recovery = %+v, want fresh queued recovery %q", current, explicit.ID)
	}

	core.release(corePathResult{err: context.Canceled})
	eventually(t, func() bool { return env.manager.pathRecoveryActiveCount() == 0 })
	current = currentPathRecoveryFromAPI(t, handler)
	if current.ID != explicit.ID || current.State != "accepted" || current.Stage != "queued" {
		t.Fatalf("GET current after canceled worker exits = %+v, want queued recovery %q", current, explicit.ID)
	}

	env.manager.releaseUpdateOperation()
	updateOperationHeld = false
	if err := <-updateDone; err == nil || err.Error() != "update_unavailable" {
		t.Fatalf("Update error = %v, want update_unavailable", err)
	}
	if got := core.waitForRequest(t); got.Reason != "manual" || got.Generation != "" {
		t.Fatalf("explicit manual Core request = %+v, want generation-less manual", got)
	}
	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
	eventually(t, func() bool {
		current := env.manager.CurrentPathRecovery()
		return current.ID == explicit.ID && current.State == "succeeded"
	})
	if got := core.callCount(); got != 2 {
		t.Fatalf("Core calls = %d, want canceled manual plus explicit manual", got)
	}
}

func TestManagerPathRecoveryOnPreservingTransitionsReplayGeneratedRecovery(t *testing.T) {
	tests := []struct {
		name string
		run  func(*managerTestEnv) error
	}{
		{
			name: "migrate protected Core",
			run: func(env *managerTestEnv) error {
				return env.manager.Migrate(context.Background(), MigrationRequest{
					Gateway:      "192.0.2.1",
					ServerBypass: []string{"198.51.100.10/32"},
				})
			},
		},
		{
			name: "startup recovery no-op",
			run: func(env *managerTestEnv) error {
				return env.manager.Recover(context.Background())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newProtectedManagerTestEnv(t)
			core := newFakeCorePathClient(false)
			env.manager.corePath = core
			first, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-a"})
			if err != nil {
				t.Fatal(err)
			}
			core.waitForRequest(t)

			if err := tt.run(env); err != nil {
				t.Fatal(err)
			}
			if got := core.waitForRequest(t); got.Generation != "wifi-a" {
				t.Fatalf("replayed Core request = %+v, want wifi-a", got)
			}
			core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
			eventually(t, func() bool {
				current := env.manager.CurrentPathRecovery()
				return current.ID == first.ID && current.State == "succeeded"
			})
			if got := core.callCount(); got != 2 {
				t.Fatalf("Core calls = %d, want interrupted attempt plus replay", got)
			}
		})
	}
}

func unavailablePathRecoveryUpdateRequest(transactionID string) UpdateRequest {
	return UpdateRequest{
		TransactionID: transactionID,
		FromVersion:   "v1",
		ToVersion:     "v2",
		AssetSHA256:   strings.Repeat("0", 64),
		PackagePath:   filepath.Join("/tmp", transactionID+".tar.gz"),
	}
}

func currentPathRecoveryFromAPI(t *testing.T, handler http.Handler) RecoverySnapshot {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/recoveries/current", nil)
	request = request.WithContext(withPeerCredentials(request.Context(), 501, true))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET current status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var snapshot RecoverySnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestManagerPathRecoveryNewerPendingGenerationSupersedesInterruptedReplay(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	env.manager.updates = nil
	if err := env.manager.acquireUpdateOperation(context.Background()); err != nil {
		t.Fatal(err)
	}
	updateOperationHeld := true
	defer func() {
		if updateOperationHeld {
			env.manager.releaseUpdateOperation()
		}
	}()

	first, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-a"})
	if err != nil {
		t.Fatal(err)
	}
	core.waitForRequest(t)
	updateDone := make(chan error, 1)
	go func() {
		_, updateErr := env.manager.Update(context.Background(), UpdateRequest{
			TransactionID: "tx-path-recovery-newer",
			FromVersion:   "v1",
			ToVersion:     "v2",
			AssetSHA256:   strings.Repeat("0", 64),
			PackagePath:   filepath.Join(t.TempDir(), "package.tar.gz"),
		})
		updateDone <- updateErr
	}()
	select {
	case <-core.canceled:
	case <-time.After(time.Second):
		t.Fatal("Update did not cancel active path recovery")
	}
	eventually(t, func() bool { return env.manager.pathRecoveryActiveCount() == 0 })

	newer, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-b"})
	if err != nil {
		t.Fatal(err)
	}
	if newer.ID == first.ID || newer.State != "accepted" {
		t.Fatalf("newer pending recovery = %+v, interrupted = %+v", newer, first)
	}
	env.manager.releaseUpdateOperation()
	updateOperationHeld = false
	if err := <-updateDone; err == nil || err.Error() != "update_unavailable" {
		t.Fatalf("Update error = %v, want update_unavailable", err)
	}
	if got := core.waitForRequest(t); got.Generation != "wifi-b" {
		t.Fatalf("Core request after Update = %+v, want newer wifi-b", got)
	}
	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
	eventually(t, func() bool {
		current := env.manager.CurrentPathRecovery()
		return current.ID == newer.ID && current.State == "succeeded"
	})
	if got, want := core.requestsSnapshot(), []supervisor.PathRecoveryRequest{
		{Reason: "underlay_changed", Generation: "wifi-a"},
		{Reason: "underlay_changed", Generation: "wifi-b"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Core requests = %#v, want %#v", got, want)
	}
}

func TestManagerPathRecoveryOnPreservingTransitionDoesNotReplayManualRecovery(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	first, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	core.waitForRequest(t)

	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := core.callCount(); got != 1 {
		t.Fatalf("Core calls after startup recovery = %d, want no implicit manual replay", got)
	}
	if got := env.manager.CurrentPathRecovery(); got.ID != first.ID || got.State != "failed" {
		t.Fatalf("interrupted manual recovery = %+v, want canceled failure for %q", got, first.ID)
	}

	repeated, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.ID == first.ID {
		t.Fatalf("explicit manual recovery reused interrupted ID %q", repeated.ID)
	}
	core.waitForRequest(t)
	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
	eventually(t, func() bool {
		current := env.manager.CurrentPathRecovery()
		return current.ID == repeated.ID && current.State == "succeeded"
	})
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
