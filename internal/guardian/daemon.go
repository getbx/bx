package guardian

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/getbx/bx/internal/setup"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/observe"
	"github.com/getbx/bx/internal/runtimedir"
	"github.com/getbx/bx/internal/secdir"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/getbx/bx/internal/version"
)

// RuntimeDir 是 bx 自有的运行期目录:socket 等「路径即信任边界」的对象都放这里。
// 不能用 /var/run 本身——macOS 出厂 /var/run 是 0775 root:daemon(组可写),
// 任何「父目录不可被他人写」的加固在那里都不可能成立。
const RuntimeDir = "/var/run/bx"

const SocketPath = RuntimeDir + "/guardian.sock"

const (
	guardianRecoveryRetryInitial = 100 * time.Millisecond
	guardianRecoveryRetryMax     = 5 * time.Second
)

type DaemonOptions struct {
	ConfigPath       string
	DNSListen        string
	SocketPath       string
	Handler          http.Handler
	OwnerUID         uint32
	LocalAPIOwnerUID uint32
	PeerCredentials  func(net.Conn) (uint32, bool)
	// UpdateCheck backs GET /v1/update-check. It is supplied by the caller
	// (internal/cli) because the release lookup + signed manifest verification
	// already lives there; Guardian publishes the answer, it does not
	// reimplement the question. Nil leaves the endpoint answering 501.
	UpdateCheck            func(context.Context) (UpdateAvailability, error)
	networkObserver        daemonNetworkObserver
	networkObserverDesired func() DesiredState
}

type Daemon struct {
	path                string
	listener            net.Listener
	server              *http.Server
	socketInfo          os.FileInfo
	mutations           mutationLifecycle
	recoveries          recoveryLifecycle
	observer            *daemonNetworkObserverLifecycle
	startupRecoveryDone chan struct{}
	reconcileLoopCancel context.CancelFunc
	reconcileLoopDone   chan struct{}
	shutdownMu          sync.Mutex
	shutdownStarted     bool
	shutdownComplete    bool
	shutdownErr         error
}

type mutationLifecycle interface {
	beginShutdown()
	waitForMutations(context.Context) error
}

type recoveringController interface {
	Controller
	PathRecoveryController
	Recover(context.Context) error
	BeginStartupRecovery()
	RunStartupRecovery(context.Context) error
	AbortStartupRecovery()
}

type daemonStarter func(context.Context, DaemonOptions) (*Daemon, error)

type daemonNetworkObserver interface {
	Run(context.Context)
}

type DaemonShutdownError struct {
	Stage string
	Err   error
}

func (e *DaemonShutdownError) Error() string {
	return fmt.Sprintf("Guardian shutdown %s: %v", e.Stage, e.Err)
}

func (e *DaemonShutdownError) Unwrap() error {
	return e.Err
}

func StartDaemon(ctx context.Context, options DaemonOptions) (*Daemon, error) {
	path := options.SocketPath
	if path == "" {
		path = SocketPath
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("Guardian socket path must be absolute")
	}
	if options.Handler == nil {
		return nil, errors.New("Guardian HTTP handler required")
	}
	if err := prepareSocketDirectory(filepath.Dir(path), options.OwnerUID); err != nil {
		return nil, err
	}
	if err := removeStaleSocket(path, options.OwnerUID); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	closeListener := func() {
		_ = listener.Close()
		_ = os.Remove(path)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		closeListener()
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		closeListener()
		return nil, err
	}
	uid, gotUID := fileOwnerUID(info)
	if !gotUID || uid != options.OwnerUID {
		closeListener()
		return nil, fmt.Errorf("Guardian socket owner is %d, want %d", uid, options.OwnerUID)
	}
	credentials := options.PeerCredentials
	if credentials == nil {
		credentials = localPeerCredentials
	}
	handler := options.Handler
	var observer *daemonNetworkObserverLifecycle
	if options.networkObserver != nil {
		observer = newDaemonNetworkObserverLifecycle(ctx, options.networkObserver)
		handler = &networkObserverDesiredHandler{
			next:     handler,
			observer: observer,
			desired:  options.networkObserverDesired,
		}
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			uid, got := credentials(conn)
			return withPeerCredentials(ctx, uid, got)
		},
	}
	mutations, _ := options.Handler.(mutationLifecycle)
	recoveries, _ := options.Handler.(recoveryLifecycle)
	daemon := &Daemon{
		path: path, listener: listener, server: server, socketInfo: info,
		mutations: mutations, recoveries: recoveries, observer: observer,
	}
	if observer != nil {
		observer.syncDesired(context.Background(), observerDesiredState(options.networkObserverDesired))
	}
	go func() { _ = server.Serve(listener) }()
	go func() {
		<-ctx.Done()
		_ = daemon.Close()
	}()
	return daemon, nil
}

