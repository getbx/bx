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
// **写与读它都守,而且两边的后果都不轻。**
//
// 写那一半是**结果投毒**:同机任意进程,或一个经 DNS rebinding 把自己解析到
// 127.0.0.1 的网页,在那个窗口里 POST 一份伪造的 ICE 结果,就能让 bx 报
// 「无泄漏」——**一个能被本地进程说服的安全工具**,而它存在的全部意义就是不该
// 被说服。
//
// 读那一半此前不成立,现在成立了:`bx leakcheck` 的 `POST /report` **在响应里
// 回吐一份判完的报告** —— 系统解析器、默认路由的接口名与它翻出来的 VPN 名字、
// bx 自己的保护状态,以及浏览器刚问到的公网出口地址。也就是说,过了这道闸就能
// 拿到这台机器完整的网络指纹。(`GET /` 仍然只是一个静态页,页面拿不到本机事实
// 那一半 —— 但报告是从写入口回来的,一次请求同时踩两边。)
//
// 记这一条是因为闸门的**判据**不因此改变(token + Host 逐字比对已经同时挡住两
// 者),但它挡住的东西变了;一份说「这里没有机器状态可读」的注释会让下一个人
// 按错误的威胁模型去权衡放宽哪一道。
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
