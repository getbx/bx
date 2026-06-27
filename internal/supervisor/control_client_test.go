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

func TestSetTransportControl(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "bx.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/transport", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Link string `json:"link"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Link == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(controlResponse{Status: "error", Error: "缺 link"})
			return
		}
		_ = json.NewEncoder(w).Encode(controlResponse{Status: "armed", State: "armed"})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	state, err := SetTransportControl(sock, "vless://x@h:443")
	if err != nil || state != "armed" {
		t.Fatalf("SetTransportControl state=%q err=%v", state, err)
	}
	if _, err := SetTransportControl(sock, ""); err == nil {
		t.Fatal("空 link 服务端 400,客户端应返回错误")
	}
}

func TestRehijackControl(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "bx.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var gotMethod, gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/rehijack", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(controlResponse{Status: "hijacked", State: "hijacked"})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	state, err := RehijackControl(sockPath)
	if err != nil {
		t.Fatalf("RehijackControl: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v0/rehijack" {
		t.Fatalf("got %s %s, want POST /v0/rehijack", gotMethod, gotPath)
	}
	if state != "hijacked" {
		t.Fatalf("state=%q want hijacked", state)
	}
}

func TestSetTransportControlBadJSON(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "bx.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/transport", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	_, err = SetTransportControl(sock, "vless://x@h:443")
	if err == nil {
		t.Fatal("200 OK + 非 JSON 回包,应返回 decode 错误,而非沉默成功")
	}
}
