# macOS Guardian DNS 生命周期设计

Status: APPROVED (2026-08-04)

## 背景

macOS 旧生命周期在 Core 就绪后调用 `install.EnableDNS("")`，验证系统 DNS 已切到
`127.0.0.1`，失败则回滚启动。Guardian 接管 bx 生命周期后，`macOSUpLifecycle`
只等待 Guardian 报告 `Protected`，没有把系统 DNS 接管纳入 Guardian 状态机。

结果是 Core 的 DNS 仍在 `127.0.0.1:53` 监听、路由与隧道也可用，但 macOS 继续使用
当前网络提供的 DNS。BxMenu 此时会显示 `DNS: Not managed`，Guardian 却仍报告绿色
`Protected`。这会造成 DNS 泄漏、解析位置与代理出口不一致，以及 CDN 或风控行为异常。

## 产品不变量

1. `sudo bx up` 在 macOS 上必须接管当前活动网络服务的系统 DNS。
2. 只有 Core 健康、路由受保护、UDP 满足策略且 DNS 已验证由 bx 管理时，Guardian
   才能报告 `Protected`。
3. DNS 未接管或无法验证时不得显示绿色；状态至少为 `Needs Attention`，并保持
   fail-closed，不回落真实网络。
4. 自动网络恢复、手动 reconnect 和 Core 更新不得丢失 DNS 接管。
5. `bx down` 必须在保护屏障内恢复原 DNS，确认恢复后才完成关闭。
6. CLI 与 BxMenu 必须读取同一份 Guardian 权威状态，不各自推断保护是否完整。

## 方案选择

### 采用：Guardian 统一拥有 DNS 生命周期

Guardian 已经拥有 macOS Core、路由屏障、恢复和更新事务。DNS 是完整保护路径的一部分，
应由同一个 root-owned 生命周期管理器负责。生产实现复用 `internal/install` 已有的 DNS
保存、切换、验证和恢复能力，通过窄接口注入 Guardian，便于无 root 单元测试。

### 不采用：CLI 在 `bx up` 后补调 DNS

这个方案只能覆盖用户显式运行 CLI 的路径。开机恢复、网络切换、菜单操作、Guardian
自恢复和更新事务仍可绕过 CLI，再次出现绿色但 DNS 未接管。

### 不采用：Core 直接修改系统 DNS

Core 是数据面进程，不应持有系统配置事务。把 DNS 生命周期放进 Core 会重新耦合平台权限、
更新交接和数据面，并使 Core 崩溃时的恢复责任不清晰。

## 架构边界

Guardian 增加一个只表达系统 DNS 意图的依赖：

```go
type DNSManager interface {
    EnsureManaged(context.Context) (DNSState, error)
    Inspect(context.Context) (DNSState, error)
    Restore(context.Context) (DNSState, error)
}

type DNSState struct {
    Managed bool
    Service string
}
```

生产适配器调用 `install.EnableDNS`、`install.InspectDNS` 与
`install.DisableDNSContext`。适配器只返回状态和脱敏错误，不向 Guardian 暴露原 DNS
服务器值。原始 DNS 仍只保存在现有 root-only 状态文件中。

`NetworkRestorer` 不再单独暗含 DNS 语义；DNS 的 Ensure、Inspect 与 Restore 由明确的
`DNSManager` 完成。其他平台使用无操作适配器，现有 Linux/Windows 生命周期不变。

## 生命周期顺序

### 启动

1. Guardian 写入 `desired=on` 并建立既有 fail-closed 屏障。
2. 启动 Core，等待传输、TUN、路由、UDP 和本地 DNS listener 健康。
3. 调用 `DNSManager.EnsureManaged`，自动识别当前活动网络服务。
4. 再调用 `Inspect`，确认该服务的 DNS 唯一指向 `127.0.0.1`。
5. 验证成功后才释放启动屏障并提交 `Protected`。

若第 3 或第 4 步失败，Guardian 保留或重新建立 fail-closed 屏障，状态改为
`Needs Attention`，错误码为 `dns_takeover_failed` 或 `dns_verification_failed`。
不得把只有隧道健康的状态降格解释为完整保护。

### 网络变化与 reconnect

