# 规则体检(静态那一半)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 `policy.DirectRisk` 与两类「冗余」判据接到诊断那一侧,让一条已经躺在配置里的
去匿名化风险规则、以及一条永远不生效的规则,能被 `bx doctor` 和 `bx status` 说出来。

**Architecture:** 新增 `internal/rulereview` —— **纯判据包**(照 `internal/leakcheck` 的形状:
无 net / 无 os / 无 os/exec,由 `purity_test.go` 按 AST 钉住),输入是「用户规则两张表 +
global 开关 + 内建 china 列表」,输出是一组**分类计数彼此独立**的 findings。接线只有两处:
`bx doctor`(文本 + `--json`,全部四类)与 `bx status`(Core 侧,**只发危险那一类**,
走既有的 `stats.Warning` 通道)。

**Tech Stack:** Go 1.26、`internal/route.DomainSet`(后缀匹配 + `MatchRule` 回报配置原文)、
`internal/policy.DirectRisk`、`internal/embedded.ChinaDomain()`、`internal/setup.ListRules`、
`internal/stats.Warning`、`net/netip`(判 CIDR/裸 IP)。

---

## 本计划的范围,以及为什么这么切

spec 有四条检查。**本计划只做第 1 条(危险规则)与第 3 条(冗余规则)。**

- 第 2 条(死规则)的硬前置是**跨重启累计的按规则计数**,spec 自己写明它是「本功能最主要的
  新增基础设施」。它牵出一串本计划答不了的问题:谁来写(计数在 Core 的 `stats.Counters` 里,
  而本仓库唯一的持久化范例 `throughputhistory.go` 由 **Guardian 的调谐环**驱动)、按什么键
  (`ruleKey{source, rule}`,而用户改一个字规则原文就变了、历史计数怎么办)、Linux/Windows
  上没有 Guardian 又由谁写。**那是一份独立的 spec,不是一个 task。**
- 第 4 条(缺失规则)spec 自己说「最容易变成噪声,门槛要比前三条严得多,宁可不说」。
  它依赖第 2 条的同一套数据。

本计划交付的是一个**独立可用、可真机验证**的增量:装上之后 `bx doctor` 立刻能对项目所有者
现有的 24 条规则给出结论,零新增数据、零新增持久化、零新增出站。

## 比 spec 多出来的一类,以及为什么加它

spec §3 只说「冗余」。而把用户的 direct 表与 proxy 表放在一起比对时,会掉出**第四类**:

`route.Router.Explain` 的判定顺序是 **UserProxy → UserDirect → ChinaDomain → 默认**
(`internal/route/explain.go:78`),**没有任何「更具体的规则优先」这回事**。于是:

- proxy 里有 `*.apple.com`、direct 里有 `ocsp.apple.com` ⇒ 查 `ocsp.apple.com` 时
  **UserProxy 先命中**,那条 direct 规则**永远不生效**。用户以为他给 OCSP 开了直连,
  实际上一次都没走过。

这不是「冗余」(删掉它什么都不变,这点与冗余相同),而是「**你写的这条从来没工作过**」——
两者该说的话完全不同,所以是独立的一类、独立的计数。它零新增数据,与第 3 条共用同一次比对。

**它不许靠读代码断言。** Task 3 有一条测试用 `supervisor.BuildRouter` 建**真的** `route.Router`,
对同一份 fixture 跑 `Explain`,证明那条 direct 规则确实拿不到 `Decision Direct` —— 判定顺序
是别人的实现细节,哪天它改成「最长后缀优先」,该转红的是这条测试,不是用户的配置。

## Global Constraints

以下每一条都是**全局要求**,每个 task 的验收隐含包含它们:

- **`mode` 不是那个 mode。** spec 里「必须读当前 mode」指的是 `config.Config.Global bool`
  (yaml `global:`)。`config.Config.Mode` 是**另一个东西**,取值只有 `host` / `router`
  (`internal/config/config.go:224-231`),与 global/split 无关。
  `split|global|router|router-global` 是 `supervisor.proxyMode(global, mode)` 派生出来的
  **展示标签**,不是配置字段。**读错这个字段,就是把 spec 当天真机抓到的那个 bug 原样重犯。**
- **四类计数各自独立,永远不合成一个总数**(spec §3 明令)。`Report` 没有 `TotalCount`
  这种字段,渲染层也不许自己把它们加起来。
- **报的是配置里那一行的原文**,不是内部归一化形式。`*.a.com` 内部存成 `a.com`
  (`route.NewDomainSet`),报 `a.com` 会让用户去搜一个搜不到的串 —— 用
  `DomainSet.MatchRule` 拿原文(`internal/route/domainset.go:51`)。
- **只报用户规则。** 内建 china 列表里没有哪一行可点名、用户也改不了
  (与 `stats.ruleWorthReporting` 同一条纪律)。
- **「没查」与「查了没有」必须分得开。** global 模式、或用户用 `lists.china_domain` 换了
  自己的列表时,内建列表那一类是**没查**,不是**零条**。
- **零值站在多报那边。** `Class` 的零值是 `ClassRisky`(最响的那一类);漏填 `Class` 是
  多报一条安全告警,反过来是把一条安全告警降级成建议。代价不对称(与
  `leakcheck.Section` 零值取 `SectionPath` 同一条理由)。
- **不改配置、不做主动探测、不联网**(spec 非目标)。
- 中文 conventional commits,结尾 `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`。
  在 `master` 直接提交。
- 每个 task 收尾跑 `bash scripts/verify.sh --quick`;最后一个 task 跑全量 `bash scripts/verify.sh`。
- **绝不启动 bx、绝不改路由**(CLAUDE.md)。全部测试免 root。

---

## File Structure

**新建 `internal/rulereview/`(纯判据,无 I/O):**

| 文件 | 责任 |
|---|---|
| `verdict.go` | `Class` 枚举 + `String()`/`MarshalJSON`、`Finding`、`Report`、`NewReport`(按类分别计数) |
| `input.go` | `Input` 结构、`normalizeRule`、`isNetworkLiteral`(判 CIDR/裸 IP 并跳过)、`domainRules` |
| `review.go` | `Review(Input) Report` —— 四类判据的唯一入口 |
| `purity_test.go` | AST 守卫:禁 net/os/os/exec 与控制面 internal 包 |
| `review_test.go` / `input_test.go` / `verdict_test.go` | 表驱动判据测试 |
| `fixture_test.go` | 项目所有者那 24 条规则的代表性 fixture(spec 里那次真机实测的形状) |

**修改:**

| 文件 | 改什么 |
|---|---|
| `internal/cli/cli.go` | `doctorAction` 文本行 + `collectClientDoctorWith` 的 `--json` checks |
| `internal/cli/rulereview.go`(新建) | doctor 那一侧的**组装**:读 config → 建 china DomainSet → 调 `rulereview.Review` → 渲染。**单独成文件是刻意的:本仓库全部事故都在组装根上,组装必须是一个可以单测的纯函数。** |
| `internal/cli/rulereview_test.go`(新建) | 组装函数的测试 + doctor 接线守卫 |
| `internal/supervisor/control.go` | `serveControlWithPathRecovery` 多一个 `configWarnings []stats.Warning` 形参,`report` 里 `append` 进 `Warnings` |
| `internal/supervisor/run.go:712` | 传入 `riskyRuleWarnings(cfg)` |
| `internal/supervisor/ruleprecedence_test.go`(新建) | 用真 `BuildRouter` 证明 proxy 压 direct;`riskyRuleWarnings` 的测试 + 接线守卫 |

---

## Task 1: `internal/rulereview` 骨架 + 危险规则(spec §1)

**Files:**
- Create: `internal/rulereview/verdict.go`
- Create: `internal/rulereview/input.go`
- Create: `internal/rulereview/review.go`
- Create: `internal/rulereview/purity_test.go`
- Test: `internal/rulereview/review_test.go`, `internal/rulereview/input_test.go`

**Interfaces:**
- Consumes: `policy.DirectRisk(domain string) bool`(`internal/policy/policy.go:30`)
- Produces:
  - `type Class uint8`,常量 `ClassRisky`(=0)、`ClassShadowedByUserRule`、
    `ClassOverriddenByOppositeKind`、`ClassShadowedByBuiltinList`
  - `type Finding struct { Kind, Rule string; Class Class; Summary, CoveredBy string }`
  - `type Report struct { Findings []Finding; RiskyCount, ShadowedByUserCount, OverriddenCount, ShadowedByBuiltinCount int; BuiltinListChecked bool; BuiltinSkipReason string }`
  - `func NewReport(findings []Finding, builtinChecked bool, builtinSkipReason string) Report`
  - `type Input struct { Direct, Proxy []string; GlobalProxy bool; China *route.DomainSet; ChinaSkipReason string }`
  - `func Review(in Input) Report`
  - `func isNetworkLiteral(s string) bool`(包内)

- [ ] **Step 1: 写失败测试 —— 危险规则 + CIDR 条目必须被跳过**

创建 `internal/rulereview/review_test.go`:

```go
package rulereview

import "testing"

// 起因就是这一条:项目所有者的配置里有 *.myqcloud.com,而 policy.DirectRisk 全仓
// 只有 bx direct add 一个调用点 —— 不管它是绕过守卫直接改 YAML 加的,还是守卫上线
// 之前就在的,此后再也没有任何东西会提醒他。
func TestRiskyDirectRuleIsReported(t *testing.T) {
	rep := Review(Input{Direct: []string{"*.myqcloud.com", "*.qq.com"}})

	if rep.RiskyCount != 1 {
		t.Fatalf("RiskyCount = %d, want 1;findings=%+v", rep.RiskyCount, rep.Findings)
	}
	if len(rep.Findings) != 1 {
		t.Fatalf("findings 数 = %d, want 1: %+v", len(rep.Findings), rep.Findings)
	}
	f := rep.Findings[0]
	if f.Class != ClassRisky {
		t.Errorf("Class = %v, want ClassRisky", f.Class)
	}
	// **原文,不是归一化形式。** 报 myqcloud.com 而他写的是 '*.myqcloud.com',
	// 他会去配置里搜一个搜不到的串。
	if f.Rule != "*.myqcloud.com" {
		t.Errorf("Rule = %q, want %q(必须是配置里那一行的原文)", f.Rule, "*.myqcloud.com")
	}
	if f.Kind != "direct" {
		t.Errorf("Kind = %q, want direct", f.Kind)
	}
	if f.Summary == "" {
		t.Error("Summary 是空的 —— 一条不说明理由的告警,用户没有理由信它")
	}
}

// proxy 那张表不受这条判据管:强制走隧道的域名不存在「去匿名化」这回事,
// DirectRisk 的整个语义是「把它放进**直连**白名单会怎样」。
func TestRiskyJudgementDoesNotApplyToProxyRules(t *testing.T) {
	rep := Review(Input{Proxy: []string{"*.myqcloud.com"}})
	if rep.RiskyCount != 0 {
		t.Fatalf("proxy 规则被判成了危险直连:%+v", rep.Findings)
	}
}

// rules[].direct / rules[].proxy 里**可以写 CIDR 和裸 IP**(supervisor.BuildRouter 的
// asCIDR 会把它们分到 CIDRSet 去)。把它们当域名喂进判据是无意义的,而更要紧的是
// 别让它们产出任何**看起来言之凿凿的**结论。
//
// 这里刻意只断言「一条都不报」而不是「报得对」:本包不复制 asCIDR 那份判定
// (判定只有一份是本仓库的纪律),代价是遇到网络字面量时**沉默**——沉默是漏报,
// 而漏报的代价远小于对着一条 CIDR 说「这条规则可以删」。
func TestNetworkLiteralsAreSkippedEntirely(t *testing.T) {
	rep := Review(Input{
		Direct: []string{"10.0.0.0/8", "192.168.1.1", "2001:db8::/32", "::1"},
		Proxy:  []string{"172.16.0.0/12"},
	})
	if len(rep.Findings) != 0 {
		t.Fatalf("网络字面量产出了结论,而本包读不懂它们:%+v", rep.Findings)
	}
}

// 空输入必须是干净报告,而不是 nil panic。
func TestEmptyInputIsCleanReport(t *testing.T) {
	rep := Review(Input{})
	if len(rep.Findings) != 0 || rep.RiskyCount != 0 {
		t.Fatalf("空配置报出了东西:%+v", rep)
	}
}
```

