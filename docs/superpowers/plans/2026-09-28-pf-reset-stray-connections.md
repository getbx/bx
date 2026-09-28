# 一次性 pf 重置残留连接 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `bx up` 劫持完路由之后,把保护关着时开的、仍从物理网卡以真实 IP 收发的连接在几秒内重置,让应用自己重连进 TUN;用户不做任何事。

**Architecture:** 新叶子包 `internal/pfreset` 分三层:纯函数出规则文本与白名单表(`rules.go`)、纯循环「装 → 每秒看 → 清零或封顶即拆」经 `Driver` 接口(`loop.go`)、darwin 的 `pfctl` 驱动与 token 落盘(`pfctl_darwin.go`)。`supervisor.Run()` 在 Hijack 成功之后调一次;残留 anchor 的三条清理路(Core defer、`bx down` 强制拆除、Guardian 起 Core 之前)都调同一个 `pfreset.FlushStale`。所有者的 Mac 上第一次真跑先 `--pf-reset dry-run`,再带 `--test-timeout` 全量。

**Tech Stack:** Go 1.26;macOS `pfctl`(`-E`/`-X` 引用计数启用、`-a <anchor> -f -` 装规则、`-a <anchor> -F all` 冲掉);既有 `appattr.StrayConnections` 判据与 `supervisor` 的 socket 表采集。

**Spec:** `docs/superpowers/specs/2026-09-28-bx-reset-stray-connections-design.md`

## Global Constraints

- 只做 darwin;linux/windows 的驱动是 nil,循环一次都不跑。
- **一次性,不常驻**:规则最多存在 10 秒;每条退出路径(清零、封顶、ctx 取消、任一步失败)都冲 anchor、释放引用。
- **没有残留连接就不碰 pf**:先读 socket 表,零条 ⇒ 一个 `pfctl` 都不调。
- 白名单表 = `route.DefaultPrivateCIDRs` + 服务器旁路 + 用户 `bypass:`;规则带 `user != root` 与 `quick`;TCP `return-rst`、UDP `return-icmp` 两条都要(所有者 2026-09-28 拍板:一次性 + 含 UDP)。
- anchor 名 `com.apple/250.bx`;token 落 `filepath.Dir(supervisor.SockPath)/pf.token`(`/var/run/bx/pf.token`)。**anchor 落点是否真被求值要在真机上核**(spec 第三点),纯函数里只是一个常量。
- 用户可见文案英文、注释中文;不在仓库里写真实基础设施地址(用 `203.0.113.x`)。
- 每个任务都 `bash scripts/verify.sh --quick` 过了再提交;全部做完跑一次全量。

## Review Focus

1. **物理网卡不是 en0**(热点是 `en0`、有线是 `en5`、USB 网卡名随机):规则里的网卡名必须来自 `PhysicalDefaultRoute`,不许写死 —— 由 Task 1 的规则文本测试用 `en5` 钉住。
2. **`pfctl -E` 的输出格式**(`Token : 1234567890`)解析失败时:不许把 0 当 token 去 `-X`;解析失败 = 整个重置放弃且不装规则 —— Task 3 的驱动解析测试。
3. **装规则成功、随后 ctx 被取消(用户 3 秒内 `bx down`)**:拆的动作必须照做 —— Task 2 的循环测试用取消的 ctx。
4. **上一个 Core 崩溃留下的 anchor**:Guardian 起新 Core 之前冲掉;`bx down` 强制拆除也冲掉 —— Task 5 各一条。
5. **用户 `bypass:` 里有裸 IP 或非法条目**:裸 IP 补成 /32、非法丢掉不编,与 `routedAroundForGuard` 同一份 `policy.RuleCIDR` —— Task 1 的表构造测试。

---

### Task 1: `internal/pfreset` 纯函数:规则文本与白名单表

**Files:**
- Create: `internal/pfreset/rules.go`
- Create: `internal/pfreset/rules_test.go`

**Interfaces:**
- Produces: `const Anchor = "com.apple/250.bx"`;`func RoutedAround(private []string, serverBypass, userBypass []string) []netip.Prefix`;`func Rules(device string, routedAround []netip.Prefix) string`。

- [ ] **Step 1: 写失败测试**

