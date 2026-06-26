# Task 9b-1 — 守护进程 commit-confirmed 引擎设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-26)。实现走单独的 plan。

## 背景与定位

Task 9b(让 MCP 改动类 ops 真正操作 bx)拆三块:**9b-1 守护进程 commit-confirmed 引擎** · 9b-2 控制 socket 命令化 + MCP 工具变 socket 客户端 · 9b-3 真实隧道 mutation(set_transport/restart_tunnel,需真机)。本 spec 只做 **9b-1**。

**为什么 9b-1 先做**:① 它是 9b-2/9b-3 的地基——所有命令最终落到这个引擎 arm/commit/revert;② 现在就能 netns 真机验;③ 不碰隧道子进程、不依赖真机;④ 它把 9a 真快照器**首次接进 live 死手路径**(围绕受控合成 mutation,brick 不了真东西)。

**为什么必须把死手搬进守护进程**:Tasks 1-8 的死手 Guard + tickLoop 跑在**短命的 `bx mcp` 进程**里。一旦 agent `bx_setup` 后断开、MCP 进程退出,tickLoop 就死,"agent 断开→自动回滚"承诺落空。引擎是常驻 supervisor 的资产,故 agent 断开后死手仍在。

## 目标 / 非目标

**目标**:实现一个常驻守护进程的 commit-confirmed 引擎单元——`confirm.Guard`(死手)+ `confirm.Snapshotter`(9a 真快照器)+ tickLoop 的薄编排,提供 `Arm(apply,undo)`/`Commit`/`Rollback`/`Run(ctx)`。纯编排免 root 单测 + netns 真快照器往返验证。

**非目标(留 9b-2/9b-3)**:挂进 `Run()` 守护循环;控制 socket 命令化;MCP 工具变 socket 客户端;真实隧道 mutation(set_transport/restart_tunnel);改 `newSystemSnapshotter()` 让 MCP live 路径用它。9b-1 只交付引擎单元 + 测试,**不接任何生产 mutation / 不挂 Run()**。

## 架构:引擎单元

- **位置**:`internal/supervisor/mutationengine.go`。薄编排类 `mutationEngine`,持有 `*confirm.Guard`(240s,时钟可注入)+ `confirm.Snapshotter`。
- **构造**:`func newMutationEngine(snapper confirm.Snapshotter, window time.Duration, now func() time.Time) *mutationEngine`。生产用 `NewSystemSnapshotter()` + 240s + `time.Now`;测试注入 fake。
- **复用**(不重造):`confirm.Guard`(死手状态机,已纯测)、`NewSystemSnapshotter`(9a,已 CI 真机验)。新代码只有这层编排 + tickLoop。

### API

```go
// Arm:抓快照 → 武装死手(restore = undo + 快照网)→ apply。
//   capture 失败 → 不武装、不 apply、返回错误(沿用 ArmWithSnapshot 约定)。
//   apply 失败 → 立即 Rollback(undo+快照网)+ 返回 apply 错误(不留半截)。
//   成功 → 返回 nil(已武装,等 Commit;240s 内不 Commit 则 Tick 自动 revert)。
func (e *mutationEngine) Arm(apply, undo func() error) error

func (e *mutationEngine) Commit() error    // Guard.Commit:解除死手
func (e *mutationEngine) Rollback() error  // Guard.Rollback:立即跑 restore(undo+快照网)
func (e *mutationEngine) Run(ctx context.Context) // tickLoop:每 2s Guard.Tick,未 commit 到点自动 revert
func (e *mutationEngine) State() confirm.State     // 透传,便于测试/诊断
```

### revert 闭包(你选的"语义 undo + 路由快照网")

```go
snap, err := e.snapper.Capture()
if err != nil { return err } // capture 失败:不武装、不改动
restore := func() error {
    var errs []error
    if undo != nil { if e := undo(); e != nil { errs = append(errs, e) } } // ① mutation 语义 undo
    if e := e.snapper.Restore(snap); e != nil { errs = append(errs, e) }   // ② 9a 快照网,兜 undo 漏掉的路由
    return errors.Join(errs...)
}
if err := e.guard.Arm(restore); err != nil { return err } // 已武装则报错
if err := apply(); err != nil {
    _ = e.guard.Rollback()                                  // apply 失败 → revert
    return fmt.Errorf("apply 失败已回滚: %w", err)
}
return nil
```

### 与 `--test-timeout` 死手的关系

两套独立机制,不冲突:`--test-timeout`(run.go)= 整个 run 到点全还原并退出(整体保命);本引擎 = 单次 mutation 未确认就 revert 这一次、不退出(逐操作保命)。不同作用域,共存。

## 错误处理

- capture 失败 → `Arm` 返回错误,不武装不 apply(与 `confirm.ArmWithSnapshot` 一致)。
- apply 失败 → 立即 `Rollback`(undo+快照网),返回 apply 错误。
- `Commit`/`Rollback` 非 Armed 态 → 透传 `confirm.ErrNotArmed`(上层 9b-2 映射 MCP 错误码 ALREADY_COMMITTED/NOTHING_TO_ROLLBACK)。
- revert 内 undo 或 Restore 失败 → `errors.Join` 聚合返回(死手把"回滚也失败"上浮,9b-2 经命令回报给 agent)。

## 测试策略(守 TDD)

- **纯编排单测(Mac 原生,免 root)——覆盖大头**:注入 fakeSnapshotter(记录 Capture/Restore 调用)+ fake undo/apply(记录调用)+ fake 时钟。覆盖:
  - capture 失败 → 不武装、apply 未被调用。
  - apply 成功 → State Armed;Commit → State Committed,Restore 未被调用。
  - apply 失败 → 自动 Rollback:undo 与 Restore 都被调用,State Reverted。
  - 未 commit + 推进时钟 + Tick → 自动 revert(undo+Restore 调用)。
  - Rollback 透传 ErrNotArmed(idle 态)。
- **netns 集成测(`//go:build integration && linux`,CI/Colima)**:引擎接**真** `NewSystemSnapshotter()` + fake 时钟,`Arm` 一个合成路由 mutation(apply = 加一条 ip rule;undo = no-op),不 commit → 推进时钟 + Tick → 断言 `ip rule list` 真机回到 arm 前基线。复用 9a/harness 的 `unshare(CLONE_NEWNET)`+`LockOSThread`+dummy。
- **CI**:现有 `integration` job + SKIP 守卫自动接住。

## 决策记录

- revert 模型 = 语义 undo + 路由快照网(`Arm(apply, undo)`),前向兼容 9b-3 隧道 mutation。
- 引擎 = `confirm.Guard` + 9a `NewSystemSnapshotter()` + tickLoop 的薄编排,时钟可注入。
- 与 `--test-timeout` 两套独立死手并存。
- **不挂 Run() / 不接 socket / 不接 MCP / 不接真实隧道 mutation**(9b-2/9b-3)。9a 快照器在引擎死手里首次 live,但只围绕 netns 合成 mutation,无生产 mutation,brick-safe。

## 范围自检

单一可实现单元(引擎编排 + 纯单测 + netns 往返测),不挂守护循环 / 不接命令通道 / 不接生产 mutation。适合一份小 plan。
