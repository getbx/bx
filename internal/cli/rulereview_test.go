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
			Summary:   "内建 china 列表把它判为直连,而你这条把它扳回隧道——这是生效中的例外,不是冗余。",
			CoveredBy: "bilibili.com",
		},
		{
			Kind: "proxy", Rule: "*.hdslb.com", Class: rulereview.ClassShadowedByBuiltinList,
			Summary:   "内建 china 列表把它判为直连,而你这条把它扳回隧道——这是生效中的例外,不是冗余。",
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
		if strings.Contains(l.Value, "删掉不改变") || strings.Contains(l.Value, "没有额外作用") {
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
			Summary:   "内建 china 列表把它判为直连,而你这条把它扳回隧道——这是生效中的例外,不是冗余。",
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
// 完全现实。此前的实现按每条 finding 各调一次 rep.addCheck(同一个 name),
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
