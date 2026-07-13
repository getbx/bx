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
	Arm(apply func() error, undo func() error) error
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
// 供 requireOwnerOrRoot 做 peer-cred 鉴权。
type ctxConnKey struct{}

type controlServer struct {
	mu       sync.Mutex // 串行化命令(满足并发契约)
	eng      controlEngine
	report   func() stats.Report
	mut      mutator
	reload   func() error // 重读配置 rules 并热重建 router(不断隧道);可空
	ownerUID uint32
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
func newControlMux(eng controlEngine, report func() stats.Report, mut mutator, reload func() error, ownerUID uint32) http.Handler {
	cs := &controlServer{eng: eng, report: report, mut: mut, reload: reload, ownerUID: ownerUID}
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/status", cs.handleStatus)
	mux.HandleFunc("/v0/commit", cs.handleCommit)
	mux.HandleFunc("/v0/rollback", cs.handleRollback)
	mux.HandleFunc("/v0/transport", cs.handleSetTransport)
	mux.HandleFunc("/v0/reconnect", cs.handleReconnect)
	mux.HandleFunc("/v0/rehijack", cs.handleRehijack)
	mux.HandleFunc("/v0/reload", cs.handleReload)
	return mux
}

func (cs *controlServer) handleReconnect(w http.ResponseWriter, r *http.Request) {
	if !cs.requireOwnerOrRoot(w, r) {
		return
	}
	if err := cs.mut.Reconnect(); err != nil {
		writeJSON(w, http.StatusInternalServerError, controlResponse{Status: "error", Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, controlResponse{Status: "ok", State: "reconnected"})
}

// handleReload 热重载路由规则(bx direct/proxy 改配置后触发):重读配置 rules、
// 重建 router 原子换入(与 china 列表刷新同一路径),不断隧道、不碰 TUN/路由。
// 同步执行并回报成败(router 重建很快,不等隧道健康)。
func (cs *controlServer) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, controlResponse{Status: "error", Error: "method not allowed"})
		return
	}
	if !cs.requireOwnerOrRoot(w, r) {
		return
	}
	if cs.reload == nil {
		writeJSON(w, http.StatusNotImplemented, controlResponse{Status: "error", Error: "reload 不可用"})
		return
	}
	if err := cs.reload(); err != nil {
		writeJSON(w, http.StatusInternalServerError, controlResponse{Status: "error", Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, controlResponse{Status: "ok", State: "reloaded"})
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

// requireOwnerOrRoot 对 mutation 路由做 peer-cred 鉴权:授权 root 或配置的业主 uid(③-1);
// unix 连接时检查;非 unix(如 httptest TCP)放行。
func (cs *controlServer) requireOwnerOrRoot(w http.ResponseWriter, r *http.Request) bool {
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
	if !authorizeMutation(uid, gotUID, cs.ownerUID) {
		msg := "改动类命令需 root 或业主"
		if !peerCredSupported {
			msg = "此平台暂不支持 peer-cred,改动类已拒绝;macOS daemon 待实现 LOCAL_PEERCRED"
		}
		writeJSON(w, http.StatusForbidden, controlResponse{Status: "error", Error: msg})
		return false
	}
	return true
}

func (cs *controlServer) handleCommit(w http.ResponseWriter, r *http.Request) {
	if !cs.requireOwnerOrRoot(w, r) {
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
	if !cs.requireOwnerOrRoot(w, r) {
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

type setTransportReq struct {
	Link string `json:"link"`
}

func (cs *controlServer) handleSetTransport(w http.ResponseWriter, r *http.Request) {
	if !cs.requireOwnerOrRoot(w, r) {
		return
	}
	var req setTransportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Link == "" {
		writeJSON(w, http.StatusBadRequest, controlResponse{Status: "error", Error: "缺 link"})
		return
	}
	cs.mu.Lock()
	if cs.eng.State() == confirm.StateArmed {
		state := stateName(cs.eng.State())
		cs.mu.Unlock()
		writeJSON(w, http.StatusConflict, controlResponse{Status: "error", Error: "已有待确认的改动", State: state})
		return
	}
	apply, undo, merr := cs.mut.SetTransport(req.Link)
	if merr != nil {
		cs.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, controlResponse{Status: "error", Error: merr.Error()})
		return
	}
	armErr := cs.eng.Arm(apply, undo)
	state := stateName(cs.eng.State())
	cs.mu.Unlock()
	respondArm(w, armErr, state)
}

func (cs *controlServer) handleRehijack(w http.ResponseWriter, r *http.Request) {
	if !cs.requireOwnerOrRoot(w, r) {
		return
	}
	cs.mu.Lock()
	if cs.eng.State() == confirm.StateArmed {
		state := stateName(cs.eng.State())
		cs.mu.Unlock()
		writeJSON(w, http.StatusConflict, controlResponse{Status: "error", Error: "已有待确认的改动", State: state})
		return
	}
	apply, undo, merr := cs.mut.Rehijack()
	if merr != nil {
		cs.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, controlResponse{Status: "error", Error: merr.Error()})
		return
	}
	armErr := cs.eng.Arm(apply, undo)
	state := stateName(cs.eng.State())
	cs.mu.Unlock()
	respondArm(w, armErr, state)
}

// respondArm 映射 engine.Arm 的结果(无锁,调用方已释放 cs.mu)。
func respondArm(w http.ResponseWriter, armErr error, state string) {
	if armErr != nil {
		if errors.Is(armErr, confirm.ErrAlreadyArmed) {
			writeJSON(w, http.StatusConflict, controlResponse{Status: "error", Error: "已有待确认的改动", State: state})
			return
		}
		writeJSON(w, http.StatusInternalServerError, controlResponse{Status: "error", Error: armErr.Error(), State: state})
		return
	}
	writeJSON(w, http.StatusOK, controlResponse{Status: "armed", State: state})
}

func requireControlSocket(start controlStarter) (io.Closer, error) {
	closer, err := start()
	if err != nil {
		return nil, fmt.Errorf("控制 socket 启动失败: %w", err)
	}
	return closer, nil
}

// serveControl 在 SockPath 上跑控制面 HTTP server,替换旧的 serveStats。
// c: 统计计数器;t: 隧道(满足 tunnelStatser);server/udpMode: 配置字符串;eng: 引擎;mut: 改动执行器。
// transportInfo(可空)返回当前活跃传输标签、容灾列表、UDP 专用传输标签,供 status 呈现;
// active 动态(容灾后反映实际),list/udp 多为静态配置。
func serveControl(ctx context.Context, c *stats.Counters, t tunnelStatser, server, mode, udpMode string, transportInfo func() (string, []string, string), eng controlEngine, mut mutator, reload func() error, ownerUID uint32) (io.Closer, error) {
	guard := startNetworkGuard(ctx)
	report := func() stats.Report {
		ts := t.Stats()
		var active, udp string
		var list []string
		if transportInfo != nil {
			active, list, udp = transportInfo()
		}
		return stats.Report{
			Snapshot:      c.Snapshot(),
			Server:        server,
			SocksAddr:     t.SocksAddr(),
			TunnelHealthy: ts.Up,
			LatencyMS:     ts.LatencyMS,
			Restarts:      ts.Restarts,
			Mode:          mode,
			UDPMode:       udpMode,
			UDPNote:       udpNote(udpMode),
			Transport:     active,
			Transports:    list,
			UDPTransport:  udp,
			Warnings:      guard.warnings(),
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
		Handler:           newControlMux(eng, report, mut, reload, ownerUID),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			return context.WithValue(ctx, ctxConnKey{}, conn)
		},
	}
	go srv.Serve(ln) //nolint:errcheck
	return ln, nil
}
