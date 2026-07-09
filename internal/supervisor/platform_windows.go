//go:build windows

// platform_windows.go 实现 platform 接口的 Windows 部分:
//   - OpenTUN:wintun(经 wireguard tun.Device)→ wgbridge 桥接成 gVisor 端点;回填适配器 LUID。
//   - DirectDialer:IP_UNICAST_IF 绑物理网卡(防环,Windows 版 SO_MARK)。
//   - Hijack:winipcfg 用 TUN 的 LUID 配地址 + split-default(0.0.0.0/1 + 128.0.0.0/1)劫进 TUN,
//     服务器/私网/SSH bypass 经物理网关旁路;IPv6 fail-closed(::/1 + 8000::/1 劫进 TUN)。
//     纯路由计划见 windows_routes.go(可跨平台单测);本文件是 winipcfg 应用/还原层。
//
// ⚠️ Hijack 依赖 Windows winipcfg/syscall + 真实网络,无法在 Linux 上 go test,仅交叉编译
// (GOOS=windows go build)+ 真机验证(务必带 `bx run --test-timeout 2m` 死手自动复原)。
// 防泄漏 WFP 封 off-TUN :53(Windows smart-multihomed DNS 泄漏)是下一轮,见 CLAUDE.md。
package supervisor

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"syscall"
	"time"

	"github.com/getbx/bx/internal/tun"
	"github.com/getbx/bx/internal/winfw"
	"golang.org/x/sys/windows"
	wgtun "golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func newPlatform() platform { return windowsPlatform{} }

type windowsPlatform struct{}

// IP_UNICAST_IF / IPV6_UNICAST_IF 选项号(x/sys/windows 未导出;值同 WinSock 头 = 31,
// 与 WireGuard conn/bind_windows.go 一致)。
const (
	ipUnicastIf   = 31
	ipv6UnicastIf = 31
)

// hijackRouteMetric:劫持路由的 metric。split-default 的 /1 比物理 /0 更具体、bypass 比 /1 更具体,
// 按最长前缀已决定胜负,metric 基本无关紧要,取 0(与 WireGuard 一致)。
const hijackRouteMetric = 0

// tunV6ULA:给 wintun 开 IPv6 用的 ULA 主机地址(/128,仅为让 v6 路由对该接口有效,不承载真实子网)。
// 随机 fd00::/8 段,极低碰撞;域名维度 v6 已由 DNS NODATA 堵死,这里是字面量 v6 的纵深防御。
const tunV6ULA = "fd0b:a78e:9c1d::1/128"

// OpenTUN 用 wintun 建 Layer-3 适配器(经 wireguard tun.Device),桥接成 gVisor 端点,并回填
// 适配器 LUID 供 Hijack 用 winipcfg 编程。wintun 允许任意适配器名(不像 macOS utun 须 utunN),
// 故 bx 默认的 "bx0" 直接可用。运行时需签名的 wintun.dll 与 bx.exe 同目录。
func (windowsPlatform) OpenTUN(name, addr string, mtu uint32) (stack.LinkEndpoint, tunHandle, func(), error) {
	if name == "" {
		name = "bx0"
	}
	dev, err := wgtun.CreateTUN(name, int(mtu))
	if err != nil {
		return nil, tunHandle{}, nil, fmt.Errorf("创建 wintun 适配器(需管理员 + wintun.dll 同目录): %w", err)
	}
	real, err := dev.Name()
	if err != nil {
		_ = dev.Close()
		return nil, tunHandle{}, nil, fmt.Errorf("取 wintun 适配器名: %w", err)
	}
	var luid uint64
	if nt, ok := dev.(*wgtun.NativeTun); ok {
		luid = nt.LUID()
	}
	link, closeTUN := tun.NewWGEndpoint(dev, mtu)
	return link, tunHandle{Name: real, Addr: addr, MTU: mtu, LUID: luid}, closeTUN, nil
}

// DirectDialer 返回把出站绑到物理默认网卡的直连器(bx 自身出站绕过 wintun 防环,
// Windows 版 SO_MARK)。用 GetBestInterfaceEx 查到公网的默认出口 index(仅读路由表、不发包);
// 在 Hijack 装路由之前构造,故拿到的是物理口而非 TUN。v4/v6 各探一次,失败得 0(不绑降级)。
func (windowsPlatform) DirectDialer() *net.Dialer {
	idx4 := bestInterfaceIndex(net.IPv4(1, 1, 1, 1))
	idx6 := bestInterfaceIndex(net.ParseIP("2606:4700:4700::1111"))
	return unicastIfDialer(idx4, idx6)
}

