//go:build linux

package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	bxdns "github.com/getbx/bx/internal/dns"
	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/stats"
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
	TunAddr         string // 给 TUN 配的地址/掩码,如 198.51.100.1/30(避开 docker 172.16/12)
	MTU             uint32
	BrookBin        string
	ChinaDomainPath string
	ChinaCIDRPath   string
	Probe           string        // 隧道健康检查目标,如 1.1.1.1:443
	Deadman         time.Duration // >0:到点自动还原(远程实测保命)
	Global          bool          // 全局模式:除 bypass/用户 direct 规则外一切走代理
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
	router.GlobalProxy = cfg.Global || opts.Global
	mode := "分流(中国直连/其余代理)"
	if router.GlobalProxy {
		mode = "全局(除内网/用户 direct 外一切走代理)"
	}
	log.Printf("分流脑就绪: 模式=%s china_domain=%d china_cidr=%d", mode, len(chinaDomain), len(chinaCIDR))

	// 2) brook 隧道
	tun0, err := tunnel.NewBrook(opts.BrookBin, cfg.Server, opts.Probe)
	if err != nil {
		return fmt.Errorf("构建隧道: %w", err)
	}
	tun0.Start()
	defer tun0.Stop()
	log.Printf("brook 隧道启动: socks5=%s 探测=%s", tun0.SocksAddr(), opts.Probe)

	// 3) fake-IP 池 + DNS 处理器
	pool, err := fakeip.New(cfg.DNS.FakeipCIDR)
	if err != nil {
		return fmt.Errorf("建 fake-IP 池: %w", err)
	}
	dnsSrv := bxdns.NewServer(pool, 1)

	// 4) Dialer:fake-IP 反查 + marked 直连 + socks 代理 + 国内 DNS resolver
	counters := &stats.Counters{}
	direct := markedDialer(fwMark)
	proxyDialer, err := socksProxy(tun0.SocksAddr(), direct)
	if err != nil {
		return fmt.Errorf("构建 socks 代理: %w", err)
	}
	d := &dialer.Dialer{
		Router:     router,
		Fake:       pool, // 连接回到 TUN 时,用 fake IP 反查域名做精确分流
		Resolver:   newResolver(cfg.DNS.China, direct),
		Proxy:      proxyDialer,
		Direct:     direct,
		Healthy:    tun0.Healthy,
		Killswitch: cfg.Killswitch,
		Stats:      counters,
	}

	// 5) TUN 设备 + 引擎(UDP:53 由 fake-IP DNS 处理器就地应答)
	link, err := tun.OpenDevice(opts.TunName, opts.MTU)
	if err != nil {
		return fmt.Errorf("建 TUN: %w", err)
	}
	eng, err := tun.New(link, d, opts.MTU, tun.WithDNS(dnsSrv), tun.WithStats(counters))
	if err != nil {
		return fmt.Errorf("启动引擎: %w", err)
	}
	defer eng.Close()

	// 状态查询 socket + pidfile
	serverHostForReport, _ := serverHostFromLink(cfg.Server)
	if closer, err := serveStats(counters, tun0, serverHostForReport); err != nil {
		log.Printf("状态 socket 启动失败(忽略): %v", err)
	} else {
		defer closer.Close()
		defer os.Remove(SockPath)
	}
	if err := os.WriteFile(PidPath, []byte(itoa(os.Getpid())), 0o644); err == nil {
		defer os.Remove(PidPath)
	}

	// 5) 劫持默认路由(含 bypass 保 SSH + 服务器防环)
	gw, gwDev, err := defaultRoute()
	if err != nil {
		return fmt.Errorf("探测默认网关: %w", err)
	}
	serverHost, err := serverHostFromLink(cfg.Server)
	if err != nil {
		return fmt.Errorf("取服务器 IP: %w", err)
	}
	// 服务器可能是域名:解析成 IP 段再 bypass(避免 brook 到服务器的连接被 tun 捕获成环)。
	serverBypass := hostToCIDRs(serverHost)
	if len(serverBypass) == 0 {
		return fmt.Errorf("无法解析 brook 服务器 %q 为 IP(bypass 必需,否则成环)", serverHost)
	}
	bypass := append(serverBypass, cfg.Bypass...)
	nc := &netConf{
		tunName: opts.TunName, tunAddr: opts.TunAddr,
		gw: gw, gwDev: gwDev, bypass: bypass,
		mainLookup: route.DefaultPrivateCIDRs, // 私网/docker 段在内核层分流到主表,绕开 tun
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
	defer signal.Stop(sig)
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

// serveStats 在 unix socket 上提供状态查询:每个连接回一份 JSON Report。
func serveStats(c *stats.Counters, t *tunnel.Tunnel, server string) (io.Closer, error) {
	_ = os.Remove(SockPath)
	ln, err := net.Listen("unix", SockPath)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(SockPath, 0o666)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			ts := t.Stats()
			rep := stats.Report{
				Snapshot:      c.Snapshot(),
				Server:        server,
				SocksAddr:     t.SocksAddr(),
				TunnelHealthy: ts.Up,
				LatencyMS:     ts.LatencyMS,
				Restarts:      ts.Restarts,
			}
			_ = json.NewEncoder(conn).Encode(rep)
			conn.Close()
		}
	}()
	return ln, nil
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

func itoa(i int) string    { return fmt.Sprintf("%d", i) }
func fmtMark(m int) string { return fmt.Sprintf("0x%x", m) }
