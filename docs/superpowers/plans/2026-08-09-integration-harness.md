# bx 集成台 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在临时 netns 里跑真正的 `supervisor.Run()`,做一次真实的服务器切换,然后去问内核路由表与屏障开口——把本轮五轮修复里那些**静默**的 bug 变成会红的测试。

**Architecture:** 长在现有 netns 基建上(`unshare(CLONE_NEWNET)` + 锁 OS 线程 + `//go:build integration && linux` + root,CI 已在跑)。唯一的生产改动是给 `Run()` 开一道 `BuildTunnel` 注入缝;平台、路由、DNS、屏障全部走生产代码。

**Tech Stack:** Go 1.26、`golang.org/x/sys/unix`(namespace)、iproute2(`ip` 命令做断言)、`internal/tunnel` 的导出构造器 `tunnel.New`。

## Global Constraints

- **台子绝不能碰到宿主上正在运行的 bx。** `supervisor.SockPath` 是包级常量 `/run/bx/core.sock`,而 netns **不隔离文件系统**;`control.go:511` 在监听前 `os.Remove(SockPath)`。故台子必须**同时 unshare mount namespace 并在 `/run` 上挂 tmpfs**。这一条是硬要求,不是优化。
- **注入缝 `nil` 分支必须与今天逐字节相同**,由「既有测试零改动」证明。
- **台子只替换建隧道这一件事**;平台、路由、DNS、屏障、控制面全部走生产代码。
- **不发任何真实外网流量**,不拉任何子进程;断言只看内核状态与控制面应答。
- 配置一律 `global: true`,绕开 china 列表(它在 global 模式下根本不加载)。
- **每条断言都要用变异证明它会红**,且变异必须取自本轮真实发生过的 bug。
- TDD;中文 conventional commits,结尾 `Co-Authored-By: Claude <noreply@anthropic.com>`;直接在 `master` 提交。
- 验证:`go build ./... && go vet ./... && go test ./... -count=1`;交叉编译 linux/darwin/windows × amd64/arm64;`gofumpt -l`(排除 `internal/embedded/assets/` 与 `internal/winfw/`)。集成台另跑 `sudo go test -tags integration ./internal/supervisor -run <名字> -v`。
- **agent 不得在 macOS 宿主上运行集成台**(它是 linux-only,且需 root)。只做交叉编译与 `go vet -tags integration` 的语法/类型检查;真跑交给 CI 或用户的 Linux 环境。

---

## 文件结构

| 文件 | 职责 | 任务 |
|---|---|---|
| `internal/supervisor/run.go` | `Options` 加 `BuildTunnel` 字段;`buildTunnel` 闭包在字段非 nil 时让路 | 1 |
| `internal/supervisor/harness_netns_linux_test.go`(新) | netns+mountns 隔离、假隧道、启停 `Run()` 的台子骨架 | 2 |
| `internal/supervisor/harness_bypass_netns_linux_test.go`(新) | 断言 1、2(bypass 覆盖;已知服务器间切换不动路由) | 3 |
| `internal/supervisor/harness_switch_netns_linux_test.go`(新) | 断言 3、4(解析失败拒绝切换;屏障开口等于当前服务器) | 4 |
| `.github/workflows/ci.yml` | 集成 job 断言新台子真跑过 | 5 |
| `internal/supervisor/runwiring_ast_test.go` | 被台子覆盖到的 AST 接线守卫逐条降级/删除 | 5 |

---

## Task 1: 给 `Run()` 开 `BuildTunnel` 注入缝

**Files:**
- Modify: `internal/supervisor/run.go`(`Options` 结构体;`buildTunnel` 闭包开头)
- Test: `internal/supervisor/run_options_test.go`(新)

**Interfaces:**
- Produces: `Options.BuildTunnel func(link, recoveryID string, auxiliaryHTTP bool) (*tunnel.Tunnel, error)`。nil = 生产默认。

- [ ] **Step 1: 写失败测试**

`internal/supervisor/run_options_test.go`:

```go
package supervisor

import (
	"reflect"
	"testing"
)

// 注入缝的存在理由是集成台要在 netns 里跑完整 Run() 而不发任何真实外网流量。
// 它是本代码库唯一一处「组装根可以被外部指定」的缝 —— 本轮两个 Critical 都住在
// Run() 的接线里,而接线不可测正是它们能活下来的原因。
func TestOptionsExposeTunnelBuilderSeam(t *testing.T) {
	f, ok := reflect.TypeOf(Options{}).FieldByName("BuildTunnel")
	if !ok {
		t.Fatal("Options 需要 BuildTunnel 注入缝")
	}
	want := "func(string, string, bool) (*tunnel.Tunnel, error)"
	if got := f.Type.String(); got != want {
		t.Fatalf("BuildTunnel 签名必须与内部 buildTunnel 闭包一致\ngot  %s\nwant %s", got, want)
	}
}
```

- [ ] **Step 2: 跑它,确认失败**

Run: `go test ./internal/supervisor -run TestOptionsExposeTunnelBuilderSeam -count=1`
Expected: FAIL,`Options 需要 BuildTunnel 注入缝`

- [ ] **Step 3: 实现**

`Options` 末尾加:

```go
	// BuildTunnel 可选:替换建隧道的方式。nil = 生产默认(拉起 brook/sing-box 子进程)。
	//
	// 存在的理由是集成台要在 netns 里跑完整 Run() 而不发任何真实外网流量。这是本
	// 代码库唯一一处「组装根可以被外部指定」的缝:2026-08-09 那一轮的两个 Critical
	// 都住在 Run() 的接线里,而接线不可测正是它们能活到复审第三轮的原因。
	//
	// 非 nil 时**只**替换建隧道这一件事;平台、路由、DNS、屏障、控制面一律走生产代码。
	BuildTunnel func(link, recoveryID string, auxiliaryHTTP bool) (*tunnel.Tunnel, error)
```

`buildTunnel` 闭包的**第一行**(拿到锁之后)让路:

```go
	buildTunnel := func(link, recoveryID string, auxiliaryHTTP bool) (*tunnel.Tunnel, error) {
		buildTunnelMu.Lock()
		defer buildTunnelMu.Unlock()
		if opts.BuildTunnel != nil {
			return opts.BuildTunnel(link, recoveryID, auxiliaryHTTP)
		}
		httpAddr, err := privateAuxiliaryAddr(cfg.HTTPProxy, auxiliaryHTTP)
		// …以下逐字不动…
```

- [ ] **Step 4: 跑测试确认通过,并证明生产路径没变**

Run: `go test ./... -count=1`
Expected: PASS,**既有测试零改动**——这是「nil 分支与今天逐字节相同」的证据。

- [ ] **Step 5: 变异验证**

把让路那一句改成无条件走 `opts.BuildTunnel`(不判 nil),确认 `go test ./...` 里有测试因空指针/行为变化转红;若**没有**任何测试转红,如实记录——那说明生产建隧道路径本身零覆盖,是台子建成后要补的第一件事。改回。

- [ ] **Step 6: 提交**

```bash
git add internal/supervisor/run.go internal/supervisor/run_options_test.go
git commit -m "feat(supervisor): Options 开一道 BuildTunnel 注入缝,供集成台跑完整 Run()"
```

---

## Task 2: 台子骨架 —— 隔离、假隧道、启停 `Run()`

**这是可行性风险集中的一步。** 若 TUN 在 netns 里建不起来、或 `Run()` 有别的硬依赖走不通,**BLOCKED 是可接受的结果**,但必须带回具体的失败点与已排除的假设,不要绕过去。

**Files:**
- Create: `internal/supervisor/harness_netns_linux_test.go`

**Interfaces:**
- Consumes: `Options.BuildTunnel`(Task 1)、`tunnel.New`(已导出)
- Produces(供 Task 3、4 复用):
  - `func enterIsolatedNetns(t *testing.T)` —— unshare net+mount,`/run` 挂 tmpfs,lo up
  - `func fakeTunnelBuilder(t *testing.T) func(string, string, bool) (*tunnel.Tunnel, error)`
  - `func startHarness(t *testing.T, cfgYAML string) *harness`,`harness` 带 `sockPath`、`stop()`
  - `func ipOut(t *testing.T, args ...string) string`

- [ ] **Step 1: 写失败测试(先只要「起得来、停得干净」)**

```go
//go:build integration && linux

package supervisor

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
```

- [ ] **Step 2: 跑它,确认失败**

