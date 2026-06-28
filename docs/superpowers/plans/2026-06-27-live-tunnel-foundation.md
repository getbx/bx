# live-tunnel 基座(SetTransport Slice 2a)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 引入 `liveTunnel` 原子 holder(满足 `tunnelStatser` + `Healthy`)+ 抽出可复用 `buildTunnel(link)`,让 `serveControl`/`refreshLoop`/dialer 经 `lt` 读当前隧道——**行为不变**,为 Slice 2b 真换隧道的多消费者跟随打基座。

**Architecture:** 新 `liveTunnel{atomic.Pointer[tunnel.Tunnel]}`(`SetRouter`/Slice1 同款原子范式);run.go 把内联 `transportKind`→`NewReality`/`NewBrook` 派发(含按需 `EnsureSingbox`)抽成 `buildTunnel` 闭包,建初始隧道存入 `lt`、设一次;serveControl 收 `lt`(满足 `tunnelStatser`),refreshLoop 经 `lt.Healthy`+`lt.SocksAddr()`。改动面 = 新增 2 文件 + run.go(自包含;run.go 半改不编译,故同任务)。

**Tech Stack:** Go 1.26.3;`internal/supervisor`(新增 `livetunnel.go`/`livetunnel_test.go`,改 `run.go`)。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3。TDD;提交中文 conventional + 结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`;master 直接提交。
- **行为零变更**:生产仍一条隧道、`lt.set` 只在启动调一次,无任何 swap(那是 2b)。`mut.SetTransport` 仍 nop。
- `liveTunnel` 满足 `tunnelStatser`(`Stats() tunnel.Stats` + `SocksAddr() string`,control.go:33)并加 `Healthy() bool`;编译期守卫 `var _ tunnelStatser = (*liveTunnel)(nil)`。
- `buildTunnel` 含按需 `EnsureSingbox`(2b 的 brook↔REALITY swap 直接复用)。
- refreshLoop 改每轮由 `lt.SocksAddr()` 现建 socks 客户端(弃捕获的单 client 复用;24h 间隔下无损),以跟随 2b swap。

---

### Task 1: liveTunnel holder + buildTunnel 抽取 + run.go 接线

**Files:**
- Create: `internal/supervisor/livetunnel.go`(`liveTunnel` 类型 + 方法 + 编译期守卫)
- Create: `internal/supervisor/livetunnel_test.go`(`TestLiveTunnelSwap`)
- Modify: `internal/supervisor/run.go`(`buildTunnel` 闭包替换内联 switch;`lt` 建立;serveControl/dialer/refreshLoop 改读 `lt`)

**Interfaces:**
- Consumes: 现有 `tunnel.New`/`tunnel.NewBrook`/`tunnel.NewReality`、`tunnelStatser`(control.go)、`provision.EnsureSingbox`、`socksProxy`/`proxyHTTPClient`/`refreshLoop`/`transportKind`、`dialer.Transport`。
- Produces:
  - `type liveTunnel struct{ cur atomic.Pointer[tunnel.Tunnel] }` + `set/get/Stats/SocksAddr/Healthy`
  - (2b 依赖:`lt.set(newTun)` 运行期换 + serveControl/refreshLoop 已经它读。)

- [ ] **Step 1: 写失败测试**

新建 `internal/supervisor/livetunnel_test.go`:
```go
package supervisor

import (
	"testing"

	"github.com/getbx/bx/internal/tunnel"
)

