# Task 7 Report: CLI 与 BxMenu Reconnect 统一迁移到 Guardian

## 状态

DONE

- Requirements source: `task-7-brief.md`
- Base commit: `65ad47e124ab6a065699ff1b0b4cbe151587752d`
- Implementation commit: `3801a02`
- 未读取整份计划替代 brief。

## 改动

### CLI Guardian-first recovery

- `bx reconnect` 改为向 Guardian `POST /v1/recoveries` 提交一次
  `reason=manual`，随后用 `GET /v1/recoveries/current` 观察同一 recovery ID。
- 首次轮询等待 250ms，后续等待 500ms；所有等待和请求都受 command
  context 约束。
- 默认人类输出固定为：

  ```text
  • Protection  Reconnecting
  ✓ Protection  Reconnected
  ```

- 新增 `--json`，终态成功或失败均输出最终 `RecoverySnapshot`。
- 观察超时/中断返回“recovery 仍在运行”并包含 recovery ID，不把观察失败
  误报为 recovery 业务失败。
- Guardian terminal failure 仅呈现 allowlist 后的稳定 error code，不输出
  `detail`。

### Legacy fallback 边界

- Guardian recovery transport 失败新增 typed `guardian.UnavailableError`。
- 仅首次 POST 尚未被 Guardian 接受且返回该 typed error 时，才调用 Task 1
  legacy direct-Core reconnect。
- Guardian HTTP/业务失败不 fallback。
- Guardian 已接受 recovery 后，GET 观察失败也不 fallback，避免重复或绕开
  Guardian 序列化。
- `reconnect --check` 同样先探测 Guardian；只有 typed unavailable 才检查
  legacy Core capability。

### BxMenu

- 删除 Reconnect 的 privileged AppleScript/CLI 路径及 `reconnect --check`
  capability 探测。
- 点击后在主线程立即显示黄色 `Reconnecting`，禁用重复点击；POST 和 GET
  轮询均在后台 queue。
- accepted/running/succeeded/failed 映射为稳定标题、颜色和短原因。
- 成功显示绿色 `Reconnected`，不弹成功 modal，2 秒后回到普通状态。
- 失败保留红色短原因，菜单提供 `Details` 和 `Run Doctor`。
- 轮询暂时失败时不触发 direct fallback；菜单定时刷新会恢复观察。

### 固定 Unix HTTP client

- BxMenu client 只暴露 `requestRecovery()` 与 `currentRecovery()`，内部 endpoint
  是封闭 enum。
- 固定 Unix socket `/var/run/bx-guard.sock`，固定两条 path 和固定 manual body。
- 不接受 shell、命令、自由 path、method 或 argument 输入。
- header 上限 32 KiB（包含 `CRLFCRLF`），body 上限 1 MiB。
- 所有响应（包括非 2xx）必须是 `Content-Type: application/json`。
- 拒绝 transfer encoding、非法 status/header、长度不一致和非法 JSON。

### Core recovery stage 延期项

- `ExecCoreRunner.RecoverPathObserved` 在 Core recovery POST 运行期间读取 Core
  `GET /v0/path-recovery`。
- Guardian 只转发既有 `publicPathRecoveryStage` allowlist，且只发布与当前
  request reason/generation 匹配的 `recovering` snapshot。
- Guardian client 因而可看到 `observe`、`rebind_underlay`、
  `transport_health`、`verify` 等 Core stage；未扩展 Task 7 产品表面，也不
  暴露 Core detail。
- ledger 的 “Guardian should relay Core recovery stages for client
  presentation” 已在本任务关闭。

### Swift 测试入口

- 当前 Command Line Tools 不提供 XCTest/Swift Testing 模块。为保证 brief
  指定的 `swift test` 真正执行现有 `swiftc + @main` 测试，而不是零测试
  退出，新增 SwiftPM build-tool plugin。
- plugin 显式声明测试/实现文件为 inputs、`tests-passed` marker 为 output；
  任一输入变化都会重跑测试，失败会使 `swift test` 非零退出。
- 原有三组菜单测试与新增 presentation/client 测试都由同一 harness 执行。

## RED 证据

### CLI

命令：

```text
go test ./internal/cli -run Reconnect -count=1
```

首次结果：FAIL。关键输出：

```text
undefined: reconnectWithDependencies
undefined: reconnectDependencies
undefined: guardian.UnavailableError
```

菜单契约独立 RED：

```text
go test ./internal/cli -run 'Reconnect|MacMenu' -count=1
```

结果：FAIL，指出 BxMenu 尚未直接提交 Guardian，且尚未持有固定
`GuardianClient`。

