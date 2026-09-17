// Package pathview 是 `bx explain` 的本机视角:「这个目标在这台机器上会怎么走」。
//
// **纯判据,无 I/O。** 输入是 cli 那一侧收集好的事实(解析结果、内核路由、绑网卡
// 时的路由、bx 的 TUN 与旁路),输出是一句给小白的结论加几行证据。bx 在不在跑都
// 能答:bx 只是路径上的一个可能环节。
//
// 动机是同一天两次误判。另一会话看到 `8.8.8.8 → utun9` 就断定 bx 吞了 Tailscale,
// 而真相是普通进程走 bx 的 TUN、绑了网卡的进程走 en0 —— 两个视角并排摆出来,
// 那个结论当时就站不住。休眠成环那次同理:哨兵地址进了 TUN,而发往服务器的
// /32 旁路早已不在,只有指着那个具体地址问才看得见。
//
// 与 observe 的分工:observe 是仪表盘(无参、固定几项、只报异常),这里是听诊器
// (你指哪它听哪)。两者共用同一批原语,不是第二套判据。
package pathview

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/tristate"
)

// RouteFact 是一次「发往这个地址的包走哪」的答案。
type RouteFact struct {
	// Applicable 为假 = 这个平台/这台机器上根本没问(不是问了没答案)。
	Applicable bool
	Interface  string
	Gateway    string
	// Reject = 命中阻断/不可达路由(bx 的 fail-closed 屏障、v6 阻断都是这个形状)。
	Reject bool
	// Missing = 问了,表里没有这条(scoped 表里 `not in table`)。
	Missing bool
	// Err 非空 = 问不出来(命令失败、超时)。**不是「没有路由」。**
	Err string
}

// Facts 是判据的全部输入。
type Facts struct {
	Target    string
	LiteralIP bool
	// Addrs 是本机系统解析器给的答案(字面量 IP 时就是它自己)。
	Addrs      []netip.Addr
	ResolveErr string
	// FakeIP 是 bx 的假 IP 段;解析落在里面 = DNS 归 bx。
	FakeIP netip.Prefix
	// Route 是普通 socket 看到的路由(不绑接口);Bound 是绑在物理网卡上的 socket
	// 看到的(macOS 的 IP_BOUND_IF 只查该接口的 scoped 表;Tailscale、部分 VPN、
	// bx 自己的直连器都属于这一类)。
	Route RouteFact
	Bound RouteFact
	// PhysicalDev 是默认路由所在的物理网卡(en0 / eno1),空 = 问不出来。
	PhysicalDev string
	// BxTun 是 bx 的 TUN 名;BxTunKnown 说的是「问出来了」,不是「有」。
	BxTun       string
	BxTunKnown  bool
	CoreRunning bool
	// ServerBypass 是 bx 给传输服务器装的旁路(经物理网关,不进 TUN)。
	ServerBypass []netip.Prefix
	// China 判一个地址在不在内建国内段;nil = 没有列表可查。
	China func(netip.Addr) bool
}

// Line 是一行证据。
type Line struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}

// View 是给人看的结果:先一句结论,再几行证据。Kind 是目标的类型,给机器读。
type View struct {
	Conclusion string `json:"conclusion"`
	Kind       string `json:"kind"`
	Lines      []Line `json:"lines"`
	// EntersBx:普通程序发往这个目标的包会不会进 bx。
	//
	// 它存在的理由是 explain 的**下半截**(Core 的分流判定)回答的是
	// 「**如果**这条连接进了 bx,bx 会怎么判」。私网 / 旁路 / 别人的隧道那些
	// 目的地的包根本到不了 TUN,于是那半截描述的事永远不会发生 —— 而两半
	// 并排摆着、措辞都很肯定,用户没有义务知道该信哪一个。
	//
	// **三态,零值 Unknown。** 认不出接口时绝不许塌成任一边:说 True 会把一条
	// 其实没进 bx 的流量说成 bx 在管,说 False 会让用户忽略一条真正生效的判定。
	EntersBx tristate.Tristate `json:"enters_bx"`
}

// 目标类型。
const (
	KindFakeIP       = "fake_ip"
	KindLoopback     = "loopback"
	KindPrivate      = "private"
	KindCGNAT        = "cgnat"
	KindLinkLocal    = "link_local"
	KindServerBypass = "server_bypass"
	KindChina        = "china"
	KindPublic       = "public"
	KindUnresolved   = "unresolved"
)

var (
	cgnat     = netip.MustParsePrefix("100.64.0.0/10")
	linkLocal = netip.MustParsePrefix("169.254.0.0/16")
)

// Judge 是判据的入口。
func Judge(f Facts) View {
	var lines []Line
	addr, resolved := firstAddr(f)
	lines = append(lines, resolutionLine(f, addr, resolved))
	kind := classify(f, addr, resolved)
	lines = append(lines, Line{Label: "Kind", Text: kindText(kind)})
	lines = append(lines, Line{Label: "Route", Text: routeText(f.Route, f)})
	if f.Bound.Applicable {
		lines = append(lines, Line{Label: "NIC-bound", Text: routeText(f.Bound, f)})
	}
	return View{Conclusion: conclusion(f, kind, resolved), Kind: kind, Lines: lines, EntersBx: entersBx(f)}
}

