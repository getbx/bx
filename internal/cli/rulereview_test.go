package cli

import (
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
