//go:build integration && linux

// harness_netns_linux_test.go 是集成台的骨架:在一次性的 net+mount namespace 里跑
// **完整的 supervisor.Run()**,用假隧道替掉唯一那件会发真实外网流量的事,其余
// (平台、TUN、策略路由、控制面、DNS、屏障)全走生产代码。
//
// 存在的理由:Run() 是 578 行的组装根,直到 2026-08-08 都没有任何测试调用过它,
// 于是整条建隧道分支的执行覆盖率是 0;那一轮的两个 Critical 就住在其中六行接线里,
// 而围着它们的每一个被抽出来的单元都测得很好。台子的价值不是"再测一遍那些单元",
// 是让断言打在**内核状态**上而不是源码文本上。
package supervisor

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/netnsguard"
	"github.com/getbx/bx/internal/socks5"
	"github.com/getbx/bx/internal/tunnel"
)

// ---------------------------------------------------------------------------
// 隔离
// ---------------------------------------------------------------------------
//
// **隔离机制已下沉到 internal/netnsguard(2026-08-30)**,guardian 的台子要用
// 同一套。它写错的后果不是测试红,是把 tmpfs 盖在宿主真实的 /run 上、删掉宿主
// bx 的控制 socket —— 这种判据只能有一份,各抄一份就是给那个后果开两次机会。
// 全部推导(为什么不是线程级 unshare、为什么外层参照取 /proc/1、为什么 mnt 与
// net 同等重要)原样搬进了那个包。

const (
	// 假上行:空 netns 里只有一个 down 的 lo,而 Hijack 要 defaultRoute() 探到默认网关,
	// server bypass 路由也要 via 它。用 TEST-NET-3,与 TUN 的 TEST-NET-2、fake-IP 的
	// 198.18/15 都不冲突。
	harnessUplinkDev = "bxup0"
	harnessGateway   = "203.0.113.1"
	harnessUplinkIP  = "203.0.113.2/24"
)

// runtimeMountPoint 是台子要盖 tmpfs 的那个目录,由 RuntimeDir 推导而非写死:
// 运行期路径若哪天搬家,隔离必须跟着搬,否则台子会退回去动宿主真实的 /run/bx。
var runtimeMountPoint = filepath.Dir(RuntimeDir)

// enterIsolatedNetns 让本测试在一套**整进程**独占的 net + mount namespace 里运行。
func enterIsolatedNetns(t *testing.T) {
	t.Helper()
	netnsguard.Enter(t, netnsguard.Options{
		MountPoint:  runtimeMountPoint,
		HiddenPaths: []string{SockPath},
		Uplink: netnsguard.Uplink{
			Dev: harnessUplinkDev, Addr: harnessUplinkIP, Gateway: harnessGateway,
		},
	})
}

// ipOut/ipQuiet 是本包沿用的旧名,转调共享实现 —— 台子里几十处调用点
// 不必跟着改名,而判据仍然只有一份。
func ipOut(t *testing.T, args ...string) string {
	t.Helper()
	return netnsguard.MustIP(t, args...)
}

func ipQuiet(args ...string) (string, error) { return netnsguard.IPQuiet(args...) }

// ---------------------------------------------------------------------------
// 假隧道
// ---------------------------------------------------------------------------

// fakeTunnels 是台子那一侧的"传输工厂":进程内 socks5 服务端 + 不拉子进程的 Runner
// + 立即健康,并**记下每一次被要求建的链接**。tunnel.New 本就是导出的,故台子不碰
// internal/tunnel 的任何内部结构。
//
// 记链接不是为了好看:切服务器(/v0/server)之后"到底切到哪一台去了"只有这里知道 ——
// 从外面看,健康的假隧道长得都一样。
type fakeTunnels struct {
	socksAddr string

	mu       sync.Mutex
	requests []fakeTunnelRequest
}

