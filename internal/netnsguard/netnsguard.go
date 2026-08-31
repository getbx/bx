//go:build integration && linux

// Package netnsguard 是集成台的**隔离机制**:把测试二进制在一套整进程独占的
// net + mount namespace 里重新 exec 一份,并向内核求证隔离真的生效。
//
// **为什么它必须是一份而不是两份。** 这段代码写错的后果不是测试红,是台子把
// tmpfs 盖在**宿主真实的 /run** 上、把宿主正在跑的 bx 控制 socket 删掉。
// supervisor 与 guardian 两个包都要用它;各抄一份就是给这个后果开两次机会,
// 而其中一份的修复不会跟着另一份走 —— 本仓库为「同一个根因修一处漏两处」
// 栽过多次。原文与全部推导住在
// internal/supervisor/harness_netns_linux_test.go 的开发史里,搬家时一字未改。
//
// 它是普通 .go 而不是 _test.go:跨包共享的前提。build tag 保证只有
// `-tags integration` 的测试构建才会编译它,生产构建里它不存在。
package netnsguard

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// isolatedEnv 非空即表示「本进程已经在台子的独立 namespace 里」,
	// 用来终止 re-exec 递归。
	isolatedEnv = "BX_HARNESS_ISOLATED"

	childTimeout = 3 * time.Minute

	// childTestTimeout 必须**短于** childTimeout,而且要短于调用方给整套测试
	// 的 -test.timeout。理由是诊断:子进程被父进程的 context 掐死(SIGKILL)
	// 时什么都不会打印,而父进程正卡在 CombinedOutput 上 —— 于是一次子进程里
	// 的死锁表现为「父进程超时 + 零输出」,连它挂在哪一行都看不到(2026-08-30
	// 变异实测)。让子进程先撞自己的 -test.timeout,它会打出完整的 goroutine
	// 栈,父进程再把那份栈原样转述出来。
	childTestTimeout = 90 * time.Second
)

// isolatedNamespaces 是台子要求独占的那几种 namespace。
//
// **mnt 与 net 同等重要,不是陪衬**:少了 mnt,tmpfs 会盖在宿主真实的 /run 上,
// control.go 的 os.Remove(SockPath) 删的就是宿主 bx 的控制 socket。
var isolatedNamespaces = []string{"net", "mnt"}

// Uplink 是台子要造的那条假上行。空 netns 里只有一个 down 的 lo,而任何要探
// 默认网关的生产代码(supervisor 的 Hijack、guardian 的 DiscoverDefaultGateway)
// 在那里必然失败。dummy 设备不发任何包,网关也不需要真的存在(路由表接受
// on-link 网关)。
type Uplink struct {
	Dev     string
	Addr    string
	Gateway string
}

// Options 描述这一次隔离要布置成什么样。
type Options struct {
	// MountPoint 是要盖 tmpfs 的目录(通常是运行期目录的父目录)。
	// **由调用方从生产常量推导而不是写死**:运行期路径若哪天搬家,隔离必须
	// 跟着搬,否则台子会退回去动宿主真实的 /run/bx。
	MountPoint string
	// HiddenPaths 是 tmpfs 盖上之后必须**看不见**的路径(通常是本包的控制
	// socket)。注意这条断言证明的是「台子看不见宿主的 socket」,**不能**用来
	// 证明隔离生效(漏掉 CLONE_NEWNS 时它照样通过,因为 socket 是被自己的
	// tmpfs 盖住的)。真正证明隔离的是 assertProcessWideIsolation。
	HiddenPaths []string
	// Uplink 非零值时布置假上行 + 默认路由。
	Uplink Uplink
}

// Enter 让本测试在一套**整进程**独占的 net + mount namespace 里运行。
//
// 为什么不是 `runtime.LockOSThread() + unix.Unshare(...)`:unshare 只作用于
// **调用它的那一个线程**,而被测的编排是重度并发的。本机实测(privileged
// busybox 容器,一个先热身出 8 个 M 的探针程序):锁线程 unshare 之后新起的
// 50 个 goroutine,50/50 全部落在**外层** netns。也就是说生产代码的 ip 命令、
// TUN 的 ioctl、监听 socket 会打在外面;mount namespace 同理,监听前那句
// os.Remove(SockPath) 会删掉宿主真实的 /run/bx/core.sock —— 正是要避免的那个
// 灾难,而线程级 unshare 恰恰防不住它。
//
// 故改为把测试二进制在 CLONE_NEWNET|CLONE_NEWNS 下重新 exec 一份:子进程从
// 单线程起步,它此后创建的每一个线程都继承这套 namespace。namespace 随子进程
// 退出销毁,宿主零残留。
func Enter(t *testing.T, opts Options) {
	t.Helper()
	if os.Getenv(isolatedEnv) != "" {
		prepare(t, opts)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("需要 root(在特权容器或 CI 里跑:scripts/run-netns-tests.sh)")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("缺 ip 命令(iproute2)")
	}
	rerun(t) // 不返回:内部以 t.Skip/t.Fatal 结束本测试
}

