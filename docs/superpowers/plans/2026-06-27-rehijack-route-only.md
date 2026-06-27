# Rehijack 路由-only 重设计 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 Rehijack 的真 apply 改成「路由-only 重落实」——重探网关 + 拆旧路由 + 在**存活的 TUN 设备**上装新路由,绝不删设备(修复旧设计 `(*teardown)()` 删 `bx0` → 每次必回滚 + 漏 IP)。

**Architecture:** Linux 把 `netConf` 步骤构建器拆成 device vs route(`upSteps`/`downSteps` 由它们组合,行为不变),新增 `routeUp()`/`routeDown()` + `linuxPlatform.RehijackRoutes`;`platform` 接口加 `RehijackRoutes`;darwin 加 compile-only 实现(其 teardown 本就路由-only)。`liveMutator` 去掉 `teardown *func()`,apply 改调 `plat.RehijackRoutes`;`run.go` 回退惰性指针捕获。fix-forward 覆盖 `f0317b4`/`ae485c6` 的错误 apply。

**Tech Stack:** Go 1.26.3;改 `internal/supervisor`(`run.go`/`platform_linux.go`/`platform_darwin.go`/`mutator.go` + 两测试文件)。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3。TDD;提交信息中文 conventional + 结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`;master 直接提交。
- **A2 无副作用契约**:`Rehijack()` 方法体只构造并返回闭包,真改动在返回的 `apply` 内。
- **必须用 `*liveMutator`(指针)**:真 `Rehijack` 是指针接收者;值会退化成嵌入的 nop。
- **设备不可碰**:Rehijack 路径绝不产 `addr add` / `link set up` / `link del`——只动 `ip rule` / `table 100` 路由。
- **`upSteps()`/`downSteps()` 复合行为不变**:重构后 `upSteps`=device+route、`downSteps`=route+`link del`,与旧版**步骤集合一致**(`link del` 由中段移到末尾,删除操作幂等、次序无关;既有 `platform_linux_test.go` 用 `stepSet` 集合断言,不依赖顺序)。
- **平台测试是 `//go:build linux`**:`netConf`/step-builder 测试只在 Linux(CI)跑;Mac 上 `go test` 跳过它们,实现者用 `GOOS=linux go vet` типе检查(可选 Colima 本地跑,见 `docs/integration-testing.md`)。`liveMutator` 测试(`mutator_test.go`,无 tag)在 Mac 跑。
- router 模式 rehijack 暂不支持(返回明确错误,YAGNI)。

---

### Task 1: 平台层 RehijackRoutes(Linux 步骤拆分 + 两平台实现 + 接口 + Linux 测试)

纯**新增**:加 `platform.RehijackRoutes` + 两平台实现 + Linux netConf 步骤拆分 + Linux 步骤测试。**不动** `mutator.go`/`run.go` 的 mutator 接线(旧 flawed liveMutator 暂留,仍自洽编译)。

**Files:**
- Modify: `internal/supervisor/run.go`(`platform` 接口加 `RehijackRoutes` 方法声明,约 line 74-84 的 interface 块内)
- Modify: `internal/supervisor/platform_linux.go`(拆 `upSteps`/`downSteps`、加 `routeUpSteps`/`routeDownSteps`/`deviceUpSteps`/`routeUp`/`routeDown` + `linuxPlatform.RehijackRoutes`)
- Modify: `internal/supervisor/platform_darwin.go`(加 `darwinPlatform.RehijackRoutes`)
- Modify: `internal/supervisor/platform_linux_test.go`(加两个步骤测试)

**Interfaces:**
- Consumes: 现有 `netConf`、`tunHandle`、`route.DefaultPrivateCIDRs`/`DefaultPrivateV6CIDRs`、`defaultRoute`/`defaultRouteDarwin`、`runIP`/`runIPQuiet`/`runCmd`/`runCmdQuiet`、`itoa`/`fmtMark`/`stepSet`(测试辅助)。
- Produces:
  - `platform` 接口方法 `RehijackRoutes(tun tunHandle, serverBypass, userBypass []string) error`(Task 2 的 `rehijacker` 与 `run.go` 接线依赖)。
  - `netConf` 方法 `routeUpSteps()`/`routeDownSteps()`/`deviceUpSteps()`/`routeUp()`/`routeDown()`。

