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

	"github.com/getbx/bx/internal/appattr"
	"github.com/getbx/bx/internal/confirm"
	"github.com/getbx/bx/internal/dialer"
	"github.com/getbx/bx/internal/route"
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
// 供 requireOwnerPeer 做 peer-cred 鉴权(改动类路由与 /v0/apps 共用)。
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
	// probeDial 是**直连**拨号器(绕开自己装的路由),供 /v0/probe 量另一台
	// 服务器的往返时间。可为 nil = 该部署不支持探测(端点回 501)。
	probeDial    probeDialer
	ownerUID     uint32
	processPID   int
	shutdown     func()
	pathRecovery *pathRecoveryOperation
	// appTraffic 是应用流量归因的采集器。可为 nil = 该部署没有接线
	// (与 probeDial/pathRecovery 同一条纪律),此时 /v0/apps 回 501
	// 而不是一份看起来正常的空报告。
	appTraffic *AppTraffic
	// explain 回答「现在向这个目标发一条连接会发生什么、为什么」。
	// 可为 nil = 该部署没有接线,此时 /v0/explain 回 501 —— 与
	// probeDial/pathRecovery/appTraffic 同一条纪律:**「没接线」不是
	// 「没有答案」**,一份看起来正常的空答案比 501 糟得多。
	explain func(route.Meta) dialer.Outcome
}

// AppTrafficResponse 是 GET /v0/apps 的响应体。三态刻意分开发布:
// Subscribed 区分「没人在看」与「在看」,Error 非空时 Report 是
// Snapshot 报错那一路的零值(Groups==nil)—— 消费方必须先判 Error
// 再碰 Report,不许把两者合并成一份「看起来正常」的空报告。
type AppTrafficResponse struct {
	Subscribed bool           `json:"subscribed"`
	Report     appattr.Report `json:"report"`
	Error      string         `json:"error,omitempty"`
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
	return newControlMuxFull(controlMuxOptions{
		Engine: eng, Report: report, Runtime: runtime, Mutator: mut, Reload: reload,
		OwnerUID: ownerUID, ProcessPID: processPID, Shutdown: shutdown, Recoverer: recoverer,
	})
}

// newControlMuxWithAppTraffic 是测试专用的便捷入口 —— 只暴露本包需要的形参,
// 其余(runtime/refreshBypass/processPID/shutdown/recoverer/probeDial)按生产
// 未接线的默认值传零值,与 newControlMuxWithPathRecovery 同一手法。
func newControlMuxWithAppTraffic(eng controlEngine, report func() stats.Report, mut mutator, ownerUID uint32, appTraffic *AppTraffic) http.Handler {
	return newControlMuxFull(controlMuxOptions{
		Engine: eng, Report: report, Mutator: mut, OwnerUID: ownerUID, AppTraffic: appTraffic,
	})
}

// controlMuxOptions 打包 newControlMuxFull 的全部依赖。**不用位置参数**——
// 这个构造函数是控制面 mux 唯一真正的组装点,历史上每加一个能力(runtime/
// shutdown/path-recovery/probe/appTraffic……)就在参数列表末尾追加一个位置参数,
// 到 appTraffic 已经是第 12 个;审查指出再加就是第 13 个,而这个仓库的事故
// 反复发生在组装根上——位置参数一多,调用点漏传或错位一个 nil 编译器完全
// 看不出来(`newControlMuxFull(eng, report, runtime, mut, reload, refreshBypass,
// ownerUID, processPID, shutdown, recoverer, probeDial, nil)` 这种调用,少一个逗号
// 或错一个位置,类型系统未必能拦住同类型的两个 nil 参数)。具名字段把「传错
// 位置」的错误从运行期行为差异变成了不存在(打错字段名是编译错误,漏填字段
// 是零值且看得见字段名)。
type controlMuxOptions struct {
	Engine        controlEngine
	Report        func() stats.Report
	Runtime       func() RuntimeState
	Mutator       mutator
	Reload        func() error // 重读配置 rules 并热重建 router;可空
	RefreshBypass func(requiredLinks []string) (changed bool, err error)
	OwnerUID      uint32
	ProcessPID    int
	Shutdown      func()
	Recoverer     pathRecoverer // 可空 = 该部署不支持路径恢复
	ProbeDial     probeDialer   // 可空 = 该部署不支持探测(端点回 501)
	AppTraffic    *AppTraffic   // 可空 = 该部署没有接线(端点回 501)
	// Explain 回答「现在向这个目标发一条连接会发生什么」。
	// 可空 = 该部署没有接线(端点回 501,而不是一份看起来正常的空答案)。
	Explain func(route.Meta) dialer.Outcome
}

