# macOS Guardian 生命周期与安全更新设计

Status: APPROVED-FOR-SPEC-REVIEW (2026-07-16)

## 背景

bx 已能验证签名 release、原子替换 CLI 与 `Bx.app`，并在保护会话运行时保持旧 Core
不动。这保证了安装过程不打断网络，却造成两个产品问题：磁盘中的版本已经更新，实际
运行的 Core 仍是旧版本；用户还需要手工 `down/up` 才能完成升级，而这正是最容易出现
DNS、launchd 和泄漏问题的阶段。

同时，`sudo bx up` 只启动 root Core，不保证已安装的 BxMenu 出现在登录用户的菜单栏。
CLI、Core 和菜单栏虽然属于同一个产品，生命周期仍显得割裂。

本设计以一个小型、root-owned 的 **bx Guardian** 收拢 macOS 生命周期。它负责 Core
启动、停止、安全更新、回滚和崩溃恢复；Core 继续只负责隧道、TUN、DNS 与数据面；
BxMenu 继续只是用户界面。更新可以短暂断网，但任何情况下都不能回落到真实公网直连。

本文替代 `2026-07-15-macos-update-architecture-design.md` 中“运行中的 Core 永不由更新器
重启”和“没有 Network Extension 就不做 fail-closed runtime replacement”的决策。
签名 manifest、完整包校验、原子安装与 receipt 设计继续保留。Network Extension 仍是
未来更强的系统级终态，但不再阻塞当前 root TUN 架构中的安全更新。

## 产品承诺

1. `sudo bx up` 启动保护，并在有登录用户且已安装 BxMenu 时尽力启动菜单栏。
2. 运行中的 bx 可以直接升级到新 Core；联网可能暂停数秒，但不会回落直连。
3. 新 Core 启动失败时自动恢复并启动旧 Core。
4. 新旧 Core 都无法健康启动时保持安全断网，明确要求诊断，不偷偷恢复公网。
5. 下载、签名验证或解包失败发生在切换前，不影响当前保护会话。
6. BxMenu 故障不能影响 Core，Core 更新失败不能损坏已验证的旧版本。
7. CLI、菜单栏和 agent 通过同一版本化 Guardian API 观察同一事务状态。

“无泄漏”在本设计中指：Guardian 已先安装 maintenance barrier 的计划内更新、迁移和
停止切换窗口内，不产生经物理公网默认路由直接访问公共地址的机会。它不宣称防御 root
恶意程序、内核故障、用户手工删除系统路由，或 Core/Guardian 被意外强杀后到 Guardian
重新取得执行机会之间的第一包级窗口；这类异步崩溃的绝对保证仍需要 Network Extension。

## 架构

macOS 运行时分为三个角色：

```text
BxMenu / bx CLI / bx MCP
            |
            v
  com.getbx.bx.guard       root, stable lifecycle owner
            |
            v
        bx Core            replaceable child process
            |
            v
 transport + TUN + DNS + routes
```

### BxMenu

BxMenu 只展示状态、取得用户同意并调用 Guardian。它不直接执行 `down && up`，不编辑 DNS
或路由，也不自行判断 Core 是否可以放行流量。退出菜单栏不等于关闭 bx。

### Guardian

Guardian 是独立 LaunchDaemon `com.getbx.bx.guard`，由稳定的 guardian mode 进程承载。
它负责：

- 作为 Core 的唯一父进程启动、监督和停止 Core；
- 执行安全更新事务及崩溃恢复；
- 在 Core 不可用的维护窗口持有 fail-closed maintenance barrier；
- 校验 Core 健康条件后决定是否恢复联网；
- 通过独立、版本化 Unix LocalAPI 暴露只读状态和受权变更操作；
- 写入不含配置、客户端链接和密钥的更新事务日志。

Guardian 常驻不等于保护常开。它持久化 `on|off` 期望状态：`up` 写入 `on`，`down` 在
网络恢复完成后写入 `off`。系统启动时只有期望状态为 `on` 才启动 Core。Guardian 自身
异常重启时，先通过 root-owned pid state、进程身份和 Core status socket 识别仍在运行的
Core；验证通过则重新接管其生命周期，验证失败才按状态机停止残留并启动一份新 Core，
绝不并行启动第二份数据面。

