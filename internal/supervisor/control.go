// control.go 是 bx 守护进程的本地控制面:HTTP/1.1 over unix socket(Tailscale LocalAPI 范式)。
// GET /v0/status 返回 Report;GET /v0/runtime 返回非秘密交接状态;
// POST /v0/commit|rollback 驱动 commit-confirmed 引擎(peer-cred 仅 root)。
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
	"github.com/getbx/bx/internal/secdir"
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
	mu           sync.Mutex // 串行化命令(满足并发契约)
	shutdownOnce sync.Once
	eng          controlEngine
	report       func() stats.Report
	runtime      func() RuntimeState
	mut          mutator
	reload       func() error // 重读配置 rules 并热重建 router(不断隧道);可空
	// refreshBypass 重读配置、重算 serverBypass 集合并写进 mutator,回报集合是否变了。
	// requiredLinks 是调用方**点名**的必需链接(切换目标):端点自己知道要切到哪台,
	// 不该让闭包去读配置文件的 current 猜 —— spec 是「先热切、成功后才落盘 current」,
	// 猜必然错一次,而错的那一次就是不装 bypass 直接切过去 = 成环。
	// 可为 nil = 该部署不支持刷新(如没有 ConfigPath),此时 /v0/server 只做配对切换、
	// 不碰路由。
	refreshBypass func(requiredLinks []string) (changed bool, err error)
	ownerUID      uint32
	processPID    int
	shutdown      func()
	pathRecovery  *pathRecoveryOperation
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
	return newControlMuxWithRuntime(eng, report, nil, mut, reload, ownerUID)
}

func newControlMuxWithRuntime(eng controlEngine, report func() stats.Report, runtime func() RuntimeState, mut mutator, reload func() error, ownerUID uint32) http.Handler {
	return newControlMuxWithRuntimeAndShutdown(eng, report, runtime, mut, reload, ownerUID, 0, nil)
}

func newControlMuxWithRuntimeAndShutdown(eng controlEngine, report func() stats.Report, runtime func() RuntimeState, mut mutator, reload func() error, ownerUID uint32, processPID int, shutdown func()) http.Handler {
	return newControlMuxWithRuntimeAndShutdownAndPathRecovery(eng, report, runtime, mut, reload, ownerUID, processPID, shutdown, nil)
}

func newControlMuxWithPathRecovery(eng controlEngine, report func() stats.Report, mut mutator, reload func() error, ownerUID uint32, recoverer pathRecoverer) http.Handler {
	return newControlMuxWithRuntimeAndShutdownAndPathRecovery(eng, report, nil, mut, reload, ownerUID, 0, nil, recoverer)
}

func newControlMuxWithRuntimeAndShutdownAndPathRecovery(eng controlEngine, report func() stats.Report, runtime func() RuntimeState, mut mutator, reload func() error, ownerUID uint32, processPID int, shutdown func(), recoverer pathRecoverer) http.Handler {
	return newControlMuxFull(eng, report, runtime, mut, reload, nil, ownerUID, processPID, shutdown, recoverer)
}

