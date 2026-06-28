# SetTransport 真换隧道(Slice 2b)设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-27)。

## 背景与定位

SetTransport(运行期换隧道)三片:Slice 1(dialer 热换基座 `c275290`)+ Slice 2a(live-tunnel 基座 `d6db541`)已交付。**Slice 2b = 让 `mut.SetTransport` 的 apply 真换隧道**(同服务器:brook↔REALITY / 换 link / 轮换凭据)。A2 协议层已就绪(`handleSetTransport` → `mut.SetTransport(link)` → `eng.Arm(apply, undo)`),仅 `liveMutator.SetTransport` 现经嵌入的 `nopMutator` 返回 nop。Slice 2a 已把 serveControl/refreshLoop/dialer 接到 `lt`,基座齐备。

## 目标 / 非目标

**目标**:`liveMutator.SetTransport(link)` 返回真 apply/undo——apply 经 `transportSwapper` 建新隧道、等健康、原子换(`lt.set`+`d.SetTransport`+新 socks)、停旧;undo 在确实换过时换回旧 link。commit 保留新(旧已停)、rollback/死手换回旧。run.go 接线 + Run 退出停「当前」隧道。

**非目标**:跨服务器(改 server-bypass/DNS,Slice 3);优雅排空旧连接(需引擎 commit-hook,且 rollback 仍会重置,收益有限——本片用硬换);darwin 真机。

## 决策:硬换(hard swap)

apply 立即停旧隧道(brook 子进程被 Kill)→ **既有 TCP 连接重置**,新连接走新隧道。这是换传输的固有行为(类比重连 VPN);commit-confirmed 下 rollback 也会重置新连接,故"无缝"本不可达。硬换契合引擎的 apply/undo(无需 commit-hook),最简。换隧道时既有连接短暂重置 = 预期行为(文档说明)。

## 架构

### linkSwapper 接缝(liveMutator 依赖,可 fake)

```go
// linkSwapper:把"换到某 link"抽象出来,使 liveMutator 的 commit-confirmed 逻辑 Mac 可测,
// 真隧道操作(建/起/等健康/原子换/停旧)留真实现、真机验。
type linkSwapper interface {
    currentLink() string
    swapTo(link string) error
}
```

### transportSwapper(真实现,supervisor 包)

```go
type transportSwapper struct {
    mu            sync.Mutex
    lt            *liveTunnel
    d             *dialer.Dialer
    build         func(link string) (*tunnel.Tunnel, error) // = run.go 的 buildTunnel
    healthTimeout time.Duration
    ctx           context.Context
    curLink       string
}

func (s *transportSwapper) currentLink() string { s.mu.Lock(); defer s.mu.Unlock(); return s.curLink }

func (s *transportSwapper) swapTo(link string) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    newTun, err := s.build(link)
    if err != nil {
        return err
    }
    newTun.Start()
    if err := waitTunnelHealthy(s.ctx, newTun, s.healthTimeout); err != nil {
        newTun.Stop()         // 新隧道没起来:停掉、不换,旧隧道仍在服务
        return err
    }
    px, err := socksProxy(newTun.SocksAddr(), &net.Dialer{Timeout: 10 * time.Second})
    if err != nil {
        newTun.Stop()
        return err
    }
    old := s.lt.get()
    s.lt.set(newTun)                                                        // serveControl/refreshLoop 跟随
    s.d.SetTransport(&dialer.Transport{Proxy: px, Healthy: newTun.Healthy}) // 新/旧连接分界
    s.curLink = link
    old.Stop()                                                             // 停旧 brook(既有连接重置)
    return nil
}
```
- 健康门:新隧道没在 `healthTimeout` 内健康 → 停新、返错、**不换**(旧隧道毫发无损)。跨服务器误传的 link 因无 bypass 不会健康 → 安全 abort。
- `mu` 串行化(control mutex/engine 已串行,`mu` 为防御 + 保 curLink/lt 一致)。

### liveMutator 改造(mutator.go)

