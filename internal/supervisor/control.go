// control.go 是 bx 守护进程的本地控制面:HTTP/1.1 over unix socket(Tailscale LocalAPI 范式)。
// GET /v0/status 返回 Report;POST /v0/commit|rollback 驱动 commit-confirmed 引擎(peer-cred 仅 root)。
// 取代旧的"连上就推 Report"私有协议。真实 mutation 路由(/v0/transport、/v0/rehijack)留 9b-2b/9b-3。
package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/getbx/bx/internal/confirm"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/tunnel"
)

// controlEngine 是 commit-confirmed 引擎的接口,由 *mutationEngine (Task 9b-1) 满足。
type controlEngine interface {
	Commit() error
	Rollback() error
	State() confirm.State
}

// tunnelStatser 解耦 serveControl 与具体 *tunnel.Tunnel,由 *tunnel.Tunnel 自动满足。
type tunnelStatser interface {
	Stats() tunnel.Stats
	SocksAddr() string
}

type controlStarter func() (io.Closer, error)

type controlResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	State  string `json:"state,omitempty"`
}

// ctxConnKey 用于在 http.Server.ConnContext 中把 net.Conn 塞入 request context,
// 供 requireRoot 做 peer-cred 鉴权。
type ctxConnKey struct{}

type controlServer struct {
	mu     sync.Mutex // 串行化命令(满足并发契约)
	eng    controlEngine
	report func() stats.Report
}

func stateName(s confirm.State) string {
	switch s {
	case confirm.StateArmed:
		return "armed"
	case confirm.StateCommitted:
		return "committed"
	case confirm.StateReverted:
		return "reverted"
	default:
		return "idle"
	}
}

// newControlMux 构建控制面 HTTP mux。
func newControlMux(eng controlEngine, report func() stats.Report) http.Handler {
	cs := &controlServer{eng: eng, report: report}
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/status", cs.handleStatus)
	mux.HandleFunc("/v0/commit", cs.handleCommit)
	mux.HandleFunc("/v0/rollback", cs.handleRollback)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (cs *controlServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, controlResponse{Status: "error", Error: "method not allowed"})
		return
	}
	rep := cs.report()
	rep.MutationState = stateName(cs.eng.State())
	writeJSON(w, http.StatusOK, rep)
}

// requireRoot 对 mutation 路由做 peer-cred 鉴权(unix 连接时);非 unix(如 httptest TCP)放行。
func requireRoot(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, controlResponse{Status: "error", Error: "method not allowed"})
		return false
	}
	conn, _ := r.Context().Value(ctxConnKey{}).(net.Conn)
	if conn == nil {
		// 无 unix conn(如 httptest TCP):放行,peer-cred 鉴权由 authorizeMutation 单测覆盖。
		return true
	}
	uid, gotUID := peerCredUID(conn)
	if !authorizeMutation(uid, gotUID) {
		msg := "改动类命令需 root"
		if !peerCredSupported {
			msg = "此平台暂不支持 peer-cred,改动类已拒绝;macOS daemon 待实现 LOCAL_PEERCRED"
		}
		writeJSON(w, http.StatusForbidden, controlResponse{Status: "error", Error: msg})
		return false
	}
	return true
}

func (cs *controlServer) handleCommit(w http.ResponseWriter, r *http.Request) {
	if !requireRoot(w, r) {
		return
	}
	cs.mu.Lock()
	err := cs.eng.Commit()
	state := stateName(cs.eng.State())
	cs.mu.Unlock()
	if err != nil {
		if errors.Is(err, confirm.ErrNotArmed) {
			writeJSON(w, http.StatusConflict, controlResponse{Status: "error", Error: "nothing to commit", State: state})
			return
		}
		writeJSON(w, http.StatusInternalServerError, controlResponse{Status: "error", Error: err.Error(), State: state})
		return
	}
	writeJSON(w, http.StatusOK, controlResponse{Status: "committed", State: state})
}

func (cs *controlServer) handleRollback(w http.ResponseWriter, r *http.Request) {
	if !requireRoot(w, r) {
		return
	}
	cs.mu.Lock()
	err := cs.eng.Rollback()
	state := stateName(cs.eng.State())
	cs.mu.Unlock()
	if err != nil {
		if errors.Is(err, confirm.ErrNotArmed) {
			writeJSON(w, http.StatusConflict, controlResponse{Status: "error", Error: "nothing to rollback", State: state})
			return
		}
		writeJSON(w, http.StatusInternalServerError, controlResponse{Status: "error", Error: err.Error(), State: state})
		return
	}
	writeJSON(w, http.StatusOK, controlResponse{Status: "reverted", State: state})
}

func requireControlSocket(start controlStarter) (io.Closer, error) {
	closer, err := start()
	if err != nil {
		return nil, fmt.Errorf("控制 socket 启动失败: %w", err)
	}
	return closer, nil
}

// serveControl 在 SockPath 上跑控制面 HTTP server,替换旧的 serveStats。
// c: 统计计数器;t: 隧道(满足 tunnelStatser);server/udpMode: 配置字符串;eng: 引擎。
func serveControl(c *stats.Counters, t tunnelStatser, server, udpMode string, eng controlEngine) (io.Closer, error) {
	report := func() stats.Report {
		ts := t.Stats()
		return stats.Report{
			Snapshot:      c.Snapshot(),
			Server:        server,
			SocksAddr:     t.SocksAddr(),
			TunnelHealthy: ts.Up,
			LatencyMS:     ts.LatencyMS,
			Restarts:      ts.Restarts,
			UDPMode:       udpMode,
			UDPNote:       udpNote(udpMode),
		}
	}
	_ = os.MkdirAll(filepath.Dir(SockPath), 0o755)
	_ = os.Remove(SockPath)
	ln, err := net.Listen("unix", SockPath)
	if err != nil {
		return nil, err
	}
	// 0o666 让非 root 的 bx status/bx mcp 均可读;mutation 门控靠 peer-cred(POST 路由),不靠 socket 权限。
	_ = os.Chmod(SockPath, 0o666)
	srv := &http.Server{
		Handler:           newControlMux(eng, report),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			return context.WithValue(ctx, ctxConnKey{}, conn)
		},
	}
	go srv.Serve(ln) //nolint:errcheck
	return ln, nil
}