Guardian 不解析代理协议，不拥有 TUN 数据面，不读取 client link 内容，也不实现分流。
Guardian 版本必须向后兼容至少一个旧 Core 和一个新 Core。一次更新可以把新版 Guardian
安装到磁盘，但当前内存中的旧 Guardian 完成本次事务；新版 Guardian 在下次系统启动或
显式维护重启时生效，避免更新者在事务中替换自己。

### Core

Core 保持现有 `bx run` 责任：配置、传输、TUN、DNS、路由和状态 socket。它作为 Guardian
子进程运行，不再由独立 KeepAlive LaunchDaemon 与 Guardian 竞争所有权。

迁移时 Guardian 识别并停止历史 `com.ggshr9.bx` 与当前 `com.getbx.bx` 直接 Core 服务，
然后删除或禁用旧 plist。迁移必须幂等；label 不存在、服务已停止或旧 plist 已删除都视为
已完成，而不是致命错误。

## 生命周期

### `sudo bx up`

1. 安装或 bootstrap Guardian；若已有兼容 Guardian 则复用。
2. Guardian 建立需要的安全边界并启动 Core。
3. 等待状态 socket、隧道、DNS 和接管路由全部健康。
4. 只有健康条件满足后，才把状态标记为 Protected。
5. CLI 检测当前 console user；若用户已安装 `Bx.app`，best-effort bootstrap 正确的
   `com.getbx.bx.menu` LaunchAgent。
6. App 未安装、没有 GUI 登录用户或菜单启动失败只显示一条提示，不回滚 Core，也不改变
   网络状态。

重复执行 `up` 必须幂等，不创建第二个 Core 或第二份菜单栏进程。

### 停止与退出

- `sudo bx down` / BxMenu 的 **Turn Off bx**：Guardian 有序停止 Core、恢复 DNS 和路由、
  取消 Core 开机启动语义；完成后允许真实网络恢复。
- BxMenu 的 **Quit Menu**：只退出用户界面，Guardian 与 Core 不变。
- `down` 在 Core 已停止、label 不存在或旧服务已清理时仍成功；缺失 DNS state 时，先检查
  当前系统 DNS 是否仍指向 bx。只有确实需要恢复且无法证明原值时才报告需处理，不能因此
  拒绝停止一个已经不存在的 Core。

## 安全更新事务

### 切换前阶段

以下工作全部在旧 Core 继续保护网络时完成：

1. 获取 release manifest，并使用内置公钥验证 Ed25519 签名。
2. 选择当前架构的完整 macOS 包，下载到 root-only staging 目录。
3. 验证 SHA-256、大小、版本、最低 updater/Guardian 版本和 archive layout。
4. 解包并检查 CLI、Core mode、`Bx.app` 和服务定义是否完整。
5. 为当前已安装版本建立可回滚快照。
6. 原子写入更新事务日志，状态为 `prepared`。

任何一步失败都删除 staging，保留当前进程和网络，不进入 maintenance barrier。

### Maintenance barrier

Guardian 在停止旧 Core 前安装一个临时、比 bx split-default 更具体的 fail-closed 路由
屏障。屏障必须：

- 阻断 IPv4 与 IPv6 公共网络经物理默认路由直接出站；
- 保留精确的代理服务器 bypass，使新 Core 可以建立传输；
- 保留 loopback 和启动所需的本机控制通道；
- 不把普通公网流量标记为 direct；
- 在 Core 路由 teardown、进程崩溃和回滚期间独立存在。

macOS 实现可用覆盖公网空间的更具体 reject/blackhole routes，但路由集合必须由纯逻辑
planner 生成并单测，随后通过 dry-run 和真机验证确认优先级。不得依赖 PF 作为隐式前提。
DNS 在维护窗口继续指向 `127.0.0.1`；Core 暂停时 DNS 查询失败，同样形成 fail-closed，
不得临时恢复 ISP DNS。

### 激活与健康门

进入屏障后，Guardian 按顺序：

1. 把事务状态改为 `barrier_active`。
2. 停止旧 Core，但保持 Guardian 和屏障。
3. 原子切换 CLI、Core 资产、App bundle 和服务资源。
4. 启动新 Core。
5. 等待以下健康条件全部成立：
   - status socket 可达且版本等于目标版本；
   - 主隧道健康；
   - UDP 配置启用时，UDP transport 已进入可用或明确 fail-closed 状态；
   - DNS listener 已绑定；
   - TUN 已建立，预期接管路由存在；
   - 一次通过 Core 本地代理入口发起的受控隧道探测成功；该探测不依赖仍被屏障阻断的
     系统默认公网路径。
