// run.go 是 Supervisor 的平台无关核心:编排隧道/分流脑/TUN 引擎/状态,
// 一切 OS 专属的事(开 TUN、防环直连器、劫持路由)经 platform 接口下沉到
// platform_<os>.go。读这里应当看不出自己在哪个操作系统上。
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
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/dialer"
	bxdns "github.com/getbx/bx/internal/dns"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/provision"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/tun"
	"github.com/getbx/bx/internal/tunnel"
	"golang.org/x/net/proxy"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
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

// tunHandle 是 OpenTUN 返回的设备句柄,交给 Hijack 配路由。
// Name 在 macOS 上可能与请求名不同(utun 由内核分配),故由平台回填。
type tunHandle struct {
	Name string
	Addr string
	MTU  uint32
}

// platform 抽象 Run 需要的全部 OS 专属能力,每个 OS 一份实现(按构建标签选取)。
// 接口按「意图」定义而非「机制」:例如 DirectDialer 表达「不绕回隧道的直连器」,
// Linux 用 SO_MARK、macOS 用 IP_BOUND_IF、Windows 用 IP_UNICAST_IF 各自实现。
type platform interface {
	// OpenTUN 创建 TUN 设备,返回 gVisor link 端点、句柄(供 Hijack 用)、
	// 以及关闭该设备的闭包(由 Run 用 defer 接管,确保任何提前返回都不泄漏设备/goroutine)。
	OpenTUN(name, addr string, mtu uint32) (link stack.LinkEndpoint, tun tunHandle, closeTUN func(), err error)
	// DirectDialer 返回一个直连器:其连接绕过 TUN(防环),供 bx 自身出站用
	//(直连决策、国内 DNS 解析、拨号到本地 socks)。
	DirectDialer() *net.Dialer
	// Hijack 把默认流量劫进 TUN,但 serverBypass(brook 服务器)与 userBypass
	//(管理网/SSH)仍走原网关;私网/docker 段由平台各自处理。返回还原闭包。
	Hijack(tun tunHandle, serverBypass, userBypass []string) (teardown func(), err error)
}

