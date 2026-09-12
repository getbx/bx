# Servers 窗口重做 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Servers 窗口答得出「我这条隧道现在怎么样」,并补齐今天只有终端能做的三个动词;顺带消灭它现在说的三句假话。

**Architecture:** 当前服务器的纵深数据**零新增发布面** —— 它全部来自每 2 秒已经落进菜单进程的 `/v1/status`,只是把管道接上。线上只改三处,每一处都是因为窗口表达不了一个**已经存在**的区分:探测的第三态、切换的四种结局、实际在跑的是哪一台。三个动词的底层原语(`RemoveServer`/`UpsertServer`/`AddServer` 的 udp 形参)都已存在,缺的是 Guardian 那道门与菜单的入口。判据一律进 `ServersModel.swift` 的纯函数,窗口只摆放。

**Tech Stack:** Go 1.26(`internal/guardian`、`internal/supervisor`、`internal/setup`、`internal/cli`)+ Swift 5.9 AppKit(`apps/macos/BxMenu`)。

**Spec:** `docs/superpowers/specs/2026-09-12-servers-window-design.md`

## Global Constraints

- **绝不 `git stash`;绝不对未提交的工作用 `git checkout <path>`。** 变异还原一律走 scratchpad `cp` + `diff -q`,收尾确认 `git status --porcelain` 为空。
- **绝不启动 bx、绝不改路由或 DNS。** 这是所有者的生产机。只读观测可以。
- Swift **用户可见字符串一律英文**,注释中文。服务端来的字符串**不许直接显示** —— 客户端按机器可读的码映射英文(CJK 守卫扫不到服务端字符串,规则窗口刚踩过)。
- 提交信息中文 conventional commits,**每条**结尾恰好两行:
  ```
  Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01Cyaqxb1Fsv9ixwAVMXybjT
  ```
- 收尾 **前台**跑 `bash scripts/verify.sh` 并等它返回。不许后台、不许挂 monitor、不许轮询。判据是退出码,不是字符串匹配。
- **凭据永不离开 Guardian**(`TestServerListNeverShipsTheLinkItself`)。任何设计都不许把链接原文发给菜单。
- **新字段一律不带 `omitempty`**,除非「缺席」本身就是要表达的意思并写明理由 —— 键缺席读作「这一版 Guardian 没说」。
- 变异验证:每条新守卫都要证明它会咬人,并记录哪一条咬中。**凡变异「全绿」先查落没落上**;`git diff --stat` 在「变异恰好把代码改回 HEAD 的样子」时会假阴性,用对副本的 `diff -q`。

---

## File Structure

**Go(线上与判据)**
- `internal/supervisor/switchserver.go` — 加四个哨兵错误,消息一字不改(`bx server use` 靠它们打实话)。
- `internal/guardian/servers.go` — 探测第三态、切换结局码、发布 Core 报的当前服务器、remove/replace 两个动作。
- `internal/guardian/daemon.go` — 吞吐峰值不再按配置名归属(§6.3 后半)。

**Swift(判据全在纯模型)**
- `apps/macos/BxMenu/Sources/BxMenu/ServersModel.swift` — 五个新纯函数 + 新线上字段的解码。
- `apps/macos/BxMenu/Sources/BxMenu/ServersWindow.swift` — 布局重做,只摆放。
- `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift` — 三个动词的请求。
- `apps/macos/BxMenu/Sources/BxMenu/main.swift` — 把 `CoreRuntime` 接进窗口;三个动词的接线。
- `apps/macos/BxMenu/Tests/ServersModelTests.swift` — 纯模型测试(记得登记进 `scripts/test-macos-menu.sh`,漏登记就一次都不跑而 CI 全绿)。

**守卫**
- `internal/guardian/servers_test.go`、`internal/supervisor/switchserver_test.go`、`internal/cli/macos_menu_servers_test.go`。

---

## Task 1:探测的第三态

**Files:**
- Modify: `internal/guardian/servers.go`(`ProbeReport`,以及两处 `ProbeReport{Error: …}` 的产地)
- Test: `internal/guardian/servers_test.go`

**Interfaces:**
- Produces:`ProbeReport.Measured bool`(json `measured`,**不带 omitempty**)。`Measured=false` ⇒ 这一轮没测成,`Reachable` 无意义。
- Consumes:无。

- [ ] **Step 1:先写失败测试**

