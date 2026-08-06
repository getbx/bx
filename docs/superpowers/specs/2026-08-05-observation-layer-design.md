# bx 观测层与不变量基线设计

Status: APPROVED (2026-08-05)

## 背景

2026-08-04~05 的连续故障与随后的架构复审暴露了一个结构性问题:**bx 的 macOS 生命周期层用记忆代替观测**。

规模上的失衡先说明问题的位置:

| | 源码 | 测试 |
|---|---|---|
| `internal/guardian`(macOS 生命周期) | 7,943 | 12,946 |
| 整个数据面(dialer+route+dns+fakeip+tun) | 1,538 | 1,686 |
| `internal/tunnel`(六种传输 + 六个链接解析) | 1,206 | 992 |

六种协议实现 1,206 行;让一个子进程活着 7,943 行。数据面已冻结两个月(最近 4 天 0 次改动),
而 guardian 改了 39 次。**动荡全部集中在生命周期层。**

### 核心病灶:信念 vs 事实

表达「屏障装没装、归谁」有**六个并存的内存载体**,**没有任何 reconcile 循环,全包从不重读路由表**。
`barrierProven` 的真实语义只是「`route add` 曾经返回过 0」——此后内核里发生什么(被别的 VPN 覆盖、
被 `launchctl bootout` 连同内存记录一起抹掉而路由留在内核),它一无所知也永远不会知道。

代价是可查证的。`internal/cli/guardian.go:127-133` 是团队自己写下的事故记录:

> Booting Guardian out drops its in-memory ownership record while the kernel keeps the /2 reject
> routes, which outrank Core's /1 split-default and blackhole the whole machine with nothing left
> able to remove them.

**而观测能力早就写好了**:`internal/supervisor/underlay_darwin.go:74` 的 `ValidateCapture` 会问内核
「发往 1.1.1.1 的包现在走哪个接口」——这是劫持是否真的生效的充分判据,**对「谁装的」完全不关心,
因此天然免疫全部所有权簿记问题**。它只在 `Rebind` 里被调用一次,不在任何循环里,不出现在任何
status 里,agent 完全看不到。

> 六个内存载体联合起来仍然回答不了「内核里现在有没有那 8 条 reject 路由」,而 `route -n get`
> 一次调用就能。

### AI-native 让这个问题从工程债升级为产品缺陷

bx 定位 AI-native(MCP 控制面、自愈、自进化)。实测 MCP 面:

- **从不读 Guardian 的 `Status`**。agent 看到 6 个字段,人类 `bx status` 看到 14 个。
  `protection_state`/`phase`/`dns_state`/`recovery`/`last_error` 一个都不给 agent。
- `bx_diagnose` 会在 DNS 未接管时报「隧道健康,无异常」,而同一时刻 `bx status` 给人类显示
  `needs_attention`(`internal/cli/cli.go:4290-4294`)。**同机同时刻的观测分叉。**
- `bx_logs` 指向 Core 日志,而 Guardian 的失败原因在 Guardian 日志里。

人看到 status 显绿但网页打不开会立刻不信 status;**agent 不会,也不该会**——不信任已发布状态的
agent 是更危险的 agent。因此对 AI-native 产品,「发布未经观测的状态」不是体验问题,是正确性问题。

真实佐证:过去 24 小时一个 AI agent 在这套系统上诊断一次 `bx up` 失败用了四轮、两次把修复做成
比原 bug 更糟的东西、一次把 `PID==0` 的语义读反。这不是能力问题,是这个架构对 agent 的必然采样。

## 设计支点:意图 / 事实 / 代码 三分

| | 谁能改 | 怎么改 | reconcile 的关系 |
|---|---|---|---|
| **意图** — desired on/off、直连白名单、选哪个传输 | 用户 / agent | 显式、声明式、幂等 | **只读,永不改写** |
| **事实** — 路由、DNS、Core 存活、屏障 | 只有 reconcile | 让事实追上意图 | 只写 |
| **代码** — 上面两者的实现 | 人(经 PR 评审) | 自进化提 PR | 无运行时权限 |

由此推出 AI-native 的核心契约:

> **agent 只声明意图,从不直接驱动动作。**