// Run 启动全局透明代理:建隧道→建 TUN→接线引擎→劫持默认路由,
// 阻塞到收到信号(或 deadman 到点),然后还原一切。
func Run(ctx context.Context, cfg *config.Config, opts Options) error {
	plat := newPlatform()

	// 0) 物料:内嵌 brook/列表落盘(零外部依赖)
	brookPath, err := provision.EnsureBrook(cfg.DataDir, firstNonEmpty(opts.BrookBin, cfg.Brook), embedded.Brook(), embedded.BrookVersion())
	if err != nil {
		return fmt.Errorf("准备 brook: %w", err)
	}
	global := cfg.Global || opts.Global

	// 1) 分流脑(global 模式不需要 china 列表)
	var chinaDomain, chinaCIDR []string
	var domainPath, cidrPath string
	var listsOverridden bool
	if !global {
		domainPath, cidrPath, err = provision.EnsureLists(cfg.DataDir, embedded.ChinaDomain(), embedded.ChinaCIDR())
		if err != nil {
			log.Printf("准备 china 列表失败(降级空列表,等刷新补): %v", err)
		}
		// 列表路径覆盖优先级:CLI flag > config lists.* > 内嵌/刷新快照
		domainOverride := firstNonEmpty(opts.ChinaDomainPath, cfg.Lists.ChinaDomain)
		cidrOverride := firstNonEmpty(opts.ChinaCIDRPath, cfg.Lists.ChinaCIDR)
		if domainOverride != "" {
			domainPath = domainOverride
		}
		if cidrOverride != "" {
			cidrPath = cidrOverride
		}
		listsOverridden = domainOverride != "" || cidrOverride != ""
		chinaDomain = readLines(domainPath)
		chinaCIDR = readLines(cidrPath)
	}
	router, err := BuildRouter(cfg, chinaDomain, chinaCIDR)
	if err != nil {
		return fmt.Errorf("构建分流脑: %w", err)
	}
	router.GlobalProxy = global
	mode := "分流(中国直连/其余代理)"
	if global {
		mode = "全局(除内网/用户 direct 外一切走代理)"
	}
	log.Printf("分流脑就绪: 模式=%s china_domain=%d china_cidr=%d", mode, len(chinaDomain), len(chinaCIDR))

	// 2) brook 隧道
	tun0, err := tunnel.NewBrook(brookPath, cfg.Server, opts.Probe)
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

	// 4) Dialer:fake-IP 反查 + 防环直连 + socks 代理 + 国内 DNS resolver
	counters := &stats.Counters{}
	direct := plat.DirectDialer()
	proxyDialer, err := socksProxy(tun0.SocksAddr(), direct)
	if err != nil {
		return fmt.Errorf("构建 socks 代理: %w", err)
	}
	d := &dialer.Dialer{
		Fake:       pool, // 连接回到 TUN 时,用 fake IP 反查域名做精确分流
		Resolver:   newResolver(cfg.DNS.China, direct),
		Proxy:      proxyDialer,
		Direct:     direct,
		Healthy:    tun0.Healthy,
		Killswitch: cfg.Killswitch,
		Stats:      counters,
	}
	d.SetRouter(router)

	// 5) TUN 设备 + 引擎(UDP:53 由 fake-IP DNS 处理器就地应答)
	link, tunH, closeTUN, err := plat.OpenTUN(opts.TunName, opts.TunAddr, opts.MTU)
	if err != nil {
		return fmt.Errorf("建 TUN: %w", err)
	}
	defer closeTUN() // Run 任何提前返回都会关 TUN(停 pump、移除设备),不泄漏
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

	// 6) 劫持默认路由(含 bypass 保 SSH + 服务器防环)
	serverHost, err := serverHostFromLink(cfg.Server)
	if err != nil {
		return fmt.Errorf("取服务器 IP: %w", err)
	}
	// 服务器可能是域名:解析成 IP 段再 bypass(避免 brook 到服务器的连接被 tun 捕获成环)。
	serverBypass := hostToCIDRs(serverHost)
	if len(serverBypass) == 0 {
		return fmt.Errorf("无法解析 brook 服务器 %q 为 IP(bypass 必需,否则成环)", serverHost)
	}
	teardown, err := plat.Hijack(tunH, serverBypass, cfg.Bypass)
	if err != nil {
		return fmt.Errorf("配置路由: %w", err)
	}
	defer teardown()
	log.Printf("✅ bx 已全局接管。中国 IP 直连,其余走 brook。")

	// 列表自动刷新(仅分流模式):隧道健康后周期经 socks5 拉最新列表热重载
	if !global && cfg.Lists.AutoUpdateEnabled() && !listsOverridden {
		client := proxyHTTPClient(proxyDialer) // 单个客户端复用连接池,跨刷新周期不重建
		go refreshLoop(ctx, cfg.Lists.RefreshInterval(), tun0.Healthy, func() error {
			if err := fetchLists(ctx, client, cfg.DataDir); err != nil {
				return err
			}
			nr, err := rebuildRouterFromFiles(cfg,
				filepath.Join(cfg.DataDir, "china_domain.txt"),
				filepath.Join(cfg.DataDir, "china_cidr4.txt"),
				global)
			if err != nil {
				return err
			}
			d.SetRouter(nr)
			return nil
		})
		log.Printf("china 列表自动刷新已启用: 间隔=%s", cfg.Lists.RefreshInterval())
	}

	// 7) 阻塞:信号 / deadman / ctx
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

// dnsResolver 用指定 DNS 服务器解析(经防环直连器,绕过 tun)。
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

func itoa(i int) string { return fmt.Sprintf("%d", i) }
