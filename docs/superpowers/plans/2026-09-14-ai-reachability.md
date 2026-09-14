# AI 站可达性检查(leakcheck 第四段)实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 `bx leakcheck` 加第四段 `SectionReach`,回答「这条路能不能到达 AI 站」,判据全部来自 bx 自己的观测,不调任何第三方。

**Architecture:** 纯判据 `JudgeReach(status, body, dialErr) ReachState` 住 `internal/leakcheck`(现有 `purity_test.go` 自动罩住);探测的 I/O 住 `internal/leakserve`;两条路径(当前路径 / 绑物理网卡)共用一个拨号器接口,物理接口名取自 leakcheck **已经读到**的路由表,不读 config。

**Tech Stack:** Go 1.26,`net/http` + 自定义 `DialContext`,darwin 的 `IP_BOUND_IF`(syscall)。

**Spec:** `docs/superpowers/specs/2026-09-14-ai-reachability-design.md`

## Global Constraints

- **`bx leakcheck` 拒绝 root、不读 config、不需要 Guardian、不需要 `bx setup`** —— 本计划任何一步都不许破坏它(spec §1.2)
- **`internal/leakcheck` 不做 I/O**:不 import net/os/exec/syscall(`purity_test.go` 按前缀禁,例外明写)
- **四态零值必须是 `ReachUndetermined`**(spec §3.1)
- **措辞**:可达 ⇒「bx 能到达 X」,**绝不是**「你可以用 X」;不可达 ⇒ 只说 bx 观测到什么,**不断言对方服务的状态**(spec §3.4)
- **fixture 必须来自真机**:用 spec §2 那张表里的真实 body 片段,不用合成数据
- 中文 conventional commits,结尾带 `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`
- 每个任务结束前跑 `bash scripts/verify.sh --quick`;全部做完跑一次全量

---

### Task 1: `SectionReach` 与 `NewReport` 的计数分支

**这个任务必须排第一,因为现有代码里有一个会静默吃掉新 Section 的陷阱。**
`NewReport` 的分支是 `if Section == SectionIdentity { identity++ } else { anomalies++ }`
—— **任何新 Section 的 Bad 都会掉进 `anomalies`**,即「Claude 连不上」被算成一次
泄漏(spec §6.1 要避免的正是这件事),而编译与现有测试都不会红。

**Files:**
- Modify: `internal/leakcheck/verdict.go`(`Section` 常量、`String()`、`Report`、`NewReport`)
- Test: `internal/leakcheck/verdict_test.go`

**Interfaces:**
- Produces: `SectionReach Section`、`Report.Reach ReachSummary`、
  `ReachSummary{Reachable, Refused, Undetermined, Unreachable int}`、
  `NewReport(now, endpoints, findings, evidence)` 签名**不变**

- [ ] **Step 1: 写失败测试**

```go
// internal/leakcheck/verdict_test.go

// 可达性的坏消息是「你用不了」,而 path/identity 的坏消息是「你泄漏了」——
// 后者是安全问题,前者不是。把它算进 AnomalyCount 就是让一次连不上
// 稀释掉真正的泄漏告警(spec §6.1)。
//
// **这条守卫钉的是 NewReport 里那个 else 分支**:它今天把所有非 identity 的
// Bad 都算进 anomalies,加新 Section 而不改它,编译和现有测试都不会红。
func TestReachSectionNeverCountsAsALeak(t *testing.T) {
	findings := []Finding{
		{ID: "x", Section: SectionPath, Verdict: Bad},
		{ID: "y", Section: SectionReach, Verdict: Bad, Reach: ReachUnreachable},
		{ID: "z", Section: SectionReach, Verdict: Bad, Reach: ReachRefused},
	}
	got := NewReport(time.Now(), EndpointDisclosure{}, findings, nil)
	if got.AnomalyCount != 1 {
		t.Fatalf("AnomalyCount = %d, want 1 —— 可达性的 Bad 不许并进流量泄漏那个数", got.AnomalyCount)
	}
	if got.IdentityCount != 0 {
		t.Fatalf("IdentityCount = %d, want 0", got.IdentityCount)
	}
	if got.Reach.Unreachable != 1 || got.Reach.Refused != 1 {
		t.Fatalf("Reach = %+v, want Unreachable=1 Refused=1", got.Reach)
	}
}

// 四态各自计数,绝不合成 —— 「一条都没查出来」与「查了、全可达」在屏幕上
// 必须长得不一样(spec §6.2)。
func TestReachSummaryCountsAllFourStatesSeparately(t *testing.T) {
	findings := []Finding{
		{ID: "a", Section: SectionReach, Verdict: OK, Reach: ReachReachable},
		{ID: "b", Section: SectionReach, Verdict: OK, Reach: ReachReachable},
		{ID: "c", Section: SectionReach, Verdict: NotChecked, Reach: ReachUndetermined},
		{ID: "d", Section: SectionReach, Verdict: Bad, Reach: ReachUnreachable},
	}
	got := NewReport(time.Now(), EndpointDisclosure{}, findings, nil).Reach
	want := ReachSummary{Reachable: 2, Undetermined: 1, Unreachable: 1}
	if got != want {
		t.Fatalf("Reach = %+v, want %+v", got, want)
	}
}

// 非 reach 段的 Finding 带着 Reach 零值,不许被算进任何一格 ——
// ReachUndetermined 恰好是零值,所以「只对 SectionReach 读这个字段」是承重的。
func TestNonReachFindingsNeverTouchTheReachSummary(t *testing.T) {
	findings := []Finding{
		{ID: "p", Section: SectionPath, Verdict: OK},
		{ID: "i", Section: SectionIdentity, Verdict: Bad},
		{ID: "s", Section: SectionSurface, Verdict: Info},
	}
	if got := NewReport(time.Now(), EndpointDisclosure{}, findings, nil).Reach; got != (ReachSummary{}) {
		t.Fatalf("Reach = %+v, want 全零 —— 非 reach 段的零值 Reach 字段不许被计数", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakcheck/ -run 'TestReachSection|TestReachSummary|TestNonReachFindings' 2>&1 | tail -5`