// trackStartupRecovery runs fn (the background startup-recovery goroutine) in
// a new goroutine and records its completion so Shutdown can wait for it
// instead of exiting mid-recovery, which could otherwise orphan Core or leave
// the path-recovery fence installed. Must be called at most once per Daemon.
func (d *Daemon) trackStartupRecovery(fn func()) {
	d.startupRecoveryDone = make(chan struct{})
	go func() {
		defer close(d.startupRecoveryDone)
		// **这条 goroutine 里的 panic 会打死整个 Guardian,而 launchd 的 KeepAlive
		// 会把它拉起来再跑一遍启动恢复 —— 崩溃循环。**
		//
		// Down 没有这个问题:它经 LocalAPI 的 HTTP handler 进来,net/http 会按连接
		// 兜住。启动恢复这条路上没有任何等价物。
		//
		// 具体的可疑面是 scanRunningCores(对 sysctl 返回的 kinfo 切片做索引运算,
		// 畸形或截断的应答并非不可能),而 recoverLocked 有**三个**入口会走到它:
		// confirmCoreStopped(desired=off)、runner.Existing 与 runner.Start
		// (desired=on 那条常见分支)。上一版只在 confirmCoreStopped 里收了一个,
		// 那是修了一个实例、看起来像修了那类问题。这里收在源头。
		//
		// 收掉之后启动恢复失败,由外层的重试循环按退避再试 —— 与其它失败同路。
		defer func() {
			if r := recover(); r != nil {
				log.Printf("guardian_startup_recovery_panicked recovered=%v\n%s", r, debug.Stack())
			}
		}()
		fn()
	}()
}

// reconcileLoopRunner 由能跑阶段③a 只观察调谐环的 controller 实现(今天只有
// *Manager)。做成**可选**接口而不是塞进 recoveringController:daemon 层的一堆
// 测试替身没有理由为了一个什么都不做的循环各自实现一个空方法,而循环缺席时
// 正确的行为就是「不跑」。
type reconcileLoopRunner interface {
	runReconcileLoop(context.Context, reconcileObservation)
}

