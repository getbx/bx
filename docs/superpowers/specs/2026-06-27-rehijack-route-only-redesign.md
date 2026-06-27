# Rehijack 真 mutator — 路由-only 重设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-27,取代 `2026-06-27-rehijack-real-mutator-design.md`)。

## 背景:为什么推倒重来

`2026-06-27-rehijack-real-mutator-design.md` 的设计(apply = `(*teardown)()` + `plat.Hijack`)已实现并提交(`f0317b4` T1、`ae485c6` T2),但**最终全分支 review(opus)发现致命设计缺陷**:

- Linux 上 `plat.Hijack` 返回的 teardown 是 `netConf.down()`,其 `downSteps()` 含 `{"link","del",<tun>}`——**删除 TUN 设备**(因 Linux `closeTUN` 是 no-op,设备清理被刻意委托给 Hijack 的 `ip link del`,见 `OpenTUN` 注释)。
- 故 Rehijack 的 apply 第一步 `(*teardown)()` 删掉 `bx0`,第二步 `plat.Hijack` → `up()` 首步 `addr add … dev bx0` **必失败(设备已不存在)**。
- 结果:**每次 `/v0/rehijack` 必回滚**,且回滚时 `snapshotter.Restore` 想重建 `default dev bx0 table 100` 也失败(设备没了)→ bx 落到**无 bx0、无劫持**态 → 流量直连 = **真实 IP 泄漏 / kill-switch 失效,重启才能恢复**。是安全回归,非单纯功能不可用。

`gvtun.Open` 建的是**非持久** TUN;`ip link del` 即便 bx 持有 fd 也会从内核移除设备(`OpenDevice` 注释印证「设备由 Hijack 的 `ip link del` 移除」)。

**根因**:`plat.Hijack` 的 teardown 是**关机级**(正确地删路由 + 删设备);Rehijack 误把它当「删路由」复用。但 Rehijack 的产品本意是**外部事件(DHCP 续约 / NetworkManager / 某条 `ip rule` 被清)破坏了路由时,重新落实劫持路由**——此时 `bx0` 仍在,只需重建**路由**。故 Rehijack 必须**只动路由、绝不碰设备**。

**对比 darwin**:darwin 的 Hijack teardown **本就只删路由**(cleanup 闭包仅 `route del`,设备由 Run 的 closeTUN 管,见 `platform_darwin.go:113`)。故本缺陷是 **Linux 特有**。

## 目标 / 非目标

**目标**:Rehijack 的 apply 改为「**路由-only 重落实**」——保住 TUN 设备,重探网关、拆旧路由、装新路由。新增 `platform.RehijackRoutes` 方法(两平台实现);`liveMutator` 去掉 `teardown *func()`(apply 不再收养 teardown);`run.go` 回退惰性指针捕获、生产挂 `&liveMutator`。全 Mac 原生免 root 单测;真机回归须验证 **rehijack 能 commit**(不止安全回滚)。

**非目标**:`SetTransport` 真换隧道(仍 nop,下一刀);router 模式的 rehijack(`RouterMode` 返回明确「暂不支持」错误,YAGNI——host 模式是常态);darwin 真机验证(compile-only)。

## 架构

### Linux:`netConf` 步骤构建器拆分(`platform_linux.go`)

把设备步骤与路由步骤分离,既有 `upSteps()`/`downSteps()` 由它们组合而成(复合行为不变):

```go
// 设备步骤:配地址 + 置 up(仅 Hijack 首次建链路时做)。
func (n *netConf) deviceUpSteps() [][]string {
    return [][]string{
        {"addr", "add", n.tunAddr, "dev", n.tunName},
        {"link", "set", n.tunName, "up"},
    }
}

// routeUpSteps:只装策略路由(bypass / default dev tun / fwmark / 私网 carve / 全量 / v6 阻断)。
// = 旧 upSteps() 去掉 deviceUpSteps 的两步。
func (n *netConf) routeUpSteps() [][]string { /* 旧 upSteps 中 addr/link 之后的全部 */ }

// upSteps = 设备 + 路由(行为同旧)。
func (n *netConf) upSteps() [][]string {
    return append(n.deviceUpSteps(), n.routeUpSteps()...)
}

// routeDownSteps:只拆策略路由(= 旧 downSteps() 去掉 {"link","del",tunName})。
func (n *netConf) routeDownSteps() [][]string { /* 旧 downSteps 去掉 link del */ }

// downSteps = 路由拆除 + 删设备(link del 移到末尾;与旧版功能等价,删除操作幂等、次序无关)。
func (n *netConf) downSteps() [][]string {
    return append(n.routeDownSteps(), []string{"link", "del", n.tunName})
}

func (n *netConf) routeUp() error   { for _, s := range n.routeUpSteps()   { if err := runIP(s...);      err != nil { return err } }; return nil }
func (n *netConf) routeDown()       { for _, s := range n.routeDownSteps() { _ = runIPQuiet(s...) } } // 尽力,忽略单步错误
```

`RehijackRoutes`(Linux):