func TestLiveTunnelSwap(t *testing.T) {
	// tunnel.New 仅存字段;不 Start 即可读 SocksAddr(确定值)/Stats(零值)/Healthy(false)。
	a := tunnel.New("127.0.0.1:1111", nil, nil)
	b := tunnel.New("127.0.0.1:2222", nil, nil)
	lt := &liveTunnel{}

	lt.set(a)
	if lt.get() != a {
		t.Fatal("set(a) 后 get 应为 a")
	}
	if lt.SocksAddr() != "127.0.0.1:1111" {
		t.Fatalf("SocksAddr 应委派到 a, got %q", lt.SocksAddr())
	}

	lt.set(b) // 原子替换
	if lt.get() != b {
		t.Fatal("set(b) 后 get 应为 b")
	}
	if lt.SocksAddr() != "127.0.0.1:2222" {
		t.Fatalf("替换后 SocksAddr 应委派到 b, got %q", lt.SocksAddr())
	}
	if lt.Healthy() {
		t.Error("未 Start 应不健康")
	}
	_ = lt.Stats() // 不 panic 即可(委派当前隧道)
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run LiveTunnel 2>&1 | head`
Expected: 编译失败(`undefined: liveTunnel`)。

- [ ] **Step 3: 写 livetunnel.go**

新建 `internal/supervisor/livetunnel.go`:
```go
package supervisor

import (
	"sync/atomic"

	"github.com/getbx/bx/internal/tunnel"
)

// liveTunnel 原子持有当前隧道,供运行期换隧道时一处替换、多消费者(serveControl/refreshLoop/
// dialer transport)跟随。满足 tunnelStatser(Stats+SocksAddr)并加 Healthy。
// 本片 set 仅启动调一次(Slice 2b 才在 swap 时调)。
type liveTunnel struct {
	cur atomic.Pointer[tunnel.Tunnel]
}

func (lt *liveTunnel) set(t *tunnel.Tunnel) { lt.cur.Store(t) }
func (lt *liveTunnel) get() *tunnel.Tunnel  { return lt.cur.Load() }

func (lt *liveTunnel) Stats() tunnel.Stats { return lt.get().Stats() }
func (lt *liveTunnel) SocksAddr() string   { return lt.get().SocksAddr() }
func (lt *liveTunnel) Healthy() bool        { return lt.get().Healthy() }

// 编译期守卫:*liveTunnel 必须满足 serveControl 要的 tunnelStatser。
var _ tunnelStatser = (*liveTunnel)(nil)
```

- [ ] **Step 4: 改 run.go —— buildTunnel 抽取 + lt + 三消费者接线**

(a) 把当前隧道构建块(run.go ~134-152,`// 2) 隧道` 注释起、到 `}` 结束 switch)替换为 `buildTunnel` 闭包 + 一次调用:
```go
	// 2) 隧道:按 server link 的 scheme 选传输(brook | reality),数据面不变。
	// buildTunnel 由 link 建隧道(含按需 sing-box 准备),供启动与 Slice 2b 运行期换隧道复用。
	buildTunnel := func(link string) (*tunnel.Tunnel, error) {
		switch transportKind(link) {
		case "reality":
			singboxPath, err := provision.EnsureSingbox(cfg.DataDir, cfg.SingboxBin, cfg.SingboxURL, cfg.SingboxSHA256)
			if err != nil {
				return nil, fmt.Errorf("准备 sing-box: %w", err)
			}
			confPath := filepath.Join(cfg.DataDir, "sing-box.json")
			return tunnel.NewReality(singboxPath, link, opts.Probe, confPath, cfg.HTTPProxy)
		default:
			return tunnel.NewBrook(brookPath, link, opts.Probe, cfg.HTTPProxy)
		}
	}
	tun0, err := buildTunnel(cfg.Server)
	if err != nil {
		return fmt.Errorf("构建隧道: %w", err)
	}
```
(保留其后的 `tun0.Start()` / `defer tun0.Stop()` / 健康等待 / 日志原样不变。)

(b) 在健康日志(`log.Printf("bx 隧道健康: …")`,~163 行)之后插入 lt 建立:
```go
	lt := &liveTunnel{}
	lt.set(tun0)
```

(c) dialer transport(run.go:234)的 `Healthy` 改读 lt:
```go
	d.SetTransport(&dialer.Transport{Proxy: proxyDialer, Healthy: lt.Healthy})
```
(`proxyDialer` 仍由 `tun0.SocksAddr()` 建,= lt 当前,不变。)

(d) serveControl(run.go:271)传 `lt` 取代 `tun0`:
```go
		return serveControl(counters, lt, serverHost, cfg.UDP.Mode, mutEng, mut)
```

(e) refreshLoop(run.go ~291-293)改为门用 `lt.Healthy`、fetch 内每轮由 `lt.SocksAddr()` 现建 client。当前:
```go
		client := proxyHTTPClient(proxyDialer) // 单个客户端复用连接池,跨刷新周期不重建
		go refreshLoop(ctx, cfg.Lists.RefreshInterval(), tun0.Healthy, func() error {
			if err := fetchLists(ctx, client, cfg.DataDir); err != nil {
				return err
			}
```
改为(删捕获的 `client :=` 行,fetch 内现建,门改 lt.Healthy):
```go
		go refreshLoop(ctx, cfg.Lists.RefreshInterval(), lt.Healthy, func() error {
			px, err := socksProxy(lt.SocksAddr(), &net.Dialer{Timeout: 10 * time.Second})
			if err != nil {
				return err
			}
			if err := fetchLists(ctx, proxyHTTPClient(px), cfg.DataDir); err != nil {
				return err
			}
```
(其后 `rebuildRouterFromFiles`…`d.SetRouter(nr)` 段不变。`net` 已 import。)

- [ ] **Step 5: 跑绿 + 全量 + 跨平台**

Run:
```bash
go test ./internal/supervisor/ -run 'LiveTunnel' -v
go build ./... && go vet ./... && go test ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
GOOS=linux go vet ./internal/supervisor/
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: `TestLiveTunnelSwap` PASS;既有 supervisor(含 serveControl/control)测全绿;全套件绿;两平台编译过。

- [ ] **Step 6: 提交**

```bash
git add internal/supervisor/livetunnel.go internal/supervisor/livetunnel_test.go internal/supervisor/run.go
git commit -m "refactor(supervisor): liveTunnel 基座 + buildTunnel 抽取(SetTransport Slice 2a)

liveTunnel 原子 holder(满足 tunnelStatser + Healthy);serveControl/refreshLoop/dialer
改经 lt 读当前隧道;内联 NewReality/NewBrook 派发(含按需 EnsureSingbox)抽成 buildTunnel。
行为不变(启动设一次、无 swap);为 2b 真换的多消费者跟随打基座。

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- `liveTunnel` 满足 tunnelStatser + Healthy + 编译守卫 → Step 3。
- `buildTunnel` 抽取(含按需 EnsureSingbox)→ Step 4a。
- serveControl 经 lt → Step 4d;dialer Healthy 经 lt → 4c;refreshLoop 经 lt.Healthy + 每轮现建 socks client → 4e。
- run.go 建初始隧道存 lt、设一次、行为不变 → Step 4a/4b。
- liveTunnel 单测(set/get/委派/原子替换)→ Step 1。

**占位扫描:** 无 TBD;每步完整代码/命令。行号为锚点核对(按内容定位,可能微移)。

**类型一致性:** `liveTunnel.Stats() tunnel.Stats`/`SocksAddr() string` 与 `tunnelStatser`(control.go:33)签名一致(编译守卫强制);`set(*tunnel.Tunnel)`/`get() *tunnel.Tunnel` 与 `buildTunnel` 返回 `*tunnel.Tunnel`、`lt.set(tun0)` 一致;`lt.Healthy` 作 `dialer.Transport.Healthy`(`func() bool`)与 `refreshLoop` 第三参(`healthy func() bool`)一致;`socksProxy(string,*net.Dialer)(dialer.ContextDialer,error)` + `proxyHTTPClient(dialer.ContextDialer)` 与 4e 用法一致。