```go
package pfreset

import (
	"net/netip"
	"strings"
	"testing"
)

// 规则文本是 pf 真正吃的东西,逐句钉:网卡名来自参数(热点是 en0、有线可能是 en5)、
// 白名单表里每一段都在、TCP 回 RST、UDP 回 ICMP、user != root、quick。
func TestRulesNameTheDeviceAndEveryRoutedAroundPrefix(t *testing.T) {
	around := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("203.0.113.92/32"),
	}
	got := Rules("en5", around)
	for _, want := range []string{
		"table <bx_routed_around> persist { 10.0.0.0/8, 203.0.113.92/32 }",
		"block return-rst out quick on en5 inet proto tcp from (en5) to !<bx_routed_around> user != root",
		"block return-icmp out quick on en5 inet proto udp from (en5) to !<bx_routed_around> user != root",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rules miss %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "en0") {
		t.Fatalf("the device must come from the caller, got a hardcoded en0:\n%s", got)
	}
}

// 白名单 = 私网 + 服务器旁路 + 用户 bypass;裸 IP 补 /32,认不出的丢掉不编,去重。
func TestRoutedAroundJoinsPrivateServerAndUserBypass(t *testing.T) {
	got := RoutedAround([]string{"10.0.0.0/8", "192.168.0.0/16"}, []string{"203.0.113.92/32"}, []string{"198.51.100.7", "garbage", "10.0.0.0/8"})
	want := []string{"10.0.0.0/8", "192.168.0.0/16", "203.0.113.92/32", "198.51.100.7/32"}
	if len(got) != len(want) {
		t.Fatalf("routed-around = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Fatalf("routed-around[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: 跑,确认红**

Run: `go test ./internal/pfreset/ 2>&1 | tail -3`
Expected: `undefined: Rules` / `undefined: RoutedAround`

- [ ] **Step 3: 最小实现**

```go
// Package pfreset 在 `bx up` 之后把保护关着时开的连接重置一次,让应用重连进 TUN。
//
// macOS 没有按 socket 重置的原语(没有 tcpdrop、没有对应 sysctl),能做到的是让本机
// TCP 栈自己把 socket 判死:pf 的 `block return-rst out` 拦下一个出站包并向本机回 RST,
// socket 立刻收到 ECONNRESET。UDP 同法 `return-icmp`。规则只在 bx 需要的那几秒存在。
// 设计:docs/superpowers/specs/2026-09-28-bx-reset-stray-connections-design.md
package pfreset

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/getbx/bx/internal/policy"
)

// Anchor 是 bx 自己的 pf anchor。放在 com.apple/ 通配 anchor 之下,主规则集
// (/etc/pf.conf 的 `anchor "com.apple/*"`)不改也会被求值。**真机未核**。
const Anchor = "com.apple/250.bx"

// Table 是白名单表名:发往这些网段的连接是 bx 自己安排走物理网卡的,不重置。
const Table = "bx_routed_around"