```go
// 「没测成」与「测了不通」在线上必须分得开。今天两者都是 Reachable:false,
// 于是菜单把一台好服务器画成红的 —— 而 ServersModel 里那段注释明写不该这样。
func TestProbeDistinguishesNotMeasuredFromUnreachable(t *testing.T) {
	notMeasured := ProbeReport{Measured: false, Error: "core not running"}
	unreachable := ProbeReport{Measured: true, Reachable: false}
	if notMeasured.Reachable == unreachable.Reachable && notMeasured.Measured == unreachable.Measured {
		t.Fatal("两种结局在线上无法区分")
	}
	// measured 缺席读作「这一版 Guardian 没说」,所以它不许带 omitempty。
	b, err := json.Marshal(ProbeReport{Measured: false})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"measured"`) {
		t.Fatalf("measured 带了 omitempty:%s —— 键缺席就与旧 Guardian 无法区分了", b)
	}
}
```

- [ ] **Step 2:跑红**。`go test ./internal/guardian/ -run TestProbeDistinguishes` 应报编译失败(没有 `Measured` 字段)。

- [ ] **Step 3:最小实现**。给 `ProbeReport` 加 `Measured bool \`json:"measured"\``,注释写明「缺席 = 这一版没说,不是没测成」。把 `servers.go` 里那两处 `ProbeReport{Error: …}`(Core 不可达、链接解析不出主机)改成显式 `Measured: false`,**并把那两句文案改成英文** —— 它们会原样出现在全英文菜单里。真正测过的那一处填 `Measured: true`。

- [ ] **Step 4:跑绿 + 补一条现有测试的更正**。`TestProbeFailureIsNotReportedAsUnreachable` 的名字承诺了一个它自己禁止的区分:现在改成断言 `Measured == false`,名字与断言对上。

- [ ] **Step 5:变异**。把新产地的 `Measured` 改回 `true`,确认 Step 1 那条转红;还原,`diff -q` 确认。

- [ ] **Step 6:提交。**

---

## Task 2:切换的四种结局各有各的码

**Files:**
- Modify: `internal/supervisor/switchserver.go`、`internal/guardian/servers.go`
- Test: `internal/supervisor/switchserver_test.go`、`internal/guardian/servers_test.go`

**Interfaces:**
- Produces:`supervisor.ErrSwitchArmFailed` / `ErrSwitchRolledBack` / `ErrSwitchRollbackFailed` / `ErrSwitchCommitFailed` 四个哨兵;Guardian 应答里的 `outcome` 字段取值 `arm_failed` / `rolled_back` / `rollback_failed` / `commit_failed`。
- Consumes:无。

**要害:`bx server use` 今天打的是 `%v` 的完整中文原话,而它比 GUI 诚实。四条消息一个字都不许改**,只在外面包一层哨兵。

- [ ] **Step 1:先写失败测试**

```go
// 四种结局要分得开。今天 Guardian 对四种都回同一个常量,于是菜单对
// 「已生效但确认失败」说「没切过去」(错的),对「回滚也失败了」轻描淡写一次断网。
func TestSwitchServerOutcomesAreDistinguishable(t *testing.T) {
	for _, tc := range []struct {
		name string
		deps SwitchDeps
		want error
	}{
		{"武装失败", SwitchDeps{Arm: failArm, Healthy: neverCalled, Rollback: neverRollback, Commit: neverCommit}, ErrSwitchArmFailed},
		{"不健康已回滚", SwitchDeps{Arm: okArm, Healthy: unhealthy, Rollback: okRollback, Commit: neverCommit}, ErrSwitchRolledBack},
		{"不健康且回滚失败", SwitchDeps{Arm: okArm, Healthy: unhealthy, Rollback: failRollback, Commit: neverCommit}, ErrSwitchRollbackFailed},
		{"已生效但确认失败", SwitchDeps{Arm: okArm, Healthy: healthy, Rollback: neverRollback, Commit: failCommit}, ErrSwitchCommitFailed},
	} {
		err := SwitchServer(tc.deps, "vps", "link", "")
		if !errors.Is(err, tc.want) {
			t.Errorf("%s:errors.Is 认不出 %v,拿到 %v", tc.name, tc.want, err)
		}
	}
}
```

(四个 helper 自己按 `SwitchDeps` 的字段签名写;`neverCalled` 之类在被调用时 `t.Fatal`。)

- [ ] **Step 2:跑红。**

