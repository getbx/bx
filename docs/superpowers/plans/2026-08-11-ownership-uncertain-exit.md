# 所有权不确定:给这条 fail-closed 拒绝一个出口 —— Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Guardian 的「Core 所有权不确定」这条 fail-closed 拒绝有一个真实存在的出口 —— `Down` 真的清掉它、用户发起的 `Up`/`Migrate` 每次重新求证而不是把第一次的结论钉死、锁存不再升级成挡住 `Down` 的 `recoveryBlocked`、以及那个「OS 已经确认进程没了、只是删不掉一个 JSON」的产地不再产出这条判定。

**Architecture:** 三处改动方向不同,必须分清:①**放宽记忆,不放宽判据** —— `Down` 清掉的是内存里的一条结论,而下一次 `Up` 会重新走 `runner.Existing`/`Start`,`ExecCoreRunner` 在那两跳上照旧向系统求证(`resolveOrphanLaunchMarker` / `refuseUnrecordedRunningCore` / `refuseLiveLaunchMarker`);②**把「问不问」改掉,「怎么判」一个字不改** —— 用户发起的 `Up`/`Migrate` 把 `m.current.Uncertain` 的短路换成一次带证明纪律的复查:**释放锁存要求两次扫描全干净、中间隔一个沉降窗口**(`confirmNoCoreForRelease`),扫不动/扫到了/runner 不会扫/panic 一律保持拒绝;③**对两处已经握着安全证明的产地停手**(`finishExistingWatch` 与 `Start` 自己那条 wait goroutine)。调谐器一侧一个字不改。

**一条不能混淆的取舍**:`confirmCoreStopped`(`Down`/`Recover` 用)与 `confirmNoCoreForRelease`(重新求证用)**偏置方向相反,必须是两个函数**。前者回答「能不能对用户说已关闭」,怕的是把每一次正常关闭都报成告警,所以第一次扫脏之后沉降重扫、第二次干净就算数;后者回答「能不能再起一个 Core」,怕的是一次假的「全清」(`cleanupFailedStart` 的残留:子进程已 fork、`Terminate()` 已发、wait 超时,进程既不是僵尸也还没消失,而 `scanRunningCores` 会跳过 argv 读不出的进程),所以**两次都必须干净**。把两者「统一」掉是本期最容易犯、后果最重的错误。

**Tech Stack:** Go 1.26(`internal/guardian`、`internal/cli`)+ Swift(`apps/macos/BxMenu`,`swiftc` 直编直跑的 `Tests/`,不是 XCTest)。无新依赖。

## Global Constraints

以下每一条对**每个** task 都成立,不再逐条重复。

- **TDD**:先写失败测试 → 跑红(并把红的输出看在眼里)→ 最小实现 → 跑绿 → 提交。任何一步不许跳过,尤其不许「先写实现再补测试」。
- **提交**:中文 conventional commits,结尾带 `Co-Authored-By: Claude <noreply@anthropic.com>`;**直接提交在 `master`**(单人项目),不开分支、不发 PR。
- **验证命令**(每个 task 的验证步骤至少跑前两条,改到跨平台/Swift 的 task 跑全部):
  - `go build ./... && go vet ./... && go test ./... -count=1`
  - `go test -race ./internal/guardian ./internal/cli -count=1`
  - 交叉编译:`GOOS=linux GOARCH=amd64 go build -o /dev/null ./...`,以及 `linux/arm64`、`darwin/amd64`、`darwin/arm64`、`windows/amd64`、`windows/arm64` 各一遍
  - `swift build --package-path apps/macos/BxMenu`
  - `bash scripts/test-macos-menu.sh`
  - `git ls-files '*.go' | grep -v 'embedded/assets\|internal/winfw' | xargs "$(go env GOPATH)/bin/gofumpt" -l` —— **按打印出来的内容判断**(它退出码永远是 0;打印出任何文件名就是不合格,打印为空才算过)
- **实现者绝不许运行**:`bx`、`sudo bx`、`launchctl`、`route`、`networksetup`、`ifconfig`。这台机器上跑着真实的 bx,启动/停止/改路由是用户的事。
- **绝不许**:`git stash`、`git reset`、`git checkout -- <path>`、`git clean`。要临时改一个文件做变异,先做字节快照(见下),改完按快照原样写回。
- **变异验证协议**(每个 task 的最后一步都要做,不许省):
  1. 快照:`cp <file> /tmp/mut-$(basename <file>).bak`
  2. 按该 task 列出的每一条变异,逐条改产品代码(**不是测试代码**)
  3. **不带 `-run` 跑整包**:`go test ./internal/guardian -count=1`(或该 task 指明的包)。本仓库里 `-run` 静默匹配不到任何测试的事故发生过不止一次,过滤跑出来的绿是无效证据。
  4. 记下**是哪几条测试红了**,确认里面有本 task 新写的那条
  5. `cp /tmp/mut-<file>.bak <file>` 原样写回,**再跑一遍整包确认全绿**(不重新证绿的话,你不知道自己有没有把产品代码改坏)
- **准入控制是本期改的东西。** 起第二个 Core 是这个系统里最坏的结果 —— 两个 Core 争默认路由,先退出的那个用自己的旧快照还原、掀掉另一个的劫持,于是 `bx status` 显绿而流量明文直连。**本计划里每一处「让某件事通过」的改动,都必须有一条专门盯着那一处放宽的测试,并且做过变异验证。**没有这条测试的放宽不许提交。
- **平台事实**(写测试与注释时都用得上):
  - Guardian 只有 darwin 实体(`daemon.go` 的 `requireDaemonPlatform()` 在构造 `ExecCoreRunner` 之前就挡住别的平台),但 `internal/guardian` 包本身在 linux/windows 上照常编译与跑单测(CI 三平台矩阵)。
  - 集成台(`internal/supervisor/harness*_netns_linux_test.go`)只覆盖 Linux 控制面,**本期一行都覆盖不到**。
  - `procscan_other.go` 的 `scanRunningCores` 在非 darwin 恒返回 `errCoreScanUnsupported` ⇒ `confirmNoCoreForRelease` 恒 `false` ⇒ **重新求证在非 darwin 是 no-op,门仍然焊死**。这句话必须以注释形式写进代码(Task 6),并有一条 `//go:build !darwin` 的回归测试钉住。
  - CI 的 `macos-app` job **确实**跑 `swift build --package-path apps/macos/BxMenu` 与 `bash scripts/test-macos-menu.sh`;但 `Tests/` 下的文件**不属于任何 SwiftPM target**,没在 `scripts/test-macos-menu.sh` 里注册的测试文件永远不会被执行,而 CI 照样全绿。注册由 `internal/cli/macos_menu_suite_registration_test.go` 守着 —— 新增 Swift 测试文件必须同时改脚本。
- **本期不做**(设计已定,不要顺手做):不给调谐器执行权;不动 `retainUncertain` 覆盖 `m.current` 顶掉健康 Core `Exit` 句柄那个正交问题;不动非 darwin 的扫描实现。

## 文件地图

| 文件 | 本期承担什么 |
|---|---|
| `internal/guardian/manager.go` | 锁存的全部生命周期:产生(`retainUncertain`、`handleUnexpectedExit` 内联那一处)、消费(`upLocked`/`Migrate` 的短路)、清除(`Down` 的新 defer、`clearUncertaintyAfterProof`)、升级(`recoverLocked` 置 `recoveryBlocked`)。新增 `uncertainCause` 字段、`clearOwnershipLatch`、`recheckOwnershipUncertain`、`upOrigin`。 |
| `internal/guardian/process.go` | 两个 (c) 类产地停止产出 uncertain(`finishExistingWatch` 与 `Start` 里那条 wait goroutine,分两次提交);`retainedUncertainCause` 辅助;`ownershipUncertainEscapeHint` 文案。 |
| `internal/guardian/update.go` | 只因 `retainUncertain` 签名变化而改(4 处调用点),行为不变。 |
| `internal/guardian/manager_latch_test.go` | **新建**。本期 Manager 级的全部新测试都住这里(`Down` 清锁存、不清自己新造的、`recoveryBlocked` 不升级、重新求证的四种结局、启动恢复不求证)。 |
| `internal/guardian/manager_confirm_test.go` | 已有的 `scannerOnlyRunner`/`nonScanningRunner`/`scriptedScanner` 供新测试复用,**不改**。 |
| `internal/guardian/manager_test.go` | 改两处:`fakeCoreRunner` 加一个扫描计数器;`TestManagerHealthFailureUsesLiveBoundedCleanupAndBlocksRetryWhenExitUnproven` 改成注入诚实扫描器(Task 7)。 |
| `internal/guardian/process_test.go` | 加 Watch 路径的新断言 + 「Stop 那几处仍然报 uncertain」的越界守卫(Task 3)。 |
| `internal/guardian/procscan_other_test.go` | **新建**(`//go:build !darwin`),钉住非 darwin 的门是焊死的。 |
| `internal/cli/client.go` | `guardianCodeHints["core_ownership_uncertain"]` 文案。 |
| `internal/cli/upgraderun.go` | 升级中止文案里那句「只有 sudo bx down 再 sudo bx up 能解」。 |
| `apps/macos/BxMenu/Sources/BxMenu/ToggleController.swift` | `toggleFailureHint` 的 `core_ownership_uncertain` 分支。 |
| `apps/macos/BxMenu/Tests/ToggleControllerTests.swift`、`Tests/GuardianFailureCodeTests.swift` | 跟着改断言(已注册在脚本里,不需要改脚本)。 |
| `CLAUDE.md` | 「已知限制:所有权不确定是锁存的……唯一脱身办法是 sudo bx down 再 sudo bx up」那一段,现在有两处不成立。 |

## 任务顺序与「每次提交树都是安全的」

顺序不是随意的,理由逐条写在这里:

1. **Task 1(`Down` 清锁存)排第一**:价值最高、改动最小、**不依赖后面任何一条**,而且方向上只增加逃生口。被文档/CLI/菜单/升级文案四处引用的那条出路今天是假的,先把它变成真的。
2. **Task 2(`recoveryBlocked` 不升级)排第二**:它保护的是 Task 1 才刚修好的那条路 —— 锁存若还能升级成 `recoveryBlocked`,`Down` 第一句就返回 `errRecoveryIncomplete`,Task 1 的 defer 一样跑不到有意义的地方。两条一起才是完整的「关得掉」。
3. **Task 3 与 Task 4(两个 (c) 类产地)独立,且刻意分成两次提交**:它们减少锁存的**产生**,与前两条无耦合。两处形状相同(OS 已确认进程没了,失败的只是删一个 JSON),但分开提交是有意的 —— 它们与重新求证那条线无关,出问题时应当能**各自独立回退**,不必把整期撤掉。
4. **Task 5(保留 cause)在重新求证之前**:重新求证失败时要带着当初那个 cause 报出来,cause 得先有地方存。它自身也有独立价值(今天锁存后的错误比第一次**更少**信息)。
5. **Task 6(求证助手,不接线)**:纯新增,谁也不影响,可以单独把释放与拒绝的每一种结局钉死。**这一步之后树的行为一个字没变。**
6. **Task 7(接进 `upLocked`)是风险最高的一步**,所以排在所有安全网都装好之后:此时 `Down` 已能清、`recoveryBlocked` 已不会升级、cause 已保留、助手已被单独证明过。也是在这一步正面处理 `manager_test.go:1034`。
7. **Task 8(`Migrate`)**紧随其后,同一套助手,只是第二个入口。
8. **Task 9/10(消费方文案与文档)**排最后:文案描述的是最终行为,提前改就是让文档撒另一种谎。

---

### Task 1: `Down` 无条件清掉进门时那个所有权锁存

**Files:**
- Modify: `internal/guardian/manager.go`(`Down` 开头,`defer m.releaseMutation()` 之后;新增 `clearOwnershipLatch` 方法)
- Test: `internal/guardian/manager_latch_test.go`(新建)

**Interfaces:**
- Produces: `func (m *Manager) clearOwnershipLatch(latchedOnEntry Process)` —— 后续 Task 5 会在它里面补一行清 `m.uncertainCause`。
- Consumes: 既有 `Manager.current Process`、`newManagerTestEnv`/`newProtectedManagerTestEnv`/`eventually`/`fakeCoreRunner`(`manager_test.go`)。

**背景(动手前读一遍)**:`Down` 从不读 `Uncertain`,它按 `m.current.PID` 分支。三种锁存形状分别停在:① `desired=off` 且 PID==0 → `manager.go:784` 的提前返回;② `desired=on` 且 PID==0 → `runner.Existing`(跑的正是当初把它锁上的那次扫描,同样失败);③ 有真实 PID(`handleUnexpectedExit`)→ `runner.Stop` 开头的 `verifyInstalledProcess` 对一个从没验明身份的进程必然失败。**三条都到不了 `m.current = Process{}`**(`:846`)。

- [ ] **Step 1: 写失败测试(三种形状 + 一条反向守卫)**

新建 `internal/guardian/manager_latch_test.go`:

```go
package guardian

import (
	"context"
	"errors"
	"testing"
)

// 形状一:PID==0(扫描派生的锁存、孤儿 launching 标记)且盘上 desired=off。
// Down 走 manager.go:784 的提前返回,根本到不了那句 m.current = Process{}。
// 这条路今天 Down 报成功、锁存原样留着,而 CLI/菜单/升级文案四处都在告诉用户
// 「down 再 up 就能清」。
func TestDownClearsTheOwnershipLatchOnTheEarlyReturnPath(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	env.manager.current = Process{Uncertain: true}

	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("Down = %v, want nil(这条形状本来就该干净返回)", err)
	}
	if env.manager.current.Uncertain {
		t.Fatal("提前返回那条路没有清掉锁存 —— 被四处文案引用的那条出路在最常见的形状下是假的")
	}
}

// 形状二(最常见):PID==0 且 desired=on —— 第一次失败的 Up 已经把 desired
// 写成 on。Down 落到 runner.Existing,而那正是当初把它锁上的同一次扫描:
// 持久条件还在就返回同一个错,Down 以 core_state_read_failed 失败。
// **失败也必须清掉锁存** —— 清除对拆除的成败无条件。
func TestDownClearsTheOwnershipLatchEvenWhenTeardownFails(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.manager.current = Process{Uncertain: true}
	env.runner.existingErr = uncertainOwnership(
		Process{Uncertain: true},
		errors.New("no Core process record on disk, and scanning for running Cores failed: sysctl"),
	)

	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("测试前提不成立:这条形状应当在 runner.Existing 上失败,否则测不到「失败路径也要清」")
	}
	if env.manager.current.Uncertain {
		t.Fatal("拆除失败时没有清掉锁存 —— 而用户此刻最需要的正是能重新开始")
	}
}

// 形状三:有真实 PID(handleUnexpectedExit 内联那一处)。Down 走到 runner.Stop,
// Stop 开头的 verifyInstalledProcess 对一个从没验明身份的进程必然失败。
func TestDownClearsTheOwnershipLatchCarryingARealPID(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	process := env.runner.currentProcess()
	env.runner.exit(process.PID, uncertainOwnership(process, errors.New("clear owned Core record after exit failed")))
	eventually(t, func() bool { return env.manager.Status().LastError == "core_ownership_uncertain" })
	if !env.manager.current.Uncertain {
		t.Fatal("测试前提不成立:这次退出没有留下锁存")
	}
	env.runner.stopErr = errors.New("verify recorded Core PID before shutdown: identity never verified")

	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("测试前提不成立:这条形状应当在 runner.Stop 上失败")
	}
	if env.manager.current.Uncertain {
		t.Fatal("带真实 PID 的锁存没有被清掉")
	}
}

// **反向守卫:Down 不许清掉它自己刚造出来的那个锁存。**
//
// Down 在 DNS 还原失败时会把 Core 放回去(startCoreLocked)。那一步可能刚刚
// 落下一个**新的**锁存 —— 一个无条件的 defer 会把它一起抹掉,而那是 fail-open:
// 系统里可能真有一个没验明身份的 Core 在跑,下一次 Up 却畅通无阻。
func TestDownDoesNotClearALatchItsOwnRestartJustCreated(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.dns.restoreErr = errors.New("resolver restore failed")
	env.runner.startErr = uncertainOwnership(
		Process{PID: 4242, Uncertain: true},
		errors.New("no Core process record on disk, but Core appears to be running (PID 4242)"),
	)

	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("测试前提不成立:DNS 还原失败 + 重启失败时 Down 应当报错")
	}
	if !env.manager.current.Uncertain {
		t.Fatal("Down 把它自己在还原补偿里新造的锁存也抹掉了 —— 那是 fail-open")
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/guardian -count=1`
Expected: 前三条 FAIL(`没有清掉锁存`),第四条 PASS(今天根本没有清除逻辑,所以它平凡地绿 —— 它的价值在 Step 4 之后才出现,现在先让它在场)。把红的输出看在眼里再往下。

- [ ] **Step 3: 最小实现**

在 `manager.go` 的 `Down` 里,`defer m.releaseMutation()` **紧随其后**(排在维护挂起那个 defer 与 `m.recoveryBlocked` 检查之前;defer 是 LIFO,注册得越早跑得越晚,这正是我们要的「最后一个跑」):

```go
	// **锁存必须在这条路上消失。**
	//
	// 被文档、CLI 指引、菜单提示、升级中止文案四处引用的那条出路(「sudo bx down
	// 再 sudo bx up」)今天在三种形状下都是假的:Down 从不读 Uncertain,它按
	// m.current.PID 分支,于是分别停在 :784 的提前返回、runner.Existing 的同一次
	// 失败扫描、以及 runner.Stop 开头那次对「从没验明身份的进程」必然失败的
	// verifyInstalledProcess —— 全都到不了下面那句 m.current = Process{}。
	// (用户报告它「有时管用」,是因为 CLI 的 bx down 失败会落到强制拆除,把
	// Guardian 整个 bootout 掉:真正管用的一直是杀掉 daemon,不是 Down 清了记录。)
	//
	// **与拆除的成败无关**:注册在最外层,每一条失败路径都会跑到。
	//
	// **清掉锁存不等于放行**:下一次 Up 会重新走 runner.Existing/Start,而
	// ExecCoreRunner 在那两跳上照旧向系统求证(resolveOrphanLaunchMarker /
	// refuseUnrecordedRunningCore / refuseLiveLaunchMarker)。这里去掉的是一个
	// **记忆**,不是那道判据。
	latchedOnEntry := m.current
	defer m.clearOwnershipLatch(latchedOnEntry)
```

在 `retainUncertain` 附近新增:

```go
// clearOwnershipLatch 清掉**进 Down 那一刻就已经在那儿**的所有权锁存。
//
// 只清那一个:Down 在 DNS 还原失败时会经 startCoreLocked 把 Core 放回去,那一步
// 可能刚落下一个新的锁存 —— 抹掉它就是 fail-open。Process 全部字段都是
// int/string/bool/chan,是可比较类型,直接按值比对身份。
func (m *Manager) clearOwnershipLatch(latchedOnEntry Process) {
	if !latchedOnEntry.Uncertain {
		return
	}
	if !m.current.Uncertain || m.current != latchedOnEntry {
		return
	}
	m.current = Process{}
	log.Printf("guardian_ownership_uncertain_latch_cleared by=down pid=%d", latchedOnEntry.PID)
}
```

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/guardian -count=1` 与 `go test -race ./internal/guardian -count=1`
Expected: PASS(四条全绿,且既有测试无一转红)。

- [ ] **Step 5: 全量验证**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Run: `git ls-files '*.go' | grep -v 'embedded/assets\|internal/winfw' | xargs "$(go env GOPATH)/bin/gofumpt" -l`(**看打印内容**,须为空)

- [ ] **Step 6: 变异验证**(按 Global Constraints 的协议,不带 `-run` 跑整包)

| 变异(改 `manager.go`) | 必须转红的测试 | 它证明了什么 |
|---|---|---|
| 删掉 `defer m.clearOwnershipLatch(latchedOnEntry)` 这一行 | 前三条 | 清除确实是新加的这行在做,不是别处顺手做掉的 |
| 把 defer 挪到 `m.recoveryBlocked` 检查**之后**、并在 `latchedOnEntry := m.current` 之前 `return` 的那条早退路径之外 —— 具体做法:把整段挪到 `desired, err := m.store.LoadDesired()` 之后 | `TestDownClearsTheOwnershipLatchOnTheEarlyReturnPath` 仍绿、但把 `LoadDesired` 改为返回错误的那条既有测试路径不再清 —— 若无既有测试转红,**记下来**:说明「早退路径也要清」只由第一条守着,这是可接受的,但要在提交信息里写明 | 覆盖边界诚实可见 |
| 把 `m.current != latchedOnEntry` 改成 `true`(即无条件清) | `TestDownDoesNotClearALatchItsOwnRestartJustCreated` | 反向守卫真的在守 fail-open 那一头 |
| 把 `!latchedOnEntry.Uncertain` 的早退改成 `false`(即不管进门时有没有锁存都清) | `TestDownDoesNotClearALatchItsOwnRestartJustCreated` | 同上,换一个入口 |

每条变异跑完 `cp` 还原并**重新跑一遍整包证绿**。

- [ ] **Step 7: 提交**

```bash
git add internal/guardian/manager.go internal/guardian/manager_latch_test.go
git commit -m "$(cat <<'EOF'
fix(guardian): Down 真的清掉所有权锁存,三种形状全覆盖

被文档、CLI 指引、菜单提示、升级中止文案四处引用的那条出路(down 再 up)
在三种最常见的锁存形状下都是假的:Down 从不读 Uncertain,按 PID 分支,
分别停在提前返回、Existing 的同一次失败扫描、Stop 开头必然失败的身份校验,
全都到不了 m.current = Process{}。用户报告它「有时管用」,靠的是 CLI 失败
落到强制拆除把 Guardian 整个 bootout —— 真正管用的一直是杀掉 daemon。

清除对拆除成败无条件,但只清进门时那一个:Down 自己在 DNS 还原失败时会
把 Core 放回去,抹掉它新造的锁存就是 fail-open。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 所有权不确定不得升级成挡住 `Down` 的 `recoveryBlocked`

**Files:**
- Modify: `internal/guardian/manager.go:1070-1072`(`recoverLocked` 末尾那三行)
- Test: `internal/guardian/manager_latch_test.go`(追加)

**Interfaces:**
- Consumes: Task 1 的 `clearOwnershipLatch`(测试里 `Down` 能走通,依赖它)、既有 `errRecoveryIncomplete`、`ErrProcessOwnershipUncertain`。
- Produces: 无新符号。

**背景**:`recoverLocked` 调 `upLocked`,失败就置 `recoveryBlocked = true`;而 `recoveryBlocked` 是 `Up`、`Migrate`、**`Down`**(`:775`)与两个更新入口的第一句判断。于是一次开机时的瞬时 `sysctl` 失败,可以把「开不了保护」变成「**关不掉保护**」—— 2026-08-04 那次 71 分钟事故的形状。先例是 `hold.go` 对 `ErrMaintenanceHoldUnreadable` 的处置(`m.recoveryBlocked = !errors.Is(err, ErrMaintenanceHoldUnreadable)`),理由一字不差:**一个会挡住 `Down` 的栅栏,本身就是事故。**

- [ ] **Step 1: 写失败测试**

追加到 `internal/guardian/manager_latch_test.go`:

```go
// 启动恢复撞上所有权不确定时,失败照常上报、Core 照常不起 —— 但绝不许把
// 关闭的路一起堵死。recoveryBlocked 是 Down 的第一句判断,它为真时 Down 直接
// 返回 errRecoveryIncomplete:那正是 2026-08-04「用户 71 分钟关不掉保护」的机制。
func TestStartupRecoveryDoesNotBlockShutdownOnUncertainOwnership(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.manager.current = Process{Uncertain: true}

	if err := env.manager.Recover(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Recover = %v, want 所有权不确定(测试前提)", err)
	}
	if env.manager.recoveryBlocked {
		t.Fatal("所有权不确定把 recoveryBlocked 置真了 —— 「开不了」升级成了「关不掉」")
	}
	if err := env.manager.Down(context.Background()); errors.Is(err, errRecoveryIncomplete) {
		t.Fatal("Down 被 recoveryBlocked 挡住 —— 这正是 71 分钟事故的形状")
	}
}

// 反向:**别的**失败仍然要置 recoveryBlocked。这条改动只针对所有权不确定
// 这一种,放宽到全部就是把一道既有的栅栏顺手拆了。
func TestStartupRecoveryStillBlocksOnOtherUpFailures(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.runner.existingErr = errors.New("inspect recorded Core PID 42: permission denied")

	if err := env.manager.Recover(context.Background()); err == nil {
		t.Fatal("Recover 应当失败(测试前提)")
	}
	if !env.manager.recoveryBlocked {
		t.Fatal("非所有权类的启动恢复失败不再置 recoveryBlocked —— 这条既有栅栏被顺手拆了")
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/guardian -count=1`
Expected: `TestStartupRecoveryDoesNotBlockShutdownOnUncertainOwnership` FAIL(`recoveryBlocked 置真`);`TestStartupRecoveryStillBlocksOnOtherUpFailures` PASS。

- [ ] **Step 3: 最小实现**

`manager.go:1070-1072` 改为:

```go
	if err := m.upLocked(ctx); err != nil {
		// **「开不了」永不许升级成「关不掉」。**
		//
		// recoveryBlocked 是 Up、Migrate、**Down**(:775)与两个更新入口的第一句
		// 判断。所有权不确定最容易由一次**瞬时**失败产生(非 root、sysctl 抖动、
		// readable==0 下限),而 retryDaemonRecovery 会无限重试 Recover —— 一次
		// 开机时的瞬时失败若把这道栅栏落下,用户就再也关不掉保护,正是 2026-08-04
		// 那次 71 分钟事故的形状。
		//
		// 判据与 hold.go 对 ErrMaintenanceHoldUnreadable 的处置一字不差:
		// fail-closed 的拒绝照常上报、Core 照常不起,但栅栏不落。
		m.recoveryBlocked = !errors.Is(err, ErrProcessOwnershipUncertain)
		return err
	}
```

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/guardian -count=1` 与 `go test -race ./internal/guardian -count=1`
Expected: PASS。

- [ ] **Step 5: 全量验证**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Run: gofumpt 检查(看打印内容)

- [ ] **Step 6: 变异验证**

| 变异 | 必须转红 |
|---|---|
| 改回 `m.recoveryBlocked = true` | `TestStartupRecoveryDoesNotBlockShutdownOnUncertainOwnership` |
| 改成 `m.recoveryBlocked = false`(全部失败都不落栅栏) | `TestStartupRecoveryStillBlocksOnOtherUpFailures` |
| 把 `errors.Is(err, ErrProcessOwnershipUncertain)` 换成 `errors.Is(err, ErrProcessNotRunning)`(判据挑错哨兵) | 两条都红 |

还原 + 重新证绿。

- [ ] **Step 7: 提交**

```bash
git add internal/guardian/manager.go internal/guardian/manager_latch_test.go
git commit -m "$(cat <<'EOF'
fix(guardian): 所有权不确定不再升级成挡住 Down 的 recoveryBlocked

