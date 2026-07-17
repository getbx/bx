package guardian

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/supervisor"
)

func TestHealthCheckerRequiresCompleteRuntimeState(t *testing.T) {
	valid := supervisor.RuntimeState{
		Version:         "v0.3.0",
		PID:             42,
		TunName:         "utun7",
		SocksAddr:       "127.0.0.1:43210",
		ServerBypass:    []string{"23.27.134.77/32"},
		TunnelHealthy:   true,
		DNSListening:    true,
		RoutesInstalled: true,
		UDPRequired:     true,
		UDPReady:        true,
	}

	tests := []struct {
		name      string
		mutate    func(*supervisor.RuntimeState)
		probeErr  error
		wantOK    bool
		wantError string
	}{
		{name: "complete", wantOK: true},
		{name: "wrong version", mutate: func(s *supervisor.RuntimeState) { s.Version = "v0.2.9" }, wantError: "version"},
		{name: "PID mismatch", mutate: func(s *supervisor.RuntimeState) { s.PID = 41 }, wantError: "PID"},
		{name: "unhealthy tunnel", mutate: func(s *supervisor.RuntimeState) { s.TunnelHealthy = false }, wantError: "tunnel"},
		{name: "DNS not listening", mutate: func(s *supervisor.RuntimeState) { s.DNSListening = false }, wantError: "DNS"},
		{name: "routes missing", mutate: func(s *supervisor.RuntimeState) { s.RoutesInstalled = false }, wantError: "routes"},
		{name: "required UDP not ready", mutate: func(s *supervisor.RuntimeState) { s.UDPReady = false }, wantError: "UDP"},
		{name: "proxy probe failed", probeErr: errors.New("SOCKS connect refused"), wantError: "SOCKS connect refused"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := valid
			if tt.mutate != nil {
				tt.mutate(&state)
			}
			checker := HealthChecker{
				SockPath:     "/tmp/test-bx.sock",
				PollInterval: time.Millisecond,
				fetchRuntime: func(context.Context, string) (supervisor.RuntimeState, error) { return state, nil },
				probe: func(context.Context, string, string) error {
					return tt.probeErr
				},
			}
			got, err := checker.Wait(context.Background(), HealthTarget{
				Version: "v0.3.0",
				PID:     42,
				Timeout: 8 * time.Millisecond,
			})
			if tt.wantOK {
				if err != nil {
					t.Fatalf("Wait() error = %v", err)
				}
				if !reflect.DeepEqual(got, state) {
					t.Fatalf("Wait() state = %+v, want %+v", got, state)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Wait() error = %v, want condition %q", err, tt.wantError)
			}
		})
	}
}

func TestHealthCheckerAllowsUDPWhenNotRequired(t *testing.T) {
	state := supervisor.RuntimeState{
		Version: "v0.3.0", PID: 42, TunName: "utun7", SocksAddr: "127.0.0.1:43210",
		ServerBypass: []string{"23.27.134.77/32"}, TunnelHealthy: true,
		DNSListening: true, RoutesInstalled: true, UDPRequired: false, UDPReady: false,
	}
	checker := HealthChecker{
		fetchRuntime: func(context.Context, string) (supervisor.RuntimeState, error) { return state, nil },
		probe:        func(context.Context, string, string) error { return nil },
	}
	if _, err := checker.Wait(context.Background(), HealthTarget{Version: "v0.3.0", PID: 42, Timeout: time.Second}); err != nil {
		t.Fatalf("optional UDP prevented health: %v", err)
	}
}

func TestHealthCheckerDefaultProbeUsesLoopbackSOCKS(t *testing.T) {
	socksAddr, requested := startHealthSOCKSServer(t)
	state := supervisor.RuntimeState{
		Version: "v0.3.0", PID: 42, TunName: "utun7", SocksAddr: socksAddr,
		ServerBypass: []string{"23.27.134.77/32"}, TunnelHealthy: true,
		DNSListening: true, RoutesInstalled: true,
	}
	checker := HealthChecker{
		ProbeAddr:    "198.51.100.10:443",
		fetchRuntime: func(context.Context, string) (supervisor.RuntimeState, error) { return state, nil },
	}
	if _, err := checker.Wait(context.Background(), HealthTarget{Version: "v0.3.0", PID: 42, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-requested:
		if got != "198.51.100.10:443" {
			t.Fatalf("SOCKS target = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS CONNECT was not observed")
	}
}

func TestHealthCheckerRejectsNonLoopbackSOCKS(t *testing.T) {
	state := supervisor.RuntimeState{
		Version: "v0.3.0", PID: 42, TunName: "utun7", SocksAddr: "192.0.2.10:1080",
		ServerBypass: []string{"23.27.134.77/32"}, TunnelHealthy: true,
		DNSListening: true, RoutesInstalled: true,
	}
	checker := HealthChecker{
		PollInterval: time.Millisecond,
		fetchRuntime: func(context.Context, string) (supervisor.RuntimeState, error) { return state, nil },
	}
	_, err := checker.Wait(context.Background(), HealthTarget{Version: "v0.3.0", PID: 42, Timeout: 8 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("Wait() error = %v, want loopback rejection", err)
	}
}

func TestHealthCheckerTimeoutCancelsRuntimeFetch(t *testing.T) {
	checker := HealthChecker{
		fetchRuntime: func(ctx context.Context, _ string) (supervisor.RuntimeState, error) {
			<-ctx.Done()
			return supervisor.RuntimeState{}, ctx.Err()
		},
	}
	start := time.Now()
	_, err := checker.Wait(context.Background(), HealthTarget{Version: "v0.3.0", PID: 42, Timeout: 10 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("Wait() error = %v, want deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("Wait() ignored target timeout: %s", elapsed)
	}
}

func startHealthSOCKSServer(t *testing.T) (string, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	requested := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var greeting [3]byte
		if _, err := io.ReadFull(conn, greeting[:]); err != nil {
			return
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			return
		}
		var head [4]byte
		if _, err := io.ReadFull(conn, head[:]); err != nil {
			return
		}
		if head[0] != 5 || head[1] != 1 || head[3] != 1 {
			requested <- fmt.Sprintf("bad request: %v", head)
			return
		}
		var addr [6]byte
		if _, err := io.ReadFull(conn, addr[:]); err != nil {
			return
		}
		requested <- net.JoinHostPort(net.IP(addr[:4]).String(), fmt.Sprint(int(addr[4])<<8|int(addr[5])))
		_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 1})
	}()
	return ln.Addr().String(), requested
}
