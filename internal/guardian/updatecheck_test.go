package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func updateCheckRequest(uid uint32, gotUID bool) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/v1/update-check", nil)
	return request.WithContext(withPeerCredentials(request.Context(), uid, gotUID))
}

// 端点的授权级别与 /v1/recoveries 一致(root 或 config 里的 owner),**不是**
// /v1/status 那样无检查。
//
// 要挡的不是 payload —— 版本号在 /v1/status 里早就人人可读了。要挡的是**副作用**:
// 这是本地 socket 上唯一一个「对端一句话就能让一个 root 守护进程发起出网请求」的
// 端点。不设防等于把它变成同机任意进程都能反复触发的外连扳机。
func TestUpdateCheckEndpointAuthorizesOnlyRootOrConfiguredOwner(t *testing.T) {
	tests := []struct {
		name     string
		uid      uint32
		gotUID   bool
		wantCode int
	}{
		{name: "missing credentials", wantCode: http.StatusForbidden},
		{name: "unrelated user", uid: 502, gotUID: true, wantCode: http.StatusForbidden},
		{name: "owner", uid: 501, gotUID: true, wantCode: http.StatusOK},
		{name: "root", uid: 0, gotUID: true, wantCode: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			handler := NewLocalAPI(&fakeController{}, LocalAPIOptions{
				OwnerUID: 501,
				UpdateCheck: func(context.Context) (UpdateAvailability, error) {
					calls++
					return UpdateAvailability{Current: "v1", Latest: "v2", Available: true, Verified: true}, nil
				},
			})
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, updateCheckRequest(tt.uid, tt.gotUID))
			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
			// 未授权时不得触发取数:授权若只过滤响应而不过滤动作,外连扳机照样在。
			if tt.wantCode == http.StatusForbidden && calls != 0 {
				t.Fatalf("未授权的请求仍然发起了 %d 次更新查询 —— 授权必须挡在网络 I/O 之前", calls)
			}
		})
	}
}

func TestUpdateCheckEndpointPublishesTheProvidersAnswer(t *testing.T) {
	handler := NewLocalAPI(&fakeController{}, LocalAPIOptions{
		OwnerUID: 501,
		UpdateCheck: func(context.Context) (UpdateAvailability, error) {
			return UpdateAvailability{Current: "v0.1.0", Latest: "v0.2.0", Available: true, Verified: true}, nil
		},
	})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, updateCheckRequest(501, true))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	// 键名逐字钉住:菜单侧 UpdateCheck 是 `bx update --check --json` 那份
	// updateCheckReport 的解码器,搬到 Guardian 之后形状不许变。
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"current":   "v0.1.0",
		"latest":    "v0.2.0",
		"available": true,
		"verified":  true,
	} {
		if decoded[key] != want {
			t.Errorf("%s = %v, want %v(菜单按这四个键解码)", key, decoded[key], want)
		}
	}
}

// 查不动就说「问不出来」,绝不退化成一个自信的答案。
//
// 若失败被就地编成 available:false,菜单会把它读成「你已经是最新」—— 一个错答案
// 比没答案糟得多,而且它会一直错到下次检查(默认一天)。
func TestUpdateCheckFailureIsUnknownNotUpToDate(t *testing.T) {
	handler := NewLocalAPI(&fakeController{}, LocalAPIOptions{
		OwnerUID: 501,
		UpdateCheck: func(context.Context) (UpdateAvailability, error) {
			return UpdateAvailability{}, errors.New("dial github.com: connection refused")
		},
	})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, updateCheckRequest(501, true))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"available", "latest", "verified"} {
		if _, present := decoded[key]; present {
			t.Fatalf("失败响应不得带 %q 字段(会被解成一个自信的答案):%s", key, body)
		}
	}
	// 与其余 handler 同一处置:原始错误只进 Guardian 日志,响应体不外传网络细节。
	if strings.Contains(body, "github.com") || strings.Contains(body, "connection refused") {
		t.Fatalf("失败响应外传了 provider 的原始错误:%s", body)
	}
}

// 没接 provider ≠ 「没有更新」。
func TestUpdateCheckWithoutProviderIsNotImplemented(t *testing.T) {
	recorder := httptest.NewRecorder()
	NewLocalAPI(&fakeController{}, LocalAPIOptions{OwnerUID: 501}).ServeHTTP(recorder, updateCheckRequest(501, true))
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501, body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestUpdateCheckRejectsNonGET(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/update-check", nil)
	request = request.WithContext(withPeerCredentials(request.Context(), 0, true))
	recorder := httptest.NewRecorder()
	NewLocalAPI(&fakeController{}, LocalAPIOptions{
		OwnerUID:    501,
		UpdateCheck: func(context.Context) (UpdateAvailability, error) { return UpdateAvailability{}, nil },
	}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}

// 成功答案缓存、失败答案不缓存。
//
// 缓存是频率闸门(root 守护进程的出网 I/O);不缓存失败是因为「问不出来」是瞬时
// 状态 —— 把一次抖动冻结一小时,等于让用户一整天看不到已经发布的更新。
func TestUpdateCheckCachesSuccessButNotFailure(t *testing.T) {
	var calls int
	failures := 2
	now := time.Now()
	cache := newUpdateCheckCache(func(context.Context) (UpdateAvailability, error) {
		calls++
		if failures > 0 {
			failures--
			return UpdateAvailability{}, errors.New("network down")
		}
		return UpdateAvailability{Current: "v1", Latest: "v2", Available: true, Verified: true}, nil
	})
	cache.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		if _, err := cache.get(context.Background()); err == nil {
			t.Fatalf("第 %d 次应当失败", i+1)
		}
	}
	if calls != 2 {
		t.Fatalf("失败被缓存了:calls = %d, want 2", calls)
	}
	if _, err := cache.get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("成功答案没被缓存:calls = %d, want 3", calls)
	}
	now = now.Add(updateCheckCacheTTL + time.Second)
	if _, err := cache.get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("缓存过期后没有重新查询:calls = %d, want 4", calls)
	}
}

