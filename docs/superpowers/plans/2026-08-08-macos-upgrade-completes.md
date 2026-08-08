# macOS 升级一次做完 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `sudo bx app-install`(及菜单的 Install / Repair)在 Guardian 正在跑时,一次确认后把升级做完——停 Core、换文件、重启 Guardian、恢复原状态——不再留下「已装但没生效」的半成品。

**Architecture:** 判定逻辑(要做哪些步、每步失败说什么)进 `internal/cli` 的纯函数并单测;
执行复用既有原语(`macOSDownLifecycleDetailed`、`install.BootoutGuardian`、
`macOSUpLifecycle`),不新造生命周期机制。**先 Down 再换文件**,使网络在整个换装期间
处于直连可用状态——「最差只是没保护、不会断网」由此成为结构保证。

**Tech Stack:** Go 1.26 · `internal/cli`(darwin build tag)· `internal/install`

## Global Constraints

- **先停 Core、再换文件。** 顺序不可调换:它保证其后任何一步失败,机器都是「网络直连可用、
  只是没有保护」,而不是「路由指向已消失的 TUN、整机断网」。对一个用来翻墙的工具,
  断网意味着用户连重装包都下不了。
- **不做版本回滚**,不建「上次升级失败」状态机(项目所有者定:回滚是开发视角,
  用户遇到升级失败会直接卸载重装)。失败就如实说清并指向 `sudo bx uninstall` 后重装。
- **不改 `Daemon.Shutdown` 的语义**。今天 Core 活过 Guardian 重启是**有意的**
  (Guardian 崩了不该连累用户的连接);升级路径显式 Down 即可。
- **绝不运行 `bx`、`sudo bx`、`launchctl`、`networksetup`、`route`**,不得启动或安装 bx。
- **绝不运行 `git stash`、`git reset --hard`、`git checkout -- .`**(仓库有一条无关的
  既有 stash 必须存活)。
- 不得削弱任何既有断言。
- 每个任务结束前跑 `go build ./... && go vet ./... && go test ./... -count=1`,全绿才提交。
- 中文 conventional commits,结尾 `Co-Authored-By: Claude <noreply@anthropic.com>`,直接提交 `master`。

## 现有原语(已核对,直接用,不要重造)

| 名字 | 位置 | 作用 |
|---|---|---|
| `install.GuardianActive() bool` | `internal/install/guardian_darwin.go` | Guardian 是否在跑 |
| `install.BootoutGuardian(ctx) error` | 同上 | 停掉 Guardian daemon |
| `install.EnableGuardianWithProbe(func() bool) error` | 同上 | bootstrap Guardian |
| `macOSDownLifecycleDetailed(ctx, configPath, deps)` | `internal/cli/guardian.go:396` | 停保护;干净路径失败自动落强制拆除 |
| `macOSUpLifecycle(ctx, configPath, deps)` | `internal/cli/guardian.go:327` | 起保护 |
| `install.UnifiedInstall(opts)` | `internal/install` | 装 Bx.app + runtime + 切符号链接 |

**实施者须先自行确认三件事**(本文档不替你断言,读代码或写临时探针):
1. `macOSUpLifecycle` 在 Guardian **未 bootstrap** 时会不会自己 bootstrap;若会,
   Task 2 就不必显式调 `EnableGuardianWithProbe`。
2. `BootoutGuardian` 之后 launchd 的 `KeepAlive` 会不会把 job 拉回来(bootout 应当是
   卸载 job,`KeepAlive` 不再适用,但要确认)。
3. `desired` 的读写入口(`guardian` 包的 store),Task 1 的判定要用到。

---

### Task 1: 升级计划与文案(纯逻辑)

