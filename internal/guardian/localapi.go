package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/getbx/bx/internal/supervisor"
)

const guardianMutationTimeout = time.Minute

type Controller interface {
	Status() Status
	Up(context.Context) error
	Down(context.Context) error
}

type MigrationController interface {
	Migrate(context.Context, MigrationRequest) error
}

type UpdateController interface {
	Update(context.Context, UpdateRequest) (UpdateResult, error)
}

type PathRecoveryController interface {
	RequestPathRecovery(RecoveryRequest) (RecoverySnapshot, error)
	CurrentPathRecovery() RecoverySnapshot
}

type pathRecoveryStatusController interface {
	currentPathRecoveryStatus() (RecoverySnapshot, string)
}

type LocalAPIOptions struct {
	OwnerUID uint32
	// WakeReconcile, if set, is called after /v1/up and /v1/down settle so the
	// reconcile loop drops out of its backoff and re-observes.
	//
	// **状态刚变过的那一刻,恰恰是那份观测最陈旧的时候。** 真机 2026-09-03:
	// 一台安静很久、已退到 10 分钟一拍的机器在 `bx up` 之后,`bx status` 把一份
	// **早于 Core 存在**的观测原样摆了出来(「扫到 0 个 Core 进程」),而同一屏上
	// Core 正在应答 —— 读的人据此写了一个针对不存在的 bug 的修复。
	//
	// 做成注入的函数而不是 Controller 上的方法:接口一扩,全部替身都要跟着改,
	// 而这件事只有一个真实调用方;更要紧的是**它必须与 watch.poke 走同一条路**
	// —— 两者是同一时刻要做的同一类事,分开接线迟早只接上一个。
	WakeReconcile   func()
	GuardianVersion string
	RuntimeVersion  func() string
	// CoreRuntime, if set, is called on every GET /v1/status to fetch the
	// Core's runtime statistics (tunnel health, latency, server, transport,
	// UDP mode) so the menu can read them from Guardian instead of spawning
	// its own CLI subprocess. Nil means "not wired" — existing callers keep
	// working with no Core field at all, not a zero-valued one.
	CoreRuntime func(context.Context) (CoreRuntime, error)
	// UpdateCheck, if set, backs GET /v1/update-check. It does network I/O
	// (GitHub release lookup + signed manifest verification), which is why it
	// is injected rather than implemented here: the code that knows how to do
	// it lives in internal/cli, and Guardian must not grow a second copy.
	// Nil means the endpoint answers 501 — "not wired" is not "no update".
	UpdateCheck func(context.Context) (UpdateAvailability, error)
	// ConfigPath backs /v1/rules. Empty means "not wired" — the endpoint then
	// answers 501 rather than an empty rule list, because "nobody told me
	// where the config is" and "you have no rules" are different answers and
	// the menu must not render the second when it got the first.
	ConfigPath string
	// ReloadRules, if set, is called after a successful POST /v1/rules write so
	// the change takes effect without a reconnect (production: Core's
	// /v0/reload, the same path `bx direct add` uses). Nil means "not wired" —
	// the endpoint then answers requires_restart:true, which is the honest
	// answer when nobody told Core to re-read the file.
	ReloadRules func() error
	// AppsSockPath backs /v1/apps — the unix socket Guardian dials to reach
	// Core's app-traffic-attribution report. Empty falls back to
	// supervisor.SockPath (the production constant), so daemon.go/
	// localAPIOptionsFor never has to change and production behaviour is
	// identical either way.
	//
	// **它存在的唯一理由是让 NewLocalAPI 的接线本身可测。** CoreRuntime 字段
	// 就是同一个先例:fetchCoreRuntime 内部同样硬编码 supervisor.SockPath、
	// 生产里那个值也从来不变,但仍然做成了可注入字段——「值是否随部署变化」
	// 从来不是这个仓库决定要不要经 LocalAPIOptions 转手的判据。没有这个字段,
	// 「成功转发」那条路径就只能绕开 NewLocalAPI、直调 appsHandler 来测,
	// 验证的是 appsHandler 本身,不是「NewLocalAPI 正确接上了它」这件事——
	// 而组装根上的接线错误正是这个仓库反复栽的形状。
	AppsSockPath string
}

// coreRuntimeFetchTimeout bounds how long observableStatus waits on
// CoreRuntime before giving up and reporting Reachable: false. /v1/status is
// the menu's only data source and gets polled at second-level frequency, so
// an unreachable or slow Core must never stall the whole response.
const coreRuntimeFetchTimeout = time.Second

// updateCheckTimeout bounds the injected UpdateCheck provider. It talks to
// GitHub, so it can be slow or hang outright; the endpoint must return either
// an answer or a failure, never a stalled connection the menu waits on.
const updateCheckTimeout = 20 * time.Second

