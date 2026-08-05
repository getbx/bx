# macOS 逃生路径修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `bx down` 在网络故障、Guardian 异常等最需要关闭保护的场景下必定可用,并让路径恢复在反复失败后停到用户可处理的状态,而不是无限重试。

**Architecture:** 三处独立修复,共享同一条原则——**关闭与恢复路径不得依赖可能已失效的前置条件**。两处修复有现成先例可循(`installBarrierForRecovery` 的网关兜底、`uninstallDarwinAction` 的强制停止),第三处是给已有重试循环加上限。

**Tech Stack:** Go 1.26,既有 `internal/guardian` 屏障/恢复事务,`internal/cli` macOS 生命周期。

## 事故背景(修复依据)

真实事故日志 `/var/log/bx-guard.err.log`(2026-08-03 10:11 ~ 2026-08-04 19:05):

- `default gateway not found` × **1725**
- 同一次恢复 `recovery-1` 重试到 **attempt 178**、`duration_ms 4284475`(**71 分钟**),每轮新起一个隧道进程(socks5 端口递增)
- 循环模式:起隧道 → `bx 隧道健康检查超时(20s)` → `recovery_failed` → 5s 后 `recovery_canceled` → 重复
- 用户 `bx down` 失败,最终只能 `bx uninstall` 脱身

三者叠加 = 用户断网且产品内无路可逃。

## Global Constraints

- 平台:改动集中在 `internal/guardian` 与 `internal/cli`;`GOOS=linux`/`windows` 构建不得受影响。
- **不得削弱 fail-closed**:任何修复都不能让隧道不健康时的流量回落直连。屏障降级为 block-only 是**收紧**(去掉 bypass),不是放松。
- **关闭路径不得新增前置依赖**。判断"是否该执行拆除"时,缺失的前置条件应触发**降级**,不是报错返回。
- 纯逻辑测试免 root(用假 runner/假 client),不碰真实路由、DNS 或设备。
- 不得在实现期间执行 `bx up`/`down`/`reconnect`/`dns on|off`/`networksetup`/`pfctl`/`launchctl` 改动类命令。
- TDD:先写失败测试→跑红→最小实现→跑绿→提交。中文 conventional commits,结尾带 `Co-Authored-By: Claude …`。
- 验证:`go build ./... && go vet ./... && go test ./...`;`go test -race ./internal/guardian ./internal/cli`;跨平台 `GOOS=linux/windows GOARCH=amd64 go build -o /dev/null ./...`。

---

### Task 1: `bx down` 在无默认网关时降级而非失败

**Files:**
- Modify: `internal/guardian/manager.go:462-466`
- Test: `internal/guardian/manager_down_test.go`(新建)

**Interfaces:**
- Consumes: 既有 `barrierContextForRuntime`、`contextForRuntime`、`blockOnlyRecoveryContext`(`manager.go`)
- Produces: 无新导出接口

**根因:** `Down` 要求解析默认网关来构造屏障上下文,失败即返回错误。而网络故障(正是最需要关闭的时刻)必然没有网关。**同一个包里 `installBarrierForRecovery` 已有兜底**:

```go
func (m *Manager) installBarrierForRecovery(ctx context.Context, state supervisor.RuntimeState) error {
	barrierContext, err := m.barrierContextForRuntime(ctx, state)
	if err != nil {
		barrierContext = blockOnlyRecoveryContext(m.contextForRuntime(state))
	}
	return m.installBarrier(ctx, barrierContext)
}
```

`Down` 没用它。block-only 屏障去掉了 ServerBypass 并强制 `BlockIPv6`,对"即将关闭"的场景是**更安全**的选择。

- [ ] **Step 1: 写失败测试**

创建 `internal/guardian/manager_down_test.go`。先阅读同包既有测试(如 `manager_test.go`)里构造 `Manager` 与假依赖的既有辅助函数,**复用它们**,不要另起一套。测试要表达的行为:

```go
package guardian

import (
	"context"
	"errors"
	"testing"
)

// 网络故障时没有默认网关——而这正是用户最需要关掉 bx 的时刻。
// Down 必须降级为 block-only 屏障继续拆除,而不是把用户锁在断网状态。
func TestDownFallsBackToBlockOnlyBarrierWhenGatewayUnavailable(t *testing.T) {
	m := newTestManagerForDown(t)
	m.gatewayProvider = failingGatewayProvider{err: errors.New("default gateway not found")}

	if err := m.Down(context.Background()); err != nil {
		t.Fatalf("无网关时 Down 必须成功降级,实际返回错误: %v", err)
	}
	installed := m.testBarrierInstalls()
	if len(installed) == 0 {
		t.Fatal("Down 仍应装屏障保护拆除过程")
	}
	last := installed[len(installed)-1]
	if !last.blockOnly {
		t.Error("无网关时应降级为 block-only 屏障")
	}
	if len(last.ServerBypass) != 0 {
		t.Error("block-only 屏障不得保留 ServerBypass")
	}
	if !last.BlockIPv6 {
		t.Error("block-only 屏障必须阻断 IPv6")
	}
}
```

`newTestManagerForDown`、`failingGatewayProvider`、`testBarrierInstalls` 若同包已有等价物则直接复用;没有就在本测试文件内实现最小版本(假 store/runner/health/barrier/dns,barrier 记录每次 Install 收到的 `BarrierContext`)。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian/ -run TestDownFallsBackToBlockOnlyBarrier -count=1`
Expected: FAIL,错误信息包含 `resolve down barrier gateway`

- [ ] **Step 3: 最小实现**

把 `internal/guardian/manager.go:462-466`:

```go
	barrierContext, err := m.barrierContextForRuntime(ctx, runtimeState)
	if err != nil {
		m.needsAttention(desired, "barrier_gateway_unavailable")
		return fmt.Errorf("resolve down barrier gateway: %w", err)
	}
```

改为:

```go
	// 网络故障时解析不到默认网关,而这正是用户最需要关闭保护的时刻。
	// 降级为 block-only 屏障(去掉 bypass、强制阻断 IPv6)继续拆除——这比
	// 带 bypass 的屏障更收紧,不会放松 fail-closed。与
	// installBarrierForRecovery 的处理保持一致。
	barrierContext, err := m.barrierContextForRuntime(ctx, runtimeState)
	if err != nil {
		barrierContext = blockOnlyRecoveryContext(m.contextForRuntime(runtimeState))
	}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./internal/guardian/ -run TestDownFallsBackToBlockOnlyBarrier -count=1
go test ./internal/guardian/ -count=1
```
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add internal/guardian/manager.go internal/guardian/manager_down_test.go
git commit -m "fix(guardian): 无默认网关时 bx down 降级屏障而非失败"
```

---

### Task 2: 路径恢复的重试上限

**Files:**
- Modify: `internal/guardian/path_recovery.go`(`shouldRetryPathRecovery` 及常量区)
- Test: `internal/guardian/path_recovery_test.go`(追加)

**Interfaces:**
- Consumes: 既有 `pathRecoveryTransaction`、`RecoverySnapshot`、`DesiredState`
- Produces: `const maxPathRecoveryAttempts = 20`

**根因:** `runPathRecovery` 是 `for {}`,退避上限仅 `pathRecoveryRetryMax = 5 * time.Second`,**没有次数或时间上限**。事故中重试 178 次、71 分钟,每轮新起隧道进程,永远不停到用户可处理的状态。

`shouldRetryPathRecovery` 是纯函数,是加上限的正确落点——它已经拿到 `transaction`(内含 `snapshot.Attempt`)。返回 false 时循环自然结束、恢复标记为失败,状态落到 needs_attention,用户可见可处理。

