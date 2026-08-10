# 阶段③:调谐循环 —— 先只观察,再逐项授权

## 先更正架构文档对本阶段的设定

`2026-08-08-control-plane-architecture-design.md` 说阶段③做完之后,五条手写补偿
「可以逐个删除 —— 它们会变成同一个循环的普通输入」。**测绘证明其中三条不成立**,
而且不成立的方式各不相同:

| 补偿 | 架构文档 | 实际 |
|---|---|---|
| DNS 残留检查 | 可删 | **确实可删**。观测原语有、动作幂等、分歧规则(`diverge.go:52-60`)都写好了 |
| `bx status` 的 believed/observed 并列 | 可删 | **按定义可删**,但有两个前提(见下) |
| 升级欠条 | 可删 | **不可删** —— 一个忠实的循环读到 `desired=off` 就会收敛到 off,**而那正是 bug 本身** |
| 孤儿屏障清理 | 可删 | **只能删一半** —— Guardian 侧可以,CLI 侧那份必须留(逃生口的定义就是「Guardian 已死时可用」,那时没有循环在跑) |
| 孤儿启动标记 / 进程扫描 | 可删 | **反而更承重** —— 它不是「修复陈旧状态」,是**准入控制**,防的是两个 Core 同时跑。循环若按时钟起 Core,它每一轮都要跑 |

**这三条不是执行细节,它们改变了本阶段该做什么。**

### 升级欠条为什么不可删

它存在的原因是:升级把「停下 Core 换二进制」表达成了 `desired=off`。于是磁盘上写着
用户不想要保护,而事实是用户想要、只是此刻不能有。**任何忠实的调谐器都会把它收敛到 off。**

删掉它需要的不是循环,是**数据模型改动**:引入一个与 `desired` 正交的
**维护挂起(maintenance hold)**——带原因与过期时间,调谐器尊重它但**永不覆盖 desired**。
做成之后,欠条文件、`?reason=upgrade` 参数(`localapi.go:123`)、请求作用域的标记
(`upgradeintent.go:22-49`)、以及 CLI 那份重复销账(`cli/guardian.go:736`)**一起消失**。

**这是阶段③的前置改动,不是它的产出。**

### `Uncertain` 的锁存会让循环永不收敛

`m.current.Uncertain` 一旦置上,`upLocked`(`manager.go:430-433`)与 `Migrate`(`:353-356`)
在**重新扫描之前**就短路返回。那是刻意的 fail-closed 判断(「拒绝的代价是启动不了,
赌错的代价是两个 Core」),**不该由调谐器来清**。

后果:循环撞上这个锁存之后,每一轮都会做同一件不可能成功的事。**循环必须把它当成
「什么都不做」,而不是「继续收敛」** —— 并且这意味着**锁存态需要一个用户可见的出口**,
今天那个出口是 `sudo bx down && sudo bx up`,而它没有出现在任何地方。

## 最危险的那件事,先说清楚

**绝不能让屏障的安装/拆除由时钟驱动。**

`PlanBarrier`(`barrier.go:53-78`)装的是 4 条 IPv4 + 4 条 IPv6 的 `/2` reject 路由,
它们比 Core 的 `/1` split-default **更长**,按最长前缀匹配压过一切(`barrier.go:120-127`)。
两条与时钟特别相关的失效路径:

1. `barrierContextForRuntime` 失败、或产出空 bypass 时,调用方降级为
   `blockOnlyRecoveryContext`(`manager.go:584-587`、`:1018-1024`)。**block-only 意味着
   一条 server bypass 都没有 —— 整机黑洞,而且隧道没有任何路可以自愈。** 一次
   `route -n get default` 的瞬时失败(`barrier_darwin.go:25-31`)就够了,**而循环每一拍
   都在重掷这个骰子**。
2. 屏障的所有权只在内存里(`m.barrierOwnership`,`manager.go:190`)。每一拍装屏障
   都在加宽「Guardian 此刻死掉 → reject 路由留在内核里」的窗口。

**其它每一处失误用户都还能自救;一条 `/2` reject 路由被一个随后死掉的进程留下,
用户连修复包都下载不了。**

第二危险的是 `handleUnexpectedExit`(`manager.go:954-1009`):它从**磁盘**读 desired
并据此重启 Core。`forcedMacOSTeardown` 之所以把 `persistDesiredOff` 排在第一步
(`cli/guardian.go:600-604`),正是为了拆掉这颗雷。**调谐循环是这个竞速者的第二个、
更快的副本** —— 今天它是边沿触发的,循环会让它常驻武装。

## 设计:分两步,第一步不许动手

### 阶段③a:只观察的调谐器

一个周期性循环,计算 desired 与 observed 的差,**把「我本来会做什么」记进日志并发布**,
但**一个动作都不执行**。

它交付三样东西:

1. **一个纯函数** `decide(desired, observed, fences) []action`,与平台无关、可在任何平台
   单测。这是本仓库对付「syscall 那一半造不出来」的既有手法 —— `decideCoreScan`
   (`procscan.go:21-38`)与 `failoverPolicy.decide`(`failover.go:26`)都是这个形状,
   而且 `decideCoreScan` 的注释明说抽出来就是为了让判据可测。
2. **真机上的误报率**。这是唯一能拿到它的办法。`2026-08-09-down-must-not-lie-design.md:107-111`
   已经标了 `looksLikeCore` 的误报率**从未测量**,而阶段③要在它之上再叠一层判断。
3. **`bx status` 的 divergence 从「一次快照」变成「持续观察」** —— 今天那个字段只在用户
   敲命令的那一刻算一次。

