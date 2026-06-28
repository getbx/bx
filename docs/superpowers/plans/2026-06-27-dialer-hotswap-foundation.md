# Dialer 热换传输基座(SetTransport Slice 1)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 `dialer.Dialer` 的 `Proxy`/`Healthy` 裸字段换成单个 `atomic.Pointer[Transport]` + `SetTransport(*Transport)`,`Dial` 经原子读取,`run.go` 启动设一次——**行为不变**,为 Slice 2 运行期换隧道打基座。

**Architecture:** 新 `dialer.Transport{Proxy, Healthy}` 由 `atomic.Pointer` 持有(`SetRouter` 同款热重载范式);`DialWithInitial` 顶部一次 `Load()` 取一致快照。改动面 = `dialer.go` + `dialer_test.go` + `run.go`(自包含;字段删除会断 run.go 编译,故三者同任务落地)。

**Tech Stack:** Go 1.26.3;改 `internal/dialer/dialer.go`、`internal/dialer/dialer_test.go`、`internal/supervisor/run.go`。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3。TDD;提交中文 conventional + 结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`;master 直接提交。
- **行为零变更**:生产仍一条隧道、启动 `SetTransport` 一次,无任何运行期 swap(那是 Slice 2)。
- **单原子 holder**:`Proxy`+`Healthy` 合进一个 `Transport`,一次 `atomic.Store` 换,绝不半换。
- `Transport.Healthy` 保留「可空」语义(同旧 `Healthy` 字段;kill-switch 判定 nil-safe)。
- `serveControl`/`refreshLoop` **不动**(仍直接读 `tun0`,Slice 2 处理)。
- 必须过 `go test -race ./internal/dialer/`(基座核心 = 并发 Dial 与 SetTransport 无竞争)。

---

### Task 1: Dialer transport 原子 holder + run.go 接线 + 测试

把 `Proxy`/`Healthy` 两裸字段替换为 `atomic.Pointer[Transport]` + `SetTransport`;`Dial` 路径改原子读;更新所有构造点(测试 4 处 + run.go 1 处);加 swap 测试 + race 测试。

**Files:**
- Modify: `internal/dialer/dialer.go`(`Transport` 类型 + `transport` 原子字段替换 `Proxy`/`Healthy` + `SetTransport` + `DialWithInitial` 改读 `tr`)
- Modify: `internal/dialer/dialer_test.go`(4 处构造改 `SetTransport` + 新增 2 个测试)
- Modify: `internal/supervisor/run.go`(构造 Dialer 改 `SetTransport`)

**Interfaces:**
- Consumes: 现有 `ContextDialer`、`route.Router`、`Dialer` 其余字段不变。
- Produces:
  - `type Transport struct { Proxy ContextDialer; Healthy func() bool }`
  - `func (d *Dialer) SetTransport(t *Transport)`
  - (Slice 2 依赖:运行期可 `d.SetTransport(newTransport)` 原子换。)

- [ ] **Step 1: 写新测试(失败)**

在 `internal/dialer/dialer_test.go` 末尾追加(import 已有 `sync`?若无则在 race 测试里加;现有 import 视实际为准):

```go
func TestSetTransportSwaps(t *testing.T) {
	pxA, pxB, dr := &recordDialer{}, &recordDialer{}, &recordDialer{}
	d := &Dialer{Direct: dr}
	d.SetTransport(&Transport{Proxy: pxA, Healthy: func() bool { return true }})
	d.SetRouter(&route.Router{GlobalProxy: true}) // 公网默认走 proxy

	m := route.Meta{IP: netip.MustParseAddr("8.8.8.8"), Port: 443}
	if _, err := d.Dial(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if pxA.lastAddr != "8.8.8.8:443" || pxB.lastAddr != "" {
		t.Fatalf("换前应命中 pxA: A=%q B=%q", pxA.lastAddr, pxB.lastAddr)
	}

	d.SetTransport(&Transport{Proxy: pxB, Healthy: func() bool { return true }})
	if _, err := d.Dial(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if pxB.lastAddr != "8.8.8.8:443" {
		t.Fatalf("换后应命中 pxB, got %q", pxB.lastAddr)
	}
}

func TestDialSetTransportRace(t *testing.T) {
	dr := &recordDialer{}
	d := &Dialer{Direct: dr}
	d.SetTransport(&Transport{Proxy: &recordDialer{}, Healthy: func() bool { return true }})
	d.SetRouter(&route.Router{GlobalProxy: true})

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				d.SetTransport(&Transport{Proxy: &recordDialer{}, Healthy: func() bool { return true }})
			}
		}
	}()
	m := route.Meta{IP: netip.MustParseAddr("8.8.8.8"), Port: 443}
	for i := 0; i < 200; i++ {
		if c, err := d.Dial(context.Background(), m); err == nil {
			c.Close()
		}
	}
	close(stop)
	wg.Wait()
}
```
(若文件未 import `sync`,在 import 块加 `"sync"`。)

- [ ] **Step 2: 跑红**

Run: `go test ./internal/dialer/ -run 'SetTransport' 2>&1 | head`
Expected: 编译失败(`d.SetTransport undefined` / `Transport undefined`)。

- [ ] **Step 3: 改 dialer.go —— Transport + 原子字段 + SetTransport**

在 `internal/dialer/dialer.go`:

(a) `Dialer` 结构体里删 `Proxy ContextDialer` 与 `Healthy func() bool` 两行,加 `transport` 原子字段。当前(约 48-62 行):
```go
type Dialer struct {
	router     atomic.Pointer[route.Router]
	Fake       *fakeip.Pool
	Resolver   Resolver
	Proxy      ContextDialer // 经 brook socks5      ← 删
	Direct     ContextDialer // 直连
	Healthy    func() bool   // 隧道是否健康(kill-switch 用),可空   ← 删
	Killswitch bool
	Stats      DecisionCounter
	UDPMode    string
	SplitDirect *splitdns.Set
	leakWarned atomic.Bool
}
```
改成:
```go
type Dialer struct {
	router     atomic.Pointer[route.Router]
	transport  atomic.Pointer[Transport] // 取代裸 Proxy/Healthy:运行期可原子换隧道
	Fake       *fakeip.Pool
	Resolver   Resolver
	Direct     ContextDialer // 直连
	Killswitch bool
	Stats      DecisionCounter
	UDPMode    string
	SplitDirect *splitdns.Set
	leakWarned atomic.Bool
}

