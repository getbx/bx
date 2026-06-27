//go:build linux

// platform_linux.go 是 platform 接口的 Linux 实现:
//   - OpenTUN:/dev/net/tun + gVisor fdbased
//   - DirectDialer:SO_MARK 打标,配合 pref 100 fwmark 规则绕过 tun
//   - Hijack:ip rule/route 策略路由(table 100 + 私网 pref 150 + 全量 pref 200)
package supervisor

import (
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/tun"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

const (
	routeTable = 100   // tun 默认路由所在表
	fwMark     = 0x162 // bx 自身直连流量打的标(走原路由表绕过 tun)

	// tailscaleTable 是 tailscale 在 Linux 默认装 peer 路由的策略路由表(`ip route show
	// table 52`)。CGNAT overlay 段(cgnatV4CIDR)的 peer 路由在此表、不在 main,故需单独 carve。
	tailscaleTable = 52
	cgnatV4CIDR    = "100.64.0.0/10" // RFC6598 CGNAT;tailscale overlay(100.64/10)即用此段
)

func newPlatform() platform { return linuxPlatform{} }

type linuxPlatform struct{}

// OpenTUN 打开 /dev/net/tun 并接上 gVisor 协议栈(地址/置 up/路由由 Hijack 完成)。
// Linux 设备由 Hijack 的 `ip link del` 移除、fd 随进程退出回收,故 closeTUN 为空操作。
func (linuxPlatform) OpenTUN(name, addr string, mtu uint32) (stack.LinkEndpoint, tunHandle, func(), error) {
	link, err := tun.OpenDevice(name, mtu)
	if err != nil {
		return nil, tunHandle{}, nil, err
	}
	return link, tunHandle{Name: name, Addr: addr, MTU: mtu}, func() {}, nil
}

// DirectDialer 返回 bx 自身出站的直连器:始终 SO_MARK(配合 pref 100 fwmark 规则绕过 tun),
// 并对公网目的地额外绑物理出口网卡(SO_BINDTODEVICE),防宿主 CONNMARK 清掉 mark 后直连自环。
// 在 Hijack 前调用,defaultRoute() 取到的是原始物理默认出口;探测失败则退化为仅 SO_MARK。
func (linuxPlatform) DirectDialer() *net.Dialer {
	dev := ""
	if _, d, err := defaultRoute(); err == nil {
		dev = d
	}
	return markedBoundDialer(fwMark, dev)
}

// Hijack 探测默认网关,装策略路由把默认流量劫进 tun,bypass 段仍走原网关。
// router 模式只劫持 LAN 转发流量(见 hijackRouter),路由器自身流量不碰。
func (p linuxPlatform) Hijack(t tunHandle, serverBypass, userBypass []string) (func(), error) {
	if t.RouterMode {
		return p.hijackRouter(t, serverBypass, userBypass)
	}
	gw, gwDev, err := defaultRoute()
	if err != nil {
		return nil, fmt.Errorf("探测默认网关: %w", err)
	}
	bypass := append(append([]string{}, serverBypass...), userBypass...)
	nc := &netConf{
		tunName: t.Name, tunAddr: t.Addr,
		gw: gw, gwDev: gwDev, bypass: bypass,
		mainLookup: route.DefaultPrivateCIDRs, // 私网/docker 段在内核层分流到主表,绕开 tun
	}
	// v6 内核启用时才装 fail-closed 阻断;禁用(ipv6.disable=1 / 未编译)则跳过,不连累 v4 启动。
	// 私网段静态 carve + 动态补 on-link 全局前缀,保住同链路 GUA 邻居的直连。
	if ipv6Enabled() {
		nc.blockV6 = true
		nc.mainLookupV6 = append(append([]string{}, route.DefaultPrivateV6CIDRs...), onLinkV6Prefixes()...)
	}
	if err := nc.up(); err != nil {
		nc.down()
		return nil, err
	}
	log.Printf("默认路由已劫持进 %s;bypass=%v via %s dev %s", t.Name, bypass, gw, gwDev)
	return nc.down, nil
}

// RehijackRoutes 在存活 TUN 设备上重落实劫持「路由」:重探网关 → 拆旧路由 → 装新路由。
// 绝不删设备(故 bx0 始终在,快照网可兜底还原,不漏 IP)。外部事件(DHCP/NM/清规则)
// 破坏路由时由 commit-confirmed 的 Rehijack mutation 调用。
func (p linuxPlatform) RehijackRoutes(t tunHandle, serverBypass, userBypass []string) error {
	if t.RouterMode {
		return fmt.Errorf("router 模式暂不支持 rehijack")
	}
	gw, gwDev, err := defaultRoute() // 重探:网关常是「为何要 rehijack」的根源
	if err != nil {
		return fmt.Errorf("探测默认网关: %w", err)
	}
	bypass := append(append([]string{}, serverBypass...), userBypass...)
	nc := &netConf{
		tunName: t.Name, tunAddr: t.Addr,
		gw: gw, gwDev: gwDev, bypass: bypass,
		mainLookup: route.DefaultPrivateCIDRs,
	}
	if ipv6Enabled() {
		nc.blockV6 = true
		nc.mainLookupV6 = append(append([]string{}, route.DefaultPrivateV6CIDRs...), onLinkV6Prefixes()...)
	}
	// 先拆后装:routeDown 删 pref-200 catch-all 到 routeUp 重装之间有 ~ms 直连窗口(catch-all 暂缺,
	// 流量回落主表直连)。仅在 agent/运维主动 rehijack 时发生、隧道健康,影响可忽略;先拆保证旧网关
	// 的残留 bypass 路由不与新网关冲突(rehijack 常因网关变更触发)。
	nc.routeDown() // 清旧路由(幂等容错,保住设备)
	if err := nc.routeUp(); err != nil {
		return err // 引擎据此 Rollback(经 9a 快照网);设备在 → 快照可重建,无泄漏
	}
	log.Printf("rehijack:路由已在 %s 重落实 via %s dev %s", t.Name, gw, gwDev)
	return nil
}

// markedBoundDialer 返回 bx 自身出站的直连器:始终打 SO_MARK(让直连经 pref 100 fwmark 规则
// 走主表绕过 tun,防自环),并对公网目的地额外 SO_BINDTODEVICE 绑物理出口网卡 dev。
//
// 绑设备解决一类宿主:其 netfilter 在 mangle OUTPUT 用 `CONNMARK --restore-mark`(宽掩码)把
// 包的 fwmark 从连接标记恢复,会把 bx 的 SO_MARK 清成 0(如 QNAP QTS)。mark 一旦被清,fwmark
// 旁路规则失配,直连包落回 pref 200 → tun 自环,公网直连全断。绑物理口让包直接从该网卡出,
// 不再依赖会被清掉的 mark。因掩码常是全 32 位,改 mark 值救不了,故用绑设备兜底(defense-in-depth)。
//
// 仅公网目的地绑(shouldBindToDevice):私网/docker/loopback/link-local 交策略路由原生投递,
// 绑物理口反而到不了 lo/docker0/内网邻居。dev 为空(探测失败)时只打 SO_MARK,行为同旧版。
func markedBoundDialer(mark int, dev string) *net.Dialer {
	return &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				if serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, mark); serr != nil {
					return
				}
				if dev != "" && shouldBindToDevice(address) {
					serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, dev)
				}
			}); err != nil {
				return err
			}
			return serr
		},
	}
}