网络观察器触发恢复时，Guardian 继续使用既有串行恢复事务。替代 Core 路径健康后、
恢复提交前，重新执行 `EnsureManaged` 和 `Inspect`。`install.EnableDNS` 已负责在活动网络
服务变化时恢复旧服务、保存新服务原值并接管新服务。

DNS 验证失败时恢复不得标记成功，保护屏障保持生效，菜单显示黄色。后续自动重试仍走
同一事务，不增加独立 DNS 定时器。

### 更新

更新事务在新 Core 健康后、提交新版本并释放屏障前验证 DNS。验证失败时按现有更新回滚
语义恢复旧 Core；若旧 Core 已恢复但 DNS 仍无法接管，状态保持 `Needs Attention` 且
fail-closed，不能把更新回滚等同于保护恢复。

### 关闭

1. 建立并确认 fail-closed 屏障。
2. 停止 Core。
3. 调用 `DNSManager.Restore`，并确认系统 DNS 不再指向 `127.0.0.1`。
4. DNS 恢复成功后释放屏障并提交 `Off`。

恢复失败时保持屏障与 `Needs Attention`，避免系统 DNS 指向已经停止的本地 listener，
也避免直接释放真实网络。

## 状态契约

Guardian `Status` 增加非秘密字段：

```text
dns_managed     bool
dns_service     string, optional
dns_state       managed | unmanaged | unknown
```

不在状态中返回原 DNS 地址。`ProtectionProtected` 的判定必须要求
`dns_state == managed`。DNS 为 `unmanaged` 或 `unknown` 时，保护状态为
`needs_attention`，`last_error` 使用稳定、非秘密错误码。

CLI `bx status --json` 原样传递这些字段。BxMenu 删除运行 `bx dns status` 的第二数据源，
直接使用 Guardian 状态：

- `managed`：DNS 行显示 `<service> managed`，可显示绿色。
- `unmanaged`：DNS 行显示 `Not managed`，整体黄色。
- `unknown`：DNS 行显示 `Status unavailable`，整体黄色。

菜单的绿色盾牌只表示完整保护，不表示“隧道大致可用”。

## 错误与恢复语义

- DNS 错误日志必须脱敏，不记录原 DNS 服务器列表。
- `bx up` 在 DNS 接管失败时返回非零，并提示查看 `bx doctor`/日志；不建议用户手动把
  `sudo bx dns on` 当作长期修复。
- `bx doctor` 将 Guardian DNS 状态作为保护检查；运行中但 DNS 非 managed 时结果不为 OK。
- Guardian 重启后若 `desired=on`，启动恢复必须重新 Inspect/Ensure DNS，不能仅凭保存的
  desired 状态宣称 Protected。
- DNS 接管幂等；已由 bx 管理时重复 Ensure 不覆盖最初保存的原 DNS。

## 测试策略

全部自动测试使用注入的 fake DNSManager，不执行 `networksetup`，不改开发机网络。

Go 测试覆盖：

1. 正常 Up 在 DNS 验证后才进入 Protected、释放屏障。
2. Ensure 或 Inspect 失败时保持 fail-closed 并进入 Needs Attention。
3. 启动恢复、网络恢复、reconnect 和更新成功路径都重新验证 DNS。
4. 更新回滚后 DNS 仍失败时不得报告 Protected。
5. Down 在屏障内恢复 DNS，失败时不得提交 Off 或释放屏障。
6. 状态 JSON 不含原 DNS 地址，且 DNS 字段在 CLI 中透传。
7. Linux/Windows 无操作适配器不改变现有行为。

Swift 测试覆盖：

1. managed DNS + 健康隧道显示绿色。
2. unmanaged/unknown DNS 即使隧道健康也显示黄色。
3. 菜单 DNS 文案来自 Guardian report，不再依赖额外 CLI 子进程。

最终验证运行 `go test ./...`、`go vet ./...`、macOS BxMenu tests，以及 Darwin、Linux、
Windows 交叉构建。真机 smoke test 只在代码验证完成后由用户明确执行。

## 非目标

- 不增加可选的“DNS 不接管但仍算安全”模式。
- 不引入 DoH/DoT provider 选择或 DNS 地区伪装设置。
- 不改变现有 fake-IP、split DNS、分流或 UDP 策略。
- 不在开发过程中自动运行 `bx up/down/dns on/off`。
