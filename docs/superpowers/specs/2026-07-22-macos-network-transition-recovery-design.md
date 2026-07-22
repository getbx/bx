# macOS 网络切换无泄漏恢复设计

Status: APPROVED (2026-07-22)

## 背景

bx 当前的 `reconnect` 只在运行中的 Core 内建立一个相同配置的替代传输，等待其健康后再
原子替换旧传输。这个动作不触碰 TUN、系统 DNS 或默认流量接管，因此在底层网络稳定时能
安全工作。

macOS 从公司 Wi-Fi 切换到家庭 Wi-Fi、热点或有线网络时，底层接口、默认网关和本地网段
可能同时变化。此时 bx 仍保留 TUN 与默认流量接管以防止真实出口泄漏，但服务器旁路、局域
网旁路和共存通道路由可能仍指向旧网关。单独重建传输会沿失效旁路访问服务器，替代传输
无法通过健康门；网络状态稳定或路由被其他流程修正后再次执行，操作又会成功。

当前菜单栏还通过 AppleScript 执行 `sudo bx reconnect`，并丢弃 CLI 的 stdout、stderr 与
结构化错误。用户只能看到通用的 Reconnect Failed / Run Doctor，而 Doctor 归档不一定包含
这次操作的真实失败原因。

此外，现有 Unix LocalAPI 客户端使用统一的 `3s` HTTP 总超时，而候选传输健康门默认允许
`20s`。当连接建立超过 3 秒时，服务端仍在安全准备候选传输，CLI 与菜单却先收到
`context deadline exceeded` 并误报失败；候选传输稍后仍可能成功切换。增加客户端超时只能
延后误报且会阻塞 UI，不能解决操作状态分裂，因此恢复必须改为异步事务。

本设计让网络切换恢复成为 Guardian/Core 的正式能力，而不是菜单串联 `rehijack` 与
`reconnect` 两条命令。它补充：

- `2026-07-13-safe-reconnect-design.md`
- `2026-07-16-macos-guardian-lifecycle-update-design.md`
- `2026-07-20-macos-unified-app-design.md`

## 产品承诺

1. bx 开启时，macOS 切换 Wi-Fi、网关或物理接口后自动恢复保护，无需用户主动操作。
2. 恢复过程中允许短暂无法联网，但不得回落到真实公网直连。
3. TUN、系统 DNS、IPv4 全量接管与 IPv6 阻断在恢复期间保持生效。
4. 只更新依赖底层网络的旁路，不重建或短暂删除全量接管路由。
5. 新传输通过健康门后才接管新连接；失败时旧传输和 kill-switch 状态保持。
6. 菜单里的 Reconnect 与自动恢复调用同一个 Guardian 操作，不再 shell 到 CLI。
7. 每次恢复都产生结构化、脱敏、可归档的事件，Doctor 能解释失败发生在哪一步。
8. App、CLI 和 agent 看到同一恢复状态，不出现各自推测的状态文案。

## 非目标

- 不保证跨网络切换时已有 TCP、QUIC 或实时通话连接不断线。
- 不把网络路径监听、路由修改或传输进程所有权交给 BxMenu。
- 不在恢复失败时启用直连 fallback。
- 不把所有网络故障都解释为底层网络切换；服务端、凭据和协议失败仍独立报告。
- 不复用现有会删除并重装全部路由的 `RehijackRoutes` 作为自动恢复实现。
- 不在未获得用户授权的开发测试中主动切换 Wi-Fi、启动或停止 bx。

## 根因与设计约束

### 当前调用链

```text
BxMenu
  -> AppleScript with administrator privileges
  -> /usr/local/bin/bx reconnect
  -> POST /v0/reconnect
  -> liveMutator.Reconnect
  -> transportSwapper.swapTo(currentLink)
```

`swapTo` 只建立替代传输。它假设服务器旁路已经指向当前物理网关，这个假设在 macOS 网络
切换后不成立。

### 不复用全量 rehijack