// updateCheckCacheTTL is how long a successful answer is reused. The menu asks
// once a day, but nothing stops another peer from asking in a loop, and each
// miss is outbound network I/O performed by a root daemon. Failures are
// deliberately NOT cached: a transient network outage must not pin the answer
// to "could not ask" for an hour.
const updateCheckCacheTTL = time.Hour

type peerCredentialsKey struct{}

type peerCredentials struct {
	uid uint32
	got bool
}

type localAPI struct {
	handler        http.Handler
	mutations      *acceptedMutations
	recoveries     recoveryLifecycle
	pathRecoveries pathRecoveryLifecycle
	// watch 是 Status 与代际号的唯一发布点。parked 的 watch 请求由
	// beginShutdown 唤醒 —— 见那个方法。
	watch *statusPublisher
}

type recoveryLifecycle interface {
	beginRecoveryShutdown()
	waitForRecoveries(context.Context) error
}

type acceptedMutations struct {
	mu        sync.Mutex
	accepting bool
	active    int
	drained   chan struct{}
	closed    bool
}

func withPeerCredentials(ctx context.Context, uid uint32, got bool) context.Context {
	return context.WithValue(ctx, peerCredentialsKey{}, peerCredentials{uid: uid, got: got})
}

func NewLocalAPI(controller Controller, provided ...LocalAPIOptions) http.Handler {
	var options LocalAPIOptions
	if len(provided) != 0 {
		options = provided[0]
	}
	mutations := &acceptedMutations{accepting: true, drained: make(chan struct{})}
	// **Status 的唯一发布点。** 连不带 wait 的那条路也走它 —— 否则应答里的
	// status_generation 与 watch 那条路发布的会是两个互不相干的数,而客户端
	// 正是拿前者发回给后者的。
	watch := newStatusPublisher(func() Status {
		return observableStatus(controller, pathRecoveryControllerFor(controller), options)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		// 读不懂的 wait **当成「没带」**:当成 0 会让每次请求都立刻返回
		// (= 满速轮询),回 4xx/5xx 会让菜单以为 Guardian 坏了。立刻返回一次
		// 当前状态最无害 —— 客户端拿到真代际号之后自然会用对。
		clientGen, parked := parseWatchGeneration(r)
		if !parked {
			status, _ := watch.current()
			writeGuardianJSON(w, http.StatusOK, status)
			return
		}
		status, _ := watch.wait(r.Context(), clientGen, parseWatchTimeout(r))
		writeGuardianJSON(w, http.StatusOK, status)
	})
	mux.HandleFunc("/v1/up", mutationHandler(controller, controller.Up, mutations, options, "/v1/up", watch))
	// markMaintenanceStop 只包 /v1/down:它把「这次停保护是维护(升级)自己的
	// 一步」翻译进请求上下文,Manager.Down 据此既不改写 desired、也不销掉那张
	// 前一秒才武装的维护挂起(见 upgradeintent.go)。
	mux.HandleFunc("/v1/down", markMaintenanceStop(mutationHandler(controller, controller.Down, mutations, options, "/v1/down", watch)))
	migrationController, _ := controller.(MigrationController)
	mux.HandleFunc("/v1/migrate", migrationHandler(controller, migrationController, mutations, options, watch))
	updateController, _ := controller.(UpdateController)
	mux.HandleFunc("/v1/update", updateHandler(controller, updateController, mutations))
	pathRecoveryController, _ := controller.(PathRecoveryController)
	mux.HandleFunc("/v1/update-check", updateCheckHandler(newUpdateCheckCache(options.UpdateCheck), options.OwnerUID))
	mux.HandleFunc("/v1/recoveries", recoveryRequestHandler(controller, pathRecoveryController, options.OwnerUID))
	mux.HandleFunc("/v1/recoveries/current", recoveryCurrentHandler(pathRecoveryController, options.OwnerUID))
	mux.HandleFunc("/v1/rules", rulesHandler(options.ConfigPath, options.OwnerUID, options.ReloadRules))
	mux.HandleFunc("/v1/servers", serversHandler(options.ConfigPath, options.OwnerUID, liveServerSwitch, liveServerProbe, liveThroughput))
	// options.AppsSockPath 空串时回落到 supervisor.SockPath(Core 控制面固定
	// 的 unix socket 路径,与 fetchCoreRuntime/throughputRecorderFor 用的是
	// 同一个常量)。字段本身只为一件事存在:让「NewLocalAPI 真的接上了
	// appsHandler」这条组装根可测——生产里 daemon.go 从不设置它,行为与直接
	// 硬编码常量完全相同。
	appsSockPath := options.AppsSockPath
	if appsSockPath == "" {
		appsSockPath = supervisor.SockPath
	}
	mux.HandleFunc("/v1/apps", appsHandler(appsSockPath, options.OwnerUID))
	recoveries, _ := controller.(recoveryLifecycle)
	pathRecoveries, _ := controller.(pathRecoveryLifecycle)
	return &localAPI{handler: mux, mutations: mutations, recoveries: recoveries, pathRecoveries: pathRecoveries, watch: watch}
}