recoverLocked 里 upLocked 一失败就落栅栏,而 recoveryBlocked 是 Down 的第一句
判断:一次开机时的瞬时扫描失败于是把「开不了保护」变成「关不掉保护」——
2026-08-04 那次 71 分钟事故的形状,而 retryDaemonRecovery 无限重试也解不开。

判据与 hold.go 对 ErrMaintenanceHoldUnreadable 的处置同源:拒绝照常上报、
Core 照常不起,栅栏不落。别的失败仍然落栅栏,由独立测试守住。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `finishExistingWatch`(接管来的 Core)不再把「已证明安全」记成所有权不确定

**Files:**
- Modify: `internal/guardian/process.go:592-598`(`finishExistingWatch`)
- Test: `internal/guardian/process_test.go`(追加两条)

**Interfaces:**
- Produces: 无新符号(行为改变 + 一行新日志 `guardian_stale_core_record_after_exit`)。
- Consumes: 既有 `newRecordedProcessRunner(t) (*ExecCoreRunner, Process, *watchTestProcessOperations)`、`swapGuardianLogOutput(w io.Writer) func()`(在 `manager_test.go`,同包可用)。

**背景**:十五个 uncertain 产地里,`process.go:594` 是最荒谬的一个 —— **手里握着「安全」的证明**(两个调用方传进来的都是 `ErrProcessNotRunning` 那一族:OS 已确认进程消失,或记录的身份已经不在那个 PID 上),只因为删不掉一个 JSON 文件,就宣布所有权不确定。`/var/lib/bx` 上任何一次文件系统抖动都能锁死 daemon。与 `603b602` 当初对 `Existing()` 的判断同源。

**这是一处准入放宽**:改完之后,这类退出会走 `handleUnexpectedExit` 的正常分支(装屏障 + 重启 Core),而不是锁存拒绝。所以下面既有「放宽被看着」的测试,也有「不许顺手放宽别处」的越界守卫。

- [ ] **Step 1: 写失败测试**

追加到 `internal/guardian/process_test.go`:

```go
// OS 已经确认被观察的 Core 没了(watchExisting 的两个调用点传的都是
// ErrProcessNotRunning 那一族),失败的只是删一个 JSON 文件。握着「安全」的
// 证明却宣布所有权不确定,等于让 /var/lib/bx 上任何一次文件系统抖动锁死 daemon。
func TestAdoptedWatcherCleanupFailureIsNotOwnershipUncertainty(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	runner.RemoveProcessRecord = func(string) error { return errors.New("record removal failed") }
	var logs bytes.Buffer
	restore := swapGuardianLogOutput(&logs)
	defer restore()

	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	process = runner.Watch(process)
	operations.setAlive(false)

	select {
	case exitErr := <-process.Exit:
		if errors.Is(exitErr, ErrProcessOwnershipUncertain) {
			t.Fatalf("删不掉一份陈旧记录被报成了所有权不确定: %v", exitErr)
		}
		if !errors.Is(exitErr, ErrProcessNotRunning) {
			t.Fatalf("退出原因丢了: %v", exitErr)
		}
	case <-time.After(time.Second):
		t.Fatal("watcher 没有报告退出")
	}
	if !strings.Contains(logs.String(), "guardian_stale_core_record_after_exit") {
		t.Fatalf("清理失败必须留下线索,日志里没有:\n%s", logs.String())
	}
}

// **越界守卫。** Stop 那几处形状相似,但它们证明的东西不同(那是关闭途中对
// 一份**可能仍属于活进程**的记录的处置),本期一个字不动。这条测试存在的
// 唯一理由是:上一条的修法很容易被写成「把 uncertainOwnership 从这个文件里
// 全删掉」。
func TestStopStillReportsUncertaintyWhenItCannotClearTheRecord(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	runner.RemoveProcessRecord = func(string) error { return errors.New("record removal failed") }
	operations.setInspectError(ErrProcessNotRunning)

	if err := runner.Stop(context.Background(), process); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Stop 清不掉记录时 = %v, want 仍然报所有权不确定(本期不动这一处)", err)
	}
}
```

同文件 import 需要 `bytes`、`strings`(若尚未 import,补上;`errors`/`time`/`context` 已在)。

- [ ] **Step 2: 跑红**

Run: `go test ./internal/guardian -count=1`
Expected: `TestAdoptedWatcherCleanupFailureIsNotOwnershipUncertainty` FAIL(`被报成了所有权不确定`);`TestStopStillReportsUncertaintyWhenItCannotClearTheRecord` PASS。

- [ ] **Step 3: 最小实现**

`process.go` 的 `finishExistingWatch` 改为:

```go
func (r *ExecCoreRunner) finishExistingWatch(process Process, exit chan<- error, err error) {
	if clearErr := r.removeRecordIfGeneration(process.PID, process.Generation); clearErr != nil {
		// **OS 已经确认这个进程没了** —— watchExisting 的两个调用点传进来的都是
		// ErrProcessNotRunning 那一族。失败的只是删一个 JSON 文件。
		//
		// 握着「安全」的证明却宣布所有权不确定,是十五个产地里最荒谬的一个:
		// /var/lib/bx 上任何一次文件系统抖动都能锁死 daemon。与 603b602 当初对
		// Existing() 的判断同源 —— 清不掉一个陈旧文件不等于所有权存疑,后者是给
		// 「进程还在但身份不匹配」准备的语义。
		//
		// **Stop(:503/:516/:543/:556)与 Existing(:470)不动**:它们处置的是
		// **可能仍属于活进程**的记录,证明不同。Start 自己那条 wait goroutine
		// (:217)与本处同源、证明其实更强(waitpid 已经返回),由**下一个 task
		// 单独一次提交**处理 —— 分开是为了两者能各自独立回退。
		log.Printf("guardian_stale_core_record_after_exit pid=%d generation=%s clear_failed=%v",
			process.PID, process.Generation, clearErr)
	}
	exit <- err
	close(exit)
}
```

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/guardian -count=1` 与 `go test -race ./internal/guardian -count=1`
Expected: PASS。**注意** `TestExecCoreRunnerRecordRemovalFailurePublishesUncertainExit`(`process_test.go:288`)测的是 **Start 自己那条 wait goroutine**(`process.go:217`),不是本处 —— 它在本 task 里必须**仍然绿**(下一个 task 才动它)。若它现在就红了,说明你改错了地方。

- [ ] **Step 5: 全量验证**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Run: gofumpt 检查(看打印内容)

- [ ] **Step 6: 变异验证**

| 变异 | 必须转红 |
|---|---|
| 把 `log.Printf` 换回 `err = errors.Join(err, uncertainOwnership(...))` | `TestAdoptedWatcherCleanupFailureIsNotOwnershipUncertainty` |
| 删掉那行 `log.Printf`(只是不再报 uncertain、也不留线索) | 同上(它同时断言了日志) |
| 把 `Stop` 里 `:503` 那处的 `uncertainOwnership` 也改成日志(模拟「顺手全删」) | `TestStopStillReportsUncertaintyWhenItCannotClearTheRecord` |

还原 + 重新证绿。

- [ ] **Step 7: 提交**

```bash
git add internal/guardian/process.go internal/guardian/process_test.go
git commit -m "$(cat <<'EOF'
fix(guardian): 已证明安全的清理失败不再产出所有权不确定

finishExistingWatch 手里握着「安全」的证明(OS 已确认被观察的进程消失),
只因为删不掉一份 JSON 记录就宣布所有权不确定 —— /var/lib/bx 上任何一次
文件系统抖动都能锁死 daemon。改为记日志、照常按「进程已消失」处理,与
603b602 当初对 Existing() 的判断同源。

Stop 与 Existing 那几处处置的是可能仍属于活进程的记录,由越界守卫测试钉住
不动;Start 自己那条 wait goroutine 同源但单独一次提交,便于各自回退。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `Start` 的 wait goroutine 也不再把「已证明安全」记成所有权不确定

**Files:**
- Modify: `internal/guardian/process.go:214-224`(`Start` 末尾那条 `go func(){ started.Wait() ... }`)
- Modify: `internal/guardian/process_test.go`(改 `TestExecCoreRunnerRecordRemovalFailurePublishesUncertainExit`,**不许删**)

**Interfaces:**
- Produces: 无新符号(行为改变 + 复用 Task 3 那行日志 `guardian_stale_core_record_after_exit`)。
- Consumes: Task 3 已经确立的判断(清不掉陈旧记录 ≠ 所有权存疑)。

**背景与它为什么比 Task 3 更该改**:这条 goroutine 里 `started.Wait()` **已经返回** —— 那是 `waitpid`,是这个系统里对「进程确定没了」最强的一种证明,比 Task 3 那处(靠 `Inspect` 返回 `ErrProcessNotRunning`)还硬。握着这样一份证明,只因为删不掉一个 JSON 就宣布所有权不确定,与 `603b602` 当初对 `Existing()` 做的判断直接冲突。**这是一处准入放宽**:改完之后这类退出会走 `handleUnexpectedExit` 的正常分支(装屏障 + 重启 Core),而不是锁存拒绝 —— 所以下面那条测试就是专门盯着这次放宽的。

- [ ] **Step 1: 改测试(先改测试,让它先红)**

`internal/guardian/process_test.go` 里把 `TestExecCoreRunnerRecordRemovalFailurePublishesUncertainExit` **改名并改断言**(保留它原有的后半段 —— 那半段断言「记录仍在盘上、而 Existing 不把它当所有权不确定」,与本改动同向,必须原样留着):

```go
// waitpid 已经返回 —— 这是「进程确定没了」最强的一种证明。握着它却因为删不掉
// 一份 JSON 记录就宣布所有权不确定,与 603b602 当初对 Existing() 的判断直接
// 冲突,而后果是一次文件系统抖动锁死 daemon。
//
// 这条测试原名 ...PublishesUncertainExit,编码的正是被推翻的那个行为。**不删,
// 改断言** —— 它守着的另外两件事(记录不许被静默丢掉、Existing 之后仍能继续)
// 依然有效。
func TestExecCoreRunnerRecordRemovalFailureAfterWaitIsNotOwnershipUncertainty(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := newStartTestProcess(55)
	operations := &startTestProcessOperations{
		started: started,
		process: Process{PID: 55, Executable: executable, UID: 0, Generation: "darwin:123:459"},
	}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.ScanRunningCores = noCoresRunning
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.Operations = operations
	runner.RemoveProcessRecord = func(string) error { return errors.New("record removal failed") }
	var logs bytes.Buffer
	restoreLog := swapGuardianLogOutput(&logs)
	defer restoreLog()

	process, err := runner.Start(context.Background(), CoreStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	started.release()
	if exitErr := <-process.Exit; errors.Is(exitErr, ErrProcessOwnershipUncertain) {
		t.Fatalf("waitpid 已经返回,却因为删不掉一份记录报了所有权不确定: %v", exitErr)
	}
	if !strings.Contains(logs.String(), "guardian_stale_core_record_after_exit") {
		t.Fatalf("清理失败必须留下线索,日志里没有:\n%s", logs.String())
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("owned record removed after failed reconciliation: %v", err)
	}
	// ↓↓↓ 原测试后半段原样保留(记录仍在磁盘上但进程已死,Existing 应视为无既有
	// Core 继续)。照抄原文件里那几行,不要重写。
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/guardian -count=1`
Expected: `TestExecCoreRunnerRecordRemovalFailureAfterWaitIsNotOwnershipUncertainty` FAIL(`却因为删不掉一份记录报了所有权不确定`)。

- [ ] **Step 3: 最小实现**

`process.go` 的 `Start` 末尾那条 goroutine 改为:

```go
	exit := make(chan error, 1)
	go func() {
		waitErr := started.Wait()
		if err := r.removeRecordIfGeneration(process.PID, process.Generation); err != nil {
			// **waitpid 已经返回** —— 进程确定没了,失败的只是删一份 JSON。
			// 与 finishExistingWatch 同源、证明更硬:那处靠 Inspect 报
			// ErrProcessNotRunning,这处是内核亲口告诉我们子进程收割完了。
			// 清不掉一个陈旧文件不等于所有权存疑(603b602 对 Existing() 的判断)。
			log.Printf("guardian_stale_core_record_after_exit pid=%d generation=%s clear_failed=%v",
				process.PID, process.Generation, err)
		}
		exit <- waitErr
		close(exit)
	}()
```

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/guardian -count=1` 与 `go test -race ./internal/guardian -count=1`
Expected: PASS。**Task 3 建的那条越界守卫 `TestStopStillReportsUncertaintyWhenItCannotClearTheRecord` 必须仍然绿** —— `Stop` 那几处一个字都不该动。

- [ ] **Step 5: 全量验证**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Run: gofumpt 检查(看打印内容)

- [ ] **Step 6: 变异验证**

| 变异 | 必须转红 |
|---|---|
| 把 `log.Printf` 换回 `waitErr = errors.Join(waitErr, uncertainOwnership(process, ...))` | `TestExecCoreRunnerRecordRemovalFailureAfterWaitIsNotOwnershipUncertainty` |
| 删掉那行 `log.Printf`(不报 uncertain 也不留线索) | 同上(它同时断言日志) |
| 把 `removeRecordIfGeneration` 的返回值整个忽略(连删都不删) | **应当没有测试转红** —— 记下来:这条 goroutine 没有测试断言「确实尝试过删」,可接受(原测试断言的是删**失败**时记录仍在盘上) |
| 顺手把 `Stop` 的 `:503` 也改成日志 | Task 3 的 `TestStopStillReportsUncertaintyWhenItCannotClearTheRecord` |

还原 + 重新证绿。

- [ ] **Step 7: 提交**

```bash
git add internal/guardian/process.go internal/guardian/process_test.go
git commit -m "$(cat <<'EOF'
fix(guardian): Start 的 wait goroutine 也不再产出所有权不确定

