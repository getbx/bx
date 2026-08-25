package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getbx/bx/internal/appattr"
	"github.com/getbx/bx/internal/confirm"
	"github.com/getbx/bx/internal/stats"
)

type fakeControlEngine struct {
	commitErr   error
	rollbackErr error
	armErr      error
	state       confirm.State
	armed       bool
	applied     bool
}

func (f *fakeControlEngine) Commit() error        { return f.commitErr }
func (f *fakeControlEngine) Rollback() error      { return f.rollbackErr }
func (f *fakeControlEngine) State() confirm.State { return f.state }
func (f *fakeControlEngine) Arm(apply, undo func() error) error {
	if f.armErr != nil {
		return f.armErr
	}
	if apply != nil {
		_ = apply()
		f.applied = true
	}
	f.armed = true
	return nil
}

func testMux(eng controlEngine) http.Handler {
	return newControlMux(eng, func() stats.Report { return stats.Report{Server: "test-node"} }, nopMutator{}, nil, 0)
}

type fakeMutator struct {
	gotLink         string
	setErr          error
	setCalled       bool
	rehCalled       bool
	reconnectCalled bool
	reconnectErr    error
	serverLink      string
	serverUDP       string
}

func (f *fakeMutator) SetTransport(link string) (func() error, func() error, error) {
	f.setCalled = true
	f.gotLink = link
	if f.setErr != nil {
		return nil, nil, f.setErr
	}
	return func() error { return nil }, func() error { return nil }, nil
}

func (f *fakeMutator) Rehijack() (func() error, func() error, error) {
	f.rehCalled = true
	return func() error { return nil }, func() error { return nil }, nil
}

func (f *fakeMutator) SetServer(link, udp string) (func() error, func() error, error) {
	f.setCalled = true
	f.gotLink = link
	f.serverLink = link
	f.serverUDP = udp
	if f.setErr != nil {
		return nil, nil, f.setErr
	}
	return func() error { return nil }, func() error { return nil }, nil
}

func (f *fakeMutator) Reconnect() error {
	f.reconnectCalled = true
	return f.reconnectErr
}

func testMuxMut(eng controlEngine, mut mutator) http.Handler {
	return newControlMux(eng, func() stats.Report { return stats.Report{Server: "test-node"} }, mut, nil, 0)
}

// newTestControlServer 直接构造 *controlServer(而非经 mux),供需要直调 handler
// 方法(如 handleSetServer)的测试用。
func newTestControlServer(t *testing.T, mut mutator) *controlServer {
	t.Helper()
	return &controlServer{
		eng:    &fakeControlEngine{},
		report: func() stats.Report { return stats.Report{Server: "test-node"} },
		mut:    mut,
	}
}

// newTestControlMux 建一个装好全部路由的 mux,供「路由是否注册」这类测试用。
func newTestControlMux(t *testing.T) http.Handler {
	t.Helper()
	return testMuxMut(&fakeControlEngine{}, &fakeMutator{})
}

func testMuxReload(eng controlEngine, reload func() error) http.Handler {
	return newControlMux(eng, func() stats.Report { return stats.Report{Server: "test-node"} }, nopMutator{}, reload, 0)
}

func mustPost(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestControlStatus(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{state: confirm.StateArmed}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v0/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status code=%d", resp.StatusCode)
	}
	var rep stats.Report
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if rep.Server != "test-node" {
		t.Fatalf("got %+v", rep)
	}
	if rep.MutationState != "armed" {
		t.Fatalf("mutation_state=%q want armed", rep.MutationState)
	}
}

func TestControlReconnect(t *testing.T) {
	mut := &fakeMutator{}
	srv := httptest.NewServer(testMuxMut(&fakeControlEngine{}, mut))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/reconnect")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if !mut.reconnectCalled {
		t.Fatal("Reconnect was not called")
	}
	var out controlResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.State != "reconnected" {
		t.Fatalf("state=%q", out.State)
	}
}