// Transport 是一次可原子替换的传输(socks 代理 + 健康判定),供运行期换隧道。
type Transport struct {
	Proxy   ContextDialer // 经隧道 socks5
	Healthy func() bool   // 隧道健康(kill-switch 用);可空
}

// SetTransport 原子替换当前传输(proxy + healthy 一并换,绝不半换)。
func (d *Dialer) SetTransport(t *Transport) { d.transport.Store(t) }
```

(b) `DialWithInitial` 顶部(现 `rt := d.router.Load()` 之后)加一次快照读:
```go
	rt := d.router.Load()
	tr := d.transport.Load()
	if tr == nil {
		tr = &Transport{} // 未 SetTransport(理论不发生):Healthy nil → kill-switch 视作不健康
	}
```

(c) 把 `DialWithInitial` 体内全部 `d.Healthy` 替换为 `tr.Healthy`(3 处,kill-switch 判定 `d.Killswitch && d.Healthy != nil && !d.Healthy()` → `d.Killswitch && tr.Healthy != nil && !tr.Healthy()`),全部 `d.Proxy` 替换为 `tr.Proxy`(2 处,`d.Proxy.DialContext(...)` → `tr.Proxy.DialContext(...)`)。**只在 `DialWithInitial` 内**;`d.Killswitch`/`d.Stats`/`d.Direct` 等不变。

- [ ] **Step 4: 改所有构造点(tests + run.go)**

`internal/dialer/dialer_test.go` —— 4 处构造去掉 `Proxy:`/`Healthy:` 字段、改 `SetTransport`:

1. `newTestDialer`(约 42-46 行):
```go
	px, dr := &recordDialer{}, &recordDialer{}
	d := &Dialer{Fake: fake, Resolver: res, Direct: dr, Killswitch: ks}
	d.SetTransport(&Transport{Proxy: px, Healthy: func() bool { return healthy }})
	d.SetRouter(r)
	return d, px, dr
