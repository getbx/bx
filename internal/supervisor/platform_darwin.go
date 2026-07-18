//go:build darwin

// platform_darwin.go 是 platform 接口的 macOS 实现:
//   - OpenTUN:utun(经 wireguard-go tun.CreateTUN)+ wgbridge 适配成 gVisor 端点
//   - DirectDialer:IP_BOUND_IF 把直连 socket 绑物理出口网卡(防环;macOS 无 SO_MARK)
//   - Hijack:split-default(0/1+128/1)劫进 utun + 服务器/私网经物理网关旁路
//   - v6 fail-closed:两个 /1 的 `-reject` 阻断全局 IPv6(回 EHOSTUNREACH 逼回 v4)
//
// ⚠️ 路由部分(Hijack)依赖 macOS `route`/`ifconfig` 语义,无法在 Linux 上验证,
//
//	需在真实 macOS(sudo)上实测、按你的网络微调。其余(bridge/OpenTUN/DirectDialer)
//	已对照各库源码实现。
package supervisor

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/getbx/bx/internal/tun"
	"golang.org/x/sys/unix"
	wgtun "golang.zx2c4.com/wireguard/tun"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

const guardianBypassHandoffEnv = "BX_GUARDIAN_BYPASS_HANDOFF"

func newPlatform() platform { return darwinPlatform{} }

type darwinPlatform struct{}

// OpenTUN 创建 utun 设备(名字须 utunN 或 "utun" 由内核分配),桥接成 gVisor 端点。
// 返回的 closeTUN 停 pump、关设备,由 Run 用 defer 接管。
func (darwinPlatform) OpenTUN(name, addr string, mtu uint32) (stack.LinkEndpoint, tunHandle, func(), error) {
	// macOS utun 不允许任意名(bx 默认的 "bx0" 不合法),非 utun 前缀一律交内核分配。
	if !strings.HasPrefix(name, "utun") {
		name = "utun"
	}
	dev, err := wgtun.CreateTUN(name, int(mtu))
	if err != nil {
		return nil, tunHandle{}, nil, fmt.Errorf("创建 utun(需 root): %w", err)
	}
	real, err := dev.Name()
	if err != nil {
		_ = dev.Close()
		return nil, tunHandle{}, nil, fmt.Errorf("取 utun 名: %w", err)
	}
	link, closeTUN := tun.NewWGEndpoint(dev, mtu)
	return link, tunHandle{Name: real, Addr: addr, MTU: mtu}, closeTUN, nil
}

// DirectDialer 返回 IP_BOUND_IF 绑物理出口网卡的直连器(bx 自身出站绕过 utun)。
func (darwinPlatform) DirectDialer() *net.Dialer {
	idx := 0
	if _, dev, err := defaultRouteDarwin(); err == nil {
		if ifi, e := net.InterfaceByName(dev); e == nil {
			idx = ifi.Index
		}
	}
	return boundIfDialer(idx)
}

// boundIfDialer 用 IP_BOUND_IF / IPV6_BOUND_IF 把出站绑到指定网卡索引(0=不绑)。
// 仅对公网目的地绑(shouldBindToDevice):loopback/私网/link-local 不绑——绑物理口
// 反而连不通 lo/内网邻居(IP_BOUND_IF 绑后无法可靠连 127/8 即此故),交主表原生投递。
func boundIfDialer(ifIndex int) *net.Dialer {
	return &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			if ifIndex == 0 || !shouldBindToDevice(address) {
				return nil
			}
			var serr error
			if err := c.Control(func(fd uintptr) {
				if strings.HasSuffix(network, "6") {
					serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, ifIndex)
				} else {
					serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, ifIndex)
				}
			}); err != nil {
				return err
			}
			return serr
		},
	}
}

// Hijack 给 utun 配地址,装 split-default 把默认流量劫进 utun,
// 服务器/用户/私网段经物理网关旁路。返回还原闭包(删路由 + 关 utun)。
func (darwinPlatform) Hijack(t tunHandle, serverBypass, userBypass []string) (func(), error) {
	gw, _, err := defaultRouteDarwin()
	if err != nil {
		return nil, fmt.Errorf("探测默认网关: %w", err)
	}

	// 1) utun 配地址并 up(点对点:本端=对端=同一地址)。TUN 关闭由 Run 的 closeTUN 负责。
	ip := t.Addr
	if i := strings.IndexByte(ip, '/'); i >= 0 {
		ip = ip[:i]
	}
	if err := runCmd("ifconfig", t.Name, "inet", ip, ip, "up"); err != nil {
		return nil, fmt.Errorf("配置 utun 地址: %w", err)
	}

	// 2) 组装路由:v4 私网/bypass 经物理网关、split-default 劫进 utun;
	//    v6(内核启用时)fail-closed reject 全局 v6(纯构造见 darwinRouteSpecs)。
	blockV6 := ipv6HostEnabled()
	handoff := parseGuardianBypassHandoff(os.Getenv(guardianBypassHandoffEnv))
	specs := darwinRouteSpecsWithHandoff(t.Name, gw, darwinDirectCIDRs, serverBypass, userBypass, blockV6, handoff)

	var done []darwinRouteSpec // 已加路由,用于对称还原(只管路由;TUN 关闭归 Run 的 closeTUN)
	cleanup := func() {
		cleanupDarwinRouteSpecs(done, func(args ...string) error { return runCmdQuiet("route", args...) })
	}
	done, err = applyDarwinRouteSpecs(specs, runDarwinRouteCommand)
	if err != nil {
		cleanup()
		return nil, err
	}
	log.Printf("默认路由已劫持进 %s;serverBypass=%v userBypass=%v via %s;v6阻断=%v", t.Name, serverBypass, userBypass, gw, blockV6)
	return cleanup, nil
}

func runDarwinRouteCommand(args ...string) error {
	output, err := exec.Command("route", args...).CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, message)
}

// RehijackRoutes 路由-only 重落实(darwin):重探网关 + 幂等配地址 + 重装路由。
// darwin 的 Hijack teardown 本就只删路由(设备归 Run 的 closeTUN),故无删设备风险。
// 未真机验证(compile-only),与 Hijack 的真机待办一并验。
func (darwinPlatform) RehijackRoutes(t tunHandle, serverBypass, userBypass []string) error {
	gw, _, err := defaultRouteDarwin()
	if err != nil {
		return fmt.Errorf("探测默认网关: %w", err)
	}
	ip := t.Addr
	if i := strings.IndexByte(ip, '/'); i >= 0 {
		ip = ip[:i]
	}
	_ = runCmdQuiet("ifconfig", t.Name, "inet", ip, ip, "up") // 幂等
	specs := darwinRouteSpecs(t.Name, gw, darwinDirectCIDRs, serverBypass, userBypass, ipv6HostEnabled())
	for _, s := range specs {
		_ = runCmdQuiet("route", s.del...) // 尽力清旧
		if err := runCmd("route", s.add...); err != nil {
			return fmt.Errorf("route %s: %w", strings.Join(s.add, " "), err)
		}
	}
	return nil
}

// defaultRouteDarwin 解析 `route -n get default` 的网关与出口网卡。
func defaultRouteDarwin() (gw, dev string, err error) {
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "gateway:":
			gw = f[1]
		case "interface:":
			dev = f[1]
		}
	}
	if gw == "" || dev == "" {
		return "", "", fmt.Errorf("解析默认路由失败: %q", strings.TrimSpace(string(out)))
	}
	return gw, dev, nil
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func runCmdQuiet(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}