// trackReconcileLoop 起那条只观察的调谐环,并记下它的结束,好让 Shutdown 叫停它。
//
// **每个 Daemon 至多一条,而且这条由代码强制,不是注释。** 上一版把它写成一句
// 「Must be called at most once」:第二次调用会覆写 reconcileLoopCancel 与
// reconcileLoopDone,于是第一条循环失去被叫停的把手、也失去被等到的把手,却仍在
// 后台按拍观测 —— 一条谁都不知道它还在的循环,正是本项目反复吃亏的那种孤儿
// (孤儿屏障、孤儿启动标记)。这里选择**拒绝第二次并留住第一条**:第二条从来
// 没有存在过,而已经在跑的那条依旧受管。
func (d *Daemon) trackReconcileLoop(ctx context.Context, run func(context.Context)) {
	if d.reconcileLoopDone != nil {
		// 这是编程错误,但绝不能用 panic 表达:Guardian 由 launchd 的 KeepAlive
		// 托管,一次启动路径上的 panic 就是崩溃循环,而用户机器上可能还留着屏障。
		log.Print("guardian_reconcile_loop_already_running refused=second_loop")
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	d.reconcileLoopCancel = cancel
	d.reconcileLoopDone = make(chan struct{})
	go func() {
		defer close(d.reconcileLoopDone)
		// **这条 goroutine 里的 panic 会打死整个 Guardian**,而 launchd 的
		// KeepAlive 会把它拉起来、再起这条循环、再 panic —— 崩溃循环,而且用户
		// 机器上可能还留着屏障。理由与 trackStartupRecovery 那条一字不差:
		// 这里没有 net/http 那样按连接兜底的东西。
		//
		// **这一层是最后一道网,不是主力。** 主力在 runReconcileRound 里:那里
		// 的 recover 只吞掉**这一轮**,循环照跑(带退避)。收在这一层的后果是
		// 「第一次 panic 就是循环的终点」—— 循环没了,而最后一份报告冻在
		// Status 上被 bx status 渲染成一轮干净的观测。
		//
		// 留着它,是因为 panic 也可能发生在轮次之外(pacing、waitReconcileInterval,
		// 或者将来有人往循环骨架里加东西),而那时进程被带走的后果与下面写的一样。
		defer func() {
			if r := recover(); r != nil {
				log.Printf("guardian_reconcile_loop_panicked recovered=%v\n%s", r, debug.Stack())
			}
		}()
		run(loopCtx)
	}()
}

// stopReconcileLoop 叫停那条循环。**不等它退出**:它只读、不持有任何锁,也不
// 改任何状态,没有什么需要等它做完;而它可能正卡在一次外部命令上,让关机去等
// 一个纯观测,方向正好与「停止永不依赖别的先成功」相反。
func (d *Daemon) stopReconcileLoop() {
	if d.reconcileLoopCancel != nil {
		d.reconcileLoopCancel()
	}
}

// liveReconcileObservation 是生产观测:向真实系统现问,不改动任何状态。
// 与 bx status 用的是同一个 observe.LiveDeps。
func liveReconcileObservation(ctx context.Context) observe.ObservedState {
	return observe.Observe(ctx, observe.LiveDeps(supervisor.SockPath))
}

// waitForStartupRecovery blocks until the tracked startup-recovery goroutine
// finishes or ctx is done, whichever comes first — it never blocks forever.
// In the ordinary shutdown path the goroutine is already unwinding by the
// time this runs: it shares the same ctx that RunDaemon derives its shutdown
// trigger from, so a SIGTERM that starts a Shutdown also cancels the
// recovery attempt directly. This wait exists as the bound for the remaining
// cases (a caller invoking Shutdown without canceling that outer context).
func (d *Daemon) waitForStartupRecovery(ctx context.Context) error {
	if d.startupRecoveryDone == nil {
		return nil
	}
	select {
	case <-d.startupRecoveryDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Daemon) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), guardianMutationTimeout)
	defer cancel()
	return d.Shutdown(ctx)
}

func (d *Daemon) Shutdown(ctx context.Context) error {
	d.shutdownMu.Lock()
	defer d.shutdownMu.Unlock()
	if d.shutdownComplete {
		return d.shutdownErr
	}
	if !d.shutdownStarted {
		d.shutdownStarted = true
		if d.recoveries != nil {
			d.recoveries.beginRecoveryShutdown()
		}
		if d.mutations != nil {
			d.mutations.beginShutdown()
		}
		if d.observer != nil {
			d.observer.beginShutdown()
		}
		d.stopReconcileLoop()
	}
	if d.observer != nil {
		if err := d.observer.wait(ctx); err != nil {
			return &DaemonShutdownError{Stage: "network_observer_drain", Err: err}
		}
	}

	shutdownErr := d.server.Shutdown(ctx)
	var mutationErr error
	if d.mutations != nil {
		mutationErr = d.mutations.waitForMutations(ctx)
	}
	var recoveryErr error
	if d.recoveries != nil {
		recoveryErr = d.recoveries.waitForRecoveries(ctx)
	}
	startupRecoveryErr := d.waitForStartupRecovery(ctx)
	if shutdownErr != nil || mutationErr != nil || recoveryErr != nil || startupRecoveryErr != nil {
		_ = d.server.Close()
	}
	listenerErr := d.listener.Close()
	if errors.Is(listenerErr, net.ErrClosed) {
		listenerErr = nil
	}
	var removeErr error
	if current, statErr := os.Lstat(d.path); statErr == nil && os.SameFile(current, d.socketInfo) {
		removeErr = os.Remove(d.path)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		removeErr = statErr
	}
	d.shutdownErr = errors.Join(shutdownErr, mutationErr, recoveryErr, startupRecoveryErr, listenerErr, removeErr)
	d.shutdownComplete = true
	return d.shutdownErr
}

func prepareSocketDirectory(path string, ownerUID uint32) error {
	if err := secdir.Ensure(path, int(ownerUID), 0o755); err != nil {
		return fmt.Errorf("create Guardian socket directory: %w", err)
	}
	return nil
}

func removeStaleSocket(path string, ownerUID uint32) error {
	return removeStaleSocketWithDial(path, ownerUID, func(ctx context.Context, path string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	})
}