- [ ] **Step 3:实现**。声明四个哨兵,四处 `fmt.Errorf` 各加一个 `%w` 把哨兵包进去,**原有措辞与 `%w` 的错误一并保留**。

- [ ] **Step 4:Guardian 侧映射**。`servers.go` 里那个常量 `servers_hot_switch_failed` 改成按 `errors.Is` 选码填进应答的 `outcome`;**原始错误串仍然只进日志、不外传**(与 `/v1/rules` 的 409 同一条门规)。加一条测试断言四种输入产出四个不同的 `outcome`,且应答体里不含原始错误文本。

- [ ] **Step 5:跑绿 + 变异**。把映射改成恒返回 `arm_failed`,确认转红;还原。

- [ ] **Step 6:提交。**

---

## Task 3:实际在跑的是哪一台,以及吞吐峰值不许张冠李戴

**Files:**
- Modify: `internal/guardian/servers.go`、`internal/guardian/daemon.go`
- Test: `internal/guardian/servers_test.go`

**Interfaces:**
- Produces:`ServerListResponse.Running string`(json `running`,**带 omitempty**,理由:问不出来时**必须缺席**,空串会被读成「没有在跑」)。与既有的 `Current`(配置里的选择)**并列,绝不合并**。
- Consumes:Task 1、2 无。

- [ ] **Step 1:先写失败测试**

```go
// 配置说 B、实际在跑 A,这两者不同**正是最有价值的诊断信号** —— 热切换是
// 先写配置再切,所以切换失败时配置已经是 B 了。合并成一个字段就再也表达不出来。
func TestRunningServerIsPublishedBesideTheConfiguredOne(t *testing.T) {
	// …构造:config.current = "B",而 Core 的 /v0/status 报 server = "A"
	// 断言:resp.Current == "B" && resp.Running == "A"
	// 再构造 Core 不可达:断言 resp.Running == "" 且 json 里该键缺席
}
```

- [ ] **Step 2:跑红。**

- [ ] **Step 3:实现**。`liveThroughput` 已经在问 Core 的 `/v0/status`,那里就有 `Server`;把它带出来填进 `Running`。Core 不可达 ⇒ 留空 ⇒ 键缺席。

- [ ] **Step 4:吞吐归属**。`attachThroughput` / `throughputRecorderFor` 今天把 Core 的实时峰值挂到**配置里**那台头上。改成挂到 **Core 报的那台**头上;两者不同时,当前那台的峰值按「历史」处理(带年龄),不冒充实时。加一条测试:配置 B、实际跑 A 时,B 那一行不得出现一个 age 为 0 的峰值。

- [ ] **Step 5:变异**。让 `Running` 恒等于 `Current`,确认 Step 1 转红;还原。

- [ ] **Step 6:提交。**

---

## Task 4:Guardian 补上 remove 与 replace 两个动作

**Files:**
- Modify: `internal/guardian/servers.go`
- Test: `internal/guardian/servers_test.go`

**Interfaces:**
- Produces:`serversRequest.Action` 新增 `"remove"` 与 `"replace"`。remove 只要 `Name`;replace 要 `Name` + `Link`(+ 可选 `UDP`),走 `setup.UpsertServer`(**它今天零生产调用方,当初就是为这件事写的**)。
- Consumes:无。

- [ ] **Step 1:先写失败测试** —— 三条:
  1. `remove` 删掉一台非当前服务器,盘上配置真的少了那一条;
  2. `remove` **当前那台**被拒(409 + 码 `servers_remove_current`),且**盘上文件一个字节没动**;
  3. `replace` 就地换掉同名那台的链接,`current` 不变,盘上其余内容不动。

- [ ] **Step 2:跑红。**

- [ ] **Step 3:实现**。`switch` 里加两个分支;remove 走 `setup.RemoveServer`,replace 走 `setup.UpsertServer`。**拒绝删当前那台**的判据用配置里的 `current`(删除是配置层操作,与「实际在跑哪台」无关)。

- [ ] **Step 4:跑绿 + 变异**。去掉「拒绝删当前那台」,确认第 2 条转红并且是**盘上文件没动**那半在红;还原。

- [ ] **Step 5:提交。**

---

