package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if controller.upCalls != 1 || controller.downCalls != 1 || status.SchemaVersion != 1 {
		t.Fatalf("calls/status = %d/%d %+v", controller.upCalls, controller.downCalls, status)
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

type fakeController struct {
	status       Status
	upCalls      int
	downCalls    int
	upErr        error
	downErr      error
	upContextErr error
}

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