Expected: 编译失败 —— `undefined: SectionReach` / `undefined: ReachUnreachable` / `Finding has no field Reach`

- [ ] **Step 3: 最小实现**

在 `internal/leakcheck/verdict.go` 的 `Section` 常量块末尾追加:

```go
	// SectionReach:这条路能不能到达目标站。**它不是安全问题** ——
	// path/identity 的坏消息是「你泄漏了」,这一段的坏消息是「你用不了」。
	// 两者合成一个数就是让一次连不上稀释掉真正的泄漏告警(spec §6.1)。
	SectionReach
)
```

`String()` 加一支:

```go
	case SectionReach:
		return "reach"
```

新增类型(同文件):

```go
// ReachState 是一次可达性探测的四态。**零值必须是 ReachUndetermined。**
//
// 与 Verdict 分开是因为它们回答不同的问题:Verdict 是给界面的**极性**(好/坏/没查),
// ReachState 是**发生了什么**(到了/被拒/没问出来/到不了)。Refused 与 Unreachable
// 都映射成 Bad,但给用户的话完全不同 —— 一个是「换服务器」,一个是「这条路不通」。
type ReachState uint8

const (
	// ReachUndetermined:没问出来。CF 人机挑战、认不出的状态码,全在这一格。
	// **绝不因为「没看到拒绝」就升格成可达。**
	ReachUndetermined ReachState = iota
	ReachReachable
	ReachRefused
	ReachUnreachable
)

func (r ReachState) String() string {
	switch r {
	case ReachReachable:
		return "reachable"
	case ReachRefused:
		return "refused"
	case ReachUnreachable:
		return "unreachable"
	default:
		return "undetermined"
	}
}

func (r ReachState) MarshalJSON() ([]byte, error) {
	return []byte(`"` + r.String() + `"`), nil
}

// ReachSummary 是第四段的计数。**四态各自一个数,绝不合成** ——
// 合成之后「一条都没查出来」与「查了、全可达」在屏幕上就一样了。
type ReachSummary struct {
	Reachable    int `json:"reachable"`
	Refused      int `json:"refused"`
	Undetermined int `json:"undetermined"`
	Unreachable  int `json:"unreachable"`
}
```

`Finding` 加字段:

```go
	// Reach 只在 Section == SectionReach 时有意义。**其余段一律忽略它** ——
	// ReachUndetermined 恰好是零值,不加这道门就会把每条 path 结论都算成
	// 「一次没问出来的可达性探测」。
	Reach ReachState `json:"reach,omitempty"`
```

`Report` 加字段:

```go
	// Reach 是第四段的四态计数。**与上面两个数并排,永不合并。**
	Reach ReachSummary `json:"reach"`
```

`NewReport` 的循环改成:

```go
	anomalies, identity := 0, 0
	var reach ReachSummary
	for _, f := range findings {
		// **可达性先分流,且不看 Verdict** —— 它的四态自己就带极性,
		// 而把它塞进下面那个 else 分支正是这个任务要堵的洞。
		if f.Section == SectionReach {
			switch f.Reach {
			case ReachReachable:
				reach.Reachable++
			case ReachRefused:
				reach.Refused++
			case ReachUnreachable:
				reach.Unreachable++
			default:
				reach.Undetermined++
			}
			continue
		}
		if f.Verdict != Bad {
			continue
		}
		if f.Section == SectionIdentity {
			identity++
		} else {
			anomalies++
		}
	}
```

并把 `reach` 填进返回的 `Report`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakcheck/ 2>&1 | tail -3`
Expected: `ok` —— 三条新测试全过,**且既有测试一条不红**(`AnomalyCount` 的含义没改)

- [ ] **Step 5: 变异验证**

把 `if f.Section == SectionReach { … continue }` 整块删掉,重跑
`go test ./internal/leakcheck/ -run TestReachSectionNeverCountsAsALeak`。
Expected: **FAIL,`AnomalyCount = 3, want 1`**。确认后用 `cp` 从 scratchpad 备份还原
(**不许用任何会写工作树的 git 操作**)。

- [ ] **Step 6: 提交**

```bash
git add internal/leakcheck/verdict.go internal/leakcheck/verdict_test.go
git commit -m "feat(leakcheck): 第四段 SectionReach 与它自己的四态计数

NewReport 原来的分支是「identity 归 identity,其余全归 anomalies」,
于是任何新 Section 的 Bad 都会被静默算成一次流量泄漏 —— 而可达性的坏消息
是「你用不了」,不是安全问题(spec §6.1)。变异验证:删掉新加的分流即
AnomalyCount = 3 而不是 1。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: 纯判据 `JudgeReach`

**Files:**
- Create: `internal/leakcheck/judge_reach.go`
- Test: `internal/leakcheck/judge_reach_test.go`

**Interfaces:**
- Consumes: `ReachState` 四态(Task 1)
- Produces: `func JudgeReach(status int, body []byte, dialErr error) ReachState`

- [ ] **Step 1: 写失败测试(fixture 全部来自 spec §2 的真机实测)**

