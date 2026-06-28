# Dialer 热换传输基座(SetTransport Slice 1)设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-27)。

## 背景与定位

SetTransport(运行期换隧道:同服务器 brook↔REALITY / 轮换凭据)是 agent 控制面最后一个还 nop 的 mutation。它比 Rehijack 大:活换隧道牵动**四个耦合关切**——① 隧道生命周期(`tun0` 链接在工厂里写死,换=起新隧道/停旧)② **dialer 热换**(`Dialer.Proxy`/`Healthy` 是被每条连接 `Dial` 并发读的裸字段,只有 `router` 是 atomic)③ server-bypass 路由 ④ DNS static-A。用户决策:**先做「同服务器换传输」**(server IP 不变 → /32 bypass 与 static-A 仍有效,③④ 不动)。

故拆片:**Slice 1 = dialer 热换基座(本设计)**——把 `Proxy`/`Healthy` 改成单个原子可换的 holder,行为不变、纯 Mac 可测,隔离并发敏感部分;**Slice 2 = SetTransport 真换**(起新隧道+等健康+原子换+停旧 + serveControl/refreshLoop 跟随,另开 spec/plan);跨服务器故障转移(改 ③④)留 Slice 3。

## 目标 / 非目标

**目标**:`dialer.Dialer` 的 `Proxy ContextDialer` + `Healthy func() bool` 两个裸字段,替换为单个 `atomic.Pointer[Transport]`(`Transport{Proxy, Healthy}`)+ `SetTransport(*Transport)`;`Dial` 路径经原子读取;`run.go` 启动时 `SetTransport` 一次。**行为完全不变**(生产仍一条隧道、启动设一次,无任何 swap)。全 Mac 原生可测,含 `-race`。

**非目标**:任何真实 swap(Slice 2);`serveControl`/`refreshLoop` 改造(仍直接读 `tun0`,Slice 2 处理);跨服务器(Slice 3)。

## 架构

`dialer.Proxy`/`Healthy` 仅在 `dialer.go` 的 `Dial`/`DialWithInitial` 内被读(行 98/127/142/199/214),仅由 `run.go`(228/230)与 `dialer_test.go` 构造。改动面 = 3 文件,自包含。

### Transport holder(dialer 包)

```go
// Transport 是一次可原子替换的传输(socks 代理 + 健康判定),供运行期换隧道。
type Transport struct {
    Proxy   ContextDialer // 经隧道 socks5
    Healthy func() bool   // 隧道健康(kill-switch 用);可空(同旧 Healthy 字段语义)
}

type Dialer struct {
    router    atomic.Pointer[route.Router]
    transport atomic.Pointer[Transport] // 取代裸 Proxy + Healthy
    Fake        *fakeip.Pool
    Resolver    Resolver
    Direct      ContextDialer
    Killswitch  bool
    Stats       DecisionCounter
    UDPMode     string
    SplitDirect *splitdns.Set
    leakWarned  atomic.Bool
}

// SetTransport 原子替换当前传输(proxy + healthy 一并换,绝不半换)。
func (d *Dialer) SetTransport(t *Transport) { d.transport.Store(t) }
```

### Dial 路径读取

`DialWithInitial` 顶部读一次:`tr := d.transport.Load()`,后续 kill-switch 判定用 `tr.Healthy != nil && !tr.Healthy()`、代理拨号用 `tr.Proxy.DialContext(...)`(替换现有 `d.Healthy`/`d.Proxy`)。单次 Load 保证本次 Dial 内 proxy 与 healthy 取自同一快照(一致)。`tr` 理论上不会 nil(生产/测试都先 SetTransport);防御性:若 nil 则视作「无 healthy」(`tr.Healthy==nil` 路径),与旧「Healthy 字段为 nil」语义一致,代理拨号路径要求已 SetTransport(同旧版要求 Proxy 非 nil,不引入新风险)。

### run.go 构造

```go
d := &dialer.Dialer{Fake: pool, Resolver: ..., Direct: direct, Killswitch: cfg.Killswitch, Stats: counters, UDPMode: cfg.UDP.Mode, SplitDirect: splitDirect}
d.SetTransport(&dialer.Transport{Proxy: proxyDialer, Healthy: tun0.Healthy})
d.SetRouter(router)
```
(删去结构体字面量里的 `Proxy:`/`Healthy:` 两行,改 SetTransport。)

## 数据流 / 错误处理

数据流不变:每条连接 `Dial` → 原子读 transport → 按 Router 决策走 Direct/Proxy/Block。kill-switch 语义不变(`tr.Healthy` nil-safe;隧道不健康 + killswitch → Proxy 决策 Block,不漏 IP)。无新错误路径。

## 测试策略(全 Mac 原生)

- 改 `dialer_test.go` 现有构造:`&Dialer{..., Proxy: px, Healthy: fn}` → `d := &Dialer{...}; d.SetTransport(&Transport{Proxy: px, Healthy: fn})`(各处)。既有用例继续绿(行为不变)。
- 新增 `TestSetTransportSwaps`:构造 dialer + SetTransport(proxyA, healthy=true)→ 一次 Proxy 决策 Dial 命中 proxyA;再 `SetTransport(proxyB, healthy=true)`→ 下次 Dial 命中 proxyB(证明原子换生效)。
- 新增 `TestDialSetTransportRace`(`-race`):N goroutine 并发 `Dial` + 1 goroutine 循环 `SetTransport`,`go test -race` 无数据竞争(基座的核心价值)。
- 回归:`go build ./... && go vet ./... && go test -race ./internal/dialer/ && go test ./...`;两平台编译;`GOOS=linux go vet -tags integration`。

## 决策记录

- 单 `atomic.Pointer[Transport]`(proxy+healthy 合一)而非两个独立 atomic——保证一致、避免半换;胜于 mutex(热路径免锁)。
- 本片纯重构、行为不变;serveControl/refreshLoop 的 tunnel 引用留 Slice 2 一并换。
- `Transport.Healthy` 保留「可空」语义(同旧 `Healthy` 字段)。
- 同服务器换传输 → Slice 2 不动 bypass/DNS;跨服务器留 Slice 3。

## 范围自检

单一可实现重构(dialer 字段→原子 holder + run.go 构造 + 测试),3 文件、全 Mac 可测(含 -race)、零行为变更。适合一份小 plan(1-2 任务)。