// 并发请求只触发一次外连,而不是 N 次。
func TestUpdateCheckSerializesConcurrentCallers(t *testing.T) {
	var mu sync.Mutex
	var concurrent, peak int
	cache := newUpdateCheckCache(func(context.Context) (UpdateAvailability, error) {
		mu.Lock()
		concurrent++
		if concurrent > peak {
			peak = concurrent
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		concurrent--
		mu.Unlock()
		return UpdateAvailability{Current: "v1", Latest: "v1"}, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cache.get(context.Background())
		}()
	}
	wg.Wait()
	if peak != 1 {
		t.Fatalf("并发取数峰值 = %d,want 1 —— 端点不得把 N 个本地请求放大成 N 次出网", peak)
	}
}

// Guardian 自己声明能力,而不是让 UI 去解析某个二进制的帮助文本。
func TestGuardianDeclaresDiagnosticsArchiveCapability(t *testing.T) {
	handler := NewLocalAPI(&fakeController{}, LocalAPIOptions{OwnerUID: 501})
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	request = request.WithContext(withPeerCredentials(request.Context(), 501, true))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var decoded struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !contains(decoded.Capabilities, CapabilityDiagnosticsArchive) {
		t.Fatalf("capabilities = %v,必须声明 %q —— 菜单靠它替代 `bx logs --help` 的文本探测",
			decoded.Capabilities, CapabilityDiagnosticsArchive)
	}
}

// capabilities 这个键必须**恒在场**。
//
// 消费方要分得开「声明过、这个能力不在里面」与「这一版压根没声明过能力」(升级
// 窗口里还没换掉的旧 Guardian)。omitempty(或忘了填)会把两者压成同一个形状,
// 而菜单正是靠这个区分决定要不要提示升级 —— 值断言看不见这种回归,故断言的是
// **键存在性**。
func TestStatusAlwaysCarriesTheCapabilitiesKey(t *testing.T) {
	handler := NewLocalAPI(&fakeController{}, LocalAPIOptions{OwnerUID: 501})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/status"},
		{http.MethodPost, "/v1/up"},
		{http.MethodPost, "/v1/down"},
	} {
		request := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		request = request.WithContext(withPeerCredentials(request.Context(), 0, true))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		if _, present := decoded["capabilities"]; !present {
			t.Errorf("%s %s 的响应缺 capabilities 键;缺席是「旧版 Guardian」的唯一信号,不能被健康的新版占用",
				tc.method, tc.path)
		}
	}

	// 上面那圈跑的是**今天的** GuardianCapabilities():它非空,所以就算给字段加上
	// `,omitempty`,键照样出现、整套测试照样全绿(实测过)。「刻意不加 omitempty」
	// 这条契约写在 types.go 的注释里,而注释拦不住任何人 —— 直接断言结构体标签。
	//
	// 真正会踩雷的是这个组合:某天能力集合被清空(或某条路径发布的是零值 Status),
	// 加了 omitempty 的字段就会静默消失,而消失恰恰是「旧版 Guardian」的专属信号
	// —— 菜单会对一台完好的新机器报「运行的是旧版」。
	field, ok := reflect.TypeOf(Status{}).FieldByName("Capabilities")
	if !ok {
		t.Fatal("Status 没有 Capabilities 字段 —— 本守卫读不懂现在的代码,请连同它一起重写")
	}
	if tag := field.Tag.Get("json"); tag != "capabilities" {
		t.Errorf(`Status.Capabilities 的 json 标签是 %q,必须恰好是 "capabilities":`+
			`加上 omitempty 会让空集合与「从未声明」塌成同一个形状,而菜单正是靠这个区分`+
			`判断对面是不是旧版 Guardian`, tag)
	}
}

// GuardianCapabilities 每次返回新切片:调用方会把它塞进 Status 发布出去,
// 共享同一份底层数组等于把包级可变状态交给 JSON 编码器旁边的任意代码。
func TestGuardianCapabilitiesDoesNotShareBackingArray(t *testing.T) {
	first := GuardianCapabilities()
	first[0] = "tampered"
	if second := GuardianCapabilities(); second[0] == "tampered" {
		t.Fatal("GuardianCapabilities 返回了共享的底层数组")
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
