//go:build linux

package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/dialer"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/tun"
	"github.com/getbx/bx/internal/tunnel"
	"golang.org/x/net/proxy"
	"golang.org/x/sys/unix"
)

const (
	routeTable = 100   // tun 默认路由所在表
	fwMark     = 0x162 // bx 自身直连流量打的标(走原路由表绕过 tun)
)

// Options 是 bx up 的运行期参数(非配置文件项)。
type Options struct {
	TunName         string
	TunAddr         string // 给 TUN 配的地址/掩码,如 172.19.0.1/24
	MTU             uint32
	BrookBin        string
	ChinaDomainPath string
	ChinaCIDRPath   string
	Probe           string        // 隧道健康检查目标,如 1.1.1.1:443
	Deadman         time.Duration // >0:到点自动还原(远程实测保命)
}

// Run 启动全局透明代理:建隧道→建 TUN→接线引擎→劫持默认路由,
// 阻塞到收到信号(或 deadman 到点),然后还原一切。
func Run(ctx context.Context, cfg *config.Config, opts Options) error {
	// 1) 分流脑
	chinaDomain := readLines(opts.ChinaDomainPath)
	chinaCIDR := readLines(opts.ChinaCIDRPath)
	router, err := BuildRouter(cfg, chinaDomain, chinaCIDR)
	if err != nil {
		return fmt.Errorf("构建分流脑: %w", err)
	}
	log.Printf("分流脑就绪: china_domain=%d china_cidr=%d", len(chinaDomain), len(chinaCIDR))

	// 2) brook 隧道
	tun0, err := tunnel.NewBrook(opts.BrookBin, cfg.Server, opts.Probe)
	if err != nil {
		return fmt.Errorf("构建隧道: %w", err)
	}
	tun0.Start()
	defer tun0.Stop()
	log.Printf("brook 隧道启动: socks5=%s 探测=%s", tun0.SocksAddr(), opts.Probe)

	// 3) Dialer:marked 直连 + socks 代理 + 国内 DNS resolver
	direct := markedDialer(fwMark)
	proxyDialer, err := socksProxy(tun0.SocksAddr(), direct)
	if err != nil {
		return fmt.Errorf("构建 socks 代理: %w", err)
	}
	d := &dialer.Dialer{
		Router:     router,
		Resolver:   newResolver(cfg.DNS.China, direct),
		Proxy:      proxyDialer,
		Direct:     direct,
		Healthy:    tun0.Healthy,
		Killswitch: cfg.Killswitch,
	}

	// 4) TUN 设备 + 引擎
	link, err := tun.OpenDevice(opts.TunName, opts.MTU)
	if err != nil {
		return fmt.Errorf("建 TUN: %w", err)
	}
	// DNS 重定向:所有 :53 查询强制走中国 DNS 直连。
	// (brook 免费版的 socks5 仅 TCP,UDP-over-proxy 不可用;且系统 resolv.conf
	//  可能指向境外 DNS。真正的防污染 fake-IP 是后续 M5。)
	engineDialer := dnsRedirect{inner: d, direct: direct, china: cfg.DNS.China}
	eng, err := tun.New(link, engineDialer, opts.MTU)
	if err != nil {
		return fmt.Errorf("启动引擎: %w", err)
	}
	defer eng.Close()

	// 5) 劫持默认路由(含 bypass 保 SSH + 服务器防环)
	gw, gwDev, err := defaultRoute()
	if err != nil {
		return fmt.Errorf("探测默认网关: %w", err)
	}
	serverHost, err := serverHostFromLink(cfg.Server)
	if err != nil {
		return fmt.Errorf("取服务器 IP: %w", err)
	}
	bypass := append([]string{serverHost + "/32"}, cfg.Bypass...)
	nc := &netConf{
		tunName: opts.TunName, tunAddr: opts.TunAddr,
		gw: gw, gwDev: gwDev, bypass: bypass,
	}
	if err := nc.up(); err != nil {
		nc.down()
		return fmt.Errorf("配置路由: %w", err)
	}
	defer nc.down()
	log.Printf("默认路由已劫持进 %s;bypass=%v via %s dev %s", opts.TunName, bypass, gw, gwDev)
	log.Printf("✅ bx 已全局接管。中国 IP 直连,其余走 brook。")

	// 6) 阻塞:信号 / deadman / ctx
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	var deadman <-chan time.Time
	if opts.Deadman > 0 {
		log.Printf("⏲ 死手定时器 %s 后自动还原", opts.Deadman)
		deadman = time.After(opts.Deadman)
	}
	select {
	case s := <-sig:
		log.Printf("收到信号 %v,还原中…", s)
	case <-deadman:
		log.Printf("死手定时器到点,还原中…")
	case <-ctx.Done():
		log.Printf("ctx 取消,还原中…")
	}
	return nil
}

// dnsRedirect 把所有 UDP :53 查询重定向到中国 DNS 直连,其余委托给内层 Dialer。
type dnsRedirect struct {
	inner  tun.Dialer
	direct *net.Dialer
	china  string
}

func (d dnsRedirect) Dial(ctx context.Context, m route.Meta) (net.Conn, error) {
	if m.UDP && m.Port == 53 {
		return d.direct.DialContext(ctx, "udp", net.JoinHostPort(d.china, "53"))
	}
	return d.inner.Dial(ctx, m)
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

// socksProxy 把 brook 本地 socks5 包成带 context 的拨号器。
func socksProxy(socksAddr string, base proxy.Dialer) (dialer.ContextDialer, error) {
	d, err := proxy.SOCKS5("tcp", socksAddr, nil, base)
	if err != nil {
		return nil, err
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("socks dialer 不支持 context")
	}
	return cd, nil
}

// dnsResolver 用指定 DNS 服务器解析(经 marked 直连器,绕过 tun)。
type dnsResolver struct{ r *net.Resolver }

func newResolver(server string, base *net.Dialer) *dnsResolver {
	return &dnsResolver{r: &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return base.DialContext(ctx, network, net.JoinHostPort(server, "53"))
		},
	}}
}

func (d *dnsResolver) Resolve(ctx context.Context, domain string) (netip.Addr, error) {
	ips, err := d.r.LookupNetIP(ctx, "ip", domain)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(ips) == 0 {
		return netip.Addr{}, fmt.Errorf("无解析结果: %s", domain)
	}
	return ips[0].Unmap(), nil
}

// netConf 管理 TUN 接口 + 策略路由(table + rule + fwmark)。
type netConf struct {
	tunName string
	tunAddr string
	gw      string
	gwDev   string
	bypass  []string
}

func (n *netConf) up() error {
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
		[]string{"rule", "add", "pref", "200", "table", itoa(routeTable)},
	)
	for _, s := range steps {
		if err := runIP(s...); err != nil {
			return err
		}
	}
	return nil
}

// down 尽力还原(忽略单步错误)。
func (n *netConf) down() {
	_ = runIPQuiet("rule", "del", "pref", "200", "table", itoa(routeTable))
	_ = runIPQuiet("rule", "del", "pref", "100", "fwmark", fmtMark(fwMark), "table", "main")
	_ = runIPQuiet("route", "flush", "table", itoa(routeTable))
	_ = runIPQuiet("link", "del", n.tunName)
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

func readLines(path string) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		log.Printf("读列表 %s 失败(忽略): %v", path, err)
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func itoa(i int) string  { return fmt.Sprintf("%d", i) }
func fmtMark(m int) string { return fmt.Sprintf("0x%x", m) }
