//go:build darwin

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"

	"github.com/getbx/bx/internal/appattr"
	"github.com/getbx/bx/internal/stats"
	"golang.org/x/sys/unix"
)

// darwinStrayConnectionWarning 点名那些**绕过 bx、从物理网卡以真实 IP 收发的连接**
// 的应用(判据 appattr.StrayConnections)。
//
// 2026-09-25 真机:`bx down` 之后 14 秒又 `bx up`,Chrome 与 Steam 在那 14 秒里开的连接,
// 36 分钟后、中间又经过一次完整的 Guardian 切换,仍在 en0 上以真实 IP 收发 —— macOS
// 不会把一条已建立的连接挪进 TUN。bx 的保护全部建立在路由上,这一类连接它此前从来
// 看不见。**被动观测**(读内核的 socket 表,不发任何包),只在真有这种连接时出现,
// 连接一关就消失 —— 是事件,不是墙纸。
//
// 取数据失败一律不报(返回空):这是一条附加的告警,问不出来时不许编一句「有泄漏」,
// 也不许让别的告警跟着消失。
func darwinStrayConnectionWarning(ctx context.Context) stats.Warning {
	_, device, err := PhysicalDefaultRoute(ctx)
	if err != nil || device == "" {
		return stats.Warning{}
	}
	physical := interfaceIPv4s(device)
	if len(physical) == 0 {
		return stats.Warning{}
	}
	var pcbs []appattr.PCB
	for _, tbl := range pcbTables {
		raw, err := unix.SysctlRaw(tbl.mib)
		if err != nil {
			return stats.Warning{}
		}
		parsed, err := appattr.ParsePcbList(raw)
		if err != nil {
			return stats.Warning{}
		}
		pcbs = append(pcbs, parsed...)
	}
	// 主人已经退出的 socket(TIME_WAIT / CLOSE_WAIT 里的残骸)不承载任何应用的流量,
	// 不点名。真机 2026-09-25:v0.4.9 升级换掉旧 Core 之后,它的隧道子进程留下的
	// 几十个收尾中的 socket 被报成了「PID 1913 绕过 bx」,20 秒后自己消失。
	skip := func(pid int32) bool { return isOwnProcess(pid) || !processAlive(pid) }
	stray := appattr.StrayConnections(pcbs, physical, skip)
	return strayConnectionWarning(device, stray, func(pid int32) string {
		return appattr.DisplayName(executablePathOf(pid))
	})
}

// strayConnectionWarning 是纯渲染:名字去重排序、说清几条、从哪块网卡、怎么办。
func strayConnectionWarning(device string, stray []appattr.PCB, name func(int32) string) stats.Warning {
	if len(stray) == 0 {
		return stats.Warning{}
	}
	seen := map[string]bool{}
	var names []string
	for _, p := range stray {
		n := name(p.LastPID)
		if n == "" {
			n = fmt.Sprintf("PID %d", p.LastPID)
		}
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	apps := strings.Join(names, ", ")
	return stats.Warning{
		Name:     stats.WarningConnectionsBypassingBX,
		Severity: "error",
		Detail: fmt.Sprintf("%d connection(s) from %s leave through %s with your real IP, outside bx "+
			"(most likely opened while protection was off; macOS never moves an open connection into the tunnel)",
			len(stray), apps, device),
		Hint: "quit and reopen " + apps + "; their new connections go through bx",
		Apps: names,
	}
}

func interfaceIPv4s(name string) []netip.Addr {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range addrs {
		if prefix, err := netip.ParsePrefix(a.String()); err == nil && prefix.Addr().Is4() {
			out = append(out, prefix.Addr())
		}
	}
	return out
}

// processAlive:kill(pid, 0) 只有明确 ESRCH 才算不在(与 OwnersByPort 的活性判据同源)。
func processAlive(pid int32) bool {
	if pid <= 0 {
		return false
	}
	return !errors.Is(unix.Kill(int(pid), 0), unix.ESRCH)
}

// isOwnProcess:bx 自己(Core)或它直接起的子进程(sing-box / brook 隧道)。隧道到服务器的
// 连接正是从物理网卡出去的,那是设计。
func isOwnProcess(pid int32) bool {
	self := int32(os.Getpid())
	if pid == self {
		return true
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", int(pid))
	if err != nil {
		return false
	}
	return info.Eproc.Ppid == self
}