// RoutedAround 把三份「bx 自己绕开隧道的网段」合成一份:私网、服务器旁路、用户
// bypass。裸 IP 补成 /32、认不出的丢掉不编、去重 —— 与 supervisor.routedAroundForGuard
// 同一份 policy.RuleCIDR,别再写一份解析。
func RoutedAround(private, serverBypass, userBypass []string) []netip.Prefix {
	seen := map[netip.Prefix]bool{}
	var out []netip.Prefix
	for _, group := range [][]string{private, serverBypass, userBypass} {
		for _, e := range group {
			p, ok := policy.RuleCIDR(e)
			if !ok || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// Rules 是装进 anchor 的全部文本。`user != root` 放过 Core、隧道子进程、tailscaled
// 这类 root 守护进程;`quick` 让这两条不被别的 anchor 覆盖;`(device)` 取网卡的当前
// 地址(热点切换时地址会变)。
func Rules(device string, routedAround []netip.Prefix) string {
	items := make([]string, 0, len(routedAround))
	for _, p := range routedAround {
		items = append(items, p.String())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "table <%s> persist { %s }\n", Table, strings.Join(items, ", "))
	fmt.Fprintf(&b, "block return-rst out quick on %s inet proto tcp from (%s) to !<%s> user != root\n", device, device, Table)
	fmt.Fprintf(&b, "block return-icmp out quick on %s inet proto udp from (%s) to !<%s> user != root\n", device, device, Table)
	return b.String()
}
```

- [ ] **Step 4: 跑,确认绿**

Run: `go test ./internal/pfreset/ 2>&1 | tail -1`
Expected: `ok`

- [ ] **Step 5: 提交**

```bash
git add internal/pfreset
git commit -m "feat(pfreset): 一次性 pf 重置的规则文本与白名单表(纯函数)"
```

---

### Task 2: `internal/pfreset` 循环:装、看、拆,经 Driver 接口

**Files:**
- Create: `internal/pfreset/loop.go`
- Create: `internal/pfreset/loop_test.go`

**Interfaces:**
- Consumes: Task 1 的 `Rules`。
- Produces:
  ```go
  type Driver interface {
      Enable(ctx context.Context) (token string, err error)   // pfctl -E,记 token
      Load(ctx context.Context, rules string) error            // pfctl -a Anchor -f -
      Flush(ctx context.Context) error                          // pfctl -a Anchor -F all
      Release(ctx context.Context, token string) error         // pfctl -X token,删 token 文件
  }
  type Options struct {
      Device       string
      RoutedAround []netip.Prefix
      Observe      func() int          // 此刻仍在绕过的连接数(0 = 完成)
      Tick         <-chan time.Time    // 每秒
      Deadline     time.Duration       // 封顶,生产 10s
      Log          func(format string, args ...any)
  }
  type Outcome struct { Attempted bool; Initial, Remaining int; Elapsed time.Duration; Err error }
  func Run(ctx context.Context, d Driver, o Options) Outcome
  ```

- [ ] **Step 1: 写失败测试**

```go
package pfreset

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

type fakeDriver struct {
	calls   []string
	loadErr error
}

func (f *fakeDriver) Enable(context.Context) (string, error) { f.calls = append(f.calls, "enable"); return "tok", nil }
func (f *fakeDriver) Load(_ context.Context, rules string) error {
	f.calls = append(f.calls, "load")
	return f.loadErr
}
func (f *fakeDriver) Flush(context.Context) error { f.calls = append(f.calls, "flush"); return nil }
func (f *fakeDriver) Release(_ context.Context, token string) error {
	f.calls = append(f.calls, "release:"+token)
	return nil
}

func opts(observe func() int, tick <-chan time.Time) Options {
	return Options{Device: "en0", RoutedAround: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		Observe: observe, Tick: tick, Deadline: 10 * time.Second, Log: func(string, ...any) {}}
}

// 常态:没有残留连接,pf 一个字都不碰。
func TestRunTouchesNothingWhenNothingIsStray(t *testing.T) {
	d := &fakeDriver{}
	out := Run(context.Background(), d, opts(func() int { return 0 }, nil))
	if out.Attempted || len(d.calls) != 0 {
		t.Fatalf("no stray connections must mean no pf calls, got %v / %+v", d.calls, out)
	}
}

// 有残留:enable → load → 每 tick 看一次 → 清零即 flush + release。
func TestRunStopsTheMomentTheStrayCountReachesZero(t *testing.T) {
	d := &fakeDriver{}
	tick := make(chan time.Time, 3)
	counts := []int{3, 1, 0}
	i := 0
	observe := func() int { c := counts[i]; if i < len(counts)-1 { i++ }; return c }
	for range counts {
		tick <- time.Time{}
	}
	out := Run(context.Background(), d, opts(observe, tick))
	want := []string{"enable", "load", "flush", "release:tok"}
	if !out.Attempted || out.Initial != 3 || out.Remaining != 0 || out.Err != nil {
		t.Fatalf("outcome = %+v", out)
	}
	if len(d.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", d.calls, want)
	}
	for j := range want {
		if d.calls[j] != want[j] {
			t.Fatalf("calls = %v, want %v", d.calls, want)
		}
	}
}

// ctx 被取消(用户几秒内 bx down):拆的动作照做。
func TestRunTearsDownWhenTheContextIsCancelled(t *testing.T) {
	d := &fakeDriver{}
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	out := Run(ctx, d, opts(func() int { return 2 }, tick))
	if d.calls[len(d.calls)-1] != "release:tok" || d.calls[len(d.calls)-2] != "flush" {
		t.Fatalf("cancelled run must still flush and release, calls = %v", d.calls)
	}
	if out.Remaining != 2 {
		t.Fatalf("remaining must report what was left, got %+v", out)
	}
}

// 装规则失败:仍要 release(引用计数是我们加的),不 flush 也无妨但做了不算错。
func TestRunReleasesTheReferenceWhenLoadingFails(t *testing.T) {
	d := &fakeDriver{loadErr: errors.New("syntax error")}
	out := Run(context.Background(), d, opts(func() int { return 1 }, nil))
	if out.Err == nil || d.calls[len(d.calls)-1] != "release:tok" {
		t.Fatalf("a failed load must still release pf, calls = %v err = %v", d.calls, out.Err)
	}
}

// 封顶:tick 一直不清零,到 Deadline 就拆,Remaining 报还剩几条(交给两段式去点名)。
func TestRunGivesUpAtTheDeadline(t *testing.T) {
	d := &fakeDriver{}
	tick := make(chan time.Time, 100)
	for i := 0; i < 100; i++ {
		tick <- time.Time{}
	}
	o := opts(func() int { return 2 }, tick)
	o.Deadline = 0 // 立刻到期
	out := Run(context.Background(), d, o)
	if out.Remaining != 2 || d.calls[len(d.calls)-1] != "release:tok" {
		t.Fatalf("deadline must tear down and report leftovers, got %+v calls %v", out, d.calls)
	}
}
```

- [ ] **Step 2: 跑,确认红**

Run: `go test ./internal/pfreset/ 2>&1 | tail -3`
Expected: `undefined: Driver` / `undefined: Run`

- [ ] **Step 3: 最小实现**

```go
package pfreset

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// Driver 是对 pfctl 的四个动作。做成接口只为让循环在没有 pf 的机器上可测。
type Driver interface {
	Enable(ctx context.Context) (token string, err error)
	Load(ctx context.Context, rules string) error
	Flush(ctx context.Context) error
	Release(ctx context.Context, token string) error
}

// Options 是一次重置的全部输入。
type Options struct {
	Device       string
	RoutedAround []netip.Prefix
	// Observe 回答「此刻还有几条连接在绕过 bx」(与 appattr.StrayConnections 同一份判据)。
	Observe  func() int
	Tick     <-chan time.Time
	Deadline time.Duration
	Log      func(format string, args ...any)
}

// Outcome 说清做了什么:没残留就 Attempted=false;做了就报初始几条、还剩几条、花了多久。
type Outcome struct {
	Attempted bool
	Initial   int
	Remaining int
	Elapsed   time.Duration
	Err       error
}

// Run 是那条循环:先看一次 —— 零条就一个 pfctl 都不调;有,则 enable → load →
// 每个 tick 看一次 → 清零 / 到 Deadline / ctx 取消即拆。**每条退出路径都拆**:
// flush 与 release 在 defer 里,装规则失败也释放引用(引用是我们加的)。
func Run(ctx context.Context, d Driver, o Options) (out Outcome) {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	out.Initial = o.Observe()
	out.Remaining = out.Initial
	if out.Initial == 0 || d == nil {
		return out
	}
	out.Attempted = true
	start := time.Now()
	defer func() { out.Elapsed = time.Since(start) }()

	token, err := d.Enable(ctx)
	if err != nil {
		out.Err = fmt.Errorf("enabling pf: %w", err)
		return out
	}
	defer func() {
		if ferr := d.Flush(context.WithoutCancel(ctx)); ferr != nil {
			out.Err = errors.Join(out.Err, fmt.Errorf("flushing the bx anchor: %w", ferr))
		}
		if rerr := d.Release(context.WithoutCancel(ctx), token); rerr != nil {
			out.Err = errors.Join(out.Err, fmt.Errorf("releasing pf: %w", rerr))
		}
	}()
	if err := d.Load(ctx, Rules(o.Device, o.RoutedAround)); err != nil {
		out.Err = fmt.Errorf("loading the reset rules: %w", err)
		return out
	}
	o.Log("pf reset: %d connection(s) opened before protection was on are being reset on %s", out.Initial, o.Device)
	deadline := time.After(o.Deadline)
	for {
		select {
		case <-ctx.Done():
			return out
		case <-deadline:
			o.Log("pf reset: gave up after %s with %d connection(s) still outside bx", o.Deadline, out.Remaining)
			return out
		case <-o.Tick:
			out.Remaining = o.Observe()
			if out.Remaining == 0 {
				o.Log("pf reset: done, all %d connection(s) reconnected through bx", out.Initial)
				return out
			}
		}
	}
}
```

- [ ] **Step 4: 跑,确认绿**

Run: `go test ./internal/pfreset/ 2>&1 | tail -1`
Expected: `ok`

- [ ] **Step 5: 提交**

```bash
git add internal/pfreset
git commit -m "feat(pfreset): 装、看、拆那条循环,每条退出路径都拆"
```

---

### Task 3: darwin 的 pfctl 驱动与 token 落盘,以及 `FlushStale`

**Files:**
- Create: `internal/pfreset/pfctl_darwin.go`
- Create: `internal/pfreset/pfctl_other.go`
- Create: `internal/pfreset/pfctl_test.go`(无 build tag,测纯解析)

**Interfaces:**
- Consumes: Task 2 的 `Driver`。
- Produces: `func NewDriver(tokenPath string) Driver`(darwin 返回 pfctl 驱动;其他平台返回 nil);`func ParseEnableToken(out string) (string, error)`;`func FlushStale(ctx context.Context, tokenPath string, run func(ctx context.Context, args ...string) (string, error)) (flushed bool, err error)`。

- [ ] **Step 1: 写失败测试(解析与 FlushStale 都是纯逻辑)**

```go
package pfreset

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `pfctl -E` 的输出形如:
//   pf enabled
//   Token : 1234567890
// 没有 Token 那一行就不是成功,不许拿 0 去 -X。
func TestParseEnableTokenReadsTheTokenLineOnly(t *testing.T) {
	tok, err := ParseEnableToken("pf enabled\nToken : 1234567890\n")
	if err != nil || tok != "1234567890" {
		t.Fatalf("token = %q err = %v", tok, err)
	}
	if _, err := ParseEnableToken("pf enabled\n"); err == nil {
		t.Fatal("no Token line must be an error")
	}
	if _, err := ParseEnableToken(""); err == nil {
		t.Fatal("empty output must be an error")
	}
}

// 残留:anchor 里有规则 / token 文件还在 ⇒ 冲掉 + 释放 + 删文件;都没有 ⇒ 一个 pfctl 都不调。
func TestFlushStaleClearsALeftoverAnchorAndToken(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "pf.token")
	os.WriteFile(tokenPath, []byte("42\n"), 0o600)
	var calls []string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if strings.HasSuffix(strings.Join(args, " "), "-s rules") {
			return "block return-rst out quick on en0 ...\n", nil
		}
		return "", nil
	}
	flushed, err := FlushStale(context.Background(), tokenPath, run)
	if err != nil || !flushed {
		t.Fatalf("flushed = %v err = %v", flushed, err)
	}
	joined := strings.Join(calls, "|")
	for _, want := range []string{"-a " + Anchor + " -s rules", "-a " + Anchor + " -F all", "-X 42"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("calls %v miss %q", calls, want)
		}
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatal("the token file must be gone after release")
	}

	calls = nil
	run2 := func(_ context.Context, args ...string) (string, error) { calls = append(calls, strings.Join(args, " ")); return "", nil }
	flushed, err = FlushStale(context.Background(), tokenPath, run2)
	if err != nil || flushed {
		t.Fatalf("a clean machine: flushed = %v err = %v", flushed, err)
	}
	for _, c := range calls {
		if strings.Contains(c, "-F") || strings.Contains(c, "-X") {
			t.Fatalf("nothing to flush must mean no destructive pfctl call, got %v", calls)
		}
	}
}
```

- [ ] **Step 2: 跑,确认红**

Run: `go test ./internal/pfreset/ 2>&1 | tail -3`
Expected: `undefined: ParseEnableToken` / `undefined: FlushStale`

- [ ] **Step 3: 最小实现**

`internal/pfreset/pfctl_darwin.go`:

```go
//go:build darwin