**Files:**
- Create: `internal/cli/upgradeplan.go`
- Create: `internal/cli/upgradeplan_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `type UpgradeStep int`,取值 `UpgradeStopProtection` / `UpgradeInstallFiles` /
    `UpgradeRestartGuardian` / `UpgradeStartProtection`
  - `func upgradeSteps(guardianRunning bool, desiredOn bool) []UpgradeStep`
  - `func upgradeConfirmMessage(desiredOn bool) string`
  - `func upgradeFailureMessage(step UpgradeStep, err error) string`

- [ ] **Step 1: 写失败测试**

新建 `internal/cli/upgradeplan_test.go`:

```go
package cli

import (
	"errors"
	"strings"
	"testing"
)

// Guardian 没在跑就只装文件 —— 没有运行中的进程要换,也就没有断网的理由。
func TestUpgradeStepsWithoutRunningGuardianOnlyInstalls(t *testing.T) {
	got := upgradeSteps(false, false)
	want := []UpgradeStep{UpgradeInstallFiles}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if len(upgradeSteps(false, true)) != 1 {
		t.Fatal("Guardian 没在跑时,desired 是什么都不该多做")
	}
}

// 关键顺序:停保护必须排在装文件之前。
//
// 它让网络在整个换装期间是直连可用的,于是其后任何一步失败,最差只是「没有保护」,
// 而不是「路由指向已消失的 TUN、整机断网」—— 对一个翻墙工具,后者意味着用户连
// 重装包都下不了。
func TestUpgradeStepsStopProtectionBeforeInstalling(t *testing.T) {
	steps := upgradeSteps(true, true)
	stop, install := -1, -1
	for i, s := range steps {
		switch s {
		case UpgradeStopProtection:
			stop = i
		case UpgradeInstallFiles:
			install = i
		}
	}
	if stop < 0 || install < 0 {
		t.Fatalf("steps = %v, 必须同时包含停保护与装文件", steps)
	}
	if stop > install {
		t.Fatalf("停保护(%d)必须早于装文件(%d),否则失败会留下断网状态", stop, install)
	}
}

// 升级前开着,升级后要开回来;原本关着就不要擅自打开。
func TestUpgradeStepsRestoreDesiredState(t *testing.T) {
	on := upgradeSteps(true, true)
	if on[len(on)-1] != UpgradeStartProtection {
		t.Fatalf("原本开着,最后一步必须是起保护,实际 %v", on)
	}
	for _, s := range upgradeSteps(true, false) {
		if s == UpgradeStartProtection {
			t.Fatal("原本关着,不得擅自打开保护")
		}
	}
}

// Guardian 必须被重启,否则换了符号链接也没用 —— 已在跑的进程不会因此换代码。
// 这正是 2026-08-08 那次真机事故的根因。
func TestUpgradeStepsAlwaysRestartGuardianWhenItIsRunning(t *testing.T) {
	for _, desired := range []bool{true, false} {
		found := false
		for _, s := range upgradeSteps(true, desired) {
			if s == UpgradeRestartGuardian {
				found = true
			}
		}
		if !found {
			t.Fatalf("desired=%v:Guardian 在跑就必须重启它", desired)
		}
	}
}

// 确认文案必须明说会断网,而不是含糊的「可能有短暂中断」。
func TestUpgradeConfirmMessageStatesTheOutage(t *testing.T) {
	msg := upgradeConfirmMessage(true)
	for _, must := range []string{"断网", "重启保护"} {
		if !strings.Contains(msg, must) {
			t.Fatalf("确认文案必须包含 %q,实际 = %q", must, msg)
		}
	}
}

// 失败文案必须说清「现在处于什么状态」,而不只是抛出错误。
func TestUpgradeFailureMessageSaysNetworkIsUsable(t *testing.T) {
	msg := upgradeFailureMessage(UpgradeStartProtection, errors.New("boom"))
	if !strings.Contains(msg, "网络") {
		t.Fatalf("装文件之后的失败必须说明网络仍可用(直连),实际 = %q", msg)
	}
	if !strings.Contains(msg, "uninstall") {
		t.Fatalf("失败必须给出真正管用的下一步(卸载重装),实际 = %q", msg)
	}
}
```

- [ ] **Step 2: 跑测试确认全红**

Run: `go test ./internal/cli -run TestUpgrade -count=1`
Expected: 编译失败(符号不存在)。

- [ ] **Step 3: 实现**

新建 `internal/cli/upgradeplan.go`:

```go
package cli

