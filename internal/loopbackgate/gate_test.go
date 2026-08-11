package loopbackgate

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const gateAddr = "127.0.0.1:53019"

func newBoundGate() *Gate {
	g := New()
	g.BindTo(gateAddr)
	return g
}

// 请求构造器:默认是一次**合法**的写请求,各测试只改它想破坏的那一处 ——
// 这样每条断言失败时,原因唯一。
func gateRequest(g *Gate) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/result?t="+g.token, nil)
	r.Host = gateAddr
	r.Header.Set("Origin", "http://"+gateAddr)
	return r
}

func TestLoopbackGateAllowsTheRealPage(t *testing.T) {
	g := newBoundGate()
	if !g.Allow(httptest.NewRecorder(), gateRequest(g), true) {
		t.Fatal("合法请求被挡了 —— 前提不成立,下面的拒绝断言都证明不了什么")
	}
}

// **token 挡的是同机其它进程。** 它们知道端口(随机但可枚举),不知道 token;
// 没有这一道,任意本地进程都能 POST 一份伪造的 ICE 结果,让 bx 报「无泄漏」。
func TestLoopbackGateRefusesWithoutTheToken(t *testing.T) {
	g := newBoundGate()
	for _, token := range []string{"", "wrong", g.token + "x", g.token[:len(g.token)-1]} {
		r := httptest.NewRequest(http.MethodPost, "/result?t="+token, nil)
		r.Host = gateAddr
		r.Header.Set("Origin", "http://"+gateAddr)
		w := httptest.NewRecorder()
		if g.Allow(w, r, true) {
			t.Errorf("token=%q 被放行了", token)
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("token=%q 的响应码 = %d, want 403", token, w.Code)
		}
	}
}

// **Host 逐字比对挡的是 DNS rebinding,而且它是这条防线唯一可靠的判据。**
//
// 一个恶意网页把自己的域名解析到 127.0.0.1,浏览器**替它**发出的请求来自本机、
// 端口也对得上(它就是照着端口来的)—— 唯一对不上的是 Host 头里那个域名。
func TestLoopbackGateRefusesARebindingHost(t *testing.T) {
	g := newBoundGate()
	for _, host := range []string{"evil.example.com", "evil.example.com:53019", "localhost:53019", "127.0.0.1:1", ""} {
		r := gateRequest(g)
		r.Host = host
		w := httptest.NewRecorder()
		if g.Allow(w, r, true) {
			t.Errorf("Host=%q 被放行了 —— DNS rebinding 就是这么进来的", host)
		}
	}
}

// 顶层导航的 GET 按规范**不带** `Origin`。无条件要求它,页面自己都打不开 ——
// 这是把安全要求照字面实现会砸掉功能的那一类。
func TestLoopbackGateAllowsTopLevelNavigationWithoutOrigin(t *testing.T) {
	g := newBoundGate()
	r := httptest.NewRequest(http.MethodGet, "/?t="+g.token, nil)
	r.Host = gateAddr // 刻意不设 Origin
	if !g.Allow(httptest.NewRecorder(), r, false) {
		t.Fatal("顶层导航不带 Origin 是规范行为,不该被挡 —— 挡了页面就打不开")
	}
}

// 但**写**入口必须要求 Origin:它是「谁让浏览器发的这一枪」唯一的线索。
func TestLoopbackGateRefusesAWriteWithoutOrigin(t *testing.T) {
	g := newBoundGate()
	r := httptest.NewRequest(http.MethodPost, "/result?t="+g.token, nil)
	r.Host = gateAddr // 刻意不设 Origin
	if g.Allow(httptest.NewRecorder(), r, true) {
		t.Fatal("回传是写入口,没有 Origin 不许放行")
	}
}

func TestLoopbackGateRefusesAForeignOrigin(t *testing.T) {
	g := newBoundGate()
	for _, origin := range []string{"http://evil.example.com", "https://" + gateAddr, "null", "http://127.0.0.1:1"} {
		r := gateRequest(g)
		r.Header.Set("Origin", origin)
		if g.Allow(httptest.NewRecorder(), r, true) {
			t.Errorf("Origin=%q 被放行了", origin)
		}
	}
}

// **闸门没建成时必须恒拒,不能恒开。**
//
// 两种建不成的方式:熵源失败(token 为空)、忘了 BindTo(addr 为空)。
// 后者尤其阴险 —— 代码看起来完整,闸门也在,只是从没拿到过它要比对的地址。
//
// **实测记录:这道判断在「拒绝」这件事上是被完全包含的。** 把它整个拿掉,
// 这三种输入照样被拒(空 addr 撞 Host 比对,空 token 撞 token 比对),没有任何
// 测试会红。它今天唯一还值钱的是**诊断**:一个忘了 BindTo 的开发者应该看到
// 「闸门没建成」,而不是一个让人查半天的 403。所以下面除了「被拒」之外,
// 还钉住那个**区分得开的状态码** —— 那才是它没被包含的那部分。
func TestLoopbackGateFailsClosedWhenItWasNeverBuilt(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate *Gate
	}{
		{"熵源失败", &Gate{addr: gateAddr}},
		{"忘了 BindTo", &Gate{token: "sometoken"}},
		{"两样都缺", &Gate{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/result?t=sometoken", nil)
			r.Host = gateAddr
			r.Header.Set("Origin", "http://"+gateAddr)
			w := httptest.NewRecorder()
			if tc.gate.Allow(w, r, true) {
				t.Fatal("闸门没建成却放行了 —— 这比没有闸门更糟,因为它让人不再检查")
			}
			// 503 而不是 403:403 说的是「你这次请求不对」,而这里的实情是
			// 「闸门自己没建成」—— 两者要查的地方完全不同。
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("响应码 = %d, want 503 —— 闸门没建成要和「请求不合法」区分开,"+
					"否则忘了 BindTo 的人会照着 403 去查请求", w.Code)
			}
		})
	}
}

// 每次运行一份新 token:一份被记下来的旧 token 不该在下一次检测里还能用。
func TestLoopbackGateMintsAFreshTokenEachRun(t *testing.T) {
	first, second := New(), New()
	if first.token == "" || second.token == "" {
		t.Fatal("token 是空的 —— 闸门恒拒,检测做不成")
	}
	if first.token == second.token {
		t.Fatal("两次运行拿到同一个 token")
	}
	if len(first.token) < 32 {
		t.Errorf("token 只有 %d 个字符,短到可以被猜", len(first.token))
	}
}
