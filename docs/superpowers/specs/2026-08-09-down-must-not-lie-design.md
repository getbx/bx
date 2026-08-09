# `bx down` 不许在保护还开着时报成功

## 问题

`Manager.Down` 有**两条**路可以返回 `ProtectionOff`,而两条都没有向系统求证过:

1. `manager.go:483` —— `desired == DesiredOff && m.current.PID == 0` 时直接返回
   `ProtectionOff`,连屏障都不装。
2. `manager.go:493-536` —— `m.current.PID == 0` 时用 `m.runner.Existing(ctx)` 找既有 Core;
   `Existing` 读的是 `/var/lib/bx/core-process.json`,**文件不存在就返回 PID 0**
   (`process.go:391-393`),于是 `if process.PID != 0 { runner.Stop(...) }` 整个被跳过。

两条都以「我的记账里没有 Core」推出「没有 Core」。而这个推理有至少四种反例:

- **legacy Core** —— 旧 launchd unit 起的,Guardian 从没为它写过记录。
- **`sudo bx run`** —— 项目自己文档化的调试路径。
- **`core-process.json` 丢失或陈旧** —— 手删那个文件曾经是本项目文档推荐的应急手段。
- **Guardian 重装/迁移之间的窗口** —— 记录属于上一任 Guardian。

四种情况下 `bx down` 都会:装屏障(或不装)、写 `desired=off`、还原 DNS、**报告成功**,
而那个 Core 连同它的 TUN 和路由一动没动。**用户被告知「已停止」,保护其实还开着。**

## 为什么 CLI 侧修不掉

2026-08-09 的阶段② 在 CLI 里加过一个 legacy 探针。它只覆盖了四种反例中的一种,
而且**根本覆盖不到主要入口**:菜单栏的 Turn Off 从阶段① 起就直接
`POST /v1/down`(`GuardianClient.swift:252-254`),一行 CLI 代码都不经过。

**判断必须发生在持有事实的那一侧。** 而 Guardian **已经有那个原语**:
`scanRunningCores`(`procscan_darwin.go:101`)按
`basename(可执行路径或 argv[0]) == "bx" && argv[1] == "run" && uid == 0` 判定
(`looksLikeCore`),**与是谁把它拉起来的无关** —— 正是为「向系统现问,不信自己的
记账」写的。它今天只被 `ExecCoreRunner` 用在启动标记那一跳,`Manager.Down` 从没问过。

## 两条不变量在「问不出来」时冲突,而出口是第三个状态

- 「**停止永不依赖别的先成功**」(2026-08-04,用户 71 分钟关不掉保护)说:扫描失败
  不许让 `bx down` 失败。
- 「**绝不在保护还开着时报成功**」说:扫不出来就不能说 off。

出口是 `ProtectionNeedsAttention`(`manager.go:21`,**已存在**,CLI 与菜单都已经认它):

| 扫描结果 | `Protection` | 理由 |
|---|---|---|
| 没有 Core 在跑 | `ProtectionOff` | 求证过了,可以说 |
| **有** Core 在跑 | `ProtectionNeedsAttention`,reason `core_still_running` | 拆除做了能做的,但保护没关掉 —— 这是事实 |
| 扫不动 / 不支持 | `ProtectionNeedsAttention`,reason `core_scan_failed` | **观测不到 ≠ 观测到没有**;不失败,也不撒谎 |

**关键取舍:扫描失败绝不让 `Down` 返回 error。** 拆除的每一步照常做完,只是最后
那句话改成「没能确认」。把它做成失败会直接违反 71 分钟那条不变量。

## 设计

### 一、注入点

`Manager` 通过 `CoreRunner` 接口拿不到 `ExecCoreRunner.scanCores`。加一个可选接口:

```go
// coreScanner 由能向系统求证「有没有 Core 在跑」的 runner 实现。
// 可选:实现不了的 runner(测试替身、将来别的平台)会让 Manager 落到
// 「扫不动」那一支 —— 不失败,但也不再声称 off。
type coreScanner interface {
    ScanRunning() ([]Process, error)
}
```

