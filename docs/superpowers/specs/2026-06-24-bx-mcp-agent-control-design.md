# bx MCP agent-control surface(agent 可操作控制面)— 设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-24)。实现走单独的 plan。

## 0. 产品命题(north star,本 spec 是它的第一刀)

> **bx = 一个自托管、不泄漏的「AI 访问代理」,从安装、配置、验证、诊断到自愈,全部被它主人的 AI agent 驱动——赌的是:当人人都有个人 agent 时,「给我搞一条稳定不被发现的 AI 通道」会变成对 agent 说的一句话,而不是一个周末的折腾。**

"AI-native" 在这里**不是聊天框界面**,而是:**bx 本身是一块"agent 可安全操作的底座"**。产品不造 agent——赌用户**自带** agent。

这个命题至少含四个独立子系统,各自后面单独开 spec:① **agent 可操作的控制面**(本 spec)· ② "访问 AI" 的定位层(路由/SNI 预设、传输自动选)· ③ 公开分发(陌生人从零到能用、身份/分享)· ④ 人类友好兜底 UX。本 spec 只做 ①,它是 ②③④ 都依赖的底座,也是 bx 的护城河(对照 `2026-06-22-market-comparison-and-gaps.md`:agent 可操作 + 结构性 fail-closed + dry-run,竞品都没有)。

## 1. 目标 / 非目标

**目标**
- 让任意 MCP agent(Claude Code/Desktop 等)能**端到端**把 bx 从「不存在/坏了」开到「验证通过」:`setup → verify → diagnose → repair → commit`。
- 任何改动类操作**永远可回滚**:借用 `commit confirmed` 模式,死手定时器(默认 240s)在未确认时自动复原到 last-known-good。**agent 永远 brick 不了那台盒子**,哪怕它通过 SSH 改路由把自己那条 SSH 改断。
- 复用现有 `internal/` 逻辑(setup/doctor/supervisor 控制),CLI 与 MCP 是同一批逻辑的两个门面。

**非目标(YAGNI,留后续)**
- 公开分发 / onboarding / 多租户(子系统 ③)。
- "访问 AI" 的内置预设、传输自动选(子系统 ②;且见 §6 修正一:这套智能搬到 agent 去了,bx 不做)。
- 自然语言/聊天界面(用户明确不要)。
- 远程网络鉴权与新监听端口(就靠 SSH,见 §2)。
- 完整回滚时间线(只做"改动事务失败/未确认时自动回滚到上一个 good",不做多步历史)。
- 常驻 agent 守护进程(agent 在部署/排障时被驱动,不常驻)。

## 2. 架构与进程/权限模型

**形态:`bx mcp` 子命令 = 一个走 stdio 的 MCP server。** agent 的 MCP 客户端把它当子进程拉起,stdio 通信。它**不 shell 调自己**,直接调用 `internal/` 现有函数。

**拉起方式(不开任何网络端口):**
- **本地**:agent 直接 spawn `bx mcp`(同机)。
- **远程**:agent 跑 `ssh user@box bx mcp`——MCP 协议照走 SSH 的 stdio。**零新增网络面、零新增鉴权**,复用既有 SSH 信任链。符合 bx「不加攻击面」的一贯气质。

**权限模型:**
- bx 已有 **root 长驻 supervisor + `/run/bx.sock`(darwin `/var/run/bx.sock`)控制 socket**。
- **只读 tools** → 读控制 socket / 现有诊断逻辑,多数免 root。
- **改动 tools** → 需 root;`bx mcp` 以被拉起时的权限运行(`sudo bx mcp` 或 `ssh root@box`),改动经 supervisor 控制 socket 或与 `bx setup` 同一条特权路径。
- 控制 socket 若需新增"改动"动词(set-transport/restart/rehijack/commit/rollback),按现有 socket 协议扩展。

## 3. 安全底座:commit-confirmed(所有改动类操作的统一协议)

每个会改状态的 tool 走两阶段(Cisco/JunOS `commit confirmed` 同款),把 `--test-timeout` 死手从 `bx run` 升格为通用底座:

1. **apply**:动手前把当前状态(路由表 / config / unit / nft 规则)**快照成 last-known-good**;应用改动;**武装死手定时器(默认 240s,可配)**。
2. agent 跑 **verify**(泄漏审计 + **自身 SSH/控制通道存活**)。
3. **commit**:verify 过 → 解除定时器,改动转正。
4. 未在窗口内 commit(agent 崩了 / 改动切断了 agent 自己的 SSH)→ 定时器到点 → **自动回滚到 last-known-good**,网络恢复,agent 重连重试。

- **last-known-good 是快照不是固定值**:全新 setup 的回滚目标 = "没 bx 的直连原状";repair 的回滚目标 = "改之前那份能用的 bx 配置"。
- 与 fail-closed 一脉相承:不是"出错就漏",而是"出错就回到上一个好状态"。
- 默认窗口 240s(对齐用户对既有死手的心智模型),可配。

## 4. 工具集(全建在 §3 协议上)

| MCP tool | 类型 | 说明 | MCP 注解 |
|---|---|---|---|
| `bx_capabilities` | 只读 | 平台/传输/已装否,权威机器能力清单(复用 `capabilities`) | readOnly |
| `bx_status` | 只读 | 隧道健康/延迟/分流/泄漏姿态(读控制 socket) | readOnly |
| `bx_diagnose` | 只读 | doctor → 结构化发现,**每条带 remediation + next** | readOnly |
| `bx_plan` | 只读 | 对某改动 dry-run,返回将执行步骤不落地(复用 router-plan/darwin-plan) | readOnly |
| `bx_verify` | 只读 | 泄漏审计五项(IP/DNS/WebRTC/IPv6/kill-switch)+ 自身通道存活,结构化 pass/fail | readOnly |
| `bx_logs` | 只读 | 拉取/过滤客户端日志供 agent 自诊断(bx 拥有日志位置/格式/权限;**解读归 agent**) | readOnly |
| `bx_setup(link)` | 改动 | 从 link 装机(二进制+config+unit+连通探测),走 commit-confirmed | destructive |
| `bx_set_transport(link)` | 改动 | 换传输(解析 link+重生成配置+重启隧道),走 commit-confirmed | destructive |
| `bx_restart_tunnel` | 改动 | 重启隧道子进程,走 commit-confirmed | destructive |
| `bx_rehijack` | 改动 | 重装路由劫持,走 commit-confirmed | destructive |
| `bx_commit` | 控制 | 确认转正,解除死手 | — |
| `bx_rollback` | 控制 | 立即手动回滚到 last-known-good | — |

改动类全部标 `destructive`,agent 客户端据此决定是否先问人;只读类标 `readOnly`,可放心自动跑。

## 5. 结构化错误契约(agent 自驱的命门)

每个 tool 返回结构化结果;**每个错误带"下一步该干嘛"**,使 agent 循环能自闭合、不靠解析自然语言:

```json
{ "status": "error",
  "code": "TUNNEL_UNHEALTHY",
  "message": "连通探测失败:443 握手超时",
  "remediation": "传输可能被探测/封锁;建议 bx_diagnose 或换 vless:// 传输重试",
  "next": ["bx_diagnose", "bx_set_transport"] }
```

错误码是**有限枚举的 taxonomy**,至少含:`LINK_INVALID` · `PRIVILEGE_REQUIRED` · `TUNNEL_UNHEALTHY` · `LEAK_DETECTED` · `LOCKOUT_RISK` · `DEADMAN_REVERTED` · `ALREADY_COMMITTED` · `NOTHING_TO_ROLLBACK`。出错期间**始终 fail-closed**;灾难级由死手兜底。

## 6. 职责边界(agent / bx)

**判定规则:一个操作满足下面任意一条,才进 bx 的 MCP 面;否则留给 agent。**
1. **特权且原子** —— 改路由/nft/服务/文件,必须全成或全不成、要 root。
2. **有状态 / 活的** —— 依赖 bx 运行时状态(隧道健康、last-known-good、死手),只有 bx 有。
3. **精确语法 / 版本敏感** —— 解析 link、生成 sing-box 配置、算路由规格;agent 手写必幻觉。
4. **安全攸关** —— 必须保住 fail-closed、必须被 commit-confirmed 罩住。
5. **权威裁判** —— 验证须从正确视角跑、给可信一致判定。