func pathRecoveryControllerFor(controller Controller) PathRecoveryController {
	pathRecoveryController, _ := controller.(PathRecoveryController)
	return pathRecoveryController
}

// applyVersionFields 填上「跑着的 Guardian 是哪一版」与「盘上装的是哪一版」。
//
// **每一个回 Status 的响应都要填**,不只 GET /v1/status:`bx up` 拿的是
// POST /v1/up 的响应,而 macOSUpLifecycle 在 Up 已经报 Protected 时根本不会再发
// 一次 GET(waitGuardianProtected 立刻返回)。只在 GET 上填,等于让「Guardian
// 仍在跑旧版」这条提示在它唯一该出现的场合恒为空 —— 那正是 2026-08-08 复审 C2:
// 一台 Guardian=dev / runtime=phase2 的机器上 `bx up` 一个字都不说。
func applyVersionFields(status *Status, options LocalAPIOptions) {
	status.GuardianVersion = options.GuardianVersion
	if options.RuntimeVersion != nil {
		status.RuntimeVersion = options.RuntimeVersion()
	}
	// 能力与版本同源:两者说的都是「正在应答你的这一版是什么」,所以在同一处
	// 填、经同一批响应发布。它是编译期常量,不问任何外部进程 —— 这正是它取代
	// `bx logs --help` 文本探测的理由。
	status.Capabilities = GuardianCapabilities()
}

// publishedIntentReporter 由能一次读出**两半意图**的 controller 实现:用户要
// 什么,以及此刻允不允许有保护。
type publishedIntentReporter interface {
	PublishedIntent() (DesiredState, *MaintenanceHoldStatus, bool)
}

// attachPublishedIntent 在**每一个**回 Status 的响应上附上这对值:菜单的开关读的是
// POST /v1/up、/v1/down 的响应,只在 GET 上附等于在最需要它的那一刻恒为空
// (与 applyVersionFields 那条注释同一个教训)。
//
// **desired 与挂起必须一起换。** 只换挂起、让 desired 留在内存里那一份,就是设计
// 取舍⑥点名禁止的混源发布:内存那份会被 needsAttention 写成调用方传进来的字面量
// (好几处是 DesiredOn 而磁盘写着 off),于是同一份应答里「挂起说磁盘」「desired
// 说内存」,下游(bx status 的挂起行、observe.Intent、Diverge)据此得出的结论与
// 调谐器相反。
//
// controller 不实现这个接口时两者都保持原样 —— 与「声明了能力、此刻没有挂起」
// 长得一样,而那正是对的:能力由 applyVersionFields 无条件声明,说的是
// 「这一版认识挂起」;某个替身答不出来不该把这句话收回去。读盘失败(ok=false)
// 同理:保留信念,不拿磁盘的沉默去覆盖它。
func attachPublishedIntent(status *Status, controller Controller) {
	reporter, ok := controller.(publishedIntentReporter)
	if !ok {
		return
	}
	desired, hold, read := reporter.PublishedIntent()
	if !read {
		return
	}
	status.Desired = desired
	status.MaintenanceHold = hold
}

// statusWithVersions 是 mutation/migration handler 回给客户端的那份状态。
//
// **StatusGeneration 由 watch 补,不是这里现算的。** publisher 才是「代际号
// 由内容派生」那条纪律唯一的记账处(statuswatch.go),这里只是原样把它此刻
// 已经发布的那个数字抄过来——不重新计算、不重新决定要不要 bump。字段没有
// `omitempty`,「存在但是 0」按它自己的文档注释意味着「这一版有 watch 这个
// 概念、只是还没发布过」;不补这一步,up/down/migrate 的响应会一直是这句假话,
// 即便 mutationHandler 在组装这份响应之前刚刚调用过 watch.poke()、确确实实
// 发布过一次。watch 为 nil(测试替身、或调用方压根没有 watch 概念)时保持原样
// 的零值,不伪造一个从没发布过的代际号。
func statusWithVersions(controller Controller, options LocalAPIOptions, watch *statusPublisher) Status {
	status := statusOf(controller)
	applyVersionFields(&status, options)
	attachPublishedIntent(&status, controller)
	if watch != nil {
		_, generation := watch.current()
		status.StatusGeneration = generation
	}
	return status
}