type fakeTunnelRequest struct {
	Link          string
	RecoveryID    string
	AuxiliaryHTTP bool
	// RoutesAtBuildErr 与 RoutesAtBuild **必须分开存**,不能把错误折进字符串。
	//
	// 折进去只对「找到某个 IP 才算通过」的正极性断言安全:ip 自己失败时错误文本里
	// 当然不含那个 IP,断言照样红。但反极性的断言(「屏障开口里**不得**含用户
	// hosts 覆盖」正是这一种)会因为同一个原因**静默通过** —— ip 挂了,于是什么都
	// 没找到,于是"没有不该有的东西",绿灯。
	//
	// 观测不到与观测到"没有",是两件事。这是本项目在 internal/observe 里已经用
	// 三态 Tristate 表达过的同一条原则,这里不能退回二值。
	RoutesAtBuildErr error
	// RoutesAtBuild 是**建这条隧道那一刻**的 table 100 快照。
	//
	// 它是「先装 bypass 路由、再把传输换过去」那条顺序不变量的唯一观测点:两种顺序的
	// **终态完全相同**(路由最后都装上了),差别只是一个转瞬即逝的窗口 —— 传输已经换到
	// 新服务器、而它的 bypass 路由还没装,隧道自己连服务器的流量在那一瞬被劫进 TUN。
	// 只比 before/after 的断言在结构上就看不见窗口,不管写得多仔细。
	// swapTo 正是在窗口里调本工厂的,故在这里问一次内核就等于站到了窗口中间。
	RoutesAtBuild string
}

func newFakeTunnels(t *testing.T) *fakeTunnels {
	t.Helper()
	return &fakeTunnels{socksAddr: startFakeSocks5(t)}
}

func (f *fakeTunnels) build(link, recoveryID string, auxiliaryHTTP bool) (*tunnel.Tunnel, error) {
	// 先问内核再上锁:ipQuiet 要 fork/exec 一个进程,持锁做它会把并发的建隧道请求
	// 串起来 —— 那会改变被观测对象本身的时序。
	routes, routesErr := ipQuiet("route", "show", "table", itoa(routeTable))
	f.mu.Lock()
	f.requests = append(f.requests, fakeTunnelRequest{
		Link: link, RecoveryID: recoveryID, AuxiliaryHTTP: auxiliaryHTTP,
		RoutesAtBuild: routes, RoutesAtBuildErr: routesErr,
	})
	f.mu.Unlock()
	return tunnel.New(
		f.socksAddr,
		func(string) (tunnel.Runner, error) { return newNoopRunner(), nil },
		func(string) (int64, error) { return 1, nil },
	), nil
}

// requests 返回至今为止的建隧道请求(按发生顺序)。
func (f *fakeTunnels) snapshot() []fakeTunnelRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeTunnelRequest(nil), f.requests...)
}

// links 返回至今为止被要求建的链接(按发生顺序)。
func (f *fakeTunnels) links() []string {
	var out []string
	for _, r := range f.snapshot() {
		out = append(out, r.Link)
	}
	return out
}

// fakeTunnelBuilder 是 fakeTunnels 的薄封装,给不关心"建过哪些链接"的调用方用。
func fakeTunnelBuilder(t *testing.T) func(string, string, bool) (*tunnel.Tunnel, error) {
	t.Helper()
	return newFakeTunnels(t).build
}

// noopRunner 冒充传输子进程:Wait() 阻塞(真隧道进程也不会自己退出),Kill() 解除阻塞。
//
// Kill() 必须真的让 Wait() 返回。tunnel.runOnce 的 defer 是 `r.Kill(); <-exitCh`,
// 而 exitCh 由 `go func(){ r.Wait(); close(exitCh) }()` 关闭 —— Wait() 若像 brief 里
// 写的那样 `select {}` 永不返回,Tunnel.Stop() 就永远等不到 t.done,Run() 的 teardown
// 死在第一个 defer 上,15s 后被关机 watchdog os.Exit(1) 掉。实测过,不是推断。
type noopRunner struct {
	stopped chan struct{}
	once    sync.Once
}

func newNoopRunner() *noopRunner { return &noopRunner{stopped: make(chan struct{})} }