func removeStaleSocketWithDial(path string, ownerUID uint32, dial func(context.Context, string) (net.Conn, error)) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace non-socket %s", path)
	}
	uid, gotUID := fileOwnerUID(info)
	if !gotUID || uid != ownerUID {
		return fmt.Errorf("refusing to replace socket owned by UID %d", uid)
	}
	dialCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	connection, dialErr := dial(dialCtx, path)
	if dialErr == nil {
		_ = connection.Close()
		return fmt.Errorf("Guardian socket %s is active", path)
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return fmt.Errorf("cannot prove Guardian socket %s is stale: %w", path, dialErr)
	}
	current, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("recheck stale Guardian socket %s: %w", path, err)
	}
	if !os.SameFile(info, current) {
		return fmt.Errorf("Guardian socket %s changed during stale check", path)
	}
	return os.Remove(path)
}

func coreExecutable(selfExecutable func() (string, error), evalSymlinks func(string) (string, error)) string {
	path, err := selfExecutable()
	if err != nil || path == "" {
		return install.BinPath
	}
	if resolved, err := evalSymlinks(path); err == nil && resolved != "" {
		return resolved
	}
	return path
}

func systemDNSManager() DNSManager {
	return NewDNSManager("")
}

// fetchCoreRuntime is the CoreRuntime provider wired into LocalAPIOptions so
// GET /v1/status can fold the Core's own statistics into Guardian's
// response, instead of leaving the menu to spawn `bx status` itself for
// them. It talks to the Core's own control socket (supervisor.SockPath),
// the same one health.go and the cooperative-shutdown path already use —
// this is not a second client, just another caller of the existing one.
//
// ctx already carries the short deadline attachCoreRuntime sets (1s); a
// failure or timeout here is reported as CoreRuntime{Reachable: false},
// never as TunnelHealthy: false standing in for "could not ask".
func fetchCoreRuntime(ctx context.Context) (CoreRuntime, error) {
	report, err := supervisor.FetchStatusReportContext(ctx, supervisor.SockPath)
	if err != nil {
		return CoreRuntime{}, err
	}
	runtime := CoreRuntime{
		Reachable:     true,
		TunnelHealthy: report.TunnelHealthy,
		LatencyMS:     report.LatencyMS,
		Server:        report.Server,
		Transport:     report.Transport,
		UDPMode:       report.UDPMode,
		UDPTransport:  report.UDPTransport,
		FailingRules:  failingRulesFrom(report),
	}
	// 直连解析器住在 RuntimeState 而不是 stats.Report,所以要多问一跳。
	//
	// **它失败不许连累整份状态。** 这一行是锦上添花,而隧道健康、延迟这些是
	// 菜单的立身之本;问不出来就留空(= 没问出来),绝不编一个值。
	if state, err := supervisor.FetchRuntimeState(supervisor.SockPath); err == nil {
		runtime.DNSUpstream = ResolverLabel(state.DNSUpstream, chinaResolverPredicate())
	}
	return runtime, nil
}

func RunDaemon(ctx context.Context, options DaemonOptions) error {
	if err := requireDaemonPlatform(); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("Guardian daemon requires root")
	}
	localAPIOwnerUID, err := loadGuardianLocalAPIOwnerUID(options.ConfigPath)
	if err != nil {
		return err
	}
	options.LocalAPIOwnerUID = localAPIOwnerUID
	runner := NewExecCoreRunner(coreExecutable(os.Executable, filepath.EvalSymlinks), options.ConfigPath, options.DNSListen)
	manager, err := NewManager(ManagerOptions{
		Store:           OpenDefaultStore(),
		Runner:          runner,
		Health:          HealthChecker{},
		Barrier:         NewBarrier(nil),
		DNS:             systemDNSManager(),
		Legacy:          systemLegacyCoreLifecycle{},
		BarrierContext:  BarrierContext{BlockIPv6: true},
		GatewayProvider: GatewayProviderFunc(DiscoverDefaultGateway),
		CoreVersion:     version.Version,
		Throughput:      throughputRecorderFor(options.ConfigPath),
	})
	if err != nil {
		return err
	}
	daemon, err := startRecoveredDaemon(ctx, options, manager, StartDaemon)
	if err != nil {
		return err
	}
	defer daemon.Close()
	<-ctx.Done()
	return daemon.Close()
}

