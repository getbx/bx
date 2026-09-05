# 阶段③c:调谐环拿到 `start_core` 的执行权 —— 有准入、有封顶、不碰屏障

## 一句话

`desired=on` 而 Core 的控制 socket 不应答时,调谐环**向系统求证没有 Core 在跑**之后,
用 `bx up` 同一条原语把它起回来;每段故障最多 5 次,过了只观察。`stop_core`、
「重启卡住的 Core」、「装屏障」三样仍不进循环。

## 要修的空洞,精确到一条路径

Core 意外退出时 `handleUnexpectedExit` 先装屏障(fail-closed)再重启**一次**;那一次
失败(`core_restart_failed`)之后**没有任何东西再试**,机器停在 Blocked(屏障装上了)或
needs_attention(屏障也没装上)直到有人敲 `sudo bx up`。启动恢复有自己的 5 秒重试
(`retryDaemonRecovery`,只管 Guardian 刚起来那一段),用户 `bx up` 有自己的重新求证,
唯独「保护开着、Core 死了、第一次重启没成」这个形状是无人区。③a 起循环每轮都提议
`start_core`,③b 把它留在观察态,理由写死在 ③b spec:准入判据不能是 socket、要先解
Uncertain 锁存、要有限次数。三样在本期各有处置,见下。

这两天三次真机故障(NAS 静默一个月、休眠成环 13 分钟、Blocked 假标签)的共同形状是
「悄悄坏掉,靠人发现」。本期是把其中「Core 没在跑」这一种从靠人变成系统自己收敛。

## 准入:向系统求证,不信 socket

`decide` **一字不改**:`CoreSocket==False` 仍然只回答「该不该考虑起它」。执行前在互斥
槽里现扫一次 `scanRunningCores`(与 `runner.Existing/Start` 用的是同一个判据
`looksLikeCore`),三态各有处置:

| 扫描结果 | 处置 | 发布的码 |
|---|---|---|
| 测成,0 个 | 起 | —(成功 `ok` / 失败 `execute_failed`) |
| 测成,≥1 个 | **不起**。那是卡住但活着的 Core,起第二个正是 af81632 双 Core 的入口 | `core_process_present` |
| 没测成 | **不起**。「问不出来」不是「没有」 | `core_scan_failed` |

这只是第一道门。起 Core 走 `startCoreLocked`,它里面 `runner.Start` 既有的 fail-closed
准入(盘上记录向 OS 求证 / 孤儿标记求证 / 无记录也扫描)一道不拆 —— 循环起 Core 与
用户 `bx up` 过的是同样的门。

**Uncertain 锁存本期不解、也不由循环清**(③a 第 4 条不动)。`startCoreLocked` 撞上
所有权不确定会 `retainUncertain`,下一轮 `heldBy` 报 `ownership_uncertain` 让路,出口仍是
用户 `bx up` 的重新求证(2026-08-11 那一期)。这意味着一次瞬时的扫描失败可能把循环
挡住直到用户动手 —— **刻意接受**:那正是这个锁存存在的意义(拒绝),而它在 `bx status`
里可见、`bx up` 一敲就重新求证。

## 有限次数:每段故障最多 5 次

「段」从 Core socket 第一次被观测到不应答开始,到 Core 再次被观测到应答(或用户显式
`bx up` 成功)结束。段内执行 `start_core` 的次数记在 Manager 上(只在互斥槽内递增,
在观测到 `CoreSocket==True` 或用户 `Up` 成功时归零);达到 5 次后循环**仍然提议**
(`guardian_reconcile_would` 照常,让人看得见它想做什么)但**不再执行**,发布
`start_core_exhausted`。

这与 ③b「清理动作永不放弃、靠退避限频」**刻意不同**:清理是幂等的,`ip route del`
重试一万次也只是一万次空操作;起进程不是 —— 一份坏配置、一个被占的端口,被每 30 秒
起一次杀一次,是敌意软件。5 取自 `retryDaemonRecovery` 的量级(它每 5 秒试、永不放弃,
但只在 Guardian 刚起来那段),没有真机依据支撑「够不够」,先取保守值。

被扫描拦下的那两种(`core_process_present`/`core_scan_failed`)**不计入次数**:它们
没有起进程,计进去会让一个卡住的 Core 在五轮之后把「起」的权利也耗光,而那正是最该
留着的时候。

## 机制(落点)

- `reconcile_execute.go`:白名单三项;槽内「前置条件」从写死的 `desired==off` 改成
  按动作(`start_core` 要 `desired==on`;两条清理仍要 `off`),仍经 `heldBy` 本尊复核
  栅栏;`start_core` 分支 = 现扫 → 判三态 → 计数 → `startCoreLocked`。
- 扫描走 ③a 循环已经在用的那个注入点(测量 `ReconcileCoreScan` 的那个函数),不另开
  一条调用路 —— 单测替身与循环、与准入,三处看到的是同一份扫描。
- 起 Core 用 `startCoreLocked`(带屏障 handoff、等健康、成功后释放屏障),**不新写**
  任何起进程的代码。它失败时自己会记 needs_attention,不重复记。