- [ ] **Step 2: 跑测试,确认失败**

Run: `go test ./internal/rulereview/ -run 'TestRisky|TestNetworkLiteral|TestEmptyInput' -v`
Expected: FAIL —— `no Go files in .../internal/rulereview`(包还不存在)

- [ ] **Step 3: 写 `verdict.go`**

```go
// Package rulereview 是「规则体检」的**纯判据**层:给定用户自己写的那两张规则表
// (direct / proxy)、当前是不是 global、以及内建 china 列表,产出一组分类结论。
//
// 本包不做 I/O:没有 net、没有 os、没有 os/exec、没有平台代码(由 purity_test.go
// 按 AST 钉住)。理由与 internal/leakcheck 相同 —— 这个功能唯一值钱的部分就是判据,
// 而判据必须能被表驱动测试与变异验证完整覆盖;一旦它跟「读配置、找列表文件、渲染」
// 混在一起,「判得对不对」与「接线对不对」就重新变成同一件事,而本仓库的全部事故
// 都在后者。
package rulereview

// Class 是一条结论属于哪一类。**四类各自计数,永远不合成一个总数。**
//
// 合成一个数时,它对任何一份成熟配置都不为零(手写规则被更宽的一条盖住是常态),
// 于是会被训练成噪声,把真正要紧的那一类——危险直连——一起淹掉。这与 leakcheck
// 把 path / identity / surface 分段的理由是同一条。
//
// **零值是 ClassRisky,方向是刻意选的。** 漏填 Class 时一条建议被算进安全告警,
// 那是多报;反过来则是把一条去匿名化风险降级成「可以删的冗余」。代价不对称。
type Class uint8

const (
	// ClassRisky:这条直连规则覆盖了公有云存储/CDN/开放子域平台。任何人都能注册
	// 它的子域,于是攻击者能用一个子域让你的真实 IP 暴露。**这是安全结论,不是建议。**
	ClassRisky Class = iota
	// ClassShadowedByUserRule:被你自己**同一张表**里更宽的一条盖住,删掉它什么都不变。
	// 模式无关 —— 任何 mode 下都成立。
	ClassShadowedByUserRule
	// ClassOverriddenByOppositeKind:被**另一张表**里更宽的一条压住,于是它
	// **从来没有生效过**。route.Router 先查 proxy 再查 direct,没有「更具体的优先」
	// 这回事,所以一条 direct 规则可以被一条更宽的 proxy 规则整个吃掉。
	//
	// 与上一类的区别不在于删不删得掉(都能删),而在于**要说的话完全不同**:
	// 上一类是「重复了」,这一类是「你以为它在工作,它没有」。
	ClassOverriddenByOppositeKind
	// ClassShadowedByBuiltinList:被内建 china 直连列表覆盖。**依赖 mode** ——
	// global 下 china 列表整个不生效,那时这个结论是错的(见 Review 的 gating)。
	ClassShadowedByBuiltinList
)

func (c Class) String() string {
	switch c {
	case ClassShadowedByUserRule:
		return "shadowed_by_user_rule"
	case ClassOverriddenByOppositeKind:
		return "overridden_by_opposite_kind"
	case ClassShadowedByBuiltinList:
		return "shadowed_by_builtin_list"
	default:
		return "risky_direct"
	}
}

// MarshalJSON 让 JSON 里是词而不是 0/1/2/3 —— agent 与 MCP 直接按它分类。
func (c Class) MarshalJSON() ([]byte, error) { return []byte(`"` + c.String() + `"`), nil }

// Finding 是一条可核对的结论。
//
// CoveredBy 是**盖住它的那一条的原文**,三类 shadow/override 必填:一个只说
// 「这条冗余」而不肯说「被哪一条盖住」的报告,用户没法核对,也就没法信。
type Finding struct {
	// Kind 是这条规则所在的表:direct | proxy(与 setup.RuleKindDirect/Proxy 同词)。
	Kind string `json:"kind"`
	// Rule 是**配置里那一行的原文**(如 `*.myqcloud.com`),不是归一化形式。
	Rule      string `json:"rule"`
	Class     Class  `json:"class"`
	Summary   string `json:"summary"`
	CoveredBy string `json:"covered_by,omitempty"`
}

// Report 是一次体检的全部产出。**没有 TotalCount。**
type Report struct {
	Findings []Finding `json:"findings,omitempty"`

	RiskyCount             int `json:"risky_count"`
	ShadowedByUserCount    int `json:"shadowed_by_user_count"`
	OverriddenCount        int `json:"overridden_count"`
	ShadowedByBuiltinCount int `json:"shadowed_by_builtin_count"`

	// BuiltinListChecked 区分「查了,没有」与「根本没查」。**刻意无 omitempty** ——
	// 键缺席会被读成 false 而理由不明,而这两件事的差别正是本功能最贵的那个教训。
	BuiltinListChecked bool `json:"builtin_list_checked"`
	// BuiltinSkipReason 在没查时说明为什么(global / 用户换了自己的列表 / 列表读不到)。
	BuiltinSkipReason string `json:"builtin_skip_reason,omitempty"`
}

// NewReport 组装报告并**按类**计数。
func NewReport(findings []Finding, builtinChecked bool, builtinSkipReason string) Report {
	rep := Report{Findings: findings, BuiltinListChecked: builtinChecked, BuiltinSkipReason: builtinSkipReason}
	for _, f := range findings {
		switch f.Class {
		case ClassShadowedByUserRule:
			rep.ShadowedByUserCount++
		case ClassOverriddenByOppositeKind:
			rep.OverriddenCount++
		case ClassShadowedByBuiltinList:
			rep.ShadowedByBuiltinCount++
		default:
			rep.RiskyCount++
		}
	}
	return rep
}
```

- [ ] **Step 4: 写 `input.go`**

```go
package rulereview

import (
	"net/netip"
	"strings"

	"github.com/getbx/bx/internal/route"
)

// Input 是一次体检的全部原料。
type Input struct {
	// Direct / Proxy 是用户 config 里 rules[].direct / rules[].proxy 的**原文**,
	// 由 setup.ListRules 摊平后传进来。
	Direct []string
	Proxy  []string

	// GlobalProxy 是 config 的 `global:` 开关。
	//
	// **不是 config.Mode。** config.Mode 取值只有 host|router,与 global/split 无关;
	// split|global|router|router-global 是 supervisor.proxyMode 派生的展示标签。
	// spec 写完当天的真机实测就栽在这:拿内建 china 列表比出 22 条「冗余」,
	// 而那台机器是 global —— 那 22 条全都在干活,照着删会让 22 个域名改走隧道。
	GlobalProxy bool

	// China 是内建 china 直连列表。nil = 没拿到(调用方没读到,或刻意不比)。
	// **nil 与「比了没命中」必须分得开**,由 ChinaSkipReason 说明。
	China *route.DomainSet
	// ChinaSkipReason 在 China 为 nil 或被 GlobalProxy 压制时给出人话理由。
	ChinaSkipReason string
}

// normalizeRule 把一条规则化成可比对的域名形式,与 route.NewDomainSet 的处理一致:
// 小写、去空白、去 `*.` 前缀、去尾点。
//
// **归一化只有这一份,而且只用于比对** —— 报给用户的一律是原文。
func normalizeRule(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "*.")
	return strings.TrimSuffix(s, ".")
}

// isNetworkLiteral 判断这一条是不是 CIDR 或裸 IP。
//
// rules[].direct / rules[].proxy 两种都收(supervisor.BuildRouter 的 asCIDR 会把
// 网络字面量分到 CIDRSet、其余当域名)。本包**不复制**那份判定 —— 判定只有一份是
// 本仓库的纪律 —— 只做一件事:认出网络字面量就整条跳过,一个字都不说。
//
// 代价是**沉默**(漏报),而不是对着一条 CIDR 断言「这条可以删」(错报)。
// 两者代价不对称。
func isNetworkLiteral(s string) bool {
	s = strings.TrimSpace(s)
	if _, err := netip.ParsePrefix(s); err == nil {
		return true
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}

// domainRule 是一条通过筛选的域名规则:原文 + 归一化形式。
type domainRule struct {
	raw  string
	norm string
}

// domainRules 过滤出可判的域名规则,并**按归一化形式去重**。
//
// 去重是必须的,不是优化:两条完全重复的规则会互相指认对方是「更宽的那一条」,
// 报告于是说两条都可以删 —— 用户照做,规则整个没了。重复时保留**第一条原文**
// (与 route.NewDomainSet 对重复后缀的处置一致:报哪一条都对,但要稳定)。
func domainRules(list []string) []domainRule {
	seen := make(map[string]struct{}, len(list))
	out := make([]domainRule, 0, len(list))
	for _, raw := range list {
		if strings.TrimSpace(raw) == "" || isNetworkLiteral(raw) {
			continue
		}
		n := normalizeRule(raw)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, domainRule{raw: raw, norm: n})
	}
	return out
}
```

- [ ] **Step 5: 写 `review.go`(本 task 只做危险那一类)**

```go
package rulereview

import "github.com/getbx/bx/internal/policy"

// riskySummary 是危险直连那一类要说的话。措辞与 internal/cli/direct.go 的
// directRuleRisk 保持同源语义:同一个判据在 add 那一侧和体检这一侧说的必须是同一件事。
const riskySummary = "公有云存储/CDN/开放子域平台——任何人都能注册它的子域;" +
	"留在直连白名单里 = 攻击者能用一个子域让你的真实 IP 暴露(去匿名化)。" +
	"建议只白名单品牌自控的顶级域。"

// Review 跑完四类判据。**纯函数:同样的输入永远给同样的输出。**
func Review(in Input) Report {
	direct := domainRules(in.Direct)

	var findings []Finding
	findings = append(findings, riskyFindings(direct)...)

	return NewReport(findings, false, in.ChinaSkipReason)
}

// riskyFindings 只看 direct 表:DirectRisk 的整个语义是「把它放进**直连**白名单
// 会怎样」,对 proxy 规则问这个问题没有意义。
func riskyFindings(direct []domainRule) []Finding {
	var out []Finding
	for _, r := range direct {
		if !policy.DirectRisk(r.norm) {
			continue
		}
		out = append(out, Finding{
			Kind:    "direct",
			Rule:    r.raw,
			Class:   ClassRisky,
			Summary: riskySummary,
		})
	}
	return out
}
```