`ExecCoreRunner` 已有 `scanCores()`,加一个导出的薄包装即可。
`Manager` 用类型断言取它,**不新增第二个注入点** —— 两个注入点会走漂,而走漂的
那一天差别正好是这条不变量。

### 二、调用时机:停完之后,不是停之前

扫描放在**所有拆除动作做完之后**、设置最终 `Status` 之前。理由:
- 停之前扫没有意义 —— 我们本来就要停它。
- 停之后扫回答的才是要报告的那件事:**现在还有没有?**

**必须处理「刚被 SIGTERM、还没退干净」的窗口。** `scanRunningCores` 已经跳过僵尸
(`isZombieProcess`),但收到 SIGTERM 正在跑 defer 的进程还不是僵尸。所以:扫到有
Core 时**短暂等待后重扫一次**再下结论;仍在就报 `needs_attention`。
上限要小(秒级),它挡在用户和「关掉了没有」这个答案之间。

### 三、两条路都要过这一关

`manager.go:483` 那条提前返回同样要走。它今天是「desired 已经是 off 且我记账里没
Core」——**两个前提都是记账**,而这条不变量正是关于记账不可信的。

### 四、状态要说清是哪一种

`needs_attention` 今天在 CLI 与菜单里都会渲染,但 reason 必须能区分
`core_still_running`(确知还在)与 `core_scan_failed`(问不出来)——
对用户,前者是「还没关掉」,后者是「没能确认」,下一步不同。
沿用既有的 `needsAttention(desired, reason)` 通路。

## 本期不做

- **不改 CLI 的 legacy 探针**。它在 Guardian 这条修好之后是冗余的,但删它是另一个
  改动,而且删之前要确认 Guardian 侧真的覆盖了(`bx down` 与菜单两条都要)。
- **不改菜单**。它读 `protection_state`,新状态自动生效。
- **不动 `Up`**。
- **不做非 darwin 实现**。`procscan_other.go` 恒返回错误,于是别的平台恒落到
  「扫不动」那一支 —— 而 Guardian 今天只有 darwin 实体(`requireDaemonPlatform`),
  所以这不是真实退化。**但要在注释里写明**:谁移植 Guardian,必须先实现
  `scanRunningCores`,否则 `bx down` 会恒报 needs_attention。

## 风险

**最大的一条:误报。** `looksLikeCore` 按 `argv[0]=="bx" && argv[1]=="run" && uid==0`
判定,**刻意不匹配可执行路径**(升级后旧版 Core 跑在 `runtime/<旧版本>/bx`,按路径
匹配会漏认 —— 而漏认的代价是灾难,过度匹配的代价只是拒绝)。在 `Start` 那一侧
过度匹配 = 拒绝启动(安全);**在 `Down` 这一侧过度匹配 = 报 needs_attention 而其实
已经关掉了**(烦人但不危险)。方向仍然是对的,但要在真机上看一眼误报率。

**第二条:每次 `Down` 都枚举全部进程。** `Up` 那一侧本来就在做同样的事
(`Start` → `refuseUnrecordedRunningCore`),所以代价已经在付。

**第三条:这是 71 分钟事故那段代码。** 任何改动都必须保证:扫描的任何失败模式
(报错、超时、panic)都不能让 `Down` 返回 error,也不能让它提前返回而跳过拆除步骤。
测试要专门钉住这一条。

## 测试策略

- **纯行为**:注入一个「扫到有 Core」的 scanner,断言 `Down` 返回的 `Protection`
  是 `needs_attention` 而非 `off`,**且拆除步骤全都跑过了**(屏障、DNS、desired=off)。
- **扫描失败**:注入报错的 scanner,断言 `Down` **不返回 error**,`Protection` 仍是
  `needs_attention`,reason 是 `core_scan_failed`。
- **两条路**:`manager.go:483` 那条提前返回单独一条用例。
- **不实现 coreScanner 的 runner**:落到「扫不动」那一支,不 panic、不失败。
- **回归**:健康机器(扫不到 Core)仍然返回 `ProtectionOff` —— 否则每个人的
  `bx down` 都会变成 needs_attention,这条修复就成了新的噪声源。
- **变异**:把「扫到有 Core 仍报 off」放回去,上面第一条必须红。
