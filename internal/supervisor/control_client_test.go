package supervisor

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/getbx/bx/internal/stats"
)

// TestFetchStatusReport 端到端验证 FetchStatusReport 正确经 unix socket 拉取 Report。
func TestFetchStatusReport(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "bx.sock")

	want := stats.Report{Server: "round-trip-node", TunnelHealthy: true, LatencyMS: 42}

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/v0/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(want)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	got, err := FetchStatusReport(sockPath)
	if err != nil {
		t.Fatalf("FetchStatusReport: %v", err)
	}
	if got.Server != want.Server || got.TunnelHealthy != want.TunnelHealthy || got.LatencyMS != want.LatencyMS {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestFetchStatusReportNonOK 验证非 200 响应返回 error。
func TestFetchStatusReportNonOK(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "bx.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/v0/status", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not running", http.StatusServiceUnavailable)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	_, err = FetchStatusReport(sockPath)
	if err == nil {
		t.Fatal("期望 non-200 返回 error")
	}
}

func TestCommitControlPostsCommit(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "bx.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var gotMethod, gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/commit", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(controlResponse{Status: "committed", State: "committed"})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	state, err := CommitControl(sockPath)
	if err != nil {
		t.Fatalf("CommitControl: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v0/commit" {
		t.Fatalf("got %s %s, want POST /v0/commit", gotMethod, gotPath)
	}
	if state != "committed" {
		t.Fatalf("state=%q want committed", state)
	}
}

func TestRollbackControlPostsRollback(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "bx.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var gotMethod, gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/rollback", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(controlResponse{Status: "reverted", State: "reverted"})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	state, err := RollbackControl(sockPath)
	if err != nil {
		t.Fatalf("RollbackControl: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v0/rollback" {
		t.Fatalf("got %s %s, want POST /v0/rollback", gotMethod, gotPath)
	}
	if state != "reverted" {
		t.Fatalf("state=%q want reverted", state)
	}
}