**留给 agent(bx 不插手):** 判断/策略(选哪个传输、哪个 SNI、卡住下一步、要不要 commit)· 解读(读 logs/diagnose 推根因)· 编排(setup→verify→diagnose→repair 循环本身)。

**一句话边界:bx 给"数据 + 安全的机械动作",agent 给"判断 + 编排"。**

**规则逼出的两个设计后果:**
- **修正一:bx 不做"自动选传输"。** 那个脑子搬到 agent:bx 只 ① `bx_diagnose` 报事实(443 被探测/UDP 被限),② `bx_setup`/`bx_set_transport` 应用 agent 选定的 link。AI-native 框架反而帮 bx 砍掉复杂度。
- **修正二:不设含糊的 `bx_repair`。** "怎么修"是 agent 判断,"修的机械步骤"才是 bx 的活,故拆成 `bx_set_transport`/`bx_restart_tunnel`/`bx_rehijack` 由 agent 组合。

## 7. 数据流(agent 端到端闭环)

```
1. bx_capabilities         → 摸清这台机(平台/传输/装没装)
2. bx_setup(link)          → 应用 + 武装 240s 死手 + 快照 last-known-good
3. bx_verify               → 泄漏审计五项 + agent 自身 SSH/控制通道存活
4. 过 → bx_commit          → 解除死手,转正
   不过 → bx_diagnose      → 结构化发现 + remediation
        → bx_set_transport / bx_restart_tunnel / bx_rehijack(agent 按判断选,重新武装死手)
        → bx_verify → 回到第 4 步循环
5. agent 崩 / SSH 被自己改断 → 240s 到点 → 自动回滚,网络恢复,重连重试
```

## 8. 错误处理

- **link 非法**:`bx_setup`/`bx_set_transport` 早失败(复用 `parseVlessLink`/brook 校验),不武装死手。
- **缺权限**:返回 `PRIVILEGE_REQUIRED`,remediation 提示 `sudo bx mcp` / `ssh root@`。
- **隧道不健康 / 探测失败**:`TUNNEL_UNHEALTHY`,next 指向 diagnose / set_transport;期间 fail-closed 不漏。
- **改动切断自身通道**:agent 失联无法 commit → 死手 240s 自动回滚 → 重连后 `bx_status` 见 `DEADMAN_REVERTED`。
- **重复 commit / 无可回滚**:`ALREADY_COMMITTED` / `NOTHING_TO_ROLLBACK`,幂等安全。
- **快照/还原失败**:拒绝继续应用(宁可不改也不留半截),日志大声报。

## 9. 测试策略(守 TDD 约定)

- **纯逻辑单测(免 root,`t.TempDir()`)**——覆盖最重:错误 taxonomy 映射、tool 分发、**commit-confirmed 状态机**、**死手定时器(注入假时钟)**、last-known-good 快照/还原纯逻辑。
- **MCP 协议层**:mock MCP 客户端跑 tool 列表/调用/注解(readOnly/destructive)断言。
- **集成测(门控/需 root,build tag 或 CI)**:netns 或 Mudi 真跑 `setup→verify→commit`;以及"故意切断控制通道 → 死手在 240s 内还原"的保命实测。
- **回归**:brook / reality 两条传输、router-mode、fail-closed 不受影响。

## 10. 决策记录

- 接口形状 = **MCP server**(原生 agent 协议),非 CLI-JSON、非统一 `bx agent` 入口。
- 传输 = `bx mcp` over **stdio**;远程靠 **SSH-stdio**,不开网络端口、不加鉴权。
- 权限:只读多数免 root;改动复用 root supervisor / setup 特权路径。
- 所有改动类操作统一走 **commit-confirmed**,死手默认 **240s** 自动回滚到 last-known-good。
- 边界:**bx 给数据 + 安全机械动作,agent 给判断 + 编排**;故 bx 不做传输自动选、不做含糊 repair。
- 第一刀范围 = `setup→verify→diagnose→repair` 垂直闭环;②③④ 子系统后续各自开 spec。

## 11. 范围自检

本 spec 聚焦单一可实现闭环(一个 `bx mcp` server + 一组 tool + commit-confirmed 底座 + 错误 taxonomy),不含分发/预设/多租户。适合一份实现 plan。
