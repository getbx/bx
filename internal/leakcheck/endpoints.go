package leakcheck

// 三个第三方端点。**它们是用户可见契约**:页面在发起任何网络请求**之前**必须
// 原样显示这三个串(设计风险三:第三方暴露是用户明确接受的,但必须可见)。
//
// 选取理由(每一条都是硬约束,换端点前逐条复核,守卫见 endpoints_test.go):
//
//   - 回声端必须**同时有 v4-only 与 v6-only 主机名**。用一个双栈主机名就问不出
//     「v6 出口是谁」,而那正是别的 VPN 最常见的漏洞。
//   - 返回体要小且格式稳定:icanhazip 返回的就是一个 IP 加换行,text/plain,
//     没有 JSON 结构可以在某次改版里换形状。
//   - 必须带 CORS。页面是 loopback origin,没有 `Access-Control-Allow-Origin`
//     就一个字节都读不到。两个回声端 2026-08-11 实测均返回 `*`。
//   - **不用 china 直连列表里的域名。** 本项目在这上面栽过:拿 ifconfig.me 当
//     出口探测,而它本就在直连列表里。api.ipify.org 有同样问题(ipify.org 在
//     列表第 6045 行,DomainSet 是后缀匹配),故一并否掉。
//   - STUN 只要一个公共且长期稳定的。**不用 stun.l.google.com**:它在中国大陆
//     链路上经常解析不出,而一个连不上 STUN 的检测只会输出 not checked。
const (
	EchoV4URL = "https://ipv4.icanhazip.com"
	EchoV6URL = "https://ipv6.icanhazip.com"
	STUNURL   = "stun:stun.cloudflare.com:3478"
	// TraceURL 一次请求同时给出**出口地址与出口国家**,身份段那几条要的就是后者。
	//
	// 选它的理由:① 一个请求两件事,不必为「国家」再引一个第三方;② 返回体是
	// `key=value` 逐行的 text/plain,没有 JSON 结构可以在某次改版里换形状;
	// ③ 2026-08-11 实测带 `Access-Control-Allow-Origin: *`;④ 它与两个回声端
	// 互为旁证 —— 两个独立第三方对同一个出口的说法本身就是一条可看的证据。
	//
	// 落选的:ipapi.co 与 ifconfig.co **都在 china 直连列表里**(踩这个坑的第三次,
	// 而这次是在选之前用生产那份 DomainSet 逐个比出来的);ipwho.is 实测当场 429。
	TraceURL = "https://www.cloudflare.com/cdn-cgi/trace"
)

// EndpointDisclosure 是页面在联网**之前**必须原样显示的第三方清单。
type EndpointDisclosure struct {
	EchoV4 string `json:"echo_v4"`
	EchoV6 string `json:"echo_v6"`
	STUN   string `json:"stun"`
	Trace  string `json:"trace"`
}

// Endpoints 返回这一版要联系的第三方。页面与 CLI 都从这里取,**不各自写一份**:
// 两处各写一份时,页面上说的与实际请求的会静默分叉,而「事先明说要联系谁」这条
// 缓解措施正是靠它们一致才成立的。
// All 按固定顺序返回这次会联系的全部第三方。
//
// **调用方不许自己拼这一行。** trace 端点加进来那次,页面(它遍历模板字段)老实列了
// 四个,而 CLI 的 `Contacted:` 是手写拼接的、还停在三个 —— 少报一个第三方不是排版
// 问题,是把「联网之前把要联系的人说全」这个用户可见契约打了折。
func (d EndpointDisclosure) All() []string {
	return []string{d.EchoV4, d.EchoV6, d.STUN, d.Trace}
}

func Endpoints() EndpointDisclosure {
	return EndpointDisclosure{EchoV4: EchoV4URL, EchoV6: EchoV6URL, STUN: STUNURL, Trace: TraceURL}
}

// ReachTarget 是一个可达性探测目标。
//
// ExpectedSignal 记的是**上一次真机实测见到什么**。它不参与判定,只用来让
// 「这个端点的行为变了」有人发现 —— CF 的防护会变,今天 claude.ai/favicon.ico
// 不挂挑战,明天可能挂(spec §10)。
type ReachTarget struct {
	ID             string
	Title          string
	URL            string
	ExpectedSignal string
	// OnChinaDirectList 是「这个域名在不在内建 china 直连列表里」。
	//
	// **它是解释,不是门。** 回声端点那三关的第三关是「不许在列表里」(在的话
	// 探测会走直连、报出真实 IP);可达性探测走哪条路由**由本功能自己指定**,
	// 不依赖分流 —— 所以这里要的是如实报告,作为该端点结论的一行证据。
	//
	// **值是写死的编译期常量,不是运行时算出来的**:本包纯度守卫的
	// allowedInternalDeps 白名单刻意很窄(只有 internal/tristate 与
	// internal/protectionstate),运行时判 china 列表要 import internal/route,
	// 会撑开这个白名单。四个值 2026-09-14 用生产 route.NewDomainSet 逐个实测过,
	// 全部为 false(见 endpoints_test.go 的
	// TestReachTargetsOnChinaDirectListMatchesRealList),换端点时重新实测再改。
	//
	// 指针类型:nil 是「没填」,由守卫拦下;false 是「查过了,不在」。
	OnChinaDirectList *bool
}

// reachTargetNotOnChinaList 是四个可达性端点共用的「不在列表里」标记。
// 用一个共享变量取地址,免得每个 target 字面量里各写一个局部 bool。
var reachTargetNotOnChinaList = false

// ReachTargets 是内嵌的一小组 AI 站。**只发 GET,不带认证,不发 body** ——
// 401/405 恰恰是我们要的信号:服务自己在说话。
//
// **chatgpt.com 刻意不在这里**:它实测恒为 CF 挑战页,会变成一行永远给不出
// 答案的噪声(spec §4.1)。
func ReachTargets() []ReachTarget {
	return []ReachTarget{
		{
			ID: "anthropic_api", Title: "Anthropic API",
			URL:               "https://api.anthropic.com/v1/messages",
			ExpectedSignal:    "405 + 服务 JSON(invalid_request_error / Method Not Allowed),2026-09-13 实测",
			OnChinaDirectList: &reachTargetNotOnChinaList,
		},
		{
			ID: "claude_web", Title: "claude.ai",
			URL:               "https://claude.ai/favicon.ico",
			ExpectedSignal:    "200 + favicon 二进制;首页是 403 CF 挑战,favicon 路径不挂防护,2026-09-13 实测",
			OnChinaDirectList: &reachTargetNotOnChinaList,
		},
		{
			ID: "openai_api", Title: "OpenAI API",
			URL:               "https://api.openai.com/v1/models",
			ExpectedSignal:    "401 + 服务 JSON(Missing bearer authentication),2026-09-13 实测",
			OnChinaDirectList: &reachTargetNotOnChinaList,
		},
		{
			ID: "google_ai_api", Title: "Google AI API",
			URL:               "https://generativelanguage.googleapis.com/v1beta/models",
			ExpectedSignal:    "403 + 服务 JSON(Method doesn't allow unregistered callers),2026-09-13 实测",
			OnChinaDirectList: &reachTargetNotOnChinaList,
		},
	}
}