// newControlMuxFull 是唯一真正构造 controlServer 的地方;上面几个包装只是历史调用点的
// 便捷入口(RefreshBypass 留零值 = 不支持刷新)。
func newControlMuxFull(opts controlMuxOptions) http.Handler {
	cs := &controlServer{
		eng: opts.Engine, report: opts.Report, runtime: opts.Runtime, mut: opts.Mutator,
		reload: opts.Reload, refreshBypass: opts.RefreshBypass, ownerUID: opts.OwnerUID,
		processPID: opts.ProcessPID, shutdown: opts.Shutdown, probeDial: opts.ProbeDial,
		appTraffic: opts.AppTraffic, explain: opts.Explain,
	}
	if opts.Recoverer != nil {
		cs.pathRecovery = newPathRecoveryOperation(opts.Recoverer)
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
	mux.HandleFunc("/v0/apps", cs.handleApps)
	mux.HandleFunc("/v0/explain", cs.handleExplain)
	mux.HandleFunc("/v0/probe", cs.handleProbe)
	mux.HandleFunc("/v0/shutdown", cs.handleShutdown)
	return mux
}

// handleProbe 量一次到某台服务器的直连往返时间。
//
// **要 owner 或 root。** 它会真的发出一个包,而且是**在隧道外面**发的 ——
// 那既是一次出站,也让网络上看得见这台机器联系过那个地址。读状态谁都可以,
// 这个不行。
// handleExplain 回答「现在向这个目标发一条连接会发生什么」。**纯读**:
// 不拨号、不解析、不记任何账(见 dialer.Explain 与 TestExplainRecordsNothing)。
func (cs *controlServer) handleExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, controlResponse{Status: "error", Error: "method not allowed"})
		return
	}
	if !cs.requireOwnerPeer(w, r) {
		return
	}
	if cs.explain == nil {
		writeJSON(w, http.StatusNotImplemented, controlResponse{Status: "error", Error: "explain unavailable"})
		return
	}
	target := r.URL.Query().Get("target")
	m, err := explainTarget(target)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, controlResponse{Status: "error", Error: err.Error()})
		return
	}
	udpMeta := m
	udpMeta.UDP = true
	var rep stats.Report
	if cs.report != nil {
		rep = cs.report()
	}
	writeJSON(w, http.StatusOK, buildExplainResponse(target, cs.explain(m), cs.explain(udpMeta), rep))
}

func (cs *controlServer) handleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, controlResponse{Status: "error", Error: "method not allowed"})
		return
	}
	if !cs.requireOwnerOrRoot(w, r) {
		return
	}
	if cs.probeDial == nil {
		// 「没接线」不是「测不通」—— 后者会让界面把一台好服务器标成红的。
		writeJSON(w, http.StatusNotImplemented, controlResponse{Status: "error", Error: "probe unavailable"})
		return
	}
	var req ProbeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, controlResponse{Status: "error", Error: "bad request"})
		return
	}
	// **刻意不持 cs.mu。** 探测最长 8 秒,而那把锁串行化的是改动类命令;
	// 让一次测速把 /v0/shutdown 挡在门外,是拿一个诊断功能去堵停止路径。
	writeJSON(w, http.StatusOK, probeServer(r.Context(), cs.probeDial, req))
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