// startRecoveredDaemon starts the LocalAPI socket first and runs the startup
// Recover in the background — it deliberately does NOT wait for Recover to
// finish before returning. Recover's budget (guardianMutationTimeout, 60s;
// a recovering Up can itself wait up to ~20s for Core health) comfortably
// exceeds how long a client should have to wait to prove the socket exists
// (guardianReadyTimeout, 10s in cli/guardian.go) — waiting for Recover here
// used to make a legitimately-recovering Guardian look like it failed to
// start at all.
//
// Recover and every mutation (Up/Down/Migrate/Update) already serialize
// through Manager's single-slot mutation channel (acquireMutation/
// releaseMutation), so a mutation request that lands on the now-earlier-
// available socket while Recover is still running simply queues behind it —
// it cannot interleave with or corrupt Recover's state changes. But which of
// the two wins that channel is a race, and a mutation winning it ahead of
// Recover would otherwise run before recoverUpdateLocked has even inspected
// the update journal. controller.BeginStartupRecovery closes that window
// synchronously, before the socket exists, by raising recoveryBlocked (and
// the path-recovery fence) up front — so no matter which side wins the
// channel, a premature mutation still observes recoveryBlocked and fails
// closed. Status reads go through a separate RWMutex and were never gated on
// Recover, so they stay observable throughout. Recover still runs exactly
// once on this path; retryDaemonRecovery below is the same single follow-up
// the synchronous version used on failure, just triggered from the
// background goroutine instead of inline.
func startRecoveredDaemon(ctx context.Context, options DaemonOptions, controller recoveringController, start daemonStarter) (*Daemon, error) {
	localAPIOptions := localAPIOptionsFor(options)
	options.Handler = NewLocalAPI(controller, localAPIOptions)
	options.OwnerUID = 0
	if options.networkObserver == nil {
		options.networkObserver = newPlatformNetworkObserver(controller)
	}
	options.networkObserverDesired = func() DesiredState {
		return controller.Status().Desired
	}
	controller.BeginStartupRecovery()
	daemon, err := start(ctx, options)
	if err != nil {
		// The socket never opened, so nothing could have raced the fence —
		// release it here rather than leaking a permanently-queued
		// path-recovery state onto a Manager that a caller might reuse.
		controller.AbortStartupRecovery()
		return nil, err
	}
	daemon.trackStartupRecovery(func() { runStartupRecovery(ctx, controller) })
	// 阶段③a 的只观察调谐环。**它一个动作都不执行**,只把「本来会做什么」记进
	// 日志,给阶段③b 的逐项授权攒真机证据(尤其是 looksLikeCore 的误报率 ——
	// 至今从未测量)。跟着 daemon 的 ctx 停。
	if loop, ok := controller.(reconcileLoopRunner); ok {
		daemon.trackReconcileLoop(ctx, func(loopCtx context.Context) {
			loop.runReconcileLoop(loopCtx, liveReconcileObservation)
		})
	}
	return daemon, nil
}

// runStartupRecovery performs the one initial post-start Recover in the
// background; on failure it hands off to the existing retry machinery
// (guardianRecoveryRetryInitial..Max backoff) so Recover still only ever
// runs sequentially, never concurrently with itself. It calls
// RunStartupRecovery rather than Recover because controller.BeginStartupRecovery
// (in startRecoveredDaemon) already raised the path-recovery fence
// synchronously before the socket opened; RunStartupRecovery releases that
// same fence instead of raising a second one, keeping the counter balanced.
// Any subsequent retry goes through the ordinary Recover, which raises and
// releases its own fence per attempt.
func runStartupRecovery(ctx context.Context, controller recoveringController) {
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, guardianMutationTimeout)
	err := controller.RunStartupRecovery(recoveryCtx)
	cancelRecovery()
	if err != nil {
		retryDaemonRecovery(ctx, controller)
	}
}

type networkObserverDesiredHandler struct {
	next     http.Handler
	observer *daemonNetworkObserverLifecycle
	desired  func() DesiredState
}

func (h *networkObserverDesiredHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.next.ServeHTTP(w, r)
	ctx, cancel := context.WithTimeout(context.Background(), guardianMutationTimeout)
	defer cancel()
	_ = h.observer.syncDesired(ctx, observerDesiredState(h.desired))
}

func observerDesiredState(desired func() DesiredState) DesiredState {
	if desired == nil {
		return DesiredOff
	}
	return desired()
}

