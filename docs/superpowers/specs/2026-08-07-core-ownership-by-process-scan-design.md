# 用进程扫描判定 Core 所有权设计

## 背景

`bx up` 有一条**只能靠 `bx uninstall` 脱身**的死锁,是这个产品里最坏的一类缺陷。

Guardian 在 fork Core 之前先落一个 `launching` 标记(PID 为 0)。如果在「fork」与
「把子进程 PID 写进标记」之间出事——Guardian 被杀、写盘失败——盘上就留下一个
`launching`。此后每次 `bx up` 都撞 `Existing()`(`process.go:268`,`launching` 分支的拒绝在 `:290`)
与 `refuseLiveLaunchMarker`(`:224`)的无条件拒绝,报
`core_ownership_uncertain`,永久失败。

真机两次:2026-08-05 与 2026-08-06,两次都是用户反复重装、最后卸载才脱身。

### 为什么之前两次都没解决

**`af81632`(已回退)** 的判据是「`launching` 标记 PID 恒为 0,结构上不可能对应任何
活进程」。复审推翻:那是范畴错误。`PID==0` 不是「没有进程」,是「连 PID 都没有、
**无法向 OS 求证**」——最强的不确定。按该判据放宽会造成真实双 Core。

**`e7e413c`(两段式标记,已合入)** 让 fork 一返回就落一条带 PID 的 `spawned` 记录,
把窗口从「fork → Inspect → verify → 写 owned」缩到「fork → 一次写盘」。**但没让
`launching` 变安全**:`spawned` 那次写盘**本身**失败时(磁盘错误),fork 已经发生而
盘上仍只有 `launching`。既有测试
`TestExecCoreRunnerPersistenceFailureLeavesDurableUncertainLaunchMarker` 当场证伪了
「放宽 launching」的尝试。

**共同的结构性原因**:两次都试图从**自己的记账**里推断有没有 Core。而记账正是出
问题的那个东西——用它去判断它自己是否可信,是循环的。

## 设计

### 核心判据

**不问记账,问系统:有没有进程在跑我们的 Core。**

这个判据能成立的原因是决定性的:**`fork` 一返回,子进程就已经作为一个进程存在了
——早于它执行我们的任何一行代码**。所以进程扫描是唯一在「fork 与写盘之间」那个
窗口里仍然有效的手段。写盘失败、Guardian 被 SIGKILL、标记停在 `launching` —— 都
不影响它。

这与 `internal/observe` 是同一条原则:**向系统现问事实,而不是相信内存/磁盘里的
记忆**。

### 匹配条件(这里有个陷阱)

**不能用「可执行路径 == 我当前的 Core 路径」。** 更新之后,旧版 Core 跑的是
`runtime/v0.2.7/bx`,而当前 Core 路径是 `runtime/dev/bx`。一个**还活着的旧版 Core
会被判成「不存在」**,于是起第二个 —— 正是 `af81632` 被回退的那个双 Core 风险换了
个入口。

判据取:

```
basename(executable) == "bx"   且   argv[1] == "run"   且   uid == 0
```

**刻意偏向过度匹配。** 多认出来的后果是 fail-closed(拒绝启动),方向安全;漏认才会
起双 Core。用户手工 `sudo bx run` 也会被认出来 —— 而那本来就是一个 Core,认出来
是正确的。

### 接线

| 位置 | 现在 | 改后 |
|---|---|---|
| `Existing()` 的 `launching` 分支(`process.go:290`) | `launching` → 无条件 uncertain | 扫描:无 Core → 清标记、当作无既有 Core;有 → uncertain 并**报出该 PID** |
| `refuseLiveLaunchMarker`(`:224`) | `PID<=0` → 无条件拒绝 | 同上判据 |

`spawned` 与 `owned` 两种状态的既有处理**不变**(它们有真实 PID,`inspectProcess`
已经能向 OS 求证)。

### 扫描失败怎么办

**保持 uncertain、fail-closed。** 「问不出来」不等于「没有」—— 这条今天刚在 EIO 那
里踩过:`SysctlKinfoProc` 对已死进程返回 EIO 而非 ESRCH,把「问不出来」当成任何确定
结论都会出事。

### 平台

darwin 实现 + `!darwin` 桩。桩**保持现有的无条件 fail-closed**,不改行为。

这套标记机制在实践中只在 macOS 存在:Guardian 只有 darwin 实体
(`daemon_darwin.go` 有实现,`daemon_other.go` 是桩),`ExecCoreRunner` 只被
Guardian 守护进程创建(`daemon.go:337`)。Linux 上 Core 就是 systemd 服务本身,
没有这条路径。

### 附带收益:错误信息第一次能区分两种情况

现在无论哪种情况都是同一句:

```
Core process ownership is uncertain: durable launch marker has no accepted process record
```

改后要么自愈(用户根本看不到错误),要么明说「PID 12345 在跑 Core,身份不认识」。
真机那次事故里这两种情况的处置完全不同,而错误信息一个字都没区分。

## 本期不做

- 不碰两段式标记(`spawned`)—— 它已经把窗口缩小了,保留。
- 不碰 `Start()` 其余的 fail-closed 判定(记录里进程还活着、身份不匹配等)。
- 不碰 Linux / Windows。
- 不做「boot 代际」方案 —— 进程扫描是它的超集(跨 boot 的进程当然扫不到)。

## 注

本文引用的行号取自 2026-08-07 的 `internal/guardian/process.go`,会随改动漂移;
以函数名为准。

## 已知局限

扫描是一个**时间点**的观测。理论上存在竞态:扫描时没有 Core,随即另一个 Guardian
fork 出一个。但 `acquireMutation` 已经把 Guardian 内部的并发串行化了,而跨 Guardian
实例的竞态在本设计之前同样存在(且更糟:那时连扫描都没有)。不引入新的竞态面。
