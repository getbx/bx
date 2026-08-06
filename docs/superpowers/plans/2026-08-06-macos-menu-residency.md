# macOS 菜单栏常驻与状态入口去重 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让菜单栏在崩溃后自动回来、主动退出时不被拉回,并删掉与一级菜单重复的 `Open Status` 入口。

**Architecture:** 纯 UI 层改动。三处 LaunchAgent plist 生成器补 `KeepAlive={SuccessfulExit:false}`;Swift 侧删除状态面板整条链路;退出确认文案抽成常量以便被现有测试单元覆盖。不碰 Guardian、CLI、保护语义。

**Tech Stack:** Go 1.26(`internal/install`)、Swift 6(`apps/macos/BxMenu`,自建 `@main` 测试可执行文件,非 XCTest)、bash 脚本。

设计文档:`docs/superpowers/specs/2026-08-06-macos-menu-residency-design.md`

## Global Constraints

- **`KeepAlive` 必须是字典形式 `{SuccessfulExit: false}`,绝不能写成 `true`。** `NSApp.terminate` 是干净退出(exit 0),`KeepAlive=true` 会让 launchd 立刻把菜单拉回来,`Quit bx` 静默失效。
- **`Quit bx` 与 `Turn Off bx` 都保留,行为不变。** 本计划只删 `Open Status`。
- **保留 `DNSPresentation` 与 `dnsPresentation`**(`StatusPresentation.swift`)——一级菜单的 DNS 行在用,只删 `StatusRow` 与 `StatusSnapshot`。
- 不碰 Guardian、CLI、保护语义;不碰 `Turn Off bx`/`Start Protection`/`Reconnect`/更新 的任何逻辑。
- **绝不擅自启动 bx / 改路由 / 跑 `install.sh`。** 本计划全部改动只写仓库文件,验证只跑编译与单测。
- 验证命令:`go build ./... && go vet ./... && go test ./...`;Swift 侧 `bash scripts/test-macos-menu.sh`;跨平台 `GOOS=linux/windows GOARCH=amd64 go build -o /dev/null ./...`。
- TDD:先写失败测试→跑红→最小实现→跑绿。中文 conventional commits,结尾带 `Co-Authored-By: Claude …`。在默认分支直接提交。

---

### Task 1: 生产路径 plist 补 KeepAlive

**Files:**
- Modify: `internal/install/unified_darwin.go`(`MenuAgentPlistText`,约 :444-476)
- Test: `internal/install/unified_darwin_test.go`(追加,文件已有 `//go:build darwin`)

**Interfaces:**
- Consumes: 无
- Produces: `MenuAgentPlistText(executable, logDir string) string` 的输出新增 `KeepAlive` 字典块;签名不变

**为什么这是最高风险的一步:** `true` 与 `{SuccessfulExit:false}` 的区别不体现在任何编译错误上,写错了只会让 `Quit bx` 静默失效——不专门钉住就发现不了。

- [ ] **Step 1: 写失败测试**

在 `internal/install/unified_darwin_test.go` 末尾追加:

```go
// 菜单栏是普通用户唯一的非命令行控制面,崩溃后必须自己回来。
//
// 但 KeepAlive 只能是 {SuccessfulExit:false} 字典形式,绝不能是 true:
// NSApp.terminate 是干净退出(exit 0),KeepAlive=true 会让 launchd 立刻把菜单
// 拉回来,于是 Quit bx 静默失效——用户点了"退出",菜单眨眼就回来了,而保护
// 已经停了。这个区别不产生任何编译错误,只能靠断言钉住。
func TestMenuAgentPlistTextRestartsOnlyOnAbnormalExit(t *testing.T) {
	text := MenuAgentPlistText(
		"/Applications/Bx.app/Contents/MacOS/BxMenu",
		"/Users/alice/Library/Logs/bx",
	)

	const keepAlive = `  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>`
	if !strings.Contains(text, keepAlive) {
		t.Fatalf("菜单栏 plist 必须带 KeepAlive={SuccessfulExit:false},否则崩溃后要等下次登录才回来。实际:\n%s", text)
	}

	const keepAliveAlways = `  <key>KeepAlive</key>
  <true/>`
	if strings.Contains(text, keepAliveAlways) {
		t.Fatalf("KeepAlive 不得写成 true:那会让 Quit bx 静默失效(exit 0 也重启)。实际:\n%s", text)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/install/ -run TestMenuAgentPlistTextRestartsOnlyOnAbnormalExit -count=1`
