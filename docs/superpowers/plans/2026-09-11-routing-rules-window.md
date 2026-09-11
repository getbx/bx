# Routing Rules 窗口重做 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 Routing Rules 窗口从「三个预设勾选框 + 手写规则的只读灰字」变成一张能看见问题、删得掉、加得上的规则表;同期堵上「菜单能一键加进一条 CLI 明确拒绝的危险 direct 规则」这个现存缺口。

**Architecture:** 判据全部落在既有的两个纯层:Swift 侧扩 `RulesModel.swift` 里**已经写好却只有测试在调**的 `ruleRows`(它已经合并 direct+proxy、挂失败归因、失败排最前),Go 侧把风险判据收紧成 `internal/policy.DirectRuleHazard` 一份、CLI 与 Guardian 共用。`/v1/rules` 的**读**路径一个字节不动(proxy 规则本来就全在 `Proxy` 里,体检本来就在 `Review` 里,只是菜单没解码);**写**路径加一个 `force` 字段与一个 409。AppKit 那半只摆放。

**Tech Stack:** Go 1.26(`internal/policy` / `internal/guardian` / `internal/cli`)、Swift 5.9 AppKit(SwiftPM;`Tests/*.swift` 是 `scripts/test-macos-menu.sh` 直编直跑的独立 `@main` 套件,必须登记)、Go 读源码守卫(`menuMainSwiftCode` / `readMenuSwiftSource` / `swiftFunctionBody` / `blankSwiftStringLiterals`)。

**Spec:** `docs/superpowers/specs/2026-09-11-routing-rules-window-design.md`

## Global Constraints

- 验证一律 `bash scripts/verify.sh --quick`,最后一个任务跑全量 `bash scripts/verify.sh`。**判据是退出码。**
- `$(go env GOPATH)/bin/gofumpt -l <dir>` 必须无输出(gofumpt 在 GOPATH/bin,不在 PATH)。
- 提交信息中文 conventional commits,结尾**恰好**这两行:
  `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`
  `Claude-Session: https://claude.ai/code/session_01Cyaqxb1Fsv9ixwAVMXybjT`
- **绝不 `git stash`;绝不 `git checkout <path>` 还原未提交的工作。** 变异验证用 `cp` 到 `/private/tmp/claude-501/` 备份再 `cp` 回来。
- **绝不启动 bx、不改路由、不跑菜单 App。** 这台是所有者的生产机。
- Guardian 端点:owner 门 `authorizeOwnerPeer` 不动;失败体只带 code,**完整理由与 pattern 只进 Guardian 日志**;JSON 一律 `writeGuardianJSON`。
- Swift 用户可见字符串**只能是英文**(`TestMacMenuUserFacingStringsAreEnglish`);注释用中文(仓库风格)。`main.swift` 不得新增 `Process` / `Timer.scheduledTimer`。
- 新增 `Tests/*.swift` 必须登记进 `scripts/test-macos-menu.sh`(`TestEveryMacOSMenuTestSuiteIsRegistered` 钉着);每个编 `RulesModel.swift` 的 `run_test` 块都要能编过。
- 既有守卫可重新锚定,**不许改弱**。
- **`review` 为 nil 与空报告必须分开**:nil = 这一版 Guardian 不做体检;空 = 查过了、都健康。

---

## 文件结构

**Task 1** `internal/policy/policy.go`(`DirectRuleHazard`)· `internal/policy/policy_test.go`
**Task 2** `internal/cli/direct.go`(改用共享判据)· `internal/cli/direct_test.go`
**Task 3** `internal/guardian/rules.go`(add 的 hazard 门 + `force`)· `internal/guardian/rules_hazard_test.go`
**Task 4** `apps/macos/BxMenu/Sources/BxMenu/RulesModel.swift`(解码 `review`、扩 `ruleRows`)· `Tests/RulesModelTests.swift`
**Task 5** `GuardianClient.swift`(`force`)· `AppTrafficModel.swift`(右键滤掉 hazard)· `Tests/AppTrafficModelTests.swift` · `internal/cli/macos_menu_hazard_test.go`
**Task 6** `RulesWindow.swift`(整张表 + 删除 + Undo + Add Rule…)· `main.swift`(接线)· `internal/cli/macos_menu_ruleswindow_test.go`
**Task 7** `CLAUDE.md`

---

### Task 1: `policy.DirectRuleHazard` —— 危险的是通配符,不是平台

**Files:**
- Modify: `internal/policy/policy.go`(在 `DirectRisk` 旁边新增;`DirectRisk` **暂不删**,Task 2 删)
- Test: `internal/policy/policy_test.go`

**Interfaces:**
- Consumes: 既有 `riskyDirect *route.DomainSet`、`norm(string) string`。
- Produces:
  ```go
  // DirectRuleHazard 判一条 direct 规则会不会重新打开去匿名化洞。
  func DirectRuleHazard(pattern string) (hazard bool, reason, suggestion string)
  ```
  `reason` / `suggestion` 是**英文**(菜单会原样显示,而菜单只准英文);hazard 为 false 时两者都是空串。

- [ ] **Step 1: 写失败测试**

```go
// internal/policy/policy_test.go 末尾追加
package policy

import "testing"

// **危险的是「在任何人都能注册子域的平台上用通配符」,不是平台本身。**
//
// `*.s3.amazonaws.com`:攻击者注册 evil.s3.amazonaws.com 即命中你的白名单,
// 于是他能把你的真实 IP 钓出来。
// `mybucket.s3.amazonaws.com`:那个确切主机他拿不到,直连只对这一个主机暴露,
// 是一次窄而明确的选择。
//
// 旧的 DirectRisk 两者一律拦 —— 不是因为判据写错,而是因为它判的是「域名落在
// 哪个平台」。过宽的门会把人逼去用 --force,那正是门死掉的方式。
func TestDirectRuleHazardOnlyFlagsWildcardsOnOpenPlatforms(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		hazard  bool
		why     string
	}{
		{"*.s3.amazonaws.com", true, "开放平台 + 通配"},
		{"*.amazonaws.com", true, "开放平台顶级域 + 通配"},
		{"*.OSS-CN-SH.ALIYUNCS.COM", true, "大小写不敏感"},
		{"*.myqcloud.com.", true, "尾点要归一"},
		{"  *.github.io  ", true, "两边空白要去掉"},
		{"mybucket.s3.amazonaws.com", false, "确切主机:攻击者注册不到它"},
		{"amazonaws.com", false, "确切域名,没有通配"},
		{"*.apple.com", false, "品牌自控域,子域拿不到"},
		{"*.qq.com", false, "同上"},
		{"192.0.2.1", false, "IP 字面量与通配无关"},
		{"", false, "空串不判危险,交给既有的模式校验去报错"},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			hazard, reason, suggestion := DirectRuleHazard(tc.pattern)
			if hazard != tc.hazard {
				t.Fatalf("hazard = %t, want %t(%s)", hazard, tc.hazard, tc.why)
			}
			if !tc.hazard {
				if reason != "" || suggestion != "" {
					t.Fatalf("不危险时不该有话说:reason=%q suggestion=%q", reason, suggestion)
				}
				return
			}
			// 说明必须回答两个问题:为什么危险、该怎么改。只说「危险」而不说
			// 怎么办,用户唯一的出路就是 --force,那等于没有这道门。
			if reason == "" || suggestion == "" {
				t.Fatalf("危险时 reason 与 suggestion 都要有:reason=%q suggestion=%q", reason, suggestion)
			}
			for _, w := range []string{"subdomain"} {
				if !containsFold(reason, w) {
					t.Errorf("reason 里没说清风险来自子域可被他人注册:%q", reason)
				}
			}
			if !containsFold(suggestion, "exact") {
				t.Errorf("suggestion 里没给出「改成确切主机」这条路:%q", suggestion)
			}
		})
	}
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && stringsContainsFold(s, sub)
}
```

