//go:build windows

package leakserve

import (
	"context"
	"errors"
	"net"
	"net/netip"

	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/supervisor"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// LiveFactDeps 是 Windows 上的本机事实采集。
//
// **全程用 API,一个命令输出都不解析。** `route print` / `ipconfig` 的文本格式随版本
// 与语言环境而变(中文 Windows 的表头就不是英文),而本轮的做法是「判据按真实输出
// 定,不凭记忆」—— 手上没有 Windows 机器可以取样,那就不写只能靠猜的解析器。
// GetIPForwardTable2 / GetAdaptersAddresses 返回的是带类型的结构,猜不错。
//
// **真机未验**:以下全部只经交叉编译与纯逻辑单测,没有人在 Windows 上跑过。
func LiveFactDeps() FactDeps {
	return FactDeps{
		LookupRoute: func(ctx context.Context, dest string, ipv6 bool) (string, error) {
			selection, err := supervisor.LookupRoute(ctx, dest, ipv6)
			if err != nil {
				if errors.Is(err, supervisor.ErrNoRouteToDestination) {
					return "", ErrNoRoute
				}
				return "", err
			}
			if selection.Interface == "" {
				return "", errors.New("route lookup returned no interface")
			}
			return selection.Interface, nil
		},
		InspectDNS:         windowsResolvers,
		ListRoutes:         listWindowsRoutes,
		ListInterfaceKinds: listInterfaceKinds,
		ListOverlays:       listOverlayTenants,
		GuardianStatus: func(context.Context) (BXRuntimeFacts, error) {
			state, err := supervisor.FetchRuntimeState(supervisor.SockPath)
			if err != nil {
				return BXRuntimeFacts{}, err
			}
			return BXRuntimeFacts{TunInterface: state.TunName}, nil
		},
	}
}

// windowsResolvers 列系统解析器。
//
// 只取**已启用**的适配器(OperStatusUp):关掉的网卡上留着的 DNS 不是「现在谁在
// 解析」,把它们报出来会让用户去查一个根本没在用的地址。
func windowsResolvers(context.Context) ([]string, error) {
	adapters, err := winipcfg.GetAdaptersAddresses(windows.AF_UNSPEC, winipcfg.GAAFlagIncludeGateways)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var servers []string
	for _, adapter := range adapters {
		if adapter.OperStatus != winipcfg.IfOperStatusUp {
			continue
		}
		for dns := adapter.FirstDNSServerAddress; dns != nil; dns = dns.Next {
			ip := dns.Address.IP()
			if ip == nil {
				continue
			}
			addr, ok := netip.AddrFromSlice(ip)
			if !ok {
				continue
			}
			s := addr.Unmap().String()
			if !seen[s] {
				seen[s] = true
				servers = append(servers, s)
			}
		}
	}
	if len(servers) == 0 {
		return nil, errors.New("no DNS servers reported by GetAdaptersAddresses")
	}
	return servers, nil
}

// listWindowsRoutes 读两个地址族的转发表。
//
// 路由行给的是 InterfaceLUID,而判据层用的是 Go 的接口名(与 listInterfaceKinds、
// WhoOwnsTheRoute 同一套)。**必须映射过去** —— 两套命名混用,「这条路由属于 bx 的
// TUN 吗」就永远比不上。
func listWindowsRoutes(context.Context) ([]leakcheck.RouteEntry, error) {
	names := luidToInterfaceName()
	var entries []leakcheck.RouteEntry
	var firstErr error
	for _, family := range routeFamilies {
		af := windows.AF_INET
		if family.flag == "inet6" {
			af = windows.AF_INET6
		}
		rows, err := winipcfg.GetIPForwardTable2(winipcfg.AddressFamily(af))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for i := range rows {
			row := &rows[i]
			prefix := row.DestinationPrefix.Prefix()
			if !prefix.IsValid() {
				continue
			}
			name := names[row.InterfaceLUID]
			if name == "" {
				// LUID 映射不回接口名就跳过 —— **不编一个名字**,那会让判据把它
				// 与 bx 自己的 TUN 比错。
				continue
			}
			entries = append(entries, leakcheck.RouteEntry{
				Destination: prefix.String(),
				Interface:   name,
				// **Windows 上没有 reject 路由这个形状**:bx 的 v6 fail-closed 是把
				// ::/1+8000::/1 劫进 TUN(见 windows_routes.go),不是装 unreachable。
				// 合成一个假的 Blocking 才是撒谎。
			})
		}
	}
	if len(entries) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, errors.New("GetIPForwardTable2 returned no usable routes")
	}
	return entries, nil
}

// luidToInterfaceName 把 Windows 的 LUID 映射到 Go 用的接口名。
//
// 经 MibIfRow2.InterfaceIndex 中转:Go 的 net.InterfaceByIndex 用的正是这个 index,
// 于是路由表与 net.Interfaces() 说的是同一套名字。
func luidToInterfaceName() map[winipcfg.LUID]string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	byIndex := make(map[int]string, len(ifaces))
	for _, iface := range ifaces {
		byIndex[iface.Index] = iface.Name
	}
	rows, err := winipcfg.GetIfTable2Ex(winipcfg.MibIfEntryNormal)
	if err != nil {
		return nil
	}
	names := make(map[winipcfg.LUID]string, len(rows))
	for i := range rows {
		if name, ok := byIndex[int(rows[i].InterfaceIndex)]; ok {
			names[rows[i].InterfaceLUID] = name
		}
	}
	return names
}
