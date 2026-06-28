# SetTransport 真换隧道(Slice 2b)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `liveMutator.SetTransport(link)` 真换隧道(同服务器):经 `transportSwapper` 建新隧道、等健康、原子换(`lt`+dialer+新 socks)、停旧;undo 条件化换回旧 link。commit-confirmed。

**Architecture:** `linkSwapper` 接缝(`currentLink`/`swapTo`)让 liveMutator 的 apply/undo 逻辑 Mac 可测;真 `transportSwapper` 实现做隧道操作(真机验)。run.go 构造 swapper 喂给 liveMutator,并把 Run 退出的隧道停止从「捕获的 tun0」改成「当前 lt.get()」(swap 后跟随)。改动面 = `mutator.go` + 新 `transportswap.go` + `run.go` + `mutator_test.go`(同任务落地:三者拆开会让生产 SetTransport panic 或不编译)。

**Tech Stack:** Go 1.26.3;`internal/supervisor`(改 `mutator.go`/`run.go`/`mutator_test.go`,新增 `transportswap.go`)。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3。TDD;提交中文 conventional + 结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`;master 直接提交。
- **A2 无副作用契约**:`SetTransport` 方法体只读 `currentLink` + 构造闭包,真换在返回的 apply 内。
- **硬换**:apply 换成后立即停旧隧道(既有 TCP 连接重置 = 预期);undo 仅在 `currentLink` 变了(确实换过)时才换回旧 link。
- **必须 `&liveMutator{}`**(两方法均指针接收者)。
- **健康门**:新隧道未在 `healthTimeout` 内健康 → 停新、返错、**不换**(旧隧道无损);跨服务器误传 link 因无 bypass 不健康 → 安全 abort。
- **Run 退出停当前隧道**:`defer` 改读 `lt.get()`(swap 后指向新隧道);`tunnel.Stop` 幂等,双停安全。
- 真机验证(同服务器换 link / commit / rollback / 死手 / 既有连接重置)是**合并后手动步骤**,非本 plan 任务(同 Rehijack/watchdog 惯例)。

---

### Task 1: linkSwapper + transportSwapper + liveMutator SetTransport + run.go 接线

**Files:**
- Modify: `internal/supervisor/mutator.go`(加 `linkSwapper` 接口;`liveMutator` 去 `nopMutator` 嵌入、加 `swap` 字段、加真 `SetTransport`)
- Create: `internal/supervisor/transportswap.go`(`transportSwapper` 真实现 + 编译守卫)
- Modify: `internal/supervisor/run.go`(lt 上移 + defer 改当前隧道 + 构造 swapper + liveMutator 加 swap)
- Modify: `internal/supervisor/mutator_test.go`(`fakeSwapper` + 替换 SetTransport 测试)

**Interfaces:**
- Consumes: `liveTunnel`(Slice 2a)、`dialer.Dialer`/`dialer.Transport`、`tunnel.Tunnel`、`waitTunnelHealthy`/`socksProxy`、run.go 的 `buildTunnel`。
- Produces:
  - `type linkSwapper interface { currentLink() string; swapTo(link string) error }`
  - `type transportSwapper struct {…}`(实现 linkSwapper)
  - `liveMutator{plat, swap, tunH, serverBypass, userBypass}` + 真 `SetTransport`。

- [ ] **Step 1: 写失败测试(改 mutator_test.go)**

把 `internal/supervisor/mutator_test.go` 中现有的 `TestLiveMutatorSetTransportNop`(约 82-90 行,`&liveMutator{}` + 期望 nop)**整体删除**,在文件末尾追加:
```go
type fakeSwapper struct {
	cur       string
	swapCalls []string
	swapErr   error
}

func (f *fakeSwapper) currentLink() string { return f.cur }
func (f *fakeSwapper) swapTo(link string) error {
	f.swapCalls = append(f.swapCalls, link)
	if f.swapErr != nil {
		return f.swapErr
	}
	f.cur = link // 仅成功才更新当前 link
	return nil
}

func TestLiveMutatorSetTransport(t *testing.T) {
	fs := &fakeSwapper{cur: "brook://old"}
	m := &liveMutator{swap: fs}

	apply, undo, err := m.SetTransport("brook://new")
	if err != nil {
		t.Fatalf("SetTransport err: %v", err)
	}
	if len(fs.swapCalls) != 0 {
		t.Fatalf("方法体应无副作用: swapCalls=%v", fs.swapCalls)
	}
	if err := apply(); err != nil {
		t.Fatalf("apply err: %v", err)
	}
	if len(fs.swapCalls) != 1 || fs.swapCalls[0] != "brook://new" {
		t.Fatalf("apply 应 swapTo(new) 一次, got %v", fs.swapCalls)
	}
	if err := undo(); err != nil {
		t.Fatalf("undo err: %v", err)
	}
	if len(fs.swapCalls) != 2 || fs.swapCalls[1] != "brook://old" {
		t.Fatalf("换过后 undo 应 swapTo(old), got %v", fs.swapCalls)
	}
}

