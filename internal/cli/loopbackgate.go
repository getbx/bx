package cli

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
)

// loopbackGate 是本地检测服务的准入闸门。
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
type loopbackGate struct {
	token string
	// addr 是监听下来的真实地址(如 `127.0.0.1:53019`)。**必须在 Listen 之后**
	// 由 bindTo 填 —— 端口是内核给的,构造闸门时还不知道。
	addr string
}

func newLoopbackGate() *loopbackGate {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand 失败在实践中意味着系统熵源坏了。**绝不退化成空 token**:
		// 那会让闸门恒开而外表看不出来。留空串 + bindTo 之后 allow 恒拒,
		// 于是检测失败而不是静默失守。
		return &loopbackGate{}
	}
	return &loopbackGate{token: base64.RawURLEncoding.EncodeToString(raw)}
}

func (g *loopbackGate) bindTo(addr string) { g.addr = addr }

// allow 判定这次请求过不过闸。不过闸时它自己写好响应,调用方直接 return。
//
// requireOrigin 只对**写**入口置真。
func (g *loopbackGate) allow(w http.ResponseWriter, r *http.Request, requireOrigin bool) bool {
	if g.token == "" || g.addr == "" {
		// 闸门没建成(熵源失败,或忘了 bindTo)。fail-closed:宁可这次检测做不成,
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