type daemonNetworkObserverLifecycle struct {
	mu        sync.Mutex
	root      context.Context
	observer  daemonNetworkObserver
	accepting bool
	target    DesiredState
	cancel    context.CancelFunc
	done      chan struct{}
}

func newDaemonNetworkObserverLifecycle(ctx context.Context, observer daemonNetworkObserver) *daemonNetworkObserverLifecycle {
	return &daemonNetworkObserverLifecycle{
		root:      ctx,
		observer:  observer,
		accepting: true,
		target:    DesiredOff,
	}
}

func (l *daemonNetworkObserverLifecycle) syncDesired(ctx context.Context, desired DesiredState) error {
	l.mu.Lock()
	if !l.accepting {
		l.target = DesiredOff
		cancel := l.cancel
		l.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return nil
	}
	if desired != DesiredOn {
		desired = DesiredOff
	}
	l.target = desired
	if l.target == DesiredOn {
		if l.done == nil {
			l.startLocked()
		}
		l.mu.Unlock()
		return nil
	}
	cancel, done := l.cancel, l.done
	l.mu.Unlock()

	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *daemonNetworkObserverLifecycle) startLocked() {
	runCtx, cancel := context.WithCancel(l.root)
	done := make(chan struct{})
	l.cancel = cancel
	l.done = done
	go l.run(runCtx, done)
}

func (l *daemonNetworkObserverLifecycle) run(ctx context.Context, done chan struct{}) {
	defer func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.done == done {
			close(done)
			l.cancel = nil
			l.done = nil
			if l.accepting && l.target == DesiredOn {
				l.startLocked()
			}
			return
		}
		close(done)
	}()
	l.observer.Run(ctx)
}

