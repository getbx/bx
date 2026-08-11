package leakserve

import (
	"net/http"
	"strings"
	"testing"
)

// raw 直接对 listener 的地址发请求,但**自己指定 Host 头** —— 这正是 DNS
// rebinding 的形状:包打到 127.0.0.1,Host 却是攻击者的域名。
func raw(t *testing.T, srv *Server, method, path, host, origin string) int {
	t.Helper()
	body := strings.NewReader(`{"exit_v4":"5.6.7.8"}`)
	req, err := http.NewRequest(method, "http://"+srv.Addr().String()+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host // net/http 用 req.Host 覆盖 Host 头
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// **DNS rebinding。** 用户访问 evil.example,那个域名解析到 127.0.0.1,浏览器
// 于是把请求打到本机、Host 是 evil.example。不校验 Host,用户访问的**任何**
// 网页都能读到这台机器的网络姿态 —— 或者往里投一份伪造的上报。
func TestWrongHostIsRejected(t *testing.T) {
	srv := newTestServer(t)
	tok := "?t=" + srv.Token()
	for _, host := range []string{
		"evil.example",
		// **端口对得上的那一种**:攻击者就是照着我们的端口来的,所以任何形如
		// 「后缀/端口对就放行」的宽松比较都必须在这一条上翻车。
		"evil.example:" + portOf(t, srv),
		"localhost:1",
		// 逐字相等才放行:localhost 与 127.0.0.1 解析到同一处,但不是同一个 origin。
		strings.Replace(srv.Addr().String(), "127.0.0.1", "localhost", 1),
	} {
		if got := raw(t, srv, http.MethodGet, "/"+tok, host, ""); got != http.StatusForbidden {
			t.Errorf("Host=%q 的 GET 必须被拒(DNS rebinding),得到 %d", host, got)
		}
	}
	if got := raw(t, srv, http.MethodGet, "/"+tok, srv.Addr().String(), ""); got != http.StatusOK {
		t.Errorf("正确 Host 应放行,得到 %d", got)
	}
}

// 顶层导航不带 Origin —— 浏览器规范如此。要求它必须存在会让页面自己都打不开。
func TestNavigationWithoutOriginIsAllowed(t *testing.T) {
	srv := newTestServer(t)
	if got := raw(t, srv, http.MethodGet, "/?t="+srv.Token(), srv.Addr().String(), ""); got != http.StatusOK {
		t.Fatalf("无 Origin 的顶层导航必须放行,否则页面打不开,得到 %d", got)
	}
}

// Origin 存在就必须匹配。
func TestWrongOriginIsRejected(t *testing.T) {
	srv := newTestServer(t)
	tok := "?t=" + srv.Token()
	for _, origin := range []string{
		"http://evil.example",
		"https://" + srv.Addr().String(),     // scheme 不同
		"http://localhost:" + portOf(t, srv), // 主机名不同
		"null",
	} {
		if got := raw(t, srv, http.MethodGet, "/"+tok, srv.Addr().String(), origin); got != http.StatusForbidden {
			t.Errorf("Origin=%q 必须被拒,得到 %d", origin, got)
		}
	}
	if got := raw(t, srv, http.MethodGet, "/"+tok, srv.Addr().String(), srv.Origin()); got != http.StatusOK {
		t.Errorf("正确 Origin 应放行,得到 %d", got)
	}
}

// /report 上 Origin 必须**存在**:浏览器的 fetch POST 一定带它,不带的只可能
// 是非浏览器客户端 —— 那正是这道闸门要挡的。
//
// 这一条与上面几条的分工要说清:Host 与「Origin 在场就得对」是**整个服务**的
// 事,由 internal/loopbackgate 无条件执行;「Origin 必须在场」只加在**写**入口
// 上,因为顶层导航按规范不带它,一刀切会让页面自己都打不开。
func TestReportRequiresOrigin(t *testing.T) {
	srv := newTestServer(t)
	tok := "?t=" + srv.Token()
	if got := raw(t, srv, http.MethodPost, "/report"+tok, srv.Addr().String(), ""); got != http.StatusForbidden {
		t.Errorf("无 Origin 的 POST /report 必须被拒,得到 %d", got)
	}
	if got := raw(t, srv, http.MethodPost, "/report"+tok, srv.Addr().String(), srv.Origin()); got != http.StatusOK {
		t.Errorf("带正确 Origin 的 POST /report 应放行,得到 %d", got)
	}
}

// **读入口不许跟着一起收紧。** 上面那条只说「写入口要 Origin」,而最省事的
// 实现方式是把 requireOrigin 一路置真 —— 那样 TestReportRequiresOrigin 照样绿,
// 页面却打不开。这条把「只有写入口收紧」这件事本身钉住:同一个服务上,GET /
// 不带 Origin 放行,POST /report 不带 Origin 被拒。
func TestOriginIsRequiredOnlyOnTheWriteEndpoint(t *testing.T) {
	srv := newTestServer(t)
	tok := "?t=" + srv.Token()
	page := raw(t, srv, http.MethodGet, "/"+tok, srv.Addr().String(), "")
	report := raw(t, srv, http.MethodPost, "/report"+tok, srv.Addr().String(), "")
	if page != http.StatusOK || report != http.StatusForbidden {
		t.Fatalf("Origin 必需这条只该加在写入口上:GET / = %d(want 200),"+
			"POST /report = %d(want 403)", page, report)
	}
}

func portOf(t *testing.T, srv *Server) string {
	t.Helper()
	addr := srv.Addr().String()
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		t.Fatalf("地址读不出端口:%s", addr)
	}
	return addr[i+1:]
}