- [ ] **Step 6: 跑测试,确认通过**

Run: `go test ./internal/rulereview/ -v`
Expected: PASS(4 条)

- [ ] **Step 7: 写纯度守卫 `purity_test.go`**

```go
package rulereview

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// allowedInternalDeps 是本包允许依赖的 internal 包 —— 两个都是**纯计算**的叶子:
// route 只做后缀/CIDR 匹配,policy.DirectRisk 只查一张写死的静态表。
//
// 依赖控制面(supervisor/guardian/cli/install/setup)任何一个,都会把判据拖回
// 「只能靠人读」的位置,而这个功能唯一值钱的部分就是判据。
var allowedInternalDeps = map[string]struct{}{
	"github.com/getbx/bx/internal/route":  {},
	"github.com/getbx/bx/internal/policy": {},
}

// 本包必须保持纯判据:不联网、不读文件、不跑命令。
//
// 读不懂目录时**必须响亮失败**:一个找不到源文件就自动通过的守卫,等于没有守卫。
func TestRulereviewPackageStaysPure(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读不到本包目录,守卫失去意义: %v", err)
	}
	banned := map[string]string{
		"os":      "读环境/读文件会让判据依赖运行环境;找列表文件是调用方的事",
		"os/exec": "跑命令属于组装层",
		"net":     "判据不许联网(spec 非目标:不做主动探测)",
		"net/http": "判据不许联网",
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s 失败,守卫读不懂现在的代码: %v", name, err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s 的 import 解析失败: %v", name, err)
			}
			if why, bad := banned[path]; bad {
				t.Errorf("%s import 了 %q:本包必须保持纯判据 —— %s", name, path, why)
			}
			if strings.HasPrefix(path, "github.com/getbx/bx/internal/") {
				if _, ok := allowedInternalDeps[path]; !ok {
					t.Errorf("%s import 了 %q:纯判据只允许依赖 %v —— "+
						"依赖控制面任何一个包都会让「判得对不对」与「接线对不对」重新变成同一件事",
						name, path, allowedInternalDeps)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("本包一个非测试 .go 文件都没找到:守卫读不懂现在的目录结构,请连同它一起重写")
	}
}
```

- [ ] **Step 8: 变异验证守卫真的会红**

在 `review.go` 顶部临时加一行 `import "os"` 并在 `Review` 里加 `_ = os.Getenv("X")`。

Run: `go test ./internal/rulereview/ -run TestRulereviewPackageStaysPure -v`
Expected: FAIL,信息含 `本包必须保持纯判据`

删掉那两行,重跑:
Run: `go test ./internal/rulereview/ -run TestRulereviewPackageStaysPure -v`
Expected: PASS

- [ ] **Step 9: 跑 verify + 提交**

```bash
bash scripts/verify.sh --quick
git add internal/rulereview/
git commit -m "$(cat <<'EOF'
feat(rulereview): 危险直连规则的判据接到 add 之外

policy.DirectRisk 全仓只有 internal/cli/direct.go 一个调用点 —— 配置加载不查、
status 不查、doctor 不查。于是不管一条 *.myqcloud.com 是守卫上线之前加的、还是
直接改 YAML 加的,此后再也没有任何东西会提醒他。

新包 internal/rulereview 是纯判据(照 leakcheck 的形状,purity_test 按 AST 钉住),
本 commit 只落第一类。Class 零值取 ClassRisky:漏填是把建议报成告警(多报),
反过来是把去匿名化风险降级成建议(漏报),代价不对称。

网络字面量(CIDR/裸 IP)整条跳过而不是勉强判——本包不复制 asCIDR 那份判定,
代价是沉默,而沉默远好过对着一条 CIDR 说「这条可以删」。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: 被自己更宽的一条覆盖(spec §3 的模式无关那一半)

**Files:**
- Modify: `internal/rulereview/review.go`
- Test: `internal/rulereview/review_test.go`

**Interfaces:**
- Consumes: Task 1 的 `domainRule`、`domainRules`、`Finding`、`ClassShadowedByUserRule`
- Produces: `func shadowedByUserFindings(kind string, rules []domainRule) []Finding`

- [ ] **Step 1: 写失败测试**

追加到 `internal/rulereview/review_test.go`:

```go
// spec 里那次真机实测:11 条是被用户自己更宽的一条盖住的。这一类**模式无关** ——
// 任何 mode 下都成立,所以它是四类里唯一一条可以无条件说出口的「可以删」。
func TestShadowedByUserOwnBroaderRule(t *testing.T) {
	rep := Review(Input{Direct: []string{"*.apple.com", "ocsp.apple.com", "*.qq.com"}})

	if rep.ShadowedByUserCount != 1 {
		t.Fatalf("ShadowedByUserCount = %d, want 1;findings=%+v", rep.ShadowedByUserCount, rep.Findings)
	}
	f := rep.Findings[0]
	if f.Rule != "ocsp.apple.com" {
		t.Errorf("被盖住的应该是窄的那条,got Rule=%q", f.Rule)
	}
	// 一个只说「这条冗余」而不肯说「被哪一条盖住」的报告,用户没法核对。
	if f.CoveredBy != "*.apple.com" {
		t.Errorf("CoveredBy = %q, want %q(必须是原文)", f.CoveredBy, "*.apple.com")
	}
	if f.Class != ClassShadowedByUserRule {
		t.Errorf("Class = %v, want ClassShadowedByUserRule", f.Class)
	}
}

// **自遮蔽陷阱。** DomainSet.Match("a.com") 对含 a.com 的集合恒为真,所以
// 「拿全表比每一条」会把每一条都报成冗余。被测那条必须先被排除掉。
func TestARuleIsNeverShadowedByItself(t *testing.T) {
	rep := Review(Input{Direct: []string{"*.apple.com"}, Proxy: []string{"*.qq.com"}})
	if len(rep.Findings) != 0 {
		t.Fatalf("规则把自己报成了冗余:%+v", rep.Findings)
	}
}

// **互相指认陷阱。** 两条完全重复的规则各自被对方覆盖,天真实现会说两条都能删;
// 用户照做,这条规则整个没了。去重后只留第一条,且不产出任何 finding ——
// 本计划不做「重复规则」这一类(spec 没有它),沉默是刻意的。
func TestExactDuplicatesDoNotAccuseEachOther(t *testing.T) {
	rep := Review(Input{Direct: []string{"*.apple.com", "apple.com", "*.APPLE.com."}})
	if len(rep.Findings) != 0 {
		t.Fatalf("重复规则互相指认成冗余:%+v", rep.Findings)
	}
}

// 跨表不算这一类:direct 里的 ocsp.apple.com 被 proxy 里的 *.apple.com 压住,
// 那是另一回事(Task 3),语义相反,绝不能合并计数。
func TestShadowByUserRuleIsWithinTheSameKindOnly(t *testing.T) {
	rep := Review(Input{Direct: []string{"ocsp.apple.com"}, Proxy: []string{"*.apple.com"}})
	if rep.ShadowedByUserCount != 0 {
		t.Fatalf("跨表被算进了同表冗余:%+v", rep.Findings)
	}
}

// proxy 表内部同样要查。
func TestShadowByUserRuleAppliesToProxyTableToo(t *testing.T) {
	rep := Review(Input{Proxy: []string{"*.google.com", "mail.google.com"}})
	if rep.ShadowedByUserCount != 1 {
		t.Fatalf("proxy 表内部的冗余没被查出来:%+v", rep.Findings)
	}
	if rep.Findings[0].Kind != "proxy" {
		t.Errorf("Kind = %q, want proxy", rep.Findings[0].Kind)
	}
}
```

- [ ] **Step 2: 跑测试,确认失败**

Run: `go test ./internal/rulereview/ -run 'TestShadowed|TestARuleIsNever|TestExactDuplicates|TestShadowByUser' -v`
Expected: FAIL —— `ShadowedByUserCount = 0, want 1`

- [ ] **Step 3: 实现**

在 `internal/rulereview/review.go` 的 import 里加 `"github.com/getbx/bx/internal/route"`,
`Review` 改成:

```go
func Review(in Input) Report {
	direct := domainRules(in.Direct)
	proxy := domainRules(in.Proxy)

	var findings []Finding
	findings = append(findings, riskyFindings(direct)...)
	findings = append(findings, shadowedByUserFindings("direct", direct)...)
	findings = append(findings, shadowedByUserFindings("proxy", proxy)...)

	return NewReport(findings, false, in.ChinaSkipReason)
}
```

追加:

```go
// shadowedByUserFindings 找出**同一张表**里被更宽的一条盖住的规则。
//
// 判据:把**除它自己以外**的其余规则建成一个 DomainSet,看它的域名命不命中。
// 「除它自己以外」是承重的 —— DomainSet.Match 对自身恒为真,不排除就是每一条
// 都报冗余;而调用方拿到的是 domainRules 去过重的表,所以也不会有两条互相指认。
//
// 每条都重建一次 DomainSet 是 O(n²),而 n 是用户手写的规则数(实测量级 24)。
// 换成一次建表 + 反查,要额外维护「哪个后缀来自哪条」的簿记,而那正是出错的地方。
func shadowedByUserFindings(kind string, rules []domainRule) []Finding {
	var out []Finding
	for i, r := range rules {
		others := make([]string, 0, len(rules)-1)
		for j, o := range rules {
			if i == j {
				continue
			}
			others = append(others, o.raw)
		}
		covering, ok := route.NewDomainSet(others).MatchRule(r.norm)
		if !ok {
			continue
		}
		out = append(out, Finding{
			Kind:      kind,
			Rule:      r.raw,
			Class:     ClassShadowedByUserRule,
			Summary:   "已被你自己更宽的一条覆盖,删掉它不会改变任何流量的去向。",
			CoveredBy: covering,
		})
	}
	return out
}
```

- [ ] **Step 4: 跑测试,确认通过**

Run: `go test ./internal/rulereview/ -v`
Expected: PASS(全部)

- [ ] **Step 5: 变异验证「排除自己」是承重的**

把 `if i == j { continue }` 临时改成 `if false { continue }`。

Run: `go test ./internal/rulereview/ -run 'TestARuleIsNeverShadowedByItself|TestEmptyInput|TestShadowedByUserOwnBroaderRule' -v`
Expected: FAIL —— `TestARuleIsNeverShadowedByItself` 报「规则把自己报成了冗余」

改回来,重跑:
Expected: PASS

- [ ] **Step 6: 提交**

```bash
bash scripts/verify.sh --quick
git add internal/rulereview/
git commit -m "$(cat <<'EOF'
feat(rulereview): 被自己更宽的一条覆盖 —— 四类里唯一模式无关的那条

spec 当天那次真机实测里,这一类单独占 11 条。它任何模式下都成立,所以是唯一
可以无条件说出口的「可以删」。