上限取 20:事故日志中每轮约 25 秒(20s 健康超时 + 5s 退避),20 次约 8 分钟。足够覆盖真实的瞬时网络抖动,又不会让用户干等一小时。

- [ ] **Step 1: 写失败测试**

在 `internal/guardian/path_recovery_test.go` 末尾追加:

```go
// 事故中同一次恢复重试了 178 次、71 分钟,永远不停到用户可处理的状态。
// 超过上限必须放弃,让状态落到 needs_attention。
func TestShouldRetryPathRecoveryStopsAtAttemptCap(t *testing.T) {
	newTransaction := func(attempt int) pathRecoveryTransaction {
		return pathRecoveryTransaction{
			request:  RecoveryRequest{Reason: "underlay_changed", Generation: "gen-1"},
			snapshot: RecoverySnapshot{Attempt: attempt},
		}
	}
	failed := RecoverySnapshot{State: "failed", ErrorCode: "recovery_failed"}

	if !shouldRetryPathRecovery(newTransaction(1), failed, DesiredOn) {
		t.Error("首次失败应当重试")
	}
	if !shouldRetryPathRecovery(newTransaction(maxPathRecoveryAttempts-1), failed, DesiredOn) {
		t.Error("未达上限时应当继续重试")
	}
	if shouldRetryPathRecovery(newTransaction(maxPathRecoveryAttempts), failed, DesiredOn) {
		t.Error("达到上限必须停止重试,否则用户会被无限重试困住")
	}
	if shouldRetryPathRecovery(newTransaction(178), failed, DesiredOn) {
		t.Error("远超上限必须停止(事故中实际到了 178 次)")
	}
}

func TestPathRecoveryAttemptCapIsBounded(t *testing.T) {
	if maxPathRecoveryAttempts <= 0 || maxPathRecoveryAttempts > 60 {
		t.Errorf("重试上限 = %d,应为正且不过大(事故中 178 次/71 分钟是不可接受的)", maxPathRecoveryAttempts)
	}
}
```

如果既有测试文件里已有构造 `pathRecoveryTransaction` 的辅助函数,优先复用它而不是新建 `newTransaction`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian/ -run 'TestShouldRetryPathRecoveryStopsAtAttemptCap|TestPathRecoveryAttemptCapIsBounded' -count=1`
Expected: FAIL,`undefined: maxPathRecoveryAttempts`

- [ ] **Step 3: 最小实现**

在 `internal/guardian/path_recovery.go` 的常量区(`pathRecoveryRetryMax` 附近)加:

```go
	// maxPathRecoveryAttempts 是同一次路径恢复的重试上限。
	//
	// 退避上限只有 5 秒,若不设次数上限,恢复会以约 25 秒一轮无限重试
	// (真实事故中达到 178 次、71 分钟),每轮新起一个隧道进程,且永远不会
	// 停到用户可处理的状态。达到上限后放弃,让状态落到 needs_attention,
	// 用户可以看到并采取行动(换服务器、关闭保护)。
	maxPathRecoveryAttempts = 20