6. 将事务标记为 `committed`，再移除 maintenance barrier。
7. 写入 update receipt，清理旧快照和 staging，通知 BxMenu。

屏障必须最后移除。单独看到进程存活、socket 出现或 tunnel latency 都不足以放行。

### 回滚

新 Core 在超时内不健康时：

1. 保持 maintenance barrier。
2. 停止新 Core 并原子恢复旧快照。
3. 启动旧 Core，执行同一健康门。
4. 旧 Core 健康后移除屏障，标记 `rolled_back`，向用户报告更新未完成但保护已恢复。
5. 旧 Core 也不健康时保持屏障，标记 `needs_attention`，不恢复真实公网。

回滚不恢复用户配置的旧副本，因为更新事务不修改 client link、模式、DNS 策略或传输
配置。若未来 release 包含配置迁移，必须另写版本化、可逆的配置迁移规格。

### 崩溃与断电恢复

事务日志位于 root-only bx state 目录，原子写入、目录 `0700`、文件 `0600`，至少记录：

```text
transaction_id
from_version / to_version
phase
asset_digest
snapshot_path
started_at / updated_at
last_error
```

日志不包含配置内容、server link、token 或完整命令行。

Guardian 启动时先读取未完成事务，再决定是否启动 Core。处于 `prepared` 时可安全丢弃
staging 并继续旧 Core；处于 `barrier_active` 或之后时，必须先恢复 maintenance barrier，
再继续激活或回滚。Guardian 成为唯一 Core 启动者后，Core 不会在 Guardian 恢复屏障前被
launchd 单独拉起。

Core 在普通运行中意外退出时，Guardian 立即安装屏障并尝试恢复，但该反应发生在进程退出
之后，因此只是缩小窗口，不纳入“更新过程中不会回落直连”的可证明承诺。

当期望状态为 `on` 时，Guardian 在系统启动后的第一个网络动作是安装屏障，再启动 Core。
这缩小开机恢复窗口，但 root LaunchDaemon 无法证明 macOS 在它获得执行机会前绝无任何
数据包发出；开机第一包级别的强保证仍需要未来的 Network Extension。本文的无直连承诺
覆盖 Guardian 已开始接管的启动、更新、重连和恢复事务。

## 更新体验

### BxMenu

当保护运行时，用户点击更新只看到：

```text
Update bx?
Internet access may pause briefly. bx will reconnect automatically.

[Not Now] [Update]
```

更新期间盾牌为黄色，状态为 **Updating**。界面不展示下载、替换、路由或回滚等内部步骤。

- 成功：恢复绿色，显示 **bx is up to date**。
- 新版失败、旧版恢复：恢复绿色并显示
  **Update couldn't be completed. Previous version restored.**
- 新旧版本都失败：红色，显示 **Protection needs attention**，提供 **Run Doctor**。

保护未运行时，更新只替换已验证资产，不建立屏障、不启动 Core，也不改变网络。

### CLI 与 agent

用户显式运行 `sudo bx update` 即表示同意更新，不再追加一次 yes/no。macOS 默认更新完整
package；`--package` 保留为内部兼容参数，不作为普通用户主要入口。

- `bx update --check` 始终只读。
- Core 运行时，`sudo bx update` 默认执行 guarded activation。
- Core 未运行时，只安装新版本。
- 人类输出只展示准备、更新、重新连接和结果四个阶段。
- JSON 输出包含 `from_version`、`to_version`、`phase`、`core_activated`、`rolled_back`、
  `protection_state` 和结构化错误；不包含路径中的秘密或 client link。

`bx capabilities` 与 MCP mutating tool 必须明确声明 update 可能短暂暂停联网、保持
fail-closed，并可被 agent 安全调用。agent 不需要拼接 `down/up`。

## 状态模型

产品层只向普通用户暴露三种颜色：

| 颜色 | 状态 | 含义 |
|---|---|---|
| 绿色 | Protected | Core 与必要网络边界健康 |
| 黄色 | Working / Needs Attention | 正在启动、更新、恢复，或存在不破坏保护的提醒 |
| 红色 | Protection needs attention | Core 无法恢复；网络保持安全阻断 |

内部可以有 `preparing`、`barrier_active`、`activating`、`rolling_back` 等精确 phase，
但不把它们变成更多普通用户概念。Tailscale 等 overlay 尚未恢复属于黄色提醒，不得被误报
为 bx 已泄漏或更新失败。

## 数据与服务路径

