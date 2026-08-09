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
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/getbx/bx/internal/config"
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
	// harnessOuterNetnsEnv 由父进程写入外层 netns 的标识,子进程据此证明自己真的换了 ns
	//(而不是 Cloneflags 被静默忽略之后照常在外面跑)。
	harnessOuterNetnsEnv = "BX_HARNESS_OUTER_NETNS"

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
	outerNet, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Fatalf("读当前 netns: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), harnessChildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe,
		"-test.run", "^"+regexp.QuoteMeta(t.Name())+"$",
		"-test.v",
		"-test.count=1",
	)
	cmd.Env = append(os.Environ(),
		harnessIsolatedEnv+"=1",
		harnessOuterNetnsEnv+"="+outerNet,
	)
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
	// 挂载点得先存在(busybox 镜像里没有 /run);这一步已在私有 mount ns 内,不外泄。
	if err := os.MkdirAll(runtimeMountPoint, 0o755); err != nil {
		t.Fatalf("建 %s 挂载点: %v", runtimeMountPoint, err)
	}
	if err := unix.Mount("tmpfs", runtimeMountPoint, "tmpfs", 0, ""); err != nil {
		t.Fatalf("在 %s 挂 tmpfs: %v", runtimeMountPoint, err)
	}
	if _, err := os.Stat(SockPath); err == nil {
		t.Fatalf("tmpfs 挂上之后 %s 仍然可见 —— 隔离没生效,再往下跑会夺走宿主 bx 的控制 socket", SockPath)
	}

	mustIP(t, "link", "set", "lo", "up")
	// 假上行 + 默认路由:Hijack 的 defaultRoute() 在只有 lo 的 netns 里必然失败。
	// dummy 设备不发任何包,网关也不需要真的存在(路由表接受 on-link 网关)。
	mustIP(t, "link", "add", harnessUplinkDev, "type", "dummy")
	mustIP(t, "addr", "add", harnessUplinkIP, "dev", harnessUplinkDev)
	mustIP(t, "link", "set", harnessUplinkDev, "up")
	mustIP(t, "route", "add", "default", "via", harnessGateway, "dev", harnessUplinkDev)
}

// assertProcessWideIsolation 证明隔离是**整进程**的,不是某一个线程的。
// 这条断言直接钉死上面注释里那个失败模式:线程级 unshare 下,本进程会有线程留在
// 外层 namespace,而 Run() 的 goroutine 恰好就跑在那些线程上。
func assertProcessWideIsolation(t *testing.T) {
	t.Helper()
	for _, ns := range []string{"net", "mnt"} {
		self, err := os.Readlink("/proc/self/ns/" + ns)
		if err != nil {
			t.Fatalf("读 %s namespace: %v", ns, err)
		}
		if ns == "net" {
			if outer := os.Getenv(harnessOuterNetnsEnv); outer != "" && outer == self {
				t.Fatalf("没有真的换 netns:仍在 %s", self)
			}
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

// fakeTunnelBuilder 造一个可以直接塞进 Options.BuildTunnel 的建隧道函数:
// 进程内 socks5 服务端 + 不拉子进程的 Runner + 立即健康。tunnel.New 本就是导出的,
// 故台子不碰 internal/tunnel 的任何内部结构。
func fakeTunnelBuilder(t *testing.T) func(string, string, bool) (*tunnel.Tunnel, error) {
	t.Helper()
	addr := startFakeSocks5(t)
	return func(link, recoveryID string, auxiliaryHTTP bool) (*tunnel.Tunnel, error) {
		return tunnel.New(addr,
			func(string) (tunnel.Runner, error) { return newNoopRunner(), nil },
			func(string) (int64, error) { return 1, nil },
		), nil
	}
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

// twoServerHarnessConfig 是台子的基准配置。
//
//   - global: true —— Run() 因此整段跳过 china 列表的准备(不下载、不读内嵌),
//     启动路径里再没有任何需要联网或落大文件的事。
//   - 两条 transports —— 走多传输那一支(runFailover 起来),并把"每一台服务器都要有
//     bypass 路由"这条防环不变量摆到台子上。
//   - 地址全用 IP 字面量 —— 启动路径一次 DNS 都不做(netns 里也没有 DNS 可用)。
//
// data_dir 由 startHarness 追加 t.TempDir()(常量里写不了)。
const twoServerHarnessConfig = `global: true
transports:
  - vless://u@203.0.113.10:443
  - vless://u@203.0.113.11:443
`

type harness struct {
	t        *testing.T
	sockPath string
	cancel   context.CancelFunc
	done     chan error
	stopOnce sync.Once
}

// startHarness 用给定配置在当前(已隔离的)namespace 里跑起完整的 Run(),
// 等到控制 socket 就绪才返回。
func startHarness(t *testing.T, cfgYAML string) *harness {
	t.Helper()
	cfg, err := config.Parse([]byte(cfgYAML + "data_dir: " + t.TempDir() + "\n"))
	if err != nil {
		t.Fatalf("解析台子配置: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, sockPath: SockPath, cancel: cancel, done: make(chan error, 1)}
	opts := Options{
		TunName:       "bx0",
		TunAddr:       "198.51.100.1/30",
		MTU:           1500,
		Probe:         "203.0.113.10:443", // 假健康检查不看它;留真实形状便于读日志
		HealthTimeout: 15 * time.Second,
		BuildTunnel:   fakeTunnelBuilder(t),
	}
	go func() { h.done <- Run(ctx, cfg, opts) }()
	t.Cleanup(h.stop)

	// 控制 socket 是 Run() 走完建隧道 / 健康等待 / DNS / TUN / 引擎之后的第一个可观测里程碑。
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(h.sockPath); err == nil {
			return h
		}
		select {
		case runErr := <-h.done:
			// Run() 已经返回,stop() 不必再等它 —— 否则 Fatalf 触发的 Cleanup 会挂死。
			h.stopOnce.Do(func() {})
			t.Fatalf("Run() 在控制 socket 就绪前就返回了: %v", runErr)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("等 %s 出现超时(60s)", h.sockPath)
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
// 测试
// ---------------------------------------------------------------------------

// 台子绝不能碰到宿主上正在运行的 bx:SockPath 是包级常量 /run/bx/core.sock,
// 而 netns **不隔离文件系统**,control.go 在监听前还会 os.Remove(SockPath)。
// 所以必须连 mount namespace 一起 unshare,并在 /run 上挂 tmpfs。
// 这一条是硬要求 —— 少了它,在开着 bx 的机器上跑一次台子就会夺走它的控制 socket。
func TestHarnessStartsAndRestoresCleanly(t *testing.T) {
	enterIsolatedNetns(t)
	base := ipOut(t, "rule", "list")

	h := startHarness(t, twoServerHarnessConfig)
	if _, err := os.Stat(h.sockPath); err != nil {
		t.Fatalf("控制 socket 应已就绪: %v", err)
	}
	h.stop()

	if after := ipOut(t, "rule", "list"); after != base {
		t.Fatalf("退出未干净还原:\n--- base ---\n%s\n--- after ---\n%s", base, after)
	}
}