```

在 `shouldRetryPathRecovery` 的首个判断块中加入上限条件:

```go
func shouldRetryPathRecovery(transaction pathRecoveryTransaction, result RecoverySnapshot, desired DesiredState) bool {
	if desired != DesiredOn ||
		transaction.request.Reason != "underlay_changed" ||
		transaction.request.Generation == "" ||
		result.State != "failed" ||
		transaction.snapshot.Attempt >= maxPathRecoveryAttempts {
		return false
	}
	switch result.ErrorCode {
	case "capture_invalid", "capture_missing", "recovery_canceled", "recovery_unavailable", "underlay_unavailable":
		return false
	default:
		return true
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./internal/guardian/ -run 'TestShouldRetryPathRecovery|TestPathRecoveryAttemptCap' -count=1
go test ./internal/guardian/ -count=1
go test -race ./internal/guardian -count=1
```
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add internal/guardian/path_recovery.go internal/guardian/path_recovery_test.go
git commit -m "fix(guardian): 路径恢复加重试上限,不再无限重试"
```

---

### Task 3: `bx down` 不再依赖启动 Guardian

**Files:**
- Modify: `internal/cli/guardian.go`(`macOSDownLifecycle` 及其依赖装配)
- Test: `internal/cli/macos_lifecycle_test.go`(追加)

**Interfaces:**
- Consumes: 既有 `macOSLifecycleDeps`(含 `guardianReady`、`client`)、`ensureGuardianOwnership`
- Produces: 无新导出接口

**根因:** `macOSDownLifecycle` 先调 `ensureGuardianOwnership`,后者会**安装 Guardian 服务、bootstrap 启动、等待 socket 就绪**,任一步失败即返回错误,`Down` 根本走不到。"关闭"依赖"先成功启动被关闭者",逻辑倒置。

对照 `uninstallDarwinAction`:它不要求 Guardian 启动,而是 `ensureGuardianNotRunningForUninstall()` 把它**停掉**——core 的 ctx 被取消,`supervisor.Run` 的 defer 自动还原路由与 DNS(事故日志中的 `ctx 取消,还原中…`)。拆除能力在 core 的 defer 里,由进程终止触发。

**修复原则:** `down` 的目标是"让保护停止且网络还原"。Guardian 可达时走正常事务(最干净);Guardian 不可达时**不应报错**,而应退化为"停止 Guardian 让 core 的 defer 还原",与 uninstall 同源。

- [ ] **Step 1: 写失败测试**

在 `internal/cli/macos_lifecycle_test.go` 末尾追加。先阅读该文件里既有的 `macOSLifecycleDeps` 假依赖构造方式并复用:

```go
// 关闭保护不该依赖"先把 Guardian 成功启动起来"——Guardian 起不来正是用户
// 最需要关掉 bx 的时刻。事故中用户因此完全无法关闭,只能 uninstall 脱身。
func TestMacOSDownDoesNotRequireGuardianBootstrap(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return false }
	deps.enableGuardian = func() error { return errors.New("bootstrap Guardian: launchctl failed") }

	if _, err := macOSDownLifecycle(context.Background(), "/etc/bx/config.yaml", deps); err != nil {
		t.Fatalf("Guardian 起不来时 down 仍必须完成拆除,实际返回错误: %v", err)
	}
	if !deps.forcedTeardownCalled() {
		t.Error("Guardian 不可达时应退化为强制拆除(停止 Guardian,由 core 的 defer 还原)")
	}
}

// Guardian 正常时仍走干净的事务路径,不要因为加了兜底就绕过它。
func TestMacOSDownUsesGuardianTransactionWhenAvailable(t *testing.T) {
	deps := newFakeMacOSLifecycleDeps()
	deps.guardianReady = func(context.Context) bool { return true }

	if _, err := macOSDownLifecycle(context.Background(), "/etc/bx/config.yaml", deps); err != nil {
		t.Fatal(err)
	}
	if !deps.clientDownCalled() {
		t.Error("Guardian 可达时应走正常的 Down 事务")
	}
	if deps.forcedTeardownCalled() {
		t.Error("Guardian 可达时不应触发强制拆除")
	}
}
```

`newFakeMacOSLifecycleDeps`、`forcedTeardownCalled`、`clientDownCalled` 若文件内已有等价物则复用;没有就实现最小版本。`macOSLifecycleDeps` 需要新增一个可注入的强制拆除钩子(命名建议 `forceTeardown func(context.Context) error`),生产实现复用 `uninstallDarwinAction` 中停止 Guardian 的那段逻辑(**只停止,不删除任何文件、不动配置**)。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli/ -run 'TestMacOSDownDoesNotRequireGuardianBootstrap|TestMacOSDownUsesGuardianTransactionWhenAvailable' -count=1`
Expected: FAIL(第一个测试因 `bootstrap Guardian` 错误而失败)

- [ ] **Step 3: 实现**

改 `macOSDownLifecycle`:Guardian 已就绪时走原有路径;不就绪时**跳过 `ensureGuardianOwnership` 的安装/bootstrap**,直接调用注入的 `forceTeardown`。

关键点:
- **不要**为了关闭而去安装或 bootstrap Guardian。
- 强制拆除只**停止**服务,不删文件、不动 `/etc/bx` 配置——那是 `uninstall` 的职责,不是 `down` 的。
- 强制拆除失败时,错误信息必须明确告诉用户下一步(例如提示 `sudo bx uninstall` 或手动 `launchctl bootout`),不能只丢一个底层错误。

`macOSDownAction` 的输出文案也要相应调整:走了强制拆除路径时,应如实说明"Guardian 未响应,已强制停止并还原网络",而不是笼统的成功文案。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./internal/cli/ -run 'TestMacOSDown' -count=1
go test ./internal/cli/ -count=1
go test -race ./internal/cli -count=1
```
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add internal/cli/guardian.go internal/cli/macos_lifecycle_test.go
git commit -m "fix(cli): bx down 不再依赖启动 Guardian,不可达时强制拆除"
```

---

### Task 4: 文档与全量验证

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

- [ ] **Step 1: README 补故障自救**

在 macOS 故障排查相关位置补一段:

```markdown
**关不掉怎么办**:`sudo bx down` 在 Guardian 无响应或网络故障(解析不到默认网关)
时也能完成拆除——前者会强制停止 Guardian 并由 core 还原网络,后者会降级为纯阻断
屏障继续拆除。若 `down` 仍失败,`sudo bx uninstall` 会停止全部服务并还原网络
(保留 `/etc/bx` 配置,便于重装)。
```

- [ ] **Step 2: CLAUDE.md 记录不变量**

在 macOS 段落补一句,记录本轮确立的原则:**关闭与恢复路径不得依赖可能已失效的前置条件**(网关缺失→降级 block-only 屏障;Guardian 不可达→强制拆除;恢复重试有上限 `maxPathRecoveryAttempts`),并注明这些是真实事故(2026-08-04,恢复重试 178 次/71 分钟致用户无法关闭)的修复。

- [ ] **Step 3: 全量验证**

```bash
go build ./... && go vet ./... && go test ./... -count=1
go test -race ./internal/guardian ./internal/cli -count=1
GOOS=linux   GOARCH=amd64 go build -o /dev/null ./...
GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
GOOS=darwin  GOARCH=arm64 go build -o /dev/null ./...
scripts/test-macos-menu.sh
git diff --check
```
Expected: 全部 rc=0

- [ ] **Step 4: 提交**

```bash
git add README.md CLAUDE.md
git commit -m "docs(macos): 记录关闭路径的降级语义与恢复重试上限"
```

- [ ] **Step 5: 停在真机验证之前**

不要执行 `bx up`/`down`/`reconnect`/`dns on|off`、`networksetup`、`launchctl` 改动类命令。向用户报告自动化验证结果,并给出真机验收建议:

```bash
# 1. 正常路径:Guardian 健康时 down 仍走干净事务
sudo bx up && bx status
sudo bx down && bx status

# 2. 无网关降级:断开所有网络后再 down(应成功而非报错)
#    (拔网线/关 Wi-Fi 后执行)
sudo bx up
# 关闭 Wi-Fi
sudo bx down          # 期望:成功,提示降级为纯阻断屏障

# 3. Guardian 不可达:强制杀掉 Guardian 后 down
sudo bx up
sudo launchctl bootout system/com.getbx.bx.guard
sudo bx down          # 期望:成功,提示已强制停止并还原

# 4. 重试上限:配一个不可达的服务器,观察恢复在约 20 次后停到 Needs Attention
#    而不是无限重试(对照 bx logs 里的 attempt 计数)
```