`stringsContainsFold` 用标准库拼:在同文件顶部加

```go
func stringsContainsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
```

并确保该测试文件 import 了 `strings`。若 `internal/policy/policy_test.go` 不存在就新建,`package policy`。

- [ ] **Step 2: 跑确认红**

Run: `go test ./internal/policy/ -run TestDirectRuleHazard 2>&1 | head -5`
Expected: `undefined: DirectRuleHazard`。

- [ ] **Step 3: 实现**

在 `internal/policy/policy.go` 的 `DirectRisk` 之后加:

```go
// DirectRuleHazard 判一条 direct 规则会不会重新打开去匿名化洞。
//
// **危险的是「在任何人都能注册子域的平台上用通配符」,不是平台本身。**
// `*.s3.amazonaws.com` 危险:攻击者注册 evil.s3.amazonaws.com 就命中你的白名单,
// 你的真实 IP 直连给他。`mybucket.s3.amazonaws.com` 不危险:那个确切主机他拿不到,
// 直连只对这一个主机暴露,是一次窄而明确的选择。
//
// 它取代 DirectRisk 那条更宽的判据(那条把确切主机也一并拦下)。收窄是刻意的:
// 过宽的门会把人逼去用 --force,而一道总被绕过的门等于没有门。
//
// reason/suggestion 是**英文**:CLI 与菜单共用这两句,而菜单的用户可见字符串
// 只准英文(TestMacMenuUserFacingStringsAreEnglish)。
func DirectRuleHazard(pattern string) (hazard bool, reason, suggestion string) {
	p := strings.TrimSuffix(norm(pattern), ".")
	if !strings.HasPrefix(p, "*.") {
		// 没有通配符就没有「邻居」可被注册 —— 确切主机是安全的,即使它落在
		// 那些平台上。
		return false, "", ""
	}
	if !riskyDirect.Match(strings.TrimPrefix(p, "*.")) {
		return false, "", ""
	}
	return true,
		"Anyone can register a subdomain on this platform, so a wildcard rule lets a stranger send your real IP outside the tunnel.",
		"Use the exact host you need instead, for example bucket.s3.amazonaws.com."
}
```

`norm` 已有(小写 + 去空白);尾点要另外去掉,`route.DomainSet.Match` 自己也去尾点,但我们在 `HasPrefix("*.")` 之前就要归一。

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/policy/ 2>&1 | tail -2`
Expected: `ok`。

- [ ] **Step 5: 提交**

```bash
$(go env GOPATH)/bin/gofumpt -l internal/policy/
git add internal/policy/
git commit -m "feat(policy): DirectRuleHazard —— 危险的是开放平台上的通配符,不是平台本身

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Cyaqxb1Fsv9ixwAVMXybjT"
```

---

### Task 2: CLI 改用同一份判据

**Files:**
- Modify: `internal/cli/direct.go`(`directRuleRisk` 改成薄壳或直接用新判据)
- Modify: `internal/cli/direct_test.go`(两条既有测试改成通配 vs 确切两组,**不是删掉**)
- Modify: `internal/policy/policy.go`(删掉 `DirectRisk`,**前提是**全仓只剩这一个调用方)

**Interfaces:**
- Consumes: `policy.DirectRuleHazard`(Task 1)。
- Produces: `bx direct add` 对确切主机不再拦;对通配仍拦并附「改窄」建议。

- [ ] **Step 1: 先查 `DirectRisk` 还有谁在用**

Run: `grep -rn "policy.DirectRisk\|DirectRisk(" internal/ --include=*.go | grep -v _test`
Expected: 只有 `internal/cli/direct.go` 与 `internal/doctor/rulereview.go` 两处(动手前以实际输出为准)。
**`internal/doctor/rulereview.go` 那处不要动** —— 它判的是「这条规则危不危险」(体检的 `ClassRisky`),与「能不能加」不是同一个问题:一条已经在配置里的 `*.aliyuncs.com` 仍然该被体检点名。**所以 `DirectRisk` 留着,不删。** 若 grep 结果与此不符,照实际情况走并在报告里说明。

- [ ] **Step 2: 写失败测试**

把 `internal/cli/direct_test.go` 里既有的两条改写(保留名字,它们被 CLAUDE.md 与 spec 点过名):

```go
// 通配 + 开放平台:仍然拦。
func TestDirectRuleRiskFlagsOpenCloud(t *testing.T) {
	for _, d := range []string{"*.oss-cn-hangzhou.aliyuncs.com", "*.s3.amazonaws.com", "*.github.io"} {
		msg := directRuleRisk(d)
		if msg == "" {
			t.Fatalf("%s 应当被拦下", d)
		}
		// **提示必须给出路**:只说危险不说怎么改,用户唯一的出路是 --force,
		// 那等于没有这道门。
		if !strings.Contains(strings.ToLower(msg), "exact") {
			t.Errorf("%s 的提示没给「改成确切主机」这条路:%s", d, msg)
		}
	}
}

