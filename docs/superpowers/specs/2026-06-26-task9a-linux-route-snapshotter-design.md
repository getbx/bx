# Task 9a — 真实 Linux 路由快照器(`confirm.Snapshotter` 实现)设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-26)。实现走单独的 plan。

## 背景与定位

Task 9(让 MCP 改动类 ops 真正能操作 bx)太大,拆成三块:**9a 真实快照器** · 9b 控制 socket 命令化 + 死手搬进守护进程 · 9c 接 4 个改动类 ops。本 spec 只做 **9a**——死手回滚依赖的 last-known-good 快照/还原组件。

**为什么 9a 先做、且单独做安全:** 它是 9b/9c 的地基(commit-confirmed 的 Restore 靠它);现在就能用已建好的 netns 验证基建(CI/Colima)真机测;**它是自包含组件,不接任何 live 路径**,brick 不了任何东西。硬约束"真快照器与真实 mutation 同 commit"落在 9b/9c(把快照器接进 live 改动路径时),**不约束 9a 单独落地**。

## 目标 / 非目标

**目标**:实现 `internal/confirm` 的 `Snapshotter` 接口的 Linux 版,精确还原(diff-reconcile)路由状态,v4+v6,经 netns 真机验证。

**非目标(留 9b/9c)**:接控制 socket、死手搬进守护进程、wire 改动类 ops、改 `newSystemSnapshotter()` 让 live 路径用它、bx_diagnose 快照就绪。9a 只交付被测过的组件,不接线。

## 架构:位置与决策/IO 切分

- **位置**:`internal/supervisor/systemsnapshot_linux.go`(`//go:build linux`)。**不**放 `internal/confirm`——该包保持纯逻辑(死手 + `Snapshotter` 接口)。快照器要 shell `ip`,属已平台拆分、已有 `runIP`/`netConf` 的 supervisor 包,复用其助手。
- **实现** `confirm.Snapshotter`:`Capture() (confirm.Snapshot, error)` / `Restore(confirm.Snapshot) error`。
- **四层切分**(正确性大头压进免 root 单测):

| 层 | 职责 | 测法 |
|---|---|---|
| 纯解析 `parseRules`/`parseRoutes` | `ip rule list`/`ip route show table 100` 文本 → 结构化 specs | 纯单测(喂文本),免 root |
| 纯 diff `diffRules(current, target)` | 算 toDel/toAdd | 表驱动纯单测 |
| 纯命令重建 `ruleAddArgs`/`ruleDelArgs`/`routeAddArgs` | spec → 可执行 `ip` 参数(`ip rule list` 输出格式 ≠ `ip rule add` 语法,最易藏 bug) | 纯单测 |
| IO 薄壳 Capture/Restore | 跑 `ip` 抓文本 / 跑 add/del | netns 集成测(CI/Colima) |

## Capture / Restore 语义

**Capture()** 抓四样(v4+v6),解析成结构化 specs 存进 `Snapshot`(`ID()` 返回递增/哈希标识):
- `ip rule list`(v4 全部规则)
- `ip -6 rule list`(v6;未启用则空)
- `ip route show table 100`(v4,bx 独占表)
- `ip -6 route show table 100`(v6)

**Restore(snap)** 两策略:

| 对象 | 策略 | 理由 |
|---|---|---|
| ip rule(v4+v6) | **diff-reconcile**:重抓当前 → 删 current∖snap、加 snap∖current | 规则表全局共享(tailscale 等也在),**绝不 flush 全部**,只精确增删 |
| table 100 路由(v4+v6) | **flush + replay**:`ip route flush table 100` 再重放 snap 的 table-100 路由 | table 100 bx 独占,清空重灌最简单,净效果等价 diff |

净效果 = 精确还原到快照那一刻(rule 集合回到快照、table 100 回到快照内容)。

**顺序**(以测试钉死):先删多余 rule → flush+replay table 100(v4、v6)→ 加缺失 rule。避免规则指向已清空表的瞬态。

**失败处理**:尽力做完所有步骤(单步出错记录但继续,沿用现有 `down()` 的容错风格),返回是否有步骤失败的 error。上层(死手/`bx_rollback`)据此把"回滚也失败"暴露给 agent(沿用 Tasks 1-8 的 `armThen` 回滚错误上浮)。

## 数据结构(概念)

- `ruleSpec`:pref(int)+ selector(如 `from all fwmark 0x162` / `to 10.0.0.0/8`)+ table(string)+ family(v4/v6)。足以从中重建 `ip [-6] rule add pref P <selector> table T` 与对应 `del`。
- `routeSpec`:table-100 路由的目的地 + 网关/设备/类型(如 `default dev bx0` / `unreachable default` / `<cidr> via <gw> dev <dev>`)。足以重建 `ip [-6] route add ... table 100`。
- `linuxSnapshot`:`{v4Rules, v6Rules []ruleSpec; v4T100, v6T100 []routeSpec; id string}`,实现 `confirm.Snapshot`。

## 错误处理

- `ip` 缺失 / 非 root:Capture 返回 error(上层 `ArmWithSnapshot` 已约定 Capture 失败则不武装、不改动)。
- v6 未启用(`/proc/net/if_inet6` 缺):跳过所有 `-6` 抓取与还原,v6 specs 为空(复用现有 `ipv6Enabled()`)。
- 解析遇到无法重建的行:记录并跳过该行(不因一行怪异整体失败),Restore 时尽力。
- flush/add/del 单步失败:记录,继续,最终返回聚合 error。

## 测试策略(守 TDD)

- **纯逻辑单测(免 root,Mac 上跑)——覆盖大头**:
  - `parseRules`/`parseRoutes`:喂真实 `ip rule list`/`ip route show table 100` 样例文本(含 bx 的 pref 100/149/150/200、tailscale 规则、v6 unreachable),断言 specs。
  - `diffRules`:表驱动(current/target 各种组合 → 期望 toDel/toAdd)。
  - 命令重建:spec → 期望 `ip` 参数数组(v4 与 v6、各 selector 形态)。
- **netns 集成测(`//go:build integration && linux`,CI/Colima)**:在临时 netns 内 `Capture()` 基线 → 跑 `netConf.up()`(或手动加 rule + table-100 路由)制造改动 → `Restore(基线)` → 断言 `ip rule`/`ip route table 100`(v4+v6)回到基线。复用 Task-9 验证基建的 `unshare`+`LockOSThread`+dummy 模式。
- **CI**:现有 `integration` job 自动接住(它跑 `-tags integration ./...`);SKIP 守卫确保真跑。

## 决策记录

- Restore = diff-reconcile(精确还原到快照),非 scoped-undo。
- 覆盖 v4+v6。
- 抓 `ip rule`(全)+ `ip route table 100`;**不抓主表路由**(内核/DHCP 自管,diff 会抖动且危险)。
- rules 走 diff(共享表,外科手术);table 100 走 flush+replay(bx 独占)。
- 快照器放 `internal/supervisor`,`confirm` 保持纯;实现 `confirm.Snapshotter`。
- **不接 live 路径**(`newSystemSnapshotter()` 仍返回 nop,留 9b 同 commit 切真);9a 只交付被测组件。

## 范围自检

单一可实现组件(解析 + diff + 命令重建 纯逻辑 + Capture/Restore IO + netns 往返测),不接控制 socket / 不动死手位置 / 不 wire ops。适合一份 plan。