macOS 当前 `RehijackRoutes` 会依次删除、重加完整 route spec，其中包含 `0.0.0.0/1`、
`128.0.0.0/1` 和 IPv6 reject 路由。即使 TUN 设备仍存在，删除两条 IPv4 `/1` 的瞬间也会
让系统原始 default route 重新可用，违反恢复期间不得直连的要求。

网络切换恢复只允许修改 **underlay-dependent routes**：

- 传输服务器 IP 的物理网关旁路；
- 当前局域网与用户明确允许的本地旁路；
- Tailscale 等已识别共存通道的 bootstrap/overlay 旁路。

以下 capture routes 在整个恢复事务中不可删除：

- `0.0.0.0/1 -> utun`
- `128.0.0.0/1 -> utun`
- `::/1 -> reject`
- `8000::/1 -> reject`

## 架构

```text
macOS path/default-route observation
              |
              v
      Guardian recovery coordinator <----- Bx.app Reconnect
              |
              +-- debounce + generation identity
              +-- Core LocalAPI: prepare underlay recovery
              +-- replace underlay-dependent routes only
              +-- build and health-check replacement transports
              +-- atomically switch data-plane dialers
              +-- publish structured result
```

### 网络变化观察器

Bx.app 不承担网络监听。root Guardian 使用一个窄平台接口接收 macOS 网络变化信号，并在每次
恢复前主动读取真相：物理默认接口、默认网关、接口地址和网络 generation identity。

V1 可用 SystemConfiguration 动态存储通知或等价的原生 reachability/path 机制实现事件唤醒；
通知只表示“可能变化”，最终状态必须由 Guardian/Core 重新探测，不能信任事件载荷。

观察器行为：

- 启动时记录初始 generation；
- 接口、默认网关或主要地址变化时触发；
- 1 秒安静窗口防抖，连续变化合并为最新 generation；
- 恢复执行中又发生变化时取消尚未提交的旧 generation，并在当前步骤安全结束后处理最新值；
- 周期性低频校验只用于弥补丢事件，不成为主要恢复机制。

### Guardian recovery coordinator

Guardian 是恢复生命周期的唯一所有者。自动事件、菜单 Reconnect、CLI 和 MCP 最终都调用同一
个版本化 LocalAPI：

```text
POST /v1/recoveries              -> 202 Accepted + recovery_id + current state
GET  /v1/recoveries/current      -> current/latest recovery snapshot
```

请求只包含原因和可选 generation，不包含链接或秘密。POST 不等待 route rebind 或 transport
health；CLI 可在本地显示进度并轮询，Bx.app 订阅或定时读取状态。客户端超时只表示 LocalAPI
不可达，不能被解释为已经接受的恢复事务失败。

同一时刻最多有一个恢复事务：

- 相同 generation 的重复请求合并；
- 新 generation 取代尚未进入传输切换点的旧请求；
- 更新 maintenance barrier 激活时，恢复排队等待更新完成；
- Guardian 重启后根据 desired state 与 Core runtime state 重新判断，不盲目重放旧事务。

### Core 恢复状态机

恢复按以下顺序执行：

1. **Observe**：读取当前物理接口、网关、本地网段及现有 capture route 身份。
2. **Validate capture**：确认 TUN、两条 IPv4 `/1`、DNS 接管与 IPv6 阻断仍在；任何缺失都
   进入 `blocked`，不得继续尝试可泄漏的局部修补。
3. **Rebind underlay**：在不删除 capture routes 的前提下，把服务器及允许的本地旁路替换到
   新物理网关。每条路由使用 replace/change 或先加新精确路由再删旧路由，不能产生无旁路窗口。
4. **Replace transports**：并行准备主传输与已配置 UDP 专用传输；每条均通过自己的健康门。
5. **Commit**：原子替换 dialer 指针，停止旧传输，记录新 generation。
6. **Verify**：确认 tunnel healthy、DNS listener、capture routes 与出口保护状态。
7. **Publish**：写结构化结果并通知 App/CLI/agent。

失败策略：

