package guardian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/getbx/bx/internal/supervisor"
)

func TestLocalAPIStatusIsReadableWithoutPeerCredentials(t *testing.T) {
	controller := &fakeController{status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, CorePID: 42, Protection: ProtectionProtected}}
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.CorePID != 42 || got.Protection != ProtectionProtected {
		t.Fatalf("response = %+v", got)
	}
}

func TestLocalAPIMutationsRequireRootPeer(t *testing.T) {
	tests := []struct {
		name      string
		uid       uint32
		gotUID    bool
		wantCode  int
		wantCalls int
	}{
		{name: "missing credentials", wantCode: http.StatusForbidden},
		{name: "logged-in user", uid: 501, gotUID: true, wantCode: http.StatusForbidden},
		{name: "root", uid: 0, gotUID: true, wantCode: http.StatusOK, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &fakeController{status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected}}
			request := httptest.NewRequest(http.MethodPost, "/v1/up", nil)
			request = request.WithContext(withPeerCredentials(request.Context(), tt.uid, tt.gotUID))
			recorder := httptest.NewRecorder()
			NewLocalAPI(controller).ServeHTTP(recorder, request)
			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
			if controller.upCalls != tt.wantCalls {
				t.Fatalf("Up calls = %d, want %d", controller.upCalls, tt.wantCalls)
			}
		})
	}
}

func TestLocalAPIDownReturnsControllerFailure(t *testing.T) {
	controller := &fakeController{downErr: errors.New("restore failed")}
	request := httptest.NewRequest(http.MethodPost, "/v1/down", nil)
	request = request.WithContext(withPeerCredentials(request.Context(), 0, true))
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if controller.downCalls != 1 {
		t.Fatalf("Down calls = %d", controller.downCalls)
	}
}

func TestLocalAPIMigrateRequiresRootAndStrictMetadata(t *testing.T) {
	validBody := []byte(`{"gateway":"192.0.2.1","server_bypass":["198.51.100.10/32"]}`)
	tests := []struct {
		name      string
		uid       uint32
		gotUID    bool
		body      []byte
		wantCode  int
		wantCalls int
	}{
		{name: "logged-in user", uid: 501, gotUID: true, body: validBody, wantCode: http.StatusForbidden},
		{name: "unknown secret field", uid: 0, gotUID: true, body: []byte(`{"gateway":"192.0.2.1","server_bypass":["198.51.100.10/32"],"server_link":"vless://secret"}`), wantCode: http.StatusBadRequest},
		{name: "root metadata", uid: 0, gotUID: true, body: validBody, wantCode: http.StatusOK, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &fakeController{status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected}}
			request := httptest.NewRequest(http.MethodPost, "/v1/migrate", bytes.NewReader(tt.body))
			request = request.WithContext(withPeerCredentials(request.Context(), tt.uid, tt.gotUID))
			recorder := httptest.NewRecorder()
			NewLocalAPI(controller).ServeHTTP(recorder, request)
			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
			if controller.migrateCalls != tt.wantCalls {
				t.Fatalf("Migrate calls = %d, want %d", controller.migrateCalls, tt.wantCalls)
			}
			if tt.wantCalls == 1 && !reflect.DeepEqual(controller.migration, MigrationRequest{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}}) {
				t.Fatalf("migration request = %+v", controller.migration)
			}
		})
	}
}

func TestLocalAPIMigrateRejectsDataBeyondBodyLimit(t *testing.T) {
	controller := &fakeController{}
	body := []byte(`{"gateway":"192.0.2.1","server_bypass":["198.51.100.10/32"]}`)
	body = append(body, bytes.Repeat([]byte(" "), (64<<10)-len(body))...)
	body = append(body, []byte(`{"server_link":"vless://secret"}`)...)
	request := httptest.NewRequest(http.MethodPost, "/v1/migrate", bytes.NewReader(body))
	request = request.WithContext(withPeerCredentials(request.Context(), 0, true))
	recorder := httptest.NewRecorder()

	NewLocalAPI(controller).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if controller.migrateCalls != 0 {
		t.Fatal("oversized migration metadata reached controller")
	}
}