**为什么必须先只观察**:Guardian 只在 darwin 上跑(`daemon.go:377`),而集成台是
Linux netns 的(`harness_netns_linux_test.go` 的 build tag),**它测不到 Guardian 侧的循环**。
每一个循环会驱动的原语在非 darwin 上都是硬报错的桩(`barrier_other.go`、`procscan_other.go`、
`route_lookup_other.go`)。也就是说阶段③的验收清单里凡是写「由测试保证」的,对
Guardian 的循环而言实际是「由替身保证」。**唯一能拿到真实证据的办法,就是让它先跑一段
只说不做的日子。**

### 阶段③b:逐项授权,从最安全的那一项开始

按危险程度排序,**只授权最上面那一项,soak 过再往下**:

| 授权项 | 幂等? | 失效后果 | 建议 |
|---|---|---|---|
| **DNS 还原** | 是 | 系统 DNS 指向一个不存在的解析器 —— 用户可自救 | **先给这一项** |
| 清孤儿屏障(只删不装) | 是(「不在表里」被容忍,`barrier_darwin.go:94-96`) | 删错 = 少一层保护,不会断网 | 第二项 |
| 起 Core | **否** —— 撞上 `Uncertain` 锁存会永久拒绝 | `bx up` 从此失败且无用户动作参与 | 需先解锁存 |
| 停 Core | 否 | 与用户的 `down`/升级抢时序 | 需先有维护挂起 |
| **装屏障** | 否 | **整机黑洞,用户下不了修复包** | **不授权。留给用户发起的路径** |

### 循环必须遵守的四条(测绘直接给出的)

1. **`acquireMutation` 用短 ctx,拿不到就安静跳过**,绝不重试风暴。channel 唤醒是 FIFO 的,
   硬重试的循环会把用户的 `up` 挤过 60s 预算变成 `guardian_busy`。
2. **`pathRecoveryFences > 0` 时整轮跳过**(`manager.go:211`)。
3. **`recoveryBlocked` 为真时「什么都不做」,不是「继续收敛」**。
4. **绝不由循环去清 `m.current.Uncertain`** —— 那是一个存在意义就是「拒绝」的 fail-closed 判断。

更便宜且更明确的做法:加一道与 `beginPathRecoveryTransition` 同形的栅栏,
由每一次用户发起的改动升起,循环在**取任何锁之前**先看它。

## 本期不做

- **不给循环任何执行权**(那是③b)。
- **不动升级欠条**。删它需要维护挂起那个数据模型改动,单独一期,而且要排在③b 之前。
- **不解 `Uncertain` 锁存**。同上,单独一期;它今天的出口(`down` 再 `up`)先写进文档。
- **不删 CLI 侧的孤儿屏障清理**。逃生口按定义在 Guardian 已死时工作。
- **不动 `bx status` 在 Linux/Windows 上的形态**。那两个平台**一个观测原语都没有**,
  「status = observed」在那里等于「status 全是 Unknown」。

## 风险

**最大的一条:观察本身也要花钱。** 一整轮观测在 darwin 上是 **6 次进程 fork**
(capture 2、barrier 1、DNS 3)加上 `scanRunningCores` 的约 900 次 syscall。
今天这些只在用户敲 `bx status` 时发生一次;循环会让它按拍发生。**周期必须保守**
(建议 ≥30s,并且在观测结果连续相同时退避),且**观测不许持有 mutation channel**。

**第二条:只观察的循环会产生噪声,而噪声会训练人忽略它。** 这正是
`cli/cli.go:4288-4295` 那道 darwin 门存在的理由 —— 五条常驻的「无法观测」divergence
会让用户和 agent 学会忽略这个字段。所以③a 的日志必须**只在差异发生变化时打印**,
不是每拍一行。

**第三条:`RuntimeState.RoutesInstalled` 是个谎。** 它是 `atomic.Bool`,在 Hijack 之后
置一次(`run.go:650`)、teardown 时清掉(`:652`),**从不复查**,而 `health.go:134` 把它
当真。调谐器**绝不能**把它当观测——那是「信自己的记账」的典型。要么用真实的路由观测
(`observe/observer.go:61-89`),要么标 Unknown。

## 测试策略

- **纯函数 `decide`**:全平台单测,表驱动。每一条 desired×observed 组合都要有用例,
  尤其「观测不到」那一列 —— 它必须产出「什么都不做」,不是「按 Unknown 收敛」。
- **栅栏**:循环在 `pathRecoveryFences > 0` / `recoveryBlocked` / `errMutationBusy` 三种
  情况下都必须安静跳过。三条各一个变异。
- **不许有动作**:③a 阶段,`decide` 的输出**只进日志**。加一条守卫断言循环的执行路径上
  没有任何 mutating 调用 —— 用注入的 fake 断言调用次数为 0,不是源码文本匹配。
- **观测成本**:一条测试钉住一轮观测的 fork 次数上限,防止有人往里加一个便宜看起来的探针。
- **真机 soak(用户执行)**:开着只观察的循环跑一天,统计 divergence 的出现频率与内容。
  **这是本阶段唯一真正的验收**,其余全是替身。

## 与阶段②、与「down 不许撒谎」的关系

「down 不许撒谎」那一轮已经把**调谐的形状**引进来了:`Down` 与 `Recover` 在报告
`ProtectionOff` 之前向系统求证。那是**事件驱动的一次求证**,不是循环。阶段③a 把同一
条纪律变成常驻观察,而③b 才开始让它动手。

顺序不能反:**先有诚实的观测,再有基于观测的动作。** 今天的 `Down`/`Recover` 已经证明
了前半句可行,而它们暴露的每一个 bug(消费方没跟着改、panic 收错地方、改动落在旁边)
都会在循环里被放大成每拍一次。
