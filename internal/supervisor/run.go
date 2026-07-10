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

// transportKind 由 server link 的 scheme 选传输。委托 tunnel.Kind(唯一真相源),
// 与 setup 探测派发同源,杜绝发散。
func transportKind(server string) string { return tunnel.Kind(server) }

// proxyMode 把生效的 global 标志 + cfg.Mode 归一成 status 呈现的分流模式标签:
// router(只劫持 LAN 转发)> global(含国内全走隧道)> split(国内直连、境外走隧道)。
func proxyMode(global bool, mode string) string {
	if mode == "router" {
		// router 是「劫持谁」(LAN 转发),global/split 是「怎么分流」——两轴正交,
		// 都要呈现,否则 status 看不出白名单(global)是否在 router 下生效。
		if global {
			return "router-global"
		}
		return "router"
	}
	if global {
		return "global"
	}
	return "split"
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
	ConfigPath      string        // 可选:配置文件路径;非空则 /v0/reload 重读它热重建 router(bx direct/proxy 用)
	NoHijack        bool          // 分步验证:起隧道+TUN+引擎但跳过 Hijack(不劫路由/不设 DNS/不装 WFP),系统网络零改动
}

// tunHandle 是 OpenTUN 返回的设备句柄,交给 Hijack 配路由。
// Name 在 macOS 上可能与请求名不同(utun 由内核分配),故由平台回填。
type tunHandle struct {
	Name string
	Addr string
	MTU  uint32

	// LUID 是 Windows wintun 适配器的接口 LUID,供 platform_windows 的 Hijack 用 winipcfg
	// 编程地址/路由。其他平台不用,恒为 0(Hijack 靠 Name 走 route/ip 命令)。
	LUID uint64

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
	// RehijackRoutes 在存活 TUN 设备上重落实劫持「路由」(重探网关 + 拆旧路由 + 装新路由),
	// 绝不删设备。供 commit-confirmed 的 Rehijack mutation 用。
	RehijackRoutes(tun tunHandle, serverBypass, userBypass []string) error
}

