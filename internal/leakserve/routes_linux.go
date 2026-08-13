//go:build linux

package leakserve

import "github.com/getbx/bx/internal/leakcheck"

// linuxRouteTypes 是 `ip route` 行首可能出现的路由类型关键字。
//
// **不是所有类型都是转发路由。** `local`/`broadcast`/`anycast`/`multicast` 是内核
// 自己那张 local 表里的东西(`ip route show table all` 会把它们一并列出来),
// 它们描述的是「这个地址属于本机」,不是「发往这里的包交给谁」。带进来只会把证据区
// 冲掉,而它们从不参与竞争。
var linuxLocalRouteTypes = map[string]bool{
	"local": true, "broadcast": true, "anycast": true, "multicast": true,
}

// linuxBlockingRouteTypes 是把流量**扔掉**的类型 —— 对应 darwin 的 R/B 标志。
// bx 自己的 v6 fail-closed 屏障就是 `unreachable default`。
var linuxBlockingRouteTypes = map[string]bool{
	"unreachable": true, "blackhole": true, "prohibit": true, "throw": true,
}

// parseLinuxRoutes 解析 `ip route show table all` 的输出。
//
// 形状与 darwin 的 netstat 完全不同(真实输出,2026-08-12 容器实测):
//
//	0.0.0.0/1 dev lo table 100 scope link
//	default via 172.17.0.1 dev eth0
//	local 127.0.0.0/8 dev lo table local scope host  src 127.0.0.1
//	unreachable default dev lo table 100
//
// 目的地是第一个 token(行首是类型关键字时则是第二个),接口跟在 `dev` 之后;
// **没有 flags 列** —— 阻断与否由类型关键字表达,故在这里翻译成 RouteEntry.Blocking。
//
// `default` 的含义随地址族而变,由调用方按读的是哪张表传进来 —— 只有读表的人知道。
func parseLinuxRoutes(out, defaultDest string) []leakcheck.RouteEntry {
	var entries []leakcheck.RouteEntry
	for _, line := range splitLines(out) {
		fields := fieldsOf(line)
		if len(fields) < 3 {
			continue
		}
		blocking := false
		if linuxLocalRouteTypes[fields[0]] {
			continue
		}
		if linuxBlockingRouteTypes[fields[0]] {
			blocking = true
			fields = fields[1:]
		}
		dest := fields[0]
		if dest == "default" {
			dest = defaultDest
		}
		iface := ""
		onLink := true
		for i := 0; i+1 < len(fields); i++ {
			switch fields[i] {
			case "dev":
				if iface == "" {
					iface = fields[i+1]
				}
			case "via":
				// 有下一跳就不是直连 —— 而注入攻击(DHCP option 121)装的正是带
				// 下一跳的路由。没有 via 的那些是「这段挂在这条线上」。
				onLink = false
			}
		}
		if iface == "" {
			// 没有 dev 的路由(比如 nexthop 组)对「谁在收走流量」答不了 —— 跳过,
			// 而不是编一个接口名。
			continue
		}
		entries = append(entries, leakcheck.RouteEntry{
			Destination: dest, Interface: iface, Blocking: blocking, OnLink: onLink,
			// Linux 没有 RTF_IFSCOPE 这个概念 —— **不合成一个假的**。
		})
	}
	return entries
}

// parseLinuxRouteGetInterface 从 `ip route get <dst>` 取出口接口。
//
// 真实输出:`1.1.1.1 dev lo  src 198.51.100.1`。**它是「谁在管这条路」唯一权威的
// 答案** —— 它经过策略路由,而 bx 在 Linux 上正是靠 ip rule 分流的,读 main 表
// 会得出完全错误的结论。
func parseLinuxRouteGetInterface(out string) string {
	for _, line := range splitLines(out) {
		fields := fieldsOf(line)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "dev" {
				return fields[i+1]
			}
		}
	}
	return ""
}
