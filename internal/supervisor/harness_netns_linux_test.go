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
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/socks5"
	"github.com/getbx/bx/internal/tunnel"
	"golang.org/x/sys/unix"
)

// ---------------------------------------------------------------------------
// 隔离
// ---------------------------------------------------------------------------

const (
	// harnessIsolatedEnv 非空即表示"本进程已经在台子的独立 namespace 里",
	// 用来终止 re-exec 递归。
	harnessIsolatedEnv = "BX_HARNESS_ISOLATED"

	// 假上行:空 netns 里只有一个 down 的 lo,而 Hijack 要 defaultRoute() 探到默认网关,
	// server bypass 路由也要 via 它。用 TEST-NET-3,与 TUN 的 TEST-NET-2、fake-IP 的
	// 198.18/15 都不冲突。
	harnessUplinkDev = "bxup0"
	harnessGateway   = "203.0.113.1"
	harnessUplinkIP  = "203.0.113.2/24"

	harnessChildTimeout = 3 * time.Minute
)

// runtimeMountPoint 是台子要盖 tmpfs 的那个目录,由 RuntimeDir 推导而非写死:
// 运行期路径若哪天搬家,隔离必须跟着搬,否则台子会退回去动宿主真实的 /run/bx。
var runtimeMountPoint = filepath.Dir(RuntimeDir)

// isolatedNamespaces 是台子要求独占的那几种 namespace。
//
// **mnt 与 net 同等重要,不是陪衬**:少了 mnt,tmpfs 会盖在宿主真实的 /run 上,
// control.go 的 os.Remove(SockPath) 删的就是宿主 bx 的控制 socket。
var isolatedNamespaces = []string{"net", "mnt"}

// cloneFlagFor 把 namespace 名映射到真实的 clone flag 常量名。
// 排查的人会照着这句话去改代码,所以它必须是真名 —— mnt 对应的是 CLONE_NEWNS,
// 不存在 CLONE_NEWMNT。
func cloneFlagFor(ns string) string {
	if ns == "mnt" {
		return "CLONE_NEWNS"
	}
	return "CLONE_NEW" + strings.ToUpper(ns)
}

// harnessOuterNSEnv 是父进程用来传递外层 namespace 标识的环境变量名。
// 子进程据此证明自己**真的**换了这套 namespace,而不是 Cloneflags 被静默忽略、
// 或者有人手工设了 harnessIsolatedEnv 就让台子在宿主 namespace 里开工。
func harnessOuterNSEnv(ns string) string {
	return "BX_HARNESS_OUTER_" + strings.ToUpper(ns) + "NS"
}

// enterIsolatedNetns 让本测试在一套**整进程**独占的 net + mount namespace 里运行。
//
// 为什么不是 brief 里那份 `runtime.LockOSThread() + unix.Unshare(...)`:unshare 只作用于
// **调用它的那一个线程**,而 Run() 是重度并发的。本机实测(privileged busybox 容器,
// 一个先热身出 8 个 M 的探针程序):锁线程 unshare 之后新起的 50 个 goroutine,
// 50/50 全部落在**外层** netns。也就是说 Run() 的 ip 命令、TUN 的 ioctl、监听 socket
// 会打在外面;mount namespace 同理,control.go 监听前那句 os.Remove(SockPath) 会删掉
// 宿主真实的 /run/bx/core.sock —— 正是 brief 点名要避免的那个灾难,而线程级 unshare
// 恰恰防不住它。
//
// 故改为把测试二进制在 CLONE_NEWNET|CLONE_NEWNS 下重新 exec 一份:子进程从单线程起步,
// 它此后创建的每一个线程都继承这套 namespace。namespace 随子进程退出销毁,宿主零残留。
func enterIsolatedNetns(t *testing.T) {
	t.Helper()
	if os.Getenv(harnessIsolatedEnv) != "" {
		prepareIsolatedNamespaces(t)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("需要 root(在特权容器或 CI 里跑:scripts/run-netns-tests.sh)")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("缺 ip 命令(iproute2)")
	}
	rerunInNewNamespaces(t) // 不返回:内部以 t.Skip/t.Fatal 结束本测试
}