func observableStatus(controller Controller, recoveries PathRecoveryController, options LocalAPIOptions) Status {
	status := controller.Status()
	status.Recovery = RecoverySnapshot{State: "idle", Stage: "idle"}
	if recoveries == nil {
		applyVersionFields(&status, options)
		attachCoreRuntime(&status, options)
		attachPublishedIntent(&status, controller)
		return status
	}
	if current, ok := recoveries.(pathRecoveryStatusController); ok {
		status.Recovery, status.NetworkGeneration = current.currentPathRecoveryStatus()
	} else {
		status.Recovery = recoveries.CurrentPathRecovery()
		status.NetworkGeneration = status.Recovery.Generation
	}
	status.Recovery = redactRecoverySnapshot(status.Recovery)
	// Core 的运行时事实在判「失败的恢复还算不算数」之前就要拿到手:它正是
	// 判据(见 recoverySupersededByCore),不只是附在末尾的展示数据。
	core := fetchCoreRuntimeForStatus(options)
	if recoverySupersededByCore(status.Protection, status.Recovery, core) {
		if retirer, ok := recoveries.(pathRecoveryRetirer); ok {
			retirer.retireSupersededPathRecovery()
		}
		status.Recovery = RecoverySnapshot{State: "idle", Stage: "idle"}
	}
	switch status.Recovery.State {
	case "accepted", "running":
		if status.Desired == DesiredOn && status.Protection != ProtectionNeedsAttention {
			status.Protection = ProtectionRecovering
		}
	case "failed":
		if status.Protection != ProtectionNeedsAttention {
			status.Protection = ProtectionBlocked
		}
	}
	applyVersionFields(&status, options)
	status.Core = core
	attachPublishedIntent(&status, controller)
	return status
}

// pathRecoveryRetirer 让 observableStatus 把一份被事实否定的失败快照从
// Manager 的记忆里清掉 —— 否则 /v1/recoveries 与 /v1/status 对同一个问题
// 给两个答案。可选接口:测试替身不实现时只做投影。
type pathRecoveryRetirer interface {
	retireSupersededPathRecovery() bool
}

// recoverySupersededByCore 判一份**已结束的失败**恢复是不是历史。
//
// 真机 2026-09-07:一小时的睡眠/暗唤醒抖动里 recovery-10 在 verify 连败 20 次
// 后放弃;机器真正醒来后一切自愈,而 failed 快照留着 —— `bx status` 与菜单
// Blocked 一个多小时,图标裂开,用户能上网。调谐环按内核观测退场要等一个
// 退避周期(最长 10 分钟),而对用户那 10 分钟就是「bx 坏了」。Guardian 每次
// 答状态时手里就有 Core 的运行时事实(菜单每 2 秒问一次),而那几项正是
// Core 那边 verify 要看的:隧道健康、路由在、DNS 在听、UDP 就绪(要的话)。
// 它们此刻全满足,那次 verify 放在现在就会通过 —— 答状态那一刻就该按事实判。
//
// 只对 Manager 自己说 Protected 的情形成立:屏障真在手里时 Manager 自己就说
// Blocked,需要修理时说 NeedsAttention,两者都不是这条要碰的。Core 问不出来
// (nil / Reachable=false)一律不算 —— 一次没拿到答案的探测不许被读成「好了」。
func recoverySupersededByCore(protection string, recovery RecoverySnapshot, core *CoreRuntime) bool {
	if recovery.State != "failed" || protection != ProtectionProtected || core == nil || !core.Reachable {
		return false
	}
	if !core.TunnelHealthy || !core.RoutesInstalled || !core.DNSListening {
		return false
	}
	if core.UDPRequired && !core.UDPReady {
		return false
	}
	return true
}

// fetchCoreRuntimeForStatus 问一次 Core 的运行时状态;没接 provider 时返回
// nil(status.Core 保持缺席,与 attachCoreRuntime 的约定一致)。
func fetchCoreRuntimeForStatus(options LocalAPIOptions) *CoreRuntime {
	if options.CoreRuntime == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), coreRuntimeFetchTimeout)
	defer cancel()
	runtime, err := options.CoreRuntime(ctx)
	if err != nil {
		runtime = CoreRuntime{Reachable: false}
	}
	return &runtime
}

// attachCoreRuntime fills status.Core when the caller wired a CoreRuntime
// provider (options.CoreRuntime != nil). With no provider, status.Core stays
// nil — existing callers of NewLocalAPI(controller) with no options keep
// emitting no "core" field at all, not a fabricated one.
//
// A fetch error or timeout is reported as CoreRuntime{Reachable: false} —
// never as a Core-unreachable status with TunnelHealthy: true left over from
// a zero value, and never by failing the whole /v1/status response. The
// menu's only data source cannot be allowed to go dark because the Core
// socket happened to be unreachable at the moment of the poll.
func attachCoreRuntime(status *Status, options LocalAPIOptions) {
	status.Core = fetchCoreRuntimeForStatus(options)
}

// updateCheckCache serializes and caches the injected update-check provider.
//
// Serializing (one mutex held across the provider call) is not an optimisation:
// it is what keeps N concurrent requests from becoming N concurrent outbound
// HTTPS conversations started by a root daemon. The second caller either waits
// and then reads the fresh cache entry, or — if the first call failed — makes
// its own attempt.
type updateCheckCache struct {
	mu      sync.Mutex
	check   func(context.Context) (UpdateAvailability, error)
	value   UpdateAvailability
	expires time.Time
	now     func() time.Time
}