// 品牌自控域、以及**开放平台上的确切主机**:不拦。
//
// 后者是 2026-09-11 的收窄:攻击者注册不到 mybucket.s3.amazonaws.com,
// 所以拦它只是在把人逼去用 --force。
func TestDirectRuleRiskSilentOnBrandDomains(t *testing.T) {
	for _, d := range []string{"*.apple.com", "*.qq.com", "taobao.com", "mybucket.s3.amazonaws.com", "amazonaws.com"} {
		if msg := directRuleRisk(d); msg != "" {
			t.Fatalf("%s 不该被拦:%s", d, msg)
		}
	}
}
```

确保该文件 import `strings`。

- [ ] **Step 3: 跑确认红**

Run: `go test ./internal/cli/ -run TestDirectRuleRisk 2>&1 | grep -E "^(--- |FAIL|ok)"`
Expected: 两条 FAIL(旧实现拦了确切主机、提示里没有 "exact")。

- [ ] **Step 4: 实现**

`internal/cli/direct.go` 的 `directRuleRisk` 改为:

```go
// directRuleRisk 返回把 domain 加进直连白名单的风险提示(空 = 可以加)。
//
// **判据在 internal/policy,一份,Guardian 与 CLI 共用。** 两份判据会让同一个
// 域名在命令行被拒、在菜单里被放行,而用户无从分辨谁对 —— 这个仓库为这个形状
// 栽过。
func directRuleRisk(domain string) string {
	hazard, reason, suggestion := policy.DirectRuleHazard(domain)
	if !hazard {
		return ""
	}
	return "⚠ " + reason + " " + suggestion
}
```

`internal/cli/direct.go` 已 import `policy`(既有 `policy.DirectRisk` 就在这里),不用加 import;若编译报未使用,按编译器提示处理。

- [ ] **Step 5: 跑绿 + verify**

Run: `go test ./internal/cli/ -run TestDirectRuleRisk 2>&1 | tail -2 && bash scripts/verify.sh --quick > /dev/null 2>&1; echo "verify=$?"`
Expected: `ok`,`verify=0`。

- [ ] **Step 6: 提交**

```bash
$(go env GOPATH)/bin/gofumpt -l internal/cli/
git add internal/cli/direct.go internal/cli/direct_test.go
git commit -m "refactor(cli): bx direct add 改用共享的 DirectRuleHazard —— 开放平台上的确切主机不再需要 --force

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Cyaqxb1Fsv9ixwAVMXybjT"
```

---

### Task 3: Guardian 的 add 门 —— 菜单不再能一键加进危险规则

**Files:**
- Modify: `internal/guardian/rules.go`(`rulesRequest` 加 `Force`;`applyRuleChange` 的 add 分支加门)
- Test: `internal/guardian/rules_hazard_test.go`(新)

**Interfaces:**
- Consumes: `policy.DirectRuleHazard`(Task 1)、既有 `setup.AddRule`、`writeGuardianJSON`。
- Produces: `POST /v1/rules {"action":"add","kind":"direct","pattern":…,"force":false}` 命中 hazard ⇒ **409** `{"code":"rules_risky_direct"}` 且**盘上一个字节不动**;`force:true` ⇒ 放行。`kind:"proxy"` 不过这道门(proxy 规则是把流量推进隧道,不是推出去)。

- [ ] **Step 1: 写失败测试**

```go
// internal/guardian/rules_hazard_test.go
package guardian

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// postRule 打一次 /v1/rules,返回状态码、响应体、以及盘上配置的前后字节。
func postRule(t *testing.T, path, body string) (int, string) {
	t.Helper()
	handler := rulesHandler(path, 501, func() error { return nil })
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodPost, "/v1/rules", strings.NewReader(body)), 501, true))
	return w.Code, w.Body.String()
}

// **菜单今天能一键加进一条 CLI 明确拒绝的规则。**
//
// 2026-09-08 上线的「按应用窗口右键 → Always direct」直接打这个端点,而
// applyRuleChange 一处都不查风险:setup.AddRule 只校验类型与模式语法。
// 一个连 x.s3.amazonaws.com 的应用,右键候选里就摆着 *.s3.amazonaws.com。
func TestAddDirectRuleRefusesAWildcardOnAnOpenPlatform(t *testing.T) {
	path := rulesTestConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	code, body := postRule(t, path, `{"action":"add","kind":"direct","pattern":"*.s3.amazonaws.com"}`)
	if code != http.StatusConflict {
		t.Fatalf("状态码 = %d, want 409(%s)", code, body)
	}
	if !strings.Contains(body, "rules_risky_direct") {
		t.Fatalf("失败码 = %s", body)
	}
	// **响应体只带 code。** 完整理由与 pattern 只进 Guardian 日志 —— 与
	// servers_name_exists 同一条纪律。
	if strings.Contains(body, "amazonaws") {
		t.Fatalf("响应体里不该出现 pattern:%s", body)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("被拒之后盘上的配置动了")
	}
}

// force 是逃生口:显式带上就放行。它存在是为了让这道门不必做到永远正确,
// 而不是为了让人顺手点过去 —— 菜单侧把它放在次要动作上。
func TestAddDirectRuleHonoursForce(t *testing.T) {
	path := rulesTestConfig(t)
	code, body := postRule(t, path, `{"action":"add","kind":"direct","pattern":"*.s3.amazonaws.com","force":true}`)
	if code != http.StatusOK {
		t.Fatalf("带 force = %d %s", code, body)
	}
	var list rulesResponse
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("解不出应答:%v", err)
	}
	found := false
	for _, d := range list.Direct {
		if strings.EqualFold(d, "*.s3.amazonaws.com") {
			found = true
		}
	}
	if !found {
		t.Fatalf("force 之后规则没进去:%v", list.Direct)
	}
}

