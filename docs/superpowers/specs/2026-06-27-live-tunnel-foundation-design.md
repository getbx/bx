# live-tunnel 基座(SetTransport Slice 2a)设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-27)。

## 背景与定位

SetTransport(运行期换隧道)拆三片:Slice 1(dialer 热换基座,已交付 `c275290`)→ **Slice 2a(live-tunnel 基座,本设计)** → Slice 2b(真换隧道,真机)→ Slice 3(跨服务器,留)。用户定:**同服务器换传输**(server IP 不变 → bypass/DNS 不动)。

A2 已建好 SetTransport 协议层(`/v0/transport` → `mut.SetTransport` → `SetTransportControl` → liveOps),只 mutator 的 apply 是 nop。真换隧道(2b)要把一次 swap 反映到 `tun0` 的**三个消费者**:① dialer(Slice 1 的 `SetTransport` 已就绪)② `serveControl` 的 `tunnelStatser` 引用 ③ `refreshLoop` 的 health 门 + socks 客户端。

**Slice 2a = 让 ②③ 经一个可原子替换的 `liveTunnel` holder 读取、并抽出可复用的 `buildTunnel(link)`**——纯重构、行为不变、全 Mac/CI 可测,隔离出 2b 真换前的所有非并发-热路径接线。

## 目标 / 非目标

**目标**:新增 `liveTunnel`(`atomic.Pointer[tunnel.Tunnel]`,满足现有 `tunnelStatser` + 加 `Healthy()`);`serveControl` 与 `refreshLoop` 改经 `liveTunnel` 读当前隧道;把 run.go 内联的 `transportKind`→`NewReality`/`NewBrook` 派发(含按需 `EnsureSingbox`)抽成可复用的 `buildTunnel(link)` 闭包;run.go 用 `buildTunnel(cfg.Server)` 建初始隧道、存入 `lt`、设一次。**行为完全不变**(无任何 swap)。全 Mac/CI 可测。

**非目标**:任何真实 swap(2b);`mut.SetTransport` 仍 nop;跨服务器 bypass/DNS(Slice 3);proxyDialer 的运行期重建(2b 在 swap 时做)。

## 架构

### liveTunnel(supervisor 包)

```go
// liveTunnel 原子持有当前隧道,供运行期换隧道时一处替换、多消费者跟随。
// 满足 tunnelStatser(serveControl 用)并加 Healthy(dialer transport + refreshLoop 门用)。
type liveTunnel struct{ cur atomic.Pointer[tunnel.Tunnel] }

func (lt *liveTunnel) set(t *tunnel.Tunnel) { lt.cur.Store(t) }
func (lt *liveTunnel) get() *tunnel.Tunnel  { return lt.cur.Load() }
func (lt *liveTunnel) Stats() tunnel.Stats  { return lt.get().Stats() }    // tunnelStatser
func (lt *liveTunnel) SocksAddr() string    { return lt.get().SocksAddr() } // tunnelStatser
func (lt *liveTunnel) Healthy() bool         { return lt.get().Healthy() }
```
`tunnelStatser`(control.go:33)= `Stats() tunnel.Stats` + `SocksAddr() string`;`*liveTunnel` 满足之(编译期 `var _ tunnelStatser = (*liveTunnel)(nil)` 守卫)。本片 `set` 仅启动时调一次(2b 才在 swap 时调)。

### buildTunnel(run.go 内闭包)

抽出现有内联派发(run.go ~135-152),捕获 `cfg`/`opts.Probe`/`brookPath`:
```go
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
```
按需 `EnsureSingbox` 内置 → 2b 的 brook↔REALITY swap 直接 `buildTunnel(newLink)` 即可,provisioning 已在内。

### run.go 接线(行为不变)

- `tun0, err := buildTunnel(cfg.Server)`(取代内联 switch)→ `tun0.Start()` → `defer tun0.Stop()` → 等健康。
- `lt := &liveTunnel{}; lt.set(tun0)`。
- `serveControl(counters, lt, serverHost, cfg.UDP.Mode, mutEng, mut)`——传 `lt` 取代 `tun0`。
- dialer:`d.SetTransport(&dialer.Transport{Proxy: proxyDialer, Healthy: lt.Healthy})`(`lt.Healthy` 取代 `tun0.Healthy`;`proxyDialer` 仍由 `tun0.SocksAddr()` 建,= `lt` 当前)。
- `refreshLoop`:门用 `lt.Healthy`;fetch 闭包内每轮由 `lt.SocksAddr()` 现建 socks 客户端(`socksProxy(lt.SocksAddr(), …)` → `proxyHTTPClient`),取代启动时捕获的单一 client——去掉「跨周期复用连接池」(24h 间隔下无意义),换取「跟随 2b swap」。

`defer tun0.Stop()` 仍用启动时的 `tun0`(2a 无 swap,等同 `lt.get()`);2b 改由 swap 流程接管旧隧道停止。

## 数据流 / 错误处理

数据流不变:每条连接经 dialer(读 Slice 1 的原子 transport);status 经 serveControl 读 `lt` 当前隧道;列表刷新经 `lt` 当前 socks。`buildTunnel` 返回与 run.go 现有处理一致的错误(sing-box 准备失败 / 隧道构建失败)。无新错误路径。

## 测试策略(全 Mac 原生)

- `liveTunnel` 单测:用 `tunnel` 包现有测试设施(`tunnel.New(addr, fakeRunnerFactory, fakeHealth)`,见 `tunnel_test.go`)造两个真 `*tunnel.Tunnel`,`lt.set(a)`→`lt.get()==a`、`lt.SocksAddr()`/`Stats()`/`Healthy()` 委派到当前;`lt.set(b)` 后委派切到 b(证明原子替换)。
- 编译期守卫:`var _ tunnelStatser = (*liveTunnel)(nil)`(放 control.go 或 liveTunnel 旁)。
- 回归:既有 `serveControl` 测试继续绿(传 `tunnelStatser`,`*liveTunnel` 满足);`go build ./... && go vet ./... && go test ./...`;两平台编译;`GOOS=linux go vet -tags integration`。

## 决策记录

- `liveTunnel` 单原子 holder,满足 `tunnelStatser` + `Healthy`;serveControl/refreshLoop 经它读当前隧道,为 2b swap 多消费者跟随打基座。
- `buildTunnel` 含按需 `EnsureSingbox`,使 2b 的 brook↔REALITY swap 一调即可。
- refreshLoop 改每轮现建 socks 客户端(弃连接池复用,24h 下无损),以跟随 swap。
- 本片纯重构、零行为变更;真 swap、proxyDialer 重建、旧隧道停止接管留 2b。

## 范围自检

单一可实现重构(liveTunnel + buildTunnel 抽取 + serveControl/refreshLoop/dialer 接线改读 lt),全 Mac 可测、零行为变更。适合一份小 plan(1-2 任务)。