// rerunInNewNamespaces 在新 net+mount namespace 里重跑当前这一个测试,并把子进程的
// 输出原样转述出来。子进程失败 → 本测试失败;子进程成功 → 本测试 Skip(断言都在子进程里跑过了)。
func rerunInNewNamespaces(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("定位测试二进制: %v", err)
	}
	env := append(os.Environ(), harnessIsolatedEnv+"=1")
	for _, ns := range isolatedNamespaces {
		id, err := os.Readlink("/proc/self/ns/" + ns)
		if err != nil {
			t.Fatalf("读当前 %s namespace: %v", ns, err)
		}
		env = append(env, harnessOuterNSEnv(ns)+"="+id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), harnessChildTimeout)
	defer cancel()
	cmd := exec.CommandContext(
		ctx, exe,
		"-test.run", "^"+regexp.QuoteMeta(t.Name())+"$",
		"-test.v",
		"-test.count=1",
	)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNET | syscall.CLONE_NEWNS,
		Pdeathsig:  syscall.SIGKILL, // 父进程被 go test 超时打死时,子进程不留下来占着 TUN
	}
	out, runErr := cmd.CombinedOutput()
	t.Logf("独立 net+mount namespace 子进程输出:\n%s", indentLines(string(out)))
	if runErr != nil {
		t.Fatalf("子进程里的 %s 失败: %v", t.Name(), runErr)
	}
	// 退出码 0 不等于跑过了:被 -test.run 过滤掉、或自己 Skip 掉,退出码同样是 0。
	// 这条断言让"台子其实没跑"没法伪装成绿灯。
	if !bytes.Contains(out, []byte("--- PASS: "+t.Name())) {
		t.Fatalf("子进程退出码为 0 却没有 %s 的 PASS 行 —— 它可能被跳过或根本没跑:\n%s", t.Name(), out)
	}
	t.Skip("断言已在子进程的独立 net+mount namespace 内跑完(输出见上)")
}

func indentLines(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  | " + l
	}
	return strings.Join(lines, "\n")
}

// prepareIsolatedNamespaces 在子进程内把 namespace 布置成台子需要的样子。
func prepareIsolatedNamespaces(t *testing.T) {
	t.Helper()
	assertProcessWideIsolation(t)

	// 让本 mount ns 的挂载不外泄到宿主。
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatalf("把根挂载改私有: %v", err)
	}
	// /run 换成 tmpfs:SockPath 与 core.pid 就此落在一次性文件系统里,
	// 宿主上正在跑的 bx 完全看不见,也不会被 control.go 的 os.Remove 掉。
	//
	// 挂载点得先存在(busybox 镜像里没有 /run)。注意 **MS_PRIVATE 挡的是挂载传播,
	// 不是文件写入** —— 新 mount namespace 看到的仍是同一个底层文件系统,这里 MkdirAll
	// 建出来的目录是真的落在盘上的。故只在缺失时建,并在收尾时卸载 + 删掉,不留痕。
	if _, err := os.Stat(runtimeMountPoint); os.IsNotExist(err) {
		if err := os.MkdirAll(runtimeMountPoint, 0o755); err != nil {
			t.Fatalf("建 %s 挂载点: %v", runtimeMountPoint, err)
		}
		t.Cleanup(func() {
			_ = unix.Unmount(runtimeMountPoint, 0)
			_ = os.Remove(runtimeMountPoint) // 只删我们建的那个空目录;非空/不存在都会失败而无害
		})
	}
	if err := unix.Mount("tmpfs", runtimeMountPoint, "tmpfs", 0, ""); err != nil {
		t.Fatalf("在 %s 挂 tmpfs: %v", runtimeMountPoint, err)
	}
	// 注意这条断言证明的是"台子看不见宿主的 socket",**不能**用来证明隔离生效
	//(漏掉 CLONE_NEWNS 时它照样通过,因为 socket 是被自己的 tmpfs 盖住的)。
	// 真正证明隔离的是上面的 assertProcessWideIsolation。
	if _, err := os.Stat(SockPath); err == nil {
		t.Fatalf("tmpfs 挂上之后 %s 仍然可见 —— 隔离没生效", SockPath)
	}

	mustIP(t, "link", "set", "lo", "up")
	// 假上行 + 默认路由:Hijack 的 defaultRoute() 在只有 lo 的 netns 里必然失败。
	// dummy 设备不发任何包,网关也不需要真的存在(路由表接受 on-link 网关)。
	mustIP(t, "link", "add", harnessUplinkDev, "type", "dummy")
	mustIP(t, "addr", "add", harnessUplinkIP, "dev", harnessUplinkDev)
	mustIP(t, "link", "set", harnessUplinkDev, "up")
	mustIP(t, "route", "add", "default", "via", harnessGateway, "dev", harnessUplinkDev)
}