它的三个直接后果:重试天然安全(声明同一意图两次 = 无操作);死循环结构上不可能(没有可被取消的
进行中动作);「看不懂的失败」变成「意图与事实的 diff」,而 diff 是自解释的。

代码里已有一个正确样板:`bx_policy_apply`(读-改-原子写,重复 add 同一域名返回 `changed:false`)
操作的正是**意图**;而问题最大的 `bx_reconnect`(重试会取消自身进度)操作的是**事实**——事实本
不该由 agent 直接驱动。

三者的依赖关系:**自进化依赖自愈依赖观测**。Guardian 今天失败时连日志都不写,没有任何素材可以
生成有意义的 PR。观测层是三条线共同的地基,因此先做。

## 产品不变量

1. 发布给外部(CLI/菜单栏/MCP)的状态中,**每个字段要么是刚从系统读回的观测,要么自证是记忆**。
   二者不得混在同一平坦结构里。
2. 观测与信念不一致时**必须产出 divergence**,不得静默,不得用观测覆盖信念。
3. 观测**只读**:本期不得因观测结果改动系统任何状态。
4. 观测失败(命令超时、权限不足)**不得**让调用方失败或让保护中断;失败即记为「未知」并出现在
   divergence 里。
5. 不变量以**可执行测试**表达,而非仅写在文档里——用户自己的 AI 不一定读过我们的文档。

## 观测层

### 观测什么

四项,每项都必须向系统现问:

| 观测项 | 判据 | 复用 |
|---|---|---|
| **劫持是否生效** | `route -n get 1.1.1.1` / `129.1.1.1` 返回的接口 == 我们的 TUN | `ValidateCapture`(`supervisor/underlay_darwin.go:74`),提成接口 |
| **屏障是否在位** | 那 8 条 `/2` reject 路由(`guardian/barrier.go:49-50` 的 `publicIPv4Blocks`/`publicIPv6Blocks`)在不在 | 新增;`ValidateCapture` 的 IPv6 分支已会读 `Reject` 标志,同法 |
| **DNS 归谁** | 读回当前 resolver,与 `127.0.0.1` 比对 | `install.InspectDNSContext`(`install/install.go:764`) |
| **Core 是否活着** | **控制 socket 是否应答、报什么 `RuntimeState`** | `supervisor.FetchRuntimeState` |

最后一项的意义超出本期:**「控制 socket 在应答」本身就是存活的观测,不需要 PID 文件**。这意味着
观测层落地后,所有权簿记在**读**这一侧即失去存在理由;剩下「停止」那一侧留待后续期次交给 launchd。

### 观测的形状

```go
// ObservedState 是某一时刻向系统现问得到的事实。所有字段都是观测,不含任何记忆。
// 观测不到的项用 Unknown 表示,绝不用零值冒充"正常"。
type ObservedState struct {
    ObservedAt        time.Time
    CaptureInterface  string        // route -n get 1.1.1.1 返回的接口;"" = 未知
    CaptureOK         Tristate      // 该接口是否为我们的 TUN
    BarrierPresent    Tristate      // 8 条 /2 reject 路由是否在位
    DNSServers        []string      // 当前系统 resolver;nil = 未知
    DNSManaged        Tristate      // resolver 是否为 bx
    CoreSocket        Tristate      // 控制 socket 是否应答
    TunnelHealthy     Tristate      // Core 自报(经 socket 取回,属观测)
    Errors            []ObserveError // 每项观测各自的失败原因
}
```

`Tristate` 三态(`True`/`False`/`Unknown`)是本设计的关键取舍:**「观测不到」与「观测到否」必须可区分**。
用 `bool` 会把「问不出来」压成 `false`,而这正是当前架构骗人的方式之一。

### 发布格式

```json
{
  "intent":     { "desired": "on", "transport": "...", "policy_version": "..." },
  "observed":   { ...ObservedState... },
  "believed":   { "protection": "protected", "phase": "...", "last_error": "..." },
  "divergence": [ { "field": "capture_ok", "believed": "protected", "observed": "false",
                    "note": "路由劫持未生效,但保护状态声称已保护" } ]
}
```

