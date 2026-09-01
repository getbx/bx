package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getbx/bx/internal/appattr"
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

// TestFetchAppTrafficAlwaysSubscribes 验证 FetchAppTraffic 总是带 subscribe=1
// (设计前提是「消费方每次拉取都会带上它」,不带会让采集在两次拉取的间隙过期),
// 并端到端验证三态字段(subscribed/report/error)原样透传、不做任何合并。
func TestFetchAppTrafficAlwaysSubscribes(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	sock := startControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		writeJSON(w, http.StatusOK, AppTrafficResponse{
			Subscribed: true,
			Report:     appattr.Report{Groups: []appattr.Group{{Path: appattr.PathTunnel}, {Path: appattr.PathDirect}, {Path: appattr.PathBlocked}}},
		})
	})

	got, err := FetchAppTraffic(sock)
	if err != nil {
		t.Fatalf("FetchAppTraffic: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/v0/apps" {
		t.Fatalf("got %s %s, want GET /v0/apps", gotMethod, gotPath)
	}
	if gotQuery != "subscribe=1" {
		t.Fatalf("query=%q, want subscribe=1(消费方每次拉取都要带,兼续期)", gotQuery)
	}
	if !got.Subscribed {
		t.Fatal("服务端报了 subscribed=true,客户端却读到 false")
	}
	if len(got.Report.Groups) != 3 {
		t.Fatalf("组数应恒为 3,got %d", len(got.Report.Groups))
	}
	if got.Error != "" {
		t.Fatalf("服务端没报错,客户端却读到 error=%q", got.Error)
	}
}

// TestFetchAppTrafficPropagatesErrorField 验证「订阅了但问不出来」这一态
// (HTTP 200 + subscribed=true + error 非空)原样透传给调用方,不当成失败。
func TestFetchAppTrafficPropagatesErrorField(t *testing.T) {
	sock := startControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, AppTrafficResponse{Subscribed: true, Error: "问不出来"})
	})

	got, err := FetchAppTraffic(sock)
	if err != nil {
		t.Fatalf("appSource 报错不该让客户端返回 transport error: %v", err)
	}
	if !got.Subscribed {
		t.Fatal("subscribed 应为 true")
	}
	if got.Error != "问不出来" {
		t.Fatalf("error=%q, want 原样透传", got.Error)
	}
	if len(got.Report.Groups) != 0 {
		t.Fatalf("appSource 报错时不该有三组齐全的空报告,got %d groups", len(got.Report.Groups))
	}
}

func TestFetchAppTrafficNonOK(t *testing.T) {
	sock := startControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not implemented", http.StatusNotImplemented)
	})
	_, err := FetchAppTraffic(sock)
	if err == nil {
		t.Fatal("期望非 200 返回 error")
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

func TestShutdownControlPostsExpectedPID(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "bxs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "bx.sock")
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	var gotMethod, gotPath string
	var gotExpectedPID int
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/shutdown", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		var request struct {
			ExpectedPID int `json:"expected_pid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		gotExpectedPID = request.ExpectedPID
		_ = json.NewEncoder(w).Encode(controlResponse{Status: "ok", State: "shutting_down"})
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener) //nolint:errcheck
	defer server.Close()

	if err := ShutdownControl(context.Background(), sockPath, 42); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v0/shutdown" || gotExpectedPID != 42 {
		t.Fatalf("shutdown request = %s %s expected_pid=%d", gotMethod, gotPath, gotExpectedPID)
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

// TestSetServerControl 端到端(真实 unix socket)验证 SetServerControl 的请求体字段名
// 与服务端 setServerReq 的 json tag 对得上——字段名错位会静默变成「link 为空」,
// 报出的错误会把用户指向错误的问题(见 handleSetServer 缺 link 分支)。
func TestSetServerControl(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "bx.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/server", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Link string `json:"link"`
			UDP  string `json:"udp"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Link == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(controlResponse{Status: "error", Error: "缺 link"})
			return
		}
		if req.UDP != "hysteria2://tokyo" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(controlResponse{Status: "error", Error: "udp 字段没传到"})
			return
		}
		_ = json.NewEncoder(w).Encode(controlResponse{Status: "armed", State: "armed"})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	state, err := SetServerControl(sock, "vless://x@h:443", "hysteria2://tokyo")
	if err != nil || state != "armed" {
		t.Fatalf("SetServerControl state=%q err=%v", state, err)
	}
	if _, err := SetServerControl(sock, "", "hysteria2://tokyo"); err == nil {
		t.Fatal("空 link 服务端 400,客户端应返回错误")
	}
}

