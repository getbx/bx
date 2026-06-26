# Task 9 验证基建 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 bx 铺好"不依赖物理 Mudi 就能验证特权路由代码"的跑道:一个 netns 门控 PoC(验证现有 `netConf.up()/down()` 路由往返)+ CI 集成 job + 本地 Colima 跑法文档。

**Architecture:** PoC 是一个 `//go:build integration && linux` 测试,用 `unshare(CLONE_NEWNET)`+`LockOSThread` 把测试线程移进一个临时 netns,用 dummy 网卡当 `tunName`,直接调 bx 现有未导出的 `netConf`(Hijack 的路由核心)做 apply→断言→restore→断言基线。零新增生产代码。CI 加一个 ubuntu+sudo 跑 `-tags integration` 的 job;再加一份 Colima 本地跑法文档。

**Tech Stack:** Go 1.26.3;`golang.org/x/sys/unix`(已有依赖,`CLONE_NEWNET`/`Unshare`);`iproute2`(`ip` 命令);GitHub Actions。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3,`CGO_ENABLED=0` 静态二进制。
- **门控**:PoC 测试带 `//go:build integration && linux`;普通 `go test ./...`(本机 Mac、现有 CI `test` job)**永不编译/运行**它。
- **前置 skip**:测试开头检测非 root 或缺 `ip` 命令 → `t.Skip`,绝不误跑。
- **netns 隔离(安全底线)**:所有路由改动只在测试自建的临时 netns 内;**绝不触碰宿主 / CI runner 的真实路由**。
- **零新增生产代码**:只新增测试 + CI YAML + 文档;不改 `internal/` 下任何生产 `.go`。
- 不碰 Task 9 生产代码(真快照器 / MCP 接线 / `newSystemSnapshotter`)。
- 提交信息:中文 conventional commits,结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`。在 master 直接提交。
- **本机验证局限(诚实记录)**:本机是 macOS,跑不了这个 Linux+root+netns 测试。本机能做的验证 = ① `GOOS=linux go vet -tags integration ./internal/supervisor/` 编得过;② `go test ./...`(无 tag)在 Mac 上**排除**它、仍全绿。**测试的真实 pass 发生在 Task 2 的 CI 首跑或用户的 Colima**,本 plan 不假装本机能跑绿。

---

### Task 1: netns PoC —— `netConf.up()/down()` 路由往返(门控测试)

**Files:**
- Create: `internal/supervisor/hijack_netns_linux_test.go`(`//go:build integration && linux`,`package supervisor`)

**Interfaces:**
- Consumes(均为 bx 现有、未导出,同包可访问 —— 见 `internal/supervisor/platform_linux.go`):
  - `type netConf struct { tunName, tunAddr, gw, gwDev string; bypass, mainLookup []string; blockV6 bool; mainLookupV6 []string }`
  - `func (n *netConf) up() error` —— 执行 `ip addr/link/route/rule` 装策略路由。
  - `func (n *netConf) down()` —— 对称还原(忽略单步错误)。
  - `route.DefaultPrivateCIDRs []string`(私网段,含 `100.64.0.0/10` CGNAT)—— 包 `github.com/getbx/bx/internal/route`。
- Produces: 无(纯测试)。

**背景(实现者必读):** `netConf.up()` 只靠接口**名**操作(`ip addr add <tunAddr> dev <tunName>`、`route add default dev <tunName> table 100`、一串 `rule add`),**不需要真实 TUN 设备**——一个 `dummy` 网卡即可当 `tunName`。`bypass` 留空时 `up()` 不使用 `gw`/`gwDev`,故无需可达网关。`up()`/`down()` 内部用 `exec.Command("ip", ...)` 在**当前线程的 netns** 里执行,所以测试必须先把本线程 `unshare` 进新 netns。

- [ ] **Step 1: 写测试文件**

