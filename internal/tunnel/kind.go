package tunnel

import (
	"slices"
	"strings"
)

// 传输种类名。它们是「scheme → 引擎」这张表的值域,也是 supervisor 的
// transportKind、setup 的连通探测等处按字面量派发时该引用的东西。
const (
	KindReality     = "reality"
	KindHysteria2   = "hysteria2"
	KindTrojan      = "trojan"
	KindShadowsocks = "shadowsocks"
	KindVmess       = "vmess"
	// KindBrook 是兜底:brook:// 与任何认不出的串都落它。
	KindBrook = "brook"
)

// schemeKinds 是「scheme 前缀 → 引擎」的唯一真相源。**Kind 与 Kinds 都从它派生**,
// 于是「加了一种传输,而某处清单没跟上」在构造上不可能发生 —— 与 internal/udpsource、
// internal/barriercidr 下沉叶子包同一个先例。
//
// 这条形状是被一次实测逼出来的:supervisor 的 transportsAnsweringTCP(「一次 TCP
// 拨号观测不观测得到这种传输」)那条守卫自称穷举了每一种 transportKind 会返回的
// 传输,而它比对的其实是它自己手抄的一张表 —— 给这里加一行 tuic 之后,两个包全绿,
// 那句自称「穷举」的话变成假话。判据现在改从 Kinds() 取。
var schemeKinds = []struct {
	prefix string
	kind   string
}{
	{"vless://", KindReality},
	{"hysteria2://", KindHysteria2},
	{"hy2://", KindHysteria2},
	{"trojan://", KindTrojan},
	{"ss://", KindShadowsocks},
	{"vmess://", KindVmess},
}

// Kind 由 server link 的 scheme 选传输引擎,是「scheme → 引擎」的唯一真相源。
// supervisor(起隧道)、setup(连通探测)等处都经此派发,避免各自 HasPrefix 发散。
// 加一种传输:在 schemeKinds 里登记一行 + kind_test.go 补一例,各调用方零改动自动跟上。
func Kind(link string) string {
	for _, entry := range schemeKinds {
		if strings.HasPrefix(link, entry.prefix) {
			return entry.kind
		}
	}
	return KindBrook
}

// Kinds 返回 Kind 可能返回的**全部**值(去重,含兜底的 KindBrook)。
//
// 它存在的理由是让下游那些「每一种传输都要登记一句什么」的表可以**结构化**地
// 被穷举,而不是各自手抄一份今天记得的清单。每次调用返回一条新切片(与
// GuardianCapabilities() 同一条纪律:调用方拿去排序/追加不会污染下一个人)。
func Kinds() []string {
	kinds := make([]string, 0, len(schemeKinds)+1)
	for _, entry := range schemeKinds {
		if !slices.Contains(kinds, entry.kind) {
			kinds = append(kinds, entry.kind)
		}
	}
	if !slices.Contains(kinds, KindBrook) {
		kinds = append(kinds, KindBrook)
	}
	return kinds
}

// IsClientLink 报告 link 是否是受支持的「裸」客户端传输链接(六种 scheme 之一)。
// 不含 bx:// / blink:// 换壳链接(那些由 blink 包解壳)。识别口径单一化,
// 供 cli/blink 各处复用,防 prefix 列表各自发散漏登记新传输。
func IsClientLink(link string) bool {
	// Kind 对 brook:// 与任意乱串都回 KindBrook;用显式前缀把乱串排除掉。
	if Kind(link) != KindBrook {
		return true
	}
	return strings.HasPrefix(link, "brook://")
}