func firstAddr(f Facts) (netip.Addr, bool) {
	for _, a := range f.Addrs {
		if a.IsValid() {
			return a.Unmap(), true
		}
	}
	return netip.Addr{}, false
}

func resolutionLine(f Facts, addr netip.Addr, resolved bool) Line {
	switch {
	case f.LiteralIP:
		return Line{Label: "Resolves", Text: "it is an IP, no resolution needed"}
	case !resolved && f.ResolveErr != "":
		return Line{Label: "Resolves", Text: "failed: " + f.ResolveErr}
	case !resolved:
		return Line{Label: "Resolves", Text: "no address"}
	case f.FakeIP.IsValid() && f.FakeIP.Contains(addr):
		return Line{Label: "Resolves", Text: fmt.Sprintf("resolved locally to fake IP %s — DNS belongs to bx, which looks the real address back up when it dials", addr)}
	default:
		// 只说事实:真 IP。**不说「DNS 没归 bx 管」** —— bx 接管着 DNS 时也会对
		// fakeip_filter / hosts 里的名字直接答真 IP(真机 2026-09-05 derphome 那次),
		// 这一层分不出是哪种,断言归属就是编。
		return Line{Label: "Resolves", Text: fmt.Sprintf("resolved locally to %s (a real IP, not one of bx's fake IPs)", addr)}
	}
}

func classify(f Facts, addr netip.Addr, resolved bool) string {
	if !resolved {
		return KindUnresolved
	}
	switch {
	case f.FakeIP.IsValid() && f.FakeIP.Contains(addr):
		return KindFakeIP
	case addr.IsLoopback():
		return KindLoopback
	case cgnat.Contains(addr):
		return KindCGNAT
	case linkLocal.Contains(addr) || addr.IsLinkLocalUnicast():
		return KindLinkLocal
	case addr.IsPrivate():
		return KindPrivate
	}
	for _, p := range f.ServerBypass {
		if p.Contains(addr) {
			return KindServerBypass
		}
	}
	if f.China != nil && f.China(addr) {
		return KindChina
	}
	return KindPublic
}

func kindText(kind string) string {
	switch kind {
	case KindFakeIP:
		return "one of bx's fake IPs (the real destination is whatever bx decides)"
	case KindLoopback:
		return "loopback"
	case KindPrivate:
		return "private network (bx goes direct in every mode)"
	case KindCGNAT:
		return "CGNAT range 100.64/10 (overlays like Tailscale use it; bx always goes direct and leaves it to them)"
	case KindLinkLocal:
		return "link-local"
	case KindServerBypass:
		return "bx's transport-server bypass (via the physical gateway, never into the TUN)"
	case KindChina:
		return "public, inside the built-in China ranges"
	case KindPublic:
		return "public, outside the China ranges"
	default:
		return "does not resolve, so it cannot be classified"
	}
}

func routeText(r RouteFact, f Facts) string {
	switch {
	case !r.Applicable:
		return "not asked on this platform"
	case r.Err != "":
		return "could not find out: " + r.Err
	case r.Reject:
		return "a blocking route (reject/unreachable)"
	case r.Missing:
		return "no such route in the table"
	case r.Interface == "":
		return "the kernel named no interface"
	}
	text := r.Interface
	if r.Gateway != "" {
		text += " via " + r.Gateway
	}
	if owner := ownerOf(r.Interface, f); owner != "" {
		text += " (" + owner + ")"
	}
	return text
}

// 接口归属,白名单式:认得出是 bx / 别的隧道 / 物理网卡才说,认不出就不说。
const (
	ownerBx       = "bx's TUN"
	ownerTunnel   = "another tunnel, not bx"
	ownerPhysical = "physical NIC"
)

func ownerOf(iface string, f Facts) string {
	iface = strings.TrimSpace(iface)
	switch {
	case iface == "":
		return ""
	case f.BxTunKnown && f.BxTun != "" && iface == f.BxTun:
		return ownerBx
	case leakcheck.IsTunnelInterface(iface):
		return ownerTunnel
	case f.PhysicalDev != "" && iface == f.PhysicalDev:
		return ownerPhysical
	}
	return ""
}

