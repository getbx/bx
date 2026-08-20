package guardian

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/appattr"
	"github.com/getbx/bx/internal/supervisor"
)

// startAppsControlSocket 起一个临时 unix socket,充当 Core 的 /v0/apps。
//
// 用 /tmp 而非 t.TempDir():unix socket 路径有长度上限(darwin 上约 104 字节),
// t.TempDir() 在这台机器上产生的路径经常超限,而 /tmp 下的短前缀不会。
// 与 internal/supervisor/control_client_test.go 的 startControlSocket 同一手法 ——
// 这里不能直接复用那个函数(不同包、未导出),故各自维护一份。
func startAppsControlSocket(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bxg-apps-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "core.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// **/v1/apps 与 /v1/rules、/v1/up、/v1/down 同一道门(owner 或 root)。**
//
// 判据不是「应用流量比开关保护更敏感」—— 恰恰相反:能关掉保护的人已经能做
// 更坏的事,取一致才是要点。
func TestLocalAPIAppsRequiresOwnerPeer(t *testing.T) {
	sock := startAppsControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONForTest(w, supervisor.AppTrafficResponse{Subscribed: true})
	})
	handler := appsHandler(sock, 501)
	for _, tc := range []struct {
		name string
		uid  uint32
		got  bool
		want int
	}{
		{"owner", 501, true, http.StatusOK},
		{"root", 0, true, http.StatusOK},
		{"别的用户", 502, true, http.StatusForbidden},
		{"拿不到对端凭据", 0, false, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/apps", nil), tc.uid, tc.got))
			if w.Code != tc.want {
				t.Fatalf("状态码 = %d, want %d, body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// ownerUID 未配置时退化成 root-only —— 与 /v1/rules 同规矩,不因为「没配」就放宽。
func TestLocalAPIAppsStaysRootOnlyWithoutOwner(t *testing.T) {
	sock := startAppsControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONForTest(w, supervisor.AppTrafficResponse{Subscribed: true})
	})
	handler := appsHandler(sock, 0)
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/apps", nil), 501, true))
	if w.Code != http.StatusForbidden {
		t.Fatalf("未配置 owner 时非 root 拿到了 %d", w.Code)
	}
}

// Capabilities 刻意无 omitempty:键缺席是「这版 Guardian 没有这个概念」的唯一信号。
// 漏掉 CapabilityApps 会让菜单永久隐藏这个功能,而不会有任何报错。
func TestGuardianCapabilitiesIncludeApps(t *testing.T) {
	caps := GuardianCapabilities()
	for _, c := range caps {
		if c == CapabilityApps {
			return
		}
	}
	t.Fatalf("能力清单里没有 %q,菜单会永久隐藏这个功能: %v", CapabilityApps, caps)
}

func TestCapabilityAppsValue(t *testing.T) {
	if CapabilityApps != "apps" {
		t.Fatalf("CapabilityApps = %q, want %q", CapabilityApps, "apps")
	}
}

// 「没接线」回 501,不回空列表 —— 与 rulesHandler 同源:空报告会让菜单显示
// 「一个应用都没有」,而事实是这条链没接上。
func TestLocalAPIAppsReportsNotWiredRatherThanEmpty(t *testing.T) {
	handler := appsHandler("", 501)
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/apps", nil), 501, true))
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("未接线时 = %d, want 501, body=%s", w.Code, w.Body.String())
	}
	// 响应体不能长得像一份「有效但为空」的报告 —— 不许出现 subscribed 字段,
	// 那正是「没接线」与「没有应用」被静默合并的具体表现形式。
	if strings.Contains(w.Body.String(), "subscribed") {
		t.Fatalf("501 响应体里出现了 subscribed 字段,看起来像一份正常报告:%s", w.Body.String())
	}
}

func TestLocalAPIAppsMethodNotAllowed(t *testing.T) {
	sock := startAppsControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONForTest(w, supervisor.AppTrafficResponse{Subscribed: true})
	})
	handler := appsHandler(sock, 501)
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodPost, "/v1/apps", nil), 501, true))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /v1/apps = %d, want 405", w.Code)
	}
}