// rerun 在新 net+mount namespace 里重跑当前这一个测试,并把子进程的输出原样
// 转述出来。子进程失败 → 本测试失败;子进程成功 → 本测试 Skip(断言都在子进程
// 里跑过了)。
func rerun(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("定位测试二进制: %v", err)
	}
	env := append(os.Environ(), isolatedEnv+"=1")
	for _, ns := range isolatedNamespaces {
		id, err := os.Readlink("/proc/self/ns/" + ns)
		if err != nil {
			t.Fatalf("读当前 %s namespace: %v", ns, err)
		}
		env = append(env, outerNSEnv(ns)+"="+id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), childTimeout)
	defer cancel()
	cmd := exec.CommandContext(
		ctx, exe,
		"-test.run", "^"+regexp.QuoteMeta(t.Name())+"$",
		"-test.v",
		"-test.count=1",
		"-test.timeout", childTestTimeout.String(),
	)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNET | syscall.CLONE_NEWNS,
		Pdeathsig:  syscall.SIGKILL, // 父进程被 go test 超时打死时,子进程不留下来占着 TUN
	}
	// **输出走临时文件,不走管道。** `CombinedOutput`(以及任何把 Stdout 设成
	// 非 *os.File 的写法)会建一个管道并起 goroutine 拷贝,而 `Wait` 要等那个
	// 拷贝读到 EOF —— 管道被**孙进程**继承时,子进程早已退出而 EOF 永远不来,
	// 父进程就此挂死。台子里的孙进程是真实存在的:被测编排会 spawn Core。
	//
	// 2026-08-30 变异实测:子进程死锁时父进程表现为「超时 + 零输出」,连子进程
	// 自己打的 goroutine 栈都拿不到 —— 一次失败没有任何可行动的线索。
	// *os.File 不建管道也不起拷贝 goroutine,Wait 在子进程退出的那一刻就返回,
	// 孙进程持不持有它都一样。
	outFile, err := os.CreateTemp("", "netnsguard-child-*.log")
	if err != nil {
		t.Fatalf("建子进程输出文件: %v", err)
	}
	defer os.Remove(outFile.Name())
	defer outFile.Close()
	cmd.Stdout = outFile
	cmd.Stderr = outFile
	runErr := cmd.Run()
	out, readErr := os.ReadFile(outFile.Name())
	if readErr != nil {
		t.Fatalf("读子进程输出: %v", readErr)
	}
	t.Logf("独立 net+mount namespace 子进程输出:\n%s", IndentLines(string(out)))
	if runErr != nil {
		t.Fatalf("子进程里的 %s 失败: %v", t.Name(), runErr)
	}
	// 退出码 0 不等于跑过了:被 -test.run 过滤掉、或自己 Skip 掉,退出码同样是 0。
	// 这条断言让「台子其实没跑」没法伪装成绿灯。
	if !bytes.Contains(out, []byte("--- PASS: "+t.Name())) {
		t.Fatalf("子进程退出码为 0 却没有 %s 的 PASS 行 —— 它可能被跳过或根本没跑:\n%s", t.Name(), out)
	}
	t.Skip("断言已在子进程的独立 net+mount namespace 内跑完(输出见上)")
}

// IndentLines 给子进程输出加前缀。CI 逐个测试名断言的正是这条带 `  | ` 前缀的
// PASS 行(顶层打印的是 SKIP),改前缀要连 ci.yml 一起改。
func IndentLines(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  | " + l
	}
	return strings.Join(lines, "\n")
}

// prepare 在子进程内把 namespace 布置成台子需要的样子。
func prepare(t *testing.T, opts Options) {
	t.Helper()
	assertProcessWideIsolation(t)

	// 让本 mount ns 的挂载不外泄到宿主。
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatalf("把根挂载改私有: %v", err)
	}
	if opts.MountPoint != "" {
		mountTmpfs(t, opts)
	}
	for _, hidden := range opts.HiddenPaths {
		if _, err := os.Stat(hidden); err == nil {
			t.Fatalf("tmpfs 挂上之后 %s 仍然可见 —— 隔离没生效", hidden)
		}
	}

	MustIP(t, "link", "set", "lo", "up")
	if opts.Uplink.Dev != "" {
		MustIP(t, "link", "add", opts.Uplink.Dev, "type", "dummy")
		MustIP(t, "addr", "add", opts.Uplink.Addr, "dev", opts.Uplink.Dev)
		MustIP(t, "link", "set", opts.Uplink.Dev, "up")
		MustIP(t, "route", "add", "default", "via", opts.Uplink.Gateway, "dev", opts.Uplink.Dev)
	}
}