func TestLocalAPIUpdateRequiresRootAndStrictMetadata(t *testing.T) {
	validBody := []byte(`{"transaction_id":"tx-1","from_version":"v1","to_version":"v2","asset_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","package_path":"/var/lib/bx/update/staging/tx-1/package.tar.gz","app_path":"/Applications/Bx.app","app_uid":0,"app_gid":0}`)
	tests := []struct {
		name      string
		uid       uint32
		gotUID    bool
		body      []byte
		wantCode  int
		wantCalls int
	}{
		{name: "missing credentials", body: validBody, wantCode: http.StatusForbidden},
		{name: "non-root", uid: 501, gotUID: true, body: validBody, wantCode: http.StatusForbidden},
		{name: "unknown bypass field", uid: 0, gotUID: true, body: append(bytes.TrimSuffix(validBody, []byte("}")), []byte(`,"gateway":"192.0.2.1"}`)...), wantCode: http.StatusBadRequest},
		{name: "unknown secret field", uid: 0, gotUID: true, body: append(bytes.TrimSuffix(validBody, []byte("}")), []byte(`,"client_link":"vless://secret"}`)...), wantCode: http.StatusBadRequest},
		{name: "trailing JSON", uid: 0, gotUID: true, body: append(validBody, []byte(` {}`)...), wantCode: http.StatusBadRequest},
		{name: "root metadata", uid: 0, gotUID: true, body: validBody, wantCode: http.StatusOK, wantCalls: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &fakeController{
				status:       Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected},
				updateResult: UpdateResult{FromVersion: "v1", ToVersion: "v2", Phase: PhaseCommitted, CoreActivated: true, ProtectionState: ProtectionProtected},
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/update", bytes.NewReader(tt.body))
			request = request.WithContext(withPeerCredentials(request.Context(), tt.uid, tt.gotUID))
			recorder := httptest.NewRecorder()

			NewLocalAPI(controller).ServeHTTP(recorder, request)

			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
			if controller.updateCalls != tt.wantCalls {
				t.Fatalf("Update calls = %d, want %d", controller.updateCalls, tt.wantCalls)
			}
			if strings.Contains(strings.ToLower(recorder.Body.String()), "vless://") {
				t.Fatalf("response reflected secret metadata: %s", recorder.Body.String())
			}
		})
	}
}

func TestLocalAPIUpdateReturnsResultAndHidesControllerFailure(t *testing.T) {
	body := []byte(`{"transaction_id":"tx-1","from_version":"v1","to_version":"v2","asset_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","package_path":"/var/lib/bx/update/staging/tx-1/package.tar.gz"}`)
	for _, tt := range []struct {
		name       string
		controller *fakeController
		wantCode   int
	}{
		{
			name: "result",
			controller: &fakeController{updateResult: UpdateResult{
				FromVersion: "v1", ToVersion: "v2", Phase: PhaseCommitted,
				CoreActivated: true, ProtectionState: ProtectionProtected,
			}},
			wantCode: http.StatusOK,
		},
		{
			name:       "failure redacted",
			controller: &fakeController{updateErr: errors.New("vless://user:password@example.test?token=secret")},
			wantCode:   http.StatusInternalServerError,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/update", bytes.NewReader(body))
			request = request.WithContext(withPeerCredentials(request.Context(), 0, true))
			recorder := httptest.NewRecorder()
			NewLocalAPI(tt.controller).ServeHTTP(recorder, request)
			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
			if strings.Contains(strings.ToLower(recorder.Body.String()), "password") || strings.Contains(strings.ToLower(recorder.Body.String()), "token=") {
				t.Fatalf("response leaked controller error: %s", recorder.Body.String())
			}
			if tt.wantCode == http.StatusOK {
				var got UpdateResult
				if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, tt.controller.updateResult) {
					t.Fatalf("result = %+v, want %+v", got, tt.controller.updateResult)
				}
			}
		})
	}
}