func TestLiveMutatorSetTransportApplyFailUndoNop(t *testing.T) {
	wantErr := errors.New("unhealthy")
	fs := &fakeSwapper{cur: "brook://old", swapErr: wantErr}
	m := &liveMutator{swap: fs}

	apply, undo, _ := m.SetTransport("brook://new")
	if err := apply(); !errors.Is(err, wantErr) {
		t.Fatalf("apply 应透传 swapTo 错误, got %v", err)
	}
	before := len(fs.swapCalls)
	if err := undo(); err != nil {
		t.Fatalf("undo 应 nil: %v", err)
	}
	if len(fs.swapCalls) != before {
		t.Fatalf("apply 未换成时 undo 应 nop, swapCalls 多了: %v", fs.swapCalls)
	}
}
```
(`errors` 已在 mutator_test.go import。`TestNopMutator`/`TestLiveMutatorRehijack`/`TestLiveMutatorRehijackError` 保留不动。)

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run 'LiveMutatorSetTransport' 2>&1 | head`
Expected: 编译失败(`liveMutator` 无 `swap` 字段 / `SetTransport` 仍来自嵌入 nopMutator 与 `swap` 用法冲突)。

- [ ] **Step 3: 改 mutator.go**

(a) 在 `rehijacker` 接口之后加 `linkSwapper`:
```go
// linkSwapper:把"换到某 link"抽象出来,使 liveMutator 的 commit-confirmed 逻辑可 fake 测;
// 真隧道操作(建/起/等健康/原子换/停旧)由 transportSwapper 实现、真机验。
type linkSwapper interface {
	currentLink() string
	swapTo(link string) error
}
```

(b) 把 `liveMutator` 结构体(去掉 `nopMutator` 嵌入,加 `swap`)与新增 `SetTransport` 替换为:
```go
// liveMutator:生产 mutator。Rehijack=路由-only 重落实(plat);SetTransport=换隧道(swap)。
// 两方法均指针接收者,必须以 &liveMutator{} 使用。
type liveMutator struct {
	plat         rehijacker
	swap         linkSwapper
	tunH         tunHandle
	serverBypass []string
	userBypass   []string
}

// SetTransport 返回真 apply:换到 newLink(建新+等健康+原子换+停旧)。
// 方法体无副作用(A2 契约):只读当前 link、构造闭包。undo 仅在确实换过时换回旧 link。
func (m *liveMutator) SetTransport(newLink string) (apply, undo func() error, err error) {
	oldLink := m.swap.currentLink()
	apply = func() error { return m.swap.swapTo(newLink) }
	undo = func() error {
		if m.swap.currentLink() == oldLink {
			return nil // apply 未换成(健康失败)→ 无需 undo
		}
		return m.swap.swapTo(oldLink)
	}
	return apply, undo, nil
}
```
保留现有 `Rehijack` 方法不变。保留 `nopMutator`/`nop()`(`TestNopMutator` 仍用)。更新 liveMutator 上方注释里"SetTransport 仍 nop"等过时描述。

- [ ] **Step 4: 写 transportswap.go**

新建 `internal/supervisor/transportswap.go`:
```go
package supervisor

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/getbx/bx/internal/dialer"
	"github.com/getbx/bx/internal/tunnel"
)

// transportSwapper 是 linkSwapper 的真实现:运行期把隧道换到某 link(同服务器)。
// build 复用 run.go 的 buildTunnel(含按需 sing-box)。硬换:换成后立即停旧隧道
//(既有 TCP 连接重置)。健康失败则停新、不换,旧隧道无损。
type transportSwapper struct {
	mu            sync.Mutex
	lt            *liveTunnel
	d             *dialer.Dialer
	build         func(link string) (*tunnel.Tunnel, error)
	healthTimeout time.Duration
	ctx           context.Context
	curLink       string
}

func (s *transportSwapper) currentLink() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.curLink
}

func (s *transportSwapper) swapTo(link string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	newTun, err := s.build(link)
	if err != nil {
		return err
	}
	newTun.Start()
	if err := waitTunnelHealthy(s.ctx, newTun, s.healthTimeout); err != nil {
		newTun.Stop() // 新隧道没起来:停掉、不换,旧隧道仍在服务
		return err
	}
	px, err := socksProxy(newTun.SocksAddr(), &net.Dialer{Timeout: 10 * time.Second})
	if err != nil {
		newTun.Stop()
		return err
	}
	old := s.lt.get()
	s.lt.set(newTun)                                                        // serveControl/refreshLoop 跟随
	s.d.SetTransport(&dialer.Transport{Proxy: px, Healthy: newTun.Healthy}) // 新连接走新隧道
	s.curLink = link
	old.Stop() // 停旧 brook(既有连接重置)
	return nil
}

var _ linkSwapper = (*transportSwapper)(nil)
```

- [ ] **Step 5: 改 run.go —— lt 上移 + defer 改当前 + 构造 swapper + liveMutator 加 swap**