```go
// internal/leakcheck/judge_reach_test.go
package leakcheck

import (
	"errors"
	"testing"
)

// fixture 全部来自 2026-09-13 那轮真机实测(spec §2)。
// **合成数据造不出这里最要紧的那个形状**:同一个 403 之下,
// 「服务 JSON」与「CF 挑战页」是相反的两件事。
const (
	bodyAnthropic405 = `{
  "type": "error",
  "error": {
    "type": "invalid_request_error",
    "message": "Method Not Allowed"
  }
}`
	bodyOpenAI401 = `{
  "error": {
    "message": "Missing bearer authentication in header",
    "type": "invalid_request_error"
  }
}`
	bodyGoogle403 = `{
  "error": {
    "code": 403,
    "message": "Method doesn't allow unregistered callers"
  }
}`
	bodyCloudflareChallenge = `<!DOCTYPE html><html><head><title>Just a moment...</title>` +
		`<meta http-equiv="content-security-policy" content="default-src 'none'; ` +
		`script-src 'nonce-x' 'unsafe-eval' https://challenges.cloudflare.com">`
	// 构造的,不是实测 —— 这台机器的出口在支持区,没见过真的地区拒绝(spec §3.3)。
	// **真机见到之后回来把真实 body 换进这里。**
	bodyRegionRefused = `{"error":{"type":"permission_error","message":"Service not available in your region"}}`
)

func TestJudgeReachOnRealMachineFixtures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		err    error
		want   ReachState
	}{
		{"anthropic 405 服务JSON", 405, bodyAnthropic405, nil, ReachReachable},
		{"openai 401 服务JSON", 401, bodyOpenAI401, nil, ReachReachable},
		{"google 403 也是服务JSON", 403, bodyGoogle403, nil, ReachReachable},
		{"favicon 404 空body", 404, "", nil, ReachReachable},
		{"favicon 200 二进制", 200, "\x00\x00\x01\x00", nil, ReachReachable},
		{"claude.ai 首页是CF挑战", 403, bodyCloudflareChallenge, nil, ReachUndetermined},
		{"地区拒绝", 403, bodyRegionRefused, nil, ReachRefused},
		{"拨不通", 0, "", errors.New("dial tcp: i/o timeout"), ReachUnreachable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := JudgeReach(tc.status, []byte(tc.body), tc.err); got != tc.want {
				t.Fatalf("JudgeReach = %v, want %v", got, tc.want)
			}
		})
	}
}

// **同一个 403,相反的两件事** —— 这是整个判据的形状,单独钉一条。
// 只看状态码的实现会让这两个必然相等。
func TestSame403MeansOppositeThings(t *testing.T) {
	api := JudgeReach(403, []byte(bodyGoogle403), nil)
	challenge := JudgeReach(403, []byte(bodyCloudflareChallenge), nil)
	if api == challenge {
		t.Fatalf("两个 403 判成了同一个 %v —— 判据一定只看了状态码", api)
	}
	if api != ReachReachable || challenge != ReachUndetermined {
		t.Fatalf("api=%v challenge=%v, want reachable/undetermined", api, challenge)
	}
}

// 认不出的东西一律「没问出来」,绝不升格成可达(spec §3.1 零值纪律)。
func TestUnrecognisedResponsesAreUndeterminedNotReachable(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{418, "teapot"},
		{502, "<html>bad gateway</html>"},
		{503, ""},
	} {
		if got := JudgeReach(tc.status, []byte(tc.body), nil); got == ReachReachable {
			t.Fatalf("status=%d body=%q 判成了 reachable —— 认不出就该是 undetermined", tc.status, tc.body)
		}
	}
}