func newUpdateCheckCache(check func(context.Context) (UpdateAvailability, error)) *updateCheckCache {
	return &updateCheckCache{check: check, now: time.Now}
}

// get returns the cached answer when it is still fresh, otherwise asks the
// provider once. A failure is returned as-is and **not** cached: "could not
// ask" is a momentary condition, and freezing it for an hour would turn one
// flaky lookup into a day of silence.
func (c *updateCheckCache) get(ctx context.Context) (UpdateAvailability, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.check == nil {
		return UpdateAvailability{}, errUpdateCheckUnavailable
	}
	if !c.expires.IsZero() && c.now().Before(c.expires) {
		return c.value, nil
	}
	value, err := c.check(ctx)
	if err != nil {
		return UpdateAvailability{}, err
	}
	c.value = value
	c.expires = c.now().Add(updateCheckCacheTTL)
	return value, nil
}

var errUpdateCheckUnavailable = errors.New("update check not wired")

// updateCheckHandler serves GET /v1/update-check.
//
// **授权:与 /v1/recoveries 同款 authorizeOwnerPeer,不是 /v1/status 那样人人可读。**
// payload 本身不敏感(版本号在 /v1/status 里早就有了),真正要挡的是**副作用**:
// 这是本地 socket 上唯一一个「任一对端一句话就能让 root 守护进程发起出网请求」的
// 端点。不设防等于给同机任何进程一个可反复触发的外连触发器(流量指纹、放大、
// 以及把 Guardian 的出口暴露给一个不该能驱动它的调用方)。缓存 + 串行化把频率
// 压住,授权把「谁可以驱动」限死 —— 两者都要,少一个都是靠另一个兜底。
//
// 失败一律 503 且**只说「问不出来」**:绝不退化成 `available:false`(那会被菜单
// 读成「你已经是最新」,一个自信的错答案),也绝不把 provider 的原始错误串外传
// (它含 URL 与网络细节,和其余 handler 的处置一致——完整错误只进 Guardian 日志)。
func updateCheckHandler(cache *updateCheckCache, ownerUID uint32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if !authorizeOwnerPeer(r.Context(), ownerUID) {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "update check requires owner or root peer"})
			return
		}
		if cache == nil || cache.check == nil {
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "update check unavailable"})
			return
		}
		// WithoutCancel:一个客户端中途走开不该打断另一个客户端正在等的那次查询。
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), updateCheckTimeout)
		defer cancel()
		availability, err := cache.get(ctx)
		if err != nil {
			log.Printf("guardian_update_check_failed err=%v", err)
			writeGuardianJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "update check unavailable"})
			return
		}
		writeGuardianJSON(w, http.StatusOK, availability)
	}
}

func recoveryRequestHandler(controller Controller, recovery PathRecoveryController, ownerUID uint32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if !authorizeOwnerPeer(r.Context(), ownerUID) {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "recovery requires owner or root peer"})
			return
		}
		if recovery == nil {
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "recovery unavailable"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request RecoveryRequest
		if err := decoder.Decode(&request); err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid recovery metadata"})
			return
		}
		if err := ensureJSONEOF(decoder); err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid recovery metadata"})
			return
		}
		normalized, err := ValidateRecoveryRequest(request)
		if err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid recovery metadata"})
			return
		}
		before := statusOf(controller)
		snapshot, err := recovery.RequestPathRecovery(normalized)
		if errors.Is(err, errPathRecoveryShuttingDown) {
			writeGuardianJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "guardian is shutting down"})
			return
		}
		if err != nil {
			// 完整错误只进 Guardian 日志(安装时被强制为 0600 root:wheel,
			// 见 install.SecureGuardianLogs——launchd 默认建的是 0644,
			// 本地任何用户可读);响应只带失败码,避免把路径/链接/凭据经
			// socket 外传。
			log.Printf("guardian_recovery_request_failed err=%v", err)
			writeGuardianJSON(w, http.StatusInternalServerError, failureResponseBody(before, statusOf(controller), err))
			return
		}
		writeGuardianJSON(w, http.StatusAccepted, redactRecoverySnapshot(snapshot))
	}
}

// statusOf reads controller.Status(), tolerating a nil Controller
// (recoveryRequestHandler may be wired without one in tests that only
// exercise the PathRecoveryController side).
func statusOf(controller Controller) Status {
	if controller == nil {
		return Status{}
	}
	return controller.Status()
}