// 确切主机不拦(与 CLI 同一判据),proxy 不走这道门。
func TestAddRuleLetsThroughExactHostsAndProxy(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"确切主机", `{"action":"add","kind":"direct","pattern":"bucket.s3.amazonaws.com"}`},
		{"proxy 不过这道门", `{"action":"add","kind":"proxy","pattern":"*.s3.amazonaws.com"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := postRule(t, rulesTestConfig(t), tc.body)
			if code != http.StatusOK {
				t.Fatalf("= %d %s", code, body)
			}
		})
	}
}
```

**这三样写计划时已逐个核实,照抄即可**:`rulesHandler(configPath string, ownerUID uint32, reload func() error) http.HandlerFunc`(`internal/guardian/rules.go:79`)、`rulesTestConfig(t)`(`internal/guardian/rules_test.go:14`)、`withPeer(r, uid, got)`(`internal/guardian/rules_test.go`)。万一与实际不符,**改测试里的调用,不改生产签名**。

- [ ] **Step 2: 跑确认红**

Run: `go test ./internal/guardian/ -run 'TestAddDirectRule|TestAddRuleLetsThrough' 2>&1 | grep -E "^(--- |FAIL|ok)" | head`
Expected: 前两条 FAIL(今天返回 200 并写进去了)。

- [ ] **Step 3: 实现**

`rulesRequest` 加字段:

```go
	// Force 放行 direct 规则的风险门(见 policy.DirectRuleHazard)。**逃生口,
	// 不是主路**:菜单把它放在次要动作上,右键那条一键路径压根不提供危险候选。
	Force bool `json:"force,omitempty"`
```

`applyRuleChange` 的 `case "add"` 分支,在调 `setup.AddRule` **之前**插:

```go
		if req.Kind == "direct" && !req.Force {
			if hazard, reason, _ := policy.DirectRuleHazard(req.Pattern); hazard {
				// pattern 不进响应体(与 servers_name_exists 同一条);进日志是
				// 可以的 —— 它是用户自己刚输入的域名,不是凭据。
				log.Printf("guardian_rule_add_refused reason=risky_direct pattern=%q why=%q", req.Pattern, reason)
				writeGuardianJSON(w, http.StatusConflict, map[string]string{"code": "rules_risky_direct"})
				return
			}
		}
```

`internal/guardian/rules.go` 需要 import `github.com/getbx/bx/internal/policy`。

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/guardian/ 2>&1 | tail -2`
Expected: `ok`。

- [ ] **Step 5: 变异验证**

把那道门改成 `if false && req.Kind == "direct"`(`cp` 备份到 `/private/tmp/claude-501/`)→ `TestAddDirectRuleRefusesAWildcardOnAnOpenPlatform` 必须红且指出状态码是 200;`cp` 还原。

- [ ] **Step 6: 提交**

```bash
$(go env GOPATH)/bin/gofumpt -l internal/guardian/
git add internal/guardian/
git commit -m "fix(guardian): /v1/rules 加 direct 规则要过风险门 —— 菜单不再能一键加进 CLI 明确拒绝的通配规则

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Cyaqxb1Fsv9ixwAVMXybjT"
```

---

### Task 4: 纯模型 —— 解码体检,接上那张早就写好的表

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/RulesModel.swift`
- Modify: `apps/macos/BxMenu/Tests/RulesModelTests.swift`

**Interfaces:**
- Consumes: 既有 `RuleList`、`RuleRow`、`ruleRows(from:failing:)`、`RuleKind`、`FailingRule`。
- Produces:
  ```swift
  struct RuleFinding: Decodable, Equatable { let kind: String; let rule: String; let cls: String; let summary: String; let coveredBy: String }
  struct RuleReview: Decodable, Equatable { let findings: [RuleFinding] }
  // RuleList 新增:var review: RuleReview?   (键 "review";缺席 = nil)
  // RuleRow 新增:let verdict: RuleFinding?
  func ruleRows(from list: RuleList, failing: [FailingRule], customOnly: Bool) -> [RuleRow]
  func ruleRowSeverity(_ row: RuleRow) -> Int   // 越小越靠前
  ```
  `Class` 的线上取值(Go 侧 `Class.String()`,逐字):`risky_direct`、`shadowed_by_user_rule`、`overridden_by_opposite_kind`、`shadowed_by_builtin_list`、`dead`。

- [ ] **Step 1: 写失败测试**

在 `Tests/RulesModelTests.swift` 里加,并在 `main()` 里调用:

```swift
    // 体检缺席与体检为空是两件事。nil = 这一版 Guardian 不做体检;空 = 查过了、
    // 你的规则都健康。压成同一个东西是这个功能最贵的教训。
    static func testReviewAbsentIsNotTheSameAsEmpty() {
        let absent = try! JSONDecoder().decode(RuleList.self, from: Data(#"{"direct":["*.a.com"]}"#.utf8))
        expect(absent.review == nil, "缺席要解成 nil")
        let empty = try! JSONDecoder().decode(RuleList.self, from: Data(#"{"direct":["*.a.com"],"review":{}}"#.utf8))
        expect(empty.review != nil && empty.review!.findings.isEmpty, "空报告不是 nil")
    }

    // 一行规则带上它的体检结论,而结论要说清「被谁盖住」—— 只说「这条冗余」
    // 而不说被哪一条盖住,用户没法核对,也就没法信。
    static func testRowsCarryTheReviewVerdict() {
        let json = """
        {"direct":["*.apple.com","*.gc.apple.com"],
         "review":{"findings":[{"kind":"direct","rule":"*.gc.apple.com",
         "class":"shadowed_by_user_rule","summary":"covered by a broader rule of yours",
         "covered_by":"*.apple.com"}]}}
        """
        let list = try! JSONDecoder().decode(RuleList.self, from: Data(json.utf8))
        let rows = ruleRows(from: list, failing: [], customOnly: false)
        let shadowed = rows.first { $0.pattern == "*.gc.apple.com" }
        expect(shadowed?.verdict?.cls == "shadowed_by_user_rule", "结论没挂上")
        expect(shadowed?.verdict?.coveredBy == "*.apple.com", "没说被谁盖住")
        expect(rows.first { $0.pattern == "*.apple.com" }?.verdict == nil, "健康的行不该有结论")
    }

    // 表只列不属于任何预设的规则:预设在顶上已经有三个勾选框,把它们的域名再
    // 摊一遍,普通用户第一眼看到的就是四十行域名。
    static func testCustomOnlyDropsPresetDerivedRules() {
        let list = RuleList(direct: ["*.apple.com", "*.mine.com"], proxy: ["*.p.com"], custom: ["*.mine.com"])
        let rows = ruleRows(from: list, failing: [], customOnly: true)
        let patterns = rows.map(\.pattern).sorted()
        expect(patterns == ["*.mine.com", "*.p.com"], "customOnly = \(patterns)")
        // proxy 规则全部是自定义的(预设只定义 direct),所以一条都不能被滤掉。
        expect(rows.contains { $0.kind == .proxy }, "proxy 规则被滤掉了")
    }

    // 排序:有问题的在前。顺序是 危险 → 从没生效 → 被自己更宽的盖住 →
    // 被内建列表覆盖 → 成片失败 → 健康。与 Checks 页同一条纪律。
    static func testProblemsSortAhead() {
        let list = RuleList(
            direct: ["healthy.com", "risky.com", "never.com", "shadow.com"],
            custom: ["healthy.com", "risky.com", "never.com", "shadow.com"]
        )
        let review = RuleReview(findings: [
            RuleFinding(kind: "direct", rule: "shadow.com", cls: "shadowed_by_user_rule", summary: "s", coveredBy: "x"),
            RuleFinding(kind: "direct", rule: "risky.com", cls: "risky_direct", summary: "r", coveredBy: ""),
            RuleFinding(kind: "direct", rule: "never.com", cls: "overridden_by_opposite_kind", summary: "o", coveredBy: "y"),
        ])
        var withReview = list
        withReview.review = review
        let order = ruleRows(from: withReview, failing: [], customOnly: true).map(\.pattern)
        expect(order == ["risky.com", "never.com", "shadow.com", "healthy.com"], "排序 = \(order)")
    }
```

- [ ] **Step 2: 跑确认红**

Run: `bash scripts/test-macos-menu.sh 2>&1 | grep -E "error:|FAIL" | head -5`
Expected: 编译错误(`RuleReview` / `customOnly` / `verdict` 不存在)。

- [ ] **Step 3: 实现**

`RulesModel.swift`:

```swift
/// 体检里的一条结论。**手写解码**:Go 侧 covered_by 是 omitempty,合成解码器
/// 对缺键会抛,而「没有被谁盖住」(危险规则那一类)是正常情形。
struct RuleFinding: Decodable, Equatable {
    let kind: String
    let rule: String
    /// 线上取值(Go 的 Class.String(),逐字):risky_direct / shadowed_by_user_rule /
    /// overridden_by_opposite_kind / shadowed_by_builtin_list / dead。
    /// **认不出的词不许丢掉这一行** —— 新版 Guardian 发来一类旧菜单不认识的结论时,
    /// 这一行仍然要显示,只是排在已知的几类后面。
    let cls: String
    let summary: String
    let coveredBy: String

    enum CodingKeys: String, CodingKey {
        case kind, rule, summary
        case cls = "class"
        case coveredBy = "covered_by"
    }

    init(kind: String, rule: String, cls: String, summary: String, coveredBy: String) {
        self.kind = kind; self.rule = rule; self.cls = cls; self.summary = summary; self.coveredBy = coveredBy
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? ""
        rule = try c.decodeIfPresent(String.self, forKey: .rule) ?? ""
        cls = try c.decodeIfPresent(String.self, forKey: .cls) ?? ""
        summary = try c.decodeIfPresent(String.self, forKey: .summary) ?? ""
        coveredBy = try c.decodeIfPresent(String.self, forKey: .coveredBy) ?? ""
    }
}

struct RuleReview: Decodable, Equatable {
    let findings: [RuleFinding]

    enum CodingKeys: String, CodingKey { case findings }

    init(findings: [RuleFinding]) { self.findings = findings }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        findings = try c.decodeIfPresent([RuleFinding].self, forKey: .findings) ?? []
    }
}
```

`RuleList` 加 `var review: RuleReview?`、`case review` 进 `CodingKeys`、memberwise init 加参数(默认 nil)、手写 `init(from:)` 里 `review = try container.decodeIfPresent(RuleReview.self, forKey: .review)`。

`RuleRow` 加 `let verdict: RuleFinding?`,并把 `detail` 改成先说体检、再说失败:

```swift
    /// 副标题。**一切正常时不说话**:每行都挂一句解释会把真正要紧的那一行淹掉。
    var detail: String? {
        if let verdict {
            if verdict.coveredBy.isEmpty { return verdict.summary }
            return verdict.summary + " ← " + verdict.coveredBy
        }
        guard let failure, failure.attempts > 0 else { return nil }
        let pct = Int((Double(failure.failures) / Double(failure.attempts) * 100).rounded())
        return "\(failure.failures) of \(failure.attempts) connections failed (\(pct)%) — this path is not working"
    }
```

`ruleRows` 扩签名并接上排序:

```swift
/// 越小越靠前。**有问题的在前,健康的一个字不写** —— 与 Checks 页同一条纪律。
/// 认不出的结论排在已知几类之后、健康之前:不丢,也不冒充自己看懂了。
func ruleRowSeverity(_ row: RuleRow) -> Int {
    switch row.verdict?.cls {
    case "risky_direct": return 0
    case "overridden_by_opposite_kind": return 1
    case "shadowed_by_user_rule": return 2
    case "shadowed_by_builtin_list": return 3
    case "dead": return 4
    case .some: return 5
    case nil: break
    }
    if let failure = row.failure, failure.failures > 0 { return 6 }
    return 7
}

func ruleRows(from list: RuleList, failing: [FailingRule], customOnly: Bool) -> [RuleRow] {
    let failureIndex = Dictionary(
        failing.map { ($0.kind.rawValue + "|" + $0.rule.lowercased(), $0) },
        uniquingKeysWith: { first, _ in first }
    )
    let verdictIndex = Dictionary(
        (list.review?.findings ?? []).map { ($0.kind + "|" + $0.rule.lowercased(), $0) },
        uniquingKeysWith: { first, _ in first }
    )
    // 预设只定义 direct 域名,所以**所有 proxy 规则按定义都是自定义的**;
    // customOnly 只对 direct 那一半生效。
    let directPatterns = customOnly ? list.custom : list.direct
    func rows(_ patterns: [String], _ kind: RuleKind) -> [RuleRow] {
        patterns.map { pattern in
            let key = kind.rawValue + "|" + pattern.lowercased()
            return RuleRow(kind: kind, pattern: pattern, failure: failureIndex[key], verdict: verdictIndex[key])
        }
    }
    let all = rows(directPatterns, .direct) + rows(list.proxy, .proxy)
    return all.enumerated().sorted { a, b in
        let sa = ruleRowSeverity(a.element), sb = ruleRowSeverity(b.element)
        if sa != sb { return sa < sb }
        return a.offset < b.offset
    }.map(\.element)
}
```

既有三条 `ruleRows(from:failing:)` 的测试调用点要补上 `customOnly:` 实参(用 `false` 保持原语义)。

- [ ] **Step 4: 跑绿**

Run: `swift build --package-path apps/macos/BxMenu 2>&1 | grep -E "error|Build complete"; bash scripts/test-macos-menu.sh 2>&1 | tail -1`
Expected: `Build complete!`、`macOS menu tests passed`。

- [ ] **Step 5: 提交**

```bash
git add apps/macos/BxMenu/Sources/BxMenu/RulesModel.swift apps/macos/BxMenu/Tests/RulesModelTests.swift
git commit -m "feat(menu): 规则表的纯模型接上体检 —— 一行一条规则、有问题的排最前

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Cyaqxb1Fsv9ixwAVMXybjT"
```

---

### Task 5: 客户端带 force,右键不再提供危险候选

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift`(`.changeRule` 加 `force`)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/AppTrafficModel.swift`(`ruleCandidates` 滤掉 hazard 通配)
- Modify: `apps/macos/BxMenu/Tests/AppTrafficModelTests.swift`
- Create: `internal/cli/macos_menu_hazard_test.go`

**Interfaces:**
- Consumes: Task 3 的 409 `rules_risky_direct`。
- Produces:
  ```swift
  // GuardianEndpoint:case changeRule(action: String, kind: String, pattern: String, force: Bool)
  func changeRule(action: String, kind: RuleKind, pattern: String, force: Bool = false) throws -> RuleList
  /// 与 Go 的 policy.DirectRuleHazard 同判据的**只读**版本,只用来决定右键要不要
  /// 提供某个候选。它不是第二道门 —— 门在 Guardian 那边,这里只是不把危险选项
  /// 摆在一键的位置上。
  func wildcardOnOpenPlatform(_ pattern: String) -> Bool
  let openSubdomainPlatforms: [String]   // 与 internal/policy 的 riskyDirect 逐字相同
  ```

> **这里刻意放了一份清单在 Swift 侧,必须有守卫钉住它与 Go 那份逐字相同。** 理由:右键是一键动作,把危险候选摆上去再靠服务端拒绝,用户体验是「点了没反应」;而让菜单在生成候选时就不生成它,只能在客户端判。**判定权仍在 Guardian**(Task 3 那道门),Swift 这份只决定「要不要摆出来」。

- [ ] **Step 1: 写失败测试(Swift + Go 守卫)**

`Tests/AppTrafficModelTests.swift` 加,并在 `main()` 里调用:

```swift
    // 右键是**一键动作**,没有确认框 —— 那就不该把危险选项摆在一键的位置上。
    // 连公有云的应用,右键里只给确切主机;真要那条通配规则,去 Add Rule… 过门。
    static func testCandidatesDropWildcardsOnOpenPlatforms() {
        let got = ruleCandidates(for: "mybucket.s3.amazonaws.com")
        expect(got == ["mybucket.s3.amazonaws.com"], "开放平台上只留确切主机:\(got)")
        let brand = ruleCandidates(for: "cdn.apple.com")
        expect(brand.contains("*.apple.com"), "品牌自控域仍然给通配候选:\(brand)")
        expect(brand.contains("cdn.apple.com"), "确切主机也要在:\(brand)")
    }
```

`internal/cli/macos_menu_hazard_test.go`:

```go
package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/policy"
)

// **两份清单必须逐字相同。** 判定权在 Guardian(policy.DirectRuleHazard),
// Swift 那份只决定右键要不要把某个候选摆出来;但两份一旦漂开,菜单会把一个
// Guardian 会拒绝的候选摆在一键的位置上,用户点下去只看到一句失败。
func TestOpenPlatformListMatchesPolicy(t *testing.T) {
	src := readMenuSwiftSource(t, "AppTrafficModel.swift")
	block := regexp.MustCompile(`(?s)let openSubdomainPlatforms: \[String\] = \[(.*?)\]`).FindStringSubmatch(src)
	if block == nil {
		t.Fatal("读不出 openSubdomainPlatforms —— 守卫已失效,先修守卫")
	}
	var swift []string
	for _, raw := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(block[1], -1) {
		swift = append(swift, raw[1])
	}
	if len(swift) == 0 {
		t.Fatal("Swift 那份清单是空的")
	}
	for _, host := range swift {
		if hazard, _, _ := policy.DirectRuleHazard("*." + host); !hazard {
			t.Errorf("Swift 列了 %q,而 policy 不认为 *.%s 危险 —— 两份漂开了", host, host)
		}
	}
	// 反向:Go 那份里的每一条都要在 Swift 里。用一个已知成员做自检,防止
	// 正则改错之后这条守卫变成空转。
	if !strings.Contains(block[1], "amazonaws.com") {
		t.Error("Swift 清单里没有 amazonaws.com —— 要么漏了,要么守卫读错了地方")
	}
}
```

- [ ] **Step 2: 跑确认红**

Run: `bash scripts/test-macos-menu.sh 2>&1 | grep -E "FAIL|error:" | head -3; go test ./internal/cli/ -run TestOpenPlatformListMatchesPolicy 2>&1 | grep -E "^(--- |FAIL|ok)"`
Expected: Swift 断言 FAIL(今天会给出 `*.s3.amazonaws.com`),Go 守卫 FAIL(读不出清单)。

- [ ] **Step 3: 实现**

`AppTrafficModel.swift` 加清单与判据,并在 `ruleCandidates` 生成通配候选处过滤:

```swift
/// 任何人都能注册子域的平台。**与 internal/policy 的 riskyDirect 逐字相同**,
/// 由 TestOpenPlatformListMatchesPolicy 钉住。
let openSubdomainPlatforms: [String] = [
    "aliyuncs.com", "myqcloud.com", "bcebos.com", "qiniucdn.com", "qbox.me", "clouddn.com", "upaiyun.com", "myhuaweicloud.com",
    "amazonaws.com", "cloudfront.net", "core.windows.net", "googleapis.com", "r2.dev", "workers.dev", "pages.dev", "github.io", "vercel.app", "netlify.app", "b-cdn.net",
]

/// 这条通配规则会不会落在「任何人都能注册子域」的平台上。
///
/// **它不是第二道门** —— 门在 Guardian(policy.DirectRuleHazard)。这里只决定
/// 右键要不要把某个候选摆出来:右键是一键动作、没有确认框,把危险选项摆上去再
/// 靠服务端拒绝,用户看到的是「点了只弹一句失败」。
func wildcardOnOpenPlatform(_ pattern: String) -> Bool {
    var p = pattern.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    while p.hasSuffix(".") { p.removeLast() }
    guard p.hasPrefix("*.") else { return false }
    let body = String(p.dropFirst(2))
    return openSubdomainPlatforms.contains { body == $0 || body.hasSuffix("." + $0) }
}
```

在 `ruleCandidates(for:)` 的返回前加一句 `.filter { !wildcardOnOpenPlatform($0) }`(确切主机不受影响)。

`GuardianClient.swift`:`case changeRule` 加 `force: Bool`,`guardianRequest` 里的 body 用 `JSONSerialization` 组装并在 `force` 为 true 时带上 `"force": true`;`changeRule(action:kind:pattern:force:)` 的 `force` 默认 `false`,既有调用点不用改。

- [ ] **Step 4: 跑绿 + 变异**

Run: `swift build --package-path apps/macos/BxMenu 2>&1 | grep -E "error|Build complete"; bash scripts/test-macos-menu.sh 2>&1 | tail -1; go test ./internal/cli/ -run TestOpenPlatformListMatchesPolicy 2>&1 | tail -1`
Expected: 三条全绿。变异:从 Swift 清单里删掉 `"github.io"`(`cp` 备份)→ Go 守卫的反向自检不会红(它只查 amazonaws),但 Swift 那条 `testCandidatesDropWildcardsOnOpenPlatforms` 用的是 amazonaws;**再补一条变异**:把 `"amazonaws.com"` 改成 `"amazonaws.example"` → Go 守卫必须红并点名。`cp` 还原。

- [ ] **Step 5: 提交**

```bash
git add apps/macos/BxMenu internal/cli/macos_menu_hazard_test.go
git commit -m "feat(menu): 右键不再提供开放平台上的通配候选,changeRule 支持 force

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Cyaqxb1Fsv9ixwAVMXybjT"
```

---

### Task 6: 窗口本体 —— 表、删除 + Undo、Add Rule…

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/RulesWindow.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`
- Create: `internal/cli/macos_menu_ruleswindow_test.go`

**Interfaces:**
- Consumes: Task 4 的 `ruleRows(from:failing:customOnly:)` / `RuleRow.detail` / `ruleRowSeverity`;Task 5 的 `changeRule(…force:)`。
- Produces:
  ```swift
  // RulesWindowController
  func show(rows: [RuleGroupRow], ruleRows: [RuleRow], configPath: String)
  func refreshIfVisible(rows: [RuleGroupRow], ruleRows: [RuleRow], configPath: String)
  var onRemoveRule: ((RuleKind, String) -> Void)?
  var onUndoRemove: ((RuleKind, String) -> Void)?
  var onAddRule: (() -> Void)?
  func markRemoved(kind: RuleKind, pattern: String)   // 那一行原地变成 Removed · Undo
  // main.swift
  private func removeRuleFromWindow(_ kind: RuleKind, _ pattern: String)
  private func addRuleFromWindow()
  ```

- [ ] **Step 1: 写 Go 接线守卫(红)**

```go
// internal/cli/macos_menu_ruleswindow_test.go
package cli

import (
	"strings"
	"testing"
)

// 这张表的判据全在纯模型里,窗口只摆 —— 窗口自己算一次分类,就没有任何测试
// 盯着它了(这个仓库为「判据落进 AppKit 那半」栽过)。
func TestMacMenuRulesWindowRendersByThePureModel(t *testing.T) {
	window := blankSwiftStringLiterals(stripSwiftComments(readMenuSwiftSource(t, "RulesWindow.swift")))
	for _, want := range []string{"row.detail", "NSButton(title: "} {
		if !strings.Contains(window, want) {
			t.Errorf("规则表缺 %s", want)
		}
	}
	// 窗口不许自己再算一遍排序或分类。
	for _, forbidden := range []string{"ruleRowSeverity(", "sorted(", "\"risky_direct\""} {
		if strings.Contains(window, forbidden) {
			t.Errorf("窗口里出现了 %s —— 判据该在 RulesModel 里", forbidden)
		}
	}
}

// 删除走 Guardian 的 remove,并且**删完那一行不消失**:原地留一句 Removed · Undo。
// 不弹确认框是刻意的(为 11 条冗余点 11 次确认是在惩罚正确的行为),但静默且
// 不可逆地毁掉一条手写规则不行。
func TestMacMenuRuleRemovalIsUndoable(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "private func removeRuleFromWindow(_ kind: RuleKind, _ pattern: String)")
	if !ok {
		t.Fatal("读不出 removeRuleFromWindow 的函数体 —— 守卫已失效,先修守卫")
	}
	for _, want := range []string{"changeRule(action: ", "markRemoved(", "showGuardianFailure(title: "} {
		if !strings.Contains(body, want) {
			t.Errorf("removeRuleFromWindow 缺 %s", want)
		}
	}
	window := blankSwiftStringLiterals(stripSwiftComments(readMenuSwiftSource(t, "RulesWindow.swift")))
	if !strings.Contains(window, "onUndoRemove?(") {
		t.Error("Undo 没有出口")
	}
	if !strings.Contains(code, "controller.onRemoveRule = ") || !strings.Contains(code, "controller.onUndoRemove = ") {
		t.Error("窗口的删除/撤销回调没接到 main.swift")
	}
}