去掉 `nopMutator` 嵌入(两方法都将为真);加 `swap linkSwapper` 字段;Rehijack 不变;新增真 SetTransport:
```go
type liveMutator struct {
    plat         rehijacker   // Rehijack 用
    swap         linkSwapper  // SetTransport 用
    tunH         tunHandle
    serverBypass []string
    userBypass   []string
}

func (m *liveMutator) SetTransport(newLink string) (apply, undo func() error, err error) {
    oldLink := m.swap.currentLink() // 方法体只读(A2 无副作用契约)
    apply = func() error { return m.swap.swapTo(newLink) }
    undo = func() error {
        if m.swap.currentLink() == oldLink { // apply 没换成(健康失败)→ 无需 undo
            return nil
        }
        return m.swap.swapTo(oldLink) // 换回旧 link(重建旧隧道,旧已在 apply 停掉)
    }
    return apply, undo, nil
}
```

### run.go 接线

- 构造 `swapper := &transportSwapper{lt: lt, d: d, build: buildTunnel, healthTimeout: healthTimeout, ctx: ctx, curLink: cfg.Server}`(`buildTunnel`/`lt`/`d`/`healthTimeout`/`ctx` 均在 Run 作用域)。
- `liveMutator` 构造加 `swap: swapper`、去 `nopMutator`(保留 `plat`/`tunH`/`serverBypass`/`userBypass`)。
- **`defer tun0.Stop()` → `defer func() { lt.get().Stop() }()`**:换隧道后 `lt` 指向新隧道,Run 退出须停「当前」而非启动时捕获的 `tun0`(gap ②)。apply 停掉换走的旧隧道;`tunnel.Stop` 幂等,双停安全。

## 数据流 / 错误处理

agent `POST /v0/transport{link}` →(A1 peer-cred)→ `mut.SetTransport(link)` → `eng.Arm(apply, undo)`:apply 换新(停旧);**commit** 保留新(旧已停);**rollback/死手** → undo 换回旧(重建)。健康/构建失败 → apply 返错、不换、旧留存、引擎 rollback(undo 因 curLink 未变而 nop)。已 armed → 409;非 root → 403(沿用 A2/A1)。

## 测试策略

**Mac 原生(fakeSwapper)**:fake 实现 `linkSwapper`,记录 `swapTo` 调用、可控 `currentLink`(swapTo 成功即更新)。断言:
- `SetTransport` 方法体无副作用(返回前 `swapTo` 零调用);
- apply 调 `swapTo(newLink)` 一次;
- swapTo 成功(fake 更新 currentLink)后 undo 调 `swapTo(oldLink)`;
- swapTo 失败(fake 不更新 currentLink)后 undo **nop**(零额外调用);
- apply 透传 swapTo 错误。

**真机(VPS,死手兜底)**:部署 → `bx run --test-timeout` → 同服务器换 link(brook `:9999`→另一监听 或 同 link 验机制):`POST /v0/transport`→armed;观察新隧道健康、旧 brook pid 退出、`/v0/status` 的 socks_addr/health 跟随新隧道、列表刷新经新 socks;`/v0/commit`→committed(旧停留新);或 `/v0/rollback`/死手→换回旧 link(重建旧隧道、新停);全程 SSH/路由不变(同服务器,bypass/DNS 不动);既有连接重置(预期)。

## 决策记录

- 硬换(apply 停旧、undo 重建旧),契合引擎 apply/undo 无 commit-hook;既有连接换时重置(预期)。
- `linkSwapper` 接缝:liveMutator commit-confirmed 逻辑 Mac 可测,真 `transportSwapper` 真机验。
- undo 条件化(curLink 变了才换回),避免 apply 健康失败时无谓重建。
- liveMutator 去 nopMutator(SetTransport+Rehijack 均真);swapTo 经 `mu` 串行。
- Run 退出改停 `lt.get()`(当前隧道);Stop 幂等保双停安全。
- 同服务器 → 不动 bypass/DNS;跨服务器留 Slice 3。

## 范围自检

单一可实现增量(linkSwapper + transportSwapper + liveMutator SetTransport + run.go 接线 + fakeSwapper 单测),Mac 测 commit-confirmed 逻辑、真机验真换。适合一份小 plan(2 任务:① swapper+liveMutator+Mac 测;② run.go 接线 + 真机验)。
