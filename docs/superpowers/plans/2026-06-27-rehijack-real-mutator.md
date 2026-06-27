# Rehijack 真 mutator(真 apply 接入)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 `mutator.Rehijack()` 的 apply 做真(真 teardown 当前劫持 + `plat.Hijack` 重装 + 收养新 teardown),生产 mutator 从 `nopMutator{}` 换成 `liveMutator`;`SetTransport` 仍 nop(下一刀)。

**Architecture:** 新 `liveMutator` 嵌 `nopMutator`(白拿 nop `SetTransport`)并 override 真 `Rehijack`;依赖窄接口 `rehijacker`(只 `Hijack`)而非整个 `platform`,使单测免 gVisor 依赖。`run.go` 用「惰性指针捕获」把控制面 socket(劫持前就起)所需的 teardown 以 `*func()` 传给 mutator,`plat.Hijack` 处赋值,`defer` 改为读当前值。undo=nop,路由还原靠 `engine.Arm` 已有的 `snapshotter.Restore`(9a 快照网,2026-06-27 真机验证无损)。

**Tech Stack:** Go 1.26.3;改 `internal/supervisor`(`mutator.go`/`run.go`/`mutator_test.go`)。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3。TDD,纯逻辑免 root,Mac 原生跑。
- 提交信息中文 conventional + 结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`。在 master 直接提交。
- **A2 无副作用契约**:`Rehijack()` 方法体只构造并返回闭包,**不**在体内调 `plat.Hijack` 或任何真实改动(`engine.Arm` 已 armed 时直接返回 `ErrAlreadyArmed` 不运行 apply,体内改动会绕过快照/undo)。
- **必须用 `*liveMutator`(指针)**:`liveMutator` 嵌 `nopMutator`,真 `Rehijack` 是指针接收者方法;值 `liveMutator` 的方法集只含提升来的 nop `Rehijack`,会静默退化成 nop。run.go 与测试一律 `&liveMutator{...}`。
- `SetTransport` 本刀保持 nop(经嵌入的 `nopMutator`)。生产仍对该路径 brick-safe。
- 不重排 `run.go` 的 serveControl/Hijack 顺序(保「控制面先于劫持就绪」)。

---

### Task 1: `liveMutator` 类型 + 真 Rehijack apply + fakePlatform 单测

本任务只产出类型与单测,**不动 run.go**(生产仍挂 `nopMutator{}`)。故可独立测、不改生产行为。

**Files:**
- Modify: `internal/supervisor/mutator.go`(加 `rehijacker` 接口 + `liveMutator` 类型 + `Rehijack` 方法)
- Modify: `internal/supervisor/mutator_test.go`(加 `fakePlatform` + 3 个 `liveMutator` 测试)

**Interfaces:**
- Consumes: 现有 `tunHandle`(`run.go:61` 的 struct)、`nopMutator`(`mutator.go`)、`mutator` 接口(`mutator.go`)。
- Produces:
  - `type rehijacker interface { Hijack(tun tunHandle, serverBypass, userBypass []string) (func(), error) }`
  - `type liveMutator struct { nopMutator; plat rehijacker; tunH tunHandle; serverBypass []string; userBypass []string; teardown *func() }`
  - `func (m *liveMutator) Rehijack() (apply, undo func() error, err error)`
  - Task 2 依赖:`&liveMutator{plat, tunH, serverBypass, userBypass, teardown}` 满足 `mutator` 接口。

- [ ] **Step 1: 写失败测试**

把以下追加进 `internal/supervisor/mutator_test.go`(文件顶部 import 改为 `import ("errors"; "testing")`):

```go
// fakePlatform 只实现 liveMutator 依赖的窄接口 rehijacker(免 gVisor 依赖)。
type fakePlatform struct {
	hijackCalls int
	hijackErr   error
	newTD       func() // Hijack 成功时返回的新 teardown
}