// Add Rule… 收到 409 时**不关 sheet**:把用户输入留着让他改窄,才是这道门的
// 意义。「仍然添加」是次要动作,不是主路。
func TestMacMenuAddRuleKeepsTheSheetOnRefusal(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "private func addRuleFromWindow()")
	if !ok {
		t.Fatal("读不出 addRuleFromWindow 的函数体")
	}
	for _, want := range []string{"rules_risky_direct", "force: true"} {
		if !strings.Contains(body, want) {
			t.Errorf("addRuleFromWindow 缺 %s —— 风险门的重试路径没接上", want)
		}
	}
	if !strings.Contains(code, "controller.onAddRule = ") || !strings.Contains(code, "self?.addRuleFromWindow()") {
		t.Error("Add Rule… 没接到 main.swift")
	}
}
```

Run: `go test ./internal/cli/ -run 'TestMacMenuRulesWindowRendersByThePureModel|TestMacMenuRuleRemovalIsUndoable|TestMacMenuAddRuleKeepsTheSheetOnRefusal' 2>&1 | grep -E "^(--- |FAIL|ok)"`
Expected: 三条 FAIL。

- [ ] **Step 2: 窗口**

`RulesWindow.swift`:`show`/`refreshIfVisible` 多收一个 `ruleRows: [RuleRow]`;`render` 在预设组之后、`Show Config` 之前摆这张表,每行一个横向 `NSStackView`:等宽的 pattern、`row.kind.rawValue` 的类型标、`row.detail`(nil 就不摆那个 label)、以及一个 `Remove` 按钮(`bezelStyle = .rounded`、`controlSize = .small`、`identifier` 存 `kind.rawValue + "|" + pattern` 供回调取值)。底部加 `Add Rule…` 按钮调 `onAddRule?()`。

`markRemoved(kind:pattern:)` 把那一行的内容换成一个 label `Removed <pattern>` 加一个 `Undo` 按钮(调 `onUndoRemove?(kind, pattern)`),**不从栈里移除**;下一次 `render` 才真正消失。

删掉既有那段只读灰字(`custom` 那个 for 循环)与 `show(rows:custom:configPath:)` 的 `custom` 形参。

- [ ] **Step 3: main.swift 接线**

lazy `rulesWindow` 里加三个回调:

```swift
        controller.onRemoveRule = { [weak self] kind, pattern in self?.removeRuleFromWindow(kind, pattern) }
        controller.onUndoRemove = { [weak self] kind, pattern in self?.addRuleBack(kind, pattern) }
        controller.onAddRule = { [weak self] in self?.addRuleFromWindow() }