- Observe 或 underlay 不稳定：保持 `recovering`，指数退避后重试；
- capture route 缺失：进入 `blocked`，保留能保留的阻断面并要求 Repair；
- 旁路重绑定失败：不启动替代传输，保持 fail-closed；
- 替代传输失败：停止候选传输，不切换 dialer，保留 capture 与 DNS；
- Verify 失败：不报告成功，进入 `blocked` 或回到最后完整 generation；
- 任何失败都不得调用系统 default route 作为公网 fallback。

### Underlay route set

平台层新增与完整 Hijack 分离的窄能力：

```go
type UnderlaySnapshot struct {
    Generation string
    Interface  string
    Gateway    netip.Addr
    LocalCIDRs []netip.Prefix
}

type UnderlayRebinder interface {
    ObserveUnderlay(context.Context) (UnderlaySnapshot, error)
    ValidateCapture(context.Context, tunHandle) error
    RebindUnderlay(context.Context, tunHandle, old, next UnderlaySnapshot,
        serverBypass, userBypass []netip.Prefix) error
}
```

`RebindUnderlay` 不拥有 TUN 生命周期，也不能创建、删除 capture route。macOS 实现必须把将执行
的路由差异构造成纯计划后再执行，便于单测证明计划中永远没有 `/1` 删除动作。

### 传输替换

当前 `Reconnect` 只替换主 TCP 传输，而 bx 的正常配置还可能有独立 Hysteria2 UDP 传输。
网络切换恢复必须把两者视为同一 generation 的 transport set：

- 主传输和 UDP 专用传输分别建立候选实例；
- 配置了 UDP transport 时，两者均健康才把整个 generation 标记为 Protected；
- 主传输健康、UDP 未健康时保持 UDP fail-closed，并显示 `Needs Attention`，不能谎报完整恢复；
- 候选进程使用事务唯一的配置文件，不覆盖仍在运行的 sing-box 配置文件；
- 固定本地辅助监听端口不得被候选传输重复绑定；共享监听由 Core 独立持有，候选只提供动态
  SOCKS/transport endpoint。

## 统一 App 交互

统一 `/Applications/Bx.app` 只展示 Guardian 发布的状态：

- `Protected`：绿色；
- `Reconnecting`：黄色，显示简短的 “Network changed” 或 “Reconnect requested”；
- `Blocked`：红色，明确流量未回落直连；
- `Repair Required`：红色，capture/运行时一致性无法自动恢复。

菜单中的 **Reconnect** 是手工重跑同一恢复状态机，不再调用 AppleScript 或 CLI。正常网络切换
自动恢复后，用户不需要点它。重复点击只合并请求，不产生多个候选传输或多个授权弹窗。

失败提示直接使用结构化错误类别：

- Waiting for network
- Could not bind the new network path
- Protected transport unavailable
- Protection state needs repair

提示保留一个 **Details** 或 **Run Doctor** 动作，但不把 Doctor 当作所有错误的正文。成功时菜单
恢复绿色，不弹模态成功框。

## CLI、agent 与诊断

CLI bridge 和 MCP 调用 Guardian LocalAPI：

```text
bx reconnect
bx status --json
bx doctor --json
```

不新增面向普通用户的网络切换命令。`rehijack` 保留为低层诊断/agent 能力，不出现在普通 help
主路径，也不由菜单串联调用。

状态增加：

```json
{
  "protection_state": "recovering",
  "network_generation": "<opaque>",
  "recovery": {
    "reason": "underlay_changed",
    "stage": "transport_health",
    "attempt": 2,
    "last_error_code": "transport_unavailable"
  }
}
```

日志以一次事务一个 `recovery_id` 关联以下事件：观察到变化、capture 验证、旁路差异、候选传输
启动、健康结果、提交或失败。日志不得包含 client link、UUID、密码、token、完整配置或浏览器
请求目的地。

Doctor 读取最近一次恢复事件并给出结论，例如：

- 网络仍在变化，bx 正在安全等待；
- 服务器旁路已经迁移到新接口，但传输健康失败；
- capture route 缺失，保护保持阻断并需要 Repair。