// netConf 管理 TUN 接口 + 策略路由(table + rule + fwmark)。
type netConf struct {
	tunName    string
	tunAddr    string
	gw         string
	gwDev      string
	bypass     []string // 走原网关绕过 tun 的网段(table 100 via gw),用户指定的公网/管理网
	mainLookup []string // 私网/docker 段:ip rule 送主表(pref 150),native 本地投递、绕开 tun

	// v6 阻断(fail-closed):宿主 v6 内核启用时由 Hijack 置 blockV6=true,装 `-6 unreachable`
	// 默认路由把全局 v6 堵死;v6 禁用则保持 false、一条 -6 都不产(不连累 v4 启动)。
	blockV6      bool
	mainLookupV6 []string // v6 私网/链路本地段:-6 rule 送主表(pref 150),carve-out 出阻断
}

// deviceUpSteps:建链路的设备步骤(配地址 + 置 up)。仅 Hijack 首次建链路做;
// Rehijack 路由重落实不碰设备。
func (n *netConf) deviceUpSteps() [][]string {
	return [][]string{
		{"addr", "add", n.tunAddr, "dev", n.tunName},
		{"link", "set", n.tunName, "up"},
	}
}

// routeUpSteps:只装策略路由(bypass / default dev tun / fwmark / 私网 carve / 全量 / v6 阻断)。
func (n *netConf) routeUpSteps() [][]string {
	steps := [][]string{}
	for _, b := range n.bypass {
		steps = append(steps, []string{"route", "add", b, "via", n.gw, "dev", n.gwDev, "table", itoa(routeTable)})
	}
	steps = append(steps,
		[]string{"route", "add", "default", "dev", n.tunName, "table", itoa(routeTable)},
		[]string{"rule", "add", "pref", "100", "fwmark", fmtMark(fwMark), "table", "main"},
	)
	// 私网/docker 段:pref 150(< 全量进 tun 的 200)送主表,由内核原路由 native 投递
	// (docker0/br-* on-link、内网 via 网关),宿主机访问容器/内网的包永不进 tun。
	for _, c := range n.mainLookup {
		// CGNAT overlay:tailscale peer 路由在 table 52(不在 main),故先在 pref 149
		// 送 table 52——tailscale 在则命中走 tailscale0;table 52 空(无 tailscale)则内核
		// 回落到下面 pref 150 → main(运营商 CGNAT 直连)。否则主动连 tailscale peer 的 TCP
		// 会因 main 无路由漏到物理网卡被丢。
		if c == cgnatV4CIDR {
			steps = append(steps, []string{"rule", "add", "to", c, "pref", "149", "table", itoa(tailscaleTable)})
		}
		steps = append(steps, []string{"rule", "add", "to", c, "pref", "150", "table", "main"})
	}
	steps = append(steps, []string{"rule", "add", "pref", "200", "table", itoa(routeTable)})

	// v6 阻断(fail-closed):仅在宿主 v6 内核启用时(blockV6)产出,镜像上面的 v4 策略路由。
	// fwmark 旁路(防 bx 自锁)→ v6 私网 carve-out → unreachable 默认 → 全量进阻断表。
	if n.blockV6 {
		steps = append(steps, []string{"-6", "rule", "add", "pref", "100", "fwmark", fmtMark(fwMark), "table", "main"})
		for _, c := range n.mainLookupV6 {
			steps = append(steps, []string{"-6", "rule", "add", "to", c, "pref", "150", "table", "main"})
		}
		steps = append(steps,
			[]string{"-6", "route", "add", "unreachable", "default", "table", itoa(routeTable)},
			[]string{"-6", "rule", "add", "pref", "200", "table", itoa(routeTable)},
		)
	}
	return steps
}

