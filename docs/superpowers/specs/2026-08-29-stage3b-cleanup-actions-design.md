# 阶段③b:调谐环拿到第一批执行权 —— 只有 desired=off 的清理动作

## 一句话

> 四个已命名动作里,**只授权 `restore_dns` 与 `clear_orphan_barrier`**(都住在
> `desired=off` 分支:用户明确要关,而系统上还挂着残留);`start_core` 与
> `stop_core` **留在观察态**,每个都有写死的理由。执行经 Manager 的互斥槽
> try-acquire,绝不与用户动作交错;每次执行进报告、进日志,statusdigest
> 穷举守卫逼着新字段自我分类。

## 为什么是这两个、为什么是现在

**证据面(③a spec 当初要求的 soak 数字,现在有了)**:
- 调谐环自 2026-08-10 起在项目所有者真机常驻,change-only 日志安静、退避阶梯
  实测正确;2026-08-29 当天 `bx status` 仍显示「无差异(连续 13 轮未变)」。
  **循环本身的判定质量有真机背书。**
- 这两个动作各自对应一次真实事故的形状:孤儿屏障(2026-08-04,71 分钟关不掉
  保护的同批产物,`/2` reject 压过一切打死整机)与 DNS 残留(强制拆除后
  networksetup 指着 127.0.0.1 而 Core 已死 ⇒ 整机断解析)。**它们发生时用户
  已经明确说了「关」**,替他清残留不违背任何意图 —— 这与「替他开保护」在
  风险上是两个物种。

**终局账本**:这两个动作拿到手,终局 spec 第 1 步点名的五条手写补偿里的两条
(「DNS 残留检查」「Guardian 侧孤儿屏障清理」)从「手写补偿」变成「同一个循环
的普通输出」,可以开删 —— 这是本期的可检验交付物,不是附带收益。

## 观察态的两个,理由写死(别在 review 里重新提议)

- **`stop_core` 不授权**:`desired=off` + Core socket 应答,最常见的真实来源是
  **`sudo bx run`(文档化的调试路径)**——一个每 30 秒杀一次你调试进程的
  调谐器是敌意软件。要授权它,先要有「这个 Core 是不是我们记账里的那一个」的
  判据(孤儿 Core vs 用户手起的 Core),那是 ③c 的题。
- **`start_core` 不授权**:decide 的注释已写明,`CoreSocket==False` 的语义是
  「socket 没应答」不是「没有 Core 在跑」——卡住但活着的 Core 会让这条提议
  出现,而按它起新 Core 正是 af81632 双 Core 的入口。授权它的前置(照 ③a
  设计原文):准入判据换 `scanRunningCores`、先解 Uncertain 锁存、且「有限次数」
  ——三样都不在本期。
- **「装屏障」这个动作依然不存在**,不是被推迟:瞬时网关探测失败会把它降级成
  block-only 整机黑洞,循环每拍重掷骰子(③a 设计原文,穷举守卫钉着白名单)。

## 机制

**判定层一个字不动。** `decide` 仍然纯、仍然产出全部四种提议;执行层新加一道
**授权名单**过滤:

```go
// reconcile_execute.go(新)
var executableActions = map[reconcileAction]bool{
    actionRestoreDNS:         true,
    actionClearOrphanBarrier: true,
    // start_core / stop_core:观察态,理由见 spec「观察态的两个」一节。
}
```

**执行的五条纪律**(每条一个测试,变异验证):

1. **互斥**:执行前对 Manager 的单槽 mutation channel 做 **try-acquire**
   (不排队);拿不到就整轮跳过并在报告里记 `skipped: mutation_busy` ——
   用户的 up/down 永远优先,调谐器绝不与它交错,也绝不让用户排在自己后面。
2. **一轮至多执行一个动作**,执行完立即结束本轮:下一轮先重新观测,按新事实
   再说。连发两个动作意味着第二个是按**执行前**的观测做的 —— 那是按陈旧事实
   行动,observe 整个包就是为了消灭它。
3. **执行结果只进报告与日志,不改判定**:`ReconcileReport` 新增
   `Executed {Action, Outcome, Error}`(上一轮执行了什么、成没成)。
   statusdigest 的嵌套穷举守卫会逼着这些新字段在
   exclusions/signals 里选边 —— Executed 是**真事件**,进 signals
   (它变了菜单就该醒);失败细节字符串若含时间戳则拆开,不许整字段排除。
4. **失败不升级、不放弃、靠退避自然重试**:动作失败记进报告,残留还在 ⇒
   下一轮照旧提议 ⇒ 退避阶梯(30s→10min)天然限频。不设「连败 N 次就停」——
   停了之后残留永久无人管,而那正是手写补偿时代的形状;10 分钟一次的可见
   失败是特性不是噪声。**但失败绝不 panic**:循环的 per-round recover 已在
   (③a),执行器不新增裸 goroutine。
5. **动作的实现复用既有原语,不新写系统操作**:
   - `clear_orphan_barrier` → `RemoveBlockingBarrierRoutes(ctx, nil)` ——
     逃生口同款、按定义不依赖所有权记账(孤儿恰恰没有记账);linux 版
     2026-08-29 已同批供货。
   - `restore_dns` → Manager 的 `restoreDNS`(带 dnsStatus 缓存与
     needsAttention 语义)而不是裸调 install —— 状态发布要跟着动。

**执行不新开授权面**:动作由 root 的 Guardian 进程自己做,与今天 Down 路径
做同样的事,没有新的本地 API、没有新的对外端点。

## 日志与可观测

- 每次执行一行:`guardian_reconcile_executed action=… outcome=ok|failed err=…`
  (紧邻既有的 `guardian_reconcile_would`,读日志的人在同一屏看到「想做什么」
  与「做了什么」)。
- `guardian_reconcile_would` 保持原样 —— 观察态动作(start/stop core)仍只
  出现在 would 行里,授权名单外的动作**永远**不出现在 executed 行里,这条
  由测试钉住(白名单反向断言,与 ③a「不许有装屏障动作」同款穷举)。
- `bx status` 的 Reconcile 面板加一行「上轮执行」——只在 Executed 非空时
  出现(健康机器静默,与整个面板同一条纪律)。

## 交付判据

1. 两条手写补偿开删:CLI/Guardian 里凡「检查 DNS 残留」「清孤儿屏障」的
   **Guardian 侧**主动路径,删除或改为读调谐报告(CLI 逃生口那份**留**,
   按不变量它必须在 Guardian 已死时可用);
2. netns 集成台(待 linux 运行时恢复后)加两条断言:desired=off + 手工装上
   孤儿 pref-120 rule ⇒ 若干轮后被清掉;darwin 真机 soak 一次
   (关保护、手工 networksetup 设 127.0.0.1、看循环在退避窗口内还原);
3. 穷举守卫全绿:动作白名单、Executed 字段分类、`skipped: mutation_busy`
   不被读成栅栏。

## 不做

- 不授权 start_core/stop_core(理由见上,写死);
- 不加配置开关/环境变量门(没有热重载,开关只会变成第二份真相;要停它,
  发一版把白名单清空 —— 与「能力由代码声明」同一条);
- 不动五道栅栏的语义与次序;
- 不动 CLI 逃生口。