// bestInterfaceIndex 查到该公网目的地的默认出口网卡 index(0=查询失败/无此协议栈)。
// GetBestInterfaceEx 只查路由表、不产生流量,故用固定公网 IP 探测是安全的。
func bestInterfaceIndex(ip net.IP) uint32 {
	if ip == nil {
		return 0
	}
	var sa windows.Sockaddr
	if v4 := ip.To4(); v4 != nil {
		sa = &windows.SockaddrInet4{Addr: [4]byte(v4)}
	} else if v6 := ip.To16(); v6 != nil {
		sa = &windows.SockaddrInet6{Addr: [16]byte(v6)}
	} else {
		return 0
	}
	var idx uint32
	if err := windows.GetBestInterfaceEx(sa, &idx); err != nil {
		return 0
	}
	return idx
}

// unicastIfDialer 用 IP_UNICAST_IF / IPV6_UNICAST_IF 把出站绑到指定网卡 index(0=不绑)。
// 仅对公网目的地绑(shouldBindToDevice):loopback/私网/link-local 不绑——绑物理口反而
// 连不通 127.0.0.1(本机 socks)/内网邻居,交主表原生投递(与 darwin boundIfDialer 同策略)。
// IPv4 的选项值须字节序转换(unicastIfV4Value,见 unicastif.go 头号坑);IPv6 用主机序 index。
func unicastIfDialer(idx4, idx6 uint32) *net.Dialer {
	return &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			if !shouldBindToDevice(address) {
				return nil
			}
			v6 := addrIsV6(address)
			if (v6 && idx6 == 0) || (!v6 && idx4 == 0) {
				return nil
			}
			var serr error
			if err := c.Control(func(fd uintptr) {
				h := windows.Handle(fd)
				if v6 {
					serr = windows.SetsockoptInt(h, windows.IPPROTO_IPV6, ipv6UnicastIf, int(idx6))
				} else {
					serr = windows.SetsockoptInt(h, windows.IPPROTO_IP, ipUnicastIf, int(unicastIfV4Value(idx4)))
				}
			}); err != nil {
				return err
			}
			return serr
		},
	}
}

// addrIsV6 判断 "host:port"(或裸 host)的 host 是否为 IPv6 字面量。
func addrIsV6(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return ip.Unmap().Is6()
}

// addedRoute 记一条已装路由,用于对称还原(DeleteRoute 需 dest + nextHop + 所在 LUID)。
type addedRoute struct {
	luid winipcfg.LUID
	dest netip.Prefix
	nh   netip.Addr
}