- [ ] **Step 1: 写失败的 Linux 步骤测试**

在 `internal/supervisor/platform_linux_test.go` 末尾追加(文件已是 `//go:build linux`,已 import `strings`/`route`/`testing`;若缺 `route` 别名以现有 import 为准):

```go
func TestRouteStepsExcludeDeviceSteps(t *testing.T) {
	nc := &netConf{
		tunName: "bx0", tunAddr: "198.51.100.1/30", gw: "192.168.1.1", gwDev: "eth0",
		bypass: []string{"1.2.3.4/32"}, mainLookup: route.DefaultPrivateCIDRs,
	}
	for _, s := range nc.routeUpSteps() {
		j := strings.Join(s, " ")
		if strings.HasPrefix(j, "addr add") || strings.HasPrefix(j, "link set") || strings.HasPrefix(j, "link del") {
			t.Errorf("routeUpSteps 不应含设备步骤: %q", j)
		}
	}
	for _, s := range nc.routeDownSteps() {
		if strings.Join(s, " ") == "link del bx0" {
			t.Error("routeDownSteps 不应含 link del")
		}
	}
	if !stepSet(nc.routeUpSteps())["route add default dev bx0 table "+itoa(routeTable)] {
		t.Error("routeUpSteps 缺 default dev bx0")
	}
	if !stepSet(nc.routeUpSteps())["rule add pref 200 table "+itoa(routeTable)] {
		t.Error("routeUpSteps 缺 pref 200")
	}
}

func TestUpDownStepsStillCompose(t *testing.T) {
	nc := &netConf{
		tunName: "bx0", tunAddr: "198.51.100.1/30", gw: "192.168.1.1", gwDev: "eth0",
		mainLookup: route.DefaultPrivateCIDRs,
	}
	up := nc.upSteps()
	if strings.Join(up[0], " ") != "addr add 198.51.100.1/30 dev bx0" || strings.Join(up[1], " ") != "link set bx0 up" {
		t.Fatalf("upSteps 前两步应为设备步骤, got %v / %v", up[0], up[1])
	}
	if len(up) != len(nc.deviceUpSteps())+len(nc.routeUpSteps()) {
		t.Error("upSteps 应 = deviceUpSteps + routeUpSteps 步数之和")
	}
	if !stepSet(nc.downSteps())["link del bx0"] {
		t.Error("downSteps 应含 link del bx0")
	}
	if stepSet(nc.routeDownSteps())["link del bx0"] {
		t.Error("routeDownSteps 不应含 link del bx0")
	}
}
```

- [ ] **Step 2: 跑红(Linux 编译失败)**

Run: `GOOS=linux go vet ./internal/supervisor/`
Expected: 编译失败(`nc.routeUpSteps undefined` / `nc.routeDownSteps undefined` / `nc.deviceUpSteps undefined`)。
(Mac 上 `go test ./internal/supervisor/` 不跑这些 linux-tagged 测试;以 `GOOS=linux go vet` 判编译红。)

- [ ] **Step 3: 拆分 netConf 步骤构建器(platform_linux.go)**

把现有 `upSteps()`(当前约 line 137-176)整体替换为下面四个函数(`deviceUpSteps`/`routeUpSteps`/`upSteps`),并把现有 `downSteps()`(约 line 188-219)替换为 `routeDownSteps`/`downSteps`:

```go
// deviceUpSteps:建链路的设备步骤(配地址 + 置 up)。仅 Hijack 首次建链路做;
// Rehijack 路由重落实不碰设备。
func (n *netConf) deviceUpSteps() [][]string {
	return [][]string{
		{"addr", "add", n.tunAddr, "dev", n.tunName},
		{"link", "set", n.tunName, "up"},
	}
}

// routeUpSteps:只装策略路由(bypass / default dev tun / fwmark / 私网 carve / 全量 / v6 阻断)。
func (n *netConf) routeUpSteps() [][]string {
	steps := [][]string{}
	for _, b := range n.bypass {
		steps = append(steps, []string{"route", "add", b, "via", n.gw, "dev", n.gwDev, "table", itoa(routeTable)})
	}
	steps = append(steps,
		[]string{"route", "add", "default", "dev", n.tunName, "table", itoa(routeTable)},
		[]string{"rule", "add", "pref", "100", "fwmark", fmtMark(fwMark), "table", "main"},
	)
	for _, c := range n.mainLookup {
		if c == cgnatV4CIDR {
			steps = append(steps, []string{"rule", "add", "to", c, "pref", "149", "table", itoa(tailscaleTable)})
		}
		steps = append(steps, []string{"rule", "add", "to", c, "pref", "150", "table", "main"})
	}
	steps = append(steps, []string{"rule", "add", "pref", "200", "table", itoa(routeTable)})
	if n.blockV6 {
		steps = append(steps, []string{"-6", "rule", "add", "pref", "100", "fwmark", fmtMark(fwMark), "table", "main"})
		for _, c := range n.mainLookupV6 {
			steps = append(steps, []string{"-6", "rule", "add", "to", c, "pref", "150", "table", "main"})
		}
		steps = append(steps,
			[]string{"-6", "route", "add", "unreachable", "default", "table", itoa(routeTable)},
			[]string{"-6", "rule", "add", "pref", "200", "table", itoa(routeTable)},
		)
	}
	return steps
}

// upSteps = 设备步骤 + 路由步骤(行为同旧)。
func (n *netConf) upSteps() [][]string {
	return append(n.deviceUpSteps(), n.routeUpSteps()...)
}

// routeDownSteps:只拆策略路由(与 routeUpSteps 对称);不删设备。
func (n *netConf) routeDownSteps() [][]string {
	steps := [][]string{
		{"rule", "del", "pref", "200", "table", itoa(routeTable)},
	}
	for _, c := range n.mainLookup {
		if c == cgnatV4CIDR {
			steps = append(steps, []string{"rule", "del", "to", c, "pref", "149", "table", itoa(tailscaleTable)})
		}
		steps = append(steps, []string{"rule", "del", "to", c, "pref", "150", "table", "main"})
	}
	steps = append(steps,
		[]string{"rule", "del", "pref", "100", "fwmark", fmtMark(fwMark), "table", "main"},
		[]string{"route", "flush", "table", itoa(routeTable)},
	)
	if n.blockV6 {
		v6 := [][]string{
			{"-6", "rule", "del", "pref", "200", "table", itoa(routeTable)},
		}
		for _, c := range n.mainLookupV6 {
			v6 = append(v6, []string{"-6", "rule", "del", "to", c, "pref", "150", "table", "main"})
		}
		v6 = append(v6,
			[]string{"-6", "rule", "del", "pref", "100", "fwmark", fmtMark(fwMark), "table", "main"},
			[]string{"-6", "route", "flush", "table", itoa(routeTable)},
		)
		steps = append(steps, v6...)
	}
	return steps
}

// downSteps = 路由拆除 + 删设备(link del 末尾;与旧版步骤集合一致)。
func (n *netConf) downSteps() [][]string {
	return append(n.routeDownSteps(), []string{"link", "del", n.tunName})
}

// routeUp 只装路由(在存活设备上),任一步失败即返错。
func (n *netConf) routeUp() error {
	for _, s := range n.routeUpSteps() {
		if err := runIP(s...); err != nil {
			return err
		}
	}
	return nil
}

// routeDown 尽力拆路由(忽略单步错误),不碰设备。
func (n *netConf) routeDown() {
	for _, s := range n.routeDownSteps() {
		_ = runIPQuiet(s...)
	}
}
```

