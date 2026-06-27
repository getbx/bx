// run.go 是 Supervisor 的平台无关核心:编排隧道/分流脑/TUN 引擎/状态,
// 一切 OS 专属的事(开 TUN、防环直连器、劫持路由)经 platform 接口下沉到
// platform_<os>.go。读这里应当看不出自己在哪个操作系统上。
package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/dialer"
	bxdns "github.com/getbx/bx/internal/dns"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/provision"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/socks5"
	"github.com/getbx/bx/internal/splitdns"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/tun"
	"github.com/getbx/bx/internal/tunnel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// transportKind 由 server link 的 scheme 选传输:vless://→reality,其余→brook。
func transportKind(server string) string {
	if strings.HasPrefix(server, "vless://") {
		return "reality"
	}
	return "brook"
}

// Options 是 bx up 的运行期参数(非配置文件项)。
type Options struct {
	TunName         string
	TunAddr         string // 给 TUN 配的地址/掩码,如 198.51.100.1/30(避开 docker 172.16/12)
	MTU             uint32
	BrookBin        string
	ChinaDomainPath string
	ChinaCIDRPath   string
	Probe           string        // 隧道健康检查目标,如 1.1.1.1:443
	HealthTimeout   time.Duration // 等待隧道健康的启动窗口
	Deadman         time.Duration // >0:到点自动还原(远程实测保命)
	Global          bool          // 全局模式:除 bypass/用户 direct 规则外一切走代理
	DNSListen       string        // 可选:本地 DNS 监听地址,如 127.0.0.1:53(macOS 系统 DNS 接入)
}