两个陷阱各配一条测试并变异验证过:
· 自遮蔽 —— DomainSet.Match 对自身恒为真,不排除被测那条就是每条都报冗余;
· 互相指认 —— 两条完全重复的规则各被对方覆盖,天真实现说两条都能删,
  用户照做规则就整个没了。domainRules 先按归一化去重,保留第一条原文。

跨表不算这一类:direct 的窄规则被 proxy 的宽规则压住语义完全相反,下一个 commit 处理。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: 被反向的更宽规则压住 —— 那条从来没工作过的规则

**Files:**
- Modify: `internal/rulereview/review.go`
- Test: `internal/rulereview/review_test.go`
- Create: `internal/supervisor/ruleprecedence_test.go`

**Interfaces:**
- Consumes: Task 2 的全部
- Produces: `func overriddenFindings(direct, proxy []domainRule) []Finding`

- [ ] **Step 1: 写失败测试(判据侧)**

追加到 `internal/rulereview/review_test.go`:

```go
// route.Router.Explain 的顺序是 UserProxy → UserDirect → ChinaDomain → 默认,
// **没有「更具体的规则优先」这回事**。于是 proxy 里一条 *.apple.com 会把 direct 里
// 的 ocsp.apple.com 整个吃掉:用户以为他给 OCSP 开了直连,实际上一次都没走过。
//
// 这一类与「冗余」的区别不在删不删得掉(都能删),而在要说的话完全不同:
// 那一类是「重复了」,这一类是「你以为它在工作,它没有」。
func TestDirectRuleOverriddenByBroaderProxyRule(t *testing.T) {
	rep := Review(Input{
		Direct: []string{"ocsp.apple.com"},
		Proxy:  []string{"*.apple.com"},
	})

	if rep.OverriddenCount != 1 {
		t.Fatalf("OverriddenCount = %d, want 1;findings=%+v", rep.OverriddenCount, rep.Findings)
	}
	f := rep.Findings[0]
	if f.Class != ClassOverriddenByOppositeKind {
		t.Errorf("Class = %v, want ClassOverriddenByOppositeKind", f.Class)
	}
	if f.Kind != "direct" || f.Rule != "ocsp.apple.com" || f.CoveredBy != "*.apple.com" {
		t.Errorf("finding 指错了对象:%+v", f)
	}
	// 计数绝不合并:同一条不许既算冗余又算失效。
	if rep.ShadowedByUserCount != 0 {
		t.Errorf("ShadowedByUserCount = %d,这一类被并进了同表冗余", rep.ShadowedByUserCount)
	}
}

// **反方向不成立。** proxy 先查,所以一条 proxy 规则不会被更宽的 direct 规则压住 ——
// 它照样生效。把方向搞反会让报告叫用户删掉一条正在工作的强制走隧道规则,
// 那是把流量从隧道里赶出去。
func TestProxyRuleIsNotOverriddenByBroaderDirectRule(t *testing.T) {
	rep := Review(Input{
		Direct: []string{"*.apple.com"},
		Proxy:  []string{"ocsp.apple.com"},
	})
	if rep.OverriddenCount != 0 {
		t.Fatalf("方向搞反了 —— proxy 规则被报成失效:%+v", rep.Findings)
	}
}

// 同一个域名同时写进两张表(手改 YAML 才会有;policy.apply 加一边会删另一边):
// proxy 赢,direct 那条从来没工作过。
func TestSameDomainInBothTablesReportsTheDirectOneDead(t *testing.T) {
	rep := Review(Input{Direct: []string{"*.qq.com"}, Proxy: []string{"*.qq.com"}})
	if rep.OverriddenCount != 1 {
		t.Fatalf("OverriddenCount = %d, want 1:%+v", rep.OverriddenCount, rep.Findings)
	}
	if rep.Findings[0].Kind != "direct" {
		t.Errorf("被判失效的应该是 direct 那条,got %q", rep.Findings[0].Kind)
	}
}
```

- [ ] **Step 2: 跑测试,确认失败**

Run: `go test ./internal/rulereview/ -run 'TestDirectRuleOverridden|TestProxyRuleIsNot|TestSameDomainInBoth' -v`
Expected: FAIL —— `OverriddenCount = 0, want 1`

- [ ] **Step 3: 实现**

`Review` 里在两条 `shadowedByUserFindings` 之后插入:

```go
	findings = append(findings, overriddenFindings(direct, proxy)...)
```

追加:

```go
// overriddenFindings 找出**被另一张表压住、于是从来没生效过**的规则。
//
// **只有一个方向。** route.Router.Explain 先查 UserProxy 再查 UserDirect
// (internal/route/explain.go:78),中间没有任何按具体程度排序的步骤,所以:
//   · direct 规则被更宽的 proxy 规则吃掉 ⇒ 它永远拿不到 Direct 判定;
//   · proxy 规则**不会**被更宽的 direct 规则吃掉 —— 它先被查到,照样生效。
//
// 把方向搞反的后果不是少报一条,是叫用户删掉一条正在工作的强制走隧道规则,
// 也就是把流量从隧道里赶出去。这个顺序**不许靠读代码断言** ——
// internal/supervisor/ruleprecedence_test.go 用真的 route.Router 证明它。
func overriddenFindings(direct, proxy []domainRule) []Finding {
	if len(proxy) == 0 {
		return nil
	}
	raw := make([]string, 0, len(proxy))
	for _, p := range proxy {
		raw = append(raw, p.raw)
	}
	proxySet := route.NewDomainSet(raw)

	var out []Finding
	for _, d := range direct {
		covering, ok := proxySet.MatchRule(d.norm)
		if !ok {
			continue
		}
		out = append(out, Finding{
			Kind:      "direct",
			Rule:      d.raw,
			Class:     ClassOverriddenByOppositeKind,
			Summary:   "被 proxy 表里更宽的一条压住,从来没有生效过——bx 先查 proxy 再查 direct,没有「更具体的优先」。要它生效就得收窄或删掉压住它的那一条。",
			CoveredBy: covering,
		})
	}
	return out
}
```

同时把 `shadowedByUserFindings("direct", direct)` 产出的结果排除掉已被判 override 的规则?
**不需要** —— 两类判据的输入不同(同表 vs 跨表),一条规则可以同时既被同表更宽的一条覆盖、
又被 proxy 压住,那时两条 finding 都对,说的是两件不同的事。

- [ ] **Step 4: 跑测试,确认通过**

Run: `go test ./internal/rulereview/ -v`
Expected: PASS

- [ ] **Step 5: 写「判定顺序」的真路由证明**

创建 `internal/supervisor/ruleprecedence_test.go`:

```go
package supervisor

import (
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/route"
)

// internal/rulereview 的 overriddenFindings 建立在一个关于**别人的实现**的断言上:
// route.Router 先查 UserProxy 再查 UserDirect,中间没有按具体程度排序的步骤。
//
// 那个断言今天是对的。哪天它改成「最长后缀优先」,该转红的是这条测试 ——
// 而不是让用户读到一份说「你这条 direct 规则从来没生效」的报告,然后照着去删掉
// 那条**正在工作的** proxy 规则。
//
// 这里刻意用真的 BuildRouter + 真的 Explain,不复制判定。
func TestProxyRuleBeatsMoreSpecificDirectRule(t *testing.T) {
	cfg := &config.Config{
		Rules: []config.Rule{{
			Proxy:  []string{"*.apple.com"},
			Direct: []string{"ocsp.apple.com"},
		}},
	}
	r, err := BuildRouter(cfg, nil, nil)
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}

	decision, reason := r.Explain(route.Meta{Domain: "ocsp.apple.com"})
	if decision != route.Proxy {
		t.Fatalf("decision = %v, want Proxy —— 判定顺序变了,rulereview.overriddenFindings "+
			"的整个前提(proxy 先查、没有最长后缀优先)已经不成立,必须连同它一起改", decision)
	}
	if reason.Source != route.SourceUserProxy {
		t.Errorf("Source = %v, want SourceUserProxy", reason.Source)
	}
	if reason.Rule != "*.apple.com" {
		t.Errorf("Rule = %q, want %q —— 归因指向的不是压住它的那一条", reason.Rule, "*.apple.com")
	}
}

// 反方向:更宽的 direct 规则**压不住** proxy 规则,后者照常生效。
func TestBroaderDirectRuleDoesNotBeatProxyRule(t *testing.T) {
	cfg := &config.Config{
		Rules: []config.Rule{{
			Direct: []string{"*.apple.com"},
			Proxy:  []string{"ocsp.apple.com"},
		}},
	}
	r, err := BuildRouter(cfg, nil, nil)
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}
	decision, reason := r.Explain(route.Meta{Domain: "ocsp.apple.com"})
	if decision != route.Proxy {
		t.Fatalf("decision = %v, want Proxy —— 若这里变成 Direct,说明宽 direct 能压住 proxy,"+
			"那 rulereview 必须**反过来**也报一类,否则会漏掉真正失效的规则", decision)
	}
	if reason.Rule != "ocsp.apple.com" {
		t.Errorf("Rule = %q, want %q", reason.Rule, "ocsp.apple.com")
	}
}
```

- [ ] **Step 6: 跑真路由证明**

Run: `go test ./internal/supervisor/ -run 'TestProxyRuleBeats|TestBroaderDirectRule' -v`
Expected: PASS(两条)

若 `BuildRouter(cfg, nil, nil)` 因空 china 列表报错,改传 `[]string{}`;若 `config.Config`
需要更多字段才能建 Router,按报错补最小字段并在测试里注明「这些字段与判定顺序无关」。

- [ ] **Step 7: 提交**

```bash
bash scripts/verify.sh --quick
git add internal/rulereview/ internal/supervisor/ruleprecedence_test.go
git commit -m "$(cat <<'EOF'
feat(rulereview): 被反向更宽规则压住的规则 —— 它从来没工作过

spec 只说了「冗余」。把两张表放一起比时掉出第四类:route.Router.Explain 先查
UserProxy 再查 UserDirect,**没有「更具体的优先」这回事**,于是 proxy 里一条
*.apple.com 会把 direct 里的 ocsp.apple.com 整个吃掉——用户以为他给 OCSP 开了
直连,实际一次都没走过。

与冗余的区别不在删不删得掉(都能删),而在要说的话完全不同,所以独立成类、
独立计数。

**只有一个方向**,且不许靠读代码断言:internal/supervisor/ruleprecedence_test.go
用真的 BuildRouter + Explain 两个方向各证一次。方向搞反的后果不是少报一条,
是叫用户删掉一条正在工作的强制走隧道规则,把流量从隧道里赶出去。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: 被内建 china 列表覆盖 —— 以及那个当天就被真机打脸的 mode 门

**Files:**
- Modify: `internal/rulereview/review.go`
- Test: `internal/rulereview/review_test.go`
- Create: `internal/rulereview/fixture_test.go`

**Interfaces:**
- Consumes: Task 3 的全部;`Input.China *route.DomainSet`、`Input.GlobalProxy bool`、`Input.ChinaSkipReason string`
- Produces: `func shadowedByBuiltinFindings(kind string, rules []domainRule, china *route.DomainSet) []Finding`;
  `Review` 现在会正确填 `Report.BuiltinListChecked` / `BuiltinSkipReason`

- [ ] **Step 1: 写失败测试(含 mode 门与 fixture)**

追加到 `internal/rulereview/review_test.go`:

```go
import "github.com/getbx/bx/internal/route"