(a) 隧道构建块(run.go 现 149-166):把 `lt := &liveTunnel{}; lt.set(tun0)` 从健康日志后(165-166)**上移到 Start 之前**,并把 `defer tun0.Stop()` 改为停当前隧道。当前:
```go
	tun0, err := buildTunnel(cfg.Server)
	if err != nil {
		return fmt.Errorf("构建隧道: %w", err)
	}
	tun0.Start()
	defer tun0.Stop()
	log.Printf("bx 隧道启动: socks5=%s 探测=%s", tun0.SocksAddr(), opts.Probe)
	healthTimeout := opts.HealthTimeout
	if healthTimeout <= 0 {
		healthTimeout = 20 * time.Second
	}
	if err := waitTunnelHealthy(ctx, tun0, healthTimeout); err != nil {
		return err
	}
	log.Printf("bx 隧道健康: 延迟=%dms", tun0.Stats().LatencyMS)

	lt := &liveTunnel{}
	lt.set(tun0)
```
改为(lt 上移到 err 检查后、Start 前;defer 改 `lt.get()`;删末尾旧 lt 两行):
```go
	tun0, err := buildTunnel(cfg.Server)
	if err != nil {
		return fmt.Errorf("构建隧道: %w", err)
	}
	lt := &liveTunnel{}
	lt.set(tun0)
	tun0.Start()
	defer func() { lt.get().Stop() }() // 停"当前"隧道:Slice 2b swap 后 lt 指向新隧道(Stop 幂等,双停安全)
	log.Printf("bx 隧道启动: socks5=%s 探测=%s", tun0.SocksAddr(), opts.Probe)
	healthTimeout := opts.HealthTimeout
	if healthTimeout <= 0 {
		healthTimeout = 20 * time.Second
	}
	if err := waitTunnelHealthy(ctx, tun0, healthTimeout); err != nil {
		return err
	}
	log.Printf("bx 隧道健康: 延迟=%dms", tun0.Stats().LatencyMS)
```

(b) liveMutator 构造(run.go 现约 267-272)前加 swapper、并给 liveMutator 加 `swap`:
```go
	swapper := &transportSwapper{
		lt:            lt,
		d:             d,
		build:         buildTunnel,
		healthTimeout: healthTimeout,
		ctx:           ctx,
		curLink:       cfg.Server,
	}
	mut := &liveMutator{
		plat:         plat,
		swap:         swapper,
		tunH:         tunH,
		serverBypass: serverBypass,
		userBypass:   cfg.Bypass,
	}
```

- [ ] **Step 6: 跑绿 + 全量 + 跨平台**

Run:
```bash
go test ./internal/supervisor/ -run 'LiveMutator|NopMutator' -v
go build ./... && go vet ./... && go test ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
GOOS=linux go vet ./internal/supervisor/
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: 2 个 SetTransport 测 + Rehijack/NopMutator 测全绿;全套件绿;两平台编译过。

- [ ] **Step 7: 提交**

```bash
git add internal/supervisor/mutator.go internal/supervisor/transportswap.go internal/supervisor/run.go internal/supervisor/mutator_test.go
git commit -m "feat(supervisor): SetTransport 真换隧道(Slice 2b)

liveMutator.SetTransport 经 linkSwapper 真换:transportSwapper 建新+等健康+原子换
(lt/dialer/新 socks)+停旧;undo 条件化换回旧 link。硬换(既有连接重置=预期)。
run.go 构造 swapper、Run 退出改停当前隧道(lt.get)。Mac 测 commit-confirmed 逻辑;
真换真机验。

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- `linkSwapper` 接缝 → Step 3a。
- `transportSwapper`(build/start/wait/原子换/停旧 + 健康门 + mu)→ Step 4。
- `liveMutator` 去 nopMutator + swap 字段 + 真 SetTransport(方法体无副作用、undo 条件化)→ Step 3b。
- run.go 构造 swapper + Run 退出停当前隧道(lt 上移 + defer 改)→ Step 5。
- Mac 测(side-effect-free、apply swapTo(new)、undo 条件化、错误透传)→ Step 1。
- 真机验证 → 合并后手动(Global Constraints 注明,非任务)。

**占位扫描:** 无 TBD;每步完整代码/命令。行号为锚点核对(按内容定位)。

**类型一致性:** `linkSwapper{currentLink() string; swapTo(link string) error}`(Step3a)与 `transportSwapper` 方法(Step4)、`fakeSwapper`(Step1)、liveMutator `swap` 用法(Step3b)一致;`transportSwapper{lt *liveTunnel, d *dialer.Dialer, build func(string)(*tunnel.Tunnel,error), healthTimeout time.Duration, ctx context.Context, curLink string}` 与 run.go 构造(Step5b)字段名一致;`liveMutator{plat, swap, tunH, serverBypass, userBypass}` 结构(Step3b)与 run.go 构造(Step5b)、测试构造(Step1:`&liveMutator{swap: fs}`)一致;`waitTunnelHealthy(ctx,*tunnel.Tunnel,timeout)`/`socksProxy(string,*net.Dialer)(dialer.ContextDialer,error)`/`lt.get()/set()`/`buildTunnel` 复用既有签名。