### Swift presentation/client

首次普通 SwiftPM test target 暴露本机无 XCTest：

```text
error: no such module 'XCTest'
```

改为仓库既有轻量测试方式并接入 SwiftPM 后，产品行为 RED 为：

```text
error opening input file '.../RecoveryPresentation.swift'
```

补充 parser 边界测试后，关闭旧实现的 RED 为：

```text
failed: error response validated status before content type
```

### Guardian typed unavailable / stage relay

首次 Guardian 定向命令：

```text
go test ./internal/guardian -run 'RecoveryClientTypes|RelaysCore' -count=1
```

结果：FAIL，关键输出：

```text
undefined: UnavailableError
```

stage relay 另做 mutation check：临时关闭 observed Core 分支后：

```text
go test ./internal/guardian -run TestManagerPathRecoveryRelaysCoreRecoveryStage -count=1
```

结果按预期 FAIL：

```text
Guardian did not receive Core recovery progress
```

恢复实现后同命令 PASS，且 worktree 回到 commit 内容。

## GREEN 与最终验证

以下命令均为 exit 0：

```text
go test ./internal/cli -run 'Reconnect|MacMenu' -count=1
swift test --package-path apps/macos/BxMenu
go test ./internal/guardian -run 'PathRecovery|RecoveryClient' -count=1
./scripts/test-macos-menu.sh
go test -race ./internal/cli ./internal/guardian -run 'Reconnect|MacMenu|PathRecovery|RecoveryClient' -count=1
go test ./...
go vet ./...
go build ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
git diff --check
```

关键结果：

- brief Go: `ok github.com/getbx/bx/internal/cli`
- fresh/clean Swift build: `BxMenu Swift tests passed`，并成功链接 BxMenu
- Guardian focused: `ok github.com/getbx/bx/internal/guardian`
- race: CLI 与 Guardian 均 `ok`
- 全仓 Go test、vet、build 与 Darwin arm64 build 全部通过

所有测试只使用 fake client、`socketpair`、missing Unix socket 或内存 HTTP
fixture；未运行 `bx up/down/reconnect`、launchctl、route、networksetup、
sudo，也未修改本机网络。

## 自审

- 未发现 Guardian 业务失败进入 direct-Core fallback 的路径。
- 未发现 Guardian 接受 recovery 后再 fallback 的路径。
- BxMenu Reconnect 不再构造或执行 shell/CLI 命令。
- Swift client endpoint、socket、body 都固定；没有菜单可传入的自由
  path/method/argument。
- Core stage 只经 allowlist 转发，`detail` 仍为空。
- `git diff --check` 通过；Task 1–6 文件改动均为增量协作，没有覆盖或回退。
- 当前环境没有可用 reviewer subagent；已人工逐文件审阅完整 diff，并用
  targeted race、cold Swift build 和 stage mutation check 补强。

## 风险 / Concerns

- 按任务安全约束，没有连接真实 Guardian socket 或驱动真实菜单交互；Unix
  HTTP framing、轮询和展示由 fake socket/fixture 与 Swift build 覆盖。
- SwiftPM build-tool test harness 是为当前无 XCTest 的 CLT 环境增加的少量
  测试基础设施；其 inputs/outputs 已显式声明，并通过 clean build 验证不会
  缓存假绿。
- 无已知 fail-open、fallback 扩张或产品范围外 concern。

---

## Fix Round 1（2026-07-27）

### 状态与提交

- 状态：DONE
- Fix commit：`5960ecaf88555eaabbaac6c3725d904a172759e4`
- 范围严格限于 controller 指定的 MCP reconnect、Guardian recovery
  client、CLI legacy fallback 输出、BxMenu Unix client / parser / failure
  presentation；未扩展其他 MCP mutator。

### 修复内容

1. **MCP `bx_reconnect`**
   - `Ops.Reconnect` 改为 context-aware Guardian recovery lifecycle。
   - `liveOps` 只向 Guardian 提交 `reason=manual`，按 250ms、后续 500ms
     观察同一 recovery ID，最多 6 次。
   - 返回 `recovery_id/state/stage/last_error_code/still_running/note`；
     terminal 与 bounded still-running 都是 agent-friendly structured result。
   - 删除 MCP `liveOps.Reconnect` 对 `supervisor.ReconnectControl` 的直接调用，
     不存在 direct-Core fallback。