// 非 global 下,被内建 china 列表覆盖的手写规则确实没有作用。
func TestShadowedByBuiltinChinaList(t *testing.T) {
	china := route.NewDomainSet([]string{"qq.com", "taobao.com"})
	rep := Review(Input{Direct: []string{"*.qq.com", "example.org"}, China: china})

	if rep.ShadowedByBuiltinCount != 1 {
		t.Fatalf("ShadowedByBuiltinCount = %d, want 1:%+v", rep.ShadowedByBuiltinCount, rep.Findings)
	}
	if !rep.BuiltinListChecked {
		t.Error("BuiltinListChecked = false,而这一轮明明比过了")
	}
	f := rep.Findings[0]
	if f.Class != ClassShadowedByBuiltinList || f.Rule != "*.qq.com" {
		t.Errorf("finding 不对:%+v", f)
	}
	if f.CoveredBy != "qq.com" {
		t.Errorf("CoveredBy = %q, want %q(内建列表里那一行的原文)", f.CoveredBy, "qq.com")
	}
}

// **这是本功能最贵的一条测试。**
//
// spec 写完当天的真机实测:拿生产的 route.DomainSet + 内嵌 china 列表跑项目所有者
// 的 24 条规则,报出 22 条「被 china 列表覆盖」——而他的机器是 **global**,
// china 列表整个不生效,那 22 条全都在干活。照着删会让 22 个域名改走隧道。
//
// 判据本身没错,错在没读 mode。而且要读的是 config.Global,**不是 config.Mode**
// (后者取值只有 host|router,与这件事无关)。
func TestGlobalModeSuppressesEveryBuiltinListFinding(t *testing.T) {
	china := route.NewDomainSet(ownerRulesChinaListFixture())
	in := Input{Direct: ownerRulesFixture(), China: china}

	split := Review(in)
	if split.ShadowedByBuiltinCount == 0 {
		t.Fatal("fixture 在非 global 下一条都没报 —— 这条测试失去了它要守的东西,请修 fixture")
	}

	in.GlobalProxy = true
	global := Review(in)

	if global.ShadowedByBuiltinCount != 0 {
		t.Fatalf("global 下报出了 %d 条「被 china 列表覆盖」—— 那些规则全都在干活,"+
			"照着删会把 %d 个域名改走隧道。这正是 spec 当天被真机抓到的那个 bug",
			global.ShadowedByBuiltinCount, global.ShadowedByBuiltinCount)
	}
	// **不是「零条」,是「没查」。** 两者必须分得开,否则报告在说一句自洽的假话。
	if global.BuiltinListChecked {
		t.Error("global 下 BuiltinListChecked 仍是 true —— 「没查」被报成了「查了没有」")
	}
	if global.BuiltinSkipReason == "" {
		t.Error("没查却不说为什么 —— 用户无从判断这份报告漏了什么")
	}
	// 模式无关的那三类一条都不许少。
	if global.ShadowedByUserCount != split.ShadowedByUserCount {
		t.Errorf("global 把模式无关的同表冗余也压掉了:%d vs %d",
			global.ShadowedByUserCount, split.ShadowedByUserCount)
	}
	if global.RiskyCount != split.RiskyCount {
		t.Errorf("global 把安全告警也压掉了:%d vs %d", global.RiskyCount, split.RiskyCount)
	}
}

// 拿不到列表(调用方读不到、或用户用 lists.china_domain 换了自己的一份)时同样是
// 「没查」,而不是「零条」。
func TestNilChinaListIsNotChecked(t *testing.T) {
	rep := Review(Input{
		Direct:          []string{"*.qq.com"},
		ChinaSkipReason: "你在 lists.china_domain 里换了自己的列表,内建那份不作数",
	})
	if rep.BuiltinListChecked {
		t.Error("没有列表却报成查过了")
	}
	if rep.ShadowedByBuiltinCount != 0 {
		t.Errorf("没有列表却报出了 %d 条", rep.ShadowedByBuiltinCount)
	}
	if rep.BuiltinSkipReason == "" {
		t.Error("理由被吞掉了")
	}
}
```

- [ ] **Step 2: 写 fixture**

创建 `internal/rulereview/fixture_test.go`:

```go
package rulereview

// ownerRulesFixture 是项目所有者那份配置的**代表性形状**:24 条手写直连规则,
// 大多是国内大厂域名、其中一条是公有云开放子域(*.myqcloud.com)、若干条被自己
// 更宽的一条盖住(*.apple.com 系)。
//
// 它不是逐字拷贝(那份配置是 0600 root,而且真机上的东西不该进仓库),
// 但**那三个数量级关系是真的**:大多数条目命中 china 列表、有一条危险直连、
// 有一小撮同表冗余。spec 里那次实测的 24 / 22 / 11 就是这个形状。
func ownerRulesFixture() []string {
	return []string{
		"*.qq.com", "*.qpic.cn", "*.qlogo.cn", "*.gtimg.cn", "*.myqcloud.com",
		"*.taobao.com", "*.tmall.com", "*.alicdn.com", "*.aliyun.com",
		"*.baidu.com", "*.bdstatic.com", "*.bilibili.com", "*.hdslb.com",
		"*.163.com", "*.126.net", "*.zhihu.com", "*.zhimg.com",
		"*.jd.com", "*.360buyimg.com", "*.weibo.com", "*.sinaimg.cn",
		"*.apple.com", "ocsp.apple.com", "*.push.apple.com",
	}
}

// ownerRulesChinaListFixture 是一份**够用的** china 列表切片:覆盖上面大多数条目,
// 但刻意不含 apple.com 系(苹果的域名不在 china 直连列表里)。
//
// 用切片而不是真的内嵌列表,是为了让这条测试的**输入是可读的** ——
// 拿 12165 条真列表跑,断言失败时没人看得出为什么。真列表那一侧由
// internal/cli 的组装测试覆盖(Task 5)。
func ownerRulesChinaListFixture() []string {
	return []string{
		"qq.com", "qpic.cn", "qlogo.cn", "gtimg.cn", "myqcloud.com",
		"taobao.com", "tmall.com", "alicdn.com", "aliyun.com",
		"baidu.com", "bdstatic.com", "bilibili.com", "hdslb.com",
		"163.com", "126.net", "zhihu.com", "zhimg.com",
		"jd.com", "360buyimg.com", "weibo.com", "sinaimg.cn",
	}
}
```

- [ ] **Step 3: 跑测试,确认失败**

Run: `go test ./internal/rulereview/ -run 'TestShadowedByBuiltin|TestGlobalMode|TestNilChinaList' -v`
Expected: FAIL —— `ShadowedByBuiltinCount = 0, want 1`

- [ ] **Step 4: 实现**

`Review` 改成:

```go
func Review(in Input) Report {
	direct := domainRules(in.Direct)
	proxy := domainRules(in.Proxy)

	var findings []Finding
	findings = append(findings, riskyFindings(direct)...)
	findings = append(findings, shadowedByUserFindings("direct", direct)...)
	findings = append(findings, shadowedByUserFindings("proxy", proxy)...)
	findings = append(findings, overriddenFindings(direct, proxy)...)

	// **global 下 china 列表整个不生效**,那时「被它覆盖」这个结论是错的 ——
	// spec 写完当天的真机实测报出 22 条,而那 22 条全都在干活。
	// 读的是 config.Global,不是 config.Mode(后者只有 host|router)。
	//
	// 压制的**只有这一类**:另外三类模式无关,一条都不许少。
	checked := false
	skip := in.ChinaSkipReason
	switch {
	case in.GlobalProxy:
		skip = "global 模式下内建 china 列表整个不生效,这一类没有比对"
	case in.China == nil:
		if skip == "" {
			skip = "没拿到内建 china 列表,这一类没有比对"
		}
	default:
		checked = true
		skip = ""
		findings = append(findings, shadowedByBuiltinFindings("direct", direct, in.China)...)
		findings = append(findings, shadowedByBuiltinFindings("proxy", proxy, in.China)...)
	}

	return NewReport(findings, checked, skip)
}

// shadowedByBuiltinFindings 找出已经被内建 china 直连列表覆盖的手写规则。
//
// 调用方负责保证这一类该不该跑(见 Review 的 gating)——本函数不认识 mode。
func shadowedByBuiltinFindings(kind string, rules []domainRule, china *route.DomainSet) []Finding {
	var out []Finding
	for _, r := range rules {
		covering, ok := china.MatchRule(r.norm)
		if !ok {
			continue
		}
		summary := "已在内建 china 直连列表里,这条手写的没有额外作用。"
		if kind == "proxy" {
			// proxy 规则命中 china 列表不是冗余 —— 它是**故意的例外**:
			// 用户就是要把一个内建列表判直连的域名扳回隧道。说反了会让他删掉它。
			summary = "内建 china 列表把它判为直连,而你这条把它扳回隧道——这是生效中的例外,不是冗余。"
		}
		out = append(out, Finding{
			Kind:      kind,
			Rule:      r.raw,
			Class:     ClassShadowedByBuiltinList,
			Summary:   summary,
			CoveredBy: covering,
		})
	}
	return out
}
```

> **注意上面 proxy 那一支。** 一条 proxy 规则命中 china 列表**不是冗余**:
> `Explain` 先查 UserProxy,内建列表根本轮不到,所以那条规则**正在工作**。
> 把它和 direct 那一支说成同一句话,就是叫用户删掉一条正在把流量拉回隧道的规则。
> 它仍归在同一类里(同一次比对、同一个计数),但**说的话必须相反**。

- [ ] **Step 5: 补一条测试钉住 proxy 那一支的措辞**

追加到 `review_test.go`:

```go
// proxy 规则命中 china 列表**不是冗余**:Explain 先查 UserProxy,内建列表轮不到,
// 那条规则正在工作。两支说同一句话,就是叫用户删掉一条正在把流量拉回隧道的规则。
func TestProxyRuleHittingChinaListIsCalledAnException(t *testing.T) {
	china := route.NewDomainSet([]string{"qq.com"})
	rep := Review(Input{Proxy: []string{"*.qq.com"}, China: china})

	if rep.ShadowedByBuiltinCount != 1 {
		t.Fatalf("want 1 条:%+v", rep.Findings)
	}
	s := rep.Findings[0].Summary
	if !strings.Contains(s, "例外") {
		t.Errorf("proxy 那一支没有说清它是生效中的例外,got %q", s)
	}
	if strings.Contains(s, "没有额外作用") {
		t.Errorf("proxy 那一支照抄了 direct 的措辞 —— 会让用户删掉一条正在工作的规则:%q", s)
	}
}
```

(文件顶部 import 加 `"strings"`。)

- [ ] **Step 6: 跑测试,确认通过**

Run: `go test ./internal/rulereview/ -v`
Expected: PASS(全部)

- [ ] **Step 7: 变异验证 mode 门是承重的**

把 `case in.GlobalProxy:` 那一支临时删掉(让 global 也走 default 分支)。

Run: `go test ./internal/rulereview/ -run TestGlobalModeSuppressesEveryBuiltinListFinding -v`
Expected: FAIL,信息含「那些规则全都在干活」

改回来,重跑:
Expected: PASS

- [ ] **Step 8: 提交**

```bash
bash scripts/verify.sh --quick
git add internal/rulereview/
git commit -m "$(cat <<'EOF'
feat(rulereview): 被内建 china 列表覆盖 —— 以及那个当天就被真机打脸的 mode 门

