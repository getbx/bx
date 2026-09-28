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
	"time"

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
//
// routedAround 是 bx 自己绕开隧道的网段(服务器旁路 + 用户 bypass):发往那里的连接
// 从物理网卡出去是 bx 安排的,不点名(真机 2026-09-28:ssh 跳板连 bx 自己的 VPS)。
// tracker 记每条连接第一次被看见的时刻,把「刚开始退场」与「顽固」分开(两段式)。
func darwinStrayWarnings(ctx context.Context, routedAround func() []netip.Prefix, tracker *strayTracker) []stats.Warning {
	_, device, err := PhysicalDefaultRoute(ctx)
	if err != nil || device == "" {
		return nil
	}
	pcbs, physical := darwinStraySnapshot(device)
	if len(physical) == 0 || pcbs == nil {
		return nil
	}
	in := strayInputs{
		device:   device,
		physical: physical,
		// 主人已经退出的 socket(TIME_WAIT / CLOSE_WAIT 里的残骸)不承载任何应用的流量,
		// 不点名。真机 2026-09-25:v0.4.9 升级换掉旧 Core 之后,它的隧道子进程留下的
		// 几十个收尾中的 socket 被报成了「PID 1913 绕过 bx」,20 秒后自己消失。
		skip:         func(pid int32) bool { return isOwnProcess(pid) || !processAlive(pid) },
		routedAround: routedAround,
		name:         func(pid int32) string { return appattr.DisplayName(executablePathOf(pid)) },
		tracker:      tracker,
	}
	return strayWarningsFrom(in, pcbs, time.Now())
}

// darwinStraySnapshot 读物理网卡的地址与内核的 socket 表(取数据那一半;网络守卫的
// 告警与 pf 重置的观测共用)。读不到就返回 nil pcbs —— 问不出来不许编。
func darwinStraySnapshot(device string) (pcbs []appattr.PCB, physical []netip.Addr) {
	physical = interfaceIPv4s(device)
	if len(physical) == 0 {
		return nil, nil
	}
	for _, tbl := range pcbTables {
		raw, err := unix.SysctlRaw(tbl.mib)
		if err != nil {
			return nil, physical
		}
		parsed, err := appattr.ParsePcbList(raw)
		if err != nil {
			return nil, physical
		}
		pcbs = append(pcbs, parsed...)
	}
	if pcbs == nil {
		pcbs = []appattr.PCB{}
	}
	return pcbs, physical
}

// strayInputs 是取完数据之后那一半的全部输入。做成结构体是因为它有六项,位置参数
// 里两个同类型的函数值换位不会有任何编译错误。
type strayInputs struct {
	device       string
	physical     []netip.Addr
	skip         func(pid int32) bool
	routedAround func() []netip.Prefix
	name         func(pid int32) string
	tracker      *strayTracker
}

// strayWarningsFrom 是取完数据之后的那一半(判据 + 分组 + 渲染),抽出来只为让
// 「bx 绕开的网段真的递到了判据手上」「两组真的分开了」可测 —— 上面那半要读
// sysctl,测不了。
func strayWarningsFrom(in strayInputs, pcbs []appattr.PCB, now time.Time) []stats.Warning {
	var outside []netip.Prefix
	if in.routedAround != nil {
		outside = in.routedAround()
	}
	stray := appattr.StrayConnections(pcbs, in.physical, in.skip, outside)
	settling, stubborn := in.tracker.classify(now, stray)
	var out []stats.Warning
	if w := strayConnectionWarning(in.device, stubborn, in.name); w.Name != "" {
		out = append(out, w)
	}
	if w := settlingConnectionWarning(in.device, len(settling)); w.Name != "" {
		out = append(out, w)
	}
	return out
}

// settlingConnectionWarning 只报数。**不点名、不叫人重开**:这些多半几分钟内自己就
// 没了,点名等于每次 `bx up` 都叫用户重开一遍 Chrome;warn 级,CLI 不把总状态降成
// Needs Attention,菜单不裂图标。数字随刷新变小,本身就是「在好转」的信号。
func settlingConnectionWarning(device string, count int) stats.Warning {
	if count <= 0 {
		return stats.Warning{}
	}
	return stats.Warning{
		Name:     stats.WarningConnectionsSettling,
		Severity: "warn",
		Detail: fmt.Sprintf("%d connection(s) opened before protection was on are still leaving through %s with your real IP; "+
			"they move into bx as their apps reconnect (usually within minutes)", count, device),
		Count: count,
	}
}

// strayConnectionWarning 是顽固那一组的纯渲染:名字去重排序、说清几条、从哪块网卡、
// 怎么办。走到这里的连接已经在保护开着之后活过了 strayStubbornAfter,重开才是出路。
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
		Detail: fmt.Sprintf("%d connection(s) from %s have been leaving through %s with your real IP for over %d minutes, outside bx "+
			"(opened while protection was off; macOS never moves an open connection into the tunnel)",
			len(stray), apps, device, int(strayStubbornAfter/time.Minute)),
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