// tunHandle 是 OpenTUN 返回的设备句柄,交给 Hijack 配路由。
// Name 在 macOS 上可能与请求名不同(utun 由内核分配),故由平台回填。
type tunHandle struct {
	Name string
	Addr string
	MTU  uint32

	// 路由器模式(mode=router):只劫持 LANCIDRs 内的转发流量,路由器自身流量不碰。
	RouterMode bool
	LANCIDRs   []string
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
		return fmt.Errorf("准备运行环境: %w", err)
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

	// 2) 隧道:按 server link 的 scheme 选传输(brook | reality),数据面不变。
	var tun0 *tunnel.Tunnel
	switch transportKind(cfg.Server) {
	case "reality":
		singboxPath, err := provision.EnsureSingbox(cfg.DataDir, cfg.SingboxBin, cfg.SingboxURL, cfg.SingboxSHA256)
		if err != nil {
			return fmt.Errorf("准备 sing-box: %w", err)
		}
		confPath := filepath.Join(cfg.DataDir, "sing-box.json")
		tun0, err = tunnel.NewReality(singboxPath, cfg.Server, opts.Probe, confPath, cfg.HTTPProxy)
		if err != nil {
			return fmt.Errorf("构建 reality 隧道: %w", err)
		}
	default:
		tun0, err = tunnel.NewBrook(brookPath, cfg.Server, opts.Probe, cfg.HTTPProxy)
		if err != nil {
			return fmt.Errorf("构建隧道: %w", err)
		}
	}
	tun0.Start()
	defer tun0.Stop()
	log.Printf("bx 隧道启动: socks5=%s 探测=%s", tun0.SocksAddr(), opts.Probe)
	healthTimeout := opts.HealthTimeout
	if healthTimeout <= 0 {
		healthTimeout = 20 * time.Second
	}
	if err := waitTunnelHealthy(ctx, tun0, healthTimeout); err != nil {
		return err
	}
	log.Printf("bx 隧道健康: 延迟=%dms", tun0.Stats().LatencyMS)

	serverHost, err := serverHostFromLink(cfg.Server)
	if err != nil {
		return fmt.Errorf("取服务器 IP: %w", err)
	}
	// 服务器可能是域名:在系统 DNS 尚未交给 bx 前解析一次,同一批 IP 同时用于
	// 路由旁路和本地 DNS 静态答案,避免运行期把 bx 自己的上游域名 fake 成环。
	serverAddrs := hostToAddrs(serverHost)
	if len(serverAddrs) == 0 {
		return fmt.Errorf("无法解析服务器 %q 为 IP(bypass 必需,否则成环)", serverHost)
	}

	// 3) fake-IP 池 + DNS 处理器
	pool, err := fakeip.New(cfg.DNS.FakeipCIDR)
	if err != nil {
		return fmt.Errorf("建 fake-IP 池: %w", err)
	}
	dnsSrv := bxdns.NewServer(pool, 1)
	splitDirect := splitdns.NewSet()
	dnsSrv.SetStaticA(map[string][]netip.Addr{serverHost: serverAddrs}, splitDirect)
	if opts.DNSListen != "" {
		dnsListener, err := bxdns.ListenUDP(opts.DNSListen, dnsSrv)
		if err != nil {
			return err
		}
		defer dnsListener.Close()
		log.Printf("本地 DNS 已监听: udp://%s", dnsListener.LocalAddr())
	}

	// split-DNS:匹配域名转发到内网 DNS 解析,真实 IP 注册进 splitDirect 强制直连。
	if len(cfg.DNS.Split) > 0 {
		var routes []bxdns.SplitRoute
		for _, r := range cfg.DNS.Split {
			routes = append(routes, bxdns.SplitRoute{
				Match:  route.NewDomainSet(r.Domains),
				Server: r.Server,
			})
		}
		dnsSrv.SetSplit(routes, bxdns.NewUDPForwarder(plat.DirectDialer()), splitDirect)
		log.Printf("split-DNS 已启用:%d 条规则", len(routes))
	}

	// fake-ip-filter:本地/反查域名(*.lan/*.arpa 等)不分配 fake-IP,转发到国内 DNS 真实解析并直连。
	if len(cfg.DNS.FakeipFilter) > 0 {
		fdns := cfg.DNS.China
		if _, _, err := net.SplitHostPort(fdns); err != nil {
			fdns = net.JoinHostPort(fdns, "53")
		}
		dnsSrv.SetFakeipFilter(cfg.DNS.FakeipFilter, fdns, bxdns.NewUDPForwarder(plat.DirectDialer()), splitDirect)
		log.Printf("fake-ip-filter 已启用:%d 条(本地/反查域名不走 fake-IP)", len(cfg.DNS.FakeipFilter))
	}

	// 4) Dialer:fake-IP 反查 + 防环直连 + socks 代理 + 国内 DNS resolver
	counters := &stats.Counters{}
	direct := plat.DirectDialer()
	// 这里连的是本机 brook socks5(127.0.0.1),必须走普通 loopback dialer。
	// macOS 的 DirectDialer 会 IP_BOUND_IF 绑物理网卡,绑后反而无法可靠连接 127/8。
	proxyDialer, err := socksProxy(tun0.SocksAddr(), &net.Dialer{Timeout: 10 * time.Second})
	if err != nil {
		return fmt.Errorf("构建 socks 代理: %w", err)
	}
	d := &dialer.Dialer{
		Fake:        pool, // 连接回到 TUN 时,用 fake IP 反查域名做精确分流
		Resolver:    newResolver(cfg.DNS.China, direct),
		Proxy:       proxyDialer,
		Direct:      direct,
		Healthy:     tun0.Healthy,
		Killswitch:  cfg.Killswitch,
		Stats:       counters,
		UDPMode:     cfg.UDP.Mode,
		SplitDirect: splitDirect,
	}
	d.SetRouter(router)

	// 5) TUN 设备 + 引擎(UDP:53 由 fake-IP DNS 处理器就地应答)
	link, tunH, closeTUN, err := plat.OpenTUN(opts.TunName, opts.TunAddr, opts.MTU)
	if err != nil {
		return fmt.Errorf("建 TUN: %w", err)
	}
	defer closeTUN() // Run 任何提前返回都会关 TUN(停 pump、移除设备),不泄漏
	// 路由器模式:把网关参数交给 Hijack,只劫持 LAN 转发流量。
	tunH.RouterMode = cfg.Mode == "router"
	tunH.LANCIDRs = cfg.Router.LANCIDRs
	eng, err := tun.New(link, d, opts.MTU, tun.WithDNS(dnsSrv), tun.WithStats(counters))
	if err != nil {
		return fmt.Errorf("启动引擎: %w", err)
	}
	defer eng.Close()

	// commit-confirmed 引擎:挂进守护进程,接 9a 真快照器;onRevert 大声记日志。
	mutEng := newMutationEngine(NewSystemSnapshotter(), 240*time.Second, time.Now, func(reverted bool, err error) {
		if err != nil {
			log.Printf("死手自动回滚失败(系统可能半改动): %v", err)
		} else if reverted {
			log.Printf("死手自动回滚:已还原到 last-known-good")
		}
	})
	go mutEng.Run(ctx)

	// 控制面 socket + pidfile(取代旧 serveStats,HTTP over unix socket)
	// 惰性指针捕获:teardown/serverBypass 在 serveControl 启动时尚未赋值,
	// liveMutator.apply 仅在 commit 路径执行,届时 plat.Hijack 已完成,指针有效。
	var teardown func()
	serverBypass := addrsToCIDRs(serverAddrs)
	mut := &liveMutator{
		plat:         plat,
		tunH:         tunH,
		serverBypass: serverBypass,
		userBypass:   cfg.Bypass,
		teardown:     &teardown,
	}
	closer, err := requireControlSocket(func() (io.Closer, error) {
		return serveControl(counters, tun0, serverHost, cfg.UDP.Mode, mutEng, mut)
	})
	if err != nil {
		return err
	}
	defer closer.Close()
	defer os.Remove(SockPath)
	if err := os.WriteFile(PidPath, []byte(itoa(os.Getpid())), 0o644); err == nil {
		defer os.Remove(PidPath)
	}

	// 6) 劫持默认路由(含 bypass 保 SSH + 服务器防环)
	teardown, err = plat.Hijack(tunH, serverBypass, cfg.Bypass)
	if err != nil {
		return fmt.Errorf("配置路由: %w", err)
	}
	defer func() { teardown() }()
	log.Printf("✅ bx 已全局接管。中国 IP 直连,其余走 bx 隧道。")

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

func waitTunnelHealthy(ctx context.Context, t *tunnel.Tunnel, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if t.Healthy() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			s := t.Stats()
			return fmt.Errorf("bx 隧道健康检查超时(%s): restarts=%d", timeout, s.Restarts)
		case <-tick.C:
		}
	}
}

func udpNote(mode string) string {
	switch mode {
	case "direct-realtime":
		return "non-DNS UDP direct; may expose real network path"
	case "proxy":
		return "non-DNS UDP relayed through bx tunnel"
	default:
		return "non-DNS UDP blocked; WebRTC/Google Meet may stutter"
	}
}

// socksProxy 把 brook 本地 socks5 包成带 context 的拨号器。
func socksProxy(socksAddr string, base *net.Dialer) (dialer.ContextDialer, error) {
	return socks5.NewDialer(socksAddr, base)
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
