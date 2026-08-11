//go:build darwin

package leakserve

import (
	"errors"
	"os/exec"
	"testing"
)

// 解析打在**抓下来的固定文本**上,不跑 scutil:本机正跑着 bx,采集层的测试
// 一次都不许碰真实系统。
func TestParseSCUtilNCList(t *testing.T) {
	out := `Available network connection services in the current set (*=enabled):
* (Disconnected)   6E1B0000-0000-0000-0000-000000000001 PPP (L2TP)  "Old VPN"    [OnDemand]
* (Connected)      A2C40000-0000-0000-0000-000000000002 IPSec       "Work VPN"
  (Invalid)        B3D50000-0000-0000-0000-000000000003 VPN         "Broken VPN"
`
	services := parseSCUtilNCList(out)
	if len(services) != 3 {
		t.Fatalf("应解析出 3 条,得到 %d:%+v", len(services), services)
	}
	connected := 0
	for _, s := range services {
		if s.Connected {
			connected++
			if s.Name != "Work VPN" {
				t.Errorf("已连接的应是 Work VPN,得到 %q", s.Name)
			}
		}
	}
	if connected != 1 {
		t.Fatalf("只有一条是 Connected,得到 %d", connected)
	}
}

// exitErrorWithStderr 造一个**生产形状**的失败:一个非零退出的子进程,诊断写在
// stderr 上,经 `exec.Cmd.Output()` 拿到。
//
// **这不是「模拟」,是同一条代码路径。** `darwinRouteLookup` 用的正是 `.Output()`,
// 而它在非零退出时返回的 `*exec.ExitError` 的 `Error()` **只有 "exit status 1"** ——
// route(1) 的那句诊断被关在 `ExitError.Stderr` 里。原来这条测试喂的是手写字符串,
// 于是它证明的是一个生产环境永远造不出的输入(隔壁 scutildns_test.go 专门点名并
// 避开的正是这个陷阱)。
func exitErrorWithStderr(t *testing.T, stderr string) error {
	t.Helper()
	_, err := exec.Command("/bin/sh", "-c", "printf '%s' \"$0\" >&2; exit 1", stderr).Output()
	if err == nil {
		t.Fatal("这个子进程本该非零退出")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("本守卫读不懂现在的形状:期待 *exec.ExitError,得到 %T", err)
	}
	if err.Error() != "exit status 1" {
		t.Fatalf("前提不成立:生产形状的 Error() 应当只有 %q,得到 %q —— "+
			"如果它现在带上了诊断,这条测试守的东西已经变了", "exit status 1", err.Error())
	}
	return err
}

// route -n get 对「没有到达该目的地的路由」是报错退出的,而那句报错必须被翻成
// ErrNoRoute —— 「确知没有 v6 通路」是唯一能支撑一句诚实 ok 的输入。
//
// 认不出来,`ipv6_leak` 就在**它唯一该说 ok 的那种机器上**(根本没有 v6 的机器)
// 永久停在 not checked —— 恒绿的镜像,一样把这一行变成装饰。
//
// 反过来同样重要:**别的失败一律不许被认成「确知没有」**,否则一台可能正在漏
// v6 的机器会收到一句「没有 IPv6 可漏」。
func TestIsNoRouteErrorSeparatesAbsenceFromFailure(t *testing.T) {
	// 生产形状:诊断在 stderr 里,Error() 只有 "exit status 1"。
	for _, stderr := range []string{
		"route: writing to routing socket: no route to host\n",
		"route: writing to routing socket: not in table\n",
		"route: writing to routing socket: network is unreachable\n",
		"host is down\n",
	} {
		if !isNoRouteError(exitErrorWithStderr(t, stderr)) {
			t.Errorf("stderr=%q 是「确知没有这条路由」,应翻成 ErrNoRoute —— "+
				"它只在 ExitError.Stderr 里,err.Error() 是 \"exit status 1\"", stderr)
		}
	}
	// 同样是非零退出,但 stderr 说的不是「没有这条路由」。
	for _, stderr := range []string{
		"route: writing to routing socket: permission denied\n",
		"", // 被信号打断之类:什么都没写
	} {
		if isNoRouteError(exitErrorWithStderr(t, stderr)) {
			t.Errorf("stderr=%q 是「没问出来」,不许被认成「确知没有路由」", stderr)
		}
	}
	// 另一半仍然是普通 error:超时、fork 失败这些从来到不了子进程的 stderr。
	for _, text := range []string{
		"context deadline exceeded",
		"fork/exec /sbin/route: permission denied",
		"signal: killed",
		"route -n get: unexpected output",
	} {
		if isNoRouteError(errString(text)) {
			t.Errorf("%q 是「没问出来」,不许被认成「确知没有路由」—— 那会让一台可能"+
				"正在漏 v6 的机器收到一句「没有 IPv6 可漏」", text)
		}
	}
	// 而「文本里带得上诊断」的那条老路不许被顺手删掉:supervisor 那一层将来可能
	// 把 stderr 包进 error 文本里,两种形状都得认。
	if !isNoRouteError(errString("route: writing to routing socket: not in table")) {
		t.Error("诊断出现在 err.Error() 里时同样要认")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