import "fmt"

// UpgradeStep 是升级流程中的一步。
//
// 抽成纯函数是因为顺序本身就是正确性:停保护必须早于装文件,而这一点只有在
// 它被单独测到时才不会在某次重构里被悄悄换掉(2026-08-08 真机事故的根因是
// 「换了符号链接但没重启进程」,同一类问题)。
type UpgradeStep int

const (
	// UpgradeStopProtection 停掉保护。放在最前面:它把网络还原成直连,
	// 于是其后任何一步失败,最差都只是「没有保护」而不是「断网」。
	UpgradeStopProtection UpgradeStep = iota
	UpgradeInstallFiles
	// UpgradeRestartGuardian 重启 daemon。仅仅换掉 runtime/current 符号链接
	// 是不够的 —— 一个已在跑的进程不会因此换代码。
	UpgradeRestartGuardian
	UpgradeStartProtection
)

func upgradeSteps(guardianRunning bool, desiredOn bool) []UpgradeStep {
	if !guardianRunning {
		// 没有运行中的进程要换,也就没有断网的理由。
		return []UpgradeStep{UpgradeInstallFiles}
	}
	steps := []UpgradeStep{UpgradeStopProtection, UpgradeInstallFiles, UpgradeRestartGuardian}
	if desiredOn {
		steps = append(steps, UpgradeStartProtection)
	}
	return steps
}

// upgradeConfirmMessage 明说会断网。
//
// 旧设计为了不打扰用户而绕开断网,代价是留下一个用户理解不了、Repair 也修不了的
// 中间态 —— 比直接断网难懂得多。含糊的「可能有短暂中断」同理:用户开着会议时
// 需要的是一个能据以决定「现在还是待会」的事实。
func upgradeConfirmMessage(desiredOn bool) string {
	if desiredOn {
		return "升级需要重启保护,期间会断网几秒。现在继续吗?"
	}
	return "升级需要重启保护服务,期间会断网几秒(当前保护未开启)。现在继续吗?"
}

func upgradeFailureMessage(step UpgradeStep, err error) string {
	switch step {
	case UpgradeStopProtection:
		return fmt.Sprintf("停止保护失败,升级未开始,当前状态未变:%v", err)
	default:
		// 走到这里说明已经停过保护 —— 网络已还原为直连,可用。
		return fmt.Sprintf(
			"升级未完成:%v\n网络仍可正常使用(直连,无保护)。"+
				"若反复失败,执行 sudo bx uninstall 后重新安装。", err)
	}
}
```

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/cli -run TestUpgrade -count=1 -v`
Expected: 全部 PASS。

- [ ] **Step 5: 变异验证**

把 `upgradeSteps` 里 `UpgradeStopProtection` 与 `UpgradeInstallFiles` 的顺序对调,跑
`go test ./internal/cli -run TestUpgradeStepsStopProtectionBeforeInstalling -count=1`
Expected: FAIL。确认后改回。

去掉 `UpgradeRestartGuardian`,跑
`go test ./internal/cli -run TestUpgradeStepsAlwaysRestartGuardian -count=1`
Expected: FAIL。确认后改回。

- [ ] **Step 6: 提交**