// 三态之一:订阅了且确实没有连接 —— 三组齐全的空报告,subscribed=true,无 error。
// Guardian 必须原样转发,不许压平或重新聚合。
func TestLocalAPIAppsForwardsSubscribedEmptyReport(t *testing.T) {
	want := supervisor.AppTrafficResponse{
		Subscribed: true,
		Report: appattr.Report{Groups: []appattr.Group{
			{Path: appattr.PathTunnel}, {Path: appattr.PathDirect}, {Path: appattr.PathBlocked},
		}},
	}
	sock := startAppsControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONForTest(w, want)
	})
	handler := appsHandler(sock, 501)
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/apps", nil), 501, true))
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var got supervisor.AppTrafficResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解不出应答:%v %s", err, w.Body.String())
	}
	if !got.Subscribed || got.Error != "" || len(got.Report.Groups) != 3 {
		t.Fatalf("got %+v, want 三组齐全的空报告 + subscribed=true + 无 error", got)
	}
}

// 三态之二:订阅了但问不出来 —— Report 是零值(Groups==nil),error 非空。
// **这是最容易被压平的一态**:如果实现先碰 Report 再判 Error,就会把「没查
// 出来」发布成「查过、一个应用都没有」。
func TestLocalAPIAppsForwardsSubscribedButUnknownError(t *testing.T) {
	want := supervisor.AppTrafficResponse{Subscribed: true, Error: "OwnersByPort 失败: 权限不足"}
	sock := startAppsControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONForTest(w, want)
	})
	handler := appsHandler(sock, 501)
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/apps", nil), 501, true))
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200(问不出来是数据,不是服务端故障), body=%s", w.Code, w.Body.String())
	}
	var got supervisor.AppTrafficResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解不出应答:%v %s", err, w.Body.String())
	}
	if !got.Subscribed {
		t.Fatal("subscribed 应为 true")
	}
	if got.Error != want.Error {
		t.Fatalf("error = %q, want 原样透传 %q", got.Error, want.Error)
	}
	if len(got.Report.Groups) != 0 {
		t.Fatalf("报错时不该有三组齐全的空报告(会读作「查过、没有应用」),got %d groups", len(got.Report.Groups))
	}
}

// 三态之三:没人订阅 —— subscribed=false,三组齐全的空报告,无 error。
func TestLocalAPIAppsForwardsNotSubscribed(t *testing.T) {
	want := supervisor.AppTrafficResponse{
		Subscribed: false,
		Report: appattr.Report{Groups: []appattr.Group{
			{Path: appattr.PathTunnel}, {Path: appattr.PathDirect}, {Path: appattr.PathBlocked},
		}},
	}
	sock := startAppsControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONForTest(w, want)
	})
	handler := appsHandler(sock, 501)
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/apps", nil), 501, true))
	var got supervisor.AppTrafficResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解不出应答:%v %s", err, w.Body.String())
	}
	if got.Subscribed {
		t.Fatal("subscribed 应为 false(没人在看)")
	}
	if len(got.Report.Groups) != 3 {
		t.Fatalf("没人订阅时也要三组齐全,got %d", len(got.Report.Groups))
	}
}

// Core 侧完全不可达(拨号失败)时,Guardian 不能报 200 也不能撒谎说没接线 ——
// 而应如实报一个失败类别,原始错误串不外传。
func TestLocalAPIAppsFetchFailureReturns503(t *testing.T) {
	handler := appsHandler(filepath.Join(t.TempDir(), "does-not-exist.sock"), 501)
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/apps", nil), 501, true))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d, want 503, body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "does-not-exist") {
		t.Fatalf("响应体外传了 socket 路径:%s", w.Body.String())
	}
}

