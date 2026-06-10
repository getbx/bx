//go:build darwin

// platform_darwin.go 是 platform 接口的 macOS 实现:
//   - OpenTUN:utun(经 wireguard-go tun.CreateTUN)+ wgbridge 适配成 gVisor 端点
//   - DirectDialer:IP_BOUND_IF 把直连 socket 绑物理出口网卡(防环;macOS 无 SO_MARK)
//   - Hijack:split-default(0/1+128/1)劫进 utun + 服务器/私网经物理网关旁路
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

// darwinDirectCIDRs:macOS 下保持原生直连(经物理网关)的私网段——RFC1918 + CGNAT + docker。
// 刻意不含 loopback(127/8)与 link-local(169.254/16):它们已有正确的本地路由,绝不可改写。
var darwinDirectCIDRs = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10",
}

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
func boundIfDialer(ifIndex int) *net.Dialer {
	return &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, _ string, c syscall.RawConn) error {
			if ifIndex == 0 {
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

	// 2) 组装路由:私网经物理网关、服务器+用户 bypass 经物理网关、split-default 进 utun。
	type spec struct {
		add  []string
		cidr string
	}
	var specs []spec
	viaGW := func(cidr string) spec { return spec{[]string{"-n", "add", "-net", cidr, gw}, cidr} }
	for _, c := range darwinDirectCIDRs {
		specs = append(specs, viaGW(c))
	}
	for _, c := range serverBypass {
		specs = append(specs, viaGW(c))
	}
	for _, c := range userBypass {
		specs = append(specs, viaGW(c))
	}
	for _, c := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		specs = append(specs, spec{[]string{"-n", "add", "-net", c, "-interface", t.Name}, c})
	}

	var done []string // 已加路由的 cidr,用于还原(只管路由;TUN 关闭归 Run 的 closeTUN)
	cleanup := func() {
		for i := len(done) - 1; i >= 0; i-- {
			_ = runCmdQuiet("route", "-n", "delete", "-net", done[i])
		}
	}
	for _, s := range specs {
		if err := runCmd("route", s.add...); err != nil {
			cleanup()
			return nil, fmt.Errorf("route %s: %w", strings.Join(s.add, " "), err)
		}
		done = append(done, s.cidr)
	}
	log.Printf("默认路由已劫持进 %s;serverBypass=%v userBypass=%v via %s", t.Name, serverBypass, userBypass, gw)
	return cleanup, nil
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