```bash
git add internal/cli/upgradeplan.go internal/cli/upgradeplan_test.go
git commit -m "$(cat <<'EOF'
feat(install): 升级步骤与文案的纯逻辑

顺序本身就是正确性:停保护必须早于装文件 —— 它让网络在整个换装期间处于
直连可用状态,于是其后任何一步失败,最差只是「没有保护」而不是「断网」。
对一个翻墙工具,断网意味着用户连重装包都下不了。

确认文案明说会断网。旧设计为了不打扰用户而绕开断网,代价是留下一个用户
理解不了、Repair 也修不了的中间态。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: app-install 执行完整升级

**Files:**
- Modify: `internal/cli/appinstall_darwin.go`(整个 `appInstallAction`)
- Modify: `internal/cli/cli.go`(给 `app-install` 加 `--yes` flag)
- Test: `internal/cli/upgradeplan_test.go`(追加回归守卫)

**Interfaces:**
- Consumes: `upgradeSteps`、`upgradeConfirmMessage`、`upgradeFailureMessage`(Task 1)
- Produces: 无

**关键背景:** 现状见 `appinstall_darwin.go:55-57`——检测到 Guardian 在跑时只打印一句
「建议尽快执行 `sudo bx down && sudo bx up` 完成切换」。**那句建议无效**:`bx down` 的
干净路径不 bootout Guardian,所以 down/up 只是让同一个旧 Guardian 重起了一个旧 Core。
用户照做两遍仍是旧版本(2026-08-08 真机实证)。

- [ ] **Step 1: 写回归守卫**

在 `internal/cli/upgradeplan_test.go` 追加:

```go
// 那句建议用户照做也没用:bx down 的干净路径不 bootout Guardian,
// 所以 down/up 只会让同一个旧 Guardian 重起一个旧 Core(2026-08-08 真机实证)。
// 照做也没用的指引比没有指引更糟 —— 它让用户以为自己已经处理过了。
func TestAppInstallDoesNotAdviseTheIneffectiveDownUp(t *testing.T) {
	source, err := os.ReadFile("appinstall_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Contains(text, "bx down && sudo bx up") {
		t.Fatal("这句建议无效,不得再出现:app-install 必须自己把升级做完")
	}
	if !strings.Contains(text, "upgradeSteps(") {
		t.Fatal("app-install 必须按 upgradeSteps 执行完整升级")
	}
}
```

(记得给该文件补 `"os"` import。)

- [ ] **Step 2: 跑测试确认红**

Run: `go test ./internal/cli -run TestAppInstallDoesNotAdvise -count=1`
Expected: FAIL(那句建议还在)。

- [ ] **Step 3: 改写 `appInstallAction`**

把 `install.UnifiedInstall(...)` 那一段改为按 `upgradeSteps` 驱动。骨架:

```go
	guardianRunning := install.GuardianActive()
	desiredOn := currentDesiredOn()   // 见下方说明
	steps := upgradeSteps(guardianRunning, desiredOn)

	if guardianRunning && !c.Bool("yes") {
		if !confirmOnTTY(upgradeConfirmMessage(desiredOn)) {
			fmt.Println("已取消,未做任何改动。")
			return nil
		}
	}

	var result install.UnifiedInstallResult
	for _, step := range steps {
		var err error
		switch step {
		case UpgradeStopProtection:
			fmt.Println("• 停止保护(网络将暂时回到直连)")
			_, err = macOSDownLifecycleDetailed(c.Context, c.String("config"), defaultMacOSLifecycleDeps())
		case UpgradeInstallFiles:
			result, err = install.UnifiedInstall(...)   // 原有调用原样搬过来
		case UpgradeRestartGuardian:
			fmt.Println("• 重启保护服务(使新版本生效)")
			err = install.BootoutGuardian(c.Context)
		case UpgradeStartProtection:
			fmt.Println("• 恢复保护")
			_, err = macOSUpLifecycle(c.Context, c.String("config"), defaultMacOSLifecycleDeps())
		}
		if err != nil {
			return errors.New(upgradeFailureMessage(step, err))
		}
	}