// Hijack 用 winipcfg 把默认流量劫进 wintun:配 TUN 地址 + split-default 劫进 TUN,
// 服务器/私网/SSH bypass 经物理默认网关旁路;IPv6 fail-closed(宿主有 v6 时把全局 v6 劫进 TUN)。
// 返回逐条对称还原的 cleanup(只删自己加的路由,绝不 flush 物理 LUID);TUN 关闭归 Run 的 closeTUN。
func (windowsPlatform) Hijack(t tunHandle, serverBypass, userBypass []string) (func(), error) {
	tunLUID := winipcfg.LUID(t.LUID)
	if tunLUID == 0 {
		return nil, errors.New("bx: 缺少 wintun 适配器 LUID(OpenTUN 未回填)")
	}
	// 1) TUN 配 v4 地址(点对点 /30)。
	tunPrefix, err := netip.ParsePrefix(t.Addr)
	if err != nil {
		return nil, fmt.Errorf("解析 TUN 地址 %q: %w", t.Addr, err)
	}
	if err := tunLUID.SetIPAddressesForFamily(windows.AF_INET, []netip.Prefix{tunPrefix}); err != nil {
		return nil, fmt.Errorf("配置 wintun v4 地址: %w", err)
	}
	// 2) 降低 TUN 接口 metric(best-effort;/1 本就比物理 /0 更具体,按最长前缀已抢赢)。
	if ipif, err := tunLUID.IPInterface(windows.AF_INET); err == nil {
		ipif.UseAutomaticMetric = false
		ipif.Metric = 0
		_ = ipif.Set()
	}
	// 3) 物理默认网关 + 其 LUID(bypass 路由挂它上,防环/私网/SSH 走原路)。
	gw, physLUID, err := physicalDefaultRoute()
	if err != nil {
		return nil, fmt.Errorf("探测物理默认路由: %w", err)
	}
	// 4) v6 fail-closed(仅宿主有 v6 时):给 TUN 开 v6,把 ::/1+8000::/1 劫进 TUN。best-effort:
	//    开 v6 失败不连累 v4 劫持(域名维度 v6 已由 DNS NODATA 堵死,此为字面量 v6 纵深防御)。
	blockV6 := ipv6HostEnabled()
	if blockV6 {
		if err := tunLUID.SetIPAddressesForFamily(windows.AF_INET6, []netip.Prefix{netip.MustParsePrefix(tunV6ULA)}); err != nil {
			log.Printf("windows: 开 TUN v6 失败,跳过 v6 阻断(v4 劫持不受影响): %v", err)
			blockV6 = false
		} else if ipif6, err := tunLUID.IPInterface(windows.AF_INET6); err == nil {
			ipif6.UseAutomaticMetric = false
			ipif6.Metric = 0
			_ = ipif6.Set()
		}
	}
	// 5) 应用路由计划,逐条记录以对称还原。
	plan := windowsRoutes(windowsDirectCIDRs, serverBypass, userBypass, blockV6)
	added, err := addPlannedRoutes(tunLUID, physLUID, gw, plan, false)
	wfpOn := false
	cleanup := func() {
		if wfpOn {
			winfw.DisableDNSLeak() // 先撤 WFP(动态会话,进程退出本也自动清)
		}
		_ = tunLUID.FlushDNS(windows.AF_INET) // 撤 TUN DNS(best-effort;适配器随 closeTUN 销毁本也消失)
		for i := len(added) - 1; i >= 0; i-- {
			_ = added[i].luid.DeleteRoute(added[i].dest, added[i].nh)
		}
	}
	if err != nil {
		cleanup()
		return nil, err
	}
	// 6) WFP 封 off-TUN :53,堵 Windows smart-multihomed DNS 泄漏。best-effort:失败不撤路由劫持
	//    (路由已把大部分 :53 导进 TUN;WFP 是纵深防御,真机验证前不让它一票否决整个 Hijack)。
	//    permitDNSServers 传 nil = 封尽所有非本进程/off-TUN :53(bx 自身 resolver 靠 permitSelf 放行)。
	if err := winfw.BlockDNSLeak(t.LUID, nil); err != nil {
		log.Printf("windows: 启用 WFP DNS 泄漏防护失败(路由劫持仍生效): %v", err)
	} else {
		wfpOn = true
	}
	// 7) DNS-into-TUN:给 TUN 设进-TUN 的哨兵 DNS(见 windns.go)。否则 Windows 系统 DNS 常指
	//    LAN 路由器(私网 bypass + 被 WFP 封)→ DNS 断。设成路由进 TUN 的公网地址,查询进 TUN
	//    由 fake-IP handler 应答;TUN 接口 metric 已 0(最优),系统优先用它。best-effort。
	if sentinel, perr := netip.ParseAddr(tunDNSSentinel); perr == nil {
		if err := tunLUID.SetDNS(windows.AF_INET, []netip.Addr{sentinel}, nil); err != nil {
			log.Printf("windows: 设 TUN DNS=%s 失败(DNS 可能走物理网卡漏/被 WFP 封): %v", tunDNSSentinel, err)
		}
	}
	log.Printf("windows: 默认路由已劫进 %s(LUID=%#x);bypass via %s;server=%v user=%v v6阻断=%v WFP-DNS=%v TUN-DNS=%s",
		t.Name, uint64(tunLUID), gw, serverBypass, userBypass, blockV6, wfpOn, tunDNSSentinel)
	return cleanup, nil
}