// failureResponseBody builds the body for a mutation-failure 500 response.
// It only includes "code" when needsAttention actually ran during this
// specific call — tracked via Status.LastErrorGeneration, a monotonic
// counter, rather than by comparing LastError's value. A value comparison
// cannot tell "this failure just set LastError to X" apart from "LastError
// already happened to be X from an earlier, unrelated failure" in the one
// case that matters most: two consecutive failures with the same code.
// That is exactly the scenario this feature exists for (bx up failing
// repeatedly for the same reason) — a value comparison would wrongly treat
// the second failure's code as stale and suppress it, making the code
// flicker on and off across a client's retries even though the failure
// never changed.
//
// Several real failure paths return an error without ever calling
// needsAttention at all: acquireMutation timing out on a busy mutation
// lock, recoveryBlocked short-circuiting with errRecoveryIncomplete, or
// Down's DNS restore-failure branch which recovers and returns without
// touching LastError. On those paths the generation is unchanged and the
// code is omitted rather than replaying a stale/unrelated value: a wrong
// code is worse than no code, because it points troubleshooting in the
// wrong direction.
//
// Two of those paths are the *main* incident use case — "startup recovery
// already failed, user keeps running bx up" and "another mutation holds the
// lock" — so they are named from the error itself (failureCodeForError)
// instead of being left codeless. That is not a stale value: the sentinel
// describes exactly this call's failure. It takes precedence over the
// LastError channel because it is derived from the error actually being
// reported, whereas LastError is shared long-term state.
func failureResponseBody(before, after Status, err error) map[string]string {
	body := map[string]string{"error": "guardian operation failed"}
	if code := failureCodeForError(err); code != "" {
		body["code"] = code
		return body
	}
	if after.LastErrorGeneration != before.LastErrorGeneration && after.LastError != "" {
		body["code"] = after.LastError
	}
	return body
}

// failureCodeForError names the failures that short-circuit before any
// needsAttention call. Only sentinels are matched — an unrecognised error
// yields "" so the caller falls back to the LastError channel (and, failing
// that, to no code at all). A bare context error is deliberately not
// "guardian_busy": only acquireMutation's queueing timeout wraps
// errMutationBusy, while a ctx error surfacing from deeper mutation work
// means something else entirely.
func failureCodeForError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errRecoveryIncomplete):
		return "recovery_incomplete"
	case errors.Is(err, errMutationBusy):
		return "guardian_busy"
	default:
		return ""
	}
}

func recoveryCurrentHandler(controller PathRecoveryController, ownerUID uint32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if !authorizeOwnerPeer(r.Context(), ownerUID) {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "recovery requires owner or root peer"})
			return
		}
		if controller == nil {
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "recovery unavailable"})
			return
		}
		writeGuardianJSON(w, http.StatusOK, redactRecoverySnapshot(controller.CurrentPathRecovery()))
	}
}

// authorizeOwnerPeer 是「root 或 config 里配置的 owner」这条判据。
//
// ownerUID 为 0(未配置)时退化为 root-only —— 绝不因为「没配」就放宽。
// 守着 /v1/recoveries(路径恢复)与 /v1/up、/v1/down(日常开关)。
// 装卸(/v1/update)与迁移(/v1/migrate)刻意不用它,见
// TestLocalAPIUpdateAndMigrateStayRootOnlyEvenWithOwnerConfigured。
// peerUIDFrom 取出对端 uid,供审计日志使用。第二个返回值区分「内核给了我们
// 凭据」与「没拿到」——没拿到时 uid 是零值 0,而 0 恰好就是 root,不区分就会
// 把「不知道谁」记成「root 干的」。今天调用它的路径都在 authorizeOwnerPeer
// 通过之后(没有凭据根本进不来),但这个函数不该依赖调用点的顺序才正确。
func peerUIDFrom(ctx context.Context) (uint32, bool) {
	credentials, _ := ctx.Value(peerCredentialsKey{}).(peerCredentials)
	return credentials.uid, credentials.got
}

func authorizeOwnerPeer(ctx context.Context, ownerUID uint32) bool {
	credentials, _ := ctx.Value(peerCredentialsKey{}).(peerCredentials)
	if !credentials.got {
		return false
	}
	return credentials.uid == 0 || (ownerUID != 0 && credentials.uid == ownerUID)
}

func updateHandler(controller Controller, updater UpdateController, mutations *acceptedMutations) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		credentials, _ := r.Context().Value(peerCredentialsKey{}).(peerCredentials)
		if !credentials.got || credentials.uid != 0 {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "mutation requires root peer"})
			return
		}
		if updater == nil {
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "update unavailable"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request UpdateRequest
		if err := decoder.Decode(&request); err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid update metadata"})
			return
		}
		if err := ensureJSONEOF(decoder); err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid update metadata"})
			return
		}
		normalized, err := ValidateUpdateRequest(request)
		if err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid update metadata"})
			return
		}
		if !mutations.accept() {
			writeGuardianJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "guardian is shutting down"})
			return
		}
		defer mutations.done()
		mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), guardianMutationTimeout)
		defer cancel()
		before := controller.Status()
		result, err := updater.Update(mutationCtx, normalized)
		if err != nil {
			// 完整错误只进 Guardian 日志(安装时被强制为 0600 root:wheel,
			// 见 install.SecureGuardianLogs——launchd 默认建的是 0644,
			// 本地任何用户可读);响应只带失败码,避免把路径/链接/凭据经
			// socket 外传。
			log.Printf("guardian_mutation_failed err=%v", err)
			writeGuardianJSON(w, http.StatusInternalServerError, failureResponseBody(before, controller.Status(), err))
			return
		}
		writeGuardianJSON(w, http.StatusOK, result)
	}
}