Create `internal/supervisor/hijack_netns_linux_test.go`:
```go
//go:build integration && linux

package supervisor

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/route"
	"golang.org/x/sys/unix"
)

// mustIP 在当前(已 unshare 的)netns 内执行 ip 命令,失败即 fatal。
func mustIP(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ip %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// ipRuleList 返回当前 netns 的 `ip rule list` 文本(用于基线比对与断言)。
func ipRuleList(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("ip", "rule", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("ip rule list: %v\n%s", err, out)
	}
	return string(out)
}

// TestNetConfRoundTripInNetns 在一个临时 netns 内证明:bx 现有的 netConf.up() 装上策略路由、
// down() 干净还原到基线。这是 Task 9 真快照器的"验证方式可行性" PoC —— 不发任何真实外网流量,
// 只断言 ip rule/route 状态,故宿主是否挂 VPN 与结果无关。需 root,门控在 build tag。
func TestNetConfRoundTripInNetns(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需要 root(在 Colima VM 或 CI 里以 sudo 运行)")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("缺 ip 命令(iproute2)")
	}

	// 把本 goroutine 钉在当前 OS 线程并 unshare 进全新空 netns。
	// 故意不 UnlockOSThread:goroutine 结束时 Go 运行时销毁该线程,临时 netns 随之消失,
	// 绝不污染宿主/runner 的真实 netns。
	runtime.LockOSThread()
	if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
		t.Skipf("unshare(CLONE_NEWNET) 失败(无 CAP_SYS_ADMIN?): %v", err)
	}

	// 新 netns 里只有一个 down 的 lo;补齐最小拓扑:lo up + 一个 dummy 当 tunName。
	mustIP(t, "link", "set", "lo", "up")
	mustIP(t, "link", "add", "bxtest0", "type", "dummy")

	base := ipRuleList(t) // 改动前基线

	nc := &netConf{
		tunName:    "bxtest0",
		tunAddr:    "198.51.100.1/30",
		mainLookup: route.DefaultPrivateCIDRs, // 触发 pref 150(+CGNAT pref 149)
		// bypass 留空 → 不需要可达 gw;blockV6 false → 只验 v4(聚焦)。
	}

	if err := nc.up(); err != nil {
		t.Fatalf("netConf.up(): %v", err)
	}

	// 断言策略路由就位。
	rules := ipRuleList(t)
	for _, want := range []string{"100:", "150:", "200:"} {
		if !strings.Contains(rules, want) {
			t.Fatalf("up() 后缺策略规则 %q;ip rule list=\n%s", want, rules)
		}
	}
	if strings.Contains(strings.Join(route.DefaultPrivateCIDRs, ","), "100.64.0.0/10") &&
		!strings.Contains(rules, "149:") {
		t.Fatalf("up() 后缺 CGNAT pref 149 规则;ip rule list=\n%s", rules)
	}
	rt := func() string {
		out, _ := exec.Command("ip", "route", "show", "table", "100").CombinedOutput()
		return string(out)
	}()
	if !strings.Contains(rt, "default") || !strings.Contains(rt, "bxtest0") {
		t.Fatalf("table 100 缺 default dev bxtest0;得到:\n%s", rt)
	}

	// 还原并断言回到基线。
	nc.down()
	if after := ipRuleList(t); after != base {
		t.Fatalf("down() 未干净还原:\n--- base ---\n%s\n--- after ---\n%s", base, after)
	}
}
```

- [ ] **Step 2: 本机编译验证(Linux 交叉 vet)**

Run:
```bash
cd /Users/nategu_mac_company/Documents/bx
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: 无输出、退出码 0(测试在 linux+integration 下编得过、引用的 `netConf`/`route.DefaultPrivateCIDRs`/`unix.Unshare`/`unix.CLONE_NEWNET` 都存在)。
若报 `netConf`/字段名不符,打开 `internal/supervisor/platform_linux.go` 核对真实字段并改正(不得改生产代码,只改测试)。

- [ ] **Step 3: 本机门控验证(确认不误跑、不影响现有套件)**

Run:
```bash
go test ./internal/supervisor/ 2>&1 | tail -3
go build ./... && echo "build OK"
```
Expected: `internal/supervisor` 现有测试照常(本测试因 `integration` tag 被排除,**不出现** `TestNetConfRoundTripInNetns`);build 绿。这证明门控生效:普通流程碰不到它。

- [ ] **Step 4: (可选)真机/容器实跑**

如有 Linux 主机或特权容器,可实跑确认 PoC 通过(本机 Mac 跳过本步):
```bash
# 例:在一台 Linux 上
sudo "$(which go)" test -tags integration -run TestNetConfRoundTripInNetns ./internal/supervisor/ -v
```
Expected: PASS。**本机 macOS 无法执行此步**;PoC 的真实 pass 留待 Task 2 的 CI 首跑或用户 Colima。报告里注明本步是否实跑过。

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/hijack_netns_linux_test.go
git commit -m "test(supervisor): netns 门控 PoC —— netConf 路由往返(验证 Task 9 方式可行)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: CI 集成 job + 本地 Colima 跑法文档

**Files:**
- Modify: `.github/workflows/ci.yml`(新增 `integration` job,不动现有 `test` job)
- Create: `docs/integration-testing.md`(本地 Colima 跑法)

**Interfaces:**
- Consumes: Task 1 的 `//go:build integration` 测试(CI job 跑 `-tags integration` 时被纳入)。
- Produces: 无代码接口。

- [ ] **Step 1: 加 CI `integration` job**