// RehijackRoutes 在存活 TUN 上重落实劫持「路由」(重探物理网关 + 幂等重装路由),绝不碰设备/地址。
// 供 commit-confirmed 的 Rehijack mutation 用。AddRoute 已存在则忽略(幂等)。
func (windowsPlatform) RehijackRoutes(t tunHandle, serverBypass, userBypass []string) error {
	tunLUID := winipcfg.LUID(t.LUID)
	if tunLUID == 0 {
		return errors.New("bx: 缺少 wintun 适配器 LUID")
	}
	gw, physLUID, err := physicalDefaultRoute()
	if err != nil {
		return fmt.Errorf("探测物理默认路由: %w", err)
	}
	plan := windowsRoutes(windowsDirectCIDRs, serverBypass, userBypass, ipv6HostEnabled())
	_, err = addPlannedRoutes(tunLUID, physLUID, gw, plan, true) // 幂等:忽略已存在
	return err
}

// addPlannedRoutes 把纯计划 plan 应用成 winipcfg 路由:winViaGateway→物理 LUID + 网关 nextHop;
// winViaTUN→TUN LUID + on-link 0.0.0.0;winV6Blackhole→TUN LUID + on-link ::。逐条返回已装路由
// 供回滚;ignoreExisting=true 时把「已存在」当成功跳过(Rehijack 幂等)。
func addPlannedRoutes(tunLUID, physLUID winipcfg.LUID, gw netip.Addr, plan []winRoute, ignoreExisting bool) ([]addedRoute, error) {
	var added []addedRoute
	for _, r := range plan {
		dest, err := netip.ParsePrefix(r.Dest)
		if err != nil {
			return added, fmt.Errorf("解析路由前缀 %q: %w", r.Dest, err)
		}
		dest = dest.Masked()
		var luid winipcfg.LUID
		var nh netip.Addr
		switch r.Via {
		case winViaGateway:
			luid, nh = physLUID, gw
		case winV6Blackhole:
			luid, nh = tunLUID, netip.IPv6Unspecified()
		default: // winViaTUN
			luid, nh = tunLUID, netip.IPv4Unspecified()
		}
		if err := luid.AddRoute(dest, nh, hijackRouteMetric); err != nil {
			if ignoreExisting && errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) {
				continue
			}
			return added, fmt.Errorf("加路由 %s via %s: %w", dest, nh, err)
		}
		added = append(added, addedRoute{luid: luid, dest: dest, nh: nh})
	}
	return added, nil
}

// physicalDefaultRoute 从 v4 路由表挑出物理默认路由(0.0.0.0/0,有真实网关、metric 最低),
// 返回网关 IP 与其接口 LUID。bx 从不装 /0(只装两个 /1),故此处永远命中物理默认,不会误取 TUN。
func physicalDefaultRoute() (netip.Addr, winipcfg.LUID, error) {
	rows, err := winipcfg.GetIPForwardTable2(windows.AF_INET)
	if err != nil {
		return netip.Addr{}, 0, err
	}
	// Windows 有效路由 metric = 路由 metric + **接口 metric**。多网卡(笔记本 Ethernet+WiFi、
	// 或已装 VPN)上多条 default 的路由 metric 常都是 0、只靠接口 metric 区分——只比 r.Metric
	// 会错选非首选网卡,把 bypass(服务器防环/私网/SSH)挂到错的 LUID → 隧道走烂路 → 健康失败
	// → kill-switch 全 Block → bx up 后断网(同 Linux Mudi 双 WAN 的老坑)。故必须相加取最小。
	ifMetric := map[winipcfg.LUID]uint32{} // 同 LUID 多条 default 不重复查
	var (
		bestGW     netip.Addr
		bestLUID   winipcfg.LUID
		bestMetric uint32
		found      bool
	)
	for i := range rows {
		r := &rows[i]
		p := r.DestinationPrefix.Prefix()
		if !p.IsValid() || p.Bits() != 0 { // 仅 0.0.0.0/0
			continue
		}
		nh := r.NextHop.Addr()
		if !nh.IsValid() || nh.IsUnspecified() { // 需真实网关(排除 on-link 无网关的 /0)
			continue
		}
		im, ok := ifMetric[r.InterfaceLUID]
		if !ok {
			if ipif, e := r.InterfaceLUID.IPInterface(windows.AF_INET); e == nil {
				im = ipif.Metric
			}
			ifMetric[r.InterfaceLUID] = im
		}
		eff := r.Metric + im
		if !found || eff < bestMetric {
			found, bestMetric, bestGW, bestLUID = true, eff, nh, r.InterfaceLUID
		}
	}
	if !found {
		return netip.Addr{}, 0, errors.New("未找到物理默认路由(0.0.0.0/0)")
	}
	return bestGW, bestLUID, nil
}