waitpid 已经返回 —— 这是「进程确定没了」最强的一种证明,比 finishExistingWatch
那处(靠 Inspect 报 ErrProcessNotRunning)还硬。握着它却因为删不掉一份 JSON
就宣布所有权不确定,与 603b602 对 Existing() 的判断直接冲突。

TestExecCoreRunnerRecordRemovalFailurePublishesUncertainExit 编码的正是被推翻的
那个行为:改断言而不是删掉,它守着的另外两件事(记录不许被静默丢掉、Existing
之后仍能继续)依然有效。Stop 那几处不动,由越界守卫钉住。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: 保留并报出当初那次拒绝的 cause

**Files:**
- Modify: `internal/guardian/manager.go`(`Manager` 结构体加字段;`retainUncertain` 签名;`handleUnexpectedExit` 内联赋值;`upLocked`/`Migrate` 两处短路;`clearUncertaintyAfterProof`;`clearOwnershipLatch`)
- Modify: `internal/guardian/process.go`(新增 `retainedUncertainCause`)
- Modify: `internal/guardian/update.go`(4 处 `retainUncertain` 调用点,只补参数)
- Test: `internal/guardian/manager_latch_test.go`(追加)

**Interfaces:**
- Produces:
  - `func retainedUncertainCause(err error) error` —— 剥掉外层 `*ownershipUncertainError` 包装,返回里面那个 cause(没有就返回原错误;`err == nil` 返回 nil)。
  - `func (m *Manager) retainUncertain(process Process, cause error)` —— **签名变了**,编译器会点名全部 7 个调用点。
  - `Manager.uncertainCause error` 字段(由 mutation channel 保护,与 `m.current` 同一把)。
- Consumes: Task 1 的 `clearOwnershipLatch`(要在里面补一行清 cause)。

**背景**:今天 `upLocked`/`Migrate` 的短路传的是 `uncertainOwnership(m.current, nil)`,于是**锁存之后的错误文本比第一次更少信息** —— 没有 PID、没有逃生提示、也看不出是十五个产地里的哪一个。Task 6 的重新求证失败时要带着当初那个 cause 一起报,cause 得先有地方存。

- [ ] **Step 1: 写失败测试**

追加到 `internal/guardian/manager_latch_test.go`(import 需要 `strings`):

```go
// 锁存之后的错误必须**不比**第一次少信息。今天 upLocked/Migrate 短路时传的是
// nil cause,于是第二次 bx up 只剩一句「Core process ownership is uncertain」:
// 没有 PID、没有逃生提示、也看不出是十五个产地里的哪一个。
func TestLatchedRefusalKeepsTheOriginalCause(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.startErr = uncertainOwnership(
		Process{PID: 4242, Uncertain: true},
		errors.New("no Core process record on disk, but Core appears to be running (PID 4242)"),
	)

	first := env.manager.Up(context.Background())
	if !errors.Is(first, ErrProcessOwnershipUncertain) {
		t.Fatalf("首次 Up = %v, want 所有权不确定(测试前提)", first)
	}
	if !strings.Contains(first.Error(), "PID 4242") {
		t.Fatalf("测试前提不成立:首次拒绝本来就该带上原因, got %q", first.Error())
	}

	second := env.manager.Up(context.Background())
	if !errors.Is(second, ErrProcessOwnershipUncertain) {
		t.Fatalf("再次 Up = %v, want 所有权不确定", second)
	}
	if !strings.Contains(second.Error(), "PID 4242") {
		t.Fatalf("锁存之后的错误比第一次更少信息 —— 用户第二次看到的反而是一句空话: %q", second.Error())
	}
}

// 迁移入口(bx up 在还带 legacy Core 的机器上走的那条)是同一条规矩。
func TestLatchedMigrateRefusalKeepsTheOriginalCause(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.current = Process{PID: 4242, Uncertain: true}
	env.manager.uncertainCause = errors.New("spawned Core record has a live process whose identity was never verified")

	err := env.manager.Migrate(context.Background(), MigrationRequest{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}})
	if !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Migrate = %v, want 所有权不确定", err)
	}
	if !strings.Contains(err.Error(), "identity was never verified") {
		t.Fatalf("Migrate 的锁存拒绝没有带上原因: %q", err.Error())
	}
}

// 锁存被清掉时 cause 必须一起清 —— 否则下一次拒绝会挂着上一个故事的原因,
// 那比没有原因更坏。
func TestClearingTheLatchAlsoClearsTheRetainedCause(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	env.manager.current = Process{Uncertain: true}
	env.manager.uncertainCause = errors.New("stale story from a previous refusal")

	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("Down = %v, want nil", err)
	}
	if env.manager.uncertainCause != nil {
		t.Fatalf("锁存清了但 cause 留着: %v", env.manager.uncertainCause)
	}
}
```

**注意**:`TestLatchedMigrateRefusalKeepsTheOriginalCause` 里 `MigrationRequest` 的字段名以 `ValidateMigrationRequest` 的实际定义为准 —— 动手前读 `internal/guardian/migration.go`,照抄它接受的最小合法请求(既有测试 `migration_test.go` 里有现成样例可复制)。

- [ ] **Step 2: 跑红**

Run: `go test ./internal/guardian -count=1`
Expected: 三条全 FAIL。第二、三条还会有编译错误(`uncertainCause` 字段不存在)—— 那也是红,继续。

- [ ] **Step 3: 最小实现**

`process.go`(挨着 `uncertainProcess` 放):

```go
// retainedUncertainCause 取出「当初那次拒绝」的原因,并剥掉外层的
// ownershipUncertainError 包装 —— 否则重新报出来时会变成
// 「uncertain: uncertain: …」的叠词。
func retainedUncertainCause(err error) error {
	if err == nil {
		return nil
	}
	var uncertain *ownershipUncertainError
	if errors.As(err, &uncertain) {
		return uncertain.cause
	}
	return err
}
```

`manager.go`:

1. `Manager` 结构体在 `current Process` 下面加:
```go
	// uncertainCause 是把 current 锁成 Uncertain 的**那一次**拒绝的原因。
	// 与 current 同由 mutation channel 保护。留着它是因为短路(以及后来的
	// 重新求证)必须报出与第一次同样多的信息:今天传的是 nil,于是用户第二次
	// 看到的反而是一句没有 PID、没有产地、没有出路的空话。
	uncertainCause error
```

2. `retainUncertain` 改签名并记 cause:
```go
func (m *Manager) retainUncertain(process Process, cause error) {
	process.Uncertain = true
	m.current = process
	m.uncertainCause = retainedUncertainCause(cause)
	...其余不变...
}
```

3. 全部调用点补第二个参数(编译器会逐个点名):
   - `manager.go` upLocked 那处:`m.retainUncertain(process, err)`
   - `manager.go` startCoreLockedWithBarrierRelease 三处:`m.retainUncertain(uncertain, err)`、`m.retainUncertain(Process{...}, cleanupErr)` ×2
   - `update.go` 四处:`m.retainUncertain(uncertain, err)`,以及三处 `m.retainUncertain(Process{...}, cleanupErr)` / `stopErr`(按各自作用域里那个真实的错误变量名)

4. `handleUnexpectedExit` 内联那一处:
```go
		process.Uncertain = true
		m.current = process
		m.uncertainCause = retainedUncertainCause(exitErr)
```

5. `upLocked` 与 `Migrate` 的短路:`return uncertainOwnership(m.current, m.uncertainCause)`

6. `clearUncertaintyAfterProof` 里 `m.current = Process{}` 之后补 `m.uncertainCause = nil`

7. Task 1 的 `clearOwnershipLatch` 里 `m.current = Process{}` 之后补 `m.uncertainCause = nil`

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/guardian -count=1` 与 `go test -race ./internal/guardian -count=1`
Expected: PASS。

- [ ] **Step 5: 全量验证**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Run: gofumpt 检查(看打印内容)

- [ ] **Step 6: 变异验证**

| 变异 | 必须转红 |
|---|---|
| `upLocked` 短路改回 `uncertainOwnership(m.current, nil)` | `TestLatchedRefusalKeepsTheOriginalCause` |
| `Migrate` 短路改回 `uncertainOwnership(m.current, nil)` | `TestLatchedMigrateRefusalKeepsTheOriginalCause`(**两处必须分别验** —— 一处改对另一处没改是本仓库反复出现的形状) |
| `retainUncertain` 里 `m.uncertainCause = retainedUncertainCause(cause)` 改成不赋值 | 第一条 |
| `clearOwnershipLatch` 里删掉 `m.uncertainCause = nil` | `TestClearingTheLatchAlsoClearsTheRetainedCause` |
| `retainedUncertainCause` 改成 `return err`(不剥包装) | 应当**没有**测试转红 —— 记下来:这说明「不叠词」只是文本品质,没有测试守着,可以接受 |

还原 + 重新证绿。

- [ ] **Step 7: 提交**

```bash
git add internal/guardian/manager.go internal/guardian/process.go internal/guardian/update.go internal/guardian/manager_latch_test.go
git commit -m "$(cat <<'EOF'
fix(guardian): 锁存后的拒绝带上当初那次拒绝的原因

upLocked/Migrate 短路时传的是 nil cause,于是第二次 bx up 的错误比第一次
**更少**信息:没有 PID、没有产地、没有出路。改为把 cause 与锁存一起记住
(retainUncertain 签名变更,编译器逐个点名 7 个调用点),短路与将来的重新
求证都报出它;锁存被清时 cause 一并清掉,免得下一次拒绝挂着上一个故事。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: `confirmNoCoreForRelease` + `recheckOwnershipUncertain` —— 带证明纪律的重新求证(先不接线)

**Files:**
- Modify: `internal/guardian/manager.go`(新增两个方法,**不改任何调用方**,**也不动 `confirmCoreStopped`**)
- Create: `internal/guardian/procscan_other_test.go`
- Test: `internal/guardian/manager_latch_test.go`(追加)

**Interfaces:**
- Produces:
  - `func (m *Manager) confirmNoCoreForRelease() (released bool, reason string)` —— **释放方向**的求证:两次扫描**都**干净、中间隔一个 `coreScanSettle`,才返回 `true`。任何一次扫脏/扫错、runner 不会扫、panic,一律 `false` + 原因(`core_still_running` / `core_scan_failed` / `core_scan_unsupported`)。**永不返回 error、永不 panic 出去。**
  - `func (m *Manager) recheckOwnershipUncertain(hop string) error` —— 返回 `nil` 表示锁存已被求证清掉、可以继续;返回非 nil 表示继续拒绝(错误包 `ErrProcessOwnershipUncertain`,并带着 Task 5 保留的 cause 与本次求证的 reason)。`hop` 只进日志,取值 `"up"` / `"migrate"`。
- Consumes: 既有 `coreScanner` 接口、`coreScanSettle`、Task 5 的 `m.uncertainCause`。

**为什么不复用 `confirmCoreStopped`(这是本 task 的全部要点)**:它在**第一次扫描就干净时直接返回 true**,不沉降、不重扫。对一个「已 fork、`Terminate()` 已发、wait 超时,进程既不是僵尸也还没消失」的残留(`cleanupFailedStart` 那个窗口),而 `scanRunningCores` 又会跳过 argv 读不出的进程 —— 一次扫描完全可能给出**假的全清**,`confirmCoreStopped` 在这一点上并不比裸扫描强。它真正强的是另外三条(永不返回 error、panic 收成「没能确认」、runner 不会扫时判「没能确认」)。

两个函数的**偏置方向相反,必须分开**:

| | `confirmCoreStopped`(`Down`/`Recover`) | `confirmNoCoreForRelease`(重新求证) |
|---|---|---|
| 回答的问题 | 能不能对用户说「已关闭」 | 能不能**再起一个 Core** |
| 怕什么 | 把每次正常关闭都报成告警(用户会学会忽略告警) | 一次假的「全清」→ 两个 Core 争默认路由,`bx status` 显绿而流量明文直连 |
| 扫脏一次 | 沉降后重扫,第二次干净就算数 | **立即拒绝** |
| 说 yes 的条件 | 任意一次干净 | **两次都干净,中间隔一个沉降窗口** |
| 代价 | 只有扫脏时才付 300ms | 成功释放时固定付 300ms(每次用户发起的 `bx up` 至多一次) |

**把这两个「统一」掉是本期最容易犯、后果最重的错误。** 既有的 `TestConfirmCoreStoppedReScansAfterASettleWindow`(`manager_confirm_test.go:83`)钉住前者的偏置,本 task 新加的 `TestRecheckDoesNotReleaseWhenOnlyTheSecondScanIsClean` 钉住后者 —— 两条必须同时绿,谁想合并它们都会撞上其中一条。

**本步之后树的行为一个字没变** —— 新方法还没有任何调用方。这是刻意的:释放与拒绝的每一种结局先单独钉死,再去动准入。