// Run 启动全局透明代理:建隧道→建 TUN→接线引擎→劫持默认路由,
// 阻塞到收到信号(或 deadman 到点),然后还原一切。
func Run(ctx context.Context, cfg *config.Config, opts Options) error {
	// 派生可取消 ctx:Run 任何返回(含信号退出)即 cancel,让 runFailover/refreshLoop/
	// mutEng 等后台 goroutine 确定性退出(不再靠进程退出兜底),并避免慢 teardown 期间
	// 后台 swapTo 复活已停隧道。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	plat := newPlatform()

	// 0) 物料:brook 改为**惰性准备**——只在真用 brook 传输时才 EnsureBrook(见 buildTunnel default)。
	// 否则 windows 上跑 reality/hysteria2 等也会无谓下载 brook(无内嵌),甚至在 github 慢/被挡时
	// 卡在与本次传输无关的 brook 下载上。china 列表仍按需在下面 EnsureLists。
	global := cfg.Global || opts.Global

	// 1) 分流脑(global 模式不需要 china 列表)
	var chinaDomain, chinaCIDR []string
	var domainPath, cidrPath string
	var listsOverridden bool
	if !global {
		dp, cp, err := provision.EnsureLists(cfg.DataDir, embedded.ChinaDomain(), embedded.ChinaCIDR())
		if err != nil {
			log.Printf("准备 china 列表失败(降级空列表,等刷新补): %v", err)
		}
		domainPath, cidrPath = dp, cp
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
	// buildTunnel 由 link 建隧道(含按需 sing-box 准备),供启动与 Slice 2b 运行期换隧道复用。
	buildTunnel := func(link string) (*tunnel.Tunnel, error) {
		switch transportKind(link) {
		case "reality":
			singboxPath, err := provision.EnsureSingbox(cfg.DataDir, cfg.SingboxBin, embedded.Singbox(), embedded.SingboxVersion(), cfg.SingboxURL, cfg.SingboxSHA256)
			if err != nil {
				return nil, fmt.Errorf("准备 sing-box: %w", err)
			}
			confPath := filepath.Join(cfg.DataDir, "sing-box.json")
			return tunnel.NewReality(singboxPath, link, opts.Probe, confPath, cfg.HTTPProxy)
		case "hysteria2":
			singboxPath, err := provision.EnsureSingbox(cfg.DataDir, cfg.SingboxBin, embedded.Singbox(), embedded.SingboxVersion(), cfg.SingboxURL, cfg.SingboxSHA256)
			if err != nil {
				return nil, fmt.Errorf("准备 sing-box: %w", err)
			}
			confPath := filepath.Join(cfg.DataDir, "sing-box-hy2.json")
			return tunnel.NewHysteria2(singboxPath, link, opts.Probe, confPath, cfg.HTTPProxy)
		case "trojan":
			singboxPath, err := provision.EnsureSingbox(cfg.DataDir, cfg.SingboxBin, embedded.Singbox(), embedded.SingboxVersion(), cfg.SingboxURL, cfg.SingboxSHA256)
			if err != nil {
				return nil, fmt.Errorf("准备 sing-box: %w", err)
			}
			confPath := filepath.Join(cfg.DataDir, "sing-box-trojan.json")
			return tunnel.NewTrojan(singboxPath, link, opts.Probe, confPath, cfg.HTTPProxy)
		case "shadowsocks":
			singboxPath, err := provision.EnsureSingbox(cfg.DataDir, cfg.SingboxBin, embedded.Singbox(), embedded.SingboxVersion(), cfg.SingboxURL, cfg.SingboxSHA256)
			if err != nil {
				return nil, fmt.Errorf("准备 sing-box: %w", err)
			}
			confPath := filepath.Join(cfg.DataDir, "sing-box-ss.json")
			return tunnel.NewShadowsocks(singboxPath, link, opts.Probe, confPath, cfg.HTTPProxy)
		case "vmess":
			singboxPath, err := provision.EnsureSingbox(cfg.DataDir, cfg.SingboxBin, embedded.Singbox(), embedded.SingboxVersion(), cfg.SingboxURL, cfg.SingboxSHA256)
			if err != nil {
				return nil, fmt.Errorf("准备 sing-box: %w", err)
			}
			confPath := filepath.Join(cfg.DataDir, "sing-box-vmess.json")
			return tunnel.NewVmess(singboxPath, link, opts.Probe, confPath, cfg.HTTPProxy)
		default:
			brookPath, err := provision.EnsureBrook(cfg.DataDir, firstNonEmpty(opts.BrookBin, cfg.Brook), embedded.Brook(), embedded.BrookVersion(), cfg.BrookURL, cfg.BrookSHA256)
			if err != nil {
				return nil, fmt.Errorf("准备 brook: %w", err)
			}
			return tunnel.NewBrook(brookPath, link, opts.Probe, cfg.HTTPProxy)
		}
	}
	tun0, err := buildTunnel(cfg.Server)
	if err != nil {
		return fmt.Errorf("构建隧道: %w", err)
	}
	lt := &liveTunnel{}
	lt.set(tun0)
	tun0.Start()
	// 停"当前"隧道:swap 后 lt 指向新隧道。swapper 就绪后经其锁停(与在飞 swapTo 串行,修 M3);
	// 未就绪(健康前提前返回)则直接停 lt(Stop 幂等,双停安全)。
	var swapper *transportSwapper
	defer func() {
		if swapper != nil {
			swapper.stop()
		} else {
			lt.get().Stop()
		}
	}()
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
	// 多传输防环:每个传输(主 + 容灾备选 + UDP 专用)的 server 都要进 serverBypass + 静态 DNS,
	// 否则切到「不同 server」的备选时,其子进程连自己 server 的连接会落进 TUN(成环/被 Block),
	// 容灾静默失效。在系统 DNS 交给 bx 前解析一次,同一批 IP 既做路由旁路又做本地 DNS 静态答案。
	staticA := map[string][]netip.Addr{}
	var serverAddrs []netip.Addr
	addServer := func(link string) error {
		h, err := serverHostFromLink(link)
		if err != nil {
			return fmt.Errorf("取传输服务器: %w", err)
		}
		if _, ok := staticA[h]; ok {
			return nil // 去重(多传输同 server)
		}
		a := hostToAddrs(h)
		if len(a) == 0 {
			return fmt.Errorf("无法解析传输服务器 %q 为 IP(bypass 必需,否则成环)", h)
		}
		staticA[h] = a
		serverAddrs = append(serverAddrs, a...)
		return nil
	}
	for _, link := range cfg.Transports { // 含主传输(transports[0]=cfg.Server)+ 容灾备选
		if err := addServer(link); err != nil {
			return err
		}
	}
	udpEnabled := cfg.UDP.Transport != "" && cfg.UDP.Mode == "proxy"
	if udpEnabled {
		if err := addServer(cfg.UDP.Transport); err != nil {
			return err
		}
	}
	if len(serverAddrs) == 0 {
		return fmt.Errorf("无法解析任何传输服务器 IP(bypass 必需)")
	}

	// 3) fake-IP 池 + DNS 处理器
	pool, err := fakeip.New(cfg.DNS.FakeipCIDR)
	if err != nil {
		return fmt.Errorf("建 fake-IP 池: %w", err)
	}
	dnsSrv := bxdns.NewServer(pool, 1)
	splitDirect := splitdns.NewSet()
	dnsSrv.SetStaticA(staticA, splitDirect)
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
		Direct:      direct,
		Killswitch:  cfg.Killswitch,
		Stats:       counters,
		UDPMode:     cfg.UDP.Mode,
		SplitDirect: splitDirect,
	}
	d.SetTransport(&dialer.Transport{Proxy: proxyDialer, Healthy: lt.Healthy})
	d.SetRouter(router)

	// 按类分流:UDP 专用传输(如 hysteria,QUIC 对丢包/高 RTT 更快)与主传输并行。
	// UDP proxy 走它;不变量保住——它挂时 UDP fail-closed Block(dialer.SetUDPTransport),绝不回落。
	// best-effort:UDP companion 是"锦上添花"的速度档,绝不阻塞主隧道(reality)把 TUN 拉起
	// (见 attachUDPCompanion)——否则 flaky UDP 上行(运营商丢包)会把秒健康的 reality 一起
	// 拖进重启循环。未健康时由上面的 fail-closed 不变量兜住,连上后自动接管 UDP/QUIC。
	if udpEnabled {
		udpTun, err := buildTunnel(cfg.UDP.Transport)
		if err != nil {
			return fmt.Errorf("构建 UDP 传输: %w", err)
		}
		defer udpTun.Stop()
		if err := attachUDPCompanion(d, udpTun, transportLabel(cfg.UDP.Transport)); err != nil {
			return fmt.Errorf("挂载 UDP 传输: %w", err)
		}
	}

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
	serverBypass := addrsToCIDRs(serverAddrs)
	swapper = &transportSwapper{
		lt:            lt,
		d:             d,
		build:         buildTunnel,
		healthTimeout: healthTimeout,
		ctx:           ctx,
	}
	swapper.setLink(cfg.Server)
	// 多传输自动容灾(reality 主 / brook 备…):后台监健康,持续不健康→按优先级 swapTo 备选,
	// 全程 fail-closed;防抖(滞回+冷静期+全挂不切)。单传输跳过(由 kill-switch 接管)。
	if len(cfg.Transports) > 1 {
		go swapper.runFailover(ctx, cfg.Transports,
			failoverPolicy{failoverAfter: 25 * time.Second, cooldown: 60 * time.Second},
			5*time.Second)
		log.Printf("多传输容灾已启用:%d 个传输,主=%s", len(cfg.Transports), transportLabel(cfg.Transports[0]))
	}
	mut := &liveMutator{
		plat:         plat,
		swap:         swapper,
		tunH:         tunH,
		serverBypass: serverBypass,
		userBypass:   cfg.Bypass,
	}
	// status 用的传输信息:active 动态(从 swapper 当前链接,容灾后反映实际),容灾列表/UDP 静态。
	var transportLabels []string
	if len(cfg.Transports) > 1 {
		for _, l := range cfg.Transports {
			transportLabels = append(transportLabels, transportLabel(l))
		}
	}
	udpLabel := ""
	if udpEnabled {
		udpLabel = transportLabel(cfg.UDP.Transport)
	}
	transportInfo := func() (string, []string, string) {
		return transportLabel(swapper.currentLink()), transportLabels, udpLabel
	}
	// rebuildRouter 从「当前」配置重建 router:ConfigPath 非空则重读配置文件拿最新 rules
	// (含 bx direct/proxy 的运行期改动),否则回退启动快照 cfg;china 列表用落盘最新。
	// reloadRouter 与 china 列表刷新共用它——否则刷新会拿启动时的陈旧 rules 覆盖掉热加的白名单
	//(split 模式下悄悄回退用户 bx direct 的改动)。
	rebuildRouter := func() (*route.Router, error) {
		rcfg := cfg
		if opts.ConfigPath != "" {
			nb, err := os.ReadFile(opts.ConfigPath)
			if err != nil {
				return nil, err
			}
			ncfg, err := config.Parse(nb)
			if err != nil {
				return nil, err
			}
			rcfg = ncfg
		}
		return rebuildRouterFromFiles(rcfg,
			filepath.Join(cfg.DataDir, "china_domain.txt"),
			filepath.Join(cfg.DataDir, "china_cidr4.txt"),
			global)
	}
	// reloadRouter(bx direct/proxy → /v0/reload):重建 router 原子换入,不断隧道、不碰 TUN/路由。
	// global 用启动值(改 mode/global 需重劫持,不在此列;这里只热更用户分流规则)。
	reloadRouter := func() error {
		nr, err := rebuildRouter()
		if err != nil {
			return err
		}
		d.SetRouter(nr)
		return nil
	}
	closer, err := requireControlSocket(func() (io.Closer, error) {
		return serveControl(counters, lt, serverHost, proxyMode(global, cfg.Mode), cfg.UDP.Mode, transportInfo, mutEng, mut, reloadRouter, uint32(cfg.OwnerUID))
	})
	if err != nil {
		return err
	}
	defer closer.Close()
	defer os.Remove(SockPath)
	if err := os.WriteFile(PidPath, []byte(itoa(os.Getpid())), 0o644); err == nil {
		defer os.Remove(PidPath)
	}

	// 6) 劫持默认路由(含 bypass 保 SSH + 服务器防环)。
	// --no-hijack:分步验证专用——隧道/TUN/引擎都已起,但**不劫持路由、不设 DNS、不装 WFP**,
	// 系统网络零改动。用于真机隔离验证「隧道能否健康 + TUN 能否起」而不冒断网/断 SSH 的风险。
	if opts.NoHijack {
		log.Printf("⚠️ --no-hijack:隧道+TUN+引擎已起,但未劫持路由/未设 DNS/未装 WFP(系统网络零改动)")
	} else {
		teardown, err := plat.Hijack(tunH, serverBypass, cfg.Bypass)
		if err != nil {
			return fmt.Errorf("配置路由: %w", err)
		}
		defer teardown()
		log.Printf("✅ bx 已全局接管。中国 IP 直连,其余走 bx 隧道。")
	}

	// 列表自动刷新(仅分流模式):隧道健康后周期经 socks5 拉最新列表热重载
	if !global && cfg.Lists.AutoUpdateEnabled() && !listsOverridden {
		go refreshLoop(ctx, cfg.Lists.RefreshInterval(), lt.Healthy, func() error {
			px, err := socksProxy(lt.SocksAddr(), &net.Dialer{Timeout: 10 * time.Second})
			if err != nil {
				return err
			}
			if err := fetchLists(ctx, proxyHTTPClient(px), cfg.DataDir); err != nil {
				return err
			}
			// 经 rebuildRouter 重读配置(而非启动快照 cfg):否则刷新会用陈旧 rules
			// 覆盖掉 bx direct/proxy 热加的白名单,悄悄回退用户改动。
			nr, err := rebuildRouter()
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
	// 关机 watchdog:还原已触发,下面的 defer 链若卡住超过 shutdownGrace(已知罕见 timing 竞态:
	// 疑 eng.Close/tun0.Stop),dump goroutine + 强制退出 —— 保证死手/信号一定终止进程,并捕获卡点根因。
	// 正常关机远快于 grace,watchdog 随进程退出自然作废、不触发。
	armShutdownWatchdog(shutdownGrace, dumpAndExit)
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

// attachUDPCompanion 启动 UDP 专用传输(如 hysteria2)并挂到 dialer——best-effort,不阻塞等待健康。
// UDP companion 只是按类分流的速度档;reality(TCP)才是抗封锁主干。启动期硬等 UDP 健康会让 flaky
// UDP 上行(运营商对 QUIC 丢包/限速)把秒健康的主隧道也一起拖进 procd 重启循环,TUN 永远起不来。
// 故此处只 Start + 挂载:未健康时 dialer 按 killswitch fail-closed 阻断 UDP(绝不回落主传输,既有
// dialer 测试保证),隧道连上后自动接管 UDP/QUIC。socksAddr 在 New 时即固定分配,不依赖健康。
func attachUDPCompanion(d *dialer.Dialer, udpTun *tunnel.Tunnel, label string) error {
	udpTun.Start()
	udpProxy, err := socksProxy(udpTun.SocksAddr(), &net.Dialer{Timeout: 10 * time.Second})
	if err != nil {
		return fmt.Errorf("构建 UDP socks 代理: %w", err)
	}
	d.SetUDPTransport(&dialer.Transport{Proxy: udpProxy, Healthy: udpTun.Healthy})
	log.Printf("UDP 专用传输已挂载:%s(UDP/QUIC 走它,TCP 走主传输;未健康则 UDP fail-closed,不拖住主隧道)", label)
	return nil
}

// dnsResolver 用指定 DNS 服务器解析(经防环直连器,绕过 tun)。
type dnsResolver struct{ r *net.Resolver }

// newResolver 按 dns.china 方案派发:https:// → DoH(TLS 校验、抗投毒,见 dohResolver);
// 否则明文 UDP:53(向后兼容,默认)。返回接口,供 Dialer.Resolver 用。
func newResolver(server string, base *net.Dialer) dialer.Resolver {
	if strings.HasPrefix(strings.ToLower(server), "https://") {
		return newDoHResolver(server, base)
	}
	return newUDPResolver(server, base)
}

func newUDPResolver(server string, base *net.Dialer) *dnsResolver {
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