func TestLocalAPIUpdateRejectsDataBeyondBodyLimit(t *testing.T) {
	controller := &fakeController{}
	body := []byte(`{"transaction_id":"tx-1","from_version":"v1","to_version":"v2","asset_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","package_path":"/var/lib/bx/update/staging/tx-1/package.tar.gz"}`)
	body = append(body, bytes.Repeat([]byte(" "), (64<<10)-len(body))...)
	body = append(body, []byte(`{"client_link":"vless://secret"}`)...)
	request := httptest.NewRequest(http.MethodPost, "/v1/update", bytes.NewReader(body))
	request = request.WithContext(withPeerCredentials(request.Context(), 0, true))
	recorder := httptest.NewRecorder()

	NewLocalAPI(controller).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if controller.updateCalls != 0 {
		t.Fatal("oversized update metadata reached controller")
	}
}

func TestLocalAPIMutationOutlivesClientCancellation(t *testing.T) {
	controller := &fakeController{}
	request := httptest.NewRequest(http.MethodPost, "/v1/up", nil)
	requestContext, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(withPeerCredentials(requestContext, 0, true))
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if controller.upContextErr != nil {
		t.Fatalf("accepted mutation inherited client cancellation: %v", controller.upContextErr)
	}
}

func TestRecoveryLocalAPIPostReturnsAcceptedWhileGetRemainsResponsive(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	handler := NewLocalAPI(env.manager, LocalAPIOptions{OwnerUID: 501})

	request := httptest.NewRequest(http.MethodPost, "/v1/recoveries", strings.NewReader(`{"reason":"underlay_changed","generation":"wifi-b"}`))
	request = request.WithContext(withPeerCredentials(request.Context(), 501, true))
	recorder := httptest.NewRecorder()
	started := time.Now()
	handler.ServeHTTP(recorder, request)
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("POST waited for Core work: %s", elapsed)
	}
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("POST status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var accepted RecoverySnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.ID == "" || accepted.State != "accepted" {
		t.Fatalf("accepted snapshot = %+v", accepted)
	}
	core.waitForRequest(t)

	get := httptest.NewRequest(http.MethodGet, "/v1/recoveries/current", nil)
	get = get.WithContext(withPeerCredentials(get.Context(), 501, true))
	currentRecorder := httptest.NewRecorder()
	started = time.Now()
	handler.ServeHTTP(currentRecorder, get)
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("GET blocked behind Core work: %s", elapsed)
	}
	if currentRecorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", currentRecorder.Code, currentRecorder.Body.String())
	}
	var running RecoverySnapshot
	if err := json.Unmarshal(currentRecorder.Body.Bytes(), &running); err != nil {
		t.Fatal(err)
	}
	if running.ID != accepted.ID || running.State != "running" {
		t.Fatalf("running snapshot = %+v, accepted = %+v", running, accepted)
	}

	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{
		State: "succeeded", Stage: "succeeded", Detail: "vless://must-not-escape",
	}})
	eventually(t, func() bool { return env.manager.CurrentPathRecovery().State == "succeeded" })
	currentRecorder = httptest.NewRecorder()
	handler.ServeHTTP(currentRecorder, get)
	var succeeded RecoverySnapshot
	if err := json.Unmarshal(currentRecorder.Body.Bytes(), &succeeded); err != nil {
		t.Fatal(err)
	}
	if succeeded.ID != accepted.ID || succeeded.State != "succeeded" || succeeded.Detail != "" {
		t.Fatalf("succeeded snapshot = %+v", succeeded)
	}
	if strings.Contains(currentRecorder.Body.String(), "vless://") {
		t.Fatalf("GET leaked Core detail: %s", currentRecorder.Body.String())
	}
}

