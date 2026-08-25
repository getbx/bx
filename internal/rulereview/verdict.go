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
	// ClassDead:这条规则**从来没有命中过一次** —— 不是「本次运行 0 次」,而是
	// 跨重启累计到足够久、足够多次判定之后仍然一次都没有。
	//
	// **加在最后,零值仍然是 ClassRisky。** 顺序是有意义的:插在中间会让所有
	// 已落盘的 JSON 里的数字含义整体平移一位。
	ClassDead
)

func (c Class) String() string {
	switch c {
	case ClassShadowedByUserRule:
		return "shadowed_by_user_rule"
	case ClassOverriddenByOppositeKind:
		return "overridden_by_opposite_kind"
	case ClassShadowedByBuiltinList:
		return "shadowed_by_builtin_list"
	case ClassDead:
		return "dead"
	default:
		return "risky_direct"
	}
}

// MarshalJSON 让 JSON 里是词而不是 0/1/2/3。**今天没有任何东西序列化 Finding/Report**
// (bx doctor --json 消费的是 internal/cli 翻出来的 doctorFinding/checkReport,不是
// 这里的 Class/Finding/Report 本身)——这个方法与上面的 json tag 是为将来那个 agent/MCP
// 直接读 rulereview 的表面准备的,今天不可达。
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

	// BuiltinListSource 在 BuiltinListChecked=true 时说明比对用的是哪一份列表
	// (Core 实时在用的那个文件、用户在 lists.china_domain 指的那个、还是回落用的
	// 内嵌快照)。「已被内建列表覆盖」不说清是哪一份内建列表,正是 wrong-reference-
	// object 那类事故的形状:判据本身没错,读错了输入。空字符串表示调用方没有
	// 经由 internal/cli 那条组装路径(如测试直接手写 Report)。
	BuiltinListSource string `json:"builtin_list_source,omitempty"`
	// DeadCount 是「从来没命中过」的规则条数。**与上面四个并列,永不相加** ——
	// Report 没有 TotalCount,理由见 Class 的注释。
	DeadCount int `json:"dead_count"`
	// DeadChecked 区分「查了,没有」与「根本没查」。**刻意无 omitempty**,
	// 与 BuiltinListChecked 同一条:键缺席会被读成 false 而理由不明。
	//
	// 这一类没查的情况比别的类多得多:三道门槛任一没到、历史拿不到、跟踪表满过,
	// 都是「还不能下结论」而不是「查过了,没有」。
	DeadChecked bool `json:"dead_checked"`
	// DeadSkipReason 在没查时说明为什么。
	DeadSkipReason string `json:"dead_skip_reason,omitempty"`
	// DeadVersionsSpanned 是这段累计跨过的 bx 版本数,让用户对它打折 ——
	// 跨了很多个版本的累计,中间可能有几版的计数行为并不一致。
	DeadVersionsSpanned int `json:"dead_versions_spanned,omitempty"`

	// BuiltinListFallback 标记这次比对**没能读到 Core 实际使用的列表,回落成了
	// 内嵌快照**。单独一个 bool 而不是让消费方去解析 BuiltinListSource 的文字 ——
	// 前者是代码判断用的信号,后者是给人看的措辞,混在一起就是又一次「判据读错输入」。
	BuiltinListFallback bool `json:"builtin_list_fallback,omitempty"`
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
		case ClassDead:
			rep.DeadCount++
		default:
			rep.RiskyCount++
		}
	}
	return rep
}