// newControlMuxFull 是唯一真正构造 controlServer 的地方;上面几个包装只是历史调用点的
// 便捷入口(refreshBypass 传 nil = 不支持刷新)。
func newControlMuxFull(eng controlEngine, report func() stats.Report, runtime func() RuntimeState, mut mutator, reload func() error, refreshBypass func([]string) (bool, error), ownerUID uint32, processPID int, shutdown func(), recoverer pathRecoverer) http.Handler {
	cs := &controlServer{eng: eng, report: report, runtime: runtime, mut: mut, reload: reload, refreshBypass: refreshBypass, ownerUID: ownerUID, processPID: processPID, shutdown: shutdown}
	if recoverer != nil {
		cs.pathRecovery = newPathRecoveryOperation(recoverer)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/status", cs.handleStatus)
	mux.HandleFunc("/v0/runtime", cs.handleRuntime)
	mux.HandleFunc("/v0/capabilities", cs.handleCapabilities)
	mux.HandleFunc("/v0/commit", cs.handleCommit)
	mux.HandleFunc("/v0/rollback", cs.handleRollback)
	mux.HandleFunc("/v0/transport", cs.handleSetTransport)
	mux.HandleFunc("/v0/server", cs.handleSetServer)
	mux.HandleFunc("/v0/reconnect", cs.handleReconnect)
	mux.HandleFunc("/v0/path-recovery", cs.handlePathRecovery)
	mux.HandleFunc("/v0/rehijack", cs.handleRehijack)
	mux.HandleFunc("/v0/reload", cs.handleReload)
	mux.HandleFunc("/v0/shutdown", cs.handleShutdown)
	return mux
}

func (cs *controlServer) handlePathRecovery(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if cs.pathRecovery == nil {
			writeJSON(w, http.StatusNotImplemented, controlResponse{Status: "error", Error: "path recovery unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, cs.pathRecovery.Snapshot())
		return
	}
	if !cs.requireOwnerOrRoot(w, r) {
		return
	}
	if cs.pathRecovery == nil {
		writeJSON(w, http.StatusNotImplemented, controlResponse{Status: "error", Error: "path recovery unavailable"})
		return
	}
	var request PathRecoveryRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Reason == "" {
		writeJSON(w, http.StatusBadRequest, controlResponse{Status: "error", Error: "reason is required"})
		return
	}
	snapshot, err := cs.pathRecovery.Recover(r.Context(), request)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, snapshot)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (cs *controlServer) handleRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, controlResponse{Status: "error", Error: "method not allowed"})
		return
	}
	var state RuntimeState
	if cs.runtime != nil {
		state = cs.runtime()
	}
	writeJSON(w, http.StatusOK, state)
}

func (cs *controlServer) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, controlResponse{Status: "error", Error: "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, struct {
		SafeReconnect bool `json:"safe_reconnect"`
	}{SafeReconnect: true})
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

type shutdownRequest struct {
	ExpectedPID int `json:"expected_pid"`
}