// 接线守卫:NewLocalAPI 必须真的挂上 /v1/apps,并且门是 authorizeOwnerPeer。
func TestNewLocalAPIWiresAppsEndpoint(t *testing.T) {
	controller := &fakeController{}
	handler := NewLocalAPI(controller, LocalAPIOptions{OwnerUID: 501})
	w := httptest.NewRecorder()
	// 未授权:不管 Core 那边接没接线,都必须先被 403 挡下来。
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/apps", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("无凭据访问 /v1/apps = %d, want 403", w.Code)
	}
}

// **这是本轮复审要买到的东西**:成功转发路径必须经 NewLocalAPI 本身被验证,
// 不能只靠直调 appsHandler(sock, …)——后者验证的是 appsHandler 这个函数,
// 不是「NewLocalAPI 正确把 options.AppsSockPath 接给了它」这件事。没有
// AppsSockPath 之前,这条路径只能绕开 NewLocalAPI 去测,而组装根上的接线
// 错误正是这个仓库反复栽的形状(CLAUDE.md)。
func TestNewLocalAPIForwardsAppsThroughAppsSockPath(t *testing.T) {
	want := supervisor.AppTrafficResponse{
		Subscribed: true,
		Report: appattr.Report{Groups: []appattr.Group{
			{Path: appattr.PathTunnel, Rows: []appattr.AppRow{{App: "Chrome", Conns: 2, BytesUp: 5, BytesDown: 6}}},
			{Path: appattr.PathDirect},
			{Path: appattr.PathBlocked},
		}},
	}
	sock := startAppsControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONForTest(w, want)
	})
	controller := &fakeController{}
	handler := NewLocalAPI(controller, LocalAPIOptions{OwnerUID: 501, AppsSockPath: sock})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/apps", nil), 501, true))
	if w.Code != http.StatusOK {
		t.Fatalf("经 NewLocalAPI 转发 = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var got supervisor.AppTrafficResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解不出应答:%v %s", err, w.Body.String())
	}
	if !got.Subscribed || len(got.Report.Groups) != 3 || got.Report.Groups[0].Rows[0].App != "Chrome" {
		t.Fatalf("got %+v, want %+v — NewLocalAPI 没有把 options.AppsSockPath 正确接给 appsHandler", got, want)
	}
}

// Client.AppTraffic 经 GET /v1/apps 取报告,三态原样解出。
func TestClientAppTrafficDecodesResponse(t *testing.T) {
	want := supervisor.AppTrafficResponse{
		Subscribed: true,
		Report: appattr.Report{Groups: []appattr.Group{
			{Path: appattr.PathTunnel, Rows: []appattr.AppRow{{App: "Chrome", Conns: 3, BytesUp: 10, BytesDown: 20}}},
			{Path: appattr.PathDirect},
			{Path: appattr.PathBlocked},
		}},
	}
	client := &Client{HTTPClient: &http.Client{Transport: recoveryRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/apps" {
			t.Fatalf("got %s %s, want GET /v1/apps", r.Method, r.URL.Path)
		}
		body, _ := json.Marshal(want)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(body))),
		}, nil
	})}}
	got, err := client.AppTraffic(context.Background())
	if err != nil {
		t.Fatalf("AppTraffic: %v", err)
	}
	if !got.Subscribed || len(got.Report.Groups) != 3 || got.Report.Groups[0].Rows[0].App != "Chrome" {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// client.AppTraffic 走独立的解析路径,同样要透传失败码(与 c.Up/c.Update 等同一条纪律)。
func TestClientAppTrafficSurfacesFailureCode(t *testing.T) {
	client := &Client{HTTPClient: &http.Client{Transport: recoveryRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotImplemented,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"apps unavailable: no core socket"}`)),
		}, nil
	})}}
	_, err := client.AppTraffic(context.Background())
	if err == nil || !strings.Contains(err.Error(), "apps unavailable") {
		t.Fatalf("AppTraffic() error = %v, want failure surfaced", err)
	}
}

// writeJSONForTest 是测试专用的极简 JSON writer(与 supervisor 包内 writeJSON
// 同形状,但那个未导出、这里不跨包复用)。
func writeJSONForTest(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