spec 写完当天的实测:拿生产的 DomainSet + 内嵌列表跑项目所有者 24 条规则,报出
22 条「被 china 列表覆盖」,而他的机器是 global —— china 列表整个不生效,那 22 条
全都在干活,照着删会让 22 个域名改走隧道。判据没错,错在没读 mode。

门读的是 config.Global,**不是 config.Mode**(后者取值只有 host|router,与
global/split 无关)。压制的只有这一类,另外三类模式无关、一条都不少 —— 由测试
分别断言。变异验证过:去掉这个门,那条测试立刻转红。

「没查」与「查了没有」分得开(BuiltinListChecked + SkipReason,前者刻意无
omitempty):global 下报「0 条冗余」是一句自洽的假话。

proxy 规则命中 china 列表**不是冗余**而是生效中的例外(Explain 先查 UserProxy,
内建列表轮不到),两支措辞相反,另有一条测试钉住 —— 说反了就是叫用户删掉一条
正在把流量拉回隧道的规则。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: 接到 `bx doctor`(文本 + `--json`)

**Files:**
- Create: `internal/cli/rulereview.go`
- Create: `internal/cli/rulereview_test.go`
- Modify: `internal/cli/cli.go`(`doctorAction` 约 1556 行后、`collectClientDoctorWith` 约 2270 行后)

**Interfaces:**
- Consumes: `rulereview.Review`、`rulereview.Report`、`embedded.ChinaDomain() []byte`、
  `config.Config{Global bool, Mode string, Lists config.Lists, Rules []config.Rule}`
  (**直接读 `cfg.Rules`,不走 `setup.ListRules`** —— doctor 已经 `config.Parse` 过一次,
  再按路径读一遍 YAML 会出现「解析器读到的」与「体检读到的」是两份东西这种可能)
- Produces:
  - `func buildRuleReviewInput(cfg *config.Config, china []byte) rulereview.Input`
  - `func ruleReviewDoctorLines(rep rulereview.Report) []doctorFinding`
  - `type doctorFinding struct { Status, Key, Value, Hint string }`

- [ ] **Step 1: 写失败测试**

创建 `internal/cli/rulereview_test.go`:

```go
package cli

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/rulereview"
)

// 组装是本仓库全部事故的所在地,所以它必须是一个能单测的纯函数,
// 而不是散在 doctorAction 里的十几行。
func TestBuildRuleReviewInputReadsGlobalNotMode(t *testing.T) {
	cfg := &config.Config{
		Global: true,
		Mode:   "host", // **无关字段**,放在这里正是为了钉住别把它当 global 读
		Rules:  []config.Rule{{Direct: []string{"*.qq.com"}, Proxy: []string{"*.google.com"}}},
	}
	in := buildRuleReviewInput(cfg, embedded.ChinaDomain())

	if !in.GlobalProxy {
		t.Fatal("GlobalProxy = false,而 cfg.Global = true —— 读错字段就是 spec 当天那个 bug")
	}
	if len(in.Direct) != 1 || in.Direct[0] != "*.qq.com" {
		t.Errorf("Direct = %v", in.Direct)
	}
	if len(in.Proxy) != 1 || in.Proxy[0] != "*.google.com" {
		t.Errorf("Proxy = %v", in.Proxy)
	}
}

// config.Mode = "router" 与 global/split 无关,绝不能被当成 global。
func TestRouterModeIsNotGlobal(t *testing.T) {
	cfg := &config.Config{Mode: "router", Rules: []config.Rule{{Direct: []string{"*.qq.com"}}}}
	if buildRuleReviewInput(cfg, embedded.ChinaDomain()).GlobalProxy {
		t.Fatal("mode=router 被当成了 global —— 那是另一个字段(host|router)")
	}
}

// 用户在 lists.china_domain 里换了自己的列表时,拿内嵌那份比是拿错了参照物,
// 结论会指着一条实际不被覆盖的规则说「可以删」。那时必须**不比**,并说清为什么。
func TestUserSuppliedChinaListDisablesTheBuiltinComparison(t *testing.T) {
	cfg := &config.Config{
		Lists: config.Lists{ChinaDomain: "/var/lib/bx/my-list.txt"},
		Rules: []config.Rule{{Direct: []string{"*.qq.com"}}},
	}
	in := buildRuleReviewInput(cfg, embedded.ChinaDomain())
	if in.China != nil {
		t.Fatal("用户换了自己的列表,却仍拿内嵌那份去比 —— 参照物是错的")
	}
	if in.ChinaSkipReason == "" {
		t.Fatal("不比却不说为什么")
	}
	if rep := rulereview.Review(in); rep.BuiltinListChecked {
		t.Error("BuiltinListChecked = true,而根本没比")
	}
}

// rules[] 有多个条目时要全部摊平(与 setup.ListRules 的语义一致)。
func TestBuildRuleReviewInputFlattensEveryRulesEntry(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{
		{Direct: []string{"a.com"}},
		{Direct: []string{"b.com"}, Proxy: []string{"c.com"}},
	}}
	in := buildRuleReviewInput(cfg, nil)
	if len(in.Direct) != 2 || len(in.Proxy) != 1 {
		t.Fatalf("摊平不完整:Direct=%v Proxy=%v", in.Direct, in.Proxy)
	}
}

// **干净配置必须一个字都不说。** 只在真有问题时才占地方,是这套东西不被训练成
// 噪声的前提(与按规则失败计数同一条纪律)。
func TestCleanConfigProducesNoDoctorLines(t *testing.T) {
	rep := rulereview.NewReport(nil, true, "")
	if lines := ruleReviewDoctorLines(rep); len(lines) != 0 {
		t.Fatalf("干净配置说了 %d 行话:%+v", len(lines), lines)
	}
}

// 危险那一条是**安全结论**,必须是 warn 而不是 info,而且要点名到规则原文。
func TestRiskyRuleGetsAWarnLineNamingTheRule(t *testing.T) {
	rep := rulereview.NewReport([]rulereview.Finding{{
		Kind: "direct", Rule: "*.myqcloud.com", Class: rulereview.ClassRisky, Summary: "…",
	}}, true, "")

	lines := ruleReviewDoctorLines(rep)
	if len(lines) == 0 {
		t.Fatal("危险规则一个字都没说")
	}
	var found bool
	for _, l := range lines {
		if strings.Contains(l.Value, "*.myqcloud.com") {
			found = true
			if l.Status != "warn" {
				t.Errorf("危险直连的状态是 %q,而它是安全结论,必须 warn", l.Status)
			}
		}
	}
	if !found {
		t.Error("没有点名到规则原文 —— 用户无从知道是哪一条")
	}
}

// 「没查」不许长得像「零条」。
func TestNotCheckedBuiltinListSaysSo(t *testing.T) {
	rep := rulereview.NewReport(nil, false, "global 模式下内建 china 列表整个不生效,这一类没有比对")
	lines := ruleReviewDoctorLines(rep)
	var said bool
	for _, l := range lines {
		if strings.Contains(l.Value, "没有比对") || strings.Contains(l.Value, "不生效") {
			said = true
		}
	}
	if !said {
		t.Fatalf("「没查」被静默成了「没问题」:%+v", lines)
	}
}
```

- [ ] **Step 2: 跑测试,确认失败**

Run: `go test ./internal/cli/ -run 'TestBuildRuleReview|TestRouterModeIsNot|TestUserSuppliedChina|TestCleanConfig|TestRiskyRuleGets|TestNotCheckedBuiltin' -v`
Expected: FAIL —— `undefined: buildRuleReviewInput`

- [ ] **Step 3: 写 `internal/cli/rulereview.go`**

```go
package cli

import (
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/rulereview"
)

// doctorFinding 是一行 doctor 输出的三段式,与 doctorLine / rep.addCheck 的形参同构。
// 单独成型是为了让「说什么」可以被单测,而「怎么打印」留在 doctorAction 里。
type doctorFinding struct {
	Status string // ok | warn | info | hint
	Key    string
	Value  string
	Hint   string
}

// buildRuleReviewInput 把一份 config 摊成体检的原料。
//
// **两处容易读错的字段,都由测试钉着:**
//  1. global 取 cfg.Global(yaml `global:`)。cfg.Mode 是另一个东西,取值只有
//     host|router;spec 当天的真机 bug 就是拿错列表比,而拿错 mode 是同一形状。
//  2. 用户在 lists.china_domain 里换了自己的列表时,内嵌那份**不是**参照物 ——
//     那时不比,并说清为什么。拿错参照物会指着一条实际没被覆盖的规则说「可以删」。
func buildRuleReviewInput(cfg *config.Config, china []byte) rulereview.Input {
	in := rulereview.Input{GlobalProxy: cfg.Global}
	for _, r := range cfg.Rules {
		in.Direct = append(in.Direct, r.Direct...)
		in.Proxy = append(in.Proxy, r.Proxy...)
	}
	switch {
	case cfg.Lists.ChinaDomain != "":
		in.ChinaSkipReason = fmt.Sprintf("你在 lists.china_domain 指了自己的列表(%s),"+
			"内嵌那份不是参照物,这一类没有比对", cfg.Lists.ChinaDomain)
	case len(china) == 0:
		in.ChinaSkipReason = "拿不到内建 china 列表,这一类没有比对"
	default:
		in.China = route.NewDomainSet(chinaDomainPatterns(china))
	}
	return in
}

// chinaDomainPatterns 按 supervisor 侧 readLines 的同一规则拆行:去空白、跳注释与空行。
func chinaDomainPatterns(raw []byte) []string {
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// ruleReviewDoctorLines 把体检报告翻成 doctor 的行。
//
// **干净就一个字都不说。** 只在真有问题时才占地方,是这套东西不被训练成噪声的前提
// (与按规则失败计数同一条纪律)。唯一的例外是「没查」——那必须说,因为静默的
// 「没查」与「没问题」在用户眼里长得一模一样。
func ruleReviewDoctorLines(rep rulereview.Report) []doctorFinding {
	var out []doctorFinding

	for _, f := range rep.Findings {
		if f.Class != rulereview.ClassRisky {
			continue
		}
		// 安全结论,warn 而不是 info,并且点名到配置里那一行的原文。
		out = append(out, doctorFinding{
			Status: "warn",
			Key:    "risky direct rule",
			Value:  fmt.Sprintf("%s —— %s", f.Rule, f.Summary),
			Hint:   fmt.Sprintf("bx direct remove '%s'(改完要 bx down && bx up)", f.Rule),
		})
	}

	if n := rep.OverriddenCount; n > 0 {
		out = append(out, doctorFinding{
			Status: "warn",
			Key:    "rules never in effect",
			Value:  summarizeClass(rep, rulereview.ClassOverriddenByOppositeKind, n, "条 direct 规则被更宽的 proxy 规则压住,从来没生效过"),
		})
	}
	if n := rep.ShadowedByUserCount; n > 0 {
		out = append(out, doctorFinding{
			Status: "info",
			Key:    "redundant rules",
			Value:  summarizeClass(rep, rulereview.ClassShadowedByUserRule, n, "条被你自己更宽的一条覆盖,删掉不改变任何流量"),
		})
	}
	if rep.BuiltinListChecked {
		if n := rep.ShadowedByBuiltinCount; n > 0 {
			out = append(out, doctorFinding{
				Status: "info",
				Key:    "covered by builtin list",
				Value:  summarizeClass(rep, rulereview.ClassShadowedByBuiltinList, n, "条与内建 china 列表相关"),
			})
		}
	} else if rep.BuiltinSkipReason != "" {
		// **「没查」不许静默。** 它与「查了没有」在用户眼里长得一样,
		// 而两者的差别正是这个功能最贵的那个教训。
		out = append(out, doctorFinding{
			Status: "info",
			Key:    "builtin list check",
			Value:  "未检查:" + rep.BuiltinSkipReason,
		})
	}
	return out
}

// summarizeClass 打出「N 条 + 前三条点名」。全部列出来会把 doctor 淹掉,
// 一条不点名又等于没说 —— 折中是给数字 + 够他去配置里搜的那几条原文。
func summarizeClass(rep rulereview.Report, class rulereview.Class, n int, tail string) string {
	var names []string
	for _, f := range rep.Findings {
		if f.Class != class {
			continue
		}
		names = append(names, fmt.Sprintf("%s ← %s", f.Rule, f.CoveredBy))
		if len(names) == 3 {
			break
		}
	}
	s := fmt.Sprintf("%d %s:%s", n, tail, strings.Join(names, "、"))
	if n > len(names) {
		s += fmt.Sprintf(" 等 %d 条", n)
	}
	return s
}
```

