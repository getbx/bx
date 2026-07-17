package guardian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
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

func TestClientUsesGuardianUnixAPI(t *testing.T) {
	controller := &fakeController{status: Status{SchemaVersion: 1, Desired: DesiredOff, Phase: PhaseIdle, Protection: ProtectionOff}}
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
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if controller.upCalls != 1 || controller.downCalls != 1 || controller.migrateCalls != 1 || status.SchemaVersion != 1 {
		t.Fatalf("calls/status = %d/%d/%d %+v", controller.upCalls, controller.downCalls, controller.migrateCalls, status)
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
	migration    MigrationRequest
	upErr        error
	downErr      error
	migrateErr   error
	upContextErr error
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
