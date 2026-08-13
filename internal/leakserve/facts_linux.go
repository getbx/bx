//go:build linux

package leakserve

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/supervisor"
)

// LiveFactDeps 是 Linux 上的本机事实采集。
//
// **此前这里是空的**(`facts_other.go` 返回零值 FactDeps),于是 `bx leakcheck` 在
// Linux 上把每一条本机结论都报成 not checked —— 而 Linux 是 bx 的第一平台。
// 那份「空是诚实的」注释本身没错,错的是它一直没被填上。
//
// **能力与 darwin 的差别写在这里,而不是靠调用方猜:**
//   - Guardian 只在 darwin 上跑,所以保护状态问不到;bx 的 TUN 名直接问 Core 的
//     控制 socket(与 darwin 那条第二跳同源)。
//   - 系统集成 VPN 列表(scutil --nc)没有 Linux 对应物 —— 留空,而不是编一个。
func LiveFactDeps() FactDeps {
	return FactDeps{
		LookupRoute:  linuxRouteLookup,
		InspectDNS:   linuxResolvers,
		ListRoutes:   listLinuxRoutes,
		ListOverlays: listOverlayTenants,
		GuardianStatus: func(context.Context) (BXRuntimeFacts, error) {
			state, err := supervisor.FetchRuntimeState(supervisor.SockPath)
			if err != nil {
				// 拨不通基本等于 Core 没在跑 —— 这是这个功能的主场景之一
				// (bx 关着、拿它检测别的 VPN),不是错误。
				return BXRuntimeFacts{}, err
			}
			return BXRuntimeFacts{TunInterface: state.TunName}, nil
		},
	}
}

// linuxRouteLookup 问「发往 dest 的包交给哪个接口」。
//
// **必须用 `ip route get`,不能读 main 表。** bx 在 Linux 上靠 ip rule 策略路由分流
// (table 100 + pref 200),而 `ip route show` 默认只列 main 表 —— 读它会在 bx 正常
// 工作时报出物理网卡,把一台受保护的机器说成没受保护。
func linuxRouteLookup(parent context.Context, dest string, ipv6 bool) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	args := []string{"route", "get", dest}
	if ipv6 {
		args = append([]string{"-6"}, args...)
	}
	out, err := exec.CommandContext(ctx, "ip", args...).CombinedOutput()
	if err != nil {
		if isNoRouteOutput(string(out)) {
			// **确知没有这条路由**,与「问不出来」必须分开:IPv6 那条规则据此才敢
			// 给出一句诚实的 ok。
			return "", ErrNoRoute
		}
		return "", err
	}
	if iface := parseLinuxRouteGetInterface(string(out)); iface != "" {
		return iface, nil
	}
	if isNoRouteOutput(string(out)) {
		return "", ErrNoRoute
	}
	return "", errors.New("ip route get returned no device")
}

func isNoRouteOutput(out string) bool {
	lowered := strings.ToLower(out)
	return strings.Contains(lowered, "network is unreachable") ||
		strings.Contains(lowered, "no route to host")
}

// linuxResolvers 读 /etc/resolv.conf。**纯文件读,不 exec** —— 少一次 fork,
// 也少一条会被 busybox/OpenWrt 的命令差异搞坏的路径。
//
// 已知边界:systemd-resolved 的机器上这里通常是 127.0.0.53(它的 stub),
// 那是**如实**的答案 —— 应用确实在问它;它再往哪转发,是另一个问题,
// 而这个功能不假装知道。
func linuxResolvers(context.Context) ([]string, error) {
	raw, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil, err
	}
	var servers []string
	for _, line := range splitLines(string(raw)) {
		fields := fieldsOf(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			servers = append(servers, fields[1])
		}
	}
	if len(servers) == 0 {
		return nil, errors.New("/etc/resolv.conf lists no nameserver")
	}
	return servers, nil
}

// listLinuxRoutes 读两个地址族的**全部**路由表。
//
// **必须是 `table all`。** bx 的劫持路由住在 table 100,而 `ip route show` 默认只列
// main —— 只读 main 就看不到 bx 自己,也看不到别人塞进别的表里的东西。
func listLinuxRoutes(parent context.Context) ([]leakcheck.RouteEntry, error) {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()

	var entries []leakcheck.RouteEntry
	var firstErr error
	for _, family := range routeFamilies {
		args := []string{"route", "show", "table", "all"}
		if family.flag == "inet6" {
			args = append([]string{"-6"}, args...)
		}
		out, err := exec.CommandContext(ctx, "ip", args...).Output()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		entries = append(entries, parseLinuxRoutes(string(out), family.deflt)...)
	}
	if len(entries) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		// 一台开着机的 Linux 不可能没有路由。空表意味着没读懂输出,
		// 而「读不懂」必须报错 —— 报成「没发现逃逸路由」正是恒绿。
		return nil, errors.New("ip route returned no routes this parser understood")
	}
	return entries, nil
}
