package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
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
	OwnerUID        uint32
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
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeGuardianJSON(w, http.StatusOK, observableStatus(controller, pathRecoveryControllerFor(controller), options))
	})
	mux.HandleFunc("/v1/up", mutationHandler(controller, controller.Up, mutations, options, "/v1/up"))
	// markUpgradeStop 只包 /v1/down:它把「这次停保护是升级自己的一步」翻译进
	// 请求上下文,Manager.Down 据此保住升级欠条(见 upgradeintent.go)。
	mux.HandleFunc("/v1/down", markUpgradeStop(mutationHandler(controller, controller.Down, mutations, options, "/v1/down")))
	migrationController, _ := controller.(MigrationController)
	mux.HandleFunc("/v1/migrate", migrationHandler(controller, migrationController, mutations, options))
	updateController, _ := controller.(UpdateController)
	mux.HandleFunc("/v1/update", updateHandler(controller, updateController, mutations))
	pathRecoveryController, _ := controller.(PathRecoveryController)
	mux.HandleFunc("/v1/update-check", updateCheckHandler(newUpdateCheckCache(options.UpdateCheck), options.OwnerUID))
	mux.HandleFunc("/v1/recoveries", recoveryRequestHandler(controller, pathRecoveryController, options.OwnerUID))
	mux.HandleFunc("/v1/recoveries/current", recoveryCurrentHandler(pathRecoveryController, options.OwnerUID))
	recoveries, _ := controller.(recoveryLifecycle)
	pathRecoveries, _ := controller.(pathRecoveryLifecycle)
	return &localAPI{handler: mux, mutations: mutations, recoveries: recoveries, pathRecoveries: pathRecoveries}
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

// statusWithVersions 是 mutation/migration handler 回给客户端的那份状态。
func statusWithVersions(controller Controller, options LocalAPIOptions) Status {
	status := statusOf(controller)
	applyVersionFields(&status, options)
	return status
}

func observableStatus(controller Controller, recoveries PathRecoveryController, options LocalAPIOptions) Status {
	status := controller.Status()
	status.Recovery = RecoverySnapshot{State: "idle", Stage: "idle"}
	if recoveries == nil {
		applyVersionFields(&status, options)
		attachCoreRuntime(&status, options)
		return status
	}
	if current, ok := recoveries.(pathRecoveryStatusController); ok {
		status.Recovery, status.NetworkGeneration = current.currentPathRecoveryStatus()
	} else {
		status.Recovery = recoveries.CurrentPathRecovery()
		status.NetworkGeneration = status.Recovery.Generation
	}
	status.Recovery = redactRecoverySnapshot(status.Recovery)
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
	attachCoreRuntime(&status, options)
	return status
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
	if options.CoreRuntime == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), coreRuntimeFetchTimeout)
	defer cancel()
	runtime, err := options.CoreRuntime(ctx)
	if err != nil {
		runtime = CoreRuntime{Reachable: false}
	}
	status.Core = &runtime
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

func migrationHandler(controller Controller, migration MigrationController, mutations *acceptedMutations, options LocalAPIOptions) http.HandlerFunc {
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
		writeGuardianJSON(w, http.StatusOK, statusWithVersions(controller, options))
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
func mutationHandler(controller Controller, mutate func(context.Context) error, mutations *acceptedMutations, options LocalAPIOptions, endpoint string) http.HandlerFunc {
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
		// 版本字段必须一起回:`bx up` 只看这一个响应,不会再补一次 GET /v1/status。
		writeGuardianJSON(w, http.StatusOK, statusWithVersions(controller, options))
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

func writeGuardianJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