```

三处 `rulesWindow.show(...)` / `refreshIfVisible(...)` 的实参改成
`ruleRows: ruleRows(from: rules, failing: self.maintenanceReport?.core?.failingRules ?? [], customOnly: true)`,并去掉 `custom:`。

```swift
    /// 删一条规则。**不弹确认框**:方向都是更安全的那一边(删一条 direct 规则 =
    /// 那些流量回到隧道)。但删完那一行不消失,原地留一句 Removed · Undo ——
    /// 一次误点不该静默毁掉一条手写规则。
    private func removeRuleFromWindow(_ kind: RuleKind, _ pattern: String) {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result { try GuardianClient().changeRule(action: "remove", kind: kind, pattern: pattern) }
            DispatchQueue.main.async {
                guard let self else { return }
                switch result {
                case .success(let list):
                    self.lastRules = list
                    self.rulesWindow.markRemoved(kind: kind, pattern: pattern)
                case .failure(let error):
                    self.showGuardianFailure(title: "Could not remove that rule", error: error)
                }
            }
        }
    }

    /// Undo:把刚删掉的那一条原样加回去。带 force —— 它本来就在配置里,
    /// 风险门不该拦一次撤销。
    private func addRuleBack(_ kind: RuleKind, _ pattern: String) {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result { try GuardianClient().changeRule(action: "add", kind: kind, pattern: pattern, force: true) }
            DispatchQueue.main.async {
                guard let self else { return }
                if case .failure(let error) = result {
                    self.showGuardianFailure(title: "Could not restore that rule", error: error)
                }
                self.fetchRulesOnDemand(forceShow: false)
            }
        }
    }
