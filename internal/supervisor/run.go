// run.go 是 Supervisor 的平台无关核心:编排隧道/分流脑/TUN 引擎/状态,
// 一切 OS 专属的事(开 TUN、防环直连器、劫持路由)经 platform 接口下沉到
// platform_<os>.go。读这里应当看不出自己在哪个操作系统上。
package supervisor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/dialer"
	bxdns "github.com/getbx/bx/internal/dns"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/overlay"
	"github.com/getbx/bx/internal/provision"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/socks5"
	"github.com/getbx/bx/internal/splitdns"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/tun"
	"github.com/getbx/bx/internal/tunnel"
	"github.com/getbx/bx/internal/version"
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

// takeoverSummary 是路由劫持成功后那句播报。**它必须与这一次实际生效的分流
// 方式一致**,而不是一句写死的话。
//
// 起因是一次真实误导(2026-08-31,项目所有者升级后读日志发现):这句话原先
// 无条件说「中国 IP 直连,其余走 bx 隧道」,而 global 模式下 china 列表**整个
// 不加载** —— 同一份日志里隔两行就是 `china_domain=0 china_cidr=0`。一句关于
// 系统当前行为的假话比不说更糟:用户会据此以为国内流量在直连,于是去别处找
// 「访问国内站点为什么慢」的原因,而真实原因正是它们全走了隧道。
//
// 与 proxyMode 共用同一套模式判定(它给 status 用、这个给日志用),两处措辞
// 对得上;四种组合各有各的话,由 TestTakeoverSummaryIsDistinctPerMode 钉住 ——
// 两种模式塌成同一句,就意味着有一种在被另一种的描述冒名顶替。
// listsOverridden 为真 = 用户用自己的表替掉了内建/刷新那张(config `lists.*`
// 或 CLI flag)。**它必须进这句话**:两种来源的行为可能差很远,而日志一模一样
// 时用户无从分辨此刻按哪张表在分流。global 下它不影响措辞 —— 那个模式根本不
// 加载任何列表,提一张不存在的表本身就是误导。
func takeoverSummary(global bool, mode string, listsOverridden bool) string {
	// split 那半也要说全:直连的**不只**是列表命中的那些,用户 direct 规则
	// (`rules:` 里的 kind: direct)同样在直连,而且优先级更高 —— 只提列表
	// 会让人以为自己加的那几条没生效。
	split := "中国 IP 与用户 direct 规则直连,其余走 bx 隧道。"
	if listsOverridden {
		split = "自定义直连列表与用户 direct 规则直连,其余走 bx 隧道。"
	}
	if global {
		// 与 proxyMode 那行「除内网/用户 direct 外一切走代理」是同一句话。
		split = "除内网与用户 direct 规则外,一切走 bx 隧道。"
	}
	if mode == "router" {
		// 只劫持 LAN 转发流量,路由器自身的出站不碰 —— 说「全局接管」会让人
		// 以为这台路由器自己也走了隧道。
		return "✅ bx 已接管 LAN 转发流量。" + split
	}
	return "✅ bx 已全局接管。" + split
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

	// BuildTunnel 可选:替换建隧道的方式。nil = 生产默认(拉起 brook/sing-box 子进程)。
	//
	// 存在的理由是集成台要在 netns 里跑完整 Run() 而不发任何真实外网流量。这是本
	// 代码库唯一一处「组装根可以被外部指定」的缝:2026-08-09 那一轮的两个 Critical
	// 都住在 Run() 的接线里,而接线不可测正是它们能活到复审第三轮的原因。
	//
	// 非 nil 时**只**替换建隧道这一件事;平台、路由、DNS、屏障、控制面一律走生产代码。
	BuildTunnel func(link, recoveryID string, auxiliaryHTTP bool) (*tunnel.Tunnel, error)
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

	// 1) 分流脑(global 模式不需要 china 列表)。相位抽在 splitbrain.go ——
	// 它的两条关键关系(global 不读列表、CLI flag 压过 config)在那里可以被
	// 直接断言,而长在这个 700 行的函数体里时只能靠读代码确认。
	brain, err := buildSplitBrain(cfg, opts)
	if err != nil {
		return err
	}
	router := brain.Router
	listsOverridden := brain.ListsOverridden
	mode := "分流(中国直连/其余代理)"
	if global {
		mode = "全局(除内网/用户 direct 外一切走代理)"
	}
	log.Printf("分流脑就绪: 模式=%s china_domain=%d china_cidr=%d", mode, brain.DomainCount, brain.CIDRCount)

	// 2) 隧道:按 server link 的 scheme 选传输(brook | reality),数据面不变。
	// buildTunnel 由 link 建隧道(含按需 sing-box 准备),供启动与 Slice 2b 运行期换隧道复用。
	var buildTunnelMu sync.Mutex
	buildTunnel := func(link, recoveryID string, auxiliaryHTTP bool) (*tunnel.Tunnel, error) {
		buildTunnelMu.Lock()
		defer buildTunnelMu.Unlock()
		if opts.BuildTunnel != nil {
			return opts.BuildTunnel(link, recoveryID, auxiliaryHTTP)
		}
		httpAddr, err := privateAuxiliaryAddr(cfg.HTTPProxy, auxiliaryHTTP)
		if err != nil {
			return nil, fmt.Errorf("分配传输私有 HTTP 监听: %w", err)
		}
		kind := transportKind(link)
		switch kind {
		case "reality":
			singboxPath, err := ensureSingboxBinary(cfg)
			if err != nil {
				return nil, err
			}
			confPath := transportConfigPath(cfg.DataDir, kind, recoveryID)
			tun, err := tunnel.NewReality(singboxPath, link, opts.Probe, confPath, httpAddr)
			if tun != nil {
				tun.CleanupFileOnStop(confPath)
			}
			return tun, err
		case "hysteria2":
			singboxPath, err := ensureSingboxBinary(cfg)
			if err != nil {
				return nil, err
			}
			confPath := transportConfigPath(cfg.DataDir, kind, recoveryID)
			tun, err := tunnel.NewHysteria2(singboxPath, link, opts.Probe, confPath, httpAddr)
			if tun != nil {
				tun.CleanupFileOnStop(confPath)
			}
			return tun, err
		case "trojan":
			singboxPath, err := ensureSingboxBinary(cfg)
			if err != nil {
				return nil, err
			}
			confPath := transportConfigPath(cfg.DataDir, kind, recoveryID)
			tun, err := tunnel.NewTrojan(singboxPath, link, opts.Probe, confPath, httpAddr)
			if tun != nil {
				tun.CleanupFileOnStop(confPath)
			}
			return tun, err
		case "shadowsocks":
			singboxPath, err := ensureSingboxBinary(cfg)
			if err != nil {
				return nil, err
			}
			confPath := transportConfigPath(cfg.DataDir, kind, recoveryID)
			tun, err := tunnel.NewShadowsocks(singboxPath, link, opts.Probe, confPath, httpAddr)
			if tun != nil {
				tun.CleanupFileOnStop(confPath)
			}
			return tun, err
		case "vmess":
			singboxPath, err := ensureSingboxBinary(cfg)
			if err != nil {
				return nil, err
			}
			confPath := transportConfigPath(cfg.DataDir, kind, recoveryID)
			tun, err := tunnel.NewVmess(singboxPath, link, opts.Probe, confPath, httpAddr)
			if tun != nil {
				tun.CleanupFileOnStop(confPath)
			}
			return tun, err
		default:
			brookPath, err := ensureBrookBinary(cfg, opts)
			if err != nil {
				return nil, err
			}
			return tunnel.NewBrook(brookPath, link, opts.Probe, httpAddr)
		}
	}
	tun0, err := buildTunnel(cfg.Server, "active-main", true)
	if err != nil {
		return fmt.Errorf("构建隧道: %w", err)
	}
	lt := &liveTunnel{}
	lt.set(tun0)
	tun0.Start()
	// 停完整传输 generation:协调器就绪后,主/UDP teardown 与 recovery/failover 共用同一
	// 串行边界并永久封住新提交;未就绪的提前返回则直接停止已创建的 live slots。
	var swapper, udpSwapper *transportSwapper
	var liveTransports *liveTransportSet
	var udpLT *liveTunnel
	// **拆除台账从这里开始接管**(teardown.go)。位置是承重的:它必须落在
	// 第一个要还原的系统资源**之前** —— 比它更早的 defer(cancel 等)仍由
	// 语言机制在台账之后跑,与迁移前的相对顺序一致;它之后的每一个资源都进
	// 台账,彼此仍是 LIFO。混着来会把顺序悄悄颠倒,而顺序错了就是把机器留在
	// 一个没有 TUN 也没有原路由的状态里。
	teardowns := &teardownLedger{}
	defer teardowns.unwind()
	// 后台工人登记册(workers.go):下面每一个长命 goroutine 都经它启动。
	// 裸 `go f(ctx)` 里的 panic 不会被上面那个 defer 接住 —— 它当场打死整个
	// 进程,而内核里的 ip rule / 策略路由**还在**,机器就此指向一个不存在的
	// TUN。六个工人没有一件事值得那个代价,逐个理由写在 workers.go 头上。
	workers := &workerRegistry{}
	teardowns.push("停止传输", func() {
		if liveTransports != nil {
			liveTransports.Stop()
			return
		}
		if udpLT != nil {
			udpLT.get().Stop()
		}
		lt.get().Stop()
	})
	log.Printf("bx 隧道启动: socks5=%s 探测=%s", tun0.SocksAddr(), opts.Probe)
	healthTimeout := opts.HealthTimeout
	if healthTimeout <= 0 {
		healthTimeout = 20 * time.Second
	}
	// 隧道没起来时**当场判别一次**「是那台服务器不通,还是它活着而隧道没建起来」——
	// 两者的处置完全相反(tunneldiagnosis.go)。判别只在失败路径上发生:拨号器
	// 是个 thunk,成功启动的那条路上连造都不会造它。
	//
	// 拨号器是**两条**:先绑物理网卡(防的是上一个崩掉的实例留下的陈旧 TUN),
	// SYN 在本机就没出去时不绑再试一次 —— 此刻 TUN 还没开、路由还没劫持,不绑
	// 走的正是隧道子进程刚才那 20 秒走的同一张主路由表(diagnosisDialWithUnboundRetry)。
	if err := awaitTunnelHealthOrDiagnose(ctx, tun0, healthTimeout, cfg.Server,
		func() tunnelDialFunc {
			return diagnosisDialWithUnboundRetry(plat.DirectDialer().DialContext, (&net.Dialer{}).DialContext)
		}); err != nil {
		return err
	}
	log.Printf("bx 隧道健康: 延迟=%dms", tun0.Stats().LatencyMS)
	var auxiliary *auxiliaryProxy
	if cfg.HTTPProxy != "" {
		if tun0.HTTPAddr() == "" {
			return errors.New("主传输缺少私有 HTTP 监听")
		}
		auxiliary, err = startAuxiliaryProxy(cfg.HTTPProxy, tun0.HTTPAddr())
		if err != nil {
			return fmt.Errorf("监听固定 HTTP 代理: %w", err)
		}
		teardowns.push("关闭辅助代理", func() { _ = auxiliary.Close() })
	}

	serverHost, err := serverHostFromLink(cfg.Server)
	if err != nil {
		return tagStartFailure(ErrConfig, fmt.Errorf("取服务器 IP: %w", err))
	}
	// 多传输防环:每个传输(主 + 容灾备选 + UDP 专用)的 server 都要进 serverBypass + 静态 DNS,
	// 否则切到「不同 server」的备选时,其子进程连自己 server 的连接会落进 TUN(成环/被 Block),
	// 容灾静默失效。在系统 DNS 交给 bx 前解析一次,同一批 IP 既做路由旁路又做本地 DNS 静态答案。
	// 服务器清单在场时这里是**每一台的两条链接**,不只当前那台 —— 见 bypassLinks 的注释。
	// 非当前那些解析失败只跳过、不连累启动;当前那台失败则拒绝启动。这套判断连同
	// 去重、空集合检查一起住在 resolveServerBypass 里:**切换前刷新 bypass 走的是
	// 同一个函数**。两条路径分头实现,迟早有一条会漏掉某类链接,而漏掉的那一条
	// 就是成环(静默:连得上、status 显绿、流量绕圈)。
	// serverStatic 是**合并用户 hosts 覆盖之前**的那一半(host → 服务器真 IP)。
	// 它必须原样留到 wireBypass:屏障开口与刷新的保留基准都只能用它,
	// 从合并后的表推回去会把用户 hosts 里的任意 IPv4 带进 fail-closed 屏障的开口。
	serverStatic, _, err := resolveServerBypass(cfg)
	if err != nil {
		return err
	}
	udpEnabled := cfg.UDP.Transport != "" && cfg.UDP.Mode == "proxy"

	// 3) fake-IP 池 + DNS 处理器
	pool, err := fakeip.New(cfg.DNS.FakeipCIDR)
	if err != nil {
		return tagStartFailure(ErrConfig, fmt.Errorf("建 fake-IP 池: %w", err))
	}
	dnsSrv := bxdns.NewServer(pool, 1)
	splitDirect := splitdns.NewSet()

	userHosts, err := cfg.HostOverrides()
	if err != nil {
		// HostOverrides 只在 cfg.Hosts 未经 Parse 校验时出错——生产路径的 cfg 全部
		// 经 loadConfig→config.Parse,理论上到不了这里。但它返回 error 而非 panic
		// 正是为了这一刻:bx Core 由 Guardian 监管,panic 会被当异常退出重启,新
		// 进程带着同一份 cfg 立刻在同一处再 panic 一次,变成崩溃循环而非一次干净
		// 的启动失败。
		return tagStartFailure(ErrConfig, fmt.Errorf("hosts 覆盖: %w", err))
	}
	staticA, appliedHosts, ignoredHosts := mergeHostOverrides(serverStatic, userHosts)
	for _, host := range ignoredHosts {
		// 只记不停:配置里那条是错的,但传输服务器的真 IP 已经用上了,
		// 隧道是安全的。停在这里对用户没有好处。
		log.Printf("hosts_override_ignored host=%s reason=transport_server", host)
	}
	for host, addr := range appliedHosts {
		log.Printf("hosts_override_applied host=%s addr=%s", host, addr)
	}
	// appliedHosts 之后 Task 3 还要用来填 stats.Report,故变量留着不丢弃
	// (即便这条 log 循环之外没人再用它)。

	dnsSrv.SetStaticA(staticA, splitDirect)
	dnsListening := false
	if opts.DNSListen != "" {
		dnsListener, err := bxdns.ListenUDP(opts.DNSListen, dnsSrv)
		if err != nil {
			return err
		}
		teardowns.push("关闭 DNS 监听", func() { _ = dnsListener.Close() })
		dnsListening = true
		log.Printf("本地 DNS 已监听: udp://%s", dnsListener.LocalAddr())
	}

	// 先问「机器上还跑着哪些 overlay」——下面的 split-DNS 与后面的中继旁路都要用它。
	presentOverlays := detectOverlayTenants()

	// split-DNS:匹配域名转发到内网 DNS 解析,真实 IP 注册进 splitDirect 强制直连。
	//
	// **在跑的 overlay 租户自带一组 split**(见 internal/overlay):Tailscale 的
	// `*.ts.net` 只有它自己的 MagicDNS 解析器答得出来。此前 bx 把这些域名放进
	// fakeip_filter(不分配假 IP)却仍然转给国内 DNS —— 那里没有答案,于是
	// **知道一半比两边都不知道更糟**:不特殊对待反而会拿到假 IP 经隧道走。
	//
	// 只对**在跑**的租户生效:解析器是租户自己起的进程,它没跑时把查询送过去
	// 只会挂住。**用户在 config 里写的 split 排在前面**(见下),同名后缀以用户为准。
	overlaySplit := overlay.SplitRoutes(presentOverlays)
	if len(cfg.DNS.Split) > 0 || len(overlaySplit) > 0 {
		// **顺序即优先级**,判据在 buildSplitRoutes(splitbrain.go):用户的规则
		// 排在 overlay 那组前面,由行为断言钉住 —— 此前它只由一条比较两个 for
		// 循环在 run.go 里出现位置的源码守卫钉着。
		routes := buildSplitRoutes(cfg.DNS.Split, overlaySplit)
		for _, r := range overlaySplit {
			log.Printf("overlay 共存:*.%s 交给 %s 解析", r.Suffix, normalizeDNSServerAddr(r.Resolver))
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
	d.SetTransport(&dialer.Transport{Proxy: proxyDialer, Healthy: tun0.Healthy})
	d.SetRouter(router)

	// 应用流量归因:**同一个实例**同时接 dialer(记判定)与 engine(记字节),
	// 见 wireAppAttribution 上的注释。没人订阅时它**不问内核、不记字节、不攒
	// 历史**(字节记账那条路径由一次 atomic 读挡在锁外),只维护一张活连接表 ——
	// 那张表是「窗口打开时看得见已经在跑的连接」的前提,代价是每条连接**两次全局
	// 锁获取 + 四次 map 操作**(建连时读改写一次、关闭时读删一次;那把锁与字节
	// 记账是同一把),详见 apptraffic.go 上 AppTraffic 的类型注释。
	appTraffic := NewAppTraffic(newAppSource(), nil)
	appAttribution := wireAppAttribution(d, appTraffic)

	// 按类分流:UDP 专用传输(如 hysteria,QUIC 对丢包/高 RTT 更快)与主传输并行。
	// UDP proxy 走它;**它挂时 UDP 回落主传输,不 Block**(dialer.SetUDPTransport 上写了
	// 为什么:同一台 VPS、同一条加密隧道,回落它不泄漏真实 IP;主传输也挂才 fail-closed)。
	// best-effort:UDP companion 是"锦上添花"的速度档,绝不阻塞主隧道(reality)把 TUN 拉起
	// (见 attachUDPCompanion)——否则 flaky UDP 上行(运营商丢包)会把秒健康的 reality 一起
	// 拖进重启循环。未健康时由上面那条回落兜住通路,连上后自动接管 UDP/QUIC。
	var udpHealthy func() bool
	if udpEnabled {
		udpTun, err := buildTunnel(cfg.UDP.Transport, "active-udp", false)
		if err != nil {
			return fmt.Errorf("构建 UDP 传输: %w", err)
		}
		udpLT = &liveTunnel{}
		udpLT.set(udpTun)
		if err := attachUDPCompanion(d, udpTun, transportLabel(cfg.UDP.Transport)); err != nil {
			return fmt.Errorf("挂载 UDP 传输: %w", err)
		}
		udpHealthy = udpLT.Healthy
	}

	// 5) TUN 设备 + 引擎(UDP:53 由 fake-IP DNS 处理器就地应答)
	link, tunH, closeTUN, err := plat.OpenTUN(opts.TunName, opts.TunAddr, opts.MTU)
	if err != nil {
		return tagStartFailure(ErrTUNOpen, fmt.Errorf("建 TUN: %w", err))
	}
	// Run 任何提前返回都会关 TUN(停 pump、移除设备),不泄漏。
	teardowns.push("关闭 TUN", closeTUN)
	// 路由器模式:把网关参数交给 Hijack,只劫持 LAN 转发流量。
	tunH.RouterMode = cfg.Mode == "router"
	tunH.LANCIDRs = cfg.Router.LANCIDRs
	eng, err := tun.New(link, d, opts.MTU, tun.WithDNS(dnsSrv), tun.WithStats(counters), appAttribution)
	if err != nil {
		return fmt.Errorf("启动引擎: %w", err)
	}
	teardowns.push("关闭引擎", func() { _ = eng.Close() })

	// commit-confirmed 引擎:挂进守护进程,接 9a 真快照器;onRevert 大声记日志。
	mutEng := newMutationEngine(NewSystemSnapshotter(), 240*time.Second, time.Now, func(reverted bool, err error) {
		if err != nil {
			log.Printf("死手自动回滚失败(系统可能半改动): %v", err)
		} else if reverted {
			log.Printf("死手自动回滚:已还原到 last-known-good")
		}
	})
	workers.start(ctx, "mutation-engine", mutEng.Run)

	// 控制面 socket + pidfile(取代旧 serveStats,HTTP over unix socket)
	//
	// 中继旁路只给**确实在跑**的 overlay 装;抓不到就先用兜底表,由后台循环一直
	// 重试(开机自启那一刻网络往往还没好,而早先抓不到就永远停在兜底表)。
	//
	// **写死的公网 IP 是一种泄漏面**:地址被回收给别人之后,那条旁路仍在,于是发往
	// 陌生人的流量绕过隧道、带着真实源地址出去。这条规矩是 internal/overlay 为
	// ZeroTier 的兜底表立的,而 Tailscale 的 DERP 表此前**没有**执行它 —— 一台从没
	// 装过 Tailscale 的机器照样会被装上十条(抓不到 DERP map 时)或整张 DERP 节点表。
	// 昨天去掉 darwin 门之后,那个影响面还扩到了 Linux 与 Windows。
	//
	// 当初不门控的理由是「会让『开机时抓不到就永远停在兜底表』更糟」,而那个缺口
	// 已经由重试循环补上了:租户晚于 bx 启动时,下一轮探测就会看见它。
	tailscaleBypass := newTailscaleBypassSource(initialOverlayBypass(ctx, direct, presentOverlays))
	workers.start(ctx, "tailscale-bypass", func(c context.Context) {
		tailscaleBypass.Run(c, overlayAwareBypassFetch(
			func(c context.Context) ([]string, error) {
				// **每轮现测**:租户可能晚于 bx 启动。没有 Tailscale 就不抓 DERP map,
				// 也就不会给一台与它无关的机器装上那些 /32。
				if !overlayPresent(detectOverlayTenants(), "tailscale") {
					return nil, errNoTailscaleForBypass
				}
				return tailscaleDERPBypassCIDRs(c, direct)
			},
			func() []string { return overlay.BypassCIDRs(detectOverlayTenants()) },
		))
	})
	bypassWire := wireBypass(bypassWiringParams{
		configPath:    opts.ConfigPath,
		serverStatics: serverStatic,
		staticA:       staticA,
		extraCIDRs:    tailscaleBypass.CIDRs,
		// 与 Dialer 同一个国内 DNS + 防环直连。**绝不能**换成系统解析器:
		// 刷新发生在 DNS 已交给 bx 之后,系统解析器此刻就是 bx 自己,新服务器
		// (还没有静态 A)会被回一个 198.18/15 的 fake IP。
		resolver:   newResolver(cfg.DNS.China, direct),
		setStaticA: func(records map[string][]netip.Addr) { dnsSrv.SetStaticA(records, splitDirect) },
		fakeipCIDR: cfg.DNS.FakeipCIDR,
	})
	bypassState := bypassWire.store
	serverBypass := bypassState.cidrs()
	generation := newTransportGeneration()
	swapper = &transportSwapper{
		generation:    generation,
		lt:            lt,
		d:             d,
		build:         buildTunnel,
		auxiliary:     auxiliary,
		healthTimeout: healthTimeout,
		ctx:           ctx,
	}
	swapper.setLink(cfg.Server)
	if udpLT != nil {
		udpSwapper = &transportSwapper{
			generation:    generation,
			lt:            udpLT,
			d:             d,
			build:         buildTunnel,
			healthTimeout: healthTimeout,
			ctx:           ctx,
			udp:           true,
		}
		udpSwapper.setLink(cfg.UDP.Transport)
	}
	var udpSlot *transportSetSlot
	if udpSwapper != nil {
		slot := udpSwapper.transportSetSlot(udpTransportSlot)
		udpSlot = &slot
	}
	liveTransports = newLiveTransportSet(
		generation,
		swapper.transportSetSlot(mainTransportSlot),
		udpSlot,
		d.SetTransportGeneration,
	)
	// 多传输自动容灾(reality 主 / brook 备…):后台监健康,持续不健康→按优先级 swapTo 备选,
	// 全程 fail-closed;防抖(滞回+冷静期+全挂不切)。单传输跳过(由 kill-switch 接管)。
	if len(cfg.Transports) > 1 {
		workers.start(ctx, "transport-failover", func(c context.Context) {
			swapper.runFailover(c, cfg.Transports,
				failoverPolicy{failoverAfter: 25 * time.Second, cooldown: 60 * time.Second},
				5*time.Second)
		})
		log.Printf("多传输容灾已启用:%d 个传输,主=%s", len(cfg.Transports), transportLabel(cfg.Transports[0]))
	}
	routes := &routeReadiness{}
	mut := &liveMutator{
		plat:         plat,
		swap:         swapper,
		tunH:         tunH,
		serverBypass: serverBypass,
		store:        bypassState,
		userBypass:   cfg.Bypass,
		routes:       routes,
	}
	// 显式判断:*transportSwapper 的 nil 指针直接赋给 linkSwapper 接口会得到一个
	// **非 nil 的接口**(typed nil),后面 m.udpSwap != nil 就成了永真,调用即 panic。
	if udpSwapper != nil {
		mut.udpSwap = udpSwapper
	}
	refreshServerBypass := bypassWire.refresh
	runtimeBypass := bypassWire.runtimeServerBypass
	runtimeState := func() RuntimeState {
		udpRequired, udpReady := udpRuntimeReadiness(cfg.UDP.Mode, lt.Healthy, udpHealthy)
		liveHost := serverHost
		if swapper != nil {
			if h, err := serverHostFromLink(swapper.currentLink()); err == nil && h != "" {
				liveHost = h
			}
		}
		return RuntimeState{
			ConfigPath:      opts.ConfigPath,
			ServerHost:      liveHost,
			Version:         version.Version,
			PID:             os.Getpid(),
			TunName:         tunH.Name,
			SocksAddr:       lt.SocksAddr(),
			ServerBypass:    runtimeBypass(),
			TunnelHealthy:   lt.Healthy(),
			DNSListening:    dnsListening,
			RoutesInstalled: routes.ready(),
			UDPRequired:     udpRequired,
			UDPReady:        udpReady,
			DNSUpstream:     cfg.DNS.China,
		}
	}
	// 具名出口:每个配置一个 SOCKS5 拨号器。
	//
	// **走普通 dialer,不走 DirectDialer** —— 出口在 loopback(config 加载期强制),
	// 而 macOS 的 DirectDialer 会 IP_BOUND_IF 绑物理网卡,绑后反而连不上 127/8
	// (与连本机 brook socks5 那处同一个坑,run.go 上面已经踩过一次)。
	if len(cfg.Egress) > 0 {
		egresses := make(map[string]dialer.ContextDialer, len(cfg.Egress))
		for _, e := range cfg.Egress {
			d, derr := socksProxy(e.Socks5, &net.Dialer{Timeout: 10 * time.Second})
			if derr != nil {
				// **接不上就不接,绝不放一个坏的进去**:dialVia 对「没有这个出口」
				// 的处置是阻断,而那正是我们要的 —— 而一个连不上的拨号器会让
				// 每条连接都走完超时才失败。
				log.Printf("具名出口 %q 接线失败(该网段将被阻断,不会回落直连): %v", e.Name, derr)
				continue
			}
			egresses[e.Name] = d
			log.Printf("具名出口 %q → %s", e.Name, e.Socks5)
		}
		d.SetEgresses(egresses)
	}

	// **直连出口的自愈。** 路径恢复只在**换网**时重装那条 scoped 默认路由
	// (darwinUnderlayPlan 头一句就按 underlayGeneration 短路),而它会因为别的
	// 原因消失 —— 真机上就发生过:早上直连 446/失败 0,傍晚 *.qq.com 55/68 失败,
	// 而 `route -n get -ifscope en0 8.8.8.8` 是 not in table。
	// 这个循环去问内核,不信记账。只在 Hijack 真的装了路由时才跑。
	if !opts.NoHijack && runtime.GOOS == "darwin" {
		egressTicker := time.NewTicker(egressCheckInterval)
		teardowns.push("停止直连出口探测", egressTicker.Stop)
		workers.start(ctx, "direct-egress-repair", func(c context.Context) {
			watchDirectEgress(c, DirectEgressReachable, liveEgressRepair, egressTicker.C)
		})
	}

	var recoverer pathRecoverer
	if provider, ok := plat.(interface{ Underlay() underlayManager }); ok && !opts.NoHijack {
		underlay := provider.Underlay()
		initialUnderlay, observeErr := underlay.Observe(ctx)
		if observeErr != nil {
			log.Printf("网络路径恢复不可用:初始 underlay 观察失败: %v", observeErr)
		} else {
			verify := func(verifyCtx context.Context) error {
				if err := underlay.ValidateCapture(verifyCtx, tunH); err != nil {
					return err
				}
				state := runtimeState()
				if !state.TunnelHealthy {
					return fmt.Errorf("main transport is unhealthy")
				}
				if !state.DNSListening {
					return fmt.Errorf("DNS listener is unavailable")
				}
				if !state.RoutesInstalled {
					return fmt.Errorf("capture routes are not installed")
				}
				if state.UDPRequired && !state.UDPReady {
					return fmt.Errorf("UDP transport is unhealthy")
				}
				return nil
			}
			recoverer = &livePathRecoverer{
				underlay:     underlay,
				transports:   liveTransports,
				tun:          tunH,
				previous:     initialUnderlay,
				serverBypass: serverBypass,
				// 读共享集合而不是上面那份启动快照:切过服务器之后,一次 Wi-Fi
				// 切换就会让自动路径恢复用旧集合重装旁路,把刚切过去的那台漏在
				// 外面 —— 静默成环,而用户什么都没做。
				bypass:     bypassState,
				userBypass: cfg.Bypass,
				verify:     verify,
			}
		}
	}
	// status 用的传输信息。**主传输与 UDP 都实时读各自的 swapper。**
	//
	// 上一版 UDP 是启动时冻结的(注释还写着「UDP 静态」)—— 那在只有自动容灾
	// (只换主传输)时说得过去,而 `SetServer` 是两个槽一起换:热切之后 status
	// 会报一个错的 UDP 出口(2026-08-14 真机)。容灾列表仍是静态的,它本来就
	// 只是一份优先级清单,不随切换变化。
	var transportLabels []string
	if len(cfg.Transports) > 1 {
		for _, l := range cfg.Transports {
			transportLabels = append(transportLabels, transportLabel(l))
		}
	}
	var udpForInfo *transportSwapper
	if udpEnabled {
		udpForInfo = udpSwapper
	}
	transportInfo := transportInfoFrom(swapper, udpForInfo, transportLabels)
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
	// 按规则计数的**跨重启累计**:周期把本次运行的增量并进 DataDir 下那份历史。
	//
	// **它不看隧道健康,也不看 global/split 模式** —— 累计时长是「Core 在跑的
	// 时长」,而规则在隧道挂着时一样被判定;把那些时间排除掉等于悄悄降低门槛。
	//
	// 剪孤儿用的是**此刻**配置里的规则,不是启动快照:`bx direct/proxy` 会在运行期
	// 热加规则,拿启动那一刻的快照会把新加的当孤儿剪掉。读不出配置时**返回 nil**,
	// 而 pruneRuleHistory 对 nil 表会把所有带 Rule 的条目当孤儿剪光 —— 所以这里
	// 读失败时**退回启动快照**,宁可少剪也不要误剪(误剪的是累计,剪掉就回不来了)。
	currentRules := func() map[string]bool {
		rules := cfg.Rules // 兜底:读不出当前配置时用启动快照
		if opts.ConfigPath != "" {
			if nb, err := os.ReadFile(opts.ConfigPath); err == nil {
				if ncfg, err := config.Parse(nb); err == nil {
					rules = ncfg.Rules
				}
			}
		}
		out := map[string]bool{}
		for _, r := range rules {
			for _, d := range r.Direct {
				out[d] = true
			}
			for _, pr := range r.Proxy {
				out[pr] = true
			}
		}
		return out
	}
	acc := newRuleHistoryAccumulator(
		filepath.Join(cfg.DataDir, ruleHistoryFile),
		counters, version.Version, currentRules, time.Now,
	)
	historyDone := make(chan struct{})
	workers.start(ctx, "rule-history", func(c context.Context) {
		runRuleHistoryLoop(c, ruleHistoryInterval, acc, historyDone)
	})
	// **等最后那次写盘,但只等一个很短的上限。** 不等的话 Run 返回后进程可能
	// 先退出,那次写静默不发生 —— 而它带着自上一拍以来最长一个周期的增量。
	teardowns.push("等规则历史写盘", func() { waitRuleHistoryFlush(historyDone) })

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
		refresh := func(requiredLinks []string) (bool, error) { return refreshServerBypass(ctx, requiredLinks) }
		return serveControlWithPathRecovery(ctx, controlServeOptions{
			Counters:       counters,
			Tunnel:         lt,
			Server:         serverHost,
			Mode:           proxyMode(global, cfg.Mode),
			UDPMode:        cfg.UDP.Mode,
			TransportInfo:  transportInfo,
			Runtime:        runtimeState,
			Engine:         mutEng,
			Mutator:        mut,
			Reload:         reloadRouter,
			RefreshBypass:  refresh,
			Shutdown:       cancel,
			OwnerUID:       uint32(cfg.OwnerUID),
			Recoverer:      recoverer,
			ProbeDial:      direct,
			ConfigWarnings: riskyRuleWarnings(cfg),
			// 跨重启累计的按规则计数。**与本次运行那份并列发布,绝不合并** ——
			// 「0 次」在本次运行里什么也说明不了。
			RuleHistory: acc.snapshot,
			// 应用流量归因:接上真实的 *AppTraffic,GET /v0/apps 才不会恒 501。
			// 这是本轮修复的要害——之前只加了端点本身,没有从 Run 把它接进来。
			AppTraffic: appTraffic,
			// 判定查询:交出**跑着的那个 Dialer 自己**的只读方法,而不是
			// 另起一份判据。CLI 照配置重建一个 Router 来回答才是第二份 ——
			// bx 不热重载,盘上的配置与跑着的那个不一致的那一刻,恰恰是最
			// 需要这个命令的那一刻。
			Explain: d.Explain,
			// 服务器旁路重新跟随 DNS(bypass_refollow.go):主传输持续不健康时,
			// 在控制面那把锁里重解析服务器域名、变了就 rehijack + 重建传输。
			// 没有它,VPS 换 IP 之后 staticA/旁路永远钉在旧 IP 上,隧道永远重连
			// (真机 2026-08-06→09-03,NAS 静默断一个月)。--no-hijack 下没有
			// 旁路路由可跟随,不起。
			OnControlReady: func(hooks controlHooks) {
				if opts.NoHijack {
					return // 没有旁路路由可跟随、可修
				}
				currentLinks := func() []string {
					links := []string{swapper.currentLink()}
					if udpSwapper != nil {
						if u := udpSwapper.currentLink(); u != "" {
							links = append(links, u)
						}
					}
					return links
				}
				refollowTicker := time.NewTicker(bypassRefollowTick)
				teardowns.push("停止服务器旁路跟随", refollowTicker.Stop)
				workers.start(ctx, "server-bypass-refollow", func(c context.Context) {
					watchServerBypass(c, lt.Healthy, func(c context.Context) error {
						out, err := hooks.RefollowServerBypass(c, currentLinks())
						if err == nil && out == refollowChanged {
							log.Printf("server_bypass_refollow 已切到服务器的新地址")
						}
						return err
					}, refollowTicker.C)
				})
				// 服务器旁路路由自愈(bypass_route_repair.go):去问内核「发往服务器的包
				// 此刻走哪个接口」,走了我们的 TUN 就重新落实路由。休眠唤醒/网卡重连会
				// 把挂在物理网关上的 /32 冲掉而劫持路由照旧,隧道从此成环(真机
				// 2026-09-04)。只在 darwin 有探测原语,与 direct_egress 同一门槛。
				if runtime.GOOS == "darwin" {
					routeTicker := time.NewTicker(serverBypassCheckInterval)
					teardowns.push("停止服务器旁路路由自愈", routeTicker.Stop)
					workers.start(ctx, "server-bypass-route-repair", func(c context.Context) {
						watchServerBypassRoutes(c, func(c context.Context) (bool, bool, error) {
							return ServerBypassRoutesIntact(c, tunH.Name, bypassState.serverAddrs)
						}, hooks.ReassertRoutes, routeTicker.C)
					})
				}
			},
		})
	})
	if err != nil {
		return err
	}
	teardowns.push("关闭控制面", func() { _ = closer.Close() })
	teardowns.push("移除控制 socket", func() { _ = os.Remove(SockPath) })
	if err := os.WriteFile(PidPath, []byte(itoa(os.Getpid())), 0o644); err == nil {
		teardowns.push("移除 pid 文件", func() { _ = os.Remove(PidPath) })
	}

	// 6) 劫持默认路由(含 bypass 保 SSH + 服务器防环)。
	// --no-hijack:分步验证专用——隧道/TUN/引擎都已起,但**不劫持路由、不设 DNS、不装 WFP**,
	// 系统网络零改动。用于真机隔离验证「隧道能否健康 + TUN 能否起」而不冒断网/断 SSH 的风险。
	if opts.NoHijack {
		log.Printf("⚠️ --no-hijack:隧道+TUN+引擎已起,但未劫持路由/未设 DNS/未装 WFP(系统网络零改动)")
	} else {
		teardown, err := plat.Hijack(tunH, serverBypass, cfg.Bypass)
		if err != nil {
			return tagStartFailure(ErrHijack, fmt.Errorf("配置路由: %w", err))
		}
		routes.set(true)
		teardowns.push("还原默认路由", func() {
			routes.set(false)
			teardown()
		})
		log.Printf("%s", takeoverSummary(global, cfg.Mode, listsOverridden))
	}

	// 列表自动刷新(仅分流模式):隧道健康后周期经 socks5 拉最新列表热重载
	if !global && cfg.Lists.AutoUpdateEnabled() && !listsOverridden {
		workers.start(ctx, "china-list-refresh", func(c context.Context) {
			refreshLoop(c, cfg.Lists.RefreshInterval(), lt.Healthy, func() error {
				px, err := socksProxy(lt.SocksAddr(), &net.Dialer{Timeout: 10 * time.Second})
				if err != nil {
					return err
				}
				if err := fetchLists(c, proxyHTTPClient(px), cfg.DataDir); err != nil {
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
	// 关机 watchdog:还原已触发,下面的拆除若整体卡住超过 ShutdownGrace,
	// dump goroutine + 强制退出 —— 保证死手/信号一定终止进程,并捕获卡点根因。
	// 正常关机远快于 grace,watchdog 随进程退出自然作废、不触发。
	//
	// **它的角色自 2026-08-30 起变轻了,但不许删**:拆除已改由 teardownLedger
	// 逐步限时(teardown.go),单步挂住只损失那一步,曾经点名的嫌疑
	// (eng.Close/tun0.Stop)因此不再能堵住其余还原。watchdog 兜的是剩下的那
	// 一类:台账本身之外的挂点(比如上面某个尚未进台账的 defer),以及「多步都慢」
	// 的累加。单步预算与 grace 的关系由
	// TestTeardownStepBudgetLeavesRoomBeforeTheShutdownWatchdog 钉住 ——
	// 一步挂住绝不该把 watchdog 逼出来,因为它会跳过剩下的还原。
	armShutdownWatchdog(ShutdownGrace, dumpAndExit)
	return nil
}

// healthErrStderrLines 是附进健康失败消息的子进程日志行数。取几行而非全部:
// 目的是给出「为什么」的线索,不是把日志搬进错误消息。
const healthErrStderrLines = 5

// tunnelStderrSource 是 withTunnelStderr 需要的窄能力(*tunnel.Tunnel 满足它)。
// 收窄成接口而非直接吃 *tunnel.Tunnel,是为了这段判定能被单测覆盖。
type tunnelStderrSource interface {
	RecentStderr() []string
}

// withTunnelStderr 把传输子进程最近的 stderr 附到错误后面。
//
// 「bx 隧道健康检查超时(20s)」本身不说明任何原因——真机事故 2026-08-06 里,
// 运维只能拿到这一句,分不出是握手失败、超时还是被 reset,于是反复重装。
// 子进程那几行(已抹密)往往就是唯一能回答「为什么」的东西。
func withTunnelStderr(src tunnelStderrSource, err error) error {
	lines := src.RecentStderr()
	if len(lines) == 0 {
		return err
	}
	if len(lines) > healthErrStderrLines {
		lines = lines[len(lines)-healthErrStderrLines:]
	}
	return fmt.Errorf("%w\n  %s", err, strings.Join(lines, "\n  "))
}

// ensureSingboxBinary / ensureBrookBinary 是「释放内嵌传输二进制」这件事的产地。
//
// 抽出来有两个理由,后一个是承重的:① buildTunnel 里那五个 sing-box 分支逐字
// 相同;② 它们是 ErrProvision 的**唯一**产地,而长在 Run() 里那个闭包里时,
// 「这个哨兵真的会被产出吗」这个问题只能靠读代码回答 —— 抽成包级函数之后
// 它是可以被直接调用、直接断言的。
func ensureSingboxBinary(cfg *config.Config) (string, error) {
	path, err := provision.EnsureSingbox(cfg.DataDir, cfg.SingboxBin, embedded.Singbox(), embedded.SingboxVersion(), cfg.SingboxURL, cfg.SingboxSHA256)
	if err != nil {
		return "", tagStartFailure(ErrProvision, fmt.Errorf("准备 sing-box: %w", err))
	}
	return path, nil
}

func ensureBrookBinary(cfg *config.Config, opts Options) (string, error) {
	path, err := provision.EnsureBrook(cfg.DataDir, firstNonEmpty(opts.BrookBin, cfg.Brook), embedded.Brook(), embedded.BrookVersion(), cfg.BrookURL, cfg.BrookSHA256)
	if err != nil {
		return "", tagStartFailure(ErrProvision, fmt.Errorf("准备 brook: %w", err))
	}
	return path, nil
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
			// 裹上家长哨兵 ErrTunnelUnhealthy,**不裹两个具体结局中的任何一个** ——
			// 这一层只知道「20 秒了还不健康」,判别是下一步的事(tunneldiagnosis.go)。
			// 忘了判别就落回「没判出来」,而不是落到一个可能是错的答案上。
			return tagStartFailure(ErrTunnelUnhealthy,
				withTunnelStderr(t, fmt.Errorf("bx 隧道健康检查超时(%s): restarts=%d", timeout, s.Restarts)))
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
// 故此处只 Start + 挂载:未健康时 dialer 让 UDP 回落主传输(同一台 VPS、同一条加密隧道,不泄漏;
// 主传输也挂才按 killswitch fail-closed 阻断,见 dialer.SetUDPTransport),隧道连上后自动接管
// UDP/QUIC。socksAddr 在 New 时即固定分配,不依赖健康。
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

// ResolveAll 给出全部解析结果,供 bypass 刷新用(见 resolveAll 的注释:
// 服务器域名多条 A 记录时只覆盖其中一条就是成环)。
func (d *dnsResolver) ResolveAll(ctx context.Context, domain string) ([]netip.Addr, error) {
	ips, err := d.r.LookupNetIP(ctx, "ip", domain)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.Unmap())
	}
	return out, nil
}

// normalizeDNSServerAddr 给解析器地址补上默认端口。
//
// **config.Parse 会给 dns.split[].server 做这件事,而 overlay 那条路绕过了它。**
// 不补的后果不是「少个端口」:DialContext 直接报 `missing port in address`,
// Respond 把它变成 SERVFAIL,于是在任何检测到 Tailscale 的机器上,**每一次
// ts.net 查询都失败** —— 正好是这个功能要修的那件事。
func normalizeDNSServerAddr(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return server
	}
	if host, port, err := net.SplitHostPort(server); err == nil {
		// **`10.0.13.23:`(有冒号没端口)要落回默认端口,不能掉进下面那一支。**
		// 掉下去会把整个 `10.0.13.23:` 当主机名再拼一次端口,得到 `10.0.13.23::53`
		// —— 一个谁也拨不通的地址,而且它是**静默**的:解析器就此对这个租户
		// 的命名空间全部超时,而日志里没有任何东西说这是个畸形配置。
		if port == "" {
			port = "53"
		}
		return net.JoinHostPort(host, port)
	}
	return net.JoinHostPort(strings.Trim(server, "[]"), "53")
}

// overlaySplitPatterns 把一个后缀变成 DomainSet 认得的模式:后缀本身与它的子域。
func overlaySplitPatterns(suffix string) []string {
	return []string{"*." + suffix, suffix}
}

// transportInfoFrom 组出 status 要的三样:当前主传输、容灾清单、当前 UDP 传输。
//
// **两个 swapper 都实时读。** 抽出来是因为它原本长在 Run() 里 —— 那个组装根
// 测试进不去,而这个仓库全部的事故都在那种够不着的地方。
func transportInfoFrom(main, udp *transportSwapper, labels []string) func() (string, []string, string) {
	return func() (string, []string, string) {
		active := ""
		if main != nil {
			active = transportLabel(main.currentLink())
		}
		udpLabel := ""
		if udp != nil {
			// 没有专用 UDP 传输时留空 —— **不拿主传输冒充**,那会让用户以为
			// 自己配了按类分流。
			udpLabel = transportLabel(udp.currentLink())
		}
		return active, labels, udpLabel
	}
}