// 拨号错误压过一切:拿到 dialErr 就不许再去看 status/body。
func TestDialErrorWinsOverEverything(t *testing.T) {
	if got := JudgeReach(200, []byte(bodyAnthropic405), errors.New("no route to host")); got != ReachUnreachable {
		t.Fatalf("JudgeReach = %v, want unreachable —— 有拨号错误时不该再看响应", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakcheck/ -run TestJudgeReach 2>&1 | tail -3`
Expected: 编译失败 `undefined: JudgeReach`

- [ ] **Step 3: 最小实现**

```go
// internal/leakcheck/judge_reach.go
package leakcheck

import "bytes"

// cfChallengeMarkers 是 Cloudflare 人机挑战页的特征。
//
// **这两个串是 2026-09-13 从真机上 `claude.ai` 的 403 body 里取的**,不是猜的。
// 带浏览器 UA 重试仍然是同一页 —— 它不是 UA 检测,是 JS 挑战或 TLS 指纹,
// 命令行过不去。把它判成「不可达」会把一台**完全正常的机器**说成用不了。
var cfChallengeMarkers = [][]byte{
	[]byte("challenges.cloudflare.com"),
	[]byte("Just a moment"),
}

// regionRefusalMarkers 是服务自己说「你这个地区不行」的特征。
//
// **今天没有真机样本**(spec §3.3):这台机器的出口在支持区。它仍然要实现 ——
// 不实现的话地区拒绝会落进 undetermined,而那恰恰是我们最想答对的一种。
// 真机见到之后回来把真实串补进来。
var regionRefusalMarkers = [][]byte{
	[]byte("not available in your region"),
	[]byte("unsupported_country"),
	[]byte("country_not_supported"),
}

// JudgeReach 把一次探测的结果判成四态。**判据同时看状态码与 body。**
//
// 只看状态码会判反:`generativelanguage.googleapis.com` 的 403 是 API 在正常
// 应答,`chatgpt.com` 的 403 是人机挑战 —— 同一个码,相反的两件事(spec §2.3)。
//
// 判据按**先后顺序**取,前一条命中就不再往下看。
func JudgeReach(status int, body []byte, dialErr error) ReachState {
	// ① 连都没连上 —— 这时 status/body 没有意义。
	if dialErr != nil {
		return ReachUnreachable
	}
	head := body
	if len(head) > 4096 {
		head = head[:4096]
	}
	// ② 服务明说地区不行。**排在 CF 之前** —— 地区拒绝也可能由 CF 边缘发出,
	//    而「被拒」比「没问出来」信息量大得多。
	for _, m := range regionRefusalMarkers {
		if bytes.Contains(head, m) {
			return ReachRefused
		}
	}
	// ③ CF 人机挑战。
	for _, m := range cfChallengeMarkers {
		if bytes.Contains(head, m) {
			return ReachUndetermined
		}
	}
	// ④ 服务自己的 JSON 在说话 —— 最强的可达证据,与状态码无关。
	if looksLikeServiceJSON(head) {
		return ReachReachable
	}
	// ⑤ 没有 body 的 2xx/404:服务器应答了。
	//    404 是「没这个文件」,那也是应答。
	if len(bytes.TrimSpace(head)) == 0 && (status == 404 || (status >= 200 && status < 300)) {
		return ReachReachable
	}
	// ⑥ 2xx 且不是挑战页:真的拿到了东西(favicon 走这一支)。
	if status >= 200 && status < 300 {
		return ReachReachable
	}
	// ⑦ 4xx/5xx 且 body 是 HTML 而非 JSON:多半是某种拦截页,但我们认不出。
	//    **认不出就是认不出**,不猜。
	return ReachUndetermined
}

// looksLikeServiceJSON 判断 body 是不是服务自己返回的 JSON 错误。
// 判据刻意窄:去掉前导空白后以 `{` 开头,且含 `"error"` 或 `"type"`。
func looksLikeServiceJSON(head []byte) bool {
	t := bytes.TrimLeft(head, " \t\r\n")
	if len(t) == 0 || t[0] != '{' {
		return false
	}
	return bytes.Contains(t, []byte(`"error"`)) || bytes.Contains(t, []byte(`"type"`))
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakcheck/ 2>&1 | tail -3`
Expected: `ok`

- [ ] **Step 5: 变异验证(两条,各咬一个方向)**

1. 把 `JudgeReach` 改成只看状态码 —— **范围必须覆盖 403**,否则变异咬不到那两个
   决定性 fixture:`if status >= 200 && status < 500 { return ReachReachable }` 放在最前
   → `TestSame403MeansOppositeThings` 必须红。
   **`status < 400` 是错的写法(2026-09-14 实测):403 不在 [200,400) 里,变异根本没落上,
   于是「测试没转红」看起来像守卫失效,其实是变异失效** —— 本仓库那条
   「凡变异全绿先查落没落上」说的就是这个。
2. 把第 ⑦ 支的 `return ReachUndetermined` 改成 `return ReachReachable`
   → `TestUnrecognisedResponsesAreUndeterminedNotReachable` 必须红

每次验证后用 `cp` 从 scratchpad 备份还原。

- [ ] **Step 6: 提交**

```bash
git add internal/leakcheck/judge_reach.go internal/leakcheck/judge_reach_test.go
git commit -m "feat(leakcheck): 可达性四态判据 —— 同时看状态码与 body

只看状态码会判反:googleapis 的 403 是 API 在正常应答,chatgpt 的 403 是
人机挑战。fixture 全部来自 2026-09-13 真机实测,合成数据造不出「同一个 403
之下两件相反的事」这个形状。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: 端点清单与它的守卫

**Files:**
- Modify: `internal/leakcheck/endpoints.go`
- Test: `internal/leakcheck/endpoints_test.go`

**Interfaces:**
- Produces: `ReachTargets() []ReachTarget`、
  `ReachTarget{ID, Title, URL, ExpectedSignal string}`

- [ ] **Step 1: 写失败测试**

```go
// 追加到 internal/leakcheck/endpoints_test.go

// 端点是用户可见契约(页面联网前原样显示),逐个钉死。
func TestReachTargetsArePinned(t *testing.T) {
	want := []string{
		"https://api.anthropic.com/v1/messages",
		"https://claude.ai/favicon.ico",
		"https://api.openai.com/v1/models",
		"https://generativelanguage.googleapis.com/v1beta/models",
	}
	got := ReachTargets()
	if len(got) != len(want) {
		t.Fatalf("端点数 = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].URL != w {
			t.Fatalf("第 %d 个 = %q, want %q", i, got[i].URL, w)
		}
	}
}

// 明文回声在路上可被改写,判据就整个失效。
func TestReachTargetsAreAllHTTPS(t *testing.T) {
	for _, tgt := range ReachTargets() {
		if !strings.HasPrefix(tgt.URL, "https://") {
			t.Fatalf("%s 不是 https —— %q", tgt.ID, tgt.URL)
		}
	}
}

// 每个端点连同「上次实测见到什么」一起记档。行为变了守卫会红一次,
// 那正是回来重测的时刻(spec §4.3)。
func TestEveryReachTargetRecordsWhatItLastReturned(t *testing.T) {
	for _, tgt := range ReachTargets() {
		if strings.TrimSpace(tgt.ExpectedSignal) == "" {
			t.Fatalf("%s 没有记录预期信号 —— 改端点的人无从判断它的行为变没变", tgt.ID)
		}
	}
}

// 第三关(spec §4.2):不是禁止端点落在内建 china 直连列表里,而是**如实报告**
// 它在不在 —— 可达性探测走哪条路由由本功能自己指定,不依赖分流,所以「在列表里」
// 不是缺陷,是这次探测经了哪条路的解释。
//
// **判据走生产那份 DomainSet,不在测试里重算一遍。**
func TestReachTargetsDeclareWhetherTheyAreOnTheChinaDirectList(t *testing.T) {
	for _, tgt := range ReachTargets() {
		if tgt.OnChinaDirectList == nil {
			t.Fatalf("%s 没有声明它在不在内建直连列表 —— 那是「这次探测经了哪条路」"+
				"唯一的解释来源(spec §4.2)", tgt.ID)
		}
	}
}

// chatgpt.com 实测恒为 CF 挑战页 ⇒ 恒 undetermined ⇒ 一行永远给不出答案的噪声。
// **这条守卫是给「顺手补全」的下一个人看的**:它不是漏了,是刻意不放。
func TestChatGPTIsDeliberatelyAbsentFromReachTargets(t *testing.T) {
	for _, tgt := range ReachTargets() {
		if strings.Contains(tgt.URL, "chatgpt.com") {
			t.Fatal("chatgpt.com 进了清单 —— 它恒为 CF 挑战页,会变成一行永远" +
				"「没查出来」的噪声;OpenAI 由 api.openai.com 代表(spec §4.1)")
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakcheck/ -run TestReachTargets 2>&1 | tail -3`
Expected: 编译失败 `undefined: ReachTargets`

- [ ] **Step 3: 最小实现**

```go
// 追加到 internal/leakcheck/endpoints.go

// ReachTarget 是一个可达性探测目标。
//
// ExpectedSignal 记的是**上一次真机实测见到什么**。它不参与判定,只用来让
// 「这个端点的行为变了」有人发现 —— CF 的防护会变,今天 claude.ai/favicon.ico
// 不挂挑战,明天可能挂(spec §10)。
type ReachTarget struct {
	ID             string
	Title          string
	URL            string
	ExpectedSignal string
	// OnChinaDirectList 是「这个域名在不在内建 china 直连列表里」。
	//
	// **它是解释,不是门。** 回声端点那三关的第三关是「不许在列表里」(在的话
	// 探测会走直连、报出真实 IP);可达性探测走哪条路由**由本功能自己指定**,
	// 不依赖分流 —— 所以这里要的是如实报告,作为该端点结论的一行证据。
	//
	// 指针类型:nil 是「没填」,由守卫拦下;false 是「查过了,不在」。
	OnChinaDirectList *bool
}

// ReachTargets 是内嵌的一小组 AI 站。**只发 GET,不带认证,不发 body** ——
// 401/405 恰恰是我们要的信号:服务自己在说话。
//
// **chatgpt.com 刻意不在这里**:它实测恒为 CF 挑战页,会变成一行永远给不出
// 答案的噪声(spec §4.1)。
func ReachTargets() []ReachTarget {
	return []ReachTarget{
		{
			ID: "anthropic_api", Title: "Anthropic API",
			URL:            "https://api.anthropic.com/v1/messages",
			ExpectedSignal: "405 + 服务 JSON(invalid_request_error / Method Not Allowed),2026-09-13 实测",
		},
		{
			ID: "claude_web", Title: "claude.ai",
			URL:            "https://claude.ai/favicon.ico",
			ExpectedSignal: "200 + favicon 二进制;首页是 403 CF 挑战,favicon 路径不挂防护,2026-09-13 实测",
		},
		{
			ID: "openai_api", Title: "OpenAI API",
			URL:            "https://api.openai.com/v1/models",
			ExpectedSignal: "401 + 服务 JSON(Missing bearer authentication),2026-09-13 实测",
		},
		{
			ID: "google_ai_api", Title: "Google AI API",
			URL:            "https://generativelanguage.googleapis.com/v1beta/models",
			ExpectedSignal: "403 + 服务 JSON(Method doesn't allow unregistered callers),2026-09-13 实测",
		},
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakcheck/ 2>&1 | tail -3`
Expected: `ok`

- [ ] **Step 5: 提交**

```bash
git add internal/leakcheck/endpoints.go internal/leakcheck/endpoints_test.go
git commit -m "feat(leakcheck): 可达性端点清单,每个带上次实测见到的信号

chatgpt.com 刻意不进清单并由一条守卫说明理由 —— 它恒为 CF 挑战页,
进来就是一行永远「没查出来」的噪声。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: 探测执行与两条路径

**Files:**
- Create: `internal/leakserve/reach.go`
- Test: `internal/leakserve/reach_test.go`

**Interfaces:**
- Consumes: `leakcheck.ReachTargets()`、`leakcheck.JudgeReach`、`leakcheck.ReachState`
- Produces: `type ReachProbe struct{ TargetID, Path string; State leakcheck.ReachState; Detail string }`、
  `func ProbeReach(ctx context.Context, dial DialFunc, path string) []ReachProbe`、
  `type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)`

**⚠️ spec §5.1 的待定项落在这里。** 本计划把「绕过隧道」那条路径的默认值设为
**关闭**(`DefaultProbeBypass = false`),理由:它会从物理网卡直接发 4 个 GET,
**把用户真实 IP 暴露给 Anthropic / OpenAI / Google**,而这是 leakcheck 今天
没有的新行为;所有者尚未明确批准这个新暴露,而本仓库的一贯默认是保守那边。
**翻转成本为零** —— 改那一个常量即可,所有者拍板后一行改动。

- [ ] **Step 1: 写失败测试**

```go
// internal/leakserve/reach_test.go
package leakserve

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getbx/bx/internal/leakcheck"
)

// 探测的结论必须来自 JudgeReach,而不是在这里重写一遍判据。
func TestProbeReachUsesTheSharedJudgement(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(405)
		_, _ = w.Write([]byte(`{"type":"error","error":{"message":"Method Not Allowed"}}`))
	}))
	defer srv.Close()

	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	got := probeOne(context.Background(), dial, leakcheck.ReachTarget{
		ID: "x", URL: srv.URL,
	}, "current")
	if got.State != leakcheck.ReachReachable {
		t.Fatalf("State = %v, want reachable —— 405 + 服务 JSON 是最强的可达证据", got.State)
	}
	if got.Path != "current" {
		t.Fatalf("Path = %q, want current", got.Path)
	}
}

