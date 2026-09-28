// Package pfreset 在 `bx up` 之后把保护关着时开的连接重置一次,让应用重连进 TUN。
//
// macOS 没有按 socket 重置的原语(没有 tcpdrop、没有对应 sysctl),能做到的是让本机
// TCP 栈自己把 socket 判死:pf 的 `block return-rst out` 拦下一个出站包并向本机回 RST,
// socket 立刻收到 ECONNRESET。UDP 同法 `return-icmp`。规则只在 bx 需要的那几秒存在。
// 设计:docs/superpowers/specs/2026-09-28-bx-reset-stray-connections-design.md
package pfreset

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/getbx/bx/internal/policy"
)

// Anchor 是 bx 自己的 pf anchor。放在 com.apple/ 通配 anchor 之下,主规则集
// (/etc/pf.conf 的 `anchor "com.apple/*"`)不改也会被求值。**真机未核**。
const Anchor = "com.apple/250.bx"

// Table 是白名单表名:发往这些网段的连接是 bx 自己安排走物理网卡的,不重置。
const Table = "bx_routed_around"

// RoutedAround 把三份「bx 自己绕开隧道的网段」合成一份:私网、服务器旁路、用户
// bypass。裸 IP 补成 /32、认不出的丢掉不编、去重 —— 与 supervisor.routedAroundForGuard
// 同一份 policy.RuleCIDR,别再写一份解析。
func RoutedAround(private, serverBypass, userBypass []string) []netip.Prefix {
	seen := map[netip.Prefix]bool{}
	var out []netip.Prefix
	for _, group := range [][]string{private, serverBypass, userBypass} {
		for _, e := range group {
			p, ok := policy.RuleCIDR(e)
			if !ok || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// Rules 是装进 anchor 的全部文本。`user != root` 放过 Core、隧道子进程、tailscaled
// 这类 root 守护进程;`quick` 让这两条不被别的 anchor 覆盖;`(device)` 取网卡的当前
// 地址(热点切换时地址会变)。
func Rules(device string, routedAround []netip.Prefix) string {
	items := make([]string, 0, len(routedAround))
	for _, p := range routedAround {
		items = append(items, p.String())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "table <%s> persist { %s }\n", Table, strings.Join(items, ", "))
	fmt.Fprintf(&b, "block return-rst out quick on %s inet proto tcp from (%s) to !<%s> user != root\n", device, device, Table)
	fmt.Fprintf(&b, "block return-icmp out quick on %s inet proto udp from (%s) to !<%s> user != root\n", device, device, Table)
	return b.String()
}