- `handleUnexpectedExit` 那次自带的重启不动:它在退出的那一刻就试,循环是它的兜底,
  不是替代。两者若同时到来,互斥槽 + 槽内现扫保证不会起两个。
- 计数器与「Core 被看见」的归零:观测在循环 goroutine、执行在槽内,用原子量,不上
  第二把锁。

## 安全:每个状态下都不漏 IP

- Core 死、未起回:屏障在(`/2` reject,压过一切),整机断网不是明文。
- 起的过程中:与 `bx up` 同一条路 —— 屏障带开口交给新 Core,Core 的 kill-switch 在
  隧道健康前 Block 一切走隧道的判定,屏障等 Core 报健康才释放;起不来就清掉,屏障留着。
- 5 次用完:屏障在,Blocked,断网不漏。
- 卡住的 Core:不动。
- **唯一会漏的形状是双 Core**(先退出的那个用旧快照掀掉另一个的劫持,status 显绿而
  流量明文),由「测成 0 个才起」+ `runner.Start` 既有准入两道门挡住。
- 反而缩短的一处:Core 意外退出且**屏障都没装上**(`barrier_install_failed`,另一个
  VPN 占着默认路由时会发生),今天机器停在 fail-open 直到有人敲 `bx up`;本期让循环
  在 30 秒内把 Core 起回来。

## 日志与可观测

- `guardian_reconcile_executed action=start_core outcome=ok|failed|skipped code=…`
  照既有格式;`core_process_present` / `core_scan_failed` / `start_core_exhausted`
  是新的稳定码,进 `ReconcileExecution.Error`,完整原因只进 Guardian 日志。
- `bx status` 的「上轮执行」一行:`start_core_exhausted` **不许渲染成「让路」**
  ——让路是暂时的,放弃是要人来的;措辞是「已放弃(5 次起不来,等你 `sudo bx up`)」。
  `core_process_present` 渲染成「有 Core 进程在跑但控制 socket 不应答」,那是一个
  卡住的 Core 的第一次显形,本期只显形不处置。
- `Executed` 已在 statusdigest 的 signals 里,每次执行会推进代际、叫醒 watch(它是
  真事件)。计数器本身不进 `Status`(一个没有阈值的常驻数字会变墙纸)。

## 验证(免 root)

- `TestExecutableWhitelistIsExactlyTheOffCleanupPair` 改名为
  `TestExecutableWhitelistIsExactlyOffCleanupPlusStartCore`,内容改成三项,穷举那半不动。
- Manager 级、跑真 `ExecCoreRunner` 替身:Core 被打死 → `handleUnexpectedExit` 那次
  重启被替身弄失败 → 下一轮循环把它起回来,且屏障已释放、`Executed.Action=start_core
  outcome=ok`。这条是本期的旗舰,变异(把白名单改回两项)必须转红。
- 扫描替身报 1 个 → 一个都不起,码 `core_process_present`;报没测成 → 不起,
  `core_scan_failed`;两者都不推进计数。
- 连续 5 次起失败 → 第 6 轮 `start_core_exhausted`;随后观测到 socket 应答(或用户
  `Up` 成功)→ 计数归零、再次可起。
- 槽内复核:`desired` 在决策与拿到槽之间翻成 off → `preconditions_changed`;挂起
  武装 → `maintenance_hold` 让路(照 ③b 既有形状)。
- `bx status` 渲染:三个新码各有一句人话;exhausted 不含「让路」二字。
- 真机验收(这台 Mac,只在所有者在场时做):把 data_dir 里的 `sing-box` 暂时改名让
  Core 起不来,`sudo kill -9 <Core PID>`;看 Guardian 日志先出 `core_unexpected_exit`
  与 `core_restart_failed`,随后循环在 30 秒–5 分钟内记 `start_core … execute_failed`
  五次后 `start_core_exhausted`,`bx status` 显示「已放弃」;改回名字、`sudo bx up`
  归零、回绿。若 `handleUnexpectedExit` 那次重启自己就成功了,本期没参与,那是正常。

## 交付判据

1. 上面每条测试全绿,且旗舰那条对「白名单改回两项」的变异转红;
2. `bash scripts/verify.sh` 全量绿;
3. CLAUDE.md 新增一节,点名的测试与文件由既有两条守卫钉住;
4. 真机验收留给所有者,本期标「真机未验」。

## 不做(理由写死,别在 review 里重新提议)

- `stop_core`:`desired=off` + socket 应答最常见的真实来源仍是 `sudo bx run` 调试路径。
- 重启卡住的 Core:先让它以 `core_process_present` 显形、攒数据,再定要不要动它。
- 「装屏障」进循环:永不(③a 原文)。
- 解 Uncertain 锁存:单独一期;本期只保证循环不把它弄得更糟。
- 把计数器或扫描结果做成 `bx status` 常驻行:墙纸。

## 风险

- 计数器归零的两处(观测到应答 / 用户 Up)漏一处不会有编译错误 —— 由测试分别钉住。
- `startCoreLocked` 等健康最长一个 `restartTimeout`,期间持互斥槽;用户此刻敲 `bx up`
  会排队等它 —— 与 `handleUnexpectedExit` 今天的行为一致,不是新代价。
- 5 这个数没有真机依据;取保守值,真机攒到数据再调。