> 现有 `up()`/`down()` 方法体不变(仍 `range n.upSteps()` / `range n.downSteps()`),自动复用新组合。

- [ ] **Step 4: 加 linuxPlatform.RehijackRoutes(platform_linux.go)**

在 `Hijack` 方法之后追加:

```go
// RehijackRoutes 在存活 TUN 设备上重落实劫持「路由」:重探网关 → 拆旧路由 → 装新路由。
// 绝不删设备(故 bx0 始终在,快照网可兜底还原,不漏 IP)。外部事件(DHCP/NM/清规则)
// 破坏路由时由 commit-confirmed 的 Rehijack mutation 调用。
func (p linuxPlatform) RehijackRoutes(t tunHandle, serverBypass, userBypass []string) error {
	if t.RouterMode {
		return fmt.Errorf("router 模式暂不支持 rehijack")
	}
	gw, gwDev, err := defaultRoute() // 重探:网关常是「为何要 rehijack」的根源
	if err != nil {
		return fmt.Errorf("探测默认网关: %w", err)
	}
	bypass := append(append([]string{}, serverBypass...), userBypass...)
	nc := &netConf{
		tunName: t.Name, tunAddr: t.Addr,
		gw: gw, gwDev: gwDev, bypass: bypass,
		mainLookup: route.DefaultPrivateCIDRs,
	}
	if ipv6Enabled() {
		nc.blockV6 = true
		nc.mainLookupV6 = append(append([]string{}, route.DefaultPrivateV6CIDRs...), onLinkV6Prefixes()...)
	}
	nc.routeDown() // 清旧路由(幂等容错,保住设备)
	if err := nc.routeUp(); err != nil {
		return err // 引擎据此 Rollback(经 9a 快照网);设备在 → 快照可重建,无泄漏
	}
	log.Printf("rehijack:路由已在 %s 重落实 via %s dev %s", t.Name, gw, gwDev)
	return nil
}
```

- [ ] **Step 5: 加 darwinPlatform.RehijackRoutes(platform_darwin.go,compile-only)**

在 `Hijack` 方法之后追加(darwin teardown 本就路由-only;此为接口对齐,未真机验):

```go
// RehijackRoutes 路由-only 重落实(darwin):重探网关 + 幂等配地址 + 重装路由。
// darwin 的 Hijack teardown 本就只删路由(设备归 Run 的 closeTUN),故无删设备风险。
// 未真机验证(compile-only),与 Hijack 的真机待办一并验。
func (darwinPlatform) RehijackRoutes(t tunHandle, serverBypass, userBypass []string) error {
	gw, _, err := defaultRouteDarwin()
	if err != nil {
		return fmt.Errorf("探测默认网关: %w", err)
	}
	ip := t.Addr
	if i := strings.IndexByte(ip, '/'); i >= 0 {
		ip = ip[:i]
	}
	_ = runCmdQuiet("ifconfig", t.Name, "inet", ip, ip, "up") // 幂等
	specs := darwinRouteSpecs(t.Name, gw, darwinDirectCIDRs, serverBypass, userBypass, ipv6EnabledDarwin())
	for _, s := range specs {
		_ = runCmdQuiet("route", s.del...) // 尽力清旧
		if err := runCmd("route", s.add...); err != nil {
			return fmt.Errorf("route %s: %w", strings.Join(s.add, " "), err)
		}
	}
	return nil
}
```

- [ ] **Step 6: 加 platform 接口方法(run.go)**

在 `internal/supervisor/run.go` 的 `platform interface` 块(`Hijack(...)` 声明附近)加一行:

```go
	// RehijackRoutes 在存活 TUN 设备上重落实劫持「路由」(重探网关 + 拆旧路由 + 装新路由),
	// 绝不删设备。供 commit-confirmed 的 Rehijack mutation 用。
	RehijackRoutes(tun tunHandle, serverBypass, userBypass []string) error
```

- [ ] **Step 7: 跑绿 + 全量**