- [ ] **Step 1: 写失败测试**

追加到 `internal/guardian/manager_latch_test.go`(import 需要 `time`):

```go
// 系统里确实没有 Core 在跑 ⇒ 释放。这是整条出口存在的理由。
// **两次扫描都干净才算数** —— 断言 calls==2,而不只是「释放了」。
func TestRecheckReleasesTheLatchOnlyAfterTwoCleanScans(t *testing.T) {
	restore := coreScanSettle
	coreScanSettle = time.Millisecond
	t.Cleanup(func() { coreScanSettle = restore })

	scan := &scriptedScanner{}
	env := newManagerTestEnv(t)
	env.manager.runner = &scannerOnlyRunner{scan: scan}
	env.manager.current = Process{PID: 4242, Uncertain: true}
	env.manager.uncertainCause = errors.New("Core appears to be running (PID 4242)")

	if err := env.manager.recheckOwnershipUncertain("up"); err != nil {
		t.Fatalf("两次都扫干净了仍然拒绝: %v", err)
	}
	if env.manager.current.Uncertain || env.manager.uncertainCause != nil {
		t.Fatal("释放之后锁存或 cause 还留着")
	}
	if scan.calls != 2 {
		t.Fatalf("释放只扫了 %d 次 —— 起第二个 Core 是这个系统里最坏的结果,一次扫描不够", scan.calls)
	}
}

// **第一次扫干净、第二次扫到了 ⇒ 绝不释放。**
//
// 这正是设计点名的那个残留:子进程已 fork、Terminate() 已发、wait 超时,进程
// 既不是僵尸也还没消失,而 scanRunningCores 会跳过 argv 读不出的进程 ——
// 一次扫描完全可能给出假的「全清」。这条测试是「一次干净就够了」这个变异的
// 唯一守卫。
func TestRecheckDoesNotReleaseWhenOnlyTheFirstScanIsClean(t *testing.T) {
	restore := coreScanSettle
	coreScanSettle = time.Millisecond
	t.Cleanup(func() { coreScanSettle = restore })

	scan := &scriptedScanner{results: [][]Process{nil, {{PID: 4242}}}}
	env := newManagerTestEnv(t)
	env.manager.runner = &scannerOnlyRunner{scan: scan}
	env.manager.current = Process{PID: 4242, Uncertain: true}

	err := env.manager.recheckOwnershipUncertain("up")
	if !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("一次假的「全清」就放行了: %v", err)
	}
	if !env.manager.current.Uncertain {
		t.Fatal("锁存被一次假的全清清掉了")
	}
	if scan.calls != 2 {
		t.Fatalf("扫了 %d 次 —— 第一次干净之后必须再扫一次才能下结论", scan.calls)
	}
}

// **第一次扫到、沉降后干净 ⇒ 同样不释放。**
//
// 这条与上一条方向相反,守的是另一个变异:「直接拿 confirmCoreStopped 来用」。
// confirmCoreStopped 在这个脚本下**会**说 stopped(它的偏置是别把正常关闭报成
// 告警,对 Down 是对的),而释放锁存是准入控制,偏置必须反过来。既有的
// TestConfirmCoreStoppedReScansAfterASettleWindow 钉住那一头,这条钉住这一头。
func TestRecheckDoesNotReleaseWhenOnlyTheSecondScanIsClean(t *testing.T) {
	restore := coreScanSettle
	coreScanSettle = time.Millisecond
	t.Cleanup(func() { coreScanSettle = restore })

	scan := &scriptedScanner{results: [][]Process{{{PID: 4242}}, nil}}
	env := newManagerTestEnv(t)
	env.manager.runner = &scannerOnlyRunner{scan: scan}
	env.manager.current = Process{PID: 4242, Uncertain: true}

	if err := env.manager.recheckOwnershipUncertain("up"); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("复用了 Down 那条偏置相反的求证: %v", err)
	}
	if !env.manager.current.Uncertain {
		t.Fatal("锁存被 Down 那套偏置清掉了")
	}
}

// 扫到了 ⇒ 保持拒绝,并且报出**当初**那个 cause 加上**这次**的理由。
// (第一次就扫脏,立即拒绝、不沉降,所以这条不需要缩 coreScanSettle。)
func TestRecheckKeepsRefusingWhenACoreIsStillRunning(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.scanResult = []Process{{PID: 4242}}
	env.manager.current = Process{PID: 4242, Uncertain: true}
	env.manager.uncertainCause = errors.New("no Core process record on disk, but Core appears to be running (PID 4242)")

	err := env.manager.recheckOwnershipUncertain("up")
	if !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("系统说还有 Core 在跑,却放行了: %v", err)
	}
	if !strings.Contains(err.Error(), "PID 4242") {
		t.Fatalf("重新求证失败时丢了当初那个 cause: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "core_still_running") {
		t.Fatalf("重新求证失败时没说清这次是怎么判的: %q", err.Error())
	}
	if !env.manager.current.Uncertain {
		t.Fatal("拒绝了却把锁存清了")
	}
	if env.manager.Status().LastError != "core_ownership_uncertain" {
		t.Fatalf("对外发布的码变了,四处消费方的指引会全部失效: %q", env.manager.Status().LastError)
	}
}

// **扫不动 ≠ 没有。** 最容易瞬时发生的一类,也是最不能塌缩成「放行」的一类。
func TestRecheckKeepsRefusingWhenTheScanFails(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.scanErr = errors.New("sysctl failed")
	env.manager.current = Process{Uncertain: true}

	if err := env.manager.recheckOwnershipUncertain("up"); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("扫不动被当成了「没有」: %v", err)
	}
	if !env.manager.current.Uncertain {
		t.Fatal("扫不动却把锁存清了 —— 「问不出来」不等于「安全」")
	}
}

// runner 压根不会扫(将来别的平台、以及非 darwin 上的 procscan 桩)⇒ 保持拒绝。
func TestRecheckKeepsRefusingWhenTheRunnerCannotScan(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.runner = &nonScanningRunner{}
	env.manager.current = Process{Uncertain: true}

	if err := env.manager.recheckOwnershipUncertain("up"); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("求证不了却放行: %v", err)
	}
}

// panic 也是一种失败模式,而且这一条比别的更要命:一次 panic 打死 Guardian,
// launchd 的 KeepAlive 把它拉起来再 panic —— 崩溃循环。收成「没能确认」,
// 方向与本函数其余部分一致。(panickingScanner 定义在 manager_down_test.go;
// 若它不是一个完整的 CoreRunner,照 scannerOnlyRunner 的写法就地包一个。)
func TestRecheckSurvivesAPanickingScanner(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.runner = panickingScanner{}
	env.manager.current = Process{Uncertain: true}

	if err := env.manager.recheckOwnershipUncertain("up"); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("panic 之后 = %v, want 仍然拒绝(而不是让 Guardian 崩掉)", err)
	}
}

// 没有锁存时**一次扫描都不许做**。两次扫描 + 300ms 沉降是只该发生在
// 已经锁存的机器上、且由用户动作触发的开销;把它加到每一次 bx up 上,
// 就是给所有人交税。
func TestRecheckDoesNotScanWhenThereIsNoLatch(t *testing.T) {
	scan := &scriptedScanner{}
	env := newManagerTestEnv(t)
	env.manager.runner = &scannerOnlyRunner{scan: scan}

	if err := env.manager.recheckOwnershipUncertain("up"); err != nil {
		t.Fatalf("没有锁存时不该有任何拒绝: %v", err)
	}
	if scan.calls != 0 {
		t.Fatalf("没有锁存却扫了 %d 次 —— 每一次正常的 bx up 都在白交这个税", scan.calls)
	}
}
```

新建 `internal/guardian/procscan_other_test.go`:

```go
//go:build !darwin

package guardian

import "testing"

// 非 darwin 上这扇门是**焊死**的:scanRunningCores 恒返回错误 ⇒
// confirmNoCoreForRelease 恒「没能确认」⇒ 重新求证在这些平台上是 no-op,
// 锁存永远不会被它释放。谁要把 Guardian 移植过来,必须先实现
// scanRunningCores —— 否则得到的不是「行为不变」,而是一个永远解不开的锁存。
func TestCoreScanIsUnsupportedOffDarwin(t *testing.T) {
	cores, err := scanRunningCores(coreScanLifecycle)
	if err == nil {
		t.Fatal("非 darwin 的进程扫描桩必须报错 —— 它一旦「成功」返回空,重新求证就会在一个从没查过的平台上放行")
	}
	if len(cores) != 0 {
		t.Fatalf("桩返回了 %d 个进程", len(cores))
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/guardian -count=1`
Expected: 八条 Manager 级测试 FAIL(编译错误:`recheckOwnershipUncertain` 未定义)。
Run: `GOOS=linux GOARCH=amd64 go test -c -o /dev/null ./internal/guardian`(确认 `!darwin` 那个文件能编)—— 若本机就是 darwin,该测试文件在本机不参与构建,靠 CI 的 ubuntu/windows runner 跑;交叉编译测试二进制是本机唯一能验它的办法。

- [ ] **Step 3: 最小实现**

`manager.go`,挨着 `confirmCoreStopped` 放(**不要改 `confirmCoreStopped` 一个字**):

```go
// confirmNoCoreForRelease 是**释放方向**的求证:能不能允许再起一个 Core?
//
// **与 confirmCoreStopped 偏置相反,所以是两个函数。** 那一个回答「能不能对用户
// 说已关闭」,怕的是把每次正常关闭都报成告警,于是第一次扫脏之后沉降重扫、
// 第二次干净就算数;**它在第一次扫描就干净时直接返回 true,不沉降、不重扫**,
// 因此对一次假的「全清」并不比裸扫描强。
//
// 而这里问的是准入。假的「全清」在这里的后果是两个 Core 争默认路由、先退出的
// 那个用旧快照还原掀掉另一个的劫持,于是 bx status 显绿而流量明文直连。设计
// 自己点名的那个残留(cleanupFailedStart:子进程已 fork、Terminate() 已发、
// wait 超时,进程既不是僵尸也还没消失,而 scanRunningCores 会跳过 argv 读不出
// 的进程)恰恰就是一次假的全清。**所以两次都必须干净,中间隔一个沉降窗口。**
//
// 代价:一次成功的释放固定多花 coreScanSettle(300ms)。它只发生在已经锁存的
// 机器上、且由用户动作触发,每次 bx up 至多一次 —— 对这个系统里唯一一个
// 「判错就是两个 Core」的决定来说很便宜。**拒绝不付这个代价**(第一次扫脏就
// 立即返回),所以反复重试的用户不会被拖慢。
//
// **永不返回 error、永不 panic 出去**:panic 打死 Guardian 会被 launchd 的
// KeepAlive 拉起来再 panic —— 崩溃循环。收成「没能确认」。
func (m *Manager) confirmNoCoreForRelease() (released bool, reason string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("guardian_core_scan_panicked_on_release recovered=%v", r)
			released, reason = false, "core_scan_failed"
		}
	}()
	scanner, ok := m.runner.(coreScanner)
	if !ok {
		return false, "core_scan_unsupported"
	}
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(coreScanSettle)
		}
		cores, err := scanner.ScanRunning()
		switch {
		case err != nil:
			log.Printf("guardian_core_scan_failed_on_release attempt=%d err=%v", attempt, err)
			return false, "core_scan_failed"
		case len(cores) > 0:
			log.Printf("guardian_core_still_running_on_release attempt=%d pids=%s", attempt, scannedCorePIDs(cores))
			return false, "core_still_running"
		}
	}
	return true, ""
}

// recheckOwnershipUncertain 在**用户显式发起**的 up/migrate 上重新求证所有权,
// 取代「把第一次的结论钉死」的短路。
//
// **判据一个字没变,变的是每次都重新求一遍。** 十五个 uncertain 产地里只有三个
// 真的意味着「有第二个 Core」:其余要么是一次失败的扫描(最容易瞬时发生)、
// 要么是一次失败的 unlink、要么是一个可能早就退出了的嫌疑 —— 而它们全都锁死
// 同一扇门。
//
// **只有「确知没有」才释放。** 扫不动、扫到了(哪怕只有两次里的一次)、runner
// 不会扫、求证本身 panic,一律保持拒绝,fail-closed 一步不让。
//
// **非 darwin 上这是个 no-op**:procscan_other.go 的 scanRunningCores 恒返回
// errCoreScanUnsupported ⇒ confirmNoCoreForRelease 恒 false ⇒ 门仍然焊死。
// 谁要移植 Guardian,必须先实现 scanRunningCores。
func (m *Manager) recheckOwnershipUncertain(hop string) error {
	if !m.current.Uncertain {
		return nil
	}
	stopped, reason := m.confirmNoCoreForRelease()
	if !stopped {
		log.Printf("guardian_ownership_uncertain_recheck hop=%s released=false reason=%s pid=%d",
			hop, reason, m.current.PID)
		m.needsAttention(DesiredOn, "core_ownership_uncertain")
		return uncertainOwnership(m.current, errors.Join(
			m.uncertainCause,
			fmt.Errorf("re-verification did not prove absence (%s)", reason),
		))
	}
	log.Printf("guardian_ownership_uncertain_recheck hop=%s released=true cores_found=0 pid=%d",
		hop, m.current.PID)
	m.current = Process{}
	m.uncertainCause = nil
	return nil
}
```

