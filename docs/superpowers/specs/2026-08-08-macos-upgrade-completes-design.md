# macOS 升级必须一次做完设计

## 背景:真机上撞到的三个 bug

2026-08-08 首次真机验证阶段①②时,升级后菜单一直黄灯显示 Repair Required,而 CLI
一切正常(Protected、隧道健康)。取证结果:

| | 版本 |
|---|---|
| Bx.app | `phase2` |
| `runtime/current` → | `phase2` |
| **正在跑的 Guardian** | **`dev`** |
| **正在跑的 Core** | **`dev`** |

**菜单判断得完全正确**(`repairActionNeeded` 比对 bundle/runtime/core 三者),错的是别处。

**① 安装脚本给出的建议无效。** `app-install` 检测到 Guardian 在跑时打印
「建议尽快执行 `sudo bx down && sudo bx up` 完成切换」。用户照做了(两遍),没用。
原因:`bx down` 的**干净路径不 bootout Guardian**(只有强制拆除路径才做,
`internal/cli/guardian.go:172`)。Guardian 的 plist 指向
`/Library/Application Support/bx/runtime/current/bx`,但那是**进程启动时**解析的——
换掉符号链接不会让一个已在跑的进程换代码。于是 down/up 只是让同一个旧 Guardian
重起了一个旧 Core。

**② `bx up` 不检查版本。** 它能看到 `runtime_version=phase2` 而自己是 `dev`,
仍照常报 Protected。这是「信念 vs 事实」的又一个实例:up 相信自己是最新的,没去问。

**③ Repair 按钮结构上修不了这个 mismatch,而这是最常见的一种。**
`repairBx` 只是重跑 `app-install`,而 app-install **只装文件、不重启 Guardian**
(它自己也只打印那句无效建议)。于是 Repair 把同样的文件又装一遍,`core_version`
依然是旧的,黄灯依旧。用户点了没反应,不是没生效,是它做的事和问题无关。

### 一条需要更正的旧记录

`CLAUDE.md` 曾记:「**真机验证**:升级场景(旧 Guardian 在跑 + 新二进制刚装)
`down`→`up` 干净通过」。那次验的是**没报错**,没人检查**版本有没有真的换过来**。
所以它「通过」了,而实际从未切换成功。**验错了对象**——这是本设计要吸取的第一条教训。

## 设计原则(项目所有者定)

> 应该保证能安装完成。回滚是开发视角,在用户视角升级失败,他就会卸载重装。

所以目标不是「优雅地处理失败」,而是**不制造半成品状态**。今天这个设计为了不打扰用户
而绕开断网,代价是留下一个用户理解不了、Repair 也修不了的中间态——比直接断网难懂得多。

改为:**如实告诉用户会短暂断网,然后一次做完。**

## 关键事实(已读代码核实)

**`Daemon.Shutdown`(`internal/guardian/daemon.go:210`)不停 Core,也不拆路由。**
它只排空 HTTP server、等在途 mutation/recovery、关 listener、删 socket。因此:

- 单靠重启 Guardian **换不掉 Core 的版本**——旧 Core 会活过 Guardian 重启。
- 必须**先停 Core**。`down` 是必需的,理由不是改 `desired`,而是杀掉旧 Core。

**Guardian 启动时会自恢复。** plist 有 `RunAtLoad`+`KeepAlive`,`daemon.go:488` 启动时
读 `desired`,为 `on` 就自己把保护拉起来。

**旧版本仍在盘上。** `runtime/` 下各版本并存,`current` 只是符号链接;版本切换是原子的
符号链接替换。

## 升级流程

```
sudo bx app-install    (或菜单 Install / Repair)
  │
  ├─ 未检测到 Guardian 在跑 → 照旧:只装文件,不碰运行时
  │
  └─ 检测到 Guardian 在跑
       │
       │  「升级需要重启保护,期间会短暂断网(约几秒)。继续?」
       │  ↓ 用户确认(--yes 可跳过,供脚本使用)
       │
       ├─ 1. 记下当前 desired
       ├─ 2. Down —— 停 Core、还原网络为直连
       ├─ 3. 装文件 + 切 current 符号链接(原子)
       ├─ 4. 重启 Guardian daemon,使其从新符号链接重新 exec
       ├─ 5. desired 原为 on 则 Up
       └─ 6. 等到 Protected(有上限),报告结果
```

**第 2 步先于第 3 步是有意的**:它把网络还原成直连,因此**其后任何一步失败,
最差只是「没有保护」,不会是「网络不通」**——这是结构保证,不需要额外机制。
对一个用来翻墙的工具,「断网」意味着用户连重装包都下不了,那是唯一不可接受的失败态。

**不做版本回滚。** 按上面的原则,失败就如实说清并指向 `sudo bx uninstall` 后重装
(该路径已存在)。不建回滚 UI、不建「上次升级失败」状态机。

**Repair 走同一条流程。** 它今天只装文件,所以对最常见的故障无效;走同一条路它就真能修了。

**删掉那句无效建议。** `建议尽快执行 sudo bx down && sudo bx up 完成切换` 从
`app-install` 的输出里去掉——用户照做也没用的指引,比没有指引更糟。

## 附带修正:`bx up` 的版本自检

`bx up` 发现自己的版本与 `runtime/current` 不一致时,不得照常报 Protected。
两种处置任选,以实现简单为准:

- 自行 kickstart Guardian 后重试一次;或
- 明确报错并给出**真正管用**的命令。

后者的措辞必须经过验证——本次事故的根源正是一句看起来权威、执行起来无效的建议。

## 本期不做

- **不做版本回滚**(见上,原则决定)。
- **不改 `Daemon.Shutdown` 让它顺带停 Core**——那会改变 Guardian 崩溃/重启时的既有语义
  (今天 Core 活过 Guardian 重启是**有意的**:Guardian 崩了不该连累用户的连接),
  风险远大于收益。升级路径显式 Down 即可。
- **不动 Linux**。`app-install` 是 macOS 独有(Bx.app);Linux 走 systemd,升级语义不同。

## 测试策略

- **纯逻辑单测(Go)**:升级计划的判定——Guardian 在跑/没跑、desired 为 on/off,
  四种组合各自应产生哪些步骤;失败发生在第 4/5 步时的对外措辞。
- **回归守卫**:`app-install` 的输出**不得**再包含 `bx down && bx up` 那句建议
  (钉住它,否则很容易被"顺手加回来")。
- **真机验收**(必须由人做,且**必须检查版本真的换了**,不能只看有没有报错——
  这正是上次验错的地方):
  1. 装旧版 → 起保护 → 装新版
  2. 全程只需一次确认,无需任何手工命令
  3. 完成后 `bx status --json` 的 `core_version`/`guardian_version`/`runtime_version` **三者一致**
  4. 菜单不再显示 Repair Required
  5. 中途断网时长可感知但短(目测几秒)

## 风险

**升级中断(断电、强杀)会留下什么。** 第 2 步之后、第 5 步之前被打断,机器处于
「已装新版、保护没开、网络直连可用」——可接受,用户重跑升级或 `bx up` 即可。
`desired` 已在第 2 步落盘为 off,不会因 `RunAtLoad` 在下次开机把坏状态带回来。

**用户在升级期间正用着网络。** 断网几秒会掐断进行中的下载/会议。确认对话框必须
说清这一点,而不是含糊地说「可能有短暂中断」。

**`kickstart -k` 的具体行为未在真机验证过。** 本次事故中用它恢复成功(用户实测:
`down` → `kickstart -k` → `up` 后三个版本一致),但那是手工分步执行;
集成进安装流程后的时序需要真机复验。