func (l *daemonNetworkObserverLifecycle) beginShutdown() {
	l.mu.Lock()
	l.accepting = false
	l.target = DesiredOff
	cancel := l.cancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (l *daemonNetworkObserverLifecycle) wait(ctx context.Context) error {
	l.mu.Lock()
	done := l.done
	l.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func loadGuardianLocalAPIOwnerUID(configPath string) (uint32, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0, fmt.Errorf("read Guardian configuration: %w", err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		return 0, fmt.Errorf("parse Guardian configuration: %w", err)
	}
	if uint64(cfg.OwnerUID) > uint64(^uint32(0)) {
		return 0, errors.New("Guardian owner UID is out of range")
	}
	return uint32(cfg.OwnerUID), nil
}

// retryDaemonRecovery 以 100ms→5s 的退避一直重试启动恢复,直到成功或 ctx 结束。
//
// **一种输入会让它永远成功不了,值得写下来:一张读不出来的维护挂起。**
// LoadMaintenanceHold 对坏 schema / 坏 reason / 时钟跳变造出的远期过期一律报错
// (刻意的:「问不出来」不许塌缩成「没有挂起」),recoverLocked 于是 fail-closed
// 返回错误,而盘上那个文件不会自己变好 —— 于是这里稳定在 5 秒一轮,每轮由
// needsAttention 打一行 `guardian_needs_attention code=intent_unreadable`。
//
// **这是对的,而且是刻意吵闹的**:出路(删掉那个文件、或跑一次显式的 up/down)
// 只有用户能做,而安静地放弃等于让机器停在「用户要保护、却没有保护」上不出声。
// 唯一需要防的是被误读成崩溃循环 —— 两者在日志里长得不一样:
//   - 崩溃循环:launchd 反复拉起 Guardian,PID 变、启动横幅重复出现。
//   - 这一条:同一个进程,PID 不变,没有启动横幅,只有那一行码在复读;
//     而那个码在 CLI 侧有自己的出路(guardianCodeHints 的 intent_unreadable)。
//
// 不给它加限次:重试耗尽之后这台机器就再没有任何东西会去看那个文件,而挂起本身
// 又压不住(它读不出来 ⇒ 谁都不动手),那才是真正的静默死局。
func retryDaemonRecovery(ctx context.Context, controller recoveringController) {
	delay := guardianRecoveryRetryInitial
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		recoveryCtx, cancel := context.WithTimeout(ctx, guardianMutationTimeout)
		err := controller.Recover(recoveryCtx)
		cancel()
		if err == nil {
			return
		}
		if delay < guardianRecoveryRetryMax/2 {
			delay *= 2
		} else {
			delay = guardianRecoveryRetryMax
		}
	}
}

type systemLegacyCoreLifecycle struct {
	present func(context.Context) (bool, error)
	stop    func(context.Context) error
	remove  func() error
}

func (l systemLegacyCoreLifecycle) Present(ctx context.Context) (bool, error) {
	present := l.present
	if present != nil {
		return present(ctx)
	}
	loaded, err := install.LegacyCoreLoaded()
	if err != nil {
		return false, err
	}
	return install.LegacyCoreInstalled() || loaded, nil
}

func (l systemLegacyCoreLifecycle) Stop(ctx context.Context) error {
	stop := l.stop
	if stop == nil {
		stop = install.BootoutLegacyCoreUnit
	}
	return stop(ctx)
}

func (l systemLegacyCoreLifecycle) Remove() error {
	remove := l.remove
	if remove == nil {
		remove = install.RemoveLegacyCoreUnit
	}
	return remove()
}

// localAPIOptionsFor 把 daemon 的配置翻译成 LocalAPI 的配置。
//
// **抽出来是为了让接线本身可测。** 这个仓库全部的事故都在组装根上:判据写对了
// 而某个字段没接上,于是功能整个不工作而所有单测照样绿。组装根进不去测试,
// 抽成纯函数就进得去了(与 internal/observe、reconcile 的 decide 同一手法)。
func localAPIOptionsFor(options DaemonOptions) LocalAPIOptions {
	return LocalAPIOptions{
		OwnerUID:        options.LocalAPIOwnerUID,
		GuardianVersion: version.Version,
		RuntimeVersion: func() string {
			info, _, err := runtimedir.Current(runtimedir.Root)
			if err != nil {
				return ""
			}
			return info.Version
		},
		CoreRuntime: fetchCoreRuntime,
		UpdateCheck: options.UpdateCheck,
		// /v1/rules 改的就是这个文件 —— 与 Core 启动用的、与 owner_uid 读出来的
		// 是**同一个路径**,不另猜一份(两处漂开会让菜单改了 A 而 Core 读 B)。
		ConfigPath: options.ConfigPath,
	}
}

// failingRulesFrom 把 Core 的规则归因翻译成菜单能直接显示的形状。
//
// **只翻译用户规则**(stats.FailingRules 已经保证了这一点):内建列表没有
// 「哪一行」可点名,用户也改不了,把它摆进规则编辑界面只会让人对着一个
// 自己动不了的东西干瞪眼。
func failingRulesFrom(report stats.Report) []FailingRule {
	var out []FailingRule
	for _, r := range report.FailingRules() {
		kind := ""
		switch r.Source {
		case "user_direct", "user_direct_ip":
			kind = "direct"
		case "user_proxy", "user_proxy_ip":
			kind = "proxy"
		default:
			// 认不出的来源不报 —— 界面按 kind 去和列表里那一行对齐,
			// 对不上的行显示出来只会让用户找一条不存在的规则。
			continue
		}
		out = append(out, FailingRule{Kind: kind, Rule: r.Rule, Attempts: r.Attempts, Failures: r.Failures})
	}
	return out
}

// throughputRecorderFor 造出「记一次吞吐观测」那个闭包。
//
// **抽成纯粹的接线函数是刻意的** —— 这个仓库全部的事故都在组装根上,而组装根
// 单测进不去。它拿两件事:当前那台叫什么(配置里的 current)、Core 此刻观测到的
// 峰值(控制面),两者都拿到了才记。
//
// 没有配置路径就返回 nil:那时循环里那一步是空操作,而不是每 30 秒往日志里
// 打一行「记不了」。
func throughputRecorderFor(configPath string) func() {
	if strings.TrimSpace(configPath) == "" {
		return nil
	}
	return func() {
		_, current, err := setup.ListServers(configPath)
		if err != nil || strings.TrimSpace(current) == "" {
			// 单服务器配置(还没有 servers 清单)就没有名字可挂 —— 安静跳过,
			// 那是正常状态,不是故障。
			return
		}
		report, err := supervisor.FetchStatusReport(supervisor.SockPath)
		if err != nil || report.PeakBPS <= 0 || report.PeakAt.IsZero() {
			// Core 没在跑,或者这段时间没人用它传东西。**都不是可记的观测。**
			return
		}
		if err := recordThroughput(DefaultThroughputHistoryPath, current, report.PeakBPS, report.PeakAt); err != nil {
			log.Printf("guardian_throughput_record_failed server=%q err=%v", current, err)
		}
	}
}