**发布出去的码保持 `core_ownership_uncertain`** —— CLI 的 `guardianCodeHints`、菜单的 `toggleFailureHint`、升级中止逻辑三处都按这个码分支,换名字会让它们同时失效。

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/guardian -count=1` 与 `go test -race ./internal/guardian -count=1`
Expected: PASS。**既有测试必须一条都没红** —— 本步没有任何调用方,行为不该变。特别确认 `manager_confirm_test.go` 那五条全绿,尤其 `TestConfirmCoreStoppedReScansAfterASettleWindow`:它钉的是 `confirmCoreStopped` 的**相反**偏置,若它红了说明你去改了那个函数(不许改)。

- [ ] **Step 5: 全量验证**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Run: 六个交叉编译目标各一遍
Run: gofumpt 检查(看打印内容)

- [ ] **Step 6: 变异验证**

| 变异 | 必须转红 |
|---|---|
| `if !stopped` 改成 `if false`(永远释放) | `...WhenACoreIsStillRunning`、`...WhenTheScanFails`、`...WhenTheRunnerCannotScan`、`...WhenOnlyTheFirstScanIsClean`、`...WhenOnlyTheSecondScanIsClean`、`...SurvivesAPanickingScanner` |
| **`confirmNoCoreForRelease` 的循环改成 `attempt < 1`(一次干净就释放)** | `TestRecheckDoesNotReleaseWhenOnlyTheFirstScanIsClean`(**这是那条测试唯一的守卫目标**),以及 `TestRecheckReleasesTheLatchOnlyAfterTwoCleanScans` 的 `calls != 2` 断言 |
| **`recheckOwnershipUncertain` 里 `m.confirmNoCoreForRelease()` 换成 `m.confirmCoreStopped()`** | `TestRecheckDoesNotReleaseWhenOnlyTheSecondScanIsClean`(**这是那条测试唯一的守卫目标** —— 两条偏置相反的求证被合并,正是本 task 最怕的错误) |
| 把 `case len(cores) > 0` 那一支删掉(扫到也不算) | `...WhenACoreIsStillRunning`、`...WhenOnlyTheFirstScanIsClean`、`...WhenOnlyTheSecondScanIsClean` |
| 把 `case err != nil` 改成 `continue`(扫不动就再试,试完当干净) | `...WhenTheScanFails` |
| 删掉 `scanner, ok := m.runner.(coreScanner)` 的 `!ok` 早退(当作干净) | `...WhenTheRunnerCannotScan`(注意:删掉后是 nil 解引用 panic → 被 recover 收成拒绝 → 可能**不红**。若不红,改成 `if !ok { return true, "" }` 再验一次) |
| 删掉 `defer func(){ recover() ... }` | `TestRecheckSurvivesAPanickingScanner`(整包会以 panic 中止 —— 那也算红,记下来) |
| 去掉 `if !m.current.Uncertain { return nil }` 这一句 | `TestRecheckDoesNotScanWhenThereIsNoLatch` |
| 释放分支里删掉 `m.uncertainCause = nil` | `TestRecheckReleasesTheLatchOnlyAfterTwoCleanScans` |
| 拒绝分支里的 `errors.Join(...)` 改成只传 `m.uncertainCause` | `...WhenACoreIsStillRunning`(它断言了 `core_still_running` 出现在文本里) |
| 拒绝分支里把 `m.needsAttention(DesiredOn, "core_ownership_uncertain")` 改成别的码 | `...WhenACoreIsStillRunning` |

还原 + 重新证绿。

- [ ] **Step 7: 提交**

```bash
git add internal/guardian/manager.go internal/guardian/manager_latch_test.go internal/guardian/procscan_other_test.go
git commit -m "$(cat <<'EOF'
feat(guardian): 加带证明纪律的所有权重新求证助手(尚未接线)

confirmNoCoreForRelease 是**释放方向**的求证,与 confirmCoreStopped 偏置相反,
故是两个函数:后者第一次扫干净就返回 true(对 Down 是对的,怕的是把正常关闭
报成告警),因此对一次假的「全清」并不比裸扫描强;而释放锁存是准入控制,
设计点名的残留(cleanupFailedStart:已 fork、Terminate 已发、wait 超时,进程
既非僵尸也未消失,而扫描会跳过 argv 读不出的进程)恰恰就是假的全清。
故**两次扫描都干净、中间隔一个沉降窗口**才释放;扫到了/扫不动/不会扫/panic
一律拒绝,且拒绝不付沉降代价。

recheckOwnershipUncertain 在此之上给出出口,发布的失败码保持
core_ownership_uncertain,CLI、菜单、升级三处消费方的分支不受影响。

本提交没有任何调用方,行为不变;非 darwin 上它恒为 no-op,门仍然焊死,
由 //go:build !darwin 的回归测试钉住。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: 把 `upLocked` 的短路换成重新求证(**只在用户发起时**),并正面处理 `manager_test.go:1034`

**Files:**
- Modify: `internal/guardian/manager.go`(`upLocked` 签名加 `origin`;`Up` 与 `recoverLocked` 两个调用点;短路换成 `recheckOwnershipUncertain`)
- Modify: `internal/guardian/manager_test.go`(`fakeCoreRunner` 加扫描计数器;改 `TestManagerHealthFailureUsesLiveBoundedCleanupAndBlocksRetryWhenExitUnproven`)
- Test: `internal/guardian/manager_latch_test.go`(追加)

**Interfaces:**
- Produces:
  - `type upOrigin int`,常量 `upOriginUser`、`upOriginStartupRecovery`
  - `func (m *Manager) upLocked(ctx context.Context, origin upOrigin) error`
  - `func (r *fakeCoreRunner) scanCount() int`(测试替身)
- Consumes: Task 6 的 `recheckOwnershipUncertain`。

**为什么要分「谁发起的」**:`upLocked` 有两个调用点 —— `Up`(用户)与 `recoverLocked`(启动恢复)。而 `daemon.go` 的 `retryDaemonRecovery` 会**无限重试** `Recover`,退避封顶 `guardianRecoveryRetryMax = 5 * time.Second`:一台锁存的机器上,把这套求证放进这条路等于**每 5 秒一轮、永远**(约 1.7 万轮/天)。设计的风险第二条明写「不许把这套纪律搬进任何按时钟驱动的路径」。启动恢复因此保留便宜的短路;它原本最坏的后果(锁存升级成 `recoveryBlocked` 把 `Down` 堵死)已经由 Task 2 拆掉,而用户的 `bx up` 现在每次都会重新求证。

**一条被考虑过、并且刻意不做的事(写在这里免得下一个读者以为是漏掉的)**:「让启动恢复自己把一次**瞬时**扫描失败治好」是有价值的 —— 设计文档抱怨的正是「一个专门设计成不放弃的重试循环,永远解不开一个瞬时错误」。本期不做,因为无限重试 × 每轮两次扫描是明确被禁止的按时钟驱动路径,而这个缺口已经有两条兜底:① Task 2 之后瞬时失败不再堵死 `Down`;② 用户的下一次 `bx up` 会重新求证并自愈。**自然的后续做法是「有限次数」而不是「每轮都做」** —— 本仓库已有现成先例 `maxPathRecoveryAttempts`(路径恢复重试 20 次封顶)。等真机 soak 显示这类瞬时锁存确实发生,再单独立一期照那个形状做。

- [ ] **Step 1: 写失败测试(盯着这次放宽本身)**

追加到 `internal/guardian/manager_latch_test.go`:

```go
// **这次放宽本身**:用户发起的 Up 撞上锁存时,必须重新去问系统,而不是把
// 第一次的结论钉死。**两次扫描都干净** ⇒ 继续,Core 起得来。
func TestUserInitiatedUpReVerifiesAndProceedsWhenTheSystemIsClean(t *testing.T) {
	// 释放要跨一个沉降窗口(拒绝不用),缩短它免得每次跑测试都白等 300ms。
	restore := coreScanSettle
	coreScanSettle = time.Millisecond
	t.Cleanup(func() { coreScanSettle = restore })

	env := newManagerTestEnv(t)
	env.manager.current = Process{PID: 4242, Uncertain: true}
	env.manager.uncertainCause = errors.New("Core appeared to be running (PID 4242)")

	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatalf("重新求证扫干净了,Up 仍然失败: %v", err)
	}
	if env.manager.Status().Protection != ProtectionProtected {
		t.Fatalf("protection = %v, want protected", env.manager.Status().Protection)
	}
	if got := env.runner.startCount(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
	if got := env.runner.scanCount(); got != 2 {
		t.Fatalf("释放只扫了 %d 次 —— 一次干净不足以放行", got)
	}
}

// 放宽的另一头:系统说**还有** Core 在跑 ⇒ 照旧拒绝,而且**一个 Core 都不许起**。
// 起第二个 Core 是这个系统里最坏的结果。
// (第一次就扫脏 ⇒ 立即拒绝、不沉降,所以这条不需要缩 coreScanSettle。)
func TestUserInitiatedUpStillRefusesWhenACoreIsStillRunning(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.scanResult = []Process{{PID: 4242}}
	env.manager.current = Process{PID: 4242, Uncertain: true}

	if err := env.manager.Up(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Up = %v, want 仍然拒绝", err)
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("拒绝时起了 %d 个 Core —— 两个 Core 争默认路由,先退出的那个用旧快照还原掀掉另一个的劫持", got)
	}
}

// 扫不动同样拒绝,同样不起 Core。
func TestUserInitiatedUpStillRefusesWhenTheScanFails(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.scanErr = errors.New("sysctl failed")
	env.manager.current = Process{Uncertain: true}

	if err := env.manager.Up(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Up = %v, want 仍然拒绝", err)
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("扫不动却起了 %d 个 Core", got)
	}
}

// **启动恢复不许重新求证。** retryDaemonRecovery 每 5 秒重试一次、永不放弃:
// 把这套求证放进去等于一天一万七千轮,正是设计里明写「不许搬进按时钟驱动的
// 路径」的那条纪律。(「让它有限次数地自愈瞬时失败」是有价值的后续,但那要
// 照 maxPathRecoveryAttempts 的形状单独做,不是每轮都做。)
func TestStartupRecoveryDoesNotReVerifyOwnership(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.manager.current = Process{Uncertain: true}

	if err := env.manager.Recover(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Recover = %v, want 所有权不确定", err)
	}
	if got := env.runner.scanCount(); got != 0 {
		t.Fatalf("启动恢复扫了 %d 次 —— 它每 5 秒重试一次,永不放弃", got)
	}
}

// 调谐器一侧一个字不改:循环仍然**永不**清这个锁存。既有的
// TestReconcileOnceNeverClearsTheOwnershipUncertainLatch 守着「不清」,
// 这条补上「也不去扫」——两者威胁模型不同:本期只动**用户发起**的那一半。
func TestReconcileLoopDoesNotReVerifyOwnership(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.current = Process{Uncertain: true}
	before := env.runner.scanCount()

	_, uncertain, acquired := env.manager.readMutationFences(context.Background())
	if !acquired || !uncertain {
		t.Fatal("测试前提不成立:调谐器这一轮应当看见锁存升起")
	}
	if got := env.runner.scanCount(); got != before {
		t.Fatalf("调谐器读栅栏时扫了 %d 次(之前 %d 次)", got, before)
	}
}
```

`manager_test.go` 的 `fakeCoreRunner` 加计数(`scanResult`/`scanErr` 旁边):

```go
	scans int
```
```go
func (r *fakeCoreRunner) ScanRunning() ([]Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scans++
	return append([]Process(nil), r.scanResult...), r.scanErr
}

func (r *fakeCoreRunner) scanCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.scans
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/guardian -count=1`
Expected: `TestUserInitiatedUpReVerifiesAndProceedsWhenTheSystemIsClean` FAIL(`Up 仍然失败`),其余四条 PASS(今天的短路恰好满足它们 —— 它们是**守住不许倒退**的,不是驱动实现的)。

- [ ] **Step 3: 最小实现**

`manager.go`:

```go
// upOrigin 区分「谁要求打开保护」。**只影响要不要重新求证所有权**,不影响别的。
//
// 用户发起的那条路每次都重新求一遍(设计的核心);启动恢复保留便宜的短路 ——
// daemon.go 的 retryDaemonRecovery 每 guardianRecoveryRetryMax(5s)重试一次
// 且永不放弃,把两次扫描 + 300ms 沉降放进去就是一天一万七千轮,违反设计里
// 「不许把这套纪律搬进任何按时钟驱动的路径」那条。启动恢复原本最坏的后果
// (锁存升级成 recoveryBlocked 把 Down 堵死)已由 recoverLocked 那处的
// errors.Is 判据拆掉。
type upOrigin int

const (
	upOriginUser upOrigin = iota
	upOriginStartupRecovery
)
```

`upLocked` 开头:

```go
func (m *Manager) upLocked(ctx context.Context, origin upOrigin) error {
	if origin == upOriginUser {
		if err := m.recheckOwnershipUncertain("up"); err != nil {
			return err
		}
	} else if m.current.Uncertain {
		m.needsAttention(DesiredOn, "core_ownership_uncertain")
		return uncertainOwnership(m.current, m.uncertainCause)
	}
	...其余不变...
```

调用点:`Up` 的 `return m.upLocked(ctx)` → `return m.upLocked(ctx, upOriginUser)`;`recoverLocked` 的 `if err := m.upLocked(ctx); err != nil` → `m.upLocked(ctx, upOriginStartupRecovery)`。

- [ ] **Step 4: 跑绿 —— 并把那条必然转红的既有测试看在眼里**

Run: `go test ./internal/guardian -count=1`(**不加 `-run`**)
Expected: 新测试全绿,但 `TestManagerHealthFailureUsesLiveBoundedCleanupAndBlocksRetryWhenExitUnproven`(`manager_test.go:1034`)**FAIL**。把它的输出完整读一遍并记下来。