func (cs *controlServer) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if !cs.requireOwnerOrRoot(w, r) {
		return
	}
	var request shutdownRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.ExpectedPID <= 0 {
		writeJSON(w, http.StatusBadRequest, controlResponse{Status: "error", Error: "expected_pid is required"})
		return
	}
	if request.ExpectedPID != cs.processPID {
		writeJSON(w, http.StatusConflict, controlResponse{Status: "error", Error: fmt.Sprintf("expected PID %d does not match Core PID %d", request.ExpectedPID, cs.processPID)})
		return
	}
	if cs.shutdown == nil {
		writeJSON(w, http.StatusNotImplemented, controlResponse{Status: "error", Error: "cooperative shutdown unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, controlResponse{Status: "ok", State: "shutting_down"})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	cs.shutdownOnce.Do(cs.shutdown)
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

type setServerReq struct {
	Link string `json:"link"`
	UDP  string `json:"udp"`
}

// handleSetServer 换一台服务器 = 一次把主传输与 UDP 专用传输一起换过去。
//
// 为什么不是对 /v0/transport 调两次:两次调用之间没有共同的 Arm/undo,做不到原子。
// 半切状态(TCP 在新那台、UDP 还在旧那台)是个合法配置 —— 不报错、还能用、
// status 也显绿,只是出口不是用户以为的那台。
//
// 与 /v0/transport 一样是 commit-confirmed:arm 后须在窗口内 /v0/commit,
// 否则死手到点自动 revert(undo + 路由快照网)。
func (cs *controlServer) handleSetServer(w http.ResponseWriter, r *http.Request) {
	if !cs.requireOwnerOrRoot(w, r) {
		return
	}
	var req setServerReq
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
	changed := false
	if cs.refreshBypass != nil {
		// ⚠️ 这一段必须留在 cs.mu 里面。刷新是**替换**语义(整组 bypass 换掉,
		// 不是并集),两次刷新一旦交错,后写的会抹掉前一次刚算进去的那台服务器,
		// 而那台的路由已经装上了 —— 集合与内核对不上,下一次 rehijack 把它拆掉
		// = 成环。把它挪到锁外面是个看起来很合理的重构(「别让 DNS 卡住控制面」),
		// 挪之前请先看 TestSetServerSerializesConcurrentBypassRefresh。
		// 持锁时长由刷新自己的 deadline 封顶,不靠挪出锁来解决。
		// 点名目标的两条链接:端点知道自己要切到哪台,直接说出来,不让刷新去猜。
		// udp 为空(目标没有 UDP 专用传输)时不塞空串 —— 空串取不出 host,
		// 会把一次完全正常的切换拒掉。
		requiredLinks := []string{req.Link}
		if req.UDP != "" {
			requiredLinks = append(requiredLinks, req.UDP)
		}
		var rerr error
		changed, rerr = cs.refreshBypass(requiredLinks)
		if rerr != nil {
			cs.mu.Unlock()
			// 落实不了新服务器的 bypass 就绝不切过去。切过去 = 隧道自己的流量
			// 被劫进 TUN = 成环,而成环是静默的(连得上、status 显绿、流量绕圈)。
			writeJSON(w, http.StatusInternalServerError,
				controlResponse{Status: "error", Error: "刷新 bypass 失败,已拒绝切换: " + rerr.Error()})
			return
		}
	}
	swapApply, swapUndo, merr := cs.mut.SetServer(req.Link, req.UDP)
	if merr != nil {
		cs.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, controlResponse{Status: "error", Error: merr.Error()})
		return
	}
	apply, undo := swapApply, swapUndo
	if changed {
		// 顺序是硬要求:先把新服务器的 bypass 路由装上,再换传输。
		// 反过来会在两步之间留下一个成环窗口。
		rhApply, rhUndo, rerr := cs.mut.Rehijack()
		if rerr != nil {
			cs.mu.Unlock()
			writeJSON(w, http.StatusInternalServerError, controlResponse{Status: "error", Error: rerr.Error()})
			return
		}
		apply, undo = composeMutations(rhApply, rhUndo, swapApply, swapUndo)
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
func serveControl(ctx context.Context, c *stats.Counters, t tunnelStatser, server, mode, udpMode string, transportInfo func() (string, []string, string), runtime func() RuntimeState, eng controlEngine, mut mutator, reload func() error, shutdown func(), ownerUID uint32) (io.Closer, error) {
	return serveControlWithPathRecovery(ctx, c, t, server, mode, udpMode, transportInfo, runtime, eng, mut, reload, nil, shutdown, ownerUID, nil)
}

func serveControlWithPathRecovery(ctx context.Context, c *stats.Counters, t tunnelStatser, server, mode, udpMode string, transportInfo func() (string, []string, string), runtime func() RuntimeState, eng controlEngine, mut mutator, reload func() error, refreshBypass func([]string) (bool, error), shutdown func(), ownerUID uint32, recoverer pathRecoverer) (io.Closer, error) {
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
	if err := secdir.Ensure(filepath.Dir(SockPath), os.Geteuid(), 0o755); err != nil {
		return nil, fmt.Errorf("准备控制 socket 目录: %w", err)
	}
	_ = os.Remove(SockPath)
	ln, err := net.Listen("unix", SockPath)
	if err != nil {
		return nil, err
	}
	// 0o666 让非 root 的 bx status/bx mcp 均可读;mutation 门控靠 peer-cred(POST 路由),不靠 socket 权限。
	_ = os.Chmod(SockPath, 0o666)
	srv := &http.Server{
		Handler:           newControlMuxFull(eng, report, runtime, mut, reload, refreshBypass, ownerUID, os.Getpid(), shutdown, recoverer),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			return context.WithValue(ctx, ctxConnKey{}, conn)
		},
	}
	go srv.Serve(ln) //nolint:errcheck
	return ln, nil
}