在 `.github/workflows/ci.yml` 的 `jobs:` 下,现有 `test:` job **之后**追加(现有 `test` job 一字不改):
```yaml
  integration:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'

      # 门控的 netns 集成测试需 root(unshare CLONE_NEWNET + ip rule/route)。
      # 以 sudo 跑 go 的完整路径;普通 test job 仍跑无 root 单测,互不影响。
      - name: Integration tests (netns, root)
        run: sudo "$(which go)" test -tags integration ./...
```

- [ ] **Step 2: 本机校验 workflow 语法 + 现有套件不受影响**

Run:
```bash
cd /Users/nategu_mac_company/Documents/bx
python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/ci.yml')); print('yaml OK')"
go test ./... 2>&1 | tail -5
```
Expected: `yaml OK`;`go test ./...` 仍全绿(无 `integration` tag,PoC 被排除)。
注:`integration` job 的**真实首验**是本次推送后 GitHub Actions 的首跑(本机/Mac 无法本地复现 ubuntu+root+netns)。这是该 job 的预期验证点,plan 不假装本机能验。

- [ ] **Step 3: 写本地 Colima 跑法文档**

Create `docs/integration-testing.md`:
```markdown
# 集成测试(netns,需 root)— 本地 Colima 跑法

bx 的门控集成测试(`//go:build integration`,如 netns 路由往返 PoC)需要 Linux + root +
网络命名空间。macOS 上跑不了,但**不必有物理 Linux 机**——用 Colima 起一个 Linux VM 即可。
所有路由改动只发生在测试自建的临时 netns 内,**不碰宿主、不碰你的 VPN/TUN**。

## 一次性准备
```sh
brew install colima docker   # 若未装
colima start                 # 起一个 Linux VM(独立网络,不劫持你的默认路由/VPN)
```

## 跑集成测试
```sh
# 在仓库根目录;Colima 默认把当前目录挂进 VM
colima ssh -- 'cd /Users/<你>/path/to/bx && sudo "$(which go)" test -tags integration ./... -v'
```
或进 VM 手动跑:
```sh
colima ssh
cd <repo>
sudo "$(which go)" test -tags integration ./internal/supervisor/ -run NetConfRoundTrip -v
```

## 能验什么 / 不能验什么(重要)
- **能验**:不发真实外网包的逻辑(路由规则装/拆、分流决策)。宿主是否挂 VPN 与结果无关。
- **不能验**:真实出口 IP / 泄漏审计。Colima VM 的真实出网经宿主 Mac;**宿主挂 VPN 时出口已被污染**,
  VM 里"测出口=VPS"不可信。真实泄漏审计**只能在真机(Mudi)的干净 WAN 上做**(见
  `docs/superpowers/specs/2026-06-25-task9-validation-harness-design.md`)。

## CI
GitHub Actions 的 `integration` job(`.github/workflows/ci.yml`)每次 push 在 ubuntu runner 上
以 root 自动跑这些测试,通常无需本地手跑。
```

- [ ] **Step 4: 提交**

```bash
git add .github/workflows/ci.yml docs/integration-testing.md
git commit -m "ci(integration): netns 集成 job(ubuntu+sudo)+ 本地 Colima 跑法文档

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- 交付物 A(本地 netns harness)→ Task 2 Step 3(`docs/integration-testing.md` + 测试自建 netns,Task 1 实现)。
- 交付物 B(CI 集成 job)→ Task 2 Step 1。
- 交付物 C(Hijack 往返 PoC)→ Task 1(以 `netConf.up()/down()` 为现有 Hijack 的路由核心,零新生产代码)。
- 安全与隔离(门控 / 非 root 或缺 ip 即 skip / 临时 netns / 不碰宿主)→ Task 1 Step 1 测试体 + Global Constraints。
- "能验/不能验 + VM-on-VPN 限制"→ Task 2 Step 3 文档明确写入。
- 不碰 Task 9 生产代码 → Global Constraints + 仅新增测试/YAML/文档。

**占位扫描:** 无 TBD/TODO 式占位;每段代码完整。Task 1 Step 4「可选真机实跑」与「本机无法跑绿」是对 macOS 开发环境的诚实记录(本就是这套基建存在的理由),非占位。

**类型一致性:** 测试引用的 `netConf` 及字段(`tunName`/`tunAddr`/`mainLookup`/`bypass`/`blockV6`)、`(*netConf).up()/down()`、`route.DefaultPrivateCIDRs`、`unix.Unshare`/`unix.CLONE_NEWNET` 均与 `internal/supervisor/platform_linux.go` 现状一致(已核对真实源码)。CI job 名 `integration` 与 Global Constraints 一致;现有 `test` job 不动。
