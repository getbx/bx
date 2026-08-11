// Package loopbackgate 是本机检测服务的**唯一**一道准入闸门。
//
// 它原本长在 internal/cli 里,只服务 `bx webrtc-check --browser`;
// internal/leakserve 需要同一道闸门,于是它搬到了这里 —— **不是抄一份**。
// 一道安全闸门有两份实现,就有两份会各自漂移的判据,而漂移是无声的:
// 两边都还在,两边都还绿,只有一边还对。
package loopbackgate

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
)

// Gate 是本地检测服务的准入闸门。
//
// **它守的不是「谁能读到数据」,而是「谁能往里写」。** 今天这个服务没有任何端点
// 吐出机器状态(`GET /` 只返回一个静态页),所以缺闸门的后果不是信息泄漏,是
// **结果投毒**:同机任意进程,或一个经 DNS rebinding 把自己解析到 127.0.0.1 的
// 网页,在那 20 秒窗口里 POST 一份伪造的 ICE 结果,就能让 bx 报「无泄漏」——
// **一个能被本地进程说服的安全工具**,而它存在的全部意义就是不该被说服。
//
// 两道闸,各挡各的:
//
//   - **token**:随机、每次运行一份,写在页面 URL 里。挡的是同机其它进程 ——
//     它们知道端口(随机,但可枚举),不知道 token。
//   - **Host 逐字比对**:挡的是 **DNS rebinding**。一个恶意网页可以把自己的域名
//     解析到 127.0.0.1,于是浏览器**替它**发出的请求来自一个合法来源、也带得上
//     cookie,但 `Host` 头里是**它的域名**,不是我们监听的那个 `127.0.0.1:port`。
//     这是这条防线唯一可靠的判据 —— 端口对不上没用(它就是照着端口来的),
//     来源 IP 对不上也没用(那确实是本机浏览器发的)。
//
// **`Origin` 只能选择性要求**:顶层导航的 GET 按规范根本不带 `Origin` 头,无条件
// 要求它会让页面自己都打不开。故 GET 只查 Host,写入口(POST)额外要求 Origin
// 存在且逐字相符。
type Gate struct {
	token string
	// addr 是监听下来的真实地址(如 `127.0.0.1:53019`)。**必须在 Listen 之后**
	// 由 BindTo 填 —— 端口是内核给的,构造闸门时还不知道。
	addr string
}

// New 现生成一份本次运行专属的 token。**它不是「用一次作废」**:页面至少要发
// 两个请求(取页面 + 回传),用一次作废会让回传永远打不进来。
func New() *Gate {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand 失败在实践中意味着系统熵源坏了。**绝不退化成空 token**:
		// 那会让闸门恒开而外表看不出来。留空串 + BindTo 之后 Allow 恒拒,
		// 于是检测失败而不是静默失守。
		return &Gate{}
	}
	return &Gate{token: base64.RawURLEncoding.EncodeToString(raw)}
}

// BindTo 记下**真实**监听地址。必须在 net.Listen 之后调用。
func (g *Gate) BindTo(addr string) { g.addr = addr }

// Token 是这次运行的 token,调用方要把它拼进给浏览器的入口 URL。
func (g *Gate) Token() string { return g.token }

// Allow 判定这次请求过不过闸。不过闸时它自己写好响应,调用方直接 return。
//
// requireOrigin 只对**写**入口置真。
func (g *Gate) Allow(w http.ResponseWriter, r *http.Request, requireOrigin bool) bool {
	if g.token == "" || g.addr == "" {
		// 闸门没建成(熵源失败,或忘了 BindTo)。fail-closed:宁可这次检测做不成,
		// 也不要一个看起来在守、实际上谁都放行的闸门。
		http.Error(w, "leak check gate unavailable", http.StatusServiceUnavailable)
		return false
	}
	// **Host 逐字比对是 DNS rebinding 的唯一防线**,所以它无条件生效。
	if r.Host != g.addr {
		http.Error(w, "unexpected Host", http.StatusForbidden)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if origin != "http://"+g.addr {
			http.Error(w, "unexpected Origin", http.StatusForbidden)
			return false
		}
	} else if requireOrigin {
		http.Error(w, "missing Origin", http.StatusForbidden)
		return false
	}
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("t")), []byte(g.token)) != 1 {
		http.Error(w, "bad token", http.StatusForbidden)
		return false
	}
	return true
}
