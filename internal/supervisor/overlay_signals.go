package supervisor

import (
	"context"
	"errors"
	"log"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"

	"github.com/getbx/bx/internal/overlay"
)

// detectOverlayTenants 采集检测信号并问 internal/overlay「谁在跑」。
//
// **纯 Go,不 exec**:接口名与地址由 net.Interfaces 就能拿到,不需要 ifconfig/ps。
// 少一次 fork,也少一条会被 busybox/OpenWrt 的命令行差异搞坏的路径。
//
// **它被反复调用**:启动时一次,之后每一轮网络守卫(15 秒)与每一次中继旁路重探
// 各一次。所以它自己不许每次都说话 —— 见 logOverlayChange。
//
// 已知边界:DNS split 仍然只在启动时喂给 dnsSrv 一次,所以晚到的 overlay 拿不到
// 自己的命名空间解析(由 lateTenantWarning 如实报出来)。旁路那半会自己跟上。
func detectOverlayTenants() []overlay.Tenant {
	ifaces, err := net.Interfaces()
	if err != nil {
		// 问不出来就当没有:多报一个租户的代价是给几条陌生 /32 开旁路,
		// 而这里连接口都枚举不了,任何推断都没有依据。
		log.Printf("overlay 共存:接口枚举失败,跳过租户检测:%v", err)
		return nil
	}
	signals := overlay.Signals{}
	for _, iface := range ifaces {
		signals.InterfaceNames = append(signals.InterfaceNames, iface.Name)
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if prefix, err := netip.ParsePrefix(a.String()); err == nil {
				signals.Addresses = append(signals.Addresses, prefix.Addr())
			}
		}
	}
	present := overlay.Detect(signals)
	logOverlayChange(present)
	return present
}

// lastOverlayNames 记住上一次看到的租户集合,好让日志只在**变化时**说话。
var lastOverlayNames struct {
	sync.Mutex
	names string
}

// logOverlayChange 只在租户集合变了的时候打一行。
//
// **不这样的话它一天打约 5760 行。** detectOverlayTenants 每 15 秒被网络守卫调一次
// (还不算中继旁路重探),而每次都打一遍「检测到 tailscale 在跑」——那是纯噪声,
// 会把真正值得看的行淹掉。措辞也改了:守卫那条路只观测,不「开路」。
func logOverlayChange(present []overlay.Tenant) {
	var names []string
	for _, t := range present {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	joined := strings.Join(names, ",")

	lastOverlayNames.Lock()
	defer lastOverlayNames.Unlock()
	if lastOverlayNames.names == joined {
		return
	}
	lastOverlayNames.names = joined
	if joined == "" {
		log.Printf("overlay 共存:不再检测到任何 overlay 网络")
		return
	}
	log.Printf("overlay 共存:检测到 %s", joined)
}

// overlayPresent 判断某个租户是否在这一组里。
func overlayPresent(present []overlay.Tenant, name string) bool {
	for _, t := range present {
		if t.Name == name {
			return true
		}
	}
	return false
}

// errNoTailscaleForBypass 让重试循环知道「这次没抓,不是失败」。
//
// 它走的是与真失败同一条路(保留上一份、退避重试),而那正是想要的:Tailscale
// 装上之后下一轮就会看见它,不需要另一套机制。
var errNoTailscaleForBypass = errors.New("tailscale not present on this machine")

// initialOverlayBypass 给旁路一个启动初值。
//
// **没有 Tailscale 就一条 DERP 都不装** —— 连内置兜底表也不装。那张表是十个写死的
// 公网 /32,给一台与 Tailscale 无关的机器装上它们,等于常开十条绕过隧道的路。
func initialOverlayBypass(ctx context.Context, direct *net.Dialer, present []overlay.Tenant) []string {
	tenant := overlay.BypassCIDRs(present)
	if !overlayPresent(present, "tailscale") {
		return tenant
	}
	return mergeBypassCIDRs(tailscaleBootstrapBypassCIDRs(ctx, direct), tenant)
}
