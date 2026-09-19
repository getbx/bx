// Package macnetprobe 是 macOS 网络共存检查的**纯解析**判据:给定
// `scutil --nc list` / `scutil --proxy` / `netstat -rn` 的输出,答出几个事实。
//
// **它下沉成叶子包的理由与 internal/udpsource、internal/barriercidr 相同,而且
// 这一次是被真机打出来的**:同样三个函数此前在 internal/supervisor(喂 `bx status`)
// 与 internal/platformcheck(喂 `bx doctor` 与菜单的 Checks 页)里**各有一份逐字
// 拷贝**。2026-09-18 修了前者的一处措辞而后者没跟上,于是同一台机器上:
//
//	bx status →  Notice  macOS VPN service active: Tailscale
//	bx doctor →  [WARN]  macOS VPN service connected: 8B24B74E-… "Tailscale"  [VPN:…]
//
// 两个面对**同一个事实**说了两句不一样的话,而两边的测试都绿 —— 因为两边各测各的
// 那一份。判定只有一份之后,这种分歧在构造上不可能再出现。
package macnetprobe

import (
	"regexp"
	"strings"
)

var (
	networkServiceLineRe = regexp.MustCompile(`^\*\s+\((Connected|Connecting)\)\s+(.+)$`)
	serviceDisplayNameRe = regexp.MustCompile(`"([^"]+)"`)
	tailscaleRouteRe     = regexp.MustCompile(`(?m)^\s*(100\.64(?:\.0\.0)?/10|100\.100\.100\.100)\s+`)
)

// ConnectedNetworkService 从 `scutil --nc list` 里取出正在连接的那个服务的**显示名**。
//
// **不是整行。** scutil 那一行的尾巴是给列对齐用的:UUID、括号里的 bundle id、
// 一长串填充空格,最后把同一个 bundle id 再印一遍。而这句话是常驻的(`bx status`、
// `bx doctor`、菜单三处都显示它),用户要的只有引号里那个显示名。
//
// 认不出显示名**绝不返回空串** —— 那会让「另一个 VPN 正开着」这条告警整个消失,
// 而它正是这个函数存在的理由。退路是原文,只压掉多余空白。
func ConnectedNetworkService(scutilNCListOut string) string {
	for _, line := range strings.Split(scutilNCListOut, "\n") {
		line = strings.TrimSpace(line)
		matches := networkServiceLineRe.FindStringSubmatch(line)
		if len(matches) != 3 {
			continue
		}
		tail := strings.TrimSpace(matches[2])
		if name := serviceDisplayNameRe.FindStringSubmatch(tail); len(name) == 2 {
			return name[1]
		}
		return strings.Join(strings.Fields(tail), " ")
	}
	return ""
}

// SystemProxyEnabled 判 `scutil --proxy` 里系统代理是否开着。
func SystemProxyEnabled(scutilProxyOut string) bool {
	for _, line := range strings.Split(scutilProxyOut, "\n") {
		line = strings.TrimSpace(line)
		if line == "HTTPEnable : 1" || line == "HTTPSEnable : 1" || line == "SOCKSEnable : 1" {
			return true
		}
	}
	return false
}

// HasTailscaleOverlayRoute 判 `netstat -rn` 里 tailscale 的 overlay 路由在不在。
func HasTailscaleOverlayRoute(netstatOut string) bool {
	return tailscaleRouteRe.MatchString(netstatOut)
}