// handleApps 发布应用流量归因报告(只读)。`?subscribe=1` 时先续期订阅
// 再取快照 —— 消费方每次拉取都会带上它,同时兼具续期作用(30 秒 TTL)。
//
// **这个只读端点也要过 peer-cred 门(requireOwnerPeer),与改动类同一份判据。**
// 控制 socket 是 0666(serveControlWithPathRecovery 里那句 os.Chmod),而本端点
// ① 发布的是实时的「哪个应用在走隧道/直连/被拦」清单(含应用名、连接数、
// 字节量、命中的用户规则原文),② 带副作用 —— 一句 `?subscribe=1` 就能无限期
// 把采集打开(每次拉取都续 30 秒 TTL)。没有这道门时,Guardian 那一层刻意加的
// authorizeOwnerPeer 就不是真正的边界,它下面一层是敞开的:本机任何 uid 的任何
// 进程一句 curl --unix-socket 即可绕过。**授权在「没接线」之前** —— 端点接没
// 接线本身也是信息,不该发给未授权的 peer。
// 生产里唯一的消费方是 Guardian(root),门不影响它;CLI 没有消费方。
//
// **三态必须分开发布,不许合并成一份空报告**:没人订阅 / 订阅了但问不出来 /
// 订阅了且确实没有连接,是三种不同的事实。Snapshot 报错时返回的是零值
// Report(Groups==nil)——这里必须**先判 err 再碰 report**,否则会把「没查
// 出来」发布成「查过、一个应用都没有」这句自洽的假话。
func (cs *controlServer) handleApps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, controlResponse{Status: "error", Error: "method not allowed"})
		return
	}
	if !cs.requireOwnerPeer(w, r) {
		return
	}
	if cs.appTraffic == nil {
		// 「没接线」不是「没有应用」—— 与 handleProbe/handlePathRecovery 同一条纪律。
		writeJSON(w, http.StatusNotImplemented, controlResponse{Status: "error", Error: "app attribution unavailable"})
		return
	}
	if r.URL.Query().Get("subscribe") == "1" {
		cs.appTraffic.Subscribe()
	}
	report, subscribed, err := cs.appTraffic.Snapshot()
	if err != nil {
		// 「问不出来」是数据,不是服务端故障:仍是 200,report 字段原样透传
		// Snapshot 给的零值,绝不额外拼一份看起来正常的空报告。
		writeJSON(w, http.StatusOK, AppTrafficResponse{Subscribed: subscribed, Report: report, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, AppTrafficResponse{Subscribed: subscribed, Report: report})
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

// requireOwnerOrRoot 对 mutation 路由做鉴权:先卡死方法(改动类只收 POST),
// 再走共用的 peer-cred 判据。
func (cs *controlServer) requireOwnerOrRoot(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, controlResponse{Status: "error", Error: "method not allowed"})
		return false
	}
	return cs.requireOwnerPeer(w, r)
}