func migrationHandler(controller Controller, migration MigrationController, mutations *acceptedMutations, options LocalAPIOptions, watch *statusPublisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		credentials, _ := r.Context().Value(peerCredentialsKey{}).(peerCredentials)
		if !credentials.got || credentials.uid != 0 {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "mutation requires root peer"})
			return
		}
		if migration == nil {
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "migration unavailable"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request MigrationRequest
		if err := decoder.Decode(&request); err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid migration metadata"})
			return
		}
		if err := ensureJSONEOF(decoder); err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid migration metadata"})
			return
		}
		normalized, err := ValidateMigrationRequest(request)
		if err != nil {
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid migration metadata"})
			return
		}
		if !mutations.accept() {
			writeGuardianJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "guardian is shutting down"})
			return
		}
		defer mutations.done()
		mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), guardianMutationTimeout)
		defer cancel()
		before := controller.Status()
		if err := migration.Migrate(mutationCtx, normalized); err != nil {
			// 完整错误只进 Guardian 日志(安装时被强制为 0600 root:wheel,
			// 见 install.SecureGuardianLogs——launchd 默认建的是 0644,
			// 本地任何用户可读);响应只带失败码,避免把路径/链接/凭据经
			// socket 外传。
			log.Printf("guardian_mutation_failed err=%v", err)
			writeGuardianJSON(w, http.StatusInternalServerError, failureResponseBody(before, controller.Status(), err))
			return
		}
		writeGuardianJSON(w, http.StatusOK, statusWithVersions(controller, options, watch))
	}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return io.ErrUnexpectedEOF
	}
	return err
}

func (a *localAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.handler.ServeHTTP(w, r)
}

func (a *localAPI) beginShutdown() {
	a.mutations.stopAccepting()
	// **parked 的 watch 必须立刻放开。** server.Shutdown 会等在跑的 handler
	// 返回,而这个方法正是在 server.Shutdown 之前被 Daemon.Shutdown 调的
	// (daemon.go:319)。不唤醒它们,升级时 Guardian 关机会慢到 25 秒 ——
	// 而这个项目在「关机慢」上栽过 71 分钟。
	if a.watch != nil {
		a.watch.beginShutdown()
	}
}

func (a *localAPI) waitForMutations(ctx context.Context) error {
	return a.mutations.wait(ctx)
}

func (a *localAPI) beginRecoveryShutdown() {
	if a.recoveries != nil {
		a.recoveries.beginRecoveryShutdown()
	}
	if a.pathRecoveries != nil {
		a.pathRecoveries.beginPathRecoveryShutdown()
	}
}

func (a *localAPI) waitForRecoveries(ctx context.Context) error {
	var recoveryErr error
	if a.recoveries != nil {
		recoveryErr = a.recoveries.waitForRecoveries(ctx)
	}
	var pathRecoveryErr error
	if a.pathRecoveries != nil {
		pathRecoveryErr = a.pathRecoveries.waitForPathRecoveries(ctx)
	}
	return errors.Join(recoveryErr, pathRecoveryErr)
}