## 与更新和生命周期的关系

- Guardian desired state 为 Off 时不执行自动恢复。
- App 退出不影响观察器和自动恢复。
- Core 意外退出由 Guardian 生命周期恢复处理，不伪装成 underlay change。
- 更新事务持有 maintenance barrier 时，网络事件被合并；新 Core 激活后用最新 underlay
  generation 启动并验证。
- 更新失败回滚旧 Core 后，Guardian 必须再对最新 underlay generation 做一次恢复。
- `sudo bx up` 首次启动仍执行完整 Hijack；本文只处理已经 Protected 后的底层网络变化。

## 测试与验收

### 自动测试

1. 网络事件防抖、generation 合并、执行中出现新 generation。
2. Off 状态忽略事件；Protected 状态触发；更新 barrier 下排队。
3. underlay route diff 永不包含 capture `/1` 或 IPv6 reject 的删除。
4. 网关 A -> B 时服务器旁路迁移到 B，TUN capture 全程存在。
5. 旁路重绑定失败时不启动候选传输，状态为 blocked/recovering。
6. 主/UDP 候选健康组合与各自 fail-closed 行为。
7. 候选失败时旧 dialer 不变、候选进程清理、无直连 fallback。
8. Reconnect 的菜单、CLI、MCP 请求都落到同一 coordinator，重复请求合并。
9. 菜单展示结构化错误，不依赖解析 CLI 文案或 AppleScript stderr。
10. 日志与 Doctor 包含 recovery_id/stage/error_code，且秘密脱敏。
11. Guardian/Core/App 版本不一致时拒绝恢复并进入 Repair Required。
12. `go test -race` 覆盖恢复、自动 failover 与更新 barrier 并发。

### macOS 真机验收

真机测试必须由用户明确授权，agent 不自行切换网络或执行 `bx up/down`。

1. Protected 状态从公司 Wi-Fi 切到家庭 Wi-Fi，菜单自动经历黄色后回绿。
2. 恢复过程中持续采样 route、DNS 与出口；结果只能是 bx 代理出口或无网，不能出现本地公网 IP。
3. 主传输与 Hysteria2 UDP 均获得新 generation，Meet/QUIC 可恢复。
4. 新网络暂时无互联网时保持 Blocked，互联网出现后自动恢复。
5. 切换中快速重复点 Reconnect，不产生重复授权、多个事务或残留进程。
6. 在恢复的 Observe、Rebind、Transport health、Commit 各阶段注入失败，均保持 capture。
7. 恢复同时触发 App 更新，新旧 Core 回滚后仍使用最新网络 generation。
8. App 退出、CLI 未打开时切换网络，Guardian 仍自动恢复；重新打开 App 显示真实结果。

## 分阶段交付

### Phase 1：可观察且单一入口

- Guardian LocalAPI 增加版本化 recovery 请求与状态；
- BxMenu Reconnect 改调 Guardian，不再 shell 到 CLI；
- CLI 复用同一 API；
- 结构化恢复日志与 Doctor 归档。

### Phase 2：安全 underlay 重绑定

- 平台无关 recovery coordinator 与 generation 模型；
- macOS underlay observer；
- capture validator 与只改旁路的 route diff/rebinder；
- 网络变化自动触发、退避与请求合并。

### Phase 3：完整 transport set 与真机验收

- 主传输 + UDP 专用传输候选事务；
- 候选配置文件和共享监听所有权收敛；
- 更新 barrier、自动 failover 与 recovery 的并发验证；
- macOS 网络切换无泄漏真机矩阵。

## 成功标准

用户开启 bx 后可以自然地在公司、家庭和热点之间移动。bx 自动识别底层网络变化，在不开放
真实公网路径的前提下重新绑定旁路并建立健康传输。菜单中的 Reconnect 只是同一可靠恢复流程
的手工入口；失败时用户和 agent 都能看到准确阶段与原因。统一 Bx.app 是唯一产品入口，CLI
继续可用，但不再拥有另一套生命周期或恢复逻辑。