```

`addRuleFromWindow()` 用一个 `NSAlert` 做 sheet:一个 `NSTextField` 输入模式、一个 `NSSegmentedControl` 选 direct/proxy、按钮 `Add` 与 `Cancel`。提交前先跑既有的 `validateRulePattern`,有话说就把它显示出来、不提交。收到 `GuardianClientError.status(409, code: "rules_risky_direct")` 时**不关闭**、把输入留着、显示风险说明(英文,取自 Guardian 返回不了的那句——所以这句文案写在 Swift 侧,与 `policy.DirectRuleHazard` 的 reason/suggestion 保持同义即可,守卫不钉它),并提供 `Add Anyway` 再提交一次、带 `force: true`。

**这里有一处必须照做的细节**:`fetchRulesOnDemand` 的 in-flight 守卫**只压环境刷新那一路**,显式动作永不被拦(2026-08-17 已经付过一次学费)。改完规则后的重拉走 `forceShow: false`。

- [ ] **Step 4: 跑绿 + 变异**

Run: `swift build --package-path apps/macos/BxMenu 2>&1 | grep -E "error|Build complete"; bash scripts/test-macos-menu.sh 2>&1 | tail -1; go test ./internal/cli/ 2>&1 | tail -2`
Expected: 全绿。既有 `TestMacMenuRulesAndServersFetchFailuresOfferShowDetails` 若因签名变化失锚,**改锚点不改判据**。

变异(各自 `cp` 备份、验证后 `cp` 还原):① `markRemoved(` 从 `removeRuleFromWindow` 里删掉 → `TestMacMenuRuleRemovalIsUndoable` 红;② `addRuleFromWindow` 里的 `force: true` 改成 `force: false` → `TestMacMenuAddRuleKeepsTheSheetOnRefusal` 红;③ 窗口里加一句 `let _ = ruleRowSeverity(rows[0])` → `TestMacMenuRulesWindowRendersByThePureModel` 红。

- [ ] **Step 5: 提交**

```bash
bash scripts/verify.sh --quick > /dev/null 2>&1; echo "verify=$?"
git add -A apps/macos/BxMenu internal/cli
git commit -m "feat(menu): Routing Rules 变成规则编辑器 —— 一行一条、有问题的排最前、可删可加

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Cyaqxb1Fsv9ixwAVMXybjT"
```

---

### Task 7: 记档

**Files:**
- Modify: `CLAUDE.md`(在「菜单侧三件用户体验」那一节之后插一节,≤ 18 行)

- [ ] **Step 1: 写记档**

内容要点:窗口从预设开关变成规则编辑器(一行一条、direct+proxy、有问题的排最前、健康的不说话、可删可加);`ruleRows` 这份纯模型**早就存在、只有测试在调**,本期是接上而不是新造;删除不弹确认但留 Undo(为 11 条冗余点 11 次确认是在惩罚正确的行为);**风险门**:`policy.DirectRuleHazard` 一份、CLI 与 Guardian 共用,危险的是开放平台上的**通配符**而不是平台本身,确切主机不再需要 `--force`;Guardian 的 add 此前一处都不查,右键能一键加进 CLI 拒绝的规则,本期堵上;右键候选直接滤掉危险的那个(一键动作不该摆危险选项),Swift 侧那份平台清单由 `TestOpenPlatformListMatchesPolicy` 钉住与 Go 逐字相同;真机未验清单。

**只点名真实存在的测试与路径**(`TestEveryTestNameMentionedInProseExists` 与 `TestDocumentedFilePathsExist` 扫 CLAUDE.md)。

- [ ] **Step 2: 全量 verify + 提交**

```bash
go test ./internal/cli/ -run 'TestEveryTestNameMentionedInProseExists|TestDocumentedFilePathsExist' 2>&1 | tail -2
bash scripts/verify.sh 2>&1 | tail -3
git add CLAUDE.md
git commit -m "docs: 记档 Routing Rules 窗口重做与加规则的风险门

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Cyaqxb1Fsv9ixwAVMXybjT"
```

---

## 自审

- **Spec 覆盖**:§3 布局 → Task 4(customOnly、排序)+ Task 6(表、Add Rule…、Show Config);§4 判据在纯模型 → Task 4;§5 删除与 Undo、添加 → Task 6;§6.1 缺口 → Task 3;§6.2 判据收紧 → Task 1;§6.3 三处 → Task 3(Guardian)+ Task 5(右键 + force)+ Task 2(CLI);§7 刷新模型 → Task 6 Step 3 的 `forceShow: false` 与那条「显式永不被拦」的提醒;§8 不做的事 → 计划里没有任何任务扩 `/v1/rules` 读路径、没有展开预设组;§9 守卫 → Task 1/3/5/6 各自的测试与变异;§10 验收 → 记在 CLAUDE.md(Task 7)。
- **占位符**:无 TBD/TODO。两处标了「以实际输出为准」:Task 2 Step 1 的 `DirectRisk` 调用方普查(写计划时查到的是 cli 与 doctor 两处,**doctor 那处不动**)、Task 6 Step 4 既有守卫的失锚——都是执行时一眼能定的事实,且各自写明了「不符时改测试不改生产」。Task 3 的三个签名已逐个核实并写进计划。
- **类型一致性**:`ruleRows(from:failing:customOnly:)` 在 Task 4 定义、Task 6 调用一致;`RuleRow.verdict: RuleFinding?` 与 `RuleFinding.cls` 的线上取值(`risky_direct` 等)在 Task 4 定义、`ruleRowSeverity` 与 Task 6 的禁用清单里一致;`changeRule(action:kind:pattern:force:)` 在 Task 5 定义、Task 6 的两个调用点一致;Go 侧 `DirectRuleHazard(pattern) (bool, string, string)` 在 Task 1 定义,Task 2/3/5 三处消费一致。
- **一处刻意的重复**:`openSubdomainPlatforms` 在 Swift 里存了第二份。它只决定「右键摆不摆这个候选」,判定权仍在 Guardian;`TestOpenPlatformListMatchesPolicy` 用 Go 那份逐条验 Swift 那份,漂移当场红。
