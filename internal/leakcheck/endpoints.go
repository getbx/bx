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
)

// EndpointDisclosure 是页面在联网**之前**必须原样显示的第三方清单。
type EndpointDisclosure struct {
	EchoV4 string `json:"echo_v4"`
	EchoV6 string `json:"echo_v6"`
	STUN   string `json:"stun"`
}

// Endpoints 返回这一版要联系的第三方。页面与 CLI 都从这里取,**不各自写一份**:
// 两处各写一份时,页面上说的与实际请求的会静默分叉,而「事先明说要联系谁」这条
// 缓解措施正是靠它们一致才成立的。
func Endpoints() EndpointDisclosure {
	return EndpointDisclosure{EchoV4: EchoV4URL, EchoV6: EchoV6URL, STUN: STUNURL}
}