func (r *noopRunner) Wait() error { <-r.stopped; return nil }

func (r *noopRunner) Kill() error {
	r.once.Do(func() { close(r.stopped) })
	return nil
}

// startFakeSocks5 起一个最小 SOCKS5 服务端(no-auth + CONNECT),返回其地址。
// 它**不真的拨出去**:台子跑在一个只有 lo 和一条假默认路由的 netns 里,任何真实外连
// 都会失败;CONNECT 应答成功之后把连接当 echo 用,后续任务据此断言字节确实走到了隧道口。
// internal/socks5 里那份 fakeServer 是 test-scoped 且只实现 UDP ASSOCIATE,跨包用不了。
func startFakeSocks5(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听假 socks5: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeSocks5(c)
		}
	}()
	return ln.Addr().String()
}

func serveFakeSocks5(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(br, greeting); err != nil || greeting[0] != 5 {
		return
	}
	if _, err := io.CopyN(io.Discard, br, int64(greeting[1])); err != nil {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil { // no-auth
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil {
		return
	}
	var addrLen int
	switch req[3] {
	case 1: // IPv4
		addrLen = 4
	case 3: // 域名
		l := make([]byte, 1)
		if _, err := io.ReadFull(br, l); err != nil {
			return
		}
		addrLen = int(l[0])
	case 4: // IPv6
		addrLen = 16
	default:
		_, _ = c.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0}) // 地址类型不支持
		return
	}
	if _, err := io.CopyN(io.Discard, br, int64(addrLen)+2); err != nil { // 地址 + 端口
		return
	}
	if req[1] != 1 { // 只认 CONNECT
		_, _ = c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	_, _ = io.Copy(c, br) // echo
}

// ---------------------------------------------------------------------------
// 起停 Run()
// ---------------------------------------------------------------------------

// harnessTunName 是台子建的 TUN 名字(= 生产默认值,不另起炉灶)。
const harnessTunName = "bx0"

// twoServerHarnessConfig 是台子的基准配置,**刻意用 servers:/current: 这一支**。
//
// 为什么不是 transports:(骨架初版用的就是那个,已改掉)——`bypassLinks` 有两条分支,
// 语义并不相同:transports 那支把每条链接都标 Required、且没有"当前服务器"与 s.UDP 的
// 概念;servers 那支只把 current 标 Required,UDP 伴随传输从 s.UDP 取。在 transports 上
// 写的断言("每台配过的服务器都在 bypass 里")会在生产真正走的 servers 分支退化时
// —— 漏掉非当前服务器、或漏掉 UDP 伴随 —— 继续绿着,而那正是静默成环本身。
// 且 /v0/server 切服务器整条路径只在 servers 模型下才存在。
//
//   - global: true —— Run() 因此整段跳过 china 列表的准备(不下载、不读内嵌),
//     启动路径里再没有任何需要联网或落大文件的事。
//   - 每台都带 udp: 伴随 —— 四条链接(2 台 × 主+UDP)全都该进 bypass。
//   - 地址全用 IP 字面量 —— 启动路径一次 DNS 都不做(netns 里也没有 DNS 可用)。
//
// data_dir 由 startHarness 追加 t.TempDir()(常量里写不了),整份 YAML 会落到临时文件
// 并经 Options.ConfigPath 交给 Run —— 少了它,/v0/server 会以
// 「未知配置路径,无法刷新 bypass」直接 500,切服务器整条路根本走不到。
const twoServerHarnessConfig = `global: true
current: alpha
servers:
  - name: alpha
    link: vless://u@203.0.113.10:443
    udp: hysteria2://u@203.0.113.11:443
  - name: beta
    link: vless://u@203.0.113.12:443
    udp: hysteria2://u@203.0.113.13:443
`

// failoverHarnessConfig 保住 transports: 那一支的覆盖(len(cfg.Transports)>1 才会
// 启动 runFailover)。基准配置搬去 servers: 之后,没有它这条分支就再没人跑过。
const failoverHarnessConfig = `global: true
transports:
  - vless://u@203.0.113.10:443
  - vless://u@203.0.113.12:443
`

type harness struct {
	t          *testing.T
	sockPath   string
	configPath string
	tunName    string
	tunnels    *fakeTunnels
	cancel     context.CancelFunc
	done       chan error
	stopOnce   sync.Once
}

// startHarness 用给定配置在当前(已隔离的)namespace 里跑起完整的 Run(),
// 等到**路由真的装好**才返回。
func startHarness(t *testing.T, cfgYAML string) *harness {
	t.Helper()
	full := cfgYAML + "data_dir: " + t.TempDir() + "\n"
	cfg, err := config.Parse([]byte(full))
	if err != nil {
		t.Fatalf("解析台子配置: %v", err)
	}
	// 配置必须真的落盘:Run 的 ConfigPath 为空时 newBypassRefresher 直接短路,
	// /v0/server 会以「未知配置路径,无法刷新 bypass」500 —— 切服务器整条路不可达。
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(full), 0o600); err != nil {
		t.Fatalf("写台子配置文件: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{
		t: t, sockPath: SockPath, configPath: configPath, tunName: harnessTunName,
		tunnels: newFakeTunnels(t), cancel: cancel, done: make(chan error, 1),
	}
	opts := Options{
		TunName:       h.tunName,
		TunAddr:       "198.51.100.1/30",
		MTU:           1500,
		Probe:         "203.0.113.10:443", // 假健康检查不看它;留真实形状便于读日志
		HealthTimeout: 15 * time.Second,
		ConfigPath:    configPath,
		BuildTunnel:   h.tunnels.build,
	}
	go func() { h.done <- Run(ctx, cfg, opts) }()
	t.Cleanup(h.stop)

	// 里程碑一:控制 socket。它在建隧道 / 等健康 / DNS / TUN / 引擎之后创建。
	h.awaitStartup(t, 60*time.Second, func() (bool, string) {
		_, err := os.Stat(h.sockPath)
		return err == nil, "控制 socket " + h.sockPath + " 未出现"
	})
	// 里程碑二:路由真的装上了。**socket 早于 Hijack**,拿 socket 当"起好了"会让
	// 后续断言撞上一个空的 table 100(实测两次同样的探针:一次空、一次满)。
	// RoutesInstalled 由 Hijack 成功后紧接着的 routes.set(true) 置位,是唯一权威信号。
	if !opts.NoHijack {
		h.awaitStartup(t, 30*time.Second, func() (bool, string) {
			state, err := FetchRuntimeState(h.sockPath)
			if err != nil {
				return false, "读运行期状态失败: " + err.Error()
			}
			return state.RoutesInstalled, "RoutesInstalled 仍为 false(Hijack 还没装完路由)"
		})
	}
	return h
}

// awaitStartup 轮询一个启动里程碑,期间 Run() 一旦提前返回就立刻如实报出来
// (而不是干等到超时,把根因埋掉)。
func (h *harness) awaitStartup(t *testing.T, timeout time.Duration, ready func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var why string
	for {
		var ok bool
		if ok, why = ready(); ok {
			return
		}
		select {
		case runErr := <-h.done:
			// Run() 已经返回,stop() 不必再等它 —— 否则 Fatalf 触发的 Cleanup 会挂死。
			h.stopOnce.Do(func() {})
			t.Fatalf("Run() 在启动完成前就返回了: %v(当时:%s)", runErr, why)
		default:
		}
		if time.Now().After(deadline) {
			h.cancel()
			t.Fatalf("等启动里程碑超时(%s):%s", timeout, why)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stop 取消 ctx 并等 Run() 返回(即完整走完 defer 还原链)。
func (h *harness) stop() {
	h.stopOnce.Do(func() {
		h.cancel()
		select {
		case err := <-h.done:
			if err != nil {
				h.t.Errorf("Run() 应干净返回,却报错: %v", err)
			}
		case <-time.After(ShutdownGrace + 15*time.Second):
			// 到这里 Run() 自己的关机 watchdog(ShutdownGrace)其实已经先一步
			// os.Exit(1) 掉整个进程了;留这条分支是为了万一它没触发也能得到一句话。
			h.t.Errorf("ctx 取消后 Run() 迟迟不返回(teardown 卡住)")
		}
	})
}

// ---------------------------------------------------------------------------
// 还原基线
// ---------------------------------------------------------------------------

// harnessBaseline 是台子开工之前的内核状态。
//
// 只比 `ip rule list` 是不够的 —— 那是 **IPv4-only**,而且对设备、路由表内容、
// 控制 socket 一个字都没说。实测:把 netConf.downSteps 的 `link del` 删掉、或者把
// v6 那半段整个删掉,只比 v4 rule 的断言**照样全绿**,而 namespace 里留着一个 bx0、
// 一条 `unreachable default` 在 table 100、以及一整套 v6 规则 —— 也就是 bx 退出之后
// 全局 IPv6 仍然被黑洞掉。
type harnessBaseline struct {
	rules4 string
	rules6 string
}

// pidPathLeftBehind 报告 core.pid 是否残留。
//
// 它值得单独一条:PidPath 的 defer os.Remove **只在写入成功时**注册,所以一个残留的
// core.pid 今天完全不可见。而「陈旧的进程记录」正是 2026-08-05/06 两次真实事故
// (core-process.json 指向已死 PID,bx up 永久 500)的形状。
func pidPathLeftBehind() bool {
	_, err := os.Stat(PidPath)
	return err == nil
}

func captureBaseline(t *testing.T) harnessBaseline {
	t.Helper()
	return harnessBaseline{
		rules4: ipOut(t, "rule", "list"),
		rules6: ipOut(t, "-6", "rule", "list"),
	}
}

// assertRestored 断言台子停掉之后,内核状态回到基线且不留任何残留。
func (b harnessBaseline) assertRestored(t *testing.T, h *harness) {
	t.Helper()
	if after := ipOut(t, "rule", "list"); after != b.rules4 {
		t.Errorf("v4 策略规则未干净还原:\n--- base ---\n%s\n--- after ---\n%s", b.rules4, after)
	}
	if after := ipOut(t, "-6", "rule", "list"); after != b.rules6 {
		t.Errorf("v6 策略规则未干净还原(bx 退出后仍在黑洞 IPv6?):\n--- base ---\n%s\n--- after ---\n%s", b.rules6, after)
	}
	if links := ipOut(t, "link", "show"); strings.Contains(links, h.tunName) {
		t.Errorf("TUN 设备 %s 未被移除:\n%s", h.tunName, links)
	}
	for _, args := range [][]string{
		{"route", "show", "table", itoa(routeTable)},
		{"-6", "route", "show", "table", itoa(routeTable)},
	} {
		if got := strings.TrimSpace(ipOut(t, args...)); got != "" {
			t.Errorf("ip %s 未清空:\n%s", strings.Join(args, " "), got)
		}
	}
	if _, err := os.Stat(h.sockPath); err == nil {
		t.Errorf("控制 socket %s 未被删除", h.sockPath)
	}
	if pidPathLeftBehind() {
		t.Errorf("%s 未被删除 —— 陈旧的进程记录正是 2026-08-05/06 两次事故的形状", PidPath)
	}
}

// ---------------------------------------------------------------------------
// 测试
// ---------------------------------------------------------------------------

// 台子绝不能碰到宿主上正在运行的 bx:SockPath 是包级常量 /run/bx/core.sock,
// 而 netns **不隔离文件系统**,control.go 在监听前还会 os.Remove(SockPath)。
// 所以必须连 mount namespace 一起 unshare,并在 /run 上挂 tmpfs。
// 这一条是硬要求 —— 少了它,在开着 bx 的机器上跑一次台子就会夺走它的控制 socket。
func TestHarnessStartsAndRestoresCleanly(t *testing.T) {
	enterIsolatedNetns(t)
	base := captureBaseline(t)

	h := startHarness(t, twoServerHarnessConfig)
	if _, err := os.Stat(h.sockPath); err != nil {
		t.Fatalf("控制 socket 应已就绪: %v", err)
	}
	// startHarness 返回时路由必须已经装上(而不只是 socket 出现了)——
	// 这条断言把那个"先到的里程碑"与"要的那个里程碑"之差钉死。
	if got := ipOut(t, "route", "show", "table", itoa(routeTable)); !strings.Contains(got, "default") {
		t.Fatalf("startHarness 返回时 table %d 里还没有 default(拿 socket 当起好了?):\n%s", routeTable, got)
	}

	h.stop()
	base.assertRestored(t, h)
}

// 清单里**每一台**服务器的**两条**链接对应的 IP,都必须真的在内核路由表里 ——
// 少一条,切过去之后隧道自己连服务器的那条流量就会被劫进 TUN = 成环。
//
// **这不是一条管道连通性检查,别当它是。** 成环是**静默**的:隧道连得上、
// bx status 显绿、没有任何一处报错,流量只是绕圈或者从错的地方出去。这条不变量
// 走过五轮修复四轮复审,而在此之前保护它的全部是对抽出来的纯函数的断言 ——
// 那些断言证明不了内核里真的有那几条路由。这里是第一次问内核。
//
// 台子跑的是 servers:/current: 那一支,故这条不变量在这里是**真的**被生产代码
// 走过的(transports: 那支 Required 语义与 s.UDP 处理都不同,是另一套)。
func TestHarnessBypassCoversEveryConfiguredServerElseSilentLoop(t *testing.T) {
	enterIsolatedNetns(t)
	base := captureBaseline(t)

	h := startHarness(t, twoServerHarnessConfig)
	table := ipOut(t, "route", "show", "table", itoa(routeTable))
	for _, want := range []string{
		"203.0.113.10", // alpha 主链接(current)
		"203.0.113.11", // alpha 的 UDP 伴随
		"203.0.113.12", // beta 主链接(非 current,但仍必须旁路)
		"203.0.113.13", // beta 的 UDP 伴随
	} {
		if !strings.Contains(table, want) {
			t.Errorf("服务器 %s 没有 bypass 路由(它的流量会落回 TUN = 成环):\n%s", want, table)
		}
	}
	// 当前那台的主链接与 UDP 伴随都得真的被建成隧道。
	links := h.tunnels.links()
	for _, want := range []string{
		"vless://u@203.0.113.10:443",
		"hysteria2://u@203.0.113.11:443",
	} {
		if !slices.Contains(links, want) {
			t.Errorf("没有经注入缝建过 %q;建过的是 %v", want, links)
		}
	}

	h.stop()
	base.assertRestored(t, h)
}

// transports: 那一支(自动容灾)在基准配置搬去 servers: 之后仍要有人跑过。
func TestHarnessAlsoStartsTheFailoverTransportsBranch(t *testing.T) {
	enterIsolatedNetns(t)
	base := captureBaseline(t)

	h := startHarness(t, failoverHarnessConfig)
	table := ipOut(t, "route", "show", "table", itoa(routeTable))
	for _, want := range []string{"203.0.113.10", "203.0.113.12"} {
		if !strings.Contains(table, want) {
			t.Errorf("容灾清单里的 %s 没有 bypass 路由:\n%s", want, table)
		}
	}

	h.stop()
	base.assertRestored(t, h)
}

// 假 socks5 服务端是台子唯一自己写的协议实现,而健康检查是个常量、永远不会去拨它 ——
// 不专门拨一次,就没人证明过它真的会说 SOCKS5。这里用**生产的** socks5 客户端拨。
func TestHarnessFakeSocks5SpeaksTheProtocol(t *testing.T) {
	addr := startFakeSocks5(t)
	d, err := socks5.NewDialer(addr, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("建 socks5 客户端: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", "203.0.113.99:443")
	if err != nil {
		t.Fatalf("经假 socks5 CONNECT: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("写: %v", err)
	}
	buf := make([]byte, 4)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("读回显: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("回显 = %q, want %q", buf, "ping")
	}
}