func (f *fakePlatform) Hijack(tunHandle, []string, []string) (func(), error) {
	f.hijackCalls++
	if f.hijackErr != nil {
		return nil, f.hijackErr
	}
	return f.newTD, nil
}

func TestLiveMutatorRehijack(t *testing.T) {
	var oldCalls, newCalls int
	oldTD := func() { oldCalls++ }
	newTD := func() { newCalls++ }
	teardown := oldTD
	fp := &fakePlatform{newTD: newTD}
	m := &liveMutator{plat: fp, teardown: &teardown}

	apply, undo, err := m.Rehijack()
	if err != nil {
		t.Fatalf("Rehijack err: %v", err)
	}
	// A2 无副作用契约:方法体不得触发任何改动
	if fp.hijackCalls != 0 || oldCalls != 0 {
		t.Fatalf("方法体应无副作用: hijackCalls=%d oldCalls=%d", fp.hijackCalls, oldCalls)
	}

	if err := apply(); err != nil {
		t.Fatalf("apply err: %v", err)
	}
	if oldCalls != 1 {
		t.Fatalf("apply 应拆旧劫持一次, got %d", oldCalls)
	}
	if fp.hijackCalls != 1 {
		t.Fatalf("apply 应调 plat.Hijack 一次, got %d", fp.hijackCalls)
	}
	// 收养:此刻 *teardown 应已是 newTD —— 调一次看新计数器涨、旧的不涨
	(*m.teardown)()
	if newCalls != 1 || oldCalls != 1 {
		t.Fatalf("apply 后应收养新 teardown: newCalls=%d oldCalls=%d", newCalls, oldCalls)
	}

	if err := undo(); err != nil {
		t.Fatalf("undo 应 nil: %v", err)
	}
}