package pfreset

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type pfctlDriver struct{ tokenPath string }

// NewDriver 返回 pfctl 驱动。tokenPath 是 `-E` 拿到的引用 token 落盘处:Core 崩溃时
// 只有它能告诉下一个人「有一个引用要释放」。
func NewDriver(tokenPath string) Driver { return pfctlDriver{tokenPath: tokenPath} }

func runPfctl(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "/sbin/pfctl", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("pfctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (d pfctlDriver) Enable(ctx context.Context) (string, error) {
	out, err := runPfctl(ctx, "-E")
	if err != nil {
		return "", err
	}
	token, err := ParseEnableToken(out)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(d.tokenPath, []byte(token+"\n"), 0o600); err != nil {
		// 记不下来就不做:崩溃时没人知道要释放,与「不留下没人管的状态」同一条。
		_, _ = runPfctl(ctx, "-X", token)
		return "", fmt.Errorf("recording the pf token: %w", err)
	}
	return token, nil
}

func (d pfctlDriver) Load(ctx context.Context, rules string) error {
	cmd := exec.CommandContext(ctx, "/sbin/pfctl", "-a", Anchor, "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pfctl -a %s -f -: %w: %s", Anchor, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (d pfctlDriver) Flush(ctx context.Context) error {
	_, err := runPfctl(ctx, "-a", Anchor, "-F", "all")
	return err
}

func (d pfctlDriver) Release(ctx context.Context, token string) error {
	_, err := runPfctl(ctx, "-X", token)
	_ = os.Remove(d.tokenPath)
	return err
}

// FlushStaleDarwin 是给 cli 强制拆除与 Guardian 用的入口(真 pfctl)。
func FlushStaleDarwin(ctx context.Context, tokenPath string) (bool, error) {
	return FlushStale(ctx, tokenPath, runPfctl)
}
```

`internal/pfreset/pfctl_other.go`:

```go
//go:build !darwin

package pfreset

import "context"

// 非 darwin 没有 pf:驱动是 nil,Run 一个字都不做。
func NewDriver(string) Driver { return nil }

func FlushStaleDarwin(context.Context, string) (bool, error) { return false, nil }
```

`internal/pfreset/token.go`(无 tag,纯逻辑):

```go
package pfreset

import (
	"context"
	"errors"
	"os"
	"strings"
)

// ParseEnableToken 从 `pfctl -E` 的输出里取 token(形如 `Token : 1234567890`)。
// 没有那一行就是失败 —— 拿一个编出来的 0 去 -X 会释放别人的引用。
func ParseEnableToken(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "Token" {
			if tok := strings.TrimSpace(v); tok != "" {
				return tok, nil
			}
		}
	}
	return "", errors.New("pfctl -E printed no Token line")
}

// FlushStale 清掉上一个 Core 留下的东西:anchor 里有规则就冲掉,token 文件在就释放
// 并删掉。都没有就一个破坏性的 pfctl 都不调。run 是 pfctl 的执行者(生产 runPfctl,
// 测试用假的)。
func FlushStale(ctx context.Context, tokenPath string, run func(ctx context.Context, args ...string) (string, error)) (bool, error) {
	flushed := false
	var errs error
	if out, err := run(ctx, "-a", Anchor, "-s", "rules"); err == nil && strings.TrimSpace(out) != "" {
		flushed = true
		if _, err := run(ctx, "-a", Anchor, "-F", "all"); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	if raw, err := os.ReadFile(tokenPath); err == nil {
		flushed = true
		if tok := strings.TrimSpace(string(raw)); tok != "" {
			if _, err := run(ctx, "-X", tok); err != nil {
				errs = errors.Join(errs, err)
			}
		}
		if err := os.Remove(tokenPath); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return flushed, errs
}
```

- [ ] **Step 4: 跑,确认绿;跨平台编译**

Run: `go test ./internal/pfreset/ 2>&1 | tail -1 && GOOS=linux go build ./internal/pfreset/ && GOOS=windows go build ./internal/pfreset/`
Expected: `ok`,两次交叉编译无输出

- [ ] **Step 5: 提交**

```bash
git add internal/pfreset
git commit -m "feat(pfreset): pfctl 驱动、token 落盘、残留清理 FlushStale"
```

---

### Task 4: 接进 `supervisor.Run()`:Hijack 之后一次;`Options.PFReset` 三态

**Files:**
- Modify: `internal/supervisor/run.go:112`(Options 加字段)与 `:897-930`(Hijack 成功之后)
- Create: `internal/supervisor/pfreset_darwin.go`、`internal/supervisor/pfreset_other.go`
- Modify: `internal/supervisor/stray_darwin.go`(把「读 socket 表 → 绕过连接」抽成 `darwinStraySnapshot`)
- Test: `internal/supervisor/pfreset_test.go`

**Interfaces:**
- Consumes: `pfreset.Run/NewDriver/RoutedAround`;`PhysicalDefaultRoute`;`route.DefaultPrivateCIDRs`;`bypassState.cidrs()`(即 `serverBypass`);`cfg.Bypass`。
- Produces: `Options.PFReset string`("on" 默认 / "off" / "dry-run");`func pfResetPrefixes(serverBypass, userBypass []string) []netip.Prefix`;`func runPFReset(ctx, mode string, serverBypass, userBypass []string)`(darwin 真跑,其他平台空)。

- [ ] **Step 1: 写失败测试(纯组装 + 模式判定)**

```go
package supervisor

import (
	"net/netip"
	"testing"
)

// 白名单必须同时装着私网、服务器旁路、用户 bypass —— 少任何一份,重置就会打到
// bx 自己安排走物理网卡的连接(到自己 VPS 的 ssh、局域网)。
func TestPFResetPrefixesCarryPrivateServerAndUserBypass(t *testing.T) {
	got := pfResetPrefixes([]string{"203.0.113.92/32"}, []string{"198.51.100.7"})
	for _, want := range []string{"10.0.0.0/8", "192.168.0.0/16", "203.0.113.92/32", "198.51.100.7/32"} {
		p := netip.MustParsePrefix(want)
		found := false
		for _, g := range got {
			if g == p {
				found = true
			}
		}
		if !found {
			t.Fatalf("routed-around %v misses %s", got, want)
		}
	}
}

// 三态:空/on 跑,off 不跑,dry-run 只看不装;认不出的当 off(少做不会漏 IP,多做会断连接)。
func TestPFResetModeDecidesWhetherToTouchPF(t *testing.T) {
	for mode, want := range map[string]pfResetDecision{"": pfResetRun, "on": pfResetRun, "off": pfResetSkip, "dry-run": pfResetDryRun, "bogus": pfResetSkip} {
		if got := decidePFReset(mode); got != want {
			t.Fatalf("mode %q → %v, want %v", mode, got, want)
		}
	}
}
```

- [ ] **Step 2: 跑,确认红**

Run: `go test ./internal/supervisor/ -run PFReset 2>&1 | tail -3`
Expected: `undefined: pfResetPrefixes` 等

- [ ] **Step 3: 最小实现**

`internal/supervisor/pfreset.go`(无 tag):

```go
package supervisor

import (
	"net/netip"

	"github.com/getbx/bx/internal/pfreset"
	"github.com/getbx/bx/internal/route"
)

type pfResetDecision int

const (
	pfResetSkip pfResetDecision = iota
	pfResetRun
	pfResetDryRun
)

// decidePFReset:空与 on 跑;off 不跑;dry-run 只打印将装的规则与将被重置的连接;
// 认不出的当 off —— 少做只是多漏几分钟(两段式会报),多做是断人连接。
func decidePFReset(mode string) pfResetDecision {
	switch mode {
	case "", "on":
		return pfResetRun
	case "dry-run":
		return pfResetDryRun
	default:
		return pfResetSkip
	}
}

// pfResetPrefixes 是白名单的组装:私网 + 服务器旁路 + 用户 bypass。
func pfResetPrefixes(serverBypass, userBypass []string) []netip.Prefix {
	return pfreset.RoutedAround(route.DefaultPrivateCIDRs, serverBypass, userBypass)
}
```

`internal/supervisor/pfreset_darwin.go`:

```go
//go:build darwin

package supervisor

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/getbx/bx/internal/appattr"
	"github.com/getbx/bx/internal/pfreset"
)

const pfResetDeadline = 10 * time.Second

func pfTokenPath() string { return filepath.Join(filepath.Dir(SockPath), "pf.token") }

// runPFReset 在 Hijack 成功之后跑一次(阻塞最多 pfResetDeadline)。观测用的判据与
// 网络守卫那条告警是同一份:appattr.StrayConnections + 同一组 routedAround。
func runPFReset(ctx context.Context, mode string, serverBypass, userBypass []string) {
	decision := decidePFReset(mode)
	if decision == pfResetSkip {
		return
	}
	_, device, err := PhysicalDefaultRoute(ctx)
	if err != nil || device == "" {
		log.Printf("pf reset skipped: no physical default route (%v)", err)
		return
	}
	around := pfResetPrefixes(serverBypass, userBypass)
	observe := func() int {
		pcbs, physical := darwinStraySnapshot(ctx, device)
		skip := func(pid int32) bool { return isOwnProcess(pid) || !processAlive(pid) }
		return len(appattr.StrayConnections(pcbs, physical, skip, around))
	}
	if decision == pfResetDryRun {
		n := observe()
		log.Printf("pf reset dry-run: %d connection(s) would be reset on %s with these rules:\n%s", n, device, pfreset.Rules(device, around))
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	out := pfreset.Run(ctx, pfreset.NewDriver(pfTokenPath()), pfreset.Options{
		Device: device, RoutedAround: around, Observe: observe, Tick: ticker.C, Deadline: pfResetDeadline, Log: log.Printf,
	})
	if out.Err != nil {
		log.Printf("pf reset: %v (initial %d, remaining %d)", out.Err, out.Initial, out.Remaining)
	}
}
```

`internal/supervisor/pfreset_other.go`:

```go
//go:build !darwin

package supervisor

import "context"

func runPFReset(context.Context, string, []string, []string) {}
```

在 `stray_darwin.go` 里把 `darwinStrayWarnings` 开头「取 device / physical / pcbs」抽成
`darwinStraySnapshot(ctx, device) (pcbs []appattr.PCB, physical []netip.Addr)`,
`darwinStrayWarnings` 改调它(行为不变,既有测试照绿)。

`run.go`:Options 加 `PFReset string // "on"(默认)/"off"/"dry-run":Hijack 之后一次性重置保护关着时开的连接`;
在 `log.Printf("%s", takeoverSummary(...))` **之前**加:

```go
		// 一次性把保护关着时开的连接重置掉(darwin;spec 2026-09-28)。阻塞最多 10 秒,
		// 每条退出路径都拆规则;没有残留就一个 pfctl 都不调。
		runPFReset(ctx, opts.PFReset, serverBypass, cfg.Bypass)
```

并在 `teardowns.push("restore default route", …)` **之后**、紧接着 push 一条
`teardowns.push("flush bx pf anchor", func() { _, _ = pfreset.FlushStaleDarwin(context.Background(), pfTokenPath()) })`
—— 非 darwin 那个函数是空的。

- [ ] **Step 4: 跑,确认绿;再加一条读源码的接线守卫**

`internal/supervisor/pfreset_wiring_test.go`:

```go
package supervisor

import (
	"os"
	"strings"
	"testing"
)

// Run() 里 pf 重置必须在 Hijack 成功之后、接管播报之前,而且拆除台账里要有那条冲
// anchor 的登记。读源码是这里唯一够得着的办法(Run() 只在 netns 集成台里跑,而那是
// linux)。锚点漂了就响亮失败。
func TestRunWiresThePFResetAfterHijack(t *testing.T) {
	src, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	hijack := strings.Index(s, "plat.Hijack(tunH, serverBypass, cfg.Bypass)")
	reset := strings.Index(s, "runPFReset(ctx, opts.PFReset, serverBypass, cfg.Bypass)")
	summary := strings.Index(s, "takeoverSummary(global, cfg.Mode, listsOverridden)")
	flush := strings.Index(s, `teardowns.push("flush bx pf anchor"`)
	if hijack < 0 || reset < 0 || summary < 0 || flush < 0 {
		t.Fatalf("anchors moved: hijack=%d reset=%d summary=%d flush=%d", hijack, reset, summary, flush)
	}
	if !(hijack < flush && flush < reset && reset < summary) {
		t.Fatalf("order must be Hijack → push flush → runPFReset → summary, got hijack=%d flush=%d reset=%d summary=%d", hijack, flush, reset, summary)
	}
}
```

Run: `go test ./internal/supervisor/ 2>&1 | tail -1 && go build ./...`
Expected: `ok`

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor
git commit -m "feat(supervisor): Hijack 之后一次性 pf 重置残留连接(darwin),三态由 Options.PFReset 定"
```

---

### Task 5: 三条清理路:Core defer(已在 Task 4)、`bx down` 强制拆除、Guardian 起 Core 之前

**Files:**
- Modify: `internal/cli/guardian.go`(`forcedMacOSTeardown` 加第 7 步;`macOSLifecycleDeps` 加 `flushPFReset func(context.Context) error`)
- Modify: `internal/guardian/manager.go:1386`(`startCoreLockedWithBarrierRelease` 起 Core 之前)
- Test: `internal/cli/guardian_test.go`(六步序列的那条测试改成七步)、`internal/guardian/pfreset_test.go`

**Interfaces:**
- Consumes: `pfreset.FlushStaleDarwin(ctx, tokenPath)`;token 路径与 supervisor 同一处 —— 把 `pfTokenPath` 导出成 `supervisor.PFTokenPath()`(darwin 与 other 都有)。

- [ ] **Step 1: 先找到钉六步顺序的那条测试,读它**

Run: `grep -n 'forcedMacOSTeardown' internal/cli/*_test.go | head`
读到它怎么记录步骤名(多半是 deps 里每个函数往一个 slice append)。

- [ ] **Step 2: 写失败测试:第 7 步「flush bx pf anchor」在,而且排在最后,失败不阻断**

按那条测试的写法加一条:deps.flushPFReset 记 `"pf"`,期望顺序末尾是它;再一条:flushPFReset 返回错误时,前六步照做、返回的错误里含 `pf`。

- [ ] **Step 3: Guardian 那半的失败测试**

```go
package guardian

import (
	"context"
	"testing"
)

// 上一个 Core 崩溃留下的 anchor 会把 en0 上非 root 的公网 TCP 全拒掉,而新 Core 起来
// 之后一切看起来正常。起 Core 之前先冲。做成 Manager 的一个可替换字段,让这一跳可测。
func TestManagerFlushesAStalePFAnchorBeforeStartingCore(t *testing.T) {
	m := newTestManager(t) // 用本包既有的 Manager 测试构造器,名字以 manager_test.go 里的为准
	called := false
	m.flushStalePF = func(context.Context) (bool, error) { called = true; return true, nil }
	_, _ = m.startCoreLockedWithBarrierRelease(context.Background(), false)
	if !called {
		t.Fatal("startCore must flush a stale bx pf anchor first")
	}
}
```

- [ ] **Step 4: 最小实现**

`internal/cli/guardian.go`:deps 加 `flushPFReset func(context.Context) error`,生产接线
`func(ctx context.Context) error { _, err := pfreset.FlushStaleDarwin(ctx, supervisor.PFTokenPath()); return err }`;
`forcedMacOSTeardown` 末尾加第 7 步,失败并进 failures、不阻断。

`internal/guardian/manager.go`:Manager 加字段 `flushStalePF func(context.Context) (bool, error)`
(构造时默认 `func(ctx) { return pfreset.FlushStaleDarwin(ctx, supervisor.PFTokenPath()) }`);
`startCoreLockedWithBarrierRelease` 第一句:

```go
	if m.flushStalePF != nil {
		if flushed, err := m.flushStalePF(ctx); flushed || err != nil {
			log.Printf("guardian_pf_reset_stale flushed=%v err=%v", flushed, err)
		}
	}
```

- [ ] **Step 5: 跑,确认绿**

Run: `go test ./internal/cli/ ./internal/guardian/ 2>&1 | tail -2`
Expected: 两个 `ok`

- [ ] **Step 6: 提交**

```bash
git add internal/cli internal/guardian internal/supervisor
git commit -m "feat(pfreset): 残留 anchor 的两条清理路:bx down 强制拆除第 7 步、Guardian 起 Core 之前"
```

---

### Task 6: `bx run --pf-reset` 与 `bx doctor` 的残留检查

**Files:**
- Modify: `internal/cli/cli.go:3537-3545`(`bx run` 的 flags)与把它递进 `supervisor.Options.PFReset` 的那处
- Modify: `internal/platformcheck/darwin.go`(加 `pf_reset_residue` check)
- Test: `internal/cli/cli_test.go`(flag → Options)、`internal/platformcheck/darwin_test.go`

- [ ] **Step 1: 写失败测试**

cli:找 `bx run` 现有 flag 的测试(如 `no-hijack` 怎么被断言递进 Options),照那条加 `--pf-reset dry-run` ⇒ `Options.PFReset == "dry-run"`,默认空。

platformcheck:

```go
func TestPFResetResidueCheckReadsTheAnchorAndTheToken(t *testing.T) {
	c := darwinPFResetResidueCheck("block return-rst out quick on en0 ...\n", true)
	if c.Status != "warn" || !strings.Contains(c.Hint, "sudo bx down") {
		t.Fatalf("leftover rules must warn and point at bx down, got %+v", c)
	}
	if c := darwinPFResetResidueCheck("", false); c.Status != "ok" {
		t.Fatalf("clean must be ok, got %+v", c)
	}
}
```

- [ ] **Step 2: 跑,确认红;最小实现**

flag:`&cli.StringFlag{Name: "pf-reset", Value: "on", Usage: "after the routes are hijacked, reset connections opened while protection was off so their apps reconnect through bx: on, off, or dry-run (print what would be reset; touch nothing)"}`,递进 `Options.PFReset`。

platformcheck:纯判定 `darwinPFResetResidueCheck(rulesOut string, tokenExists bool) Check`,采集处调 `darwinCommand(ctx, "pfctl", "-a", pfreset.Anchor, "-s", "rules")` 与 `os.Stat(supervisor.PFTokenPath())`——若本包纯度守卫禁 `os`,采集放到 `internal/cli/doctor_facts.go` 与 `internal/guardian/doctor.go` 各自那侧、判定留在 platformcheck。

- [ ] **Step 3: 跑,确认绿(含 doctor golden)**

Run: `go test ./internal/cli/ ./internal/platformcheck/ ./internal/doctor/ 2>&1 | tail -3`
Expected: 全 `ok`。golden 若红,说明 Platform 列表的顺序/名字进了契约:按 `internal/doctor/CLAUDE.md` 的规矩更新 golden 并在提交信息里说明。

- [ ] **Step 4: 提交**

```bash
git add internal/cli internal/platformcheck internal/doctor
git commit -m "feat(cli,doctor): bx run --pf-reset 三态;doctor 报 bx pf anchor 残留"
```

---

### Task 7: 文档与真机验收清单

**Files:**
- Modify: `docs/known-gaps.md`(A11 那行:实现已落地、真机未验)
- Modify: `docs/acceptance-pending.md`(加 A13:pf 重置的梯度验收)
- Modify: `internal/supervisor/CLAUDE.md`(一节:pf 重置的判据、三条清理路、为什么一次性)
- Modify: `docs/superpowers/specs/2026-09-28-bx-reset-stray-connections-design.md`(状态行改「已实施,真机未验」)

- [ ] **Step 1: 写 A13**

```
### A13. 一次性 pf 重置残留连接(2026-09-28,v0.4.14 起)—— 由 agent 做,带抓包,先 dry-run

前提:所有者在场;`bx status` Protected;`sudo tcpdump -ni en0 -w <scratch>/pfreset.pcap` 开着。
1. `bx down`,开几个网页、让 Mail/WeChat 重连,等 20 秒。
2. `sudo bx run --pf-reset dry-run --test-timeout 2m` **不可行**(Guardian 管着 Core,两个 Core 冲突)
   → 改走:`bx up`,看 Core 日志 `/var/log/bx.log` 里 `pf reset:` 那几行:初始几条、几秒清零、有没有封顶。
   (dry-run 只在 Guardian 没在管的机器上用 `bx run` 跑;所有者的 Mac 直接看日志。)
3. 抓包用 python 逐包分类:`bx up` 之后 en0 上非 bx 进程到公网的 TCP 是否在几秒内只剩 RST;
   之后没有新的明文 SYN。
4. 菜单:`Settling` 那行要么不出现、要么几秒内消失;`Outside bx` 不出现。
5. 到自己 VPS 的 ssh(Codex 跳板)**不许断**;局域网设备不许断;Tailscale 不许断。
6. `bx down` 之后 `sudo pfctl -a com.apple/250.bx -s rules` 为空、`/var/run/bx/pf.token` 不在。
7. 对照:`sudo pfctl -s info` 里 pf 的状态与 `bx up` 之前一致(bx 释放了自己的引用)。
```

- [ ] **Step 2: 全量 verify,提交**

Run: `bash scripts/verify.sh`
Expected: `✓ verify passed (all steps ran)`

```bash
git add docs internal/supervisor/CLAUDE.md
git commit -m "docs(pfreset): 判据、三条清理路、真机验收 A13"
```