func TestControlCapabilitiesAdvertisesSafeReconnect(t *testing.T) {
	h := newControlMux(&fakeControlEngine{}, func() stats.Report { return stats.Report{} }, &fakeMutator{}, nil, 0)
	r := httptest.NewRequest(http.MethodGet, "/v0/capabilities", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		SafeReconnect bool `json:"safe_reconnect"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil || !out.SafeReconnect {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

// 「没订阅」「订阅了但问不出来」「订阅了且确实没连接」是三种不同的状态,
// 不许合并成一个空列表 —— 空列表读作「一条都没有」是句自洽的假话。
func TestControlAppsDistinguishesUnsubscribedFromEmpty(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{}}
	at := NewAppTraffic(src, nil)
	h := newControlMuxWithAppTraffic(&fakeControlEngine{}, func() stats.Report { return stats.Report{} }, nopMutator{}, 0, at)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// 未订阅:subscribed=false,但组数仍须齐全(消费方按下标取组)。
	resp, err := http.Get(srv.URL + "/v0/apps")
	if err != nil {
		t.Fatal(err)
	}
	var got AppTrafficResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("未订阅时 status=%d", resp.StatusCode)
	}
	if got.Subscribed {
		t.Fatal("未订阅却报 subscribed=true")
	}
	if len(got.Report.Groups) != 3 {
		t.Fatalf("未订阅时组数应恒为 3,got %d", len(got.Report.Groups))
	}
	if got.Error != "" {
		t.Fatalf("未订阅不该有 error: %q", got.Error)
	}

	// subscribe=1 之后:subscribed=true 且 groups 恒为三组(此刻没有连接,
	// 而不是「没在采集」)。
	resp, err = http.Get(srv.URL + "/v0/apps?subscribe=1")
	if err != nil {
		t.Fatal(err)
	}
	got = AppTrafficResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("订阅后 status=%d", resp.StatusCode)
	}
	if !got.Subscribed {
		t.Fatal("订阅了却报 subscribed=false")
	}
	if len(got.Report.Groups) != 3 {
		t.Fatalf("订阅后组数应恒为 3,got %d", len(got.Report.Groups))
	}
	if got.Error != "" {
		t.Fatalf("订阅了且能问出来时不该有 error: %q", got.Error)
	}
}

// appSource 报错:HTTP 200 + subscribed=true + error 非空(不是 500,因为
// 「问不出来」是数据,不是服务端故障),且**绝不**用三组齐全的空报告冒充
// 「查过、一个应用都没有」—— Snapshot 出错时返回的是零值 Report(Groups==nil),
// 这里必须原样透传,不许先摸一下 Report 再决定。
func TestControlAppsReportsSourceErrorAsDataNotFault(t *testing.T) {
	src := &fakeAppSource{err: errors.New("问不出来")}
	at := NewAppTraffic(src, nil)
	at.Subscribe()
	h := newControlMuxWithAppTraffic(&fakeControlEngine{}, func() stats.Report { return stats.Report{} }, nopMutator{}, 0, at)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v0/apps?subscribe=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("appSource 报错也应是 200,got %d", resp.StatusCode)
	}
	var got AppTrafficResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Subscribed {
		t.Fatal("订阅了却报 subscribed=false")
	}
	if got.Error == "" {
		t.Fatal("appSource 报错时 error 字段不能为空")
	}
	if len(got.Report.Groups) != 0 {
		t.Fatalf("appSource 报错时不许伪造出三组齐全的空报告,got %d groups", len(got.Report.Groups))
	}
}

// controlMuxOptionsFromServe 是 serveControlWithPathRecovery(要 root socket,
// 测不了)与 newControlMuxFull 之间那一跳纯字段翻译。这里直接调用它、逐字段
// 核对,专门堵「controlServeOptions 加了新字段,这一跳的翻译却漏写」这类
// 事故——编译器对漏写的结构体字段完全沉默,AppTraffic 正是本轮要修的那个
// 真实先例(Task 8 加完端点,run.go 却没把 appTraffic 接进 controlServeOptions/
// controlMuxOptions 这条链)。
func TestControlMuxOptionsFromServeCarriesEveryField(t *testing.T) {
	eng := &fakeControlEngine{}
	mut := &fakeMutator{}
	recoverer := &scriptedPathRecoverer{}
	probeDial := &fakeProbeDialer{}
	at := NewAppTraffic(&fakeAppSource{}, nil)
	report := func() stats.Report { return stats.Report{Server: "carried-report"} }

	var reloadCalled, refreshCalled, shutdownCalled bool
	opts := controlServeOptions{
		Engine:        eng,
		Runtime:       func() RuntimeState { return RuntimeState{ServerHost: "carried-runtime"} },
		Mutator:       mut,
		Reload:        func() error { reloadCalled = true; return nil },
		RefreshBypass: func([]string) (bool, error) { refreshCalled = true; return false, nil },
		OwnerUID:      7,
		Shutdown:      func() { shutdownCalled = true },
		Recoverer:     recoverer,
		ProbeDial:     probeDial,
		AppTraffic:    at,
	}

	got := controlMuxOptionsFromServe(opts, report, 4242)

	if got.Engine != eng {
		t.Error("Engine 没搬过来")
	}
	if got.Mutator != mut {
		t.Error("Mutator 没搬过来")
	}
	if got.Recoverer != recoverer {
		t.Error("Recoverer 没搬过来")
	}
	if got.ProbeDial != probeDial {
		t.Error("ProbeDial 没搬过来")
	}
	if got.AppTraffic != at {
		t.Error("AppTraffic 没搬过来 —— GET /v0/apps 会恒 501")
	}
	if got.OwnerUID != 7 {
		t.Errorf("OwnerUID = %d, want 7", got.OwnerUID)
	}
	if got.ProcessPID != 4242 {
		t.Errorf("ProcessPID = %d, want 4242(调用方传入的值,不是 opts 里的字段)", got.ProcessPID)
	}
	if got.Report == nil || got.Report().Server != "carried-report" {
		t.Error("Report 没搬过来")
	}
	if got.Runtime == nil || got.Runtime().ServerHost != "carried-runtime" {
		t.Error("Runtime 没搬过来")
	}
	if got.Reload == nil {
		t.Fatal("Reload 没搬过来")
	}
	got.Reload()
	if !reloadCalled {
		t.Error("Reload 搬过来的不是同一个闭包")
	}
	if got.RefreshBypass == nil {
		t.Fatal("RefreshBypass 没搬过来")
	}
	got.RefreshBypass(nil)
	if !refreshCalled {
		t.Error("RefreshBypass 搬过来的不是同一个闭包")
	}
	if got.Shutdown == nil {
		t.Fatal("Shutdown 没搬过来")
	}
	got.Shutdown()
	if !shutdownCalled {
		t.Error("Shutdown 搬过来的不是同一个闭包")
	}
}

// 没接线(appTraffic==nil)回 501,不是空报告 —— 与 handleProbe/handlePathRecovery
// 的「没接线不是测不通」同一条纪律。**不只查状态码**——501 是不是真的说清了
// 「没接线」这句措辞,此前只靠读代码背书,现在断言响应体里那句话确实在。
func TestControlAppsNotImplementedWhenNil(t *testing.T) {
	h := newControlMuxWithAppTraffic(&fakeControlEngine{}, func() stats.Report { return stats.Report{} }, nopMutator{}, 0, nil)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v0/apps")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got controlResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Error, "unavailable") {
		t.Fatalf("501 没说清「没接线」:error=%q", got.Error)
	}
}

// POST /v0/apps 应 405 —— 这是只读端点,与 TestControlStatusRejectsPost 同一模式。
func TestControlAppsRejectsPost(t *testing.T) {
	at := NewAppTraffic(&fakeAppSource{}, nil)
	h := newControlMuxWithAppTraffic(&fakeControlEngine{}, func() stats.Report { return stats.Report{} }, nopMutator{}, 0, at)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/apps")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("apps POST 应 405,得 %d", resp.StatusCode)
	}
}

// `subscribe` 只认字面 `"1"` 才续订 —— `0`/`true`/无值这几种「看起来像开」的
// 输入都必须**不**续订,否则「显式关闭」会被误当成「开启」。用
// fakeAppSource.callCount 而不是响应体来判断,因为未订阅与已过期都会返回
// subscribed=false、三组齐全的空报告,响应体本身分不出「到底有没有调
// Subscribe」;calls 计数是 appSource 有没有真的被问过的唯一证据。
func TestControlAppsSubscribeOnlyAcceptsLiteralOne(t *testing.T) {
	for _, q := range []string{"subscribe=0", "subscribe=true", "subscribe", "subscribe="} {
		t.Run(q, func(t *testing.T) {
			src := &fakeAppSource{owners: map[appattr.PortKey]string{}}
			at := NewAppTraffic(src, nil)
			h := newControlMuxWithAppTraffic(&fakeControlEngine{}, func() stats.Report { return stats.Report{} }, nopMutator{}, 0, at)
			srv := httptest.NewServer(h)
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/v0/apps?" + q)
			if err != nil {
				t.Fatal(err)
			}
			var got AppTrafficResponse
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if got.Subscribed {
				t.Fatalf("?%s 不该续订,却报 subscribed=true", q)
			}
		})
	}
}

func TestControlReconnectPropagatesFailure(t *testing.T) {
	mut := &fakeMutator{reconnectErr: errors.New("unhealthy")}
	srv := httptest.NewServer(testMuxMut(&fakeControlEngine{}, mut))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/reconnect")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestControlPathRecoveryPublishesCurrentSnapshotWhilePostRuns(t *testing.T) {
	release := make(chan struct{})
	recoverer := &scriptedPathRecoverer{entered: make(chan struct{}), release: release}
	h := newControlMuxWithPathRecovery(&fakeControlEngine{}, func() stats.Report { return stats.Report{} }, nopMutator{}, nil, 0, recoverer)
	srv := httptest.NewServer(h)
	defer srv.Close()

	postDone := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/v0/path-recovery", "application/json", strings.NewReader(`{"reason":"underlay_changed","generation":"wifi-b"}`))
		if err != nil {
			t.Errorf("POST path recovery: %v", err)
			return
		}
		postDone <- resp
	}()
	<-recoverer.entered

	resp, err := http.Get(srv.URL + "/v0/path-recovery")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	var current PathRecoverySnapshot
	if err := json.NewDecoder(resp.Body).Decode(&current); err != nil {
		t.Fatal(err)
	}
	if current.State != "recovering" || current.Stage != "observe" || current.Generation != "wifi-b" {
		t.Fatalf("current snapshot = %+v", current)
	}

	close(release)
	post := <-postDone
	defer post.Body.Close()
	if post.StatusCode != http.StatusOK {
		t.Fatalf("POST status = %d", post.StatusCode)
	}
}

func TestControlPathRecoveryMapsTypedErrorsToSafeCodes(t *testing.T) {
	secret := "vless://secret-uuid@proxy.example:443?pbk=secret-key"
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{
			name: "unknown typed code falls back",
			err:  &PathRecoveryError{Code: "vless://secret-code", Detail: secret},
			code: "recovery_failed",
		},
		{
			name: "wrapped known code is preserved",
			err:  fmt.Errorf("candidate failed: %w", &PathRecoveryError{Code: "transport_unavailable", Detail: secret}),
			code: "transport_unavailable",
		},
		{
			name: "capture missing is preserved",
			err:  &PathRecoveryError{Code: "capture_missing", Detail: secret},
			code: "capture_missing",
		},
		{
			name: "underlay rebind failure is preserved",
			err:  &PathRecoveryError{Code: "underlay_rebind_failed", Detail: secret},
			code: "underlay_rebind_failed",
		},
		{
			name: "network unavailable is preserved",
			err:  &PathRecoveryError{Code: "network_unavailable", Detail: secret},
			code: "network_unavailable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recoverer := &scriptedPathRecoverer{err: tc.err}
			h := newControlMuxWithPathRecovery(&fakeControlEngine{}, func() stats.Report { return stats.Report{} }, nopMutator{}, nil, 0, recoverer)
			req := httptest.NewRequest(http.MethodPost, "/v0/path-recovery", strings.NewReader(`{"reason":"manual"}`))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
			}
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("HTTP body leaked recovery detail: %s", w.Body.String())
			}
			var snapshot PathRecoverySnapshot
			if err := json.NewDecoder(w.Body).Decode(&snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.ErrorCode != tc.code || snapshot.Detail != "" {
				t.Fatalf("snapshot = %+v, want code %q without detail", snapshot, tc.code)
			}
		})
	}
}

func TestControlCommitOK(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{state: confirm.StateCommitted}))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/commit")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("commit ok 应 200,得 %d", resp.StatusCode)
	}
}

func TestControlCommitNotArmed(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{commitErr: confirm.ErrNotArmed}))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/commit")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("nothing to commit 应 409,得 %d", resp.StatusCode)
	}
}

func TestControlRollbackOK(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{state: confirm.StateReverted}))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/rollback")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("rollback ok 应 200,得 %d", resp.StatusCode)
	}
}

func TestControlRollbackError(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{rollbackErr: errors.New("回滚也失败")}))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/rollback")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("rollback 出错应 500,得 %d", resp.StatusCode)
	}
}

func TestControlStatusRejectsPost(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{}))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/status")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status POST 应 405,得 %d", resp.StatusCode)
	}
}

func TestControlReloadInvokesReload(t *testing.T) {
	called := false
	srv := httptest.NewServer(testMuxReload(&fakeControlEngine{}, func() error { called = true; return nil }))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/reload")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reload 应 200,得 %d", resp.StatusCode)
	}
	if !called {
		t.Fatal("reload 未被调用")
	}
}

func TestControlReloadPropagatesError(t *testing.T) {
	srv := httptest.NewServer(testMuxReload(&fakeControlEngine{}, func() error { return errors.New("解析失败") }))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/reload")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("reload 出错应 500,得 %d", resp.StatusCode)
	}
}

func TestControlReloadNotImplementedWhenNil(t *testing.T) {
	srv := httptest.NewServer(testMuxReload(&fakeControlEngine{}, nil))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/reload")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("reload=nil 应 501,得 %d", resp.StatusCode)
	}
}

func TestControlShutdownRejectsExpectedPIDMismatch(t *testing.T) {
	shutdownCalls := 0
	h := newControlMuxWithRuntimeAndShutdown(
		&fakeControlEngine{},
		func() stats.Report { return stats.Report{} },
		func() RuntimeState { return RuntimeState{PID: 42} },
		nopMutator{}, nil, 0, 42,
		func() { shutdownCalls++ },
	)

	mismatch := httptest.NewRequest(http.MethodPost, "/v0/shutdown", strings.NewReader(`{"expected_pid":43}`))
	mismatchResponse := httptest.NewRecorder()
	h.ServeHTTP(mismatchResponse, mismatch)
	if mismatchResponse.Code != http.StatusConflict {
		t.Fatalf("PID mismatch status = %d, want %d; body=%s", mismatchResponse.Code, http.StatusConflict, mismatchResponse.Body.String())
	}
	if shutdownCalls != 0 {
		t.Fatalf("PID mismatch triggered shutdown %d times", shutdownCalls)
	}

	matching := httptest.NewRequest(http.MethodPost, "/v0/shutdown", strings.NewReader(`{"expected_pid":42}`))
	matchingResponse := httptest.NewRecorder()
	h.ServeHTTP(matchingResponse, matching)
	if matchingResponse.Code != http.StatusOK {
		t.Fatalf("matching PID status = %d, body=%s", matchingResponse.Code, matchingResponse.Body.String())
	}
	if shutdownCalls != 1 {
		t.Fatalf("matching PID shutdown calls = %d, want 1", shutdownCalls)
	}
}

func TestControlShutdownUsesMutationPeerAuthorization(t *testing.T) {
	shutdownCalls := 0
	h := newControlMuxWithRuntimeAndShutdown(
		&fakeControlEngine{},
		func() stats.Report { return stats.Report{} },
		func() RuntimeState { return RuntimeState{PID: 42} },
		nopMutator{}, nil, 501, 42,
		func() { shutdownCalls++ },
	)
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	request := httptest.NewRequest(http.MethodPost, "/v0/shutdown", strings.NewReader(`{"expected_pid":42}`))
	request = request.WithContext(context.WithValue(request.Context(), ctxConnKey{}, serverConn))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unverifiable peer status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if shutdownCalls != 0 {
		t.Fatalf("unauthorized peer triggered shutdown %d times", shutdownCalls)
	}
}

func TestRequireControlSocketPropagatesStartError(t *testing.T) {
	want := errors.New("bind failed")
	_, err := requireControlSocket(func() (io.Closer, error) {
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("want wrapped start error %v, got %v", want, err)
	}
}

func TestControlSetTransportArmed(t *testing.T) {
	mut := &fakeMutator{}
	eng := &fakeControlEngine{}
	srv := httptest.NewServer(testMuxMut(eng, mut))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v0/transport", "application/json",
		strings.NewReader(`{"link":"vless://x@h:443"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("armed 应 200,得 %d", resp.StatusCode)
	}
	if !mut.setCalled || mut.gotLink != "vless://x@h:443" || !eng.armed {
		t.Fatalf("应调 mut.SetTransport(link) 且 engine.Arm;mut=%+v armed=%v", mut, eng.armed)
	}
}

func TestControlSetTransportEmptyLink(t *testing.T) {
	mut := &fakeMutator{}
	srv := httptest.NewServer(testMuxMut(&fakeControlEngine{}, mut))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v0/transport", "application/json", strings.NewReader(`{"link":""}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空 link 应 400,得 %d", resp.StatusCode)
	}
	if mut.setCalled {
		t.Fatal("空 link 不应调 mut")
	}
}