- [ ] **Step 4: 跑测试,确认通过**

Run: `go test ./internal/cli/ -run 'TestBuildRuleReview|TestRouterModeIsNot|TestUserSuppliedChina|TestCleanConfig|TestRiskyRuleGets|TestNotCheckedBuiltin' -v`
Expected: PASS(6 条)

- [ ] **Step 5: 接进 `doctorAction`(文本路径)**

在 `internal/cli/cli.go` 的 `doctorAction` 里,`cfg, err := config.Parse(b)` 成功那一支的
末尾(现有 `doctorProbe(...)` 那一段之后、`}` 之前)插入:

```go
			for _, l := range ruleReviewDoctorLines(rulereview.Review(buildRuleReviewInput(cfg, embedded.ChinaDomain()))) {
				doctorLine(l.Status, l.Key, l.Value)
				if l.Hint != "" {
					doctorLine("hint", l.Key, l.Hint)
				}
			}
```

在 `collectClientDoctorWith` 的对应位置(`rep.addReport(probeCheck(...))` 之后、
同一个 `else` 块内)插入:

```go
			for _, l := range ruleReviewDoctorLines(rulereview.Review(buildRuleReviewInput(cfg, embedded.ChinaDomain()))) {
				rep.addCheck(ruleReviewCheckName(l.Key), l.Status, l.Value, l.Hint)
			}
```

在 `internal/cli/rulereview.go` 追加:

```go
// ruleReviewCheckName 把人话 key 换成 JSON 里稳定的 snake_case 名 ——
// agent 与 MCP 按名字取,名字变了就是接口变了。
func ruleReviewCheckName(key string) string {
	return "rule_" + strings.ReplaceAll(key, " ", "_")
}
```

`cli.go` 的 import 加 `"github.com/getbx/bx/internal/embedded"` 与
`"github.com/getbx/bx/internal/rulereview"`(若尚未存在)。

- [ ] **Step 6: 写接线守卫**

追加到 `internal/cli/rulereview_test.go`:

```go
// **接线守卫。** 判据全对而没人调用,与没有这个功能在输出上完全一样 ——
// 本仓库反复栽在这一点上(阶段③a 那次:goroutine 体空转,而断言只证明 channel 会关)。
//
// 这里不查源码文本,而是真的跑一遍两条 doctor 路径,断言那条危险规则出现在输出里。
func TestDoctorSurfacesRiskyRuleOnBothPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "server: brook://example.com:9999?password=x\nrules:\n  - direct:\n      - '*.myqcloud.com'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	rep := collectClientDoctorWith(path, "", time.Second, true, false)

	var found bool
	for _, c := range rep.Checks {
		if strings.Contains(c.Value, "*.myqcloud.com") {
			found = true
			if c.Status != "warn" {
				t.Errorf("JSON 路径上危险规则的状态是 %q,want warn", c.Status)
			}
		}
	}
	if !found {
		t.Fatal("bx doctor --json 里一个字都没提那条危险直连规则 —— 接线没接上")
	}
}
```

> **两处写这条测试之前必须先查清、不许照抄的东西:**
>
> 1. `doctorReport` 里那张 checks 表的**字段名**(上面写的 `rep.Checks` 与
>    `c.Status`/`c.Value` 是按 `rep.addCheck(name, status, value, hint)` 的形参顺序
>    推的,**没有核对过结构体定义**)。先 `grep -n "type doctorReport" -A 20
>    internal/cli/cli.go` 拿到真名再写。**推出来的名字编不过是好事;编得过而字段
>    含义错位才是灾难。**
> 2. 上面那条 yaml fixture 能不能通过 `config.Parse` 与 server link 校验。过不了就
>    换一条真链接 —— **fixture 必须是生产解析器能读的那种**(维护挂起那一期的教训:
>    假 fixture 让每一步都成功,而整条路径从没跑过,断言却条条成立)。
>
> 文件顶部 import 补 `"os"`、`"path/filepath"`、`"time"`。

- [ ] **Step 7: 跑测试 + 变异验证接线守卫**

Run: `go test ./internal/cli/ -run TestDoctorSurfacesRiskyRuleOnBothPaths -v`
Expected: PASS

把 `collectClientDoctorWith` 里新加的那个 for 循环临时注释掉,重跑:
Expected: FAIL —— 「接线没接上」

恢复,重跑:
Expected: PASS

- [ ] **Step 8: 人眼看一遍真输出(不启动 bx)**

```bash
cat > /tmp/bx-rulereview-demo.yaml <<'EOF'
server: brook://example.com:9999?password=x
rules:
  - direct:
      - '*.myqcloud.com'
      - '*.apple.com'
      - ocsp.apple.com
      - '*.qq.com'
    proxy:
      - '*.google.com'
EOF
go run ./cmd/bx doctor --config /tmp/bx-rulereview-demo.yaml --skip-probe
```

Expected:输出里出现 `risky direct rule  *.myqcloud.com`、`redundant rules  1 条…ocsp.apple.com ← *.apple.com`、
以及 `covered by builtin list` 那一行(`*.qq.com`)。**只读,不碰网络。**

再把 `global: true` 加进那份 yaml 重跑:`covered by builtin list` 那一行必须变成
`builtin list check  未检查:global 模式下…`。

- [ ] **Step 9: 提交**

```bash
bash scripts/verify.sh --quick
git add internal/cli/rulereview.go internal/cli/rulereview_test.go internal/cli/cli.go
git commit -m "$(cat <<'EOF'
feat(cli): bx doctor 现在会体检你的规则

判据只长在一条路上、而它要保护的状态可以从别的路进来 —— 这一条把 rulereview
接到 doctor 的两条路径(文本 + --json,后者是 MCP/agent 那一侧)。

组装单独成文件、做成纯函数 buildRuleReviewInput 并单测:本仓库全部事故都在
组装根上。两处最容易读错的字段各有一条测试钉着 —— cfg.Global(不是 cfg.Mode)、
以及用户在 lists.china_domain 换了自己的列表时**不比**(拿错参照物会指着一条
实际没被覆盖的规则说「可以删」)。

干净配置一个字都不说;唯一的例外是「没查」必须说 —— 静默的「没查」与「没问题」
在用户眼里长得一模一样。

接线守卫真的跑一遍 collectClientDoctorWith 并断言那条危险规则出现在输出里,
变异验证过:注释掉接线立刻转红。判据全对而没人调用,与没有这个功能在输出上
完全一样。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: `bx status` 也说一句 —— 只说危险那一类

**Files:**
- Create: `internal/supervisor/riskyrules.go`
- Create: `internal/supervisor/riskyrules_test.go`
- Modify: `internal/supervisor/control.go:515`(形参)、`:566`(`Warnings`)
- Modify: `internal/supervisor/run.go:712`(传参)

**Interfaces:**
- Consumes: `rulereview.Review`、`config.Config`、`stats.Warning{Name, Severity, Detail, Hint}`
- Produces: `func riskyRuleWarnings(cfg *config.Config) []stats.Warning`;
  `serveControlWithPathRecovery` 末尾多一个 `configWarnings []stats.Warning` 形参

**为什么只发危险那一类:** `bx status` 是常驻面板,不是诊断命令。冗余是建议、
对任何成熟配置都不为零,放进去会变墙纸,把真正要紧的那一条一起淹掉(与项目所有者
否掉「Direct rules: N unreachable」常驻红字同一条判断)。危险直连是**安全结论**,
而这个功能的起因正是「他可能永远不会敲 doctor」。

**为什么用 `severity=warn`:** `11338a0` 之后只有 `severity=error` 才把总状态降级。
一条配置建议不该让一台工作正常的机器显示 `Needs Attention` —— 那正是 Tailscale
共存 advisory 当初犯的错。

- [ ] **Step 1: 写失败测试**

创建 `internal/supervisor/riskyrules_test.go`:

```go
package supervisor

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
)

func TestRiskyRuleWarningNamesTheRule(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{{Direct: []string{"*.myqcloud.com", "*.qq.com"}}}}
	ws := riskyRuleWarnings(cfg)
	if len(ws) != 1 {
		t.Fatalf("want 1 条告警,got %d: %+v", len(ws), ws)
	}
	w := ws[0]
	if !strings.Contains(w.Detail, "*.myqcloud.com") {
		t.Errorf("没点名到规则原文:%q", w.Detail)
	}
	// **必须是 warn 不是 error。** error 会把总状态降级成 Needs Attention,
	// 而这是一条配置建议,不是保护失效 —— 那正是 advisory 拉低总状态那个 bug。
	if w.Severity != "warn" {
		t.Errorf("Severity = %q, want warn", w.Severity)
	}
	if w.Hint == "" {
		t.Error("没有给出下一步 —— 一条没有附带动作的常驻告警会变成墙纸")
	}
}

// 干净配置一条都不发。
func TestNoRiskyRuleNoWarning(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{{Direct: []string{"*.qq.com"}}}}
	if ws := riskyRuleWarnings(cfg); len(ws) != 0 {
		t.Fatalf("干净配置发了 %d 条告警:%+v", len(ws), ws)
	}
}

// **status 只发危险那一类。** 冗余是建议、对任何成熟配置都不为零,
// 放进常驻面板会变墙纸,把真正要紧的那一条一起淹掉。
func TestStatusWarningsCarryOnlyTheRiskyClass(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{{
		Direct: []string{"*.apple.com", "ocsp.apple.com"}, // 一条同表冗余 + 无危险
	}}}
	if ws := riskyRuleWarnings(cfg); len(ws) != 0 {
		t.Fatalf("冗余那一类漏进了 status:%+v", ws)
	}
}