```

2. `TestDialerHotSwapRouter`(约 202 行):
```go
	d := &Dialer{Direct: dr}
	d.SetTransport(&Transport{Proxy: px, Healthy: func() bool { return true }})
```

3. `TestDialSplitDirectForcesDirect`(约 230 行):
```go
	d := &Dialer{Direct: direct, SplitDirect: set}
	d.SetTransport(&Transport{Proxy: proxy})
```

4. `TestDialNonSplitPublicGoesProxy`(约 247 行):
```go
	d := &Dialer{Direct: direct, SplitDirect: splitdns.NewSet()}
	d.SetTransport(&Transport{Proxy: proxy})
```

`internal/supervisor/run.go` —— 构造 Dialer(约 225-233 行)去掉 `Proxy:`/`Healthy:` 字段、改 `SetTransport`:
```go
	d := &dialer.Dialer{
		Fake:        pool,
		Resolver:    newResolver(cfg.DNS.China, direct),
		Direct:      direct,
		Healthy:     tun0.Healthy,   ← 删这行
		Killswitch:  cfg.Killswitch,
		Stats:       counters,
		UDPMode:     cfg.UDP.Mode,
		SplitDirect: splitDirect,
	}
	d.SetRouter(router)
```
改为(删 `Proxy:`/`Healthy:`,在 `d :=` 之后、`d.SetRouter` 之前插 `SetTransport`):
```go
	d := &dialer.Dialer{
		Fake:        pool,
		Resolver:    newResolver(cfg.DNS.China, direct),
		Direct:      direct,
		Killswitch:  cfg.Killswitch,
		Stats:       counters,
		UDPMode:     cfg.UDP.Mode,
		SplitDirect: splitDirect,
	}
	d.SetTransport(&dialer.Transport{Proxy: proxyDialer, Healthy: tun0.Healthy})
	d.SetRouter(router)
```
(注:`run.go` 现有 `d := &dialer.Dialer{...}` 字面量里有 `Proxy: proxyDialer,` 和 `Healthy: tun0.Healthy,` 两行——都删掉,合并进 SetTransport。)

- [ ] **Step 5: 跑绿 + race + 全量 + 跨平台**

Run:
```bash
go test ./internal/dialer/ -run 'SetTransport|Dial|Hot' -v 2>&1 | tail -20
go test -race ./internal/dialer/
go build ./... && go vet ./... && go test ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: 新 2 测 + 既有 dialer 测全绿;`-race` 干净;全套件绿;两平台编译过。

- [ ] **Step 6: 提交**

```bash
git add internal/dialer/dialer.go internal/dialer/dialer_test.go internal/supervisor/run.go
git commit -m "refactor(dialer): Proxy/Healthy → atomic.Pointer[Transport] + SetTransport

为运行期换隧道(SetTransport Slice 2)打基座:两裸字段合进单个原子 Transport
(proxy+healthy 一并换、绝不半换),Dial 顶部取一致快照;run.go 启动 SetTransport 一次。
行为不变;新增 swap + -race 并发测试。serveControl/refreshLoop 留 Slice 2。

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- `Transport{Proxy, Healthy}` + `atomic.Pointer[Transport]` 替换裸字段 + `SetTransport` → Task1 Step 3a。
- `DialWithInitial` 顶部一次 Load 取一致快照 + nil 守卫 + 5 处替换 → Step 3b/3c。
- run.go 启动设一次、行为不变 → Step 4。
- swap 测试 + `-race` 并发测试 → Step 1 + Step 5。
- serveControl/refreshLoop 不动 → 计划未触碰(spec 明列 Slice 2)。

**占位扫描:** 无 TBD;每步完整代码/命令。行号是锚点核对(按内容定位,可能微移)。

**类型一致性:** `Transport{Proxy ContextDialer; Healthy func() bool}`(Step3a)与测试/ run.go 构造(Step1/Step4)字段名一致;`SetTransport(*Transport)` 签名一致;`tr.Proxy`/`tr.Healthy`(Step3c)与 Transport 字段一致;`recordDialer`/`route.Meta`/`netip` 沿用现有测试辅助(已 import)。
