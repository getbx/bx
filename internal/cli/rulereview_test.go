package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	in := buildRuleReviewInput(cfg, embedded.ChinaDomain(), nil)

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
	if buildRuleReviewInput(cfg, embedded.ChinaDomain(), nil).GlobalProxy {
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
	in := buildRuleReviewInput(cfg, embedded.ChinaDomain(), nil)
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

// **wrong-reference-object 修复的核心断言。** Core 实际比对的是 dataDir 下落盘、
// 经隧道刷新的 china_domain.txt,不是编进二进制的内嵌快照——两者可能不同(上游
// 删掉一个域名,快照仍带着它)。当那个文件**读得到**时,必须用它比,而不是恒用
// 内嵌快照;且报告要点名用的是"Core 当前实际使用的"那一份,不是含糊的"内建列表"。
func TestBuildRuleReviewInputUsesCoresLiveChinaListWhenReadable(t *testing.T) {
	dir := t.TempDir() // 绝不碰 /var/lib/bx —— 注入路径全由测试自己造
	if err := os.WriteFile(filepath.Join(dir, "china_domain.txt"), []byte("live-only.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 内嵌快照刻意**不含** live-only.example,证明比对确实用的是 live 文件、
	// 不是内嵌快照(用同一个域名两边都有的话,分不清到底比的是哪一份)。
	staleEmbedded := []byte("embedded-only.example\n")

	cfg := &config.Config{
		DataDir: dir,
		Rules:   []config.Rule{{Direct: []string{"*.live-only.example"}}},
	}
	in := buildRuleReviewInput(cfg, staleEmbedded, nil)
	if in.China == nil {
		t.Fatal("China == nil —— live 文件读得到,不该落到没比对")
	}
	if in.ChinaFallback {
		t.Fatal("ChinaFallback = true,而 live 文件读得到,根本没回落")
	}
	if !strings.Contains(in.ChinaSource, "Core 当前实际使用") || !strings.Contains(in.ChinaSource, dir) {
		t.Fatalf("ChinaSource 没说清用的是 Core 实时列表: %q", in.ChinaSource)
	}

	rep := rulereview.Review(in)
	if !rep.BuiltinListChecked {
		t.Fatal("BuiltinListChecked = false")
	}
	if rep.BuiltinListFallback {
		t.Fatal("Report.BuiltinListFallback = true —— 没有回落")
	}

	lines := ruleReviewDoctorLines(rep)
	var found bool
	for _, l := range lines {
		if l.Key != "covered by builtin list" {
			continue
		}
		found = true
		if !strings.Contains(l.Value, "*.live-only.example") {
			t.Errorf("没点名到规则原文: %q", l.Value)
		}
		// 三:finding 文本必须点名用的是哪一份列表。
		if !strings.Contains(l.Value, "source:") || !strings.Contains(l.Value, "Core 当前实际使用") {
			t.Errorf("finding 文本没说清用的是哪份列表: %q", l.Value)
		}
	}
	if !found {
		t.Fatal("live-only.example 命中了 live 文件,却没有产出 covered-by-builtin-list 这一行")
	}
}

// **中间态:读不到 Core 的实时列表(非 root,或 Core 从没跑过 provision)时回落到
// 内嵌快照,而且必须明说回落了。** 一份可能过期的快照仍然有用(总比不比强),
// 但"查了、用的是实时数据"与"查了、用的是可能过期的快照"必须让用户分得清——
// 这正是这次修复要解决的不对称:静默回落会让"covered"这个结论看起来和用
// live 列表算出来的一模一样。
func TestBuildRuleReviewInputFallsBackToEmbeddedWhenCoreListUnreadable(t *testing.T) {
	dir := t.TempDir() // 没有 china_domain.txt —— 模拟 Core 从没 provision 过
	embeddedChina := []byte("fallback-only.example\n")

	cfg := &config.Config{
		DataDir: dir,
		Rules:   []config.Rule{{Direct: []string{"*.fallback-only.example"}}},
	}
	in := buildRuleReviewInput(cfg, embeddedChina, nil)
	if in.China == nil {
		t.Fatal("China == nil —— 该回落到内嵌快照,不该整个跳过比对")
	}
	if !in.ChinaFallback {
		t.Fatal("ChinaFallback = false —— live 文件读不到,这就是回落")
	}
	if in.ChinaSource == "" {
		t.Fatal("ChinaSource 为空 —— 回落也要说清楚回落到了什么")
	}

	rep := rulereview.Review(in)
	if !rep.BuiltinListChecked {
		t.Fatal("BuiltinListChecked = false —— 回落之后仍然比了")
	}
	if !rep.BuiltinListFallback {
		t.Fatal("Report.BuiltinListFallback = false —— 没有把回落状态透传给报告")
	}

	lines := ruleReviewDoctorLines(rep)
	var sawFallbackNotice bool
	for _, l := range lines {
		if l.Key == "builtin list source" && strings.Contains(l.Value, "回落") {
			sawFallbackNotice = true
		}
	}
	if !sawFallbackNotice {
		t.Fatalf("回落发生了,却没有一行说「回落」:%+v", lines)
	}
}

// **第三种结局:用户自己指定的列表读不到,不回落、不比,只说明为什么。**
// 回落内嵌快照在这里是错的——那是拿用户明确换掉的参照物硬凑数,不是
// "可能过期的同一份东西"。
func TestBuildRuleReviewInputUserOverrideUnreadableDoesNotFallBack(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.txt")
	embeddedChina := []byte("embedded.example\n")

	cfg := &config.Config{
		DataDir: dir,
		Lists:   config.Lists{ChinaDomain: missing},
		Rules:   []config.Rule{{Direct: []string{"*.embedded.example"}}},
	}
	in := buildRuleReviewInput(cfg, embeddedChina, nil)
	if in.China != nil {
		t.Fatal("China != nil —— 用户换了自己的列表且读不到,不该回落内嵌快照")
	}
	if in.ChinaFallback {
		t.Fatal("ChinaFallback = true —— 用户覆盖的情形不许标成回落(那会暗示比过)")
	}
	if in.ChinaSkipReason == "" {
		t.Fatal("不比却没说为什么")
	}
	if !strings.Contains(in.ChinaSkipReason, missing) {
		t.Errorf("ChinaSkipReason 没点名是哪个路径读不到: %q", in.ChinaSkipReason)
	}

	rep := rulereview.Review(in)
	if rep.BuiltinListChecked {
		t.Fatal("BuiltinListChecked = true —— 用户覆盖且读不到时不该比")
	}

	lines := ruleReviewDoctorLines(rep)
	var said bool
	for _, l := range lines {
		if l.Key == "builtin list check" && strings.Contains(l.Value, missing) {
			said = true
		}
	}
	if !said {
		t.Fatalf("没查却没说清是哪个用户指定的路径读不到:%+v", lines)
	}
}

// **用户指定的列表读得到时,必须真的拿它比**——「解析成 Core 实际会用的路径」
// 这条规则对用户覆盖和默认路径一视同仁,不是只对默认路径生效。这是三种结局之外
// 容易漏掉的第四种组合(读得到 + 有覆盖),此前没有测试专门盯着它。
func TestBuildRuleReviewInputUsesUserOverrideChinaListWhenReadable(t *testing.T) {
	dir := t.TempDir()
	overridePath := filepath.Join(dir, "my-own-list.txt")
	if err := os.WriteFile(overridePath, []byte("override-only.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 内嵌快照与默认路径都刻意留空/不含这个域名,证明命中的确实是用户指定的文件。
	embeddedChina := []byte("embedded-only.example\n")

	cfg := &config.Config{
		DataDir: t.TempDir(), // 默认路径下没有 china_domain.txt,避免误判成走了默认路径
		Lists:   config.Lists{ChinaDomain: overridePath},
		Rules:   []config.Rule{{Direct: []string{"*.override-only.example"}}},
	}
	in := buildRuleReviewInput(cfg, embeddedChina, nil)
	if in.China == nil {
		t.Fatal("China == nil —— 用户指定的列表读得到,不该跳过比对")
	}
	if in.ChinaFallback {
		t.Fatal("ChinaFallback = true —— 用户指定的列表读得到,根本没有回落")
	}
	if !strings.Contains(in.ChinaSource, "lists.china_domain") || !strings.Contains(in.ChinaSource, overridePath) {
		t.Fatalf("ChinaSource 没说清用的是用户自己指定的列表: %q", in.ChinaSource)
	}

	rep := rulereview.Review(in)
	if !rep.BuiltinListChecked {
		t.Fatal("BuiltinListChecked = false —— 用户指定的列表读得到,该比")
	}

	lines := ruleReviewDoctorLines(rep)
	var found bool
	for _, l := range lines {
		if l.Key == "covered by builtin list" && strings.Contains(l.Value, "*.override-only.example") {
			found = true
			if !strings.Contains(l.Value, overridePath) {
				t.Errorf("finding 文本没点名用的是用户自己那份列表: %q", l.Value)
			}
		}
	}
	if !found {
		t.Fatal("override-only.example 命中了用户指定的列表,却没有产出 covered-by-builtin-list 这一行")
	}
}

// rules[] 有多个条目时要全部摊平(与 setup.ListRules 的语义一致)。
func TestBuildRuleReviewInputFlattensEveryRulesEntry(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{
		{Direct: []string{"a.com"}},
		{Direct: []string{"b.com"}, Proxy: []string{"c.com"}},
	}}
	in := buildRuleReviewInput(cfg, nil, nil)
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
	rep := rulereview.NewReport(nil, false, "in global mode the built-in china list does not apply at all, so this class was not compared")
	lines := ruleReviewDoctorLines(rep)
	var said bool
	for _, l := range lines {
		if strings.Contains(l.Value, "was not compared") || strings.Contains(l.Value, "does not apply") {
			said = true
		}
	}
	if !said {
		t.Fatalf("「没查」被静默成了「没问题」:%+v", lines)
	}
}

// **Finding 1 的核心回归。** ClassShadowedByBuiltinList 对 direct/proxy 两支写的是
// 两句意思相反的话(review.go 的 shadowedByBuiltinFindings):direct 命中是「删了没
// 影响」,proxy 命中是「这是生效中的例外,删了会改变流量」。而渲染层此前把两支的
// finding 全塞进同一个 summarizeClass 调用、同一个 Key("covered by builtin list"),
// Summary 里那句相反的话从没被打印过 —— 一份只有 proxy 例外的配置(如 bilibili.com/
// hdslb.com 两条 proxy 规则)读到的是跟「可以安全删除」同一种版式的一行。
//
// 这里刻意只断言两条 proxy 命中都出现、且没有落进 direct 专用的 key,不检查具体
// 措辞字面 —— 那样改错了措辞照样通过没有意义,但 Key 分离是判据完整性的底线。
func TestProxyOnlyBuiltinListHitsAreFiledAsExceptionsNotSafeToDelete(t *testing.T) {
	rep := rulereview.NewReport([]rulereview.Finding{
		{
			Kind: "proxy", Rule: "*.bilibili.com", Class: rulereview.ClassShadowedByBuiltinList,
			Summary:   "the built-in china list sends this direct, and your rule pulls it back through the tunnel — an exception in force, not a duplicate.",
			CoveredBy: "bilibili.com",
		},
		{
			Kind: "proxy", Rule: "*.hdslb.com", Class: rulereview.ClassShadowedByBuiltinList,
			Summary:   "the built-in china list sends this direct, and your rule pulls it back through the tunnel — an exception in force, not a duplicate.",
			CoveredBy: "hdslb.com",
		},
	}, true, "")

	lines := ruleReviewDoctorLines(rep)

	// **必须没有**「covered by builtin list」这一行——那是 direct 专用,意思是
	// 「删掉没有影响」;这份配置只有 proxy 例外,一条都不该落进那个 key。
	for _, l := range lines {
		if l.Key == "covered by builtin list" {
			t.Fatalf("proxy 命中被归进了 direct 那一支(可安全删除),"+
				"把两条生效中的例外说成了冗余:%+v", l)
		}
	}

	var found bool
	for _, l := range lines {
		if !strings.Contains(l.Value, "*.bilibili.com") {
			continue
		}
		found = true
		if l.Key != "builtin list exception" {
			t.Errorf("proxy 命中的 Key = %q, want %q", l.Key, "builtin list exception")
		}
		if !strings.Contains(l.Value, "*.hdslb.com") {
			t.Errorf("两条 proxy 命中应该合并在同一行,不是各占一行:%q", l.Value)
		}
		// **禁的是渲染层 direct 那一支的原话,不是 Finding.Summary 的。**
		// builtinListLines 自己写两句方向相反的话,Summary 一个字都不打印 ——
		// 第一版改英文时照着 Summary 选词,于是这条禁词永远命中不了(变异实测
		// 全绿)。direct 说「deleting them changes no traffic」,proxy 说
		// 「…changes traffic」,前者不是后者的子串。
		if strings.Contains(l.Value, "changes no traffic") {
			t.Errorf("proxy 那一行用了「可以安全删除」的措辞——会让用户删掉一条正在把流量拉回"+
				"隧道的规则(bilibili.com 会因此改走直连):%q", l.Value)
		}
	}
	if !found {
		t.Fatal("proxy 命中的两条规则在 doctor 输出里一个字都没提")
	}
}

// 同一个域名同时出现在 direct 与 proxy 两张表(review.go 的 fixture 就是这个形状:
// *.myqcloud.com 既在 direct 里被判「删了没影响」,也可能在别的配置里以 proxy 形式
// 被判「生效中的例外」)——两条 finding 必须落进**两条独立的行**、**两个独立的
// 计数**、以及(--json 路径)**两个不同的 check name**,不许合并成一行也不许共用
// 同一个 check name(check name 撞了,agent/MCP 按名字取只会拿到其中一条,
// 55ef8ea 就是这个形状,只是那次撞的是 ClassRisky)。
func TestBuiltinListHitsSplitByKindIntoTwoLinesAndTwoCheckNames(t *testing.T) {
	rep := rulereview.NewReport([]rulereview.Finding{
		{
			Kind: "direct", Rule: "*.myqcloud.com", Class: rulereview.ClassShadowedByBuiltinList,
			Summary:   "已在内建 china 直连列表里,这条手写的没有额外作用。",
			CoveredBy: "myqcloud.com",
		},
		{
			Kind: "proxy", Rule: "*.myqcloud.com", Class: rulereview.ClassShadowedByBuiltinList,
			Summary:   "the built-in china list sends this direct, and your rule pulls it back through the tunnel — an exception in force, not a duplicate.",
			CoveredBy: "myqcloud.com",
		},
	}, true, "")

	lines := ruleReviewDoctorLines(rep)
	var direct, proxy *doctorFinding
	for i := range lines {
		switch lines[i].Key {
		case "covered by builtin list":
			direct = &lines[i]
		case "builtin list exception":
			proxy = &lines[i]
		}
	}
	if direct == nil {
		t.Fatalf("direct 命中没有落进「covered by builtin list」这一行:%+v", lines)
	}
	if proxy == nil {
		t.Fatalf("proxy 命中没有落进「builtin list exception」这一行:%+v", lines)
	}
	if direct.Value == proxy.Value {
		t.Fatalf("两行的文本完全相同——split 没有生效:%q", direct.Value)
	}
	if direct.Status != "info" || proxy.Status != "info" {
		t.Errorf("两行都应是 info(建议,不是安全告警):direct=%q proxy=%q", direct.Status, proxy.Status)
	}

	nameDirect := ruleReviewCheckName(direct.Key)
	nameProxy := ruleReviewCheckName(proxy.Key)
	if nameDirect == nameProxy {
		t.Fatalf("两条 check 用了同一个名字 %q —— --json 消费方按名字取,会静默丢掉一条", nameDirect)
	}
}

// **接线守卫。** 判据全对而没人调用,与没有这个功能在输出上完全一样 ——
// 本仓库反复栽在这一点上(阶段③a 那次:goroutine 体空转,而断言只证明 channel 会关)。
//
// 这里不查源码文本,而是真的跑一遍两条 doctor 路径,断言那条危险规则出现在输出里。
//
// **字段名核实过,不是照抄 brief**:doctorReport.Checks 是 []checkReport,
// checkReport 的字段是 Name/Status/Detail/Hint —— 没有 Value 这个字段,
// 用的是 c.Detail(`grep -n "type checkReport" -A 10 internal/cli/cli.go` 核对过)。
//
// **按 Name 而不是按子串找那一行**:*.myqcloud.com 恰好**也**在真实内嵌 china
// 列表里(rulereview 包自己的 fixture 就特意选了它),于是它同时触发
// risky_direct(warn)与 shadowed_by_builtin_list(info)两条 finding,后者的文本
// 里同样含有子串 "*.myqcloud.com"。brief 里按子串扫全部 checks 的写法在合成小
// report(Step 1 那几条单测)下没问题,换成真实内嵌列表跑就会撞见这第二条同文
// 的 info 行,把断言判错——这正是「fixture 必须是生产解析器/生产数据能读的
// 那种」在这里的具体形状。改按 ruleReviewCheckName("risky direct rule") 这个
// 稳定的 check name 定位,不受同一子串还出现在别的 finding 里影响。
func TestDoctorSurfacesRiskyRuleOnBothPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "server: brook://example.com:9999?password=x\nrules:\n  - direct:\n      - '*.myqcloud.com'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	rep := collectClientDoctorWith(path, "", time.Second, true, false)

	wantName := ruleReviewCheckName("risky direct rule")
	var found bool
	for _, c := range rep.Checks {
		if c.Name != wantName {
			continue
		}
		if !strings.Contains(c.Detail, "*.myqcloud.com") {
			t.Errorf("危险规则行没有点名到规则原文: %q", c.Detail)
		}
		found = true
		if c.Status != "warn" {
			t.Errorf("JSON 路径上危险规则的状态是 %q,want warn", c.Status)
		}
	}
	if !found {
		t.Fatal("bx doctor --json 里一个字都没提那条危险直连规则 —— 接线没接上")
	}
}

// **回归 review finding**:policy.DirectRisk 的名单有 19 个域
// (aliyuncs/myqcloud/amazonaws/cloudfront/github.io…),配置里同时有两条危险直连
// 完全现实。此前的实现按每条 finding 各调一次 rep.AddCheck(同一个 name),
// --json 里会出现**多个同名** checkReport;ruleReviewCheckName 的注释自己写的
// 就是「agent 与 MCP 按名字取」,按名字取的消费方只会拿到其中一条,静默丢掉
// 其余的安全结论 —— 一个去匿名化风险被静默丢掉,方向正好是这个功能要防的
// 那个错误的反面。
//
// 断言:同名 check 恰好一条,且它的 Detail 里两条规则都点到了名。
func TestDoctorJSONMergesMultipleRiskyRulesIntoOneNamedCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "server: brook://example.com:9999?password=x\n" +
		"rules:\n  - direct:\n      - '*.myqcloud.com'\n      - '*.aliyuncs.com'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	rep := collectClientDoctorWith(path, "", time.Second, true, false)

	wantName := ruleReviewCheckName("risky direct rule")
	var matches []checkReport
	for _, c := range rep.Checks {
		if c.Name == wantName {
			matches = append(matches, c)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("同名 check %q 出现 %d 次,想要恰好 1 次"+
			"(按名字取的消费方会静默丢掉除一条之外的全部危险规则):%+v",
			wantName, len(matches), matches)
	}
	for _, rule := range []string{"*.myqcloud.com", "*.aliyuncs.com"} {
		if !strings.Contains(matches[0].Detail, rule) {
			t.Errorf("合并后的 Detail 里丢了 %q: %q", rule, matches[0].Detail)
		}
	}
	if matches[0].Status != "warn" {
		t.Errorf("Status = %q, want warn", matches[0].Status)
	}
}

// captureStdout 把 fn 执行期间写往 os.Stdout 的内容整个捕获回来。
// doctorLine 直接 fmt.Printf 到 os.Stdout(全局变量),测试期间原地替换即可,
// 不需要给 doctorAction 单独开一个可注入 writer 的口子。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return buf.String()
}

// 同一个 review finding,文本路径这一侧:doctorAction 把 ruleReviewDoctorLines
// 的每一条按 doctorLine(status, key, value) 打一行,合并后仍必须**两条规则都
// 出现在输出里**——只打第一条的话,用户会以为按提示删掉那一条就完了。
//
// 这里真的跑一遍 bx doctor(经 New() 装配的完整 App,与命令行用户看到的路径
// 一致),不是直接调 ruleReviewDoctorLines——后者已经被上面那条 JSON 测试
// 和这条共用同一份判据的事实覆盖了;这条要证明的是**渲染那一层没有偷偷截断**。
func TestDoctorTextPathListsEveryRiskyRuleNotJustTheFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "server: brook://example.com:9999?password=x\n" +
		"rules:\n  - direct:\n      - '*.myqcloud.com'\n      - '*.aliyuncs.com'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// 隔离诊断归档:doctorAction 非 --json 路径会 defer 一次日志归档,
	// 默认目标是系统路径(/Library/Logs/bx/diagnostics),测试环境下大概率
	// 没权限,失败本身不影响断言(只打到 stderr),但改到 t.TempDir() 更干净、
	// 不依赖真实系统目录是否可写。
	t.Setenv("BX_LOG_ARCHIVE_DIR", filepath.Join(dir, "archive"))

	out := captureStdout(t, func() {
		app := New()
		if err := app.Run([]string{"bx", "doctor", "--config", path, "--skip-probe"}); err != nil {
			t.Fatalf("bx doctor: %v", err)
		}
	})

	for _, rule := range []string{"*.myqcloud.com", "*.aliyuncs.com"} {
		if !strings.Contains(out, rule) {
			t.Errorf("文本路径的输出里丢了 %q(只给第一条会让用户以为删掉那一条就完了):\n%s", rule, out)
		}
	}
}