func TestControlSetTransportAlreadyArmed(t *testing.T) {
	mut := &fakeMutator{}
	srv := httptest.NewServer(testMuxMut(&fakeControlEngine{state: confirm.StateArmed}, mut))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v0/transport", "application/json", strings.NewReader(`{"link":"vless://x@h:443"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("已 armed 应 409,得 %d", resp.StatusCode)
	}
	if mut.setCalled {
		t.Fatal("已 armed 不应调用 mutator")
	}
}

func TestControlRehijackArmed(t *testing.T) {
	mut := &fakeMutator{}
	eng := &fakeControlEngine{}
	srv := httptest.NewServer(testMuxMut(eng, mut))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v0/rehijack", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !mut.rehCalled || !eng.armed {
		t.Fatalf("rehijack 应 200 + mut.Rehijack + Arm;code=%d mut=%+v armed=%v", resp.StatusCode, mut, eng.armed)
	}
}

func TestHandleSetServerArmsPairedSwap(t *testing.T) {
	rec := &fakeMutator{}
	cs := newTestControlServer(t, rec)
	body := strings.NewReader(`{"link":"vless://tokyo","udp":"hysteria2://tokyo"}`)
	w := httptest.NewRecorder()
	cs.handleSetServer(w, httptest.NewRequest(http.MethodPost, "/v0/server", body))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if rec.serverLink != "vless://tokyo" || rec.serverUDP != "hysteria2://tokyo" {
		t.Fatalf("两条链接都要传到 mutator, got %q / %q", rec.serverLink, rec.serverUDP)
	}
}

