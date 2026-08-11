// Package leakserve 起一次性的本机检测服务:127.0.0.1 上一个随机端口、
// 一个随机 token、拿到结果就关。
//
// **它必须比它检测的东西更干净。** 一个「报告本机网络姿态」的 loopback 服务,
// 做砸了就是让任意网页读到用户的网络指纹,或者更糟 —— 让同机任意进程投喂一份
// 伪造的上报,把 bx 说服成「无泄漏」。四道约束(只绑 loopback + 随机端口 /
// 每请求校验 token / 校验 Host 与 Origin 挡 DNS rebinding / 一次性 + 硬超时)
// 是硬要求,每一道都有自己的测试,删任何一道都必须有测试转红。
//
// 其中第二、三道**不在本包实现**:它们是 internal/loopbackgate,与
// `bx webrtc-check --browser` 共用同一份判据。一道安全闸门有两份实现,就有两份
// 会各自漂移的判据,而漂移是无声的。
package leakserve

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/loopbackgate"
)

// DefaultHardTimeout 是这个服务活着的绝对上限。用户关掉标签页就走人是常态,
// 没有它就会留一个开着的口。
const DefaultHardTimeout = 2 * time.Minute

// maxReportBytes 限制上报体大小。页面上报是几百字节量级。
const maxReportBytes = 1 << 20

// listenAddress 是这个服务唯一的监听地址,**不可配置**。
//
// 可配置的监听地址是这类工具最常见的一个洞:有人为了「在虚拟机/容器里也能开」
// 把它做成 flag,于是默认之外的每一次使用都在局域网上裸奔。这个服务报告的是
// 本机网络姿态 —— 那正是最不该对局域网开口的东西。端口交给内核随机分配(`:0`),
// 固定端口会让「同机其它进程猜到它在哪」变成一件不用猜的事。
//
// 注意 `127.0.0.1:0` 与 `:0` 差的不是写法:后者绑通配地址,等于对整个局域网
// 开口,而它在开发机上的表现与前者一模一样 —— 这个洞不会自己暴露。
const listenAddress = "127.0.0.1:0"

// Options 是起这个服务需要的全部东西。**刻意小**:见 bind_test.go 里那条
// 穷举字段名的守卫 —— 尤其不许出现任何形式的监听地址。
type Options struct {
	// Judge 把浏览器上报变成成品结论。注入而不是直接调 leakcheck.Judge,
	// 是因为本机事实那一半由调用方采集(平台相关),而本包不该知道怎么采。
	Judge func(leakcheck.BrowserReport) leakcheck.Report
	// HardTimeout 为零时用 DefaultHardTimeout。
	HardTimeout time.Duration
}

// Server 是一次检测专属的 loopback 服务。用完即弃,不可复用。
type Server struct {
	listener    net.Listener
	http        *http.Server
	gate        *loopbackgate.Gate
	hostPort    string
	judge       func(leakcheck.BrowserReport) leakcheck.Report
	hardTimeout time.Duration

	reports   chan leakcheck.Report
	closeOnce sync.Once
}

// Listen 绑定 127.0.0.1 上一个随机端口(约束一)。
func Listen(opts Options) (*Server, error) {
	gate := loopbackgate.New()
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return nil, err
	}
	timeout := opts.HardTimeout
	if timeout <= 0 {
		timeout = DefaultHardTimeout
	}
	// 端口是内核给的,只有 Listen 成功之后才知道;闸门要比对的正是这个地址。
	gate.BindTo(listener.Addr().String())
	srv := &Server{
		listener:    listener,
		gate:        gate,
		hostPort:    listener.Addr().String(),
		judge:       opts.Judge,
		hardTimeout: timeout,
		reports:     make(chan leakcheck.Report, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handlePage)
	mux.HandleFunc("/report", srv.handleReport)
	srv.http = &http.Server{
		Handler:           srv.guard(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv, nil
}

// Token 是本次运行专属的 token。**不是「用一次作废」**:页面至少要发两个请求
// (取页面 + 上报),用一次作废会让上报永远打不进来。「单次」指的是这一次运行
// 专属 —— 进程退出即消失,不落盘,不复用。
func (s *Server) Token() string  { return s.gate.Token() }
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Origin 是这个服务**唯一**合法的 Origin(闸门用它逐字比对)。
func (s *Server) Origin() string { return "http://" + s.hostPort }

// URL 是给浏览器的入口,自带 token。调用方自己拼 URL 就会拼错,而拼错不会有
// 任何报错 —— 只会得到一个打不开的页面。
func (s *Server) URL() string { return s.Origin() + "/?t=" + s.Token() }

// Serve 阻塞地跑这个服务,直到它被关掉。
func (s *Server) Serve() { _ = s.http.Serve(s.listener) }

// Close 立即关掉服务与 listener,可重复调用。
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.http.Close()
		// http.Server.Close 会关它自己接管过的 listener,但 Serve 还没被调用时
		// 不会 —— 这条路径在 Listen 成功而调用方直接 Close 时会走到。
		_ = s.listener.Close()
	})
	return err
}

