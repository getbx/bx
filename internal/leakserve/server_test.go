package leakserve

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
)

// newTestServer 起一个真实的 loopback 服务。**不用 httptest**:约束一要断言的
// 正是「绑在哪」,而 httptest 自己决定绑哪,那就把被测的东西替换掉了。
func newTestServer(t *testing.T) *Server {
	t.Helper()
	srv, err := Listen(Options{
		Judge: func(b leakcheck.BrowserReport) leakcheck.Report {
			return leakcheck.Judge(time.Unix(0, 0).UTC(), b, leakcheck.LocalFacts{})
		},
		HardTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	go srv.Serve()
	return srv
}

// get 按浏览器的样子发一个顶层导航请求(Host 正确、无 Origin —— 浏览器导航
// 本来就不带 Origin)。
func get(t *testing.T, srv *Server, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+srv.Addr().String()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestTokenIsRequiredOnEveryRequest(t *testing.T) {
	srv := newTestServer(t)

	// 无 token
	if resp := get(t, srv, "/"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("无 token 的 GET / 必须被拒,得到 %d", resp.StatusCode)
	}
	// 错 token
	if resp := get(t, srv, "/?t=wrong"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("错 token 的 GET / 必须被拒,得到 %d", resp.StatusCode)
	}
	// 对 token
	resp := get(t, srv, "/?t="+srv.Token())
	if resp.StatusCode != http.StatusOK {
		t.Errorf("对 token 的 GET / 应放行,得到 %d", resp.StatusCode)
	}
	// **同一个 token 必须还能再用一次**:页面至少要发两个请求(取页面 + 上报)。
	// 「单次」是「本次运行专属」,不是「用一次作废」。
	if resp := get(t, srv, "/?t="+srv.Token()); resp.StatusCode != http.StatusOK {
		t.Errorf("同一 token 的第二个请求也应放行(GET 页面 + POST 上报),得到 %d", resp.StatusCode)
	}

	// 上报端点同样校验。
	body := strings.NewReader(`{"exit_v4":"5.6.7.8"}`)
	req, err := http.NewRequest(http.MethodPost, "http://"+srv.Addr().String()+"/report", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srv.Origin())
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("无 token 的 POST /report 必须被拒,得到 %d", resp2.StatusCode)
	}
}

// token 必须够长、够随机:两次运行不能撞。**长度是硬要求** —— 一个 8 位 token
// 在本机是可枚举的,而它挡的正是「同机其它进程顺手读到这台机器的网络姿态」。
func TestTokenIsLongAndUnique(t *testing.T) {
	a := newTestServer(t)
	b := newTestServer(t)
	if a.Token() == b.Token() {
		t.Fatal("两次运行的 token 撞了:token 必须是每次运行现生成的随机值")
	}
	if len(a.Token()) < 32 {
		t.Fatalf("token 太短(%d 字符):本机可枚举,等于没有", len(a.Token()))
	}
}

// URL() 必须自带 token,否则调用方会自己拼一个,而拼错了不会有任何报错 ——
// 只会得到一个打不开的页面。
func TestURLCarriesToken(t *testing.T) {
	srv := newTestServer(t)
	u, err := url.Parse(srv.URL())
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("t"); got != srv.Token() {
		t.Fatalf("URL() 里的 token 是 %q,应为 %q", got, srv.Token())
	}
	if u.Path != "/" {
		t.Fatalf("URL() 应指向 /,得到 %q", u.Path)
	}
}

// 除本次检测所需之外不提供任何端点(设计:最小面)。
func TestOnlyTwoEndpointsExist(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{"/debug/pprof/", "/status", "/facts", "/index.html"} {
		resp := get(t, srv, path+"?t="+srv.Token())
		if resp.StatusCode != http.StatusNotFound {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			t.Errorf("%s 应当是 404(只提供 / 与 /report),得到 %d: %s", path, resp.StatusCode, body)
		}
	}
}