// conclusion 是给小白的那一句。规则按优先级:问不出 → 阻断 → 按接口归属;
// 绑网卡的视角与普通视角不同时,补一句。
func conclusion(f Facts, kind string, resolved bool) string {
	if !resolved {
		if f.ResolveErr != "" {
			return "This name does not resolve on this machine (" + f.ResolveErr + "), so no packet can even leave."
		}
		return "This name resolved to no address at all, so no packet can even leave."
	}
	r := f.Route
	var main string
	switch {
	case !r.Applicable:
		main = "This platform cannot be asked about kernel routes, so only bx's own verdict is below."
	case r.Err != "":
		main = "Could not find out where the kernel would send it (" + r.Err + "), so only bx's half is below."
	case r.Reject:
		main = "A blocking route in the kernel stops it: packets do not get out. bx's fail-closed barrier and its v6 block both look like this."
	case r.Interface == "":
		main = "The kernel did not say which interface this packet takes, and bx will not guess."
	default:
		switch ownerOf(r.Interface, f) {
		case ownerBx:
			if f.CoreRunning {
				main = "An ordinary program reaching it goes into bx; where it ends up is bx's decision (below)."
			} else {
				main = "An ordinary program reaching it goes into bx's TUN, but bx is not running: the packets enter a door nobody is behind."
			}
		case ownerTunnel:
			main = fmt.Sprintf("An ordinary program reaching it goes into another tunnel (%s), not through bx.", r.Interface)
		case ownerPhysical:
			main = fmt.Sprintf("An ordinary program reaching it leaves straight from %s, not through bx, with your real IP as the source%s.", r.Interface, physicalReason(f, kind))
		default:
			main = fmt.Sprintf("An ordinary program reaching it goes over %s, and bx cannot tell what that interface is.", r.Interface)
		}
	}
	return main + boundClause(f, kind)
}

func physicalReason(f Facts, kind string) string {
	switch kind {
	case KindServerBypass:
		return " — it is in bx's server bypass, which is how it should be"
	case KindPrivate:
		return " — a private network, which is how it should be"
	case KindCGNAT:
		return " — the CGNAT range, which is how it should be"
	case KindLoopback, KindLinkLocal:
		return ""
	}
	if !f.CoreRunning {
		return " (bx is not running)"
	}
	return ""
}

// boundClause 说绑了网卡的程序会怎样 —— 只在它与普通视角**不同**时说。
func boundClause(f Facts, kind string) string {
	b := f.Bound
	if !b.Applicable || f.Route.Err != "" || !f.Route.Applicable {
		return ""
	}
	switch kind {
	case KindCGNAT, KindLoopback, KindLinkLocal:
		// overlay 自己的段 / 本机:底层绑网卡那一句对它们是噪声。
		return ""
	case KindFakeIP:
		// 绑了网卡的程序拿到的是假 IP,从物理网卡发出去石沉大海 —— 2026-09-03
		// Tailscale 自建 DERP 域名那次(dial 198.18.0.40 超时)就是这个形状。
		return " A NIC-bound program (Tailscale, say) gets this fake IP, and sending it out the physical NIC goes nowhere: " +
			"put the domain it uses into dns.fakeip_filter or hosts, or give it the IP directly."
	}
	switch {
	case b.Err != "":
		return " What a NIC-bound program (Tailscale, say) would do could not be determined: " + b.Err
	case b.Missing:
		return " A NIC-bound program (Tailscale, say) cannot reach it: the scoped route table has no route for it."
	case b.Reject:
		return " A NIC-bound program (Tailscale, say) is stopped by the blocking route too."
	case b.Interface == "" || b.Interface == f.Route.Interface:
		return ""
	}
	if ownerOf(b.Interface, f) == ownerPhysical {
		return fmt.Sprintf(" A NIC-bound program (Tailscale, say) leaves straight from %s with your real IP as the source.", b.Interface)
	}
	return fmt.Sprintf(" A NIC-bound program (Tailscale, say) goes over %s.", b.Interface)
}

// ChinaSetFromList 把内建的 china_cidr4.txt 变成判据函数;解析失败返回 nil
// (「没有列表」是合法答案,判成公网不是)。
func ChinaSetFromList(lines []string) func(netip.Addr) bool {
	set, err := route.NewCIDRSet(lines)
	if err != nil || set == nil {
		return nil
	}
	return set.Contains
}

// entersBx 判「普通程序发往这个目标的包会不会进 bx」。
//
// **它刻意不是 ownerOf 的薄壳**,差别只在一处而那一处承重:ownerOf 认得出
// 「这是条隧道」就说「另一条隧道,不是 bx」,而在 **bx 自己的 TUN 名没问出来**
// 时(BxTunKnown 为假),一条 utunN 完全可能就是 bx 的 —— 这时候答 False 等于
// 告诉用户「下面那条判定走不到」,而它可能正走得到。那是这个函数最不能犯的错。
func entersBx(f Facts) tristate.Tristate {
	iface := strings.TrimSpace(f.Route.Interface)
	if iface == "" {
		return tristate.Unknown // 没问出路由,不是「没进 bx」
	}
	if f.BxTunKnown && f.BxTun != "" && iface == f.BxTun {
		return tristate.True
	}
	if f.PhysicalDev != "" && iface == f.PhysicalDev {
		return tristate.False // 物理网卡是确定的:它不是任何隧道
	}
	// 认得出是隧道、**而且**知道 bx 的 TUN 叫什么(所以知道这条不是它)才敢说 False。
	if f.BxTunKnown && f.BxTun != "" && leakcheck.IsTunnelInterface(iface) {
		return tristate.False
	}
	return tristate.Unknown
}