// guard 是每个请求的必经之路:token、Host、Origin 全在 internal/loopbackgate 里判。
//
// **每个请求都校验**,不做「第一次之后就信这个连接」这类优化:HTTP 连接是可以
// 被复用的,而 token 挡的就是同机别的进程。
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.gate.Allow(w, r, requiresOrigin(r)) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requiresOrigin 判定这次请求是不是**写**入口 —— 只有写入口要求 Origin 必须在场。
//
// **这条例外不是妥协,是规范**:顶层导航(用户打开页面那一下)按规范不带
// `Origin`,无条件要求它会让页面自己都打不开 —— 这正是那类「照字面实现安全要求
// 就把功能砸掉」的条款。而豁免的**理由**是「它是一次导航 GET」,所以判据就写成
// 方法,而不是写成某个路径:将来多一个写端点,它不该因为没人记得改这里而漏掉。
//
// 挡 DNS rebinding 的那一半(Host 逐字比对)与「Origin 在场就必须相符」不受此
// 影响,它们对**每个**请求无条件生效,由 internal/loopbackgate 执行。
func requiresOrigin(r *http.Request) bool {
	return r.Method != http.MethodGet && r.Method != http.MethodHead
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		// ServeMux 的 "/" 是通配。不加这一句,`/debug/pprof/` 之类的路径全都会
		// 返回页面,而「最小面」这条约束就只剩注释。
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 页面内容在 Task 12 接上;先给一个占位,让约束测试能跑。
	_, _ = io.WriteString(w, "<!doctype html><title>bx leak check</title>")
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var browser leakcheck.BrowserReport
	if err := json.NewDecoder(io.LimitReader(r.Body, maxReportBytes)).Decode(&browser); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	report := s.judge(browser)
	select {
	case s.reports <- report:
	default:
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(report)
	// **拿到结果即关**(约束四)。用户关掉标签页就走人是常态,没有这一步就会
	// 留一个开着的口。
	//
	// 用 goroutine 是因为我们正身处被关的那条连接上;用 Shutdown 而不是 Close
	// 是因为**「拿到结果」必须是「响应已经写完」** —— Close 会当场掐掉 active
	// 连接,而这条响应此刻还在 handler 的缓冲里,页面就会拿不到它要渲染的结论。
	// Shutdown 先关 listener(不再进新连接),再等这条连接回到 idle,那时响应
	// 已经发完。
	go s.shutdownAfterResponse()
}

// gracefulShutdownTimeout 是等响应发完的上限。到点仍未 idle 就硬关 ——
// **关不掉比迟一点关严重得多**,这个口不许因为某条连接赖着不走就一直挂着。
const gracefulShutdownTimeout = 5 * time.Second

func (s *Server) shutdownAfterResponse() {
	ctx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
	defer cancel()
	_ = s.http.Shutdown(ctx)
	// 兜底:Shutdown 超时也要关,而且 listener 要一并关掉。
	_ = s.Close()
}

// Wait 等一份上报,或等到硬超时。**超时返回的是「什么都没问出来」的报告**,
// 不是错误、更不是空结构:调用方要渲染的正是那份 not checked。
func (s *Server) Wait(ctx context.Context) leakcheck.Report {
	timer := time.NewTimer(s.hardTimeout)
	defer timer.Stop()
	select {
	case report := <-s.reports:
		return report
	case <-timer.C:
		// 硬超时:用户关掉标签页就走人是常态,没有这一步就会留一个开着的口。
		_ = s.Close()
		return s.timedOutReport()
	case <-ctx.Done():
		_ = s.Close()
		return s.timedOutReport()
	}
}

// timedOutReport 是「什么都没问出来」的那份报告。
//
// **它必须是判出来的,不能是 leakcheck.Report{}。** 零值报告的 Findings 是空的、
// AnomalyCount 是 0,下游渲染出来就是一句「0 处异常」—— 与「全部检查通过」逐字
// 相同,而实情是一项都没跑成。这正是设计里的风险四:把「没跑成」渲染成「一切
// 正常」。让空上报走一遍 Judge,得到的是三条明写着 not checked 的结论。
func (s *Server) timedOutReport() leakcheck.Report {
	return s.judge(leakcheck.BrowserReport{})
}