// assertProcessWideIsolation 证明隔离**既真的换过**、又是**整进程**的。
//
// 两条缺一不可,而且对 net 和 mnt 都要查:
//   - 换没换(与父进程传来的外层标识比):Cloneflags 少写一个、或者有人手工设了
//     harnessIsolatedEnv 直接跑,进程会安安静静地留在宿主 namespace 里。少了 mnt 这半边,
//     漏掉 CLONE_NEWNS 的台子会把 tmpfs 盖在**宿主真实的 /run** 上、把宿主 bx 的控制
//     socket 删掉,然后报绿 —— 下面那条 os.Stat(SockPath) 的不可见断言此时**恰恰会通过**,
//     因为 socket 正是被自己的 tmpfs 盖住/删掉的。
//   - 是不是整进程(逐线程比):线程级 unshare 下本进程会有线程留在外层 namespace,
//     而 Run() 的 goroutine 恰好就跑在那些线程上。
//
// 外层标识**缺席即 fatal**,不是"跳过这一条":缺席只可能发生在没有经过
// rerunInNewNamespaces 的路径上,而那正是最需要拦住的情形。
func assertProcessWideIsolation(t *testing.T) {
	t.Helper()
	for _, ns := range isolatedNamespaces {
		self, err := os.Readlink("/proc/self/ns/" + ns)
		if err != nil {
			t.Fatalf("读 %s namespace: %v", ns, err)
		}
		// 外层参照必须**不可伪造**。它此前来自父进程写进环境变量的 id ——
		// 复审把那一行改成写死的假 id、同时去掉 CLONE_NEWNS,台子照样 PASS,
		// 跑完把外层的 /run 用 tmpfs 盖住、宿主的 core.sock 删掉:正是本守卫要防的那件事。
		// 更糟的是下面那条「socket 不可见」的检查会**确认错的东西** —— 它通过恰恰
		// 因为 tmpfs 把宿主的 socket 藏起来了。信自己的记账而不去问内核,是这个项目
		// 在别处反复点名的反模式,这里不能再犯。
		//
		// /proc/1 永远在外层:容器里 PID 1 是父进程(它不进新 namespace),裸机上是 init。
		// 子进程伪造不了它。
		//
		// 注意:将来若给 Cloneflags 加上 CLONE_NEWPID,/proc/1 就变成子进程自己、
		// self == outer,本守卫会**响亮失败** —— 方向是安全的,但届时要连它一起重写。
		outer, err := os.Readlink("/proc/1/ns/" + ns)
		if err != nil {
			t.Fatalf("读 PID 1 的 %s namespace(外层参照): %v", ns, err)
		}
		if outer == self {
			t.Fatalf("没有真的换 %s namespace:仍与 PID 1 同在 %s(Cloneflags 少了 %s?)",
				ns, self, cloneFlagFor(ns))
		}
		// 父进程传来的那份保留为冗余交叉核对:两者不一致说明有人在中间做了手脚。
		if declared := os.Getenv(harnessOuterNSEnv(ns)); declared != "" && declared != outer {
			t.Fatalf("父进程声称的外层 %s namespace 是 %s,而 PID 1 实际在 %s —— 对不上",
				ns, declared, outer)
		}
		entries, err := os.ReadDir("/proc/self/task")
		if err != nil {
			t.Fatalf("枚举本进程线程: %v", err)
		}
		for _, e := range entries {
			link := filepath.Join("/proc/self/task", e.Name(), "ns", ns)
			got, err := os.Readlink(link)
			if err != nil {
				continue // 线程可能刚退出;不因此判失败
			}
			if got != self {
				t.Fatalf("线程 %s 的 %s namespace 是 %s,与进程的 %s 不一致 —— 隔离不是整进程的",
					e.Name(), ns, got, self)
			}
		}
	}
}

// ipOut 在当前 netns 执行 ip 命令并返回其输出(基线比对用)。
func ipOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ip %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

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
}

func newFakeTunnels(t *testing.T) *fakeTunnels {
	t.Helper()
	return &fakeTunnels{socksAddr: startFakeSocks5(t)}
}

func (f *fakeTunnels) build(link, recoveryID string, auxiliaryHTTP bool) (*tunnel.Tunnel, error) {
	f.mu.Lock()
	f.requests = append(f.requests, fakeTunnelRequest{link, recoveryID, auxiliaryHTTP})
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
		case <-time.After(shutdownGrace + 15*time.Second):
			// 到这里 Run() 自己的关机 watchdog(shutdownGrace)其实已经先一步
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

// 台子跑的是 servers:/current: 那一支,故"每一条配置过的链接都要有 bypass 路由"
// 这条防环不变量在这里是**真的**被生产代码走过的(transports: 那支是另一套语义)。
func TestHarnessRunsTheServersBranchWithEveryLinkBypassed(t *testing.T) {
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
