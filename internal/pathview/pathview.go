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
	lines = append(lines, Line{Label: "目标类型", Text: kindText(kind)})
	lines = append(lines, Line{Label: "本机路由", Text: routeText(f.Route, f)})
	if f.Bound.Applicable {
		lines = append(lines, Line{Label: "绑网卡时", Text: routeText(f.Bound, f)})
	}
	return View{Conclusion: conclusion(f, kind, resolved), Kind: kind, Lines: lines}
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
		return Line{Label: "解析", Text: "是 IP,不用解析"}
	case !resolved && f.ResolveErr != "":
		return Line{Label: "解析", Text: "失败:" + f.ResolveErr}
	case !resolved:
		return Line{Label: "解析", Text: "没有地址"}
	case f.FakeIP.IsValid() && f.FakeIP.Contains(addr):
		return Line{Label: "解析", Text: fmt.Sprintf("本机解析到假 IP %s —— DNS 归 bx 管,真实地址由 bx 拨号时反查", addr)}
	default:
		return Line{Label: "解析", Text: fmt.Sprintf("本机解析到 %s —— DNS 没归 bx 管", addr)}
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
		return "bx 的假 IP(真实目标要看 bx 的判定)"
	case KindLoopback:
		return "本机回环"
	case KindPrivate:
		return "私网(bx 任何模式下都直连)"
	case KindCGNAT:
		return "CGNAT 段 100.64/10(Tailscale 等 overlay 用这一段;bx 恒直连并让给它们)"
	case KindLinkLocal:
		return "链路本地"
	case KindServerBypass:
		return "bx 的传输服务器旁路(经物理网关,不进 TUN)"
	case KindChina:
		return "公网,在内建国内段"
	case KindPublic:
		return "公网,不在国内段"
	default:
		return "解析不出,无法归类"
	}
}

func routeText(r RouteFact, f Facts) string {
	switch {
	case !r.Applicable:
		return "本平台没问"
	case r.Err != "":
		return "问不出来:" + r.Err
	case r.Reject:
		return "阻断路由(reject/unreachable)"
	case r.Missing:
		return "表里没有这条路由"
	case r.Interface == "":
		return "内核没给接口"
	}
	text := r.Interface
	if r.Gateway != "" {
		text += " via " + r.Gateway
	}
	if owner := ownerOf(r.Interface, f); owner != "" {
		text += "(" + owner + ")"
	}
	return text
}

// 接口归属,白名单式:认得出是 bx / 别的隧道 / 物理网卡才说,认不出就不说。
const (
	ownerBx       = "bx 的 TUN"
	ownerTunnel   = "另一条隧道,不是 bx"
	ownerPhysical = "物理网卡"
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
			return "这个名字在本机解析不出来(" + f.ResolveErr + "),包根本发不出去。"
		}
		return "这个名字没有解析到任何地址,包根本发不出去。"
	}
	r := f.Route
	var main string
	switch {
	case !r.Applicable:
		main = "本平台问不了内核路由,只能看 bx 的判定。"
	case r.Err != "":
		main = "问不出内核会把它送去哪(" + r.Err + "),下面只有 bx 那一半。"
	case r.Reject:
		main = "内核里有一条阻断路由挡着它:包出不去。bx 的 fail-closed 屏障与 v6 阻断都是这个形状。"
	case r.Interface == "":
		main = "内核没说这个包走哪个接口,不猜。"
	default:
		switch ownerOf(r.Interface, f) {
		case ownerBx:
			if f.CoreRunning {
				main = "普通程序连它会进 bx,去向由 bx 判定(见下)。"
			} else {
				main = "普通程序连它会进 bx 的 TUN,但 bx 没在跑:包进了一个没人接的口子。"
			}
		case ownerTunnel:
			main = fmt.Sprintf("普通程序连它会进另一条隧道(%s),不经过 bx。", r.Interface)
		case ownerPhysical:
			main = fmt.Sprintf("普通程序连它会直接从 %s 出去,不经过 bx,源 IP 是你的真实 IP%s。", r.Interface, physicalReason(f, kind))
		default:
			main = fmt.Sprintf("普通程序连它会走 %s,我认不出这个接口是什么。", r.Interface)
		}
	}
	return main + boundClause(f, kind)
}

func physicalReason(f Facts, kind string) string {
	switch kind {
	case KindServerBypass:
		return "(它在 bx 的服务器旁路里,本该如此)"
	case KindPrivate:
		return "(私网,本该如此)"
	case KindCGNAT:
		return "(CGNAT 段,本该如此)"
	case KindLoopback, KindLinkLocal:
		return ""
	}
	if !f.CoreRunning {
		return "(bx 没在跑)"
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
		return " 绑了网卡的程序(如 Tailscale)拿到的是这个假 IP,从物理网卡发出去会石沉大海、连不上:" +
			"给它用的域名要进 dns.fakeip_filter 或 hosts,或者直接写 IP。"
	}
	switch {
	case b.Err != "":
		return " 绑了网卡的程序(如 Tailscale)会怎样问不出来:" + b.Err
	case b.Missing:
		return " 绑了网卡的程序(如 Tailscale)连不上它:scoped 路由表里没有这条路。"
	case b.Reject:
		return " 绑了网卡的程序(如 Tailscale)也被阻断路由挡着。"
	case b.Interface == "" || b.Interface == f.Route.Interface:
		return ""
	}
	if ownerOf(b.Interface, f) == ownerPhysical {
		return fmt.Sprintf(" 绑了网卡的程序(如 Tailscale)会从 %s 直出,源 IP 是你的真实 IP。", b.Interface)
	}
	return fmt.Sprintf(" 绑了网卡的程序(如 Tailscale)会走 %s。", b.Interface)
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