Expected: FAIL,提示 plist 里没有 `KeepAlive={SuccessfulExit:false}`

- [ ] **Step 3: 实现**

在 `internal/install/unified_darwin.go` 的 `MenuAgentPlistText` 里,把

```go
  <key>RunAtLoad</key>
  <true/>
  <key>StandardOutPath</key>
```

改成

```go
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>StandardOutPath</key>
```

注意这段在一个 Go 原始字符串字面量(反引号)里,直接改字面量内容即可,不要动
`writeXMLEscaped` 的调用顺序。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/install/ -count=1`
Expected: PASS(全包)

- [ ] **Step 5: 全量验证并提交**

```bash
go build ./... && go vet ./... && go test ./... -count=1
GOOS=linux   GOARCH=amd64 go build -o /dev/null ./...
GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
git add internal/install/unified_darwin.go internal/install/unified_darwin_test.go
git commit -m "fix(install): 菜单栏 LaunchAgent 崩溃自愈,主动退出不拉回"
```

---

### Task 2: 两个脚本的 plist 与生产路径对齐

**Files:**
- Modify: `scripts/install-macos-menu.sh`(`write_launch_agent`,约 :116-140)
- Modify: `scripts/package-macos-menu.sh`(生成 `$LAUNCH_AGENT` 的 heredoc,约 :75-92)

**Interfaces:**
- Consumes: Task 1 确定的 plist 形状
- Produces: 无(脚本产物)

**为什么要做:** 三处生成器不一致会让不同安装路径行为不同——统一安装的用户菜单会自愈,legacy 开发安装的不会,而这种差异只在真机崩溃时才暴露。

- [ ] **Step 1: 改 `scripts/install-macos-menu.sh`**

在 `write_launch_agent()` 的 heredoc 里,把

```
  <key>RunAtLoad</key>
  <true/>
  <key>StandardOutPath</key>
