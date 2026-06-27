# Rehijack 真 mutator(真 apply 接入)设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-27)。实现走单独的 plan。

## 背景与定位

A2 把控制面真 mutation 路由(`/v0/transport`、`/v0/rehijack`)+ MCP 改接 + 控制客户端全部交付,**生产挂 `nopMutator`**——agent→socket→arm→commit/revert 整条回路真,只「改动」为空(brick-safe)。2026-06-27 已在 VPS 203.0.113.20(Ubuntu 24.04 amd64,该机是 brook 服务器)真机端到端验证全绿:真 TUN+真隧道+真劫持 + 控制面 + A1 peer-cred + **真快照器 Restore 跑在 live table 100 上无损** + 死手 180s 准点还原。

唯一还 nop 的就是 mutator 的 **apply 真执行**。本刀把**两个 mutation 中较小的一个——`Rehijack`——的 apply 做真**:真正 teardown 当前劫持 + 重装劫持。`SetTransport` 的真换隧道(需改 dialer 热换 + 隧道生命周期,大且高风险)留**下一刀**。

**为什么 Rehijack 先行**:小、低风险、复用已被反复验证的 `plat.Hijack` 路径;在真机上端到端跑通「真 apply」机制的回路(arm→真改动→commit 保留 / rollback+死手经快照还原),给后续更大的 SetTransport 真换提供可靠的真实回归靶子。

## 目标 / 非目标

**目标**:`Rehijack()` 返回真 apply(`(*teardown)()` → `plat.Hijack(...)` → 收养新 teardown)+ nop undo(路由还原靠 9a 快照网);生产 mutator 从 `nopMutator{}` 换成 `liveMutator`(真 Rehijack + nop SetTransport);全 Mac 原生免 root 单测(fake platform);真机回归(真 `ip` 改动,死手兜底)。

**非目标(留下一刀)**:`SetTransport` 真换隧道(dialer `Proxy`/`Healthy` 原子热换 + 新隧道生命周期 + serveControl 隧道引用更新 + undo=换回旧 link);darwin 真机验证(本刀真机回归用 Linux VPS / netns)。

## 架构

### `liveMutator`(生产 mutator,替换 nopMutator)

```go
// liveMutator:真 Rehijack apply;SetTransport 仍 nop(嵌 nopMutator,下一刀替换)。
type liveMutator struct {
    nopMutator                 // 提供 nop SetTransport(方法提升)
    plat         platform
    tunH         tunHandle
    serverBypass []string
    userBypass   []string
    teardown     *func()       // 指向 run.go 的 teardown 变量(惰性捕获)
}

func (m *liveMutator) Rehijack() (apply, undo func() error, err error) {
    apply = func() error {            // 真改动发生在这(commit 路径,engine.Arm 持有)
        (*m.teardown)()               // 拆当前劫持
        td, err := m.plat.Hijack(m.tunH, m.serverBypass, m.userBypass)
        if err != nil {
            return err                // 引擎据此 Rollback(经 9a 快照还原)
        }
        *m.teardown = td              // 收养新 teardown
        return nil
    }
    undo = func() error { return nil } // 路由还原靠 engine.Arm 的 snapshotter.Restore
    return apply, undo, nil            // 方法体无副作用(A2 契约:不在体内改动)
}
```

- 嵌 `nopMutator` → `SetTransport` 自动得 nop;显式 `Rehijack` 方法遮蔽提升来的 nop 版本。
- **A2 无副作用契约**:`Rehijack()` 方法体只构造闭包、不调 `plat.Hijack`。原因同 A2——`engine.Arm` 在已 armed 时直接返回 `ErrAlreadyArmed` 而不运行 apply,任何在方法体内的改动都会绕过快照/undo。

### 惰性 teardown 捕获(run.go,方案 A)

控制面 socket(`serveControl`,注入 mutator)在 `run.go` 现于 `plat.Hijack`(teardown 诞生处)**之前**起——刻意保证「控制面先于劫持就绪」,故 mutator 构造时拿不到 teardown 值。解法:

1. 在 `serveControl` 之前 `var teardown func()` 声明(并提前算 `serverBypass := addrsToCIDRs(serverAddrs)`,`serverAddrs` 已在更早处得到)。
2. 构造 `&liveMutator{plat: plat, tunH: tunH, serverBypass: serverBypass, userBypass: cfg.Bypass, teardown: &teardown}`,传给 `serveControl` 取代 `nopMutator{}`。
3. `plat.Hijack` 处改赋值:`teardown, err = plat.Hijack(tunH, serverBypass, cfg.Bypass)`(`=` 不是 `:=`)。
4. `defer teardown()` → `defer func() { teardown() }()`,使 Run 退出时清理读到的是**当前** teardown(apply 可能已换新)。

`apply` 只在 commit 时运行(远晚于启动),那时 `teardown` 已被 `plat.Hijack` 赋值,惰性指针读到的是有效值。

## 数据流

agent → `POST /v0/rehijack` → `requireRoot`(A1 peer-cred)→ `mut.Rehijack()` 拿 (apply, undo) → `engine.Arm(apply, undo)`:
- 捕获路由快照(pre-apply 的 rules+table 100)
- 运行 `apply`:真拆劫持 + 真重装劫持(live 路由)
- 200 `armed`
- **commit** → 清死手、保留新劫持;
- **rollback / 死手到点** → `undo`(nop)+ `snapshotter.Restore`(flush+replay table 100 回 pre-arm 态,今日已在真机验证无损)。

## 错误处理

- `plat.Hijack` 中途失败 → `apply` 返 err → 引擎 Rollback(快照还原)→ 路由回 pre-arm;最坏由死手兜底,同今日。
- 已 armed → `/v0/rehijack` 返 409(沿用 A2,方法体无副作用保证 already-armed 路径安全)。
- 非 root POST → 403(A1)。

## 测试策略

**Mac 原生,免 root(fakePlatform)**:fake 实现 `platform` 接口,`Hijack` 记录调用次数/参数并返回 sentinel teardown 闭包(置标志)。断言:
1. `Rehijack()` 方法体**零** `Hijack` 调用(无副作用契约);
2. `apply()` 先调旧 teardown(置标志)、再调 `plat.Hijack`、并把 `*teardown` 换成新 sentinel;
3. `undo()` 是 nop(返回 nil、无调用);
4. `apply` 在 `plat.Hijack` 返错时透传该错、且**不**覆盖 `*teardown`(保持旧值,便于快照网接管)。
5. 嵌入校验:`liveMutator` 的 `SetTransport` 返回 nop(apply/undo 均 nil-返回)。

**回归**:既有控制面/引擎/MCP 测不受影响;两平台编译;`GOOS=linux go vet -tags integration`。

**真机回归(B 阶段,Linux VPS / netns,死手兜底)**:部署后 `bx run --test-timeout`,经 `curl --unix-socket` 验真 apply:先人为破坏劫持(如 `ip rule del pref 200`)→ `POST /v0/rehijack`(root)→ armed 200 → 观察劫持被真实重装修复(pref 200 + table 100 回来)→ commit 保留 / 或 rollback 经快照还原 → 死手到点全还原。证明 apply 真做了 teardown+rehijack,而非 nop。

## 决策记录

- 本刀只做 Rehijack 真 apply;SetTransport 真换隧道(改 dialer 热换)留下一刀。
- 生产 mutator:`liveMutator`(嵌 nopMutator 得 nop SetTransport,override 真 Rehijack)。
- teardown 惰性指针捕获(方案 A):`var teardown func()` 早声明 + `&teardown` 注入 + `defer func(){teardown()}()` 当前值清理。不重排 serveControl/Hijack 顺序(保「控制面先于劫持」)。
- Rehijack undo = nop;路由还原全靠 9a 快照网(`engine.Arm` 的 `snapshotter.Restore`),与 A2 决策一致。
- 真机回归用 Linux(VPS 203.0.113.20 / netns);darwin 真机验证另计。

## 范围自检

单一可实现增量(`liveMutator` 类型 + run.go 惰性捕获接线 + fakePlatform 单测),全 Mac 可测,真 apply 仅 Rehijack、不碰 SetTransport/dialer。适合一份小 plan(2-3 任务)。