2. **Guardian Go client fail-closed 分类**
   - 只有默认 Unix `DialContext` 明确失败时携带私有 dial marker，并转成
     `UnavailableError`。
   - 其他 `HTTPClient.Do` 错误均为 `AmbiguousRecoveryError`。
   - POST 收到 202 后 response body 中途断开或无法解码也为 ambiguous；
     CLI 因而绝不 legacy fallback。
   - 回归使用真实临时 Unix listener，分别覆盖完整 POST 后无响应断开和
     202 partial-body 断开。

3. **CLI legacy fallback 输出**
   - human legacy success 精确输出：

     ```text
     • Protection  Reconnecting
     ✓ Protection  Reconnected
     ```

   - `--json` legacy success 只输出稳定 JSON：

     ```json
     {
       "state": "succeeded",
       "stage": "legacy_core",
       "reason": "manual",
       "attempt": 1
     }
     ```

   - 测试同时固定 stdout、合法 JSON 和 nil error（exit 0）语义。

4. **Swift Unix client 与菜单失败收口**
   - 默认 Unix connect 改为 nonblocking `poll`，使用单调 deadline；
     read/write 设置有限 `SO_RCVTIMEO` / `SO_SNDTIMEO`。
   - 完整 Content-Length body 到齐立即返回，不等待 EOF。
   - partial/stall 在 deadline 内抛错，所有成功/失败路径关闭 client FD。
   - request 或 observation 失败统一生成 failed transition：
     `reconnectInFlight=false`、红色失败 presentation，并保留 Details /
     Run Doctor 菜单入口。

5. **Swift strict parser**
   - 只接受 HTTP/1.0 或 HTTP/1.1 与三位 100...599 status。
   - 校验 header field-name token；Content-Length 必填、上限 1 MiB，
     重复时只允许完全相同值，冲突拒绝。
   - Content-Type 必须且只能出现一次，并为 `application/json`；
     Transfer-Encoding 一律拒绝（重复同样拒绝）。
   - body 必须与 Content-Length 精确相等；short body、extra bytes、
     malformed status/version、缺失长度均拒绝。

### RED 证据

1. MCP lifecycle：

   ```text
   go test ./internal/mcp -run 'TestReconnect' -count=1
   ```

   首次 FAIL：`reconnectOut` 缺少 `RecoveryID`、`Stage`、
   `StillRunning`，证明旧 MCP 结果没有 Guardian lifecycle identity/state。

2. Guardian ambiguous POST：

   ```text
   go test ./internal/guardian -run 'RecoveryClient' -count=1
   ```

   首次 FAIL：`undefined: AmbiguousRecoveryError`。补强 202 partial-body
   回归后再次按预期 FAIL：

   ```text
   partial accepted response error = *errors.errorString unexpected EOF,
   want typed ambiguous
   ```

3. CLI fallback 输出：

   ```text
   go test ./internal/cli -run 'ReconnectLegacy|ReconnectFallsBack' -count=1
   ```

   首次 FAIL：human 与 JSON stdout 均为 `""`。

4. Swift timeout / menu transition：

   ```text
   swift test --package-path apps/macos/BxMenu --filter RecoveryPresentationTests
   ```

   首次 FAIL：`cannot find 'recoveryFailureTransition' in scope`；新增 client
   tests 同时要求无 EOF 完成、partial timeout、finite socket options 与 FD
   close。

5. Swift parser：

   ```text
   bash apps/macos/BxMenu/run-swift-tests.sh /tmp/bxmenu-test-marker
   ```

   在换成合法 RecoverySnapshot fixture 后准确 FAIL：

   ```text
   failed: conflicting duplicate Content-Length
   ```

### GREEN 与最终验证

以下命令均 exit 0：

```text
go test ./internal/mcp -run 'TestReconnect' -count=1
go test ./internal/guardian -run 'RecoveryClient' -count=1
go test ./internal/cli -run 'Reconnect' -count=1
go test ./internal/mcp ./internal/guardian ./internal/cli -run 'Reconnect|RecoveryClient|MacMenu' -count=1
go test -race ./internal/mcp ./internal/guardian ./internal/cli -run 'Reconnect|RecoveryClient|MacMenu' -count=1
swift test --package-path apps/macos/BxMenu
./scripts/test-macos-menu.sh
go test ./...
go vet ./...
go build ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
git diff --check
```

关键结果：

- focused 与 race：MCP、Guardian、CLI 全部 `ok`。
- clean/repeated Swift build：`BxMenu Swift tests passed`，
  `Build complete!`。
- 全仓 Go test、vet、build 与 Darwin arm64 build 全通过。
- `rg 'ReconnectControl|ReconnectControlContext' internal/mcp` 无结果。

### Concerns

- 按安全约束未连接真实 `/var/run/bx-guard.sock`、未驱动真实 AppKit 菜单；
  Unix protocol、timeout、FD close 与 presentation 使用临时 Unix listener /
  `socketpair` 确定性覆盖。