```

改成

```
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>StandardOutPath</key>
```

- [ ] **Step 2: 改 `scripts/package-macos-menu.sh`**

在生成 `$LAUNCH_AGENT` 的 heredoc 里做**完全相同**的替换(该文件里 `RunAtLoad`
只出现一次,就在那个 heredoc 中)。

- [ ] **Step 3: 跑打包脚本,验证产物真的带上了**

```bash
bash scripts/package-macos-menu.sh >/dev/null
grep -A 4 KeepAlive dist/macos/com.getbx.bx.menu.plist
```
Expected: 打印出 `KeepAlive` → `<dict>` → `SuccessfulExit` → `<false/>`

该脚本只写 `dist/`,不碰系统。`install-macos-menu.sh` 无法在不改系统的前提下执行,
其正确性靠与本步产物逐字比对保证。

- [ ] **Step 4: 逐字比对三处一致**

```bash
grep -A 4 '<key>KeepAlive</key>' scripts/install-macos-menu.sh
grep -A 4 '<key>KeepAlive</key>' scripts/package-macos-menu.sh
grep -A 4 '<key>KeepAlive</key>' internal/install/unified_darwin.go
```
Expected: 三处的 `<dict>`/`SuccessfulExit`/`<false/>` 结构完全相同

- [ ] **Step 5: 提交**

```bash
git add scripts/install-macos-menu.sh scripts/package-macos-menu.sh
git commit -m "fix(scripts): 两处菜单栏 plist 生成器与生产路径对齐 KeepAlive"
```

---

### Task 3: 删除 Open Status 整条链路

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`(:73、:395、:441-478)
- Delete: `apps/macos/BxMenu/Sources/BxMenu/StatusPanel.swift`(83 行)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/StatusPresentation.swift`(删 `StatusRow`、`StatusSnapshot`)
- Modify: `apps/macos/BxMenu/Tests/StatusPresentationTests.swift`(删对应断言)

**Interfaces:**
- Consumes: 无
- Produces: 无(纯删除)

**关于测试:** 这是删除,守卫是「现有测试仍全绿 + 仍能编译」。菜单构造在
`main.swift` 里直接依赖 AppKit,当前测试架子(独立 `@main` 可执行文件)覆盖不到,
故不为"菜单里没有 Open Status"新增单测——那需要重构菜单构造,超出本期范围。

- [ ] **Step 1: 先确认现有测试是绿的(改之前的基线)**

Run: `bash scripts/test-macos-menu.sh`
Expected: 全部 passed

- [ ] **Step 2: 从 main.swift 删掉菜单项**

在 `.connected, .warning` 那个 case 里删掉这一行(仅此一处):

```swift
            menu.addAction("Open Status", symbol: "list.bullet.rectangle", target: self, action: #selector(openStatus))
```

保留紧随其后的 `View Logs` 与 `Run Doctor` 两行。

- [ ] **Step 3: 从 main.swift 删掉实现与成员**

删掉 `statusPanel` 成员声明(约 :73):

```swift
    private let statusPanel = StatusPanelController()
```

删掉 `openStatus()` 与 `statusSnapshot()` 两个方法(约 :441-478,从
`@objc private func openStatus() {` 起到 `statusSnapshot()` 的收尾大括号止)。

- [ ] **Step 4: 删掉 StatusPanel.swift**

```bash
git rm apps/macos/BxMenu/Sources/BxMenu/StatusPanel.swift
```

- [ ] **Step 5: 从 StatusPresentation.swift 删掉两个类型**

删掉 `struct StatusRow`(:3 起)与 `struct StatusSnapshot`(:8 起,含其
`protected(...)`/`off()` 静态构造)。

**保留 `struct DNSPresentation`(:30)与 `func dnsPresentation(...)`(:41)**——
一级菜单的 DNS 行在用它们,删了会连累菜单。

- [ ] **Step 6: 从 StatusPresentationTests.swift 删掉对应断言**

删掉 `main()` 开头到 `off rows` 那一段(约 :6-27),即所有 `StatusSnapshot` 与
`StatusRow` 的断言。**保留从 `let managed = dnsPresentation(...)`(约 :29)起的
全部 dnsPresentation 断言,以及文件末尾的 `expect` 辅助函数。**

- [ ] **Step 7: 跑 Swift 测试确认仍绿**

Run: `bash scripts/test-macos-menu.sh`
Expected: 全部 passed —— 其中 `status-presentation` 单元通过即证明
`dnsPresentation` 没被删除连累

- [ ] **Step 8: 确认整个 App 仍能编译**

Run: `bash scripts/package-macos-menu.sh`
Expected: `Build complete!` 且打印出 `Built: …/dist/macos/Bx.app`

该脚本只写 `dist/`,不碰系统。

- [ ] **Step 9: 提交**

```bash
git add -A apps/macos/BxMenu
git commit -m "refactor(menu): 删除与一级菜单重复的 Open Status 面板"
```

---

### Task 4: Quit 确认文案补恢复路径

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/UpdatePresentation.swift`(菜单文案集散地,`quitBxActionTitle` 在 :15)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`(`quitBx()` 里的 `informativeText`)
- Test: `apps/macos/BxMenu/Tests/InstallPresentationTests.swift`(追加)

**Interfaces:**
- Consumes: 无
- Produces: `let quitBxConfirmMessage: String`(定义于 `UpdatePresentation.swift`)

**为什么抽成常量:** 文案现在内联在 `main.swift` 的 `quitBx()` 里,而 `main.swift`
依赖 AppKit、测不到。`install-presentation` 测试单元已经编译
`UpdatePresentation.swift`(见 `scripts/test-macos-menu.sh:37-40`),把常量放那儿
即可被现有单元覆盖,无需新建测试单元。

- [ ] **Step 1: 写失败测试**

在 `apps/macos/BxMenu/Tests/InstallPresentationTests.swift` 的 `main()` 里追加:

```swift
        // Quit 会同时停掉保护并关掉菜单,而菜单是普通用户唯一的非命令行入口。
        // 文案必须告诉他们怎么回来,否则只剩 CLI 这条路——对普通用户等于没有路。
        expect(quitBxConfirmMessage.contains("Bx.app"), "quit confirm names the app to reopen")
        expect(quitBxConfirmMessage.contains("stop protecting"), "quit confirm still states protection stops")
```

- [ ] **Step 2: 跑测试确认失败**

Run: `bash scripts/test-macos-menu.sh`
Expected: FAIL,`error: cannot find 'quitBxConfirmMessage' in scope`

- [ ] **Step 3: 定义常量**

在 `apps/macos/BxMenu/Sources/BxMenu/UpdatePresentation.swift` 里 `quitBxActionTitle`
(:15)那一行下面追加:

```swift
let quitBxConfirmMessage = "bx will stop protecting system traffic, restore managed DNS settings, and close this menu. To start bx again, open Bx.app from Applications."
```

- [ ] **Step 4: 跑测试确认通过**

Run: `bash scripts/test-macos-menu.sh`
Expected: 全部 passed

- [ ] **Step 5: 让 main.swift 用这个常量**

在 `quitBx()` 里,把

```swift
        alert.informativeText = "bx will stop protecting system traffic, restore managed DNS settings, and close this menu."
```

改成

```swift
        alert.informativeText = quitBxConfirmMessage
```

- [ ] **Step 6: 确认整个 App 仍能编译**

Run: `bash scripts/package-macos-menu.sh`
Expected: `Build complete!`

- [ ] **Step 7: 提交**

```bash
git add apps/macos/BxMenu
git commit -m "feat(menu): Quit 确认文案告诉用户怎么把 bx 开回来"
```

---

### Task 5: 文档与全量验证

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: 在 macOS 段落补记**

补一条,写明:

- 菜单栏 LaunchAgent 现为 `KeepAlive={SuccessfulExit:false}`:崩溃/被强退自动回来,
  用户主动 `Quit bx`(exit 0)不拉回。**绝不能写成 `KeepAlive=true`**——`NSApp.terminate`
  是干净退出,那会让 `Quit bx` 静默失效;该区别不产生编译错误,由
  `TestMenuAgentPlistTextRestartsOnlyOnAbnormalExit` 钉住。三处生成器
  (`MenuAgentPlistText`、`install-macos-menu.sh`、`package-macos-menu.sh`)须保持一致。
- `Open Status` 面板已删:它显示的 5 项是一级菜单 7 项的严格子集,多点一次换来信息更少。
- `Quit bx` 与 `Turn Off bx` 都保留,区分是「不用了(停保护+关菜单)」与
  「暂停(停保护,菜单留着显示 Off)」。Quit 确认文案现含 `To start bx again, open
  Bx.app from Applications.`——菜单是普通用户唯一的非命令行入口。
- 观测面板(把 `bx status --json` 的 `observed`/`divergence` 做成「信念 vs 事实」对照)
  **本期未做**,留待真机攒够 divergence 样本后另立一期。

**注明本期全部为 UI 层改动,真机未验。**

- [ ] **Step 2: 全量验证**

```bash
go build ./... && go vet ./... && go test ./... -count=1
GOOS=linux   GOARCH=amd64 go build -o /dev/null ./...
GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
GOOS=darwin  GOARCH=arm64 go build -o /dev/null ./...
bash scripts/test-macos-menu.sh
git diff --check
```
Expected: 全部 rc=0

- [ ] **Step 3: 提交并停在重装之前**

```bash
git add CLAUDE.md
git commit -m "docs: 记录菜单栏常驻与 Open Status 去重"
```

**不要重装、不要跑 `install.sh`、不要重启用户机器上运行中的 bx。** 向用户报告验证
结果,并说明真机验收要点:

```
① 重新打包安装后,菜单栏图标仍在、点开没有 Open Status 一项
② Quit bx → 确认框里能看到 "open Bx.app from Applications";点确认后
   保护停、菜单消失,且**不会**被 launchd 拉回来
③ 从 /Applications 打开 Bx.app → 菜单栏图标回来
④ 用活动监视器强退 BxMenu → 十秒内自动回来(这条验的是 KeepAlive)
```