// requireOwnerPeer 是这个控制面 peer-cred 判据的**唯一**落点:授权 root 或
// 配置的业主 uid(③-1);unix 连接时检查,非 unix(如 httptest TCP)放行。
//
// **与方法门刻意分开**,因为不是所有需要授权的路由都是 POST —— GET /v0/apps
// 带副作用(?subscribe=1 会开采集并续 TTL)且发布的是实时的应用级隐私数据,
// 同样要过这道门。抽出来而不是在 handleApps 里另写一份,是因为这个仓库反复
// 栽在「判据抄两份」上(见 CLAUDE.md:route.Explain/Decide、leakcheck、
// rulereview 三处都是同一条纪律)——两份判据里只会有一份被后来的人改到。
//
// 非 darwin/linux 上 peerCredSupported=false ⇒ peerCredUID 恒 (0,false) ⇒
// authorizeMutation 恒 false ⇒ fail-closed 拒绝,与仓库既有纪律一致。
func (cs *controlServer) requireOwnerPeer(w http.ResponseWriter, r *http.Request) bool {
	conn, _ := r.Context().Value(ctxConnKey{}).(net.Conn)
	if conn == nil {
		// 无 unix conn(如 httptest TCP):放行,peer-cred 鉴权由 authorizeMutation 单测
		// 与走真实 unix socket 的 TestControlAppsOverRealUnixSocketHonoursOwnerUID 覆盖。
		return true
	}
	uid, gotUID := peerCredUID(conn)
	if !authorizeMutation(uid, gotUID, cs.ownerUID) {
		msg := "此命令需 root 或业主"
		if !peerCredSupported {
			msg = "此平台暂不支持 peer-cred,已拒绝"
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

// newStatusReporter 组装 report 闭包:把计数器快照、隧道状态、运行时状态、
// network guard 告警与配置派生告警(configWarnings)拼成一份 stats.Report。
//
// **单独成一个具名函数,是为了让它能在不建 socket、不需要 root 的情况下被直接
// 调用测试。** serveControlWithPathRecovery 本身要在 SockPath(darwin
// `/var/run/bx`、linux `/run/bx`)下经 secdir.Ensure 建目录再 net.Listen ——
// SockPath 是编译期常量,不是可注入的测试缝,把它改成可覆盖的变量是一次比
// 「configWarnings 有没有到达 Report.Warnings」这个问题大得多的重构。本机以
// 非 root 身份实测过:`mkdir /var/run/bx-probe` → `Permission denied`(uid=501)。
// 这个提取是不碰 SockPath、不需要 root 就能验证生产代码真的把 configWarnings
// 拼进了 Warnings 字段的唯一办法——TestStatusReporterIncludesBothGuardAndConfigWarnings
// (control_reporter_test.go)直接调用它,不是重新拼一遍它的逻辑。
func newStatusReporter(c *stats.Counters, t tunnelStatser, server, mode, udpMode string, transportInfo func() (string, []string, string), runtime func() RuntimeState, guard *networkGuard, rate *stats.RateMeter, configWarnings []stats.Warning,
	// history 给出跨重启累计的按规则计数。**必填,不认 nil provider** ——
	// 漏传就编不过,那是接线正确的唯一硬凭据。(编译器只证明**实参被传了**、
	// 不证明它**被用了**,所以另有一条走真 reporter 的测试盯着后半句。)
	history func() *stats.RuleHistorySnapshot,
) func() stats.Report {
	if history == nil {
		// 传 nil 是编程错误,不是运行期情况。**当场 panic 好过静默发布 nil** ——
		// 后者会让死规则那一类永远显示「没查」,而没有任何一处会说为什么。
		panic("newStatusReporter: history provider 必填")
	}
	return func() stats.Report {
		ts := t.Stats()
		var active, udp string
		var list []string
		if transportInfo != nil {
			active, list, udp = transportInfo()
		}
		// 配置路径取自 Core 自己的运行时状态 —— 发布**它此刻在用的**那个文件,
		// 不是某处猜出来的默认值(与 DNSUpstream 同一条纪律)。
		var configPath, liveServer string
		if runtime != nil {
			rs := runtime()
			configPath = rs.ConfigPath
			// **热切之后「节点」必须跟着变。** 启动时那个值在 `bx server use`
			// 之后会指向旧服务器,而出口早已换了 —— status 说的与实际不符。
			liveServer = rs.ServerHost
		}
		reportServer := server
		if liveServer != "" {
			reportServer = liveServer
		}
		peakBPS, peakAt, havePeak := rate.PeakBPS(time.Now())
		if !havePeak {
			// **没观测到就一个字都不发。** 0 会被读成「这条隧道跑不动」,
			// 而真相是「这段时间没人用它传东西」。
			peakBPS = 0
			peakAt = time.Time{}
		}
		return stats.Report{
			Snapshot: c.Snapshot(),
			// **与 Snapshot.Rules 并列,绝不合并。** 那一份是「本次运行」,
			// 这一份是「跨重启累计」——「0 次」在前者里什么也说明不了。
			RuleHistory:   history(),
			PeakBPS:       peakBPS,
			PeakAt:        peakAt,
			ConfigPath:    configPath,
			Server:        reportServer,
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
			// 配置派生的告警(危险直连规则)在 Run 里算好一次传进来 ——
			// **不在读状态那条路上重算**:菜单每 2 秒拉一次,而配置在运行期不变
			// (bx 不热重载),重算既浪费又会让 status 说出 Core 此刻并没有在用的那份配置。
			Warnings: append(guard.warnings(), configWarnings...),
		}
	}
}

// controlServeOptions 打包 serveControlWithPathRecovery 的全部依赖(ctx 除外,
// 按惯例留作独立的第一个形参)。**同一个理由,不用位置参数**:这个函数曾是
// 17 个位置参数,审查指出这正是「静默传错一个 nil」的形状本身——本仓库曾有
// 一个叫 serveControl 的瘦包装函数,在位置上传了三个 `nil`(refreshBypass/
// recoverer/probeDial),多一个字段(本轮的 AppTraffic)就会变成第 18 个位置、
// 第四个挨着写的 nil,谁也分不清哪个 nil 对应哪个依赖。**该函数复审确认全仓
// 零引用、零测试(比"被绿色测试守着的死代码"更坏),修复轮 2 已整个删除**——
// 它承载的「哪些能力这个部署没有」这份信息,现在由 controlServeOptions 的
// 字段零值 + 字段名本身表达得更清楚,不需要一个没人调用的包装函数来演示。
type controlServeOptions struct {
	Counters       *stats.Counters
	Tunnel         tunnelStatser
	Server         string
	Mode           string
	UDPMode        string
	TransportInfo  func() (string, []string, string)
	Runtime        func() RuntimeState
	Engine         controlEngine
	Mutator        mutator
	Reload         func() error
	RefreshBypass  func([]string) (bool, error)
	Shutdown       func()
	OwnerUID       uint32
	Recoverer      pathRecoverer
	ProbeDial      probeDialer
	ConfigWarnings []stats.Warning
	// RuleHistory 给出跨重启累计的按规则计数。**必填** —— newStatusReporter 对
	// nil provider 直接 panic:静默发布 nil 会让死规则那一类永远显示「没查」,
	// 而没有任何一处会说为什么。
	RuleHistory func() *stats.RuleHistorySnapshot
	// AppTraffic 是应用流量归因采集器,接进控制面才能让 GET /v0/apps 真的
	// 发布报告(而不是恒 501)。留零值 = 该部署没有接线。
	AppTraffic *AppTraffic
	// Explain 接进控制面才能让 GET /v0/explain 真的作答。留零值 = 没接线。
	Explain func(route.Meta) dialer.Outcome
}

// controlMuxOptionsFromServe 把 serveControlWithPathRecovery 收到的依赖翻译成
// newControlMuxFull 要的 controlMuxOptions。**单独抽成一个纯函数**,理由与
// newStatusReporter(上面)完全相同——serveControlWithPathRecovery 本身要在
// SockPath 下建 unix socket,非 root 测不了;而 opts.AppTraffic(以及其余每
// 一个字段)有没有真的搬到 controlMuxOptions 里,是一次纯粹的字段翻译,没有
// 理由绑死在需要 root 的那条路径上。少翻译一个字段(例如漏写 AppTraffic:
// opts.AppTraffic)编译器不会报错——这正是当初把 appTraffic 传进
// controlServeOptions、却在这一跳漏接的那类事故,由
// TestControlMuxOptionsFromServeCarriesEveryField 直接调用本函数钉住。
func controlMuxOptionsFromServe(opts controlServeOptions, report func() stats.Report, processPID int) controlMuxOptions {
	return controlMuxOptions{
		Engine: opts.Engine, Report: report, Runtime: opts.Runtime, Mutator: opts.Mutator,
		Reload: opts.Reload, RefreshBypass: opts.RefreshBypass, OwnerUID: opts.OwnerUID,
		ProcessPID: processPID, Shutdown: opts.Shutdown, Recoverer: opts.Recoverer,
		ProbeDial: opts.ProbeDial, AppTraffic: opts.AppTraffic, Explain: opts.Explain,
	}
}

// controlMuxOptionsForServe 是 serveControlWithPathRecovery 的**组装那一半**,
// 单独抽出来只为一件事:让它可测。
//
// 它上下两跳早就各有覆盖(`controlMuxOptionsFromServe` 有逐字段单测,
// `run.go → opts` 有文本守卫),**唯独这几行自己没有** —— 而两轮复审各实测过一次:
// 把 `opts.ConfigWarnings` 换成 nil、或在这一跳丢掉 `AppTraffic`,
// `internal/supervisor` 与 `internal/cli` **两个包都绿**。后者的生产后果是
// `/v0/apps` 变成永久 501:菜单那个窗口一个应用都不显示,而没有任何报错。
//
// 「这个仓库全部的事故都在组装根上」是本仓库自己的立论,而这正是一个组装根。
//
// **`newStatusReporter` 有 10 个位置参数,其中 server/mode/udpMode 是连着三个
// string** —— 换位不会有任何编译错误,而症状是 `bx status` 里三个字段互相串台。
// TestControlMuxOptionsForServeCarriesEveryField 用三个互不相同的值把顺序钉住。
//
// 不碰 socket、不碰 /var/run —— 那一半仍留在 serveControlWithPathRecovery 里,
// 它非 root 测不了(secdir.Ensure 要 MkdirAll 到 /var/run)。
func controlMuxOptionsForServe(ctx context.Context, opts controlServeOptions, pid int) controlMuxOptions {
	guard := startNetworkGuard(ctx)
	// **吞吐要按固定节拍采样,不能搭在读状态那条路上。**
	// 读状态的间隔由调用方决定(菜单开着 2 秒、关着 30 秒、CLI 一次就走),
	// 而峰值是一个「有没有在那一秒看到」的问题 —— 采样疏了就整个错过。
	rate := &stats.RateMeter{}
	go sampleThroughput(ctx, opts.Counters, rate)
	report := newStatusReporter(opts.Counters, opts.Tunnel, opts.Server, opts.Mode, opts.UDPMode, opts.TransportInfo, opts.Runtime, guard, rate, opts.ConfigWarnings, opts.RuleHistory)
	return controlMuxOptionsFromServe(opts, report, pid)
}

func serveControlWithPathRecovery(ctx context.Context, opts controlServeOptions) (io.Closer, error) {
	muxOpts := controlMuxOptionsForServe(ctx, opts, os.Getpid())
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
		Handler:           newControlMuxFull(muxOpts),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			return context.WithValue(ctx, ctxConnKey{}, conn)
		},
	}
	go srv.Serve(ln) //nolint:errcheck
	return ln, nil
}

// sampleThroughput 按固定节拍把累计字节数喂给速率计。
//
// 一秒一拍、一个 goroutine、随 ctx 结束 —— 它只读两个 atomic,代价可以忽略,
// 而换来的是「这条隧道实际跑多快」这个 bx 本来就看得见、却一直扔掉的事实。
func sampleThroughput(ctx context.Context, c *stats.Counters, rate *stats.RateMeter) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			snap := c.Snapshot()
			rate.Observe(now, snap.BytesUp, snap.BytesDown)
		}
	}
}