// 拨不通要判 unreachable,而且**不许把错误原文带给用户** ——
// 它可能含内网地址、接口名。
func TestProbeReachOnDialFailureSaysNothingAboutTheError(t *testing.T) {
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Err: net.UnknownNetworkError("boom /private/var/secret")}
	}
	got := probeOne(context.Background(), dial, leakcheck.ReachTarget{
		ID: "x", URL: "https://example.invalid/",
	}, "bypass")
	if got.State != leakcheck.ReachUnreachable {
		t.Fatalf("State = %v, want unreachable", got.State)
	}
	if strings.Contains(got.Detail, "/private/var/secret") {
		t.Fatalf("Detail 带出了原始错误:%q", got.Detail)
	}
}

// 默认值是 spec §5.1 的待定项,由一条守卫钉住「今天是关的」——
// 所有者拍板改成开的时候,这条测试会红一次,那正是回来读 §5.1 的时刻。
func TestBypassProbeIsOffByDefaultUntilTheOwnerDecides(t *testing.T) {
	if DefaultProbeBypass {
		t.Fatal("绕过隧道的探测默认开着 —— 它会把用户真实 IP 暴露给 Anthropic/OpenAI/Google," +
			"spec §5.1 把这个决定留给所有者;真要改成默认开,连同这条守卫一起改")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakserve/ -run TestProbeReach 2>&1 | tail -3`
Expected: 编译失败 `undefined: probeOne` / `undefined: DefaultProbeBypass`

- [ ] **Step 3: 最小实现**

```go
// internal/leakserve/reach.go
package leakserve

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
)

// DefaultProbeBypass 决定「绕过隧道」那条路径默不默认跑。
//
// **今天是 false,这是 spec §5.1 的待定项。** 那条路径会从物理网卡直接发 4 个
// GET,把用户的**真实 IP** 暴露给 Anthropic / OpenAI / Google —— 而 leakcheck
// 今天的探测(icanhazip / cloudflare trace)走的都是当前路径,绑物理网卡发请求
// 是新增行为。所有者拍板之前保守。
const DefaultProbeBypass = false

// probeTimeout 是单个目标的上限。四个目标串行,最坏 4×。
const probeTimeout = 8 * time.Second

// DialFunc 是探测用的拨号器。两条路径的区别**只在这里**:
// 当前路径给普通 Dialer,绕过隧道给绑了物理接口的那个。
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// ReachProbe 是一个目标在一条路径上的结果。
type ReachProbe struct {
	TargetID string              `json:"target_id"`
	Path     string              `json:"path"` // "current" | "bypass"
	State    leakcheck.ReachState `json:"state"`
	// Detail 是给用户看的一句话。**绝不放原始错误** —— 它可能含内网地址、
	// 接口名、路径(与 Guardian 响应体只带失败码同一条纪律)。
	Detail string `json:"detail,omitempty"`
}

func probeOne(ctx context.Context, dial DialFunc, tgt leakcheck.ReachTarget, path string) ReachProbe {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	client := &http.Client{Transport: &http.Transport{DialContext: dial}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tgt.URL, nil)
	if err != nil {
		return ReachProbe{TargetID: tgt.ID, Path: path,
			State: leakcheck.ReachUnreachable, Detail: "这个地址解析不了"}
	}
	resp, err := client.Do(req)
	if err != nil {
		return ReachProbe{TargetID: tgt.ID, Path: path,
			State:  leakcheck.JudgeReach(0, nil, err),
			Detail: "这条路到不了"}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return ReachProbe{TargetID: tgt.ID, Path: path,
		State: leakcheck.JudgeReach(resp.StatusCode, body, nil)}
}

// ProbeReach 串行跑完一条路径上的全部目标。
// **串行是刻意的**:四个目标同时握手是一个很整齐的模式,而它们恰好都是
// AI 服务商(与 Servers 窗口「只在用户点时发、且串行」同一条)。
func ProbeReach(ctx context.Context, dial DialFunc, path string) []ReachProbe {
	targets := leakcheck.ReachTargets()
	out := make([]ReachProbe, 0, len(targets))
	for _, tgt := range targets {
		out = append(out, probeOne(ctx, dial, tgt, path))
	}
	return out
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakserve/ 2>&1 | tail -3`
Expected: `ok`

- [ ] **Step 5: 提交**

```bash
git add internal/leakserve/reach.go internal/leakserve/reach_test.go
git commit -m "feat(leakserve): 可达性探测执行,两条路径共用一个拨号器接口

绕过隧道那条默认关(spec §5.1 待所有者拍板)—— 它会把真实 IP 暴露给三家
AI 服务商,是 leakcheck 今天没有的新行为。守卫钉住「今天是关的」,
改成开的时候它会红一次,那正是回来读 §5.1 的时刻。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: 接进 `Outline()` 与 `Judge()`

**Files:**
- Modify: `internal/leakcheck/outline.go`(常量 + 骨架)
- Modify: `internal/leakcheck/judge.go`(把 probe 结果变成 Finding)
- Modify: `internal/leakcheck/input.go`(输入结构带上 reach 探测结果)
- Test: `internal/leakcheck/outline_test.go`
- Modify: `CLAUDE.md`(「十条结论」那句话)

**Interfaces:**
- Consumes: `ReachProbe`(Task 4 的形状,经 `Input` 传进来)
- Produces: `FindingReach` 常量族、`judgeReach(...) []Finding`

- [ ] **Step 1: 确认要改的两个数字与输入怎么传**

条数守卫在 `internal/leakcheck/outline_test.go:146`,当前是 `const documented = 10`。
新增 4 个目标 ⇒ **改成 14**。CLAUDE.md 里「**十条结论**」那句话同批改成**十四条**,
并写明第四段是 reach(path 5 / identity 4 / surface 1 / **reach 4**)。

**探测结果经 `LocalFacts` 传进 `Judge`** —— 它是**本机观测到的事实**,与
`DefaultRouteV4`/`DNSServers` 同一类,不是浏览器上报的那一半。`Judge` 的签名
`Judge(now, browser BrowserReport, local LocalFacts) Report` **不变**。

- [ ] **Step 2: 写失败测试**

```go
// 追加到 internal/leakcheck/outline_test.go

// 骨架与 Judge 的 ID/顺序/分段逐项对上 —— 页面按骨架先摆行、再按 ID 塞结论,
// 而页面对认不出的 ID 是静默丢弃,少一条就是一行永远等不到结论的空壳。
func TestEveryReachTargetHasASkeletonRow(t *testing.T) {
	outline := Outline()
	byID := map[string]bool{}
	for _, o := range outline {
		byID[o.ID] = true
	}
	for _, tgt := range ReachTargets() {
		id := FindingReachPrefix + tgt.ID
		if !byID[id] {
			t.Fatalf("端点 %s 没有骨架行(%s)—— 页面会永远停在「还在等」", tgt.ID, id)
		}
	}
}

// 第四段的骨架行必须标 SectionReach,否则它的结论会被算进流量泄漏数。
func TestReachSkeletonRowsAreInTheReachSection(t *testing.T) {
	for _, o := range Outline() {
		if strings.HasPrefix(o.ID, FindingReachPrefix) && o.Section != SectionReach {
			t.Fatalf("%s 的 Section = %v, want reach", o.ID, o.Section)
		}
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/leakcheck/ -run 'TestEveryReachTarget|TestReachSkeleton' 2>&1 | tail -3`
Expected: 编译失败 `undefined: FindingReachPrefix`

- [ ] **Step 4: 实现**

`outline.go` 加常量与骨架:

```go
// FindingReachPrefix 是可达性结论 ID 的前缀。**每个端点一条结论**,
// ID 由前缀拼端点 ID 得来 —— 拼接而不是手抄一份清单,加端点时骨架自动跟上。
const FindingReachPrefix = "reach_"
```

在 `Outline()` 返回的切片末尾追加:

```go
	for _, tgt := range ReachTargets() {
		rows = append(rows, OutlineRow{
			ID:      FindingReachPrefix + tgt.ID,
			Title:   tgt.Title,
			Section: SectionReach,
			Inputs:  nil, // 本机就能跑,不等浏览器
		})
	}
```

`judge.go` 加 `judgeReach`,把每个 `ReachProbe` 变成一条 Finding:
- `ReachReachable` → `Verdict: OK`,Summary「bx 能到达 <host>」
  **绝不写「你可以用 X」**(spec §3.4)
- `ReachRefused` → `Verdict: Bad`,Summary「<host> 拒绝了这个出口所在的地区」
- `ReachUndetermined` → `Verdict: NotChecked`,Summary「这是 Cloudflare 的人机
  挑战,**不是你的出口有问题**;浏览器能过,命令行过不了」
- `ReachUnreachable` → `Verdict: Bad`,Summary「这条路到不了 <host>」
  **不断言对方服务的状态**
- 每条 Finding 的 `Reach` 字段填对应 `ReachState`(Task 1 的计数靠它)
- 两条路径都有结果时,Evidence 里并排出示

- [ ] **Step 5: 跑测试确认通过 + 更新那两个数字**

Run: `go test ./internal/leakcheck/ 2>&1 | tail -3`
Expected: `ok`。若 `TestOutlineHasTheDocumentedNumberOfConclusions` 红,
**那正是它该红的时刻** —— 把数字改成 N+4,并同批改 CLAUDE.md 里那句话。

- [ ] **Step 6: 提交**

```bash
git add internal/leakcheck/ CLAUDE.md
git commit -m "feat(leakcheck): 第四段接进骨架与判定,措辞只说 bx 观测到什么

结论 ID 由前缀拼端点 ID 得来,不手抄第二份清单;加端点时骨架自动跟上。
措辞纪律:可达只说「bx 能到达 X」,绝不说「你可以用 X」—— 地区限制可能在
登录或调用层,本期只观测边缘。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: 渲染 —— CLI 与页面

**Files:**
- Modify: `internal/leakserve/page.html`(骨架分区 + 第四段的行)
- Modify: `internal/cli/leakcheck.go`(`renderLeakCheckReport`,摘要行在 152-155)
- Test: `internal/leakserve/pagejs_test.go`(若引入新 probe 名)

- [ ] **Step 1: 写失败测试**

```go
// 追加到 internal/cli/leakcheck_test.go

// 四态并排,而且 undetermined 与 unreachable **为零也要打印** ——
// 否则「一条都没查出来」与「查了、全可达」在屏幕上一样(spec §6.2)。
func TestLeakCheckSummaryPrintsReachCountsSeparately(t *testing.T) {
	rep := leakcheck.Report{
		Reach: leakcheck.ReachSummary{Reachable: 4},
	}
	out := strings.Join(renderLeakCheckReport(rep), "\n")
	if !strings.Contains(out, "4 reachable") {
		t.Fatalf("没报可达数:%q", out)
	}
	if !strings.Contains(out, "0 not checked") && !strings.Contains(out, "0 undetermined") {
		t.Fatalf("没报「没查出来」那一格 —— 它为零也必须打印:%q", out)
	}
}

// **现有的 notChecked 是数所有 Findings 里 Verdict==NotChecked 的**
// (`internal/cli/leakcheck.go:145` 那个循环),而 reach 段「没问出来」的
// Verdict 恰好也是 NotChecked ⇒ 它会被算进**泄漏检测**那个 not checked 数。
// 这是 Task 1 在 NewReport 里堵过的同一种静默合并,在渲染层又出现一次。
func TestReachUndeterminedDoesNotInflateTheLeakNotCheckedCount(t *testing.T) {
	rep := leakcheck.Report{
		Findings: []leakcheck.Finding{
			{ID: "p", Section: leakcheck.SectionPath, Verdict: leakcheck.OK},
			{ID: "reach_a", Section: leakcheck.SectionReach,
				Verdict: leakcheck.NotChecked, Reach: leakcheck.ReachUndetermined},
			{ID: "reach_b", Section: leakcheck.SectionReach,
				Verdict: leakcheck.NotChecked, Reach: leakcheck.ReachUndetermined},
		},
		Reach: leakcheck.ReachSummary{Undetermined: 2},
	}
	out := strings.Join(renderLeakCheckReport(rep), "\n")
	if strings.Contains(out, "2 not checked.") {
		t.Fatalf("把两条可达性「没问出来」算进了泄漏检测的 not checked:%q", out)
	}
	if !strings.Contains(out, "0 not checked") {
		t.Fatalf("泄漏检测那一格应是 0(没有一条 path/identity 结论没查):%q", out)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli/ -run TestLeakCheckSummary 2>&1 | tail -3`
Expected: FAIL

- [ ] **Step 3: 实现渲染**

**先修那个 notChecked 循环**(`internal/cli/leakcheck.go:145`):它今天数所有
Findings,加上 reach 段之后会把可达性的「没问出来」算进泄漏检测那一格 ——
循环里加一句 `if f.Section == leakcheck.SectionReach { continue }`。

然后在现有「N leak(s) / N identifying trait(s) / N not checked」之后
**并排**追加一段,不合并:

```
0 leak(s) in the traffic path, 0 identifying trait(s), 4 reachable, 0 refused, 0 not checked, 0 unreachable
```

页面:在三段之后加第四段容器,骨架行由 `Outline()` 里 `section == "reach"` 的行
生成。**新 ID 必须同批加进页面骨架** —— 页面对认不出的 ID 是 `if (!row) return;`
静默丢弃,漏一条就是一行永远停在「还在等」。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cli/ ./internal/leakserve/ 2>&1 | tail -3`
Expected: `ok`

- [ ] **Step 5: 全量 verify**

Run: `bash scripts/verify.sh`
Expected: `✓ verify passed (all steps ran)`,14 步全 ok

- [ ] **Step 6: 提交**

```bash
git add -A
git commit -m "feat(leakcheck,cli): 第四段的渲染 —— 四态并排,零也要打印

undetermined 与 unreachable 为零也打印:否则「一条都没查出来」与
「查了、全可达」在屏幕上长得一样。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: 收尾 —— 文档与待人工验收

**Files:**
- Modify: `CLAUDE.md`(泄漏检测那一节:第四段、端点、措辞纪律、已知边界)
- Modify: `docs/acceptance-pending.md`(A 组加一条)

- [ ] **Step 1: CLAUDE.md**

在「泄漏检测」那一节补:第四段 `SectionReach` 与它**独立的四态计数**;
端点清单与 `chatgpt.com` 刻意缺席的理由;**措辞纪律**(只说「bx 能到达」);
已知边界(favicon 200 只证明边缘可达、`Refused` 无真机样本、CF 防护会变);
`DefaultProbeBypass` 今天是 false 与它背后的取舍。**「十条结论」那个数字
在 Task 5 已改,这里核对一遍别漏。**

- [ ] **Step 2: 验收清单**

`docs/acceptance-pending.md` 的 A 组追加:

```markdown
### A8. AI 站可达性(第四段)

- [ ] `bx leakcheck` 页面上出现第四段,四个目标各一行
- [ ] 四态计数**与泄漏计数并排**,不是合成一个数
- [ ] `claude.ai` 那行应是**可达**(favicon 路径);若显示「没查出来」,
      说明 CF 把防护加到 favicon 上了 —— 回来更新端点的 ExpectedSignal
- [ ] 措辞是「bx 能到达 claude.ai」,**不是**「你可以用 Claude」
- [ ] 把服务器切到一台不通的 VPS,再跑一次:应变成 unreachable 而**不是**
      多了几条「泄漏」
```

- [ ] **Step 3: 全量 verify + 提交**

```bash
bash scripts/verify.sh && git add -A && git commit -m "docs: 第四段进 CLAUDE.md 与待验收清单

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```