Run: `sudo go test -tags integration ./internal/supervisor -run TestHarnessStartsAndRestoresCleanly -v`
(agent 在 macOS 上跑不了;此步至少要做到 `GOOS=linux go vet -tags integration ./internal/supervisor` 通过,真跑交给 CI 或用户的 Linux 环境。)
Expected: FAIL,`undefined: enterIsolatedNetns`

- [ ] **Step 3: 实现骨架**

`enterIsolatedNetns`:

```go
func enterIsolatedNetns(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("需要 root")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("缺 ip 命令(iproute2)")
	}
	// 钉住 OS 线程;故意不 UnlockOSThread —— goroutine 结束时运行时销毁该线程,
	// 临时 namespace 随之消失,绝不污染宿主。
	runtime.LockOSThread()
	if err := unix.Unshare(unix.CLONE_NEWNET | unix.CLONE_NEWNS); err != nil {
		t.Skipf("unshare(NET|NS) 失败(无 CAP_SYS_ADMIN?): %v", err)
	}
	// 让本 mount ns 的挂载不外泄到宿主。
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatalf("把根挂载改私有: %v", err)
	}
	// /run 换成 tmpfs:SockPath 与 core.pid 就此落在一次性文件系统里,
	// 宿主上正在跑的 bx 完全看不见,也不会被 os.Remove 掉。
	if err := unix.Mount("tmpfs", "/run", "tmpfs", 0, ""); err != nil {
		t.Fatalf("在 /run 挂 tmpfs: %v", err)
	}
	mustIP(t, "link", "set", "lo", "up")
}
```

`fakeTunnelBuilder`:

```go
// 假隧道:进程内 socks5 服务端 + 空 Runner + 立即健康。不拉子进程、不发外网包。
// tunnel.New(socksAddr, RunnerFactory, HealthCheck) 本就是导出的,故台子无需碰
// internal/tunnel 的任何内部结构。
func fakeTunnelBuilder(t *testing.T) func(string, string, bool) (*tunnel.Tunnel, error) {
	t.Helper()
	addr := startFakeSocks5(t) // 见下
	return func(link, recoveryID string, auxiliaryHTTP bool) (*tunnel.Tunnel, error) {
		return tunnel.New(addr,
			func(string) (tunnel.Runner, error) { return noopRunner{}, nil },
			func(string) (int64, error) { return 1, nil },
		), nil
	}
}

type noopRunner struct{ done chan struct{} }

func (noopRunner) Wait() error { select {} } // 永不退出:真隧道进程也是这样
func (noopRunner) Kill() error { return nil }
```

`startFakeSocks5` 写一个最小 socks5 CONNECT 服务端(no-auth、只认 CONNECT、把连接转给一个本地 echo 或直接回 success 后关闭)。**参照 `internal/socks5/*_test.go` 里既有的 `fakeServer`**,但那份是 test-scoped 不能跨包 import,故在本文件内重写一份最小的。

`startHarness` 写配置到 `t.TempDir()`,起 `go Run(ctx, cfg, opts)`,轮询 `SockPath` 出现或超时,返回带 `stop()`(cancel + 等 Run 返回)的句柄。

`twoServerHarnessConfig` 用 `global: true`,两台服务器指向**可解析**的地址(直接写 IP 字面量,避免依赖 DNS)。

- [ ] **Step 4: 跑测试确认通过**

Run: `sudo go test -tags integration ./internal/supervisor -run TestHarnessStartsAndRestoresCleanly -v`
Expected: PASS。**若 BLOCKED**,报告里必须写清:卡在 `Run()` 的哪一步、报什么错、已排除哪些原因。

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/harness_netns_linux_test.go
git commit -m "test(supervisor): netns+mountns 集成台骨架,起停完整 Run()"
```

---

## Task 3: 断言 1、2 —— bypass 覆盖与切换不动路由

**Files:**
- Create: `internal/supervisor/harness_bypass_netns_linux_test.go`

**Interfaces:**
- Consumes: Task 2 的 `enterIsolatedNetns`/`startHarness`/`ipOut`

- [ ] **Step 1: 写失败测试**

```go
//go:build integration && linux