**不用观测覆盖信念。** 二者的 diff 是本设计给 agent(含用户自己的 AI)的核心产物:它自解释、可操作,
而今天这个信号存在于内核里但无人读取。

「status 显绿而流量明文直连」在这个结构下**表达不出来**:绿是 believed,而 observed 会同时说
`capture_ok: false`,divergence 会明写这一条。

## 不变量测试基线

不钉调用序列、不钉调用计数、不用 `time.Sleep` 当同步。钉的是**给定观测 + 意图 → 必须发布什么**,
纯函数、表驱动、对内部重构免疫。

首批五条:

1. `desired=off` ⇒ observed 中不得有 bx 装的屏障路由
2. `desired=off` ⇒ 系统 DNS 不得是 bx 的
3. `protection=protected` ⇒ `CaptureOK && DNSManaged && TunnelHealthy` 三者皆为 `True`
   (`Unknown` 不满足——不确定不等于正常)
4. observed 与 believed 不一致 ⇒ 必须产出 divergence,不得静默
5. 任何「减少 bx 足迹」的操作,在任何前置条件缺失下都必须能执行(本期只写测试表达该不变量,
   实现留待控制面期次;测试此时应标记为**已知失败**并注明期次,不得删除或弱化)

**这五条就是「用户自己的 AI 需要读懂的规约」,而且是可执行测试而非散文。**

### 为什么测试先行

本期采用**就地重构**(用户决策),而现有 12,946 行测试大量钉的是偶然交错:13 处 `time.Sleep` 当同步、
44 处精确调用计数、整串 trace 字符串相等断言(该字符串在 52 分钟内被改了三次,它没保护任何东西,
只是跟着实现走)。

就地重构 + 钉交错的测试 = 2026-08-05 那次 revert 的确切配方:改一处实现 → 测试红 → 红的信息是
「inspectCount = 2, want 1」,**它不告诉你破坏了什么不变量,只告诉你「和我记的不一样」**。于是只有
改测试(可能悄悄放弃真实保护——`603b602` 把一个测试的断言翻转成相反,而测试名至今未改)或退回不改。

因此不变量测试不是收尾工作,是**前置条件**:先建立对内部重构免疫的安全网,后续重构才敢删旧测试。

## 本期不做什么

- **不改任何控制流**:`Manager` 的 48 个字段、47 个 `needsAttention` 调用点一个不动
- **不给 reconcile 写权限**:本期只观测、只发布
- **不碰 launchd 迁移**
- **不碰用户正在运行的 bx 实例**(用户明确要求;真机切换由用户决定时机)

本期结束时的可交付价值:**第一次能在真机上看到「信念」与「事实」的实际差异**。那份数据直接决定
后续期次怎么改——而不是靠推断。

## 后续期次(本 spec 不含,列出以明确边界)

| 期次 | 内容 | 依赖 |
|---|---|---|
| 二 | 控制面永不 fail-closed;`forcedTeardown` 提到 API 层并暴露为 MCP 工具 | 可与本期并行 |
| 三 | Core 交还 launchd,所有权簿记整类退场 | 需本期提供验证判据 |
| 四 | Agent 契约重做(意图声明式 API、divergence 驱动的诊断、错误码接线) | 需一、二 |

## 已知风险与待验证

1. **观测本身的开销**:每次观测要跑 `route -n get`×2、屏障查询、`networksetup`/`scutil`。本期只在
   状态被请求时观测(不进循环),开销可控;进入 reconcile 循环是后续期次的事,届时需实测频率与开销。
2. **`route -n get` 的输出解析**跨 macOS 版本是否稳定——`ValidateCapture` 已在生产使用该解析,
   风险由既有代码承担,但屏障 reject 路由的查询是新增,需真机确认输出形态。
3. **观测与被观测之间的时间窗**:observed 与 believed 取自不同时刻,divergence 可能是竞态而非真差异。
   缓解:`ObservedAt` 随观测一同发布,消费者可自行判断新鲜度。本期不做原子快照。
4. 不变量第 5 条在本期是**已知失败**的测试。这是有意的:它把「控制面还没修」这件事变成 CI 里可见
   的事实,而不是一句待办。需在实现时确保 CI 对该测试的处理方式明确(标记而非跳过)。
