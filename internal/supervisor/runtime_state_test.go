package supervisor

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/getbx/bx/internal/confirm"
	"github.com/getbx/bx/internal/stats"
)

func TestRuntimeStateContainsOnlyHandoffMetadata(t *testing.T) {
	state := RuntimeState{
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
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"vless://", "hysteria2://", "password", "token", "uuid"} {
		if bytes.Contains(bytes.ToLower(b), []byte(forbidden)) {
			t.Fatalf("runtime state leaked %q", forbidden)
		}
	}

	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"dns_listening", "pid", "routes_installed", "server_bypass", "socks_addr",
		"tun_name", "tunnel_healthy", "udp_ready", "udp_required", "version",
	}
	got := make([]string, 0, len(fields))
	for field := range fields {
		got = append(got, field)
	}
	slicesSort(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime JSON fields = %v, want %v", got, want)
	}
}

func TestRuntimeIPv4BypassUsesExactResolvedAddresses(t *testing.T) {
	addrs := []netip.Addr{
		netip.MustParseAddr("23.27.134.77"),
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("23.27.134.77"),
	}
	want := []string{"23.27.134.77/32"}
	if got := runtimeIPv4Bypass(addrs); !reflect.DeepEqual(got, want) {
		t.Fatalf("runtimeIPv4Bypass() = %v, want %v", got, want)
	}
}

func TestUDPRuntimeReadiness(t *testing.T) {
	tests := []struct {
		name             string
		mode             string
		primaryHealthy   bool
		companionHealthy *bool
		wantRequired     bool
		wantReady        bool
	}{
		{name: "proxy uses primary", mode: "proxy", primaryHealthy: true, wantRequired: true, wantReady: true},
		{name: "proxy primary unhealthy", mode: "proxy", wantRequired: true, wantReady: false},
		{name: "proxy companion ready", mode: "proxy", companionHealthy: boolPtr(true), wantRequired: true, wantReady: true},
		{name: "proxy companion not ready", mode: "proxy", primaryHealthy: true, companionHealthy: boolPtr(false), wantRequired: true, wantReady: false},
		{name: "blocked UDP needs no transport", mode: "block", wantRequired: false, wantReady: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var companion func() bool
			if tt.companionHealthy != nil {
				companion = func() bool { return *tt.companionHealthy }
			}
			required, ready := udpRuntimeReadiness(tt.mode, func() bool { return tt.primaryHealthy }, companion)
			if required != tt.wantRequired || ready != tt.wantReady {
				t.Fatalf("udpRuntimeReadiness() = (%v, %v), want (%v, %v)", required, ready, tt.wantRequired, tt.wantReady)
			}
		})
	}
}

func boolPtr(value bool) *bool { return &value }

func TestRuntimeEndpointIsReadOnlyAndStatusIsPreserved(t *testing.T) {
	runtime := RuntimeState{Version: "v0.3.0", PID: 42, TunName: "utun7"}
	report := stats.Report{Server: "status-node", TunnelHealthy: true}
	h := newControlMuxWithRuntime(
		&fakeControlEngine{state: confirm.StateArmed},
		func() stats.Report { return report },
		func() RuntimeState { return runtime },
		nopMutator{}, nil, 0,
	)

	runtimeReq := httptest.NewRequest(http.MethodGet, "/v0/runtime", nil)
	runtimeRes := httptest.NewRecorder()
	h.ServeHTTP(runtimeRes, runtimeReq)
	if runtimeRes.Code != http.StatusOK {
		t.Fatalf("runtime status = %d, body=%s", runtimeRes.Code, runtimeRes.Body.String())
	}
	var gotRuntime RuntimeState
	if err := json.NewDecoder(runtimeRes.Body).Decode(&gotRuntime); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotRuntime, runtime) {
		t.Fatalf("runtime = %+v, want %+v", gotRuntime, runtime)
	}

	postReq := httptest.NewRequest(http.MethodPost, "/v0/runtime", nil)
	postRes := httptest.NewRecorder()
	h.ServeHTTP(postRes, postReq)
	if postRes.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST runtime status = %d, want %d", postRes.Code, http.StatusMethodNotAllowed)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/v0/status", nil)
	statusRes := httptest.NewRecorder()
	h.ServeHTTP(statusRes, statusReq)
	var gotReport stats.Report
	if err := json.NewDecoder(statusRes.Body).Decode(&gotReport); err != nil {
		t.Fatal(err)
	}
	if gotReport.Server != report.Server || !gotReport.TunnelHealthy || gotReport.MutationState != "armed" {
		t.Fatalf("status behavior changed: %+v", gotReport)
	}
}

func TestFetchRuntimeState(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "bx.sock")
	want := RuntimeState{Version: "v0.3.0", PID: 42, TunName: "utun7", SocksAddr: "127.0.0.1:43210"}

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/runtime", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(want)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	got, err := FetchRuntimeState(sockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FetchRuntimeState() = %+v, want %+v", got, want)
	}
}

func slicesSort(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