// global 不影响这一类 —— 危险直连与分流模式无关。
func TestRiskyRuleWarningIsModeIndependent(t *testing.T) {
	cfg := &config.Config{Global: true, Rules: []config.Rule{{Direct: []string{"*.myqcloud.com"}}}}
	if len(riskyRuleWarnings(cfg)) != 1 {
		t.Fatal("global 下危险直连告警被压掉了 —— 它与分流模式无关")
	}
}
```

- [ ] **Step 2: 跑测试,确认失败**

Run: `go test ./internal/supervisor/ -run 'TestRiskyRuleWarning|TestNoRiskyRule|TestStatusWarningsCarry' -v`
Expected: FAIL —— `undefined: riskyRuleWarnings`

- [ ] **Step 3: 写 `internal/supervisor/riskyrules.go`**

```go
package supervisor

import (
	"fmt"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/rulereview"
	"github.com/getbx/bx/internal/stats"
)

// riskyRuleWarnings 从配置里挑出**危险直连规则**,做成 bx status 的常驻告警。
//
// **只有这一类进 status。** 冗余与失效是建议,对任何成熟配置都不为零,放进常驻
// 面板会变墙纸、把真正要紧的这一条一起淹掉(与项目所有者否掉「Direct rules: N
// unreachable」常驻红字同一条判断)。它们留在 bx doctor 里,那是诊断命令。
//
// severity 取 warn:11338a0 之后只有 error 会把总状态降级成 Needs Attention,
// 而一条配置建议不该让一台工作正常的机器显示需注意。
//
// 与 mode 无关:公有云开放子域的去匿名化风险不因 global/split 而变。
func riskyRuleWarnings(cfg *config.Config) []stats.Warning {
	if cfg == nil {
		return nil
	}
	in := rulereview.Input{GlobalProxy: cfg.Global}
	for _, r := range cfg.Rules {
		in.Direct = append(in.Direct, r.Direct...)
	}
	var out []stats.Warning
	for _, f := range rulereview.Review(in).Findings {
		if f.Class != rulereview.ClassRisky {
			continue
		}
		out = append(out, stats.Warning{
			Name:     "risky_direct_rule",
			Severity: "warn",
			Detail:   fmt.Sprintf("直连白名单里的 %s 是公有云/开放子域平台:任何人都能注册它的子域,用一个子域让你的真实 IP 暴露", f.Rule),
			Hint:     fmt.Sprintf("bx direct remove '%s',然后 bx down && bx up", f.Rule),
		})
	}
	return out
}
```

- [ ] **Step 4: 跑测试,确认通过**

Run: `go test ./internal/supervisor/ -run 'TestRiskyRuleWarning|TestNoRiskyRule|TestStatusWarningsCarry|TestRiskyRuleWarningIsMode' -v`
Expected: PASS(4 条)

- [ ] **Step 5: 接线 —— 形参 + 传参 + `Warnings`**

`internal/supervisor/control.go:515`,在 `probeDial probeDialer` 之后加一个形参:

```go
func serveControlWithPathRecovery(ctx context.Context, c *stats.Counters, t tunnelStatser, server, mode, udpMode string, transportInfo func() (string, []string, string), runtime func() RuntimeState, eng controlEngine, mut mutator, reload func() error, refreshBypass func([]string) (bool, error), shutdown func(), ownerUID uint32, recoverer pathRecoverer, probeDial probeDialer, configWarnings []stats.Warning) (io.Closer, error) {
```

`control.go:566` 那一行改成:

```go
			// 配置派生的告警(危险直连规则)在 Run 里算好一次传进来 ——
			// **不在读状态那条路上重算**:菜单每 2 秒拉一次,而配置在运行期不变
			// (bx 不热重载),重算既浪费又会让 status 说出 Core 此刻并没有在用的那份配置。
			Warnings: append(guard.warnings(), configWarnings...),
```

`internal/supervisor/run.go:712` 的调用末尾补上实参:

```go
		return serveControlWithPathRecovery(ctx, counters, lt, serverHost, proxyMode(global, cfg.Mode), cfg.UDP.Mode, transportInfo, runtimeState, mutEng, mut, reloadRouter, refresh, cancel, uint32(cfg.OwnerUID), recoverer, direct, riskyRuleWarnings(cfg))
```

> `append(guard.warnings(), …)` 若 `guard.warnings()` 返回的是一个会被复用的切片,
> 直接 append 可能改到它的底层数组。**编译通过后先确认 `warnings()` 每次返回新切片**
> (`network_guard_*.go`);若不是,改成显式复制:
> `ws := append([]stats.Warning(nil), guard.warnings()...); ws = append(ws, configWarnings...)`。

- [ ] **Step 6: 写接线守卫**

追加到 `internal/supervisor/riskyrules_test.go`:

```go
// **接线守卫。** 判据全对而没人把它接进 status,与没有这个功能在输出上完全一样。
// 这里不查源码文本 —— 那类守卫在本仓库被绕过过八次 —— 而是断言 Run 传下去的那个
// 参数确实到了 Report.Warnings 里。
//
// 走真的 serveControlWithPathRecovery 太重(要 socket、要 engine),所以退一步:
// 断言 report 组装那一步把 configWarnings 并进了 guard 的告警。若将来 report 的
// 组装被重构,这条测试读不懂它就必须 t.Fatal,而不是静默放行。
func TestConfigWarningsReachTheStatusReport(t *testing.T) {
	guardWarnings := []stats.Warning{{Name: "tailscale", Severity: "warn"}}
	configWarnings := riskyRuleWarnings(&config.Config{
		Rules: []config.Rule{{Direct: []string{"*.myqcloud.com"}}},
	})
	if len(configWarnings) != 1 {
		t.Fatalf("前置断言失败:riskyRuleWarnings 没产出告警,这条守卫会为错误的理由通过")
	}

	merged := append(append([]stats.Warning(nil), guardWarnings...), configWarnings...)
	if len(merged) != 2 {
		t.Fatalf("合并后 %d 条,want 2", len(merged))
	}
	var sawRisky, sawGuard bool
	for _, w := range merged {
		switch w.Name {
		case "risky_direct_rule":
			sawRisky = true
		case "tailscale":
			sawGuard = true
		}
	}
	if !sawRisky || !sawGuard {
		t.Fatalf("合并把一边吃掉了:risky=%v guard=%v", sawRisky, sawGuard)
	}
}
```

> **这条守卫弱于它想守的东西**,并且要在 commit message 里说明:它证明的是合并
> 语义,不是 `run.go:712` 真的传了那个实参。真正钉住后者的是编译器 —— 形参必填,
> 漏传就编不过。**这一点必须靠 Step 7 的手工验证补上。**

- [ ] **Step 7: 手工验证接线(编译期 + 真跑一次 dry-run)**

```bash
go build ./... && go vet ./...
```
Expected: 通过。(形参必填 ⇒ `run.go` 漏传就编不过,这是接线的真凭据。)

再确认没有第二个调用点漏掉:
```bash
grep -rn "serveControlWithPathRecovery(" internal/ --include="*.go"
```
Expected: 一个定义 + 一个调用(`run.go:712`);若测试里还有调用点,一并补参数。

- [ ] **Step 8: 全量 verify + 提交**

```bash
bash scripts/verify.sh
git add internal/supervisor/riskyrules.go internal/supervisor/riskyrules_test.go internal/supervisor/control.go internal/supervisor/run.go
git commit -m "$(cat <<'EOF'
feat(supervisor): bx status 常驻说出危险直连规则(只说这一类)

这个功能的起因是「他可能永远不会敲 doctor」,所以危险直连必须进常驻面板。
**只有这一类进 status**:冗余与失效是建议、对任何成熟配置都不为零,放进去会变
墙纸、把真正要紧的这一条一起淹掉(与否掉「Direct rules: N unreachable」常驻红字
同一条判断)。它们留在 doctor 里,那是诊断命令。

severity 取 warn 不是 error:11338a0 之后只有 error 会把总状态降级成 Needs
Attention,而一条配置建议不该让一台工作正常的机器显示需注意 —— 那正是 Tailscale
共存 advisory 当初犯的错。

告警在 Run 里算好一次传进来,**不在读状态那条路上重算**:菜单每 2 秒拉一次,而
配置在运行期不变(bx 不热重载),重算既浪费又会让 status 说出 Core 此刻并没有在
用的那份配置。

接线的真凭据是编译器(形参必填,run.go 漏传就编不过);那条 Go 测试只证明合并
语义不吃掉任何一边,弱于它想守的东西,已在测试注释里写明。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## 收尾:文档与真机验收

- [ ] **Step 1: 更新 CLAUDE.md**

在「规则可观测与可编辑(2026-08-13)」那一节之后新增一节,要点(照本仓库的密度写):
判据只长在一条路上是这个仓库反复出现的形状;四类各自计数绝不合成;
**mode 门读的是 `cfg.Global` 不是 `cfg.Mode`**、spec 当天真机报 22 条冗余而那 22 条
全在干活;proxy 命中 china 列表是生效中的例外不是冗余;`ClassOverriddenByOppositeKind`
是比 spec 多出来的一类、由 `internal/supervisor/ruleprecedence_test.go` 用真 Router 背书;
status 只发危险那一类且 severity=warn;**第 2 条(死规则)与第 4 条(缺失规则)未做**,
硬前置是跨重启累计计数,另立 spec。

- [ ] **Step 2: 提交文档**

```bash
git add CLAUDE.md
git commit -m "docs: 规则体检(静态那一半)—— 四类判据与两个 mode 陷阱

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

- [ ] **Step 3: 真机验收(交给项目所有者跑,只读,不改网络)**

以下命令**一条都不动网络、不需要重启 bx**:

```bash
sudo bx doctor --skip-probe            # 看规则体检那几行
bx status                              # 看有没有 risky_direct_rule 告警(要 Core 在跑)
bx status --json | jq '.warnings'
```

**要盯的三件事:**
1. `risky direct rule  *.myqcloud.com` 应当出现 —— 这是整个功能的起因。
2. 他的机器是 **global**,所以 `builtin list check` 必须是 **未检查**,
   **绝不能**报出 22 条「被 china 列表覆盖」。报出来就是 spec 当天那个 bug 复现。
3. `redundant rules` 那一行应当在 **11 条上下**(spec 实测的同表冗余数),
   且点名的是 `ocsp.apple.com ← *.apple.com` 这种形状。

**注意 `bx status` 那一条要 Core 在跑;新的告警只有在 Core 重启后才会出现**
(配置派生的告警在 `Run()` 里算一次)。**别为了看它去重启保护** —— 下次他自己
`bx down && bx up` 时自然会有。

---

## 后续(不在本计划内,各自需要独立 spec)

- **死规则(spec §2)** —— 硬前置是跨重启累计的按规则计数。未决问题:谁写
  (`stats.Counters` 在 Core,而 `throughputhistory.go` 那个持久化范例由 Guardian
  的调谐环驱动;Linux/Windows 没有 Guardian)、按什么键(`ruleKey{source, rule}`
  的 `rule` 是配置原文,用户改一个字历史就断)、多久算「死」。
- **缺失规则(spec §4)** —— 依赖同一套数据,且 spec 自己说门槛要严得多、宁可不说。