```

**你必须自行确认并按实际情况补全的三处**(本文档不替你断言):

1. `desiredOn` 从哪读——`guardian` 包的 store。若 `app-install` 拿不到,退而用
   `install.GuardianActive() && <控制 socket 报 protected>`;把你的取法写进注释。
2. `macOSUpLifecycle` 在 Guardian 已被 bootout 时会不会自己 bootstrap。若不会,
   `UpgradeRestartGuardian` 那步要在 `BootoutGuardian` 之后补
   `install.EnableGuardianWithProbe(...)`。
3. `defaultMacOSLifecycleDeps()` 的真实名字与构造方式(`macOSUpAction` /
   `macOSDownAction` 里怎么构造的,照抄)。

**`desiredOn` 必须在第一步之前读**——`UpgradeStopProtection` 会把 desired 写成 off,
之后再读就永远是 off,保护再也回不来。

删掉 `appinstall_darwin.go:55-57` 那三行(`GuardianActive` 的告警与无效建议)。

- [ ] **Step 4: 加 `--yes`**

在 `cli.go` 里 `app-install` 命令的 `Flags` 加:

```go
&urfavecli.BoolFlag{
	Name:  "yes",
	Usage: "跳过升级确认(供脚本/菜单使用;会断网几秒)",
},
```

`install.sh`(由 `scripts/package-macos-release.sh` 生成)里那行
`sudo "$DIR/Bx.app/Contents/Resources/bx-cli" app-install --app-source "$DIR/Bx.app"`
**不加 `--yes`**——命令行安装时用户就在终端前,该问就问。

- [ ] **Step 5: 跑全部测试**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Expected: 全绿,含新的回归守卫。

- [ ] **Step 6: 变异验证**

把那句建议加回 `appinstall_darwin.go`,跑
`go test ./internal/cli -run TestAppInstallDoesNotAdvise -count=1`
Expected: FAIL。确认后删掉。

- [ ] **Step 7: 提交**

```bash
git add internal/cli/appinstall_darwin.go internal/cli/cli.go internal/cli/upgradeplan_test.go
git commit -m "$(cat <<'EOF'
fix(install): app-install 一次把升级做完,不再留半成品

原先检测到 Guardian 在跑只打印一句「建议尽快执行 sudo bx down && sudo bx up
完成切换」——那句建议无效:bx down 的干净路径不 bootout Guardian,down/up
只是让同一个旧 Guardian 重起了一个旧 Core。用户照做两遍仍是旧版本
(2026-08-08 真机实证),而菜单据此正确地报 Repair Required,Repair 又只重装
文件、同样修不了。

现在一次确认后按序做完:停保护 → 换文件 → 重启 Guardian → 恢复原状态。
停保护排在最前是有意的:网络在整个换装期间是直连可用的,于是其后任何一步
失败,最差只是「没有保护」而不是「断网」。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `bx up` 的版本自检