```go
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
    nc.routeDown()              // 清旧路由(幂等容错,保住 bx0)
    if err := nc.routeUp(); err != nil { // 在存活设备上重装路由
        return err              // 引擎据此 Rollback(经 9a 快照网)
    }
    return nil
}
```

### Darwin:`RehijackRoutes`(接口对齐,compile-only)

```go
func (darwinPlatform) RehijackRoutes(t tunHandle, serverBypass, userBypass []string) error {
    gw, _, err := defaultRouteDarwin()
    if err != nil { return fmt.Errorf("探测默认网关: %w", err) }
    ip := t.Addr
    if i := strings.IndexByte(ip, '/'); i >= 0 { ip = ip[:i] }
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

### `liveMutator`(`mutator.go`)—— 去掉 teardown 指针

```go
type rehijacker interface {
    RehijackRoutes(t tunHandle, serverBypass, userBypass []string) error
}

type liveMutator struct {
    nopMutator   // nop SetTransport(下一刀替换)
    plat         rehijacker
    tunH         tunHandle
    serverBypass []string
    userBypass   []string
}

func (m *liveMutator) Rehijack() (apply, undo func() error, err error) {
    apply = func() error { return m.plat.RehijackRoutes(m.tunH, m.serverBypass, m.userBypass) }
    undo = func() error { return nil } // 路由还原靠 engine.Arm 的 snapshotter.Restore
    return apply, undo, nil            // 方法体无副作用(A2 契约)
}
```
仍须 `&liveMutator{}`(指针接收者 Rehijack;值会退化成嵌入的 nop)。

### `run.go`—— 回退惰性捕获

- `platform` 接口加 `RehijackRoutes(t tunHandle, serverBypass, userBypass []string) error`。
- Hijack 段回退为原样:`teardown, err := plat.Hijack(tunH, serverBypass, cfg.Bypass)` + `defer teardown()`(`liveMutator` 不再碰 teardown,无需惰性指针/defer 包裹)。
- `serverBypass := addrsToCIDRs(serverAddrs)` 提前到 `serveControl` 前;构造 `mut := &liveMutator{plat: plat, tunH: tunH, serverBypass: serverBypass, userBypass: cfg.Bypass}`,传给 `serveControl`(取代 `nopMutator{}`)。

## 数据流

agent → `POST /v0/rehijack` → `requireRoot`(A1)→ `mut.Rehijack()` → `engine.Arm(apply, undo)`:捕获快照 → `apply` = `plat.RehijackRoutes`(重探网关 + 路由拆/装,**设备存活**)→ 200 armed → commit 保留 / rollback+死手经快照还原。

## 错误处理

- `routeUp` 失败 → apply 返 err → 引擎 Rollback(快照还原);设备始终在,快照可重建 → 无泄漏态。
- router 模式 → apply 立即返「暂不支持」err → 引擎 Rollback → 安全。
- 已 armed → 409;非 root → 403(沿用 A1/A2)。

## 测试策略

**Mac 原生免 root**:
- 纯步骤构建器:断言 `routeUpSteps()`/`routeDownSteps()` **不含** `addr add` / `link set up` / `link del`,且含路由步骤(default dev tun、rule 200 等);回归 `upSteps()` = deviceUp + routeUp、`downSteps()` = routeDown + link del(组合后与旧序列集合一致)。
- `fakePlatform.RehijackRoutes` 记录调用:`liveMutator.Rehijack()` 方法体无副作用(零调用);`apply()` 调 `RehijackRoutes` 一次、参数正确;`apply` 透传其错误;`undo()` nop;`SetTransport` 仍 nop(嵌入)。

**真机回归(B 阶段,Linux VPS,死手兜底)**:`bx run --test-timeout` → 人为破坏劫持(`ip rule del pref 200` 或 `ip route del default table 100`)→ `POST /v0/rehijack`(root)→ **armed 200** → 观察路由被重装修复(pref 200 + default dev bx0 回来、**bx0 始终在**)→ **`POST /v0/commit` 到 committed**(关键:证明能 commit,非只安全回滚)→ 或 rollback 经快照还原 → 死手到点全还原。

## 决策记录

- Rehijack apply = 路由-only 重落实(保设备),取代旧「teardown + Hijack」(后者删设备 → 必回滚 + 泄漏)。
- 新增 `platform.RehijackRoutes`(方案 A);`liveMutator` 去 teardown 指针;`run.go` 回退惰性捕获(更简单)。
- Linux 拆 `netConf` 步骤构建器(device vs route);darwin 加 compile-only `RehijackRoutes`(其 teardown 本就路由-only)。
- router 模式 rehijack = 明确不支持错误(YAGNI)。
- undo=nop 靠 9a 快照网(同 A2)。本设计取代 `2026-06-27-rehijack-real-mutator-design.md`;实现 fix-forward 覆盖 `f0317b4`/`ae485c6` 的错误 apply。

## 范围自检

单一可实现增量(netConf 步骤拆分 + 两平台 `RehijackRoutes` + liveMutator 去指针 + run.go 回退接线 + 纯单测),全 Mac 可测,真路由重落实仅 Rehijack、不碰 SetTransport。适合一份小 plan(3-4 任务)。