func TestRecoveryLocalAPIAuthorizesOnlyRootOrConfiguredOwner(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		uid      uint32
		gotUID   bool
		wantCode int
	}{
		{name: "POST missing credentials", method: http.MethodPost, path: "/v1/recoveries", body: `{"reason":"manual"}`, wantCode: http.StatusForbidden},
		{name: "POST unrelated user", method: http.MethodPost, path: "/v1/recoveries", body: `{"reason":"manual"}`, uid: 502, gotUID: true, wantCode: http.StatusForbidden},
		{name: "POST owner", method: http.MethodPost, path: "/v1/recoveries", body: `{"reason":"manual"}`, uid: 501, gotUID: true, wantCode: http.StatusAccepted},
		{name: "POST root", method: http.MethodPost, path: "/v1/recoveries", body: `{"reason":"manual"}`, uid: 0, gotUID: true, wantCode: http.StatusAccepted},
		{name: "GET missing credentials", method: http.MethodGet, path: "/v1/recoveries/current", wantCode: http.StatusForbidden},
		{name: "GET unrelated user", method: http.MethodGet, path: "/v1/recoveries/current", uid: 502, gotUID: true, wantCode: http.StatusForbidden},
		{name: "GET owner", method: http.MethodGet, path: "/v1/recoveries/current", uid: 501, gotUID: true, wantCode: http.StatusOK},
		{name: "GET root", method: http.MethodGet, path: "/v1/recoveries/current", uid: 0, gotUID: true, wantCode: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &fakeController{
				recoveryResult:  RecoverySnapshot{ID: "recovery-1", State: "accepted", Stage: "queued", Reason: "manual"},
				recoveryCurrent: RecoverySnapshot{ID: "recovery-1", State: "running", Stage: "core_recovery", Reason: "manual"},
			}
			request := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			request = request.WithContext(withPeerCredentials(request.Context(), tt.uid, tt.gotUID))
			recorder := httptest.NewRecorder()
			NewLocalAPI(controller, LocalAPIOptions{OwnerUID: 501}).ServeHTTP(recorder, request)
			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
		})
	}

	controller := &fakeController{}
	request := httptest.NewRequest(http.MethodPost, "/v1/up", nil)
	request = request.WithContext(withPeerCredentials(request.Context(), 501, true))
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller, LocalAPIOptions{OwnerUID: 501}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || controller.upCalls != 0 {
		t.Fatalf("owner-only lifecycle mutation = status %d calls %d, want root-only", recorder.Code, controller.upCalls)
	}
}

func TestRecoveryLocalAPIRejectsUnsafeMetadataAndRedactsCoreFailures(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	handler := NewLocalAPI(env.manager, LocalAPIOptions{OwnerUID: 501})
	for _, body := range []string{
		`{"reason":"manual","client_link":"vless://user:password@example.test"}`,
		`{"reason":"vless://user:password@example.test"}`,
		`{"reason":"manual","generation":"token=secret value"}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/recoveries", strings.NewReader(body))
		request = request.WithContext(withPeerCredentials(request.Context(), 501, true))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("unsafe metadata status = %d, body=%s", recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "vless://") || strings.Contains(recorder.Body.String(), "token=") {
			t.Fatalf("validation response reflected secret: %s", recorder.Body.String())
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/recoveries", strings.NewReader(`{"reason":"manual"}`))
	request = request.WithContext(withPeerCredentials(request.Context(), 501, true))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("POST status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	core.waitForRequest(t)
	secret := "vless://user:password@example.test?token=secret"
	core.release(corePathResult{
		snapshot: supervisor.PathRecoverySnapshot{State: "blocked", Stage: "blocked", ErrorCode: "transport_unavailable", Detail: secret},
		err:      &supervisor.PathRecoveryError{Code: "transport_unavailable", Detail: secret},
	})
	eventually(t, func() bool { return env.manager.CurrentPathRecovery().State == "failed" })

	get := httptest.NewRequest(http.MethodGet, "/v1/recoveries/current", nil)
	get = get.WithContext(withPeerCredentials(get.Context(), 501, true))
	currentRecorder := httptest.NewRecorder()
	handler.ServeHTTP(currentRecorder, get)
	var failed RecoverySnapshot
	if err := json.Unmarshal(currentRecorder.Body.Bytes(), &failed); err != nil {
		t.Fatal(err)
	}
	if failed.ErrorCode != "transport_unavailable" || failed.Detail != "" {
		t.Fatalf("failed snapshot = %+v", failed)
	}
	if strings.Contains(currentRecorder.Body.String(), "password") || strings.Contains(currentRecorder.Body.String(), "token=") {
		t.Fatalf("failure response leaked Core detail: %s", currentRecorder.Body.String())
	}
}

func TestRecoveryLocalAPIClientRequiresExactStatuses(t *testing.T) {
	client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusOK
		if request.Method == http.MethodGet {
			status = http.StatusAccepted
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"recovery_id":"recovery-1","state":"running","stage":"core_recovery","reason":"manual"}`)),
			Request:    request,
		}, nil
	})}}
	if _, err := client.RequestRecovery(context.Background(), RecoveryRequest{Reason: "manual"}); err == nil {
		t.Fatal("RequestRecovery accepted HTTP 200 instead of requiring 202")
	}
	if _, err := client.CurrentRecovery(context.Background()); err == nil {
		t.Fatal("CurrentRecovery accepted HTTP 202 instead of requiring 200")
	}
}

