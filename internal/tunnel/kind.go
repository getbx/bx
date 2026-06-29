package tunnel

import "strings"

// Kind 由 server link 的 scheme 选传输引擎,是「scheme → 引擎」的唯一真相源。
// supervisor(起隧道)、setup(连通探测)等处都经此派发,避免各自 HasPrefix 发散。
// 加一种传输:在此登记一行 + kind_test.go 补一例,各调用方零改动自动跟上。
func Kind(link string) string {
	switch {
	case strings.HasPrefix(link, "vless://"):
		return "reality"
	case strings.HasPrefix(link, "hysteria2://"), strings.HasPrefix(link, "hy2://"):
		return "hysteria2"
	case strings.HasPrefix(link, "trojan://"):
		return "trojan"
	case strings.HasPrefix(link, "ss://"):
		return "shadowsocks"
	case strings.HasPrefix(link, "vmess://"):
		return "vmess"
	default:
		return "brook"
	}
}

// IsClientLink 报告 link 是否是受支持的「裸」客户端传输链接(六种 scheme 之一)。
// 不含 bx:// / blink:// 换壳链接(那些由 blink 包解壳)。识别口径单一化,
// 供 cli/blink 各处复用,防 prefix 列表各自发散漏登记新传输。
func IsClientLink(link string) bool {
	// Kind 对 brook:// 与任意乱串都回 "brook";用显式前缀把乱串排除掉。
	if Kind(link) != "brook" {
		return true
	}
	return strings.HasPrefix(link, "brook://")
}