它为什么必然红:它断言第二次 `Up` 返回 `ErrProcessOwnershipUncertain` 且 `startCount() == 1`,而它用的 `fakeCoreRunner` **没有真实的扫描**(`scanResult` 为零值 = 一台干净机器)。重新求证于是释放锁存、一路走到 `runner.Existing`(返回零值)、再 `startCoreLocked` —— **起了第二个 Core**。

**这条测试编码的安全属性是对的**(fork 清理未证实时不许重试),错的只是它用「粘住」来表达这个属性。**不许删,改成注入一个诚实的扫描器。**

- [ ] **Step 5: 把那条测试改成注入诚实的扫描器**

`manager_test.go` 里改为:

```go
// 注:第二次 Up 的重新求证在**第一次扫描**就扫到那个 Core,立即拒绝、不跨沉降
// 窗口,所以这条测试不需要缩 coreScanSettle。
func TestManagerHealthFailureUsesLiveBoundedCleanupAndBlocksRetryWhenExitUnproven(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.cleanupTimeout = 20 * time.Millisecond
	env.health.err = errors.New("Core unhealthy")
	env.runner.stopErr = errors.New("cooperative shutdown could not prove exit")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := env.manager.Up(ctx); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Up error = %v, want uncertain ownership", err)
	}
	if got := env.runner.stopEntryContextError(); got != nil {
		t.Fatalf("cleanup inherited expired health context: %v", got)
	}

	// **诚实的扫描器,而不是「粘住」。**
	//
	// 这条测试要守的属性是「fork 清理没能证明退出时,不许重试起第二个 Core」。
	// 它原本靠锁存的短路来表达 —— 而那正是本期要换掉的东西。属性本身没变:
	// 清理**没能证明**那个进程退出了,所以此刻向系统求证时它就该还在那儿。
	// 替身的默认扫描结果是「一台干净机器」,拿它当证据等于让重新求证凭空放行。
	uncertain := env.manager.current
	if !uncertain.Uncertain || uncertain.PID == 0 {
		t.Fatal("测试前提不成立:这次失败的启动没有留下带 PID 的锁存")
	}
	env.runner.scanResult = []Process{{PID: uncertain.PID}}

	if err := env.manager.Up(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("retry error = %v, want uncertain ownership", err)
	}
	if got := env.runner.startCount(); got != 1 {
		t.Fatalf("retry started duplicate Core: starts=%d", got)
	}
}
```

**同时检查两条相邻的既有测试**(它们的 runner 本来就诚实,应当仍绿):
- `manager_test.go:~1010` 那条(`newRunner()` 注入了 `ScanRunningCores`:fork 之前干净、fork 之后有 Core)—— 它是「诚实扫描器」的现成范本;重新求证在第一次扫描就扫到,立即拒绝,不会变慢。
- `TestManagerLateLaunchCleanupProofClearsUncertaintyForRetry`(`~1094`)—— 它用的是 `noCoresRunning`,即「如实查过,系统里确实没有 Core」,与它要表达的「fork 清理**已经**被证明」一致,应当仍绿;若它里面有第二次 `Up` 且会走到释放,给它加 `coreScanSettle` 缩短(用 `t.Cleanup` 还原),**只改速度,一个断言都不许动**。

- [ ] **Step 6: 跑绿**

Run: `go test ./internal/guardian -count=1` 与 `go test -race ./internal/guardian -count=1`
Expected: 全绿。

- [ ] **Step 7: 全量验证**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Run: 六个交叉编译目标各一遍
Run: gofumpt 检查(看打印内容)

- [ ] **Step 8: 变异验证(本 task 最重要的一步)**

| 变异 | 必须转红 | 它证明了什么 |
|---|---|---|
| `recheckOwnershipUncertain` 的拒绝分支删掉(永远释放) | `TestManagerHealthFailureUsesLiveBoundedCleanupAndBlocksRetryWhenExitUnproven`、`TestUserInitiatedUpStillRefusesWhenACoreIsStillRunning`、`...WhenTheScanFails` | 改造后的那条测试**仍然**在守它原本那条安全属性 |
| 把 `!stopped` 里的 `reason == "core_still_running"` 单独放行(只在「确知还在」时放行) | 同上第一条 | 换个角度证明它盯的是「系统说还有」 |
| **把改造后测试里那句 `env.runner.scanResult = ...` 注掉**(退回默认的「干净机器」) | 那条测试本身必须红 | **注入的扫描器是承重的** —— 它现在绿是因为系统说有 Core,不是因为锁存粘住了 |
| `confirmNoCoreForRelease` 的循环改成 `attempt < 1`(一次干净就放行) | `TestUserInitiatedUpReVerifiesAndProceedsWhenTheSystemIsClean` 的 `scanCount != 2` 断言 | 两次干净这条纪律在**接线之后**仍然成立,不只是助手内部的事 |
| `upLocked` 里 `origin == upOriginUser` 改成恒真 | `TestStartupRecoveryDoesNotReVerifyOwnership` | 启动恢复那条路真的没被卷进来 |
| `recoverLocked` 的调用点改成 `upOriginUser` | 同上 | 分支映射没写反(本仓库出过「分支映射错」的守卫漏洞) |
| `Up` 的调用点改成 `upOriginStartupRecovery` | `TestUserInitiatedUpReVerifiesAndProceedsWhenTheSystemIsClean` | 反方向也钉住 |

**每条变异都不带 `-run` 跑整包**,还原后重新证绿。

- [ ] **Step 9: 提交**

```bash
git add internal/guardian/manager.go internal/guardian/manager_test.go internal/guardian/manager_latch_test.go
git commit -m "$(cat <<'EOF'
feat(guardian): 用户发起的 Up 重新求证所有权,不再把第一次的结论钉死

十五个 uncertain 产地里只有三个真的意味着「有第二个 Core」,其余是一次失败的
扫描、一次失败的 unlink、或一个可能早就退出了的嫌疑 —— 而它们全都锁死同一扇门。
现在用户发起的 Up 每次都用 confirmNoCoreForRelease 重新求一遍(两次扫描都干净才放行):
判据一个字没变,只有「确知没有」才释放,扫不动/扫到了/求证失败一律保持拒绝。

启动恢复保留便宜的短路:retryDaemonRecovery 每 5 秒重试且永不放弃,把这套
纪律搬进去等于一天一万七千轮,违反设计里「不许搬进按时钟驱动的路径」那条。

TestManagerHealthFailureUsesLiveBoundedCleanupAndBlocksRetryWhenExitUnproven
如期转红:它编码的安全属性(fork 清理未证实时不许重试)是对的,错的只是它用
「粘住」来表达。改为注入一个诚实的扫描器 —— 清理没能证明退出,所以求证时那个
进程就该还在。变异验证:注掉那次注入,它立刻转红。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: `Migrate` 同样改为重新求证

**Files:**
- Modify: `internal/guardian/manager.go:482-485`
- Test: `internal/guardian/manager_latch_test.go`(追加)

**Interfaces:**
- Consumes: Task 6 的 `recheckOwnershipUncertain`。
- Produces: 无新符号。

**背景**:`Migrate` 是 `bx up` 在一台还带 legacy Core 的机器上走的那条路,同样是**用户显式说的 on**。它今天的短路与 `upLocked` 那处一模一样(`uncertainOwnership(m.current, nil)`),漏掉它等于修了一半 —— 而「只修一跳是假绿」是这个仓库反复付过学费的形状(`60b76f3`)。

- [ ] **Step 1: 写失败测试**

追加到 `internal/guardian/manager_latch_test.go`:

```go
// Migrate 是 bx up 在还带 legacy Core 的机器上走的那条路,同样是用户显式说的 on。
// 漏掉它就是「只修一跳」——这个仓库为这个形状付过学费(60b76f3)。
func TestMigrateReVerifiesOwnershipWhenTheSystemIsClean(t *testing.T) {
	// 释放要跨一个沉降窗口(拒绝不用)。
	restore := coreScanSettle
	coreScanSettle = time.Millisecond
	t.Cleanup(func() { coreScanSettle = restore })

	env := newManagerTestEnv(t)
	env.manager.current = Process{PID: 4242, Uncertain: true}
	env.manager.uncertainCause = errors.New("Core appeared to be running (PID 4242)")

	err := env.manager.Migrate(context.Background(), MigrationRequest{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}})
	if errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("扫干净了 Migrate 仍以所有权不确定拒绝: %v", err)
	}
	if env.manager.current.Uncertain {
		t.Fatal("Migrate 没有释放已被求证的锁存")
	}
}

// 另一头:系统说还有 Core 在跑 ⇒ 照旧拒绝,且一个 Core 都不起。
// (第一次就扫脏 ⇒ 立即拒绝、不沉降。)
func TestMigrateStillRefusesWhenACoreIsStillRunning(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.scanResult = []Process{{PID: 4242}}
	env.manager.current = Process{PID: 4242, Uncertain: true}

	err := env.manager.Migrate(context.Background(), MigrationRequest{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}})
	if !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Migrate = %v, want 仍然拒绝", err)
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("拒绝时起了 %d 个 Core", got)
	}
}
```

`MigrationRequest` 的字段以 `internal/guardian/migration.go` 与 `migration_test.go` 里的现成样例为准。

- [ ] **Step 2: 跑红**

Run: `go test ./internal/guardian -count=1`
Expected: `TestMigrateReVerifiesOwnershipWhenTheSystemIsClean` FAIL;第二条 PASS。

- [ ] **Step 3: 最小实现**

`manager.go:482-485` 改为:

```go
	// 与 upLocked 同一条规矩、同一个助手:Migrate 是 bx up 在一台还带 legacy Core
	// 的机器上走的那条路,同样是用户显式说的 on。只改一处就是假绿(60b76f3 的教训)。
	if err := m.recheckOwnershipUncertain("migrate"); err != nil {
		return err
	}
```

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/guardian -count=1` 与 `go test -race ./internal/guardian -count=1`
Expected: PASS。**注意**:Task 5 写的 `TestLatchedMigrateRefusalKeepsTheOriginalCause` 现在会走进重新求证 —— 该 env 的 `scanResult` 为零值(干净机器),于是它会**释放**并不再报 uncertain。**这条测试必须改**:给它加 `env.runner.scanResult = []Process{{PID: 4242}}`,让它在「系统说还有」的前提下继续断言 cause 被带出(第一次就扫脏,不沉降,不必缩 `coreScanSettle`)。这是一次合法的测试改动 —— 它守的属性(cause 不丢)没变,变的是造出该属性的前提。

- [ ] **Step 5: 全量验证**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Run: gofumpt 检查(看打印内容)

- [ ] **Step 6: 变异验证**

| 变异 | 必须转红 |
|---|---|
| 把这段改回 `if m.current.Uncertain { ...; return uncertainOwnership(m.current, m.uncertainCause) }` | `TestMigrateReVerifiesOwnershipWhenTheSystemIsClean` |
| 把 `recheckOwnershipUncertain` 的返回值丢弃(`_ = m.recheck...`) | `TestMigrateStillRefusesWhenACoreIsStillRunning` |
| `hop` 传 `"up"`(标签写错) | 应当**没有**测试转红 —— 记下来:hop 只进日志,没有测试守着,可接受 |

还原 + 重新证绿。

- [ ] **Step 7: 提交**

```bash
git add internal/guardian/manager.go internal/guardian/manager_latch_test.go
git commit -m "$(cat <<'EOF'
feat(guardian): Migrate 与 Up 同样重新求证所有权

Migrate 是 bx up 在还带 legacy Core 的机器上走的那条路,同样是用户显式说的 on,
短路形状与 upLocked 那处一模一样。只改一处就是假绿(60b76f3 的教训)。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: 三处消费方文案与行为对齐

**Files:**
- Modify: `internal/cli/client.go`(`guardianCodeHints["core_ownership_uncertain"]` 与它上面那段注释)
- Modify: `internal/cli/upgraderun.go:146-163`(中止文案里那句「只有 sudo bx down 再 sudo bx up 能解」)
- Modify: `internal/guardian/process.go`(`ownershipUncertainEscapeHint` 常量与它的注释)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/ToggleController.swift`(`toggleFailureHint` 的 `core_ownership_uncertain` 分支)
- Modify: `apps/macos/BxMenu/Tests/ToggleControllerTests.swift`(断言跟着改)
- Test: `internal/cli/cli_test.go`(新增一条钉住 Go 侧文案不再声称「只有 down 能清」)

**Interfaces:**
- Consumes: Task 7/8 之后的真实行为。
- Produces: 无新符号,只有文案。

**背景**:这条出路的文案**不是**「不存在」,而是**已经过时** —— 它出现在 CLI 的 `guardianCodeHints`、菜单的 `toggleFailureHint`、升级中止文案三处,三处都在说「只有 down 才会清」。Task 7 之后这句话在两个方向上都不再准确:① `bx up` 自己就会重新求证;② 若它**仍然**拒绝,那说明系统里真有一个 Core、或者根本扫不动 —— 此时再跑一遍 down+up 并不会有帮助,该看的是 Guardian 日志。

- [ ] **Step 1: 写失败测试**

`internal/cli/cli_test.go` 追加:

```go
// 指引必须描述**现在**的行为。core_ownership_uncertain 已经不是一条靠 down
// 清掉的锁存了:bx up 每次都会重新向系统求证,它仍然拒绝就意味着系统里真有一个
// Core、或者根本扫不动 —— 再跑一遍 down+up 帮不上忙,该看的是 Guardian 日志。
func TestOwnershipUncertainHintNoLongerClaimsDownIsTheOnlyEscape(t *testing.T) {
	hint, ok := guardianCodeHints["core_ownership_uncertain"]
	if !ok {
		t.Fatal("core_ownership_uncertain 没有指引 —— 这个码最需要指引")
	}
	if strings.Contains(hint, "只有 down") || strings.Contains(hint, "已锁存") {
		t.Fatalf("指引还在把它描述成一条靠 down 清掉的锁存: %q", hint)
	}
	if !strings.Contains(hint, install.GuardianStderrLogPath) {
		t.Fatalf("指引没有把人送到唯一写着完整原因的地方(Guardian 日志): %q", hint)
	}
}
```

(`install.GuardianStderrLogPath` 在同文件里已被别处引用;若引用名不同,按 `client.go` 顶部那个 `install.GuardianStderrLogPath + ...` 的写法照抄。)

`apps/macos/BxMenu/Tests/ToggleControllerTests.swift` 里那条断言改成:

```swift
        let uncertain = toggleFailureHint(code: "core_ownership_uncertain")
        expect(uncertain != nil, "所有权不确定必须有指引 —— 它是最无从下手的失败")
        expect(uncertain?.contains("bx-guard.err.log") == true,
               "指引必须把人送到唯一写着完整原因的地方:Guardian 日志")
        expect(uncertain?.contains("down") == false,
               "bx up 现在每次都会重新求证;它仍然拒绝时再跑一遍 down+up 帮不上忙")
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/cli -count=1`
Expected: FAIL(`还在把它描述成一条靠 down 清掉的锁存`)。
Run: `bash scripts/test-macos-menu.sh`
Expected: `toggle-controller` 那个套件 FAIL。

- [ ] **Step 3: 最小实现**

`internal/cli/client.go`:把该条改成(并把它上面那段讲「latch 刻意留着」的英文注释改写成讲「现在每次 up 都会重新求证,仍然拒绝意味着什么」):

```go
	"core_ownership_uncertain": "Guardian 没能证明系统里没有第二个 bx Core 在跑,因此拒绝再起一个" +
		"(两个 Core 会争默认路由,先退出的那个用旧快照还原、掀掉另一个的劫持)。" +
		"每次 sudo bx up 都会重新向系统求证,所以重跑一遍没有意义:" +
		"先 sudo tail -50 " + install.GuardianStderrLogPath +
		" 看是扫到了哪个进程(guardian_core_still_running / guardian_core_scan)——" +
		"若是另一个终端里的 sudo bx run,退出它再 sudo bx up",
```

`internal/guardian/process.go` 的 `ownershipUncertainEscapeHint`:

```go
// ownershipUncertainEscapeHint 告诉用户这条判定会怎么解开。
//
// 它**不再**是一条只能靠 down 清掉的锁存:用户发起的 up/migrate 每次都会经
// recheckOwnershipUncertain 重新求证一遍,两次扫描都干净(确知没有 Core 在跑)
// 就自行释放。仍然拒绝就说明系统里真有一个、或者根本扫不动 —— 那时该看的是
// Guardian 日志里那两行普查,不是再跑一遍 down+up。
const ownershipUncertainEscapeHint = "`sudo bx up` re-verifies this on every attempt; if it still refuses, a Core really is running (or the scan cannot answer) — see guardian_core_scan / guardian_core_still_running in /var/log/bx-guard.err.log"
```

`internal/cli/upgraderun.go`:把那句「(core_ownership_uncertain,只有 sudo bx down 再 sudo bx up 能解)」改成「(core_ownership_uncertain —— 每次 sudo bx up 都会重新求证,但只要那个 Core 还在跑就一直拒绝)」。

`ToggleController.swift`:

```swift
    case "core_ownership_uncertain":
        return "bx re-checked and still cannot prove no second bx Core is running. " +
            "Quit any `sudo bx run` you have open, then try again — " +
            "see sudo tail -50 /var/log/bx-guard.err.log for which process it found"
```

(菜单文案必须是英文 —— 既有 CJK 守卫禁止用户可见串混入中文。)

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/cli -count=1`
Run: `swift build --package-path apps/macos/BxMenu`
Run: `bash scripts/test-macos-menu.sh`
Expected: 全绿。**注意** `GuardianFailureCodeTests.swift` 是拿 `toggleFailureHint(code:)` 的返回值做对照的(`expect(uncertain.message == toggleFailureHint(...))`),所以它自动跟着新文案走,不需要改。若它红了,说明你在别处硬编码了旧文案。

- [ ] **Step 5: 全量验证**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Run: 六个交叉编译目标各一遍
Run: gofumpt 检查(看打印内容)
**不需要改 `scripts/test-macos-menu.sh`** —— 本 task 没有新增 Swift 测试文件,改的两个文件都已注册。(若你出于任何理由新建了 `Tests/*.swift`,**必须**同时在脚本里注册,否则它永远不会跑而 CI 照样绿;`internal/cli/macos_menu_suite_registration_test.go` 会抓住这一点。)

- [ ] **Step 6: 变异验证**

| 变异 | 必须转红 |
|---|---|
| `guardianCodeHints["core_ownership_uncertain"]` 改回旧文案(含「只有 down」) | `TestOwnershipUncertainHintNoLongerClaimsDownIsTheOnlyEscape` |
| 从该条里去掉 `install.GuardianStderrLogPath` | 同上 |
| `ToggleController.swift` 的该分支改回旧文案 | `bash scripts/test-macos-menu.sh` 的 `toggle-controller` 套件 |
| `ToggleController.swift` 的该分支 `return nil` | 同上 |

Swift 那两条变异跑的是 `bash scripts/test-macos-menu.sh`(整脚本,不过滤)。还原 + 重新证绿。

- [ ] **Step 7: 提交**

```bash
git add internal/cli/client.go internal/cli/upgraderun.go internal/cli/cli_test.go internal/guardian/process.go apps/macos/BxMenu/Sources/BxMenu/ToggleController.swift apps/macos/BxMenu/Tests/ToggleControllerTests.swift
git commit -m "$(cat <<'EOF'
docs(cli,menu): 三处指引改说现在的行为,不再声称只有 down 能清

CLI 的 guardianCodeHints、菜单的 toggleFailureHint、升级中止文案与
ownershipUncertainEscapeHint 都在说「只有 sudo bx down 再 sudo bx up 能解」。
每次 up 都会重新求证之后,这句话两头都不准:up 自己就会试,而它仍然拒绝
就意味着系统里真有一个 Core 或根本扫不动 —— 那时该看 Guardian 日志里的
guardian_core_scan / guardian_core_still_running,不是再跑一遍 down+up。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: 更正 `CLAUDE.md` 里那两条已经不成立的说法

**Files:**
- Modify: `CLAUDE.md`(macOS 那一大段里「**已知限制:所有权不确定是锁存的**……唯一脱身办法是 **`sudo bx down` 再 `sudo bx up`**」那几句)

**Interfaces:** 无代码,不需要测试;但**必须**在改之前把 Task 1–9 的实际行为读一遍,不要凭这份计划的措辞去写文档 —— 计划与代码不一致时以代码为准。

- [ ] **Step 1: 定位**

Run: `grep -n "所有权不确定是锁存的" CLAUDE.md`
把该段前后各读 20 行,确认上下文(它挨着「Core 所有权改判据(2026-08-07,进程扫描)」那一节)。

- [ ] **Step 2: 改写**

把「已知限制」那几句替换成(措辞按实际实现校准):

> **锁存已有出口(2026-08-11)**:上面那条「所有权不确定是锁存的、唯一脱身办法是 `sudo bx down` 再 `sudo bx up`」**两半都不再成立,而且其中一半从来就不成立**。① `Down` 从不读 `Uncertain`,它按 `m.current.PID` 分支,三种锁存形状(PID 0 + desired=off / PID 0 + desired=on / 带真实 PID)分别停在提前返回、`runner.Existing` 的同一次失败扫描、`runner.Stop` 开头必然失败的身份校验 —— 全都到不了那句 `m.current = Process{}`。用户报告它「有时管用」,靠的是 CLI 的 `bx down` 失败落到强制拆除、把 Guardian 整个 bootout 掉:**真正管用的一直是杀掉 daemon**。现在 `Down` 在所有出口无条件清掉进门时那一个锁存(但**不清**它自己在 DNS 还原补偿里新造的那个,那会是 fail-open)。② **用户发起**的 `Up`/`Migrate` 不再短路,改为 `confirmNoCoreForRelease` **重新求证**:**两次扫描都干净、中间隔一个沉降窗口**才释放,扫到了(哪怕只有一次)/扫不动/runner 不会扫/panic 一律保持拒绝,判据一个字没变。**它与 `Down` 用的 `confirmCoreStopped` 是两个函数、偏置刻意相反**:后者第一次扫干净就算数(怕把正常关闭报成告警),对一次假的「全清」并不比裸扫描强;而释放锁存是准入控制,`cleanupFailedStart` 那个残留(已 fork、`Terminate()` 已发、wait 超时,进程既非僵尸也未消失,而扫描会跳过 argv 读不出的进程)恰恰就是假的全清 —— **把两者「统一」掉就是把双 Core 的门打开**。**启动恢复不走这条** —— `retryDaemonRecovery` 每 5 秒重试且永不放弃,把这套纪律搬进按时钟驱动的路径就是一天一万七千轮;「有限次数地自愈瞬时失败」是刻意留给后续的(照 `maxPathRecoveryAttempts` 的形状)。③ 锁存不再经 `recoverLocked` 升级成 `recoveryBlocked`(判据与 `hold.go` 对 `ErrMaintenanceHoldUnreadable` 的处置同源):**「开不了」永不许升级成「关不掉」**。④ 两处「已证明安全」的产地不再产出这条判定 —— `finishExistingWatch`(OS 确认进程消失)与 `Start` 自己那条 wait goroutine(`waitpid` 已返回,证明更硬),删不掉一个 JSON 是清理失败不是所有权存疑;`Stop`/`Existing` 那几处处置的是可能仍属于活进程的记录,不动。**非 darwin 上重新求证是 no-op**(`procscan_other.go` 恒返回错误 ⇒ 恒拒绝),门仍然焊死;谁移植 Guardian 必须先实现 `scanRunningCores`。**真机未验** —— 以上全部由单元测试覆盖,没有人在真机上人为造一个锁存验证 `bx up` 是否真的恢复。设计 `docs/superpowers/specs/2026-08-11-ownership-uncertain-exit-design.md`、计划 `docs/superpowers/plans/2026-08-11-ownership-uncertain-exit.md`。

- [ ] **Step 3: 交叉检查同段里别的过时句子**

Run: `grep -n "只有 sudo bx down\|down 再 sudo bx up\|锁存" CLAUDE.md`
逐条判断是否还成立;不成立的就地改掉,成立的不动。**不要顺手重写整段** —— 那段记录着好几期的事故与教训,它们仍然有效。

- [ ] **Step 4: 验证**

Run: `go build ./... && go vet ./... && go test ./... -count=1`(文档改动不该影响任何东西,这一步是防手滑)

- [ ] **Step 5: 提交**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
docs: 更正「所有权不确定是锁存的、只能 down+up」——两半都不再成立

Down 从不读 Uncertain,三种锁存形状都到不了清除那一句;用户报告的「有时管用」
靠的是 CLI 失败落到强制拆除把 Guardian 整个 bootout。现在 Down 无条件清掉进门
时那一个,用户发起的 Up/Migrate 每次重新求证,锁存不再升级成 recoveryBlocked,
已证明安全的清理失败不再产出这条判定。非 darwin 仍焊死。真机未验。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

## 收尾检查(全部 task 做完之后跑一遍)

- [ ] `go build ./... && go vet ./... && go test ./... -count=1`
- [ ] `go test -race ./internal/guardian ./internal/cli -count=1`
- [ ] 六个交叉编译目标:`linux/amd64`、`linux/arm64`、`darwin/amd64`、`darwin/arm64`、`windows/amd64`、`windows/arm64`
- [ ] `swift build --package-path apps/macos/BxMenu`
- [ ] `bash scripts/test-macos-menu.sh`
- [ ] `git ls-files '*.go' | grep -v 'embedded/assets\|internal/winfw' | xargs "$(go env GOPATH)/bin/gofumpt" -l` —— **打印为空**
- [ ] `go test ./internal/guardian -run TestReconcile -count=1` **之外**再跑一次不过滤的整包,确认 `TestReconcileOnceNeverClearsTheOwnershipUncertainLatch` 与 `reconcile_loop.go:88-91,147`、`reconcile.go:35-40,168-169` 一行未改(`git diff --stat` 里不该出现这两个文件)
- [ ] `git log --oneline -10` 复核:10 个提交、每个都有 `Co-Authored-By`、都在 `master` 上
- [ ] `manager_confirm_test.go` 五条全绿 —— 尤其 `TestConfirmCoreStoppedReScansAfterASettleWindow`:它与 `TestRecheckDoesNotReleaseWhenOnlyTheSecondScanIsClean` 断言的是**同一个扫描脚本下的相反结论**,两条同时绿才证明那两个偏置没有被合并
- [ ] **真机验收无人执行,必须写进最终汇报**:没有人在真机上人为造一个孤儿标记 / 第三方 `sudo bx run`,验证 `bx up` 是否真的能自己恢复、以及 `bx down` 之后 `bx up` 是否不再被拒。
