package guardian

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/install"
)

func TestDaemonRunsJournalRecoveryBeforeServingLocalAPI(t *testing.T) {
	for _, recoveryErr := range []error{nil, errors.New("needs attention")} {
		ctx, cancel := context.WithCancel(context.Background())
		events := []string{}
		controller := &daemonStartupController{
			recover: func(context.Context) error {
				events = append(events, "recover")
				return recoveryErr
			},
		}
		start := func(_ context.Context, options DaemonOptions) (*Daemon, error) {
			events = append(events, "serve")
			if options.Handler == nil {
				t.Fatal("LocalAPI handler was not installed")
			}
			if options.OwnerUID != 0 {
				t.Fatalf("OwnerUID = %d, want root", options.OwnerUID)
			}
			return &Daemon{}, nil
		}

		if _, err := startRecoveredDaemon(ctx, DaemonOptions{}, controller, start); err != nil {
			t.Fatal(err)
		}
		cancel()
		if want := []string{"recover", "serve"}; !reflect.DeepEqual(events, want) {
			t.Fatalf("events = %#v, want %#v", events, want)
		}
	}
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

type daemonStartupController struct {
	recover func(context.Context) error
}

func (*daemonStartupController) Status() Status                      { return Status{} }
func (*daemonStartupController) Up(context.Context) error            { return nil }
func (*daemonStartupController) Down(context.Context) error          { return nil }
func (c *daemonStartupController) Recover(ctx context.Context) error { return c.recover(ctx) }
func (*daemonStartupController) RequestPathRecovery(request RecoveryRequest) (RecoverySnapshot, error) {
	return RecoverySnapshot{ID: "recovery-1", State: "accepted", Stage: "queued", Reason: request.Reason, Generation: request.Generation}, nil
}
func (*daemonStartupController) CurrentPathRecovery() RecoverySnapshot {
	return RecoverySnapshot{State: "idle", Stage: "idle"}
}

var _ http.Handler = NewLocalAPI(&daemonStartupController{recover: func(context.Context) error { return nil }})

func TestSystemNetworkRestorerPropagatesCancellationToDNSRestore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	restorer := systemNetworkRestorer{disableDNS: func(got context.Context, service string) (install.DNSStatus, error) {
		called = true
		if service != "" {
			t.Fatalf("service = %q, want auto-detect", service)
		}
		return install.DNSStatus{}, got.Err()
	}}

	err := restorer.Restore(ctx)
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