// mutationHandler 服务 /v1/up 与 /v1/down。endpoint 只用于审计日志。
//
// **审计是 owner_uid 授权那条已接受风险的唯一缓解措施**:开关不再需要管理员
// 密码之后,以该 uid 身份运行的**任何**进程都能悄无声息地开关 bx。设计文档
// (docs/superpowers/specs/2026-08-07-macos-menubar-redesign-design.md 「风险」)
// 把「Guardian 日志记录每一次 up/down 的发起 uid 与时间,事后可追溯」写成了
// 接受这个风险的条件——写在文档里而代码里没有的缓解措施比从未声称的更糟,
// 因为它会被相信。故这里对**每一次**授权通过的调用各打两行:发起时一行(哪怕
// 这次 mutation 挂住了 60 秒也已经留下「谁发起的」),落定时一行带成败与耗时。
//
// 两行都只有 uid / 端点 / 时间 / 成败这些非敏感字段。原始错误仍然只在
// guardian_mutation_failed 那行里(见下),响应体照旧只带失败码。
func mutationHandler(controller Controller, mutate func(context.Context) error, mutations *acceptedMutations, options LocalAPIOptions, endpoint string, watch *statusPublisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if !authorizeOwnerPeer(r.Context(), options.OwnerUID) {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "mutation requires root or owner peer"})
			return
		}
		// 显式带 at= 时间戳,不指望日志行首的时间前缀:调用方随时可能
		// log.SetFlags(0),而「时间」是这条缓解措施被点名要求的一半。
		uid, _ := peerUIDFrom(r.Context())
		started := time.Now()
		log.Printf("guardian_mutation_requested endpoint=%s uid=%d at=%s",
			endpoint, uid, started.UTC().Format(time.RFC3339))
		if !mutations.accept() {
			log.Printf("guardian_mutation_result endpoint=%s uid=%d outcome=refused reason=shutting_down", endpoint, uid)
			writeGuardianJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "guardian is shutting down"})
			return
		}
		defer mutations.done()
		mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), guardianMutationTimeout)
		defer cancel()
		before := controller.Status()
		if err := mutate(mutationCtx); err != nil {
			// 完整错误只进 Guardian 日志(安装时被强制为 0600 root:wheel,
			// 见 install.SecureGuardianLogs——launchd 默认建的是 0644,
			// 本地任何用户可读);响应只带失败码,避免把路径/链接/凭据经
			// socket 外传。
			log.Printf("guardian_mutation_failed err=%v", err)
			log.Printf("guardian_mutation_result endpoint=%s uid=%d outcome=failed elapsed=%s",
				endpoint, uid, time.Since(started).Round(time.Millisecond))
			writeGuardianJSON(w, http.StatusInternalServerError, failureResponseBody(before, controller.Status(), err))
			return
		}
		log.Printf("guardian_mutation_result endpoint=%s uid=%d outcome=ok elapsed=%s",
			endpoint, uid, time.Since(started).Round(time.Millisecond))
		// **广播点。** 用户正站在旁边等反馈:up/down 成功后立刻重算并唤醒任何
		// parked 的 watch,而不是让它们等下一个兵底拍(最长 3 秒)。
		watch.poke()
		// **同一时刻也把调谐环从退避里叫醒。**
		// 状态刚变过的那一刻,恰恰是它那份观测最陈旧的时候 —— 一台安静很久、
		// 已经退到 10 分钟一拍的机器,在 up 之后会把一份**早于 Core 存在**的
		// 观测原样摆进 bx status(真机 2026-09-03 撞到)。
		if options.WakeReconcile != nil {
			options.WakeReconcile()
		}
		// 版本字段必须一起回:`bx up` 只看这一个响应,不会再补一次 GET /v1/status。
		// 代际号同理:poke() 刚发布过,statusWithVersions 把它原样抄进这份响应。
		writeGuardianJSON(w, http.StatusOK, statusWithVersions(controller, options, watch))
	}
}

func (m *acceptedMutations) accept() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.accepting {
		return false
	}
	m.active++
	return true
}

func (m *acceptedMutations) done() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
	m.closeDrainedLocked()
}

func (m *acceptedMutations) stopAccepting() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accepting = false
	m.closeDrainedLocked()
}

func (m *acceptedMutations) closeDrainedLocked() {
	if !m.accepting && m.active == 0 && !m.closed {
		close(m.drained)
		m.closed = true
	}
}

func (m *acceptedMutations) wait(ctx context.Context) error {
	select {
	case <-m.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// writeGuardianJSON 先整体 marshal、显式带 Content-Length、一次写出。
//
// **不用 json.Encoder 流式写。** net/http 只对 handler 返回前攒在 2KB 缓冲里的体
// 补 Content-Length,更大的体改用 chunked;而菜单那份手写的 HTTP 读取器(刻意最小)
// 只认 Content-Length。真机 2026-09-05:/v1/rules 的体随 review 一节长过 2KB,
// 菜单的「规则」窗口从此报「Guardian returned an invalid response」,而 curl 拿到的
// 是 200 + 合法 JSON。响应的框架不该由体的大小决定。
// marshal 失败是编程错误(值里有不可序列化的东西),如实回 500 而不是半截 JSON。
func writeGuardianJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		log.Printf("guardian_json_marshal_failed err=%v", err)
		http.Error(w, "guardian response could not be encoded", http.StatusInternalServerError)
		return
	}
	body = append(body, '\n')
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// parseWatchGeneration 取出 `wait` 参数。第二个返回值 = 「这是一次长轮询」。
//
// 缺席或读不懂都返回 false(当成普通 GET),理由见调用点。
func parseWatchGeneration(r *http.Request) (uint64, bool) {
	raw := r.URL.Query().Get("wait")
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// parseWatchTimeout 取出可选的 `timeout`(秒),并钳进服务端允许的区间。
func parseWatchTimeout(r *http.Request) time.Duration {
	raw := r.URL.Query().Get("timeout")
	if raw == "" {
		return watchMaxHold
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return watchMaxHold
	}
	return clampWatchTimeout(time.Duration(seconds) * time.Second)
}

// clampWatchTimeout 把客户端要求的挂住时长钳进 [watchMinHold, watchMaxHold]。
//
// **上限不能交给客户端决定**:它同时是 Guardian 关机可能被拖住的上限。
// 下限不能是 0:那会让 watch 退化成满速轮询。
func clampWatchTimeout(d time.Duration) time.Duration {
	if d < watchMinHold {
		return watchMinHold
	}
	if d > watchMaxHold {
		return watchMaxHold
	}
	return d
}