// mountTmpfs 把运行期目录换成 tmpfs:控制 socket 与 pid 文件就此落在一次性
// 文件系统里,宿主上正在跑的 bx 完全看不见,也不会被生产代码的 os.Remove 掉。
//
// 挂载点得先存在(busybox 镜像里没有 /run)。注意 **MS_PRIVATE 挡的是挂载传播,
// 不是文件写入** —— 新 mount namespace 看到的仍是同一个底层文件系统,这里
// MkdirAll 建出来的目录是真的落在盘上的。故只在缺失时建,并在收尾时卸载 + 删掉,
// 不留痕。
func mountTmpfs(t *testing.T, opts Options) {
	t.Helper()
	if _, err := os.Stat(opts.MountPoint); os.IsNotExist(err) {
		if err := os.MkdirAll(opts.MountPoint, 0o755); err != nil {
			t.Fatalf("建 %s 挂载点: %v", opts.MountPoint, err)
		}
		t.Cleanup(func() {
			_ = unix.Unmount(opts.MountPoint, 0)
			_ = os.Remove(opts.MountPoint) // 只删我们建的那个空目录;非空/不存在都会失败而无害
		})
	}
	if err := unix.Mount("tmpfs", opts.MountPoint, "tmpfs", 0, ""); err != nil {
		t.Fatalf("在 %s 挂 tmpfs: %v", opts.MountPoint, err)
	}
}

// assertProcessWideIsolation 证明隔离**既真的换过**、又是**整进程**的。
//
// 两条缺一不可,而且对 net 和 mnt 都要查:
//   - 换没换(与父进程传来的外层标识比):Cloneflags 少写一个、或者有人手工设了
//     isolatedEnv 直接跑,进程会安安静静地留在宿主 namespace 里。少了 mnt 这半边,
//     漏掉 CLONE_NEWNS 的台子会把 tmpfs 盖在**宿主真实的 /run** 上、把宿主 bx 的
//     控制 socket 删掉,然后报绿 —— HiddenPaths 那条不可见断言此时**恰恰会通过**,
//     因为 socket 正是被自己的 tmpfs 盖住/删掉的。
//   - 是不是整进程(逐线程比):线程级 unshare 下本进程会有线程留在外层 namespace,
//     而被测编排的 goroutine 恰好就跑在那些线程上。
//
// 外层标识**缺席即 fatal**,不是「跳过这一条」:缺席只可能发生在没有经过
// rerun 的路径上,而那正是最需要拦住的情形。
func assertProcessWideIsolation(t *testing.T) {
	t.Helper()
	for _, ns := range isolatedNamespaces {
		self, err := os.Readlink("/proc/self/ns/" + ns)
		if err != nil {
			t.Fatalf("读 %s namespace: %v", ns, err)
		}
		// 外层参照必须**不可伪造**。它此前来自父进程写进环境变量的 id ——
		// 复审把那一行改成写死的假 id、同时去掉 CLONE_NEWNS,台子照样 PASS,
		// 跑完把外层的 /run 用 tmpfs 盖住、宿主的 core.sock 删掉:正是本守卫要防的
		// 那件事。更糟的是 HiddenPaths 那条检查会**确认错的东西** —— 它通过恰恰
		// 因为 tmpfs 把宿主的 socket 藏起来了。信自己的记账而不去问内核,是这个
		// 项目在别处反复点名的反模式,这里不能再犯。
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
		if declared := os.Getenv(outerNSEnv(ns)); declared != "" && declared != outer {
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

// cloneFlagFor 把 namespace 名映射到真实的 clone flag 常量名。
// 排查的人会照着这句话去改代码,所以它必须是真名 —— mnt 对应的是 CLONE_NEWNS,
// 不存在 CLONE_NEWMNT。
func cloneFlagFor(ns string) string {
	if ns == "mnt" {
		return "CLONE_NEWNS"
	}
	return "CLONE_NEW" + strings.ToUpper(ns)
}

// outerNSEnv 是父进程用来传递外层 namespace 标识的环境变量名。
// 子进程据此证明自己**真的**换了这套 namespace,而不是 Cloneflags 被静默忽略、
// 或者有人手工设了 isolatedEnv 就让台子在宿主 namespace 里开工。
func outerNSEnv(ns string) string {
	return "BX_HARNESS_OUTER_" + strings.ToUpper(ns) + "NS"
}

// MustIP 在当前 netns 执行 ip 命令并返回其输出(基线比对用)。
func MustIP(t *testing.T, args ...string) string {
	t.Helper()
	out, err := IPQuiet(args...)
	if err != nil {
		t.Fatalf("ip %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// IPQuiet 与 MustIP 问的是同一个内核,但**不吃 *testing.T**。
//
// 从非测试 goroutine 调 t.Fatalf **不做它看起来做的事**:那只会标记失败并结束
// 那一个 goroutine,测试本体照常往下跑。故失败在这里不是控制流,而是**数据**:
// 原样并进返回值,让读到它的断言消息自解释。
func IPQuiet(args ...string) (string, error) {
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("ip %s: %w\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}