## Task 5:纯模型

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/ServersModel.swift`
- Test: `apps/macos/BxMenu/Tests/ServersModelTests.swift`

**Interfaces:**
- Consumes:Task 1 的 `measured`、Task 2 的 `outcome`、Task 3 的 `running`。
- Produces(名字是契约,Task 6 按这些名字调):
  - `currentServerPanel(list:core:) -> CurrentServerPanel?`
  - `otherServerRows(list:core:) -> [ServerRow]`
  - `probePresentation(_:) -> ProbePresentation`(`.notChecked` / `.notMeasured(String)` / `.measured(reachable:rttMS:)`)
  - `switchOutcomeMessage(_:) -> String`
  - `serverListEmptyReason(list:) -> String?`

- [ ] **Step 1:先写失败测试**。三态那条**必须分别喂三种输入** —— 只喂「没测过」与「测了不通」正是今天那条测试假绿的原因:

```swift
func testProbeHasThreeStatesNotTwo() {
    expect(probePresentation(nil) == .notChecked, "没测过")
    expect(probePresentation(ProbeReport(measured: false, error: "core not running"))
           == .notMeasured("core not running"), "没测成 —— 绝不许画成不可达")
    expect(probePresentation(ProbeReport(measured: true, reachable: false)) == .measured(reachable: false, rttMS: 0),
           "测了不通")
}

func testCurrentPanelOmitsCoreFieldsWhenCoreIsSilent() {
    let panel = currentServerPanel(list: listWithCurrent(), core: nil)
    expect(panel?.latencyMS == nil, "Core 没答就不许给一个 0 毫秒")
    expect(panel?.tunnelHealthy == nil, "同上")
    expect(panel?.coreSilentNote != nil, "必须明说下面这些没量到")
}

func testEmptyReasonTellsSingleServerConfigApartFromNoServers() {
    expect(serverListEmptyReason(list: singleServerConfig())?.contains("single server") == true)
    expect(serverListEmptyReason(list: emptyServerList())?.contains("No servers yet") == true)
    expect(serverListEmptyReason(list: listWithTwo()) == nil)
}
```

- [ ] **Step 2:跑红**(`bash scripts/test-macos-menu.sh`)。

- [ ] **Step 3:实现**。判据取 `answeringCore()`(`MenuRows.swift`)判 Core 在不在答 —— **不许写第二份**,规则窗口刚因为「有一份判据没被用上」出过同一个 bug。`switchOutcomeMessage` 按 Task 2 的四个码给四句英文,其中两句必须说清要害:已生效但确认失败 ⇒ 它**切过去了**、去让配置落定;回滚失败 ⇒ 隧道现在可能是断的、给逃生命令。认不出的码 ⇒ 一句诚实的兜底,**不许静默消失**。

- [ ] **Step 4:跑绿。别忘了新测试文件要登记进 `scripts/test-macos-menu.sh`** —— 漏登记就一次都不跑而 CI 全绿(实测过)。

- [ ] **Step 5:变异**。让 `probePresentation` 把 `.notMeasured` 折成 `.measured(reachable: false)`,确认转红;还原。

- [ ] **Step 6:提交。**

---

## Task 6:窗口重做与接线

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/ServersWindow.swift`、`main.swift`、`GuardianClient.swift`
- Test: `internal/cli/macos_menu_servers_test.go`

**Interfaces:**
- Consumes:Task 5 的五个纯函数;Task 4 的两个动作。
- Produces:无(终点)。

- [ ] **Step 1:布局**。按 spec §4:当前那台一整块(名字、`host:port`、传输、实时延迟、健康、UDP 传输与档、带年龄的峰值),其余是候选行(名字、`host:port`、探测呈现、`Use`、一个放三个动词的 `⋯`)。配置路径摆右上角。窗口 `.resizable`(会截断而不横向滚动,与 Traffic by App 同一条)。

- [ ] **Step 2:空列表不再是死路**。`rows.isEmpty` 那一支改成用 `serverListEmptyReason`,**并且删掉那个 `return`** —— 按钮带必须照画。那行 `bx setup --name …` 的死提示整个删掉(`bx setup` 没有 `--name` 这个 flag)。顺带把 `internal/cli/servercmd.go:153` 那份同样的死提示也改对。

- [ ] **Step 3:把 CoreRuntime 接进窗口**。`show(...)` / `refreshIfVisible(...)` 加一个 `core:` 形参,`main.swift` 每个调用点传 `maintenanceReport?.core`。**找全所有调用点** —— 规则窗口那次就是漏了一个渲染点。