func TestReconnectControl(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "bx.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var gotMethod, gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/reconnect", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewEncoder(w).Encode(controlResponse{Status: "ok", State: "reconnected"})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	state, err := ReconnectControl(sock)
	if err != nil || state != "reconnected" {
		t.Fatalf("state=%q err=%v", state, err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v0/reconnect" {
		t.Fatalf("got %s %s", gotMethod, gotPath)
	}
}

func TestReconnectControlUsesCallerDeadline(t *testing.T) {
	sock := startControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(75 * time.Millisecond)
		writeJSON(w, http.StatusOK, controlResponse{Status: "ok", State: "reconnected"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	state, err := ReconnectControlContext(ctx, sock)
	if err != nil || state != "reconnected" {
		t.Fatalf("ReconnectControlContext = %q, %v", state, err)
	}
}

func TestReconnectControlOperationTimeoutDiffersFromGeneric(t *testing.T) {
	sock := startControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(75 * time.Millisecond)
		writeJSON(w, http.StatusOK, controlResponse{Status: "ok", State: "reconnected"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	started := time.Now()
	state, err := reconnectControlContext(ctx, sock, func(sockPath string) *http.Client {
		return controlHTTPClientWithTimeout(sockPath, 10*time.Millisecond)
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("generic reconnect error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("generic timeout path took %s, want short timeout", elapsed)
	}

	state, err = reconnectControlContext(ctx, sock, controlHTTPClientForOperation)
	if err != nil || state != "reconnected" {
		t.Fatalf("operation reconnect = %q, %v", state, err)
	}
}

func TestPathRecoveryControlClient(t *testing.T) {
	sock := startControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/path-recovery":
			if r.Method == http.MethodPost {
				var in PathRecoveryRequest
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Errorf("decode request: %v", err)
				}
				if in.Reason != "manual" || in.Generation != "wifi-b" {
					t.Errorf("request = %+v", in)
				}
			}
			writeJSON(w, http.StatusOK, PathRecoverySnapshot{ID: "recovery-1", State: "succeeded", Stage: "succeeded", Reason: "manual", Generation: "wifi-b"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			writeJSON(w, http.StatusNotFound, controlResponse{Status: "error"})
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started, err := RecoverPathControl(ctx, sock, PathRecoveryRequest{Reason: "manual", Generation: "wifi-b"})
	if err != nil || started.ID != "recovery-1" {
		t.Fatalf("RecoverPathControl = %+v, %v", started, err)
	}
	current, err := FetchPathRecovery(ctx, sock)
	if err != nil || current.ID != "recovery-1" {
		t.Fatalf("FetchPathRecovery = %+v, %v", current, err)
	}
}

func TestPathRecoveryControlMapsUnknownErrorCodeToStableFailure(t *testing.T) {
	sock := startControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusInternalServerError, PathRecoverySnapshot{
			State:     "blocked",
			ErrorCode: "vless://secret-uuid@proxy.example",
			Detail:    "secret transport diagnostic",
		})
	})

	_, err := RecoverPathControl(context.Background(), sock, PathRecoveryRequest{Reason: "manual"})
	var recoveryErr *PathRecoveryError
	if !errors.As(err, &recoveryErr) {
		t.Fatalf("RecoverPathControl error = %v, want PathRecoveryError", err)
	}
	if recoveryErr.Code != "recovery_failed" || recoveryErr.Detail != "" {
		t.Fatalf("recovery error = %+v", recoveryErr)
	}
}

func TestPathRecoveryControlPreservesAllowlistedSafetyCodesWithoutDetail(t *testing.T) {
	const secret = "server route and transport diagnostics"
	for _, code := range []string{"capture_missing", "network_unavailable", "underlay_rebind_failed"} {
		t.Run(code, func(t *testing.T) {
			sock := startControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusInternalServerError, PathRecoverySnapshot{
					State:     "blocked",
					ErrorCode: code,
					Detail:    secret,
				})
			})

			snapshot, err := RecoverPathControl(context.Background(), sock, PathRecoveryRequest{Reason: "manual"})
			var recoveryErr *PathRecoveryError
			if !errors.As(err, &recoveryErr) {
				t.Fatalf("RecoverPathControl error = %v, want PathRecoveryError", err)
			}
			if recoveryErr.Code != code || recoveryErr.Detail != "" {
				t.Fatalf("recovery error = %+v, want redacted %q", recoveryErr, code)
			}
			if snapshot.ErrorCode != code || snapshot.Detail != "" {
				t.Fatalf("snapshot = %+v, want redacted %q", snapshot, code)
			}
		})
	}
}

func startControlSocket(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bxs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "bx.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &http.Server{Handler: http.HandlerFunc(handler)}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { _ = srv.Close() })
	return sock
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

// **非 200 时也必须把 State 带回去。**
//
// 控制面对「没有可确认的改动」返回 409,而 State 里如实写着是 `reverted`
// (死手到点自动还原了)还是 `idle`(从没武装过)—— 这两件事对调用方的含义
// 完全相反:前者是「你的改动没了,而且是在你不在场时没的」,后者是「你调错了」。
// postControl 此前在错误路径上 `return "", err`,**这个区分就死在这里**,
// 下游只好把它压成一句「控制面返回 409」。
func TestPostControlKeepsTheStateWhenTheCallIsRejected(t *testing.T) {
	// 不用 t.TempDir():它把测试名嵌进路径,而 unix socket 路径在 macOS 上有
	// ~104 字节上限,长测试名会让 bind 报 invalid argument。
	dir, err := os.MkdirTemp("", "bxctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "bx.sock")
	ln, lnErr := net.Listen("unix", sockPath)
	if lnErr != nil {
		t.Fatal(lnErr)
	}
	defer ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/v0/commit", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(controlResponse{Status: "error", Error: "nothing to commit", State: "reverted"})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	state, err := CommitControl(sockPath)
	if err == nil {
		t.Fatal("409 应当返回 error")
	}
	if state != "reverted" {
		t.Errorf("被拒时 State 丢了:%q,应当是 reverted", state)
	}
}

// 连不上控制面时 State 必须是空串 —— 「没问出来」不许被读成任何一种状态。
// 下游正是靠这个区分来决定该不该报「隧道/进程有问题」。
func TestPostControlReportsNoStateWhenItCannotReachTheSocket(t *testing.T) {
	state, err := CommitControl(filepath.Join(t.TempDir(), "absent.sock"))
	if err == nil {
		t.Fatal("连不上应当返回 error")
	}
	if state != "" {
		t.Errorf("连不上却报了状态 %q —— 那是编出来的", state)
	}
}