func TestClientUsesGuardianUnixAPI(t *testing.T) {
	controller := &fakeController{
		status:          Status{SchemaVersion: 1, Desired: DesiredOff, Phase: PhaseIdle, Protection: ProtectionOff},
		recoveryResult:  RecoverySnapshot{ID: "recovery-1", State: "accepted", Stage: "queued", Reason: "manual"},
		recoveryCurrent: RecoverySnapshot{ID: "recovery-1", State: "succeeded", Stage: "succeeded", Reason: "manual"},
	}
	socketDir := filepath.Join("/tmp", fmt.Sprintf("bxg-%d", os.Getpid()))
	_ = os.RemoveAll(socketDir)
	if err := os.Mkdir(socketDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "guard.sock")
	uid := uint32(os.Geteuid())
	daemon, err := StartDaemon(context.Background(), DaemonOptions{
		SocketPath: socketPath,
		Handler:    NewLocalAPI(controller),
		OwnerUID:   uid,
		PeerCredentials: func(net.Conn) (uint32, bool) {
			return 0, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer daemon.Close()

	client := NewClient(socketPath)
	if _, err := client.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Migrate(context.Background(), MigrationRequest{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}}); err != nil {
		t.Fatal(err)
	}
	updateRequest := UpdateRequest{
		TransactionID: "tx-1", FromVersion: "v1", ToVersion: "v2",
		AssetSHA256: strings.Repeat("a", 64), PackagePath: "/var/lib/bx/update/staging/tx-1/package.tar.gz",
	}
	if _, err := client.Update(context.Background(), updateRequest); err != nil {
		t.Fatal(err)
	}
	accepted, err := client.RequestRecovery(context.Background(), RecoveryRequest{Reason: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := client.CurrentRecovery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if controller.upCalls != 1 || controller.downCalls != 1 || controller.migrateCalls != 1 || controller.updateCalls != 1 || controller.recoveryCalls != 1 ||
		accepted.ID != "recovery-1" || current.State != "succeeded" || status.SchemaVersion != 1 {
		t.Fatalf("calls/recovery/status = %d/%d/%d/%d/%d %+v/%+v/%+v", controller.upCalls, controller.downCalls, controller.migrateCalls, controller.updateCalls, controller.recoveryCalls, accepted, current, status)
	}
}

func TestDaemonRefusesToReplaceNonSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bx-guard.sock")
	if err := os.WriteFile(path, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := StartDaemon(context.Background(), DaemonOptions{SocketPath: path, Handler: http.NewServeMux(), OwnerUID: uint32(os.Geteuid())})
	if err == nil {
		t.Fatal("daemon replaced a non-socket")
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "do not replace" {
		t.Fatalf("existing file changed: %q, %v", got, readErr)
	}
}

func TestDaemonShutdownDrainsAcceptedDetachedMutation(t *testing.T) {
	controller := &blockingController{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		status:  Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected},
	}
	ctx, cancel := context.WithCancel(context.Background())
	socketPath := filepath.Join(shortSocketDir(t), "guard.sock")
	daemon, err := StartDaemon(ctx, DaemonOptions{
		SocketPath: socketPath,
		Handler:    NewLocalAPI(controller),
		OwnerUID:   uint32(os.Geteuid()),
		PeerCredentials: func(net.Conn) (uint32, bool) {
			return 0, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan error, 1)
	go func() {
		_, err := NewClient(socketPath).Up(context.Background())
		requestDone <- err
	}()
	select {
	case <-controller.entered:
	case <-time.After(time.Second):
		t.Fatal("mutation was not accepted")
	}

	cancel()
	closeDone := make(chan error, 1)
	go func() { closeDone <- daemon.Close() }()
	select {
	case err := <-closeDone:
		close(controller.release)
		<-requestDone
		t.Fatalf("daemon returned before accepted mutation drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	probeCtx, probeCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer probeCancel()
	if _, err := NewClient(socketPath).Status(probeCtx); err == nil {
		close(controller.release)
		<-requestDone
		t.Fatal("daemon accepted a new request after shutdown began")
	}

	close(controller.release)
	if err := <-requestDone; err != nil {
		t.Fatalf("accepted mutation response failed during drain: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("graceful daemon shutdown: %v", err)
	}
}

func TestRemoveStaleSocketOnlyUnlinksOnConnectionRefused(t *testing.T) {
	path := makeOrphanedUnixSocket(t)
	err := removeStaleSocketWithDial(path, uint32(os.Geteuid()), func(context.Context, string) (net.Conn, error) {
		return nil, syscall.ECONNREFUSED
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale socket still exists: %v", err)
	}
}

func TestRemoveStaleSocketRetainsSocketOnAmbiguousDialErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "resource exhaustion", err: syscall.EMFILE},
		{name: "permission", err: syscall.EACCES},
		{name: "unknown", err: errors.New("unclassified dial failure")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := makeOrphanedUnixSocket(t)
			err := removeStaleSocketWithDial(path, uint32(os.Geteuid()), func(context.Context, string) (net.Conn, error) {
				return nil, tt.err
			})
			if err == nil {
				t.Fatalf("ambiguous dial error %v treated as stale", tt.err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("socket removed after ambiguous dial error: %v", err)
			}
		})
	}
}

func makeOrphanedUnixSocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(shortSocketDir(t), "stale.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		listener.Close()
		t.Fatal("unix listener type unavailable")
	}
	unixListener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bxg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type fakeController struct {
	status       Status
	upCalls      int
	downCalls    int
	migrateCalls int
	updateCalls  int
	migration    MigrationRequest
	update       UpdateRequest
	updateResult UpdateResult
	upErr        error
	downErr      error
	migrateErr   error
	updateErr    error
	upContextErr error

	recoveryCalls   int
	recoveryRequest RecoveryRequest
	recoveryResult  RecoverySnapshot
	recoveryCurrent RecoverySnapshot
	recoveryErr     error
}

type blockingController struct {
	status  Status
	entered chan struct{}
	release chan struct{}
}

func (c *blockingController) Status() Status { return c.status }

func (c *blockingController) Up(ctx context.Context) error {
	close(c.entered)
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*blockingController) Down(context.Context) error { return nil }

func (c *fakeController) Status() Status { return c.status }

func (c *fakeController) Up(ctx context.Context) error {
	c.upContextErr = ctx.Err()
	c.upCalls++
	return c.upErr
}

func (c *fakeController) Down(context.Context) error {
	c.downCalls++
	return c.downErr
}

func (c *fakeController) Migrate(_ context.Context, request MigrationRequest) error {
	c.migrateCalls++
	c.migration = request
	return c.migrateErr
}

func (c *fakeController) Update(_ context.Context, request UpdateRequest) (UpdateResult, error) {
	c.updateCalls++
	c.update = request
	return c.updateResult, c.updateErr
}

func (c *fakeController) RequestPathRecovery(request RecoveryRequest) (RecoverySnapshot, error) {
	c.recoveryCalls++
	c.recoveryRequest = request
	return c.recoveryResult, c.recoveryErr
}

func (c *fakeController) CurrentPathRecovery() RecoverySnapshot {
	return c.recoveryCurrent
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
