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
)

func newPlatform() platform { return linuxPlatform{} }

type linuxPlatform struct{}

// OpenTUN 打开 /dev/net/tun 并接上 gVisor 协议栈(地址/置 up/路由由 Hijack 完成)。
func (linuxPlatform) OpenTUN(name, addr string, mtu uint32) (stack.LinkEndpoint, tunHandle, error) {
	link, err := tun.OpenDevice(name, mtu)
	if err != nil {
		return nil, tunHandle{}, err
	}
	return link, tunHandle{Name: name, Addr: addr, MTU: mtu}, nil
}

// DirectDialer 返回打 SO_MARK 的直连器(配合 pref 100 fwmark 规则绕过 tun)。
func (linuxPlatform) DirectDialer() *net.Dialer { return markedDialer(fwMark) }

// Hijack 探测默认网关,装策略路由把默认流量劫进 tun,bypass 段仍走原网关。
func (linuxPlatform) Hijack(t tunHandle, serverBypass, userBypass []string) (func(), error) {
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
	if err := nc.up(); err != nil {
		nc.down()
		return nil, err
	}
	log.Printf("默认路由已劫持进 %s;bypass=%v via %s dev %s", t.Name, bypass, gw, gwDev)
	return nc.down, nil
}

// markedDialer 返回打 SO_MARK 的直连器:让 bx 自身的直连绕过 tun(防环)。
func markedDialer(mark int) *net.Dialer {
	return &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, mark)
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
}

// upSteps 是 up() 要执行的 ip 命令序列(纯构造,无副作用,便于测试)。
func (n *netConf) upSteps() [][]string {
	steps := [][]string{
		{"addr", "add", n.tunAddr, "dev", n.tunName},
		{"link", "set", n.tunName, "up"},
	}
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
		steps = append(steps, []string{"rule", "add", "to", c, "pref", "150", "table", "main"})
	}
	steps = append(steps, []string{"rule", "add", "pref", "200", "table", itoa(routeTable)})
	return steps
}

func (n *netConf) up() error {
	for _, s := range n.upSteps() {
		if err := runIP(s...); err != nil {
			return err
		}
	}
	return nil
}

// downSteps 是 down() 要执行的还原命令序列(与 upSteps 对称)。
func (n *netConf) downSteps() [][]string {
	steps := [][]string{
		{"rule", "del", "pref", "200", "table", itoa(routeTable)},
	}
	for _, c := range n.mainLookup {
		steps = append(steps, []string{"rule", "del", "to", c, "pref", "150", "table", "main"})
	}
	steps = append(steps,
		[]string{"rule", "del", "pref", "100", "fwmark", fmtMark(fwMark), "table", "main"},
		[]string{"route", "flush", "table", itoa(routeTable)},
		[]string{"link", "del", n.tunName},
	)
	return steps
}

// down 尽力还原(忽略单步错误)。
func (n *netConf) down() {
	for _, s := range n.downSteps() {
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