func TestLiveMutatorRehijackHijackError(t *testing.T) {
	var oldCalls int
	oldTD := func() { oldCalls++ }
	teardown := oldTD
	wantErr := errors.New("boom")
	fp := &fakePlatform{hijackErr: wantErr}
	m := &liveMutator{plat: fp, teardown: &teardown}

	apply, _, _ := m.Rehijack()
	if err := apply(); !errors.Is(err, wantErr) {
		t.Fatalf("apply 应透传 Hijack 错误, got %v", err)
	}
	// 失败不覆盖 teardown:仍指向旧值(交快照网接管)。
	// apply 内已调旧 teardown 一次(oldCalls=1);此处再手调一次应到 2,证明仍是旧的。
	(*m.teardown)()
	if oldCalls != 2 {
		t.Fatalf("Hijack 失败应保持旧 teardown 不被覆盖: oldCalls=%d", oldCalls)
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

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run 'LiveMutator' -v`
Expected: 编译失败(`undefined: liveMutator` / `undefined: rehijacker`)。

- [ ] **Step 3: 写实现**

把以下追加进 `internal/supervisor/mutator.go`(`nopMutator` 之后):

```go
// rehijacker 是 liveMutator 对 platform 的窄依赖(只需 Hijack)。
// platform 接口的方法集 ⊇ rehijacker,故 run.go 的 plat 可直接赋值;
// 单测的 fakePlatform 也只需实现这一个方法(免 gVisor 依赖)。
type rehijacker interface {
	Hijack(tun tunHandle, serverBypass, userBypass []string) (teardown func(), err error)
}

// liveMutator:生产 mutator。真 Rehijack apply;SetTransport 仍 nop(嵌 nopMutator,下一刀替换)。
// teardown 指向 run.go 的劫持 teardown 变量(惰性捕获):构造时该变量尚未赋值,
// apply 仅在 commit 路径运行,那时 plat.Hijack 已赋值,指针读到有效值。
// 注意:真 Rehijack 是指针接收者方法,必须以 &liveMutator{} 使用,
// 否则值方法集只含嵌入的 nop Rehijack,会静默退化成 nop。
type liveMutator struct {
	nopMutator   // 提供 nop SetTransport(方法提升)
	plat         rehijacker
	tunH         tunHandle
	serverBypass []string
	userBypass   []string
	teardown     *func()
}

// Rehijack 返回真 apply:拆当前劫持 + 重装劫持 + 收养新 teardown。
// 方法体无副作用(A2 契约):只构造闭包。undo 为 nop —— 路由还原靠
// engine.Arm 的 snapshotter.Restore(9a 快照网)。
func (m *liveMutator) Rehijack() (apply, undo func() error, err error) {
	apply = func() error {
		(*m.teardown)() // 拆当前劫持
		td, err := m.plat.Hijack(m.tunH, m.serverBypass, m.userBypass)
		if err != nil {
			return err // 引擎据此 Rollback(经快照还原);保持旧 teardown 不覆盖
		}
		*m.teardown = td // 收养新 teardown
		return nil
	}
	undo = func() error { return nil }
	return apply, undo, nil
}
```

- [ ] **Step 4: 跑绿 + 全量**

Run:
```bash
go test ./internal/supervisor/ -run 'LiveMutator|NopMutator' -v
go build ./... && go vet ./... && go test ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: 3 个 LiveMutator 用例 + 既有 NopMutator PASS;全套件绿;两平台编译过;integration vet 干净。

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/mutator.go internal/supervisor/mutator_test.go
git commit -m "feat(supervisor): liveMutator 真 Rehijack apply(拆+重劫持,经窄接口 rehijacker)

undo=nop 靠 9a 快照网;SetTransport 仍 nop(嵌 nopMutator,下一刀)。
方法体无副作用(A2 契约);必须 &liveMutator 使用(指针接收者)。
本提交不改 run.go,生产仍挂 nopMutator。

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: run.go 惰性捕获接线(生产挂 liveMutator)

把生产 mutator 从 `nopMutator{}` 换成 `&liveMutator{...}`,惰性指针捕获劫持 teardown。run.go 是编排层、真 apply 需 root,故无新单测;验证靠编译/vet/既有测全绿 + 跨平台,真 apply 行为由 B 阶段真机回归验证(spec 测试策略段)。

**Files:**
- Modify: `internal/supervisor/run.go`(serveControl 前构造 mut + 提前声明 teardown/serverBypass;Hijack 处改赋值;defer 改读当前值)

**Interfaces:**
- Consumes: Task 1 的 `&liveMutator{plat, tunH, serverBypass, userBypass, teardown}`(满足 `mutator`)。
- Produces: 生产控制面 `/v0/rehijack` 现执行真 apply(commit 保留 / rollback+死手经快照还原)。

- [ ] **Step 1: 改 run.go —— 提前声明 + 构造 mut**

打开 `internal/supervisor/run.go`。当前 `serveControl` 调用在 line 261-263:
```go
	closer, err := requireControlSocket(func() (io.Closer, error) {
		return serveControl(counters, tun0, serverHost, cfg.UDP.Mode, mutEng, nopMutator{})
	})
```
当前 Hijack 段在 line 273-279:
```go
	// 6) 劫持默认路由(含 bypass 保 SSH + 服务器防环)
	serverBypass := addrsToCIDRs(serverAddrs)
	teardown, err := plat.Hijack(tunH, serverBypass, cfg.Bypass)
	if err != nil {
		return fmt.Errorf("配置路由: %w", err)
	}
	defer teardown()
```

**改动 a**:在 `serveControl` 调用**之前**(line 260 的 `// 控制面 socket ...` 注释之前)插入:
```go
	// liveMutator 需在控制面起前构造(socket 先于劫持就绪),但劫持 teardown 此刻尚未诞生:
	// 惰性指针捕获 —— 提前声明 teardown,plat.Hijack 处赋值,apply 仅在 commit 时读(那时已赋值)。
	serverBypass := addrsToCIDRs(serverAddrs)
	var teardown func()
	mut := &liveMutator{
		plat:         plat,
		tunH:         tunH,
		serverBypass: serverBypass,
		userBypass:   cfg.Bypass,
		teardown:     &teardown,
	}
```

**改动 b**:`serveControl` 末参 `nopMutator{}` → `mut`:
```go
		return serveControl(counters, tun0, serverHost, cfg.UDP.Mode, mutEng, mut)
```

**改动 c**:Hijack 段删掉重复的 `serverBypass :=`、改 `:=` 为 `=`(复用已声明的 teardown/serverBypass):
```go
	// 6) 劫持默认路由(含 bypass 保 SSH + 服务器防环)
	teardown, err = plat.Hijack(tunH, serverBypass, cfg.Bypass)
	if err != nil {
		return fmt.Errorf("配置路由: %w", err)
	}
	defer func() { teardown() }() // 读当前值:apply 可能已换新 teardown
```

> 说明:`defer func(){ teardown() }()` 仍在 `if err != nil { return }` 之后注册,故 Hijack 失败(teardown 仍 nil)时提前 return、不会注册该 defer,无 nil 调用风险(与原行为一致)。

- [ ] **Step 2: 编译 + vet + 全量测试**

Run:
```bash
go build ./... && go vet ./...
go test ./internal/supervisor/ ./internal/mcp/ ./internal/cli/ ./...
```
Expected: 编译过;`internal/supervisor` 既有控制面/引擎/mutator 测全绿(`serveControl` 现收 `*liveMutator`,接口满足);无回归。

- [ ] **Step 3: 跨平台编译 + integration vet**

Run:
```bash
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: 两平台编译过;integration vet 干净。(`liveMutator` 平台无关:`rehijacker` 由各平台 `plat` 满足。)

- [ ] **Step 4: 提交**

```bash
git add internal/supervisor/run.go
git commit -m "feat(supervisor): run.go 挂 liveMutator + 惰性指针捕获劫持 teardown

生产 mutator nopMutator{} → &liveMutator;teardown 提前声明、Hijack 处赋值、
defer 改读当前值(apply 可换新)。/v0/rehijack 现执行真 teardown+重劫持。
SetTransport 仍 nop。真 apply 行为由 B 阶段真机回归验证。

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- `liveMutator` 嵌 nopMutator + 真 Rehijack(apply: `(*teardown)()` → `plat.Hijack` → 收养)→ Task 1 Step 3。
- 方法体无副作用契约 → Task 1 测试 Step 1(`hijackCalls==0 && oldCalls==0`)。
- undo=nop 靠快照网 → Task 1 Step 3 + 测试 undo nil。
- Hijack 失败透传 + 不覆盖旧 teardown → Task 1 `TestLiveMutatorRehijackHijackError`。
- SetTransport 仍 nop(嵌入)→ Task 1 `TestLiveMutatorSetTransportNop`。
- run.go 惰性指针捕获(方案 A:提前声明 + `&teardown` 注入 + defer 读当前值)+ 不重排顺序 → Task 2 Step 1。
- 生产挂 liveMutator → Task 2 改动 b。
- Mac 原生免 root(窄接口 fakePlatform)→ Task 1 全部;真机回归留 B 阶段(spec 已述,非本 plan 任务)。

**占位扫描:** 无 TBD;每步完整代码/命令。Task 2 引用 run.go 现有行号(261-263/273-279)是锚点核对,非占位——实现时以「serveControl 调用」「Hijack 段」定位,行号可能因前序提交微移。

**类型一致性:** `rehijacker.Hijack(tunHandle, []string, []string)(func(), error)` 与 `platform.Hijack`(run.go:83)签名一致;`liveMutator.teardown *func()` 与 run.go `var teardown func()` + `&teardown` 一致;`&liveMutator{}` 满足 `mutator`(serveControl 末参,control.go:237)——SetTransport 由嵌入 nopMutator 提升、Rehijack 由指针方法提供;Task 1 测试与 Task 2 接线字段名(plat/tunH/serverBypass/userBypass/teardown)一致。