- 未运行任何 `bx up/down/reconnect`、`launchctl`、`route`、
  `networksetup`、`sudo` 或网络变更命令。
- 当前会话无 reviewer subagent 工具；controller findings 已逐项落实，
  并完成最终逐文件 diff 自审。
- 无已知 fail-open、direct-Core fallback 或其他开放功能 concern。

---

## Fix Round 2（2026-07-27）

### 状态与提交

- 状态：DONE
- Fix commit：`d66c8877d39e7d0d428d8a8d44d9571ee9ac60cf`
- 范围仅限 controller 指定的两个 Swift 生命周期问题：BxMenu recovery
  observation 的替换 ID，以及 Guardian Unix client 的请求级 deadline。

### 修复内容

1. **被替换 recovery 的有限终态**
   - `GET /v1/recoveries/current` 返回的 ID 与提交 ID 不同时，菜单不再
     `continue` 轮询。
   - 该情况转换为结构化 `recovery_replaced` observation failure，保留原
     submitted recovery ID，主线程将 `reconnectInFlight` 清为 `false`，发布
     红色 `Reconnect Failed` 与短原因 `Recovery was replaced`。
   - 既有 Details / Run Doctor 路径继续读取该失败 snapshot，因此不会把新
     recovery 当作旧 recovery 成功，也不会永久禁用 Reconnect。

2. **Guardian client 单一绝对 deadline**
   - 每个请求在 connect 前建立基于 `systemUptime` 的单调 deadline；默认
     Unix connect、request write 和 response read 共用该 deadline。
   - FD 在 I/O 前设为 nonblocking；每次 `poll` 都使用剩余毫秒预算，
     `EAGAIN` / `EWOULDBLOCK` 继续等待同一 deadline。`SO_RCVTIMEO` /
     `SO_SNDTIMEO` 仍只作为有限的内核 inactivity 保护，不再定义总时限。
   - 收到完整 `Content-Length` 仍立即返回；超时和其他失败继续通过 `defer`
     关闭 client FD。

### RED 证据

1. 替换 ID presentation：

   ```text
   bash apps/macos/BxMenu/run-swift-tests.sh /tmp/bxmenu-red-fix-round-2
   ```

   首次按预期 FAIL：

   ```text
   cannot find 'recoveryObservationFailure' in scope
   ```

   该测试构造 submitted `recovery-1` 与已成功的 replacement `recovery-2`，
   固定要求失败 snapshot 保留 `recovery-1`、清除 in-flight 且不得显示成功。

2. progressive trickle read：

   ```text
   swiftc .../StatusIndicator.swift .../RecoveryPresentation.swift \
     .../GuardianClient.swift .../GuardianClientTests.swift \
     -o /tmp/bx-guardian-client-red-fix-round-2
   /tmp/bx-guardian-client-red-fix-round-2
   ```

   旧 client 在 `socketpair` 上每 20ms 收到一个响应字节时仍持续约 6.5 秒，
   随后按预期 FAIL：

   ```text
   failed: progressive response uses one total deadline
   ```

   这证明旧 `SO_RCVTIMEO` 仅限制 inactivity，无法限制持续慢滴流的总时间。

### GREEN 与最终验证

以下命令均 exit 0：

```text
bash apps/macos/BxMenu/run-swift-tests.sh /tmp/bxmenu-green-fix-round-2
swift test --package-path apps/macos/BxMenu
./scripts/test-macos-menu.sh
go test ./internal/mcp ./internal/guardian ./internal/cli -run 'Reconnect|RecoveryClient|MacMenu' -count=1
go test -race ./internal/mcp ./internal/guardian ./internal/cli -run 'Reconnect|RecoveryClient|MacMenu' -count=1
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
go test ./...
go vet ./...
go build ./...
git diff --check
```

关键结果：

- Swift harness、SwiftPM build 与菜单脚本全部通过。
- progressive trickle regression 在 80ms 总预算内抛出 `ETIMEDOUT`，client FD
  被 peer 观察到关闭；完整 `Content-Length` 的无 EOF 成功回归仍通过。
- focused Go、race、全仓 Go test / vet / build 与 Darwin arm64 build 全部通过。

### Concerns

- 未连接真实 Guardian socket 或运行 AppKit 菜单；该轮的 lifecycle 边界由
  `socketpair` 与 pure presentation test 覆盖，未运行任何 live `bx`、网络、
  `sudo`、路由或 launchd 命令。
- 无已知 fail-open、无限 observation 或 deadline reset concern。