// 断言 1:清单里**每一台**服务器的**两条**链接对应的 IP,都真的在内核里有 bypass。
// 少一条 = 切过去之后隧道自己的流量被劫进 TUN = 成环,而成环是静默的
// (连得上、status 显绿、流量绕圈)。这条走过五轮修复,此前只有单元测试背书。
func TestHarnessBypassCoversEveryConfiguredServer(t *testing.T) {
	enterIsolatedNetns(t)
	h := startHarness(t, twoServerHarnessConfig)
	defer h.stop()

	rules := ipOut(t, "rule", "list")
	for _, ip := range []string{"192.0.2.10", "192.0.2.11", "198.51.100.20", "198.51.100.21"} {
		if !strings.Contains(rules, ip) {
			t.Errorf("服务器 %s 不在 bypass 里 —— 切过去就成环(静默)\nip rule list=\n%s", ip, rules)
		}
	}
}

// 断言 2:已知服务器之间切换**一条路由都不该动**(启动时已全部铺好)。
// 这是「全铺好」那条设计的唯一实证。
func TestHarnessSwitchBetweenKnownServersDoesNotTouchRoutes(t *testing.T) {
	enterIsolatedNetns(t)
	h := startHarness(t, twoServerHarnessConfig)
	defer h.stop()

	before := ipOut(t, "rule", "list") + ipOut(t, "route", "show", "table", "100")
	if _, err := SetServerControl(h.sockPath, "vless://u@198.51.100.20:443", "hysteria2://u@198.51.100.21:443"); err != nil {
		t.Fatalf("切换应成功: %v", err)
	}
	if _, err := CommitControl(h.sockPath); err != nil {
		t.Fatalf("确认应成功: %v", err)
	}
	after := ipOut(t, "rule", "list") + ipOut(t, "route", "show", "table", "100")
	if before != after {
		t.Fatalf("已知服务器之间切换不该动任何路由\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}
```

- [ ] **Step 2: 跑它,确认失败**(实现前 `bypassLinks` 若被改窄即红;此步先确认测试本身跑得起来)

Run: `sudo go test -tags integration ./internal/supervisor -run 'TestHarnessBypass|TestHarnessSwitch' -v`

- [ ] **Step 3: 变异验证 —— 这两条断言必须抓住真实发生过的 bug**

① 把 `bypassLinks` 改成只返回当前那台(`return links[:2]` 之类)→ 断言 1 必须红。
② 把 `handleSetServer` 的 `composeMutations(rhApply, rhUndo, swapApply, swapUndo)` 两对参数对调(先换传输再装路由)→ 断言 2 必须红(路由集合会变)。
两处改回。**任一变异没能让台子转红,如实报告** —— 那说明台子没测到它自称测的东西。

- [ ] **Step 4: 提交**

```bash
git add internal/supervisor/harness_bypass_netns_linux_test.go
git commit -m "test(supervisor): 集成台钉住 bypass 覆盖与「切换不动路由」"
```

---

## Task 4: 断言 3、4 —— 拒绝切换与屏障开口

**Files:**
- Create: `internal/supervisor/harness_switch_netns_linux_test.go`

- [ ] **Step 1: 写失败测试**

```go
//go:build integration && linux

// 断言 3:点名目标解析不出 IP 时必须**拒绝切换**并留在原地。
// 切过去 = bypass 没落实 = 成环。这条是 Task 6 第二轮修复的核心,从没被真机走过。
func TestHarnessRefusesSwitchWhenTargetUnresolvable(t *testing.T) {
	enterIsolatedNetns(t)
	h := startHarness(t, twoServerHarnessConfig)
	defer h.stop()

	beforeRT, err := FetchRuntimeState(h.sockPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetServerControl(h.sockPath, "vless://u@does-not-resolve.invalid:443", ""); err == nil {
		t.Fatal("解析不出 IP 的目标必须被拒绝 —— 切过去就是在 bypass 没落实的情况下换出口")
	}
	afterRT, err := FetchRuntimeState(h.sockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeRT.ServerBypass, afterRT.ServerBypass) {
		t.Fatalf("拒绝之后必须原地不动\nbefore=%v\nafter =%v", beforeRT.ServerBypass, afterRT.ServerBypass)
	}
}

// 断言 4:屏障开口(RuntimeState.ServerBypass → BarrierContext.ServerBypass)
// 只含当前那台的传输服务器地址 —— 不含用户 hosts: 覆盖、不含上一台。
// 它在 /2 reject 屏障上打洞,混进任何别的东西都是 fail-closed 上的一个口子。
// 这条走过五轮修复、三次翻车,全部只有单元测试背书。
func TestHarnessBarrierCarveOutFollowsCurrentServerOnly(t *testing.T) {
	enterIsolatedNetns(t)
	h := startHarness(t, harnessConfigWithHostOverride) // hosts: public-override.example → 203.0.113.77
	defer h.stop()

	if _, err := SetServerControl(h.sockPath, "vless://u@198.51.100.20:443", "hysteria2://u@198.51.100.21:443"); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitControl(h.sockPath); err != nil {
		t.Fatal(err)
	}
	rt, err := FetchRuntimeState(h.sockPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(rt.ServerBypass, ",")
	if strings.Contains(got, "203.0.113.77") {
		t.Fatalf("用户 hosts: 覆盖绝不能进屏障开口(它在 /2 reject 上打洞): %s", got)
	}
	for _, want := range []string{"198.51.100.20", "198.51.100.21"} {
		if !strings.Contains(got, want) {
			t.Errorf("刚切过去那台的 %s 必须在屏障开口里,否则屏障把它挡在外面: %s", want, got)
		}
	}
}
```

- [ ] **Step 2: 跑它**

Run: `sudo go test -tags integration ./internal/supervisor -run TestHarness -v`

- [ ] **Step 3: 变异验证**

③ 把 `handleSetServer` 里刷新失败那一支改成「只记日志、继续切」→ 断言 3 必须红。
④ 把 `bypassrefresh.go` 的 `d.store.set(next, staticA, serverStatic)` 写成 `set(next, staticA, staticA)` → 断言 4 必须红。
两处改回。

- [ ] **Step 4: 提交**

```bash
git add internal/supervisor/harness_switch_netns_linux_test.go
git commit -m "test(supervisor): 集成台钉住「解析失败拒绝切换」与屏障开口"
```

---

## Task 5: CI 接入,并逐条清算 AST 接线守卫

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `internal/supervisor/runwiring_ast_test.go`

- [ ] **Step 1: CI 断言台子真跑过**

`integration` job 已有「断言 netns PoC 真跑过(PASS 而非 SKIP)」的先例(`ci.yml:115`)。照同一手法为新台子加断言——`go test` 对 SKIP 也报成功,所以只看退出码等于没看:

```yaml
          grep -q -- '--- PASS: TestHarnessBarrierCarveOutFollowsCurrentServerOnly' /tmp/itest.log \
            || { echo "::error::集成台未真正运行(被 SKIP?)—— runner 可能缺 CAP_SYS_ADMIN 或 iproute2"; exit 1; }
```

- [ ] **Step 2: 逐条清算 AST 接线守卫**

`runwiring_ast_test.go` 里每一条守卫,逐条判定:

- **台子能覆盖 → 删掉**,并在提交信息里写明由哪条断言接手。它们是更弱的替代品(本轮被绕过 8 次),留着只会让人误以为有两道防线。
- **台子覆盖不到 → 留下**,并在注释里写明**为什么台子照不到它**(例如它守的是 macOS-only 路径)。

判定方法不是读代码:**对每条守卫所守的属性做一次变异,看台子红不红**。台子红 → 删守卫;台子绿 → 留守卫并记下这个缺口。

- [ ] **Step 3: 全量验证并提交**

Run: `go build ./... && go vet ./... && go test ./... -count=1`;`GOOS=linux go vet -tags integration ./internal/supervisor`;六向交叉编译;`gofumpt -l`。

```bash
git add .github/workflows/ci.yml internal/supervisor/runwiring_ast_test.go
git commit -m "ci: 集成台接入并断言真跑过;被它覆盖的 AST 接线守卫逐条退场"
```

---

## 完成后应当为真的判据

- `sudo go test -tags integration ./internal/supervisor` 里,五条断言全部 PASS 而非 SKIP。
- 本轮五轮修复里真实发生过的**五个** bug,每一个都能让台子转红(逐条实测记录在案)。
- `runwiring_ast_test.go` 里剩下的每一条守卫,都带着「台子为什么照不到它」的理由。

## 明确不做

- 不测真实隧道协议(reality/hysteria2 的握手已有真机 e2e 背书)。
- 不发真实外网流量。
- **不覆盖 macOS 与 Windows**。台子照不到主平台,`CLAUDE.md` 的「真机未验」清单不因它存在而清空。