- Guardian LaunchDaemon：`com.getbx.bx.guard`，plist 位于
  `/Library/LaunchDaemons/com.getbx.bx.guard.plist`。
- Guardian LocalAPI：`/var/run/bx-guard.sock`；Core status socket 继续使用
  `/var/run/bx.sock`。两者协议独立版本化。
- Guardian desired state：`/var/lib/bx/guardian-state.json`。
- 当前事务：`/var/lib/bx/update/transaction.json`。
- 最近 receipt：`/var/lib/bx/update/receipt.json`。
- staging：`/var/lib/bx/update/staging/<transaction-id>/`。
- rollback snapshot：`/var/lib/bx/update/snapshots/<transaction-id>/`。
- `/var/lib/bx/update/` 及子目录为 `root:wheel`、`0700`，状态文件为 `0600`。
- Core 不再拥有独立 KeepAlive label。
- BxMenu LaunchAgent 统一为 `com.getbx.bx.menu`；安装和 `up` 清理历史
  `com.ggshr9.bx.menu`，且只保留一个进程。

## 测试与验收

### 无 root 自动测试

1. maintenance barrier planner：IPv4/IPv6 覆盖、服务器 bypass、loopback、安装/卸载顺序。
2. Guardian 状态机：正常提交、新 Core 超时、旧版回滚、双失败、重复请求。
3. 每个事务 phase 的 crash injection 与 journal resume。
4. manifest 签名、checksum、archive traversal、版本降级和最低 Guardian 版本拒绝。
5. Core 健康门的组合测试，证明缺少任一必要条件都不会移除屏障。
6. 历史 launchd label 与 plist 迁移的幂等测试。
7. BxMenu 文案、三色状态、确认、成功、回滚和 Doctor 动作测试。
8. CLI human/JSON 输出及 secrets redaction 测试。

### macOS 真机测试

真机测试必须使用现有 watchdog/rollback 工具，并先 dry-run 展示全部网络变更。自动执行需
由用户明确授权，不能由开发 agent 自行启动。

验收矩阵包括：

- 运行中从 N-1 更新到 N，确认实际 Core 版本立即变化；
- 在 stop、replace、start、health、commit 各阶段强制失败；
- 新版失败后旧版自动恢复；
- 新旧双失败时公共网络保持阻断；
- 更新期间通过抓包和独立出口探测确认物理接口无公共直连流量；
- DNS 在整个事务中不回落到 ISP resolver；
- 断电/kill -9 Guardian 后重启恢复未完成事务；
- sleep/wake、Wi-Fi 切换和默认网关变化；
- Tailscale 在 bx 前启动、bx 后启动和更新中重连；
- `sudo bx up` 启动且只启动一个 BxMenu；无 GUI 用户时 Core 仍正常；
- 更新 Core 时 BxMenu 可更新并重新启动，Guardian 与网络状态不受 UI 退出影响。

只有上述真机测试通过后，产品文案才可声明“更新过程中不会回落直连”。

## 发布与迁移

1. 先发布能识别 Guardian 的 CLI/BxMenu，但保持现有 Core 运行方式。
2. 安装 Guardian；验证其 LocalAPI 和当前 Core 兼容。
3. 第一次 `up` 或 guarded update 时迁移旧 Core LaunchDaemon。
4. 迁移健康后删除旧 label/plist；失败则保留旧 Core，不进入半迁移状态。
5. 至少跨一个 release 保留读取旧 receipt、旧 label 和旧菜单 LaunchAgent 的兼容代码。

未签 Apple Developer ID 时，继续依赖 bx 自己的签名 manifest 和显式管理员授权。未来加入
Developer ID、notarization 和 privileged helper 后，不改变 Guardian 事务与网络不变量。

## 非目标

- 本阶段不实现 Network Extension 或手机端。
- 不追求双 Core 无感并行切换；允许数秒安全断网。
- 不做静默自动安装；必须由用户或明确授权的 agent 发起。
- 不在更新中修改 client link、模式、DNS 策略、app preset 或 Tailscale 配置。
- 不把 Guardian 做成通用进程管理器或动态插件框架。
- 不让 BxMenu、updater 或 Core 分别拥有独立的网络恢复逻辑。

## 后续方向

macOS Guardian 稳定后，Windows 可以复用事务状态机和产品语义，以 Windows service/WFP
实现平台屏障。手机端需要独立的 Network Extension/VPN service 规格，不与本阶段混写。