**Files:**
- Modify: `internal/cli/guardian.go`(`macOSUpLifecycle` 或 `macOSUpAction`)
- Test: `internal/cli/upgradeplan_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `func upVersionMismatchMessage(guardianVersion, runtimeVersion string) string`
  ——不一致时返回提示,一致(或任一为空)时返回 `""`

**关键背景:** 2026-08-08 事故里 `bx up` 明明能看到 `runtime_version=phase2` 而自己是
`dev`,仍照常报 Protected。这是「信念 vs 事实」的又一个实例:up 相信自己是最新的,没去问。

- [ ] **Step 1: 写失败测试**

```go
// up 不能在自己是旧版时若无其事地报 Protected。
func TestUpVersionMismatchIsReported(t *testing.T) {
	if got := upVersionMismatchMessage("phase2", "phase2"); got != "" {
		t.Fatalf("版本一致时不该提示,实际 = %q", got)
	}
	if got := upVersionMismatchMessage("", "phase2"); got != "" {
		t.Fatalf("信息不全时不该猜,实际 = %q", got)
	}
	msg := upVersionMismatchMessage("dev", "phase2")
	if msg == "" {
		t.Fatal("Guardian 跑着旧版而盘上是新版,必须提示")
	}
	// 给出的命令必须是真正管用的那条。上次事故的根因正是一句看起来权威、
	// 执行起来无效的建议。
	if strings.Contains(msg, "bx down && sudo bx up") {
		t.Fatalf("不得给出那条无效建议,实际 = %q", msg)
	}
	if !strings.Contains(msg, "app-install") {
		t.Fatalf("必须指向真正能完成切换的入口,实际 = %q", msg)
	}
}
```

- [ ] **Step 2: 跑测试确认红**

Run: `go test ./internal/cli -run TestUpVersionMismatch -count=1`
Expected: 编译失败。

- [ ] **Step 3: 实现**

在 `internal/cli/upgradeplan.go` 追加:

```go
// upVersionMismatchMessage 在 Guardian 跑着旧版时给出提示。
//
// 2026-08-08:`bx up` 能看到 runtime 是新版而自己是旧版,却照常报 Protected ——
// 「信念 vs 事实」在这里又演了一遍。任一版本为空说明信息不全,不猜。
func upVersionMismatchMessage(guardianVersion, runtimeVersion string) string {
	if guardianVersion == "" || runtimeVersion == "" || guardianVersion == runtimeVersion {
		return ""
	}
	return fmt.Sprintf(
		"! Guardian 仍在跑旧版 %s(已安装 %s)。执行 sudo bx app-install 完成切换(会断网几秒)。",
		guardianVersion, runtimeVersion)
}
```

- [ ] **Step 4: 在 up 的输出里用上它**

在 `macOSUpAction`(或渲染 up 结果的地方)拿到 Guardian 状态后调用它,非空就打印。
**只提示、不阻断**——用户此刻可能正急着上网,拦住他没有好处;但那句提示必须给出
真正管用的命令。

- [ ] **Step 5: 跑全部测试并提交**

Run: `go build ./... && go vet ./... && go test ./... -count=1`

```bash
git add internal/cli/upgradeplan.go internal/cli/upgradeplan_test.go internal/cli/guardian.go
git commit -m "$(cat <<'EOF'
fix(cli): bx up 发现自己跑着旧版时说出来

2026-08-08 事故里 up 能看到 runtime 是新版而 Guardian 是旧版,仍照常报
Protected —— 「信念 vs 事实」又演了一遍。现在提示但不阻断(用户此刻可能
正急着上网),并给出真正能完成切换的命令,而不是上次那条照做也没用的建议。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

## 真机验收(由用户执行)

**必须检查版本真的换了,不能只看有没有报错**——上次验错的正是这一点:
「`down`→`up` 干净通过」被当成升级成功,而实际从未切换过。

1. 起保护 → 重新打包一个新版本号 → `./install.sh`
2. **全程只需一次确认**,不需要任何手工命令
3. 完成后 `bx status --json | grep version`,`core_version`/`guardian_version`/
   `runtime_version` **三者一致**
4. 菜单不再显示 Repair Required
5. 中途断网时长可感知但短(目测几秒)
6. 再试一次菜单里的 **Repair**,同样应完整生效

## 自查

**Spec 覆盖:** 一次确认做完 → Task 1+2;删掉无效建议 → Task 2(含回归守卫);
`bx up` 版本自检 → Task 3;Repair 走同一条路 → 由 Task 2 自动获得
(`repairBx` → `runEmbeddedInstaller` → `app-install`),真机验收第 6 项覆盖。

**占位符:** 无 TBD。Task 2 有三处显式要求实施者读代码确认(`desiredOn` 的来源、
`macOSUpLifecycle` 是否自 bootstrap、deps 的构造),那是**防止本文档的猜测覆盖代码事实**,
不是占位——每一处都写明了确认方法与两种情况各自怎么办。

**类型一致:** `UpgradeStep` 及其四个取值(Task 1)在 Task 2 的 switch 中穷尽使用;
`upgradeFailureMessage(step, err)` 的入参与 Task 2 的循环变量一致;
`upVersionMismatchMessage`(Task 3)与 Task 1 同文件,签名为两个 `string`。