// upSteps = 设备步骤 + 路由步骤(行为同旧)。
func (n *netConf) upSteps() [][]string {
	return append(n.deviceUpSteps(), n.routeUpSteps()...)
}

func (n *netConf) up() error {
	for _, s := range n.upSteps() {
		if err := runIP(s...); err != nil {
			return err
		}
	}
	return nil
}

// routeDownSteps:只拆策略路由(与 routeUpSteps 对称);不删设备。
func (n *netConf) routeDownSteps() [][]string {
	steps := [][]string{
		{"rule", "del", "pref", "200", "table", itoa(routeTable)},
	}
	for _, c := range n.mainLookup {
		if c == cgnatV4CIDR {
			steps = append(steps, []string{"rule", "del", "to", c, "pref", "149", "table", itoa(tailscaleTable)})
		}
		steps = append(steps, []string{"rule", "del", "to", c, "pref", "150", "table", "main"})
	}
	steps = append(steps,
		[]string{"rule", "del", "pref", "100", "fwmark", fmtMark(fwMark), "table", "main"},
		[]string{"route", "flush", "table", itoa(routeTable)},
	)
	// 对称清理 v6 阻断(仅 blockV6 时装过)。
	if n.blockV6 {
		v6 := [][]string{
			{"-6", "rule", "del", "pref", "200", "table", itoa(routeTable)},
		}
		for _, c := range n.mainLookupV6 {
			v6 = append(v6, []string{"-6", "rule", "del", "to", c, "pref", "150", "table", "main"})
		}
		v6 = append(v6,
			[]string{"-6", "rule", "del", "pref", "100", "fwmark", fmtMark(fwMark), "table", "main"},
			[]string{"-6", "route", "flush", "table", itoa(routeTable)},
		)
		steps = append(steps, v6...)
	}
	return steps
}