- [ ] **Step 4:切换要有可见反馈**。`switchInFlight` 传进窗口:切换中那一行显示进行中、`Use` 全部禁用。今天它是 `main.swift` 的私有量,于是确认之后 45 秒内屏幕上什么都不发生、再点一次连对话框都不弹。

- [ ] **Step 5:三个动词**。`⋯` 菜单挂 Remove / Replace Link… / (Add 表单加第二个可选的 UDP 链接框)。Remove **弹确认**并说明链接会丢、bx 手里没有副本(spec §7.1:菜单在构造上做不到 Undo);当前那台的 Remove 置灰。Replace 当前那台 ⇒ 如实说「已写入,重连后生效」并给「现在就重连」,**绝不替他重连**。`GuardianClient` 三个请求一律 `JSONSerialization`,不手拼。

- [ ] **Step 6:守卫**(`internal/cli/macos_menu_servers_test.go`),判据打在**语义位置**上:
  1. 空列表时按钮带**真的被摆进了视图树**(判据是 `addArrangedSubview` 的实参,不是「文件里出现过 Add Server」);
  2. 每个渲染点都把 `core:` 传进去了(漏一个就是那半数据永远不显示而界面看起来正常);
  3. 窗口不自己判 `reachable`,必须走 `answeringCore()`。

- [ ] **Step 7:变异四条**,各确认咬中一条:去掉空列表那支的按钮、某一个渲染点不传 `core:`、窗口内联判 `reachable`、Remove 去掉确认。

- [ ] **Step 8:提交。**

---

## Task 7:记档

**Files:**
- Modify: `CLAUDE.md`(新起一节,≤ 20 行)

- [ ] **Step 1:写记档**。要点:这个窗口此前**没有任何 CLAUDE.md 记录**(整条开发弧只有 Task-6 的刷新说明与一行「仍未验」);三句假话各一句;线上三处改动各自的理由;删除为什么与 Rules 窗口相反(链接是凭据、菜单构造上做不到 Undo);spec §8 那四条所有者定死的边界原样搬过来;真机未验清单(spec §10)。

**只点名真实存在的测试与路径**(`TestEveryTestNameMentionedInProseExists` 与 `TestDocumentedFilePathsExist` 扫 CLAUDE.md,后者现在也扫 Swift)。

- [ ] **Step 2:全量 verify + 提交**

```bash
go test ./internal/cli/ -run 'TestEveryTestNameMentionedInProseExists|TestDocumentedFilePathsExist' 2>&1 | tail -2
bash scripts/verify.sh 2>&1 | tail -3
```

---

## 自审

- **Spec 覆盖**:§4 布局 → Task 6;§4.1 空列表 → Task 6 Step 2;§5 纯函数 → Task 5;§5.1 三态 → Task 1(线上)+ Task 5(呈现)+ Task 6 Step 6.3(守卫);§6.1 → Task 1;§6.2 → Task 2;§6.3 → Task 3;§7.1 删除 → Task 4 + Task 6 Step 5;§7.2 换链接 → Task 4 + Task 6 Step 5;§7.3 UDP → Task 6 Step 5;§8 不做的事 → 计划里没有任何任务碰自动容灾/延迟排序/后台探测/每台独立配置;§9 守卫 → 各任务的 Step;§10 验收 → Task 7。
- **占位符**:无 TBD。Task 3 Step 1 与 Task 4 Step 1 的测试体写的是断言意图而不是完整 fixture —— 那两处要照 `servers_test.go` 既有的 helper 构造,实施时以实际 helper 签名为准(计划里编一个假签名比留一句明确指示更糟)。
- **类型一致性**:`ProbeReport.Measured`(Task 1)→ Swift `ProbeReport.measured`(Task 5)→ `probePresentation`(Task 5)→ 候选行(Task 6);`outcome` 码(Task 2)→ `switchOutcomeMessage`(Task 5);`Running`(Task 3)→ `currentServerPanel`(Task 5)。三条链首尾一致。
- **顺序依赖**:Task 5 消费 1/2/3 的线上字段,Task 6 消费 5 与 4。1→2→3→4 之间无依赖,但都动 `servers.go`,**必须串行**(本仓库明令不许并行派两个会写盘的代理进同一个 checkout)。
- **一处刻意的不对称**:Remove 弹确认而 Rules 窗口的删除不弹。理由写在 spec §7.1 与 Task 6 Step 5,实施时不许「为了一致」把它改掉。