func TestHandleSetServerRejectsMissingLink(t *testing.T) {
	cs := newTestControlServer(t, &fakeMutator{})
	w := httptest.NewRecorder()
	cs.handleSetServer(w, httptest.NewRequest(http.MethodPost, "/v0/server", strings.NewReader(`{"udp":"hysteria2://tokyo"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 link 必须 400, got %d", w.Code)
	}
}

func TestControlSetServerUnauthorizedPeerRejected(t *testing.T) {
	mut := &fakeMutator{}
	h := newControlMux(&fakeControlEngine{}, func() stats.Report { return stats.Report{} }, mut, nil, 501)
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	request := httptest.NewRequest(http.MethodPost, "/v0/server", strings.NewReader(`{"link":"vless://tokyo","udp":"hysteria2://tokyo"}`))
	request = request.WithContext(context.WithValue(request.Context(), ctxConnKey{}, serverConn))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unauthorized peer status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if mut.setCalled {
		t.Fatal("unauthorized peer 不应触发 mutator.SetServer")
	}
}

func TestControlSetServerAlreadyArmed(t *testing.T) {
	mut := &fakeMutator{}
	srv := httptest.NewServer(testMuxMut(&fakeControlEngine{state: confirm.StateArmed}, mut))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v0/server", "application/json", strings.NewReader(`{"link":"vless://tokyo"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("已 armed 应 409,得 %d", resp.StatusCode)
	}
	if mut.setCalled {
		t.Fatal("已 armed 不应调用 mutator")
	}
}

func TestSetServerRouteIsRegistered(t *testing.T) {
	// 端点没注册时 mux 返回 404 text/plain,在客户端表现为解析错误而不是
	// 「切换失败」—— 用户看到的是一句读不懂的话,而不是「这台连不上」。
	mux := newTestControlMux(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v0/server", strings.NewReader(`{"link":"vless://tokyo"}`)))
	if w.Code == http.StatusNotFound {
		t.Fatal("/v0/server 没注册进 mux")
	}
}

// recordingMutator 在 **apply 闭包里** 记录动作发生的顺序(不是方法体里 ——
// mutator 的契约是方法本身无副作用,真实改动只发生在 apply 内)。
type recordingMutator struct {
	onRehijack  func()
	onSetServer func()
	rehijackErr error
	setErr      error
}

func (r *recordingMutator) SetTransport(string) (func() error, func() error, error) {
	return func() error { return nil }, func() error { return nil }, nil
}

func (r *recordingMutator) SetServer(string, string) (func() error, func() error, error) {
	if r.setErr != nil {
		return nil, nil, r.setErr
	}
	return func() error {
		if r.onSetServer != nil {
			r.onSetServer()
		}
		return nil
	}, func() error { return nil }, nil
}

func (r *recordingMutator) Rehijack() (func() error, func() error, error) {
	if r.rehijackErr != nil {
		return nil, nil, r.rehijackErr
	}
	return func() error {
		if r.onRehijack != nil {
			r.onRehijack()
		}
		return nil
	}, func() error { return nil }, nil
}

func (r *recordingMutator) Reconnect() error { return nil }

// 先换传输再装路由 = 在新服务器的 bypass 还没落实的那一小段时间里,
// 隧道自己的流量被劫进 TUN —— 成环。而成环是静默的:连得上、status 显绿。
func TestSetServerRehijacksBeforeSwappingWhenBypassChanged(t *testing.T) {
	var order []string
	rec := &recordingMutator{
		onRehijack:  func() { order = append(order, "rehijack") },
		onSetServer: func() { order = append(order, "swap") },
	}
	cs := newTestControlServer(t, rec)
	cs.refreshBypass = func([]string) (bool, error) { return true, nil } // 集合变了

	w := httptest.NewRecorder()
	cs.handleSetServer(w, httptest.NewRequest(http.MethodPost, "/v0/server",
		strings.NewReader(`{"link":"vless://new"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(order) != 2 || order[0] != "rehijack" || order[1] != "swap" {
		t.Fatalf("必须先 rehijack 再换传输, got %v", order)
	}
}

func TestSetServerSkipsRehijackWhenBypassUnchanged(t *testing.T) {
	var order []string
	rec := &recordingMutator{
		onRehijack:  func() { order = append(order, "rehijack") },
		onSetServer: func() { order = append(order, "swap") },
	}
	cs := newTestControlServer(t, rec)
	cs.refreshBypass = func([]string) (bool, error) { return false, nil } // 已知服务器之间切换

	w := httptest.NewRecorder()
	cs.handleSetServer(w, httptest.NewRequest(http.MethodPost, "/v0/server",
		strings.NewReader(`{"link":"vless://known"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	// 已知服务器的 bypass 启动时就铺好了。此时重装路由是纯粹的风险:
	// 它会重探网关、拆装真实路由,而这次切换根本不需要动路由。
	if len(order) != 1 || order[0] != "swap" {
		t.Fatalf("集合没变时不该重装路由, got %v", order)
	}
}

func TestSetServerRefusesWhenBypassRefreshFails(t *testing.T) {
	cs := newTestControlServer(t, &recordingMutator{})
	cs.refreshBypass = func([]string) (bool, error) { return false, errors.New("解析不了新服务器的 IP") }

	w := httptest.NewRecorder()
	cs.handleSetServer(w, httptest.NewRequest(http.MethodPost, "/v0/server",
		strings.NewReader(`{"link":"vless://new"}`)))
	if w.Code == http.StatusOK {
		t.Fatal("bypass 刷新失败必须拒绝切换 —— 落实不了就绝不切过去,这是成环与不成环的分界")
	}
}

// refreshBypass 可为 nil(该部署不支持刷新,如没有 ConfigPath):此时 /v0/server
// 仍须正常做配对切换,只是不碰路由。既有测试构造的 controlServer 全是这种。
func TestSetServerWorksWithoutBypassRefresh(t *testing.T) {
	var order []string
	rec := &recordingMutator{
		onRehijack:  func() { order = append(order, "rehijack") },
		onSetServer: func() { order = append(order, "swap") },
	}
	cs := newTestControlServer(t, rec) // refreshBypass 未设 = nil

	w := httptest.NewRecorder()
	cs.handleSetServer(w, httptest.NewRequest(http.MethodPost, "/v0/server",
		strings.NewReader(`{"link":"vless://x"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(order) != 1 || order[0] != "swap" {
		t.Fatalf("无刷新能力时只做配对切换, got %v", order)
	}
}

// 端点知道自己要切到哪台(请求体里就是),必须把目标显式告诉刷新闭包。
// 交给闭包去读配置文件的 current 猜 = 在「先热切、成功后才落盘」的 spec 下
// 必然猜错一次 —— 而猜错的那一次就是不装 bypass 直接切过去 = 成环。
func TestSetServerPassesTargetLinksToBypassRefresh(t *testing.T) {
	var got []string
	cs := newTestControlServer(t, &recordingMutator{})
	cs.refreshBypass = func(required []string) (bool, error) {
		got = append([]string(nil), required...)
		return false, nil
	}
	w := httptest.NewRecorder()
	cs.handleSetServer(w, httptest.NewRequest(http.MethodPost, "/v0/server",
		strings.NewReader(`{"link":"vless://tokyo","udp":"hysteria2://tokyo"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	want := map[string]bool{"vless://tokyo": false, "hysteria2://tokyo": false}
	for _, l := range got {
		if _, ok := want[l]; !ok {
			t.Fatalf("传了不该传的 %q, got %v", l, got)
		}
		want[l] = true
	}
	for l, seen := range want {
		if !seen {
			t.Fatalf("目标的 %s 必须点名为必需 —— 漏掉它就会被当成「没在用的服务器」跳过 = 成环", l)
		}
	}
}

// udp 为空(目标没有 UDP 专用传输)时不能把空串塞进去:空串取不出 host,
// 会让刷新失败,进而把一次完全正常的切换拒掉。
func TestSetServerOmitsEmptyUDPFromRequiredLinks(t *testing.T) {
	var got []string
	cs := newTestControlServer(t, &recordingMutator{})
	cs.refreshBypass = func(required []string) (bool, error) {
		got = append([]string(nil), required...)
		return false, nil
	}
	w := httptest.NewRecorder()
	cs.handleSetServer(w, httptest.NewRequest(http.MethodPost, "/v0/server",
		strings.NewReader(`{"link":"vless://tokyo"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(got) != 1 || got[0] != "vless://tokyo" {
		t.Fatalf("udp 为空时不该传空串, got %#v", got)
	}
}

// 刷新是**替换**语义(SetServerBypass/store.set 整组换掉,不是并集),故两次
// 刷新绝不能交错:后写的那次会抹掉前一次刚算进去的那台服务器,而那台的路由
// 已经装上了 —— 集合与内核就此对不上,下一次 rehijack 把它拆掉 = 成环。
// 串行化由 cs.mu 提供,这条测试就是钉住「刷新确实在锁里面」。
func TestSetServerSerializesConcurrentBypassRefresh(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var inFlight atomic.Int32
	var overlapped atomic.Bool
	var calls atomic.Int32

	cs := newTestControlServer(t, &recordingMutator{})
	cs.refreshBypass = func([]string) (bool, error) {
		if inFlight.Add(1) > 1 {
			overlapped.Store(true)
		}
		if calls.Add(1) == 1 {
			close(entered)
			<-release // 第一次刷新卡在锁里
		}
		inFlight.Add(-1)
		return false, nil
	}

	first := make(chan struct{})
	go func() {
		defer close(first)
		cs.handleSetServer(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v0/server",
			strings.NewReader(`{"link":"vless://a"}`)))
	}()
	<-entered

	second := make(chan struct{})
	go func() {
		defer close(second)
		cs.handleSetServer(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v0/server",
			strings.NewReader(`{"link":"vless://b"}`)))
	}()
	// 第二个必须还没进到刷新里(否则就是没被 cs.mu 挡住)。
	select {
	case <-second:
		t.Fatal("第二次切换在第一次还没做完时就返回了 —— 刷新没有被 cs.mu 串行化")
	case <-time.After(100 * time.Millisecond):
	}
	if calls.Load() != 1 {
		t.Fatalf("第二次刷新在第一次持锁期间就跑了 = 交错,替换语义下会互相抹掉, calls=%d", calls.Load())
	}

	close(release)
	<-first
	<-second
	if overlapped.Load() {
		t.Fatal("两次刷新出现了重叠")
	}
}

// **组装根那一跳:两轮复审各点过一次,至今没有任何测试。**
//
// `controlMuxOptionsForServe` 上下两跳早就各有覆盖(`controlMuxOptionsFromServe`
// 有逐字段单测、`run.go → opts` 有文本守卫),唯独它自己那几行没有。复审实测:
// 把 `opts.ConfigWarnings` 换成 nil、或在这一跳丢掉 `AppTraffic`,
// `internal/supervisor` 与 `internal/cli` **两个包都绿** —— 而后者的生产后果是
// `/v0/apps` 变成永久 501,菜单那个应用流量窗口一个应用都不显示、没有任何报错。
//
// 判据分两半,各钉一件事:
//   - 这一条走**反射**、**默认参与**:构造一个所有字段都非零的入参,断言产出的
//     每一个字段也非零。新加字段自动被覆盖 —— 与 statusdigest 的排除名单同一条
//     纪律(漏填是多报、反过来是漏报,代价不对称)。
//   - 下面那条钉 `newStatusReporter` 的 **10 个位置参数**,尤其连着三个 string。
func TestControlMuxOptionsForServeCarriesEveryField(t *testing.T) {
	at := NewAppTraffic(&fakeAppSource{}, nil)
	opts := controlServeOptions{
		Counters:       &stats.Counters{},
		Tunnel:         fakeReporterTunnel{},
		Server:         "carried-server",
		Mode:           "carried-mode",
		UDPMode:        "carried-udp",
		TransportInfo:  func() (string, []string, string) { return "t", []string{"t"}, "u" },
		Runtime:        func() RuntimeState { return RuntimeState{ServerHost: "carried-runtime"} },
		Engine:         &fakeControlEngine{},
		Mutator:        &fakeMutator{},
		Reload:         func() error { return nil },
		RefreshBypass:  func([]string) (bool, error) { return false, nil },
		Shutdown:       func() {},
		OwnerUID:       7,
		Recoverer:      &scriptedPathRecoverer{},
		ProbeDial:      &fakeProbeDialer{},
		ConfigWarnings: []stats.Warning{{Name: "carried-warning", Severity: "warn"}},
		AppTraffic:     at,
		RuleHistory: func() *stats.RuleHistorySnapshot {
			return &stats.RuleHistorySnapshot{Decisions: 1}
		},
	}

	got := controlMuxOptionsForServe(context.Background(), opts, 4242)

	v := reflect.ValueOf(got)
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		name := v.Type().Field(i).Name
		if f.IsZero() {
			t.Errorf("%s 在这一跳上丢了 —— 编译器对漏填的结构体字段完全沉默,"+
				"而这一跳的两侧各有测试、它自己没有(两轮复审各点过一次)", name)
		}
	}
}

// **`newStatusReporter` 的 10 个位置参数里,server/mode/udpMode 是连着三个 string。**
//
// 换位不会有任何编译错误,症状是 `bx status` 里三个字段互相串台 —— 而没有人会
// 对着一份 status 输出逐字核对它们的对应关系。这里用三个互不相同的值把顺序钉死,
// 顺带钉住 `ConfigWarnings` 真的到了报告里(复审实测:换成 nil,两个包全绿)。
func TestControlMuxOptionsForServeWiresTheReporterInTheRightOrder(t *testing.T) {
	opts := controlServeOptions{
		Counters:       &stats.Counters{},
		Tunnel:         fakeReporterTunnel{},
		Server:         "the-server",
		Mode:           "the-mode",
		UDPMode:        "the-udp-mode",
		Runtime:        func() RuntimeState { return RuntimeState{} },
		ConfigWarnings: []stats.Warning{{Name: "the-warning", Severity: "warn"}},
		// 这条测试看的是形参顺序;累计历史给一个「没有」的提供者即可 ——
		// **但它必须传**,newStatusReporter 对 nil provider 直接 panic。
		RuleHistory: func() *stats.RuleHistorySnapshot { return nil },
	}

	rep := controlMuxOptionsForServe(context.Background(), opts, 1).Report()

	if rep.Server != "the-server" {
		t.Errorf("Server = %q,想要 the-server —— 三个 string 参数换了位", rep.Server)
	}
	if rep.Mode != "the-mode" {
		t.Errorf("Mode = %q,想要 the-mode —— 三个 string 参数换了位", rep.Mode)
	}
	if rep.UDPMode != "the-udp-mode" {
		t.Errorf("UDPMode = %q,想要 the-udp-mode —— 三个 string 参数换了位", rep.UDPMode)
	}
	var found bool
	for _, w := range rep.Warnings {
		if w.Name == "the-warning" {
			found = true
		}
	}
	if !found {
		t.Errorf("ConfigWarnings 没到报告里(warnings=%#v)—— 它在这一跳被换成 nil "+
			"时,supervisor 与 cli 两个包都不会红", rep.Warnings)
	}
}