// downSteps = 路由拆除 + 删设备(link del 末尾;与旧版步骤集合一致)。
func (n *netConf) downSteps() [][]string {
	return append(n.routeDownSteps(), []string{"link", "del", n.tunName})
}

// down 尽力还原(忽略单步错误)。
func (n *netConf) down() {
	for _, s := range n.downSteps() {
		_ = runIPQuiet(s...)
	}
}

// routeUp 只装路由(在存活设备上),任一步失败即返错。
func (n *netConf) routeUp() error {
	for _, s := range n.routeUpSteps() {
		if err := runIP(s...); err != nil {
			return err
		}
	}
	return nil
}

// routeDown 尽力拆路由(忽略单步错误),不碰设备。
func (n *netConf) routeDown() {
	for _, s := range n.routeDownSteps() {
		_ = runIPQuiet(s...)
	}
}

// defaultRoute 解析当前 IPv4 默认路由的网关与出口设备。
func defaultRoute() (gw, dev string, err error) {
	out, err := exec.Command("ip", "-4", "route", "show", "default").Output()
	if err != nil {
		return "", "", err
	}
	f := strings.Fields(string(out))
	for i := 0; i+1 < len(f); i++ {
		switch f[i] {
		case "via":
			gw = f[i+1]
		case "dev":
			dev = f[i+1]
		}
	}
	if gw == "" || dev == "" {
		return "", "", fmt.Errorf("解析默认路由失败: %q", strings.TrimSpace(string(out)))
	}
	return gw, dev, nil
}

// ipv6Enabled 判断宿主内核是否启用 IPv6:/proc/net/if_inet6 仅在 ipv6 模块加载时存在
// (ipv6.disable=1 或未编译时缺席)。缺席即无 v6 可漏,跳过 v6 阻断步骤。
func ipv6Enabled() bool {
	_, err := os.Stat("/proc/net/if_inet6")
	return err == nil
}

// onLinkV6Prefixes 读 `ip -6 route show` 提取 on-link 全局 v6 前缀,用于 carve 出阻断,
// 让同链路用 GUA 寻址的邻居在 bx 阻断全局 v6 时仍可直连。失败返回 nil(无额外 carve,不致命)。
func onLinkV6Prefixes() []string {
	out, err := exec.Command("ip", "-6", "route", "show").Output()
	if err != nil {
		return nil
	}
	return parseOnLinkV6Prefixes(string(out))
}

// parseOnLinkV6Prefixes 纯解析:提取「有 dev 无 via(连接路由)、属 2000::/3 全局单播、非 default」
// 的前缀。link-local(fe80)/ULA(fc00)已由 DefaultPrivateV6CIDRs 静态 carve,这里只补全局段。
func parseOnLinkV6Prefixes(routeOutput string) []string {
	var out []string
	for _, line := range strings.Split(routeOutput, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[0] == "default" {
			continue
		}
		var hasVia, hasDev bool
		for _, t := range f {
			switch t {
			case "via":
				hasVia = true
			case "dev":
				hasDev = true
			}
		}
		if hasVia || !hasDev || !isGlobalUnicastV6Prefix(f[0]) {
			continue
		}
		out = append(out, f[0])
	}
	return out
}

// isGlobalUnicastV6Prefix 报告前缀的网络地址是否落在 2000::/3(全局单播)。
func isGlobalUnicastV6Prefix(prefix string) bool {
	p, err := netip.ParsePrefix(prefix)
	if err != nil || !p.Addr().Is6() {
		return false
	}
	return globalUnicastV6.Contains(p.Addr())
}

var globalUnicastV6 = netip.MustParsePrefix("2000::/3")

func runIP(args ...string) error {
	cmd := exec.Command("ip", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ip %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func runIPQuiet(args ...string) error {
	return exec.Command("ip", args...).Run()
}

func fmtMark(m int) string { return fmt.Sprintf("0x%x", m) }