Run:
```bash
GOOS=linux go vet ./internal/supervisor/                       # linux 步骤测试编译过
go build ./... && go vet ./... && go test ./...                # Mac:既有套件绿(旧 flawed mutator 暂留,仍编译)
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...           # darwin 编译(含 RehijackRoutes)
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: 全部干净。新 Linux 步骤测试在 CI 跑(Mac 仅类型检查);Mac 套件不回归。
注:本任务**不动** mutator 接线,故 Mac `go test` 仍是旧 flawed liveMutator 的测试(暂绿);Task 2 才替换。

- [ ] **Step 8: 提交**

```bash
git add internal/supervisor/run.go internal/supervisor/platform_linux.go internal/supervisor/platform_darwin.go internal/supervisor/platform_linux_test.go
git commit -m "feat(supervisor): platform.RehijackRoutes 路由-only 重落实(保设备)

Linux 拆 netConf 步骤构建器 device vs route(upSteps/downSteps 组合行为不变),
加 routeUp/routeDown + linuxPlatform.RehijackRoutes(重探网关+拆装路由,绝不删 bx0)。
darwin 加 compile-only 实现(其 teardown 本就路由-only)。platform 接口加 RehijackRoutes。
纯新增,不动 mutator 接线。

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: liveMutator 路由-only + run.go 回退接线 + Mac 测试

把 `liveMutator` 改成调 `plat.RehijackRoutes`(去掉 `teardown *func()`),重写 Mac 单测,`run.go` 回退惰性指针捕获。这一步把生产从旧 flawed apply 切到正确的路由-only apply。

**Files:**
- Modify: `internal/supervisor/mutator.go`(`rehijacker` 接口改 `RehijackRoutes`;`liveMutator` 去 `teardown` 字段;`Rehijack` 改实现)
- Modify: `internal/supervisor/mutator_test.go`(`fakePlatform` 改实现 `RehijackRoutes`;重写 liveMutator 测试)
- Modify: `internal/supervisor/run.go`(回退惰性捕获:去 `var teardown func()`/lazy 注释,`mut` 去 teardown 字段,`teardown, err :=` + `defer teardown()`)

**Interfaces:**
- Consumes: Task 1 的 `platform.RehijackRoutes(tun tunHandle, serverBypass, userBypass []string) error`(`plat` 满足)。
- Produces: 生产控制面 `/v0/rehijack` 执行路由-only 重落实(设备存活,可 commit)。

- [ ] **Step 1: 写失败测试(改 mutator_test.go)**

把 `internal/supervisor/mutator_test.go` 中现有的 `fakePlatform` + `TestLiveMutatorRehijack` + `TestLiveMutatorRehijackHijackError` + `TestLiveMutatorSetTransportNop`(Task-1-slice 旧版本)整体替换为:

```go
type fakePlatform struct {
	rehijackCalls int
	gotTun        tunHandle
	gotServer     []string
	gotUser       []string
	rehijackErr   error
}

func (f *fakePlatform) RehijackRoutes(t tunHandle, serverBypass, userBypass []string) error {
	f.rehijackCalls++
	f.gotTun = t
	f.gotServer = serverBypass
	f.gotUser = userBypass
	return f.rehijackErr
}

func TestLiveMutatorRehijack(t *testing.T) {
	fp := &fakePlatform{}
	m := &liveMutator{plat: fp, tunH: tunHandle{Name: "bx0"},
		serverBypass: []string{"1.1.1.1/32"}, userBypass: []string{"2.2.2.2/32"}}

	apply, undo, err := m.Rehijack()
	if err != nil {
		t.Fatalf("Rehijack err: %v", err)
	}
	if fp.rehijackCalls != 0 {
		t.Fatalf("方法体应无副作用: rehijackCalls=%d", fp.rehijackCalls)
	}
	if err := apply(); err != nil {
		t.Fatalf("apply err: %v", err)
	}
	if fp.rehijackCalls != 1 {
		t.Fatalf("apply 应调 RehijackRoutes 一次, got %d", fp.rehijackCalls)
	}
	if fp.gotTun.Name != "bx0" || len(fp.gotServer) != 1 || fp.gotServer[0] != "1.1.1.1/32" ||
		len(fp.gotUser) != 1 || fp.gotUser[0] != "2.2.2.2/32" {
		t.Fatalf("apply 传参不对: tun=%v server=%v user=%v", fp.gotTun, fp.gotServer, fp.gotUser)
	}
	if err := undo(); err != nil {
		t.Fatalf("undo 应 nil: %v", err)
	}
}

func TestLiveMutatorRehijackError(t *testing.T) {
	wantErr := errors.New("boom")
	fp := &fakePlatform{rehijackErr: wantErr}
	m := &liveMutator{plat: fp}
	apply, _, _ := m.Rehijack()
	if err := apply(); !errors.Is(err, wantErr) {
		t.Fatalf("apply 应透传 RehijackRoutes 错误, got %v", err)
	}
}

func TestLiveMutatorSetTransportNop(t *testing.T) {
	m := &liveMutator{}
	apply, undo, err := m.SetTransport("vless://x@h:443")
	if err != nil {
		t.Fatalf("SetTransport err: %v", err)
	}
	if apply() != nil || undo() != nil {
		t.Fatal("liveMutator.SetTransport 应为 nop(经嵌入 nopMutator)")
	}
}
```
(`mutator_test.go` import 保持 `import ("errors"; "testing")`。)

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run 'LiveMutator' -v`
Expected: 编译失败(`fakePlatform` 现实现 `RehijackRoutes` 但 `liveMutator.plat` 仍是旧 `rehijacker{Hijack}` → 类型不匹配;且 `liveMutator{...}` 字面量无 `teardown` 但结构体仍有该字段等)。

- [ ] **Step 3: 改 mutator.go**

把 `internal/supervisor/mutator.go` 现有的 `rehijacker` 接口(约 line 24-29)、`liveMutator` 结构体(约 line 31-43)、`Rehijack` 方法(约 line 45-60)整体替换为:

```go
// rehijacker 是 liveMutator 对 platform 的窄依赖(只需路由-only 重落实)。
// platform 接口的方法集 ⊇ rehijacker,故 run.go 的 plat 可直接赋值;
// 单测的 fakePlatform 也只需实现这一个方法。
type rehijacker interface {
	RehijackRoutes(tun tunHandle, serverBypass, userBypass []string) error
}

// liveMutator:生产 mutator。真 Rehijack apply = 路由-only 重落实(保 TUN 设备);
// SetTransport 仍 nop(嵌 nopMutator,下一刀替换)。
// 注意:真 Rehijack 是指针接收者方法,必须以 &liveMutator{} 使用,
// 否则值方法集只含嵌入的 nop Rehijack,会静默退化成 nop。
type liveMutator struct {
	nopMutator   // 提供 nop SetTransport(方法提升)
	plat         rehijacker
	tunH         tunHandle
	serverBypass []string
	userBypass   []string
}

// Rehijack 返回真 apply:在存活设备上重落实劫持路由(重探网关 + 拆旧路由 + 装新路由)。
// 方法体无副作用(A2 契约):只构造闭包。undo 为 nop —— 路由还原靠
// engine.Arm 的 snapshotter.Restore(9a 快照网);设备始终在,故快照可兜底。
func (m *liveMutator) Rehijack() (apply, undo func() error, err error) {
	apply = func() error { return m.plat.RehijackRoutes(m.tunH, m.serverBypass, m.userBypass) }
	undo = func() error { return nil }
	return apply, undo, nil
}
```

- [ ] **Step 4: 改 run.go(回退惰性捕获)**

把 `internal/supervisor/run.go` 现有块(约 line 260-289,从 `// 控制面 socket` 注释到 `defer func() { teardown() }()`)替换为:

```go
	// 控制面 socket + pidfile(取代旧 serveStats,HTTP over unix socket)
	serverBypass := addrsToCIDRs(serverAddrs)
	mut := &liveMutator{
		plat:         plat,
		tunH:         tunH,
		serverBypass: serverBypass,
		userBypass:   cfg.Bypass,
	}
	closer, err := requireControlSocket(func() (io.Closer, error) {
		return serveControl(counters, tun0, serverHost, cfg.UDP.Mode, mutEng, mut)
	})
	if err != nil {
		return err
	}
	defer closer.Close()
	defer os.Remove(SockPath)
	if err := os.WriteFile(PidPath, []byte(itoa(os.Getpid())), 0o644); err == nil {
		defer os.Remove(PidPath)
	}

	// 6) 劫持默认路由(含 bypass 保 SSH + 服务器防环)
	teardown, err := plat.Hijack(tunH, serverBypass, cfg.Bypass)
	if err != nil {
		return fmt.Errorf("配置路由: %w", err)
	}
	defer teardown()
```

- [ ] **Step 5: 跑绿 + 全量 + 跨平台**

Run:
```bash
go test ./internal/supervisor/ -run 'LiveMutator|NopMutator' -v
go build ./... && go vet ./... && go test ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
GOOS=linux go vet ./internal/supervisor/
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: 3 个 LiveMutator + NopMutator PASS;全套件绿;两平台编译过;integration vet 干净。

- [ ] **Step 6: 提交**

```bash
git add internal/supervisor/mutator.go internal/supervisor/mutator_test.go internal/supervisor/run.go
git commit -m "feat(supervisor): liveMutator 路由-only Rehijack + run.go 回退惰性捕获

apply 改调 plat.RehijackRoutes(保设备),去掉 teardown 指针;run.go 回退为
teardown, err := plat.Hijack + defer teardown()。生产 /v0/rehijack 现执行
路由-only 重落实(设备存活、可 commit),修复旧设计删 bx0 必回滚 + 漏 IP。
SetTransport 仍 nop。真 apply 行为由 B 阶段真机回归验证(须验证能 commit)。

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- Linux netConf 步骤拆分(device vs route、upSteps/downSteps 组合不变)→ Task 1 Step 3。
- `routeUp`/`routeDown` + `linuxPlatform.RehijackRoutes`(重探网关 + 路由拆装、不删设备、router 模式报错)→ Task 1 Step 3-4。
- darwin compile-only `RehijackRoutes` → Task 1 Step 5。
- `platform` 接口加 `RehijackRoutes` → Task 1 Step 6。
- `liveMutator` 去 teardown 指针 + apply 调 RehijackRoutes + A2 无副作用 + undo nop → Task 2 Step 3。
- `run.go` 回退惰性捕获、生产挂 `&liveMutator` → Task 2 Step 4。
- Mac 测试(fakePlatform.RehijackRoutes、方法体无副作用、传参、错误透传、SetTransport nop)→ Task 2 Step 1。
- Linux 步骤测试(routeUp/Down 排除设备步骤、upSteps/downSteps 组合)→ Task 1 Step 1。
- 真机回归须验 commit-reachable → spec 测试策略段(B 阶段,非本 plan 任务)。

**占位扫描:** 无 TBD;每步完整代码/命令。Task 1/2 引用 run.go/platform_linux.go 行号是锚点核对(按内容定位,行号或微移)。

**类型一致性:** `RehijackRoutes(tun tunHandle, serverBypass, userBypass []string) error` 在 platform 接口(Task1 S6)、linuxPlatform(S4)、darwinPlatform(S5)、`rehijacker`(Task2 S3)、`fakePlatform`(Task2 S1)五处签名一致;`liveMutator{plat, tunH, serverBypass, userBypass}` 字段(Task2 S3 结构体 / S1 测试 / S4 run.go 接线)一致,无 teardown 字段;`netConf` 新方法 `routeUpSteps/routeDownSteps/deviceUpSteps/routeUp/routeDown` 在 Task1 S3 定义、S1 测试与 S4 RehijackRoutes 引用一致;`&liveMutator` 指针(S1 测试 + S4 run.go)。
