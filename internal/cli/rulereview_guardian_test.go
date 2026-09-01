package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/rulereview"
	"github.com/getbx/bx/internal/stats"
)

// 规则体检对 agent 不可见,而根因不是「没做」,是**被 root 挡住了**。
//
// 实测(2026-08-31,项目所有者的 Mac,非 root):
//
//	config_readable  fail  open /etc/bx/config.yaml: permission denied
//	ok: False
//
// 配置读不出来 ⇒ 整个 config 分支被跳过 ⇒ 规则体检根本没跑。而 MCP 的设计前提
// 恰恰是「agent 以业主身份、免 sudo 操作 bx」——于是对 agent 来说这一整块诊断
// 等于不存在,拿到的还是一个读起来像「bx 坏了」的 ok:false。
//
// **修法是换一条被授权的路,不是第二个真相源**:Guardian 的 /v1/rules 对业主
// 开放(authorizeOwnerPeer),它读的正是同一个 /etc/bx/config.yaml,只是它有 root。

// **china 那一类必须诚实地跳过。** Guardian 的 /v1/rules 给得出规则,给不出
// `lists.china_domain` —— 用户可能指了自己的列表,而拿默认列表去比正是
// buildRuleReviewInput 头上那段长注释警告的 wrong-reference-object 事故:
// 判据没错,读错了输入,然后指着一条**仍然生效**的规则说「可以删」。
func TestGuardianSourcedInputSkipsTheBuiltinListClassHonestly(t *testing.T) {
	in := ruleReviewInputFromGuardianRules([]string{"*.a.com"}, nil, false, nil)
	if in.China != nil {
		t.Fatal("这条路上无从确认 Core 实际在用哪张 china 列表,不许拿默认的去比")
	}
	if in.ChinaSkipReason == "" {
		t.Fatal("跳过必须给理由 —— 「没查」与「查了没有」分不开正是这个功能最贵的教训")
	}
	if !strings.Contains(in.ChinaSkipReason, "config") && !strings.Contains(in.ChinaSkipReason, "配置") {
		t.Fatalf("理由没说清是因为读不到配置: %q", in.ChinaSkipReason)
	}
}

// 另外三类与 china 无关,一条都不许少 —— 这正是这条路仍然值得存在的理由。
func TestGuardianSourcedInputStillFindsTheModeIndependentClasses(t *testing.T) {
	// 同一条原文同时在 direct 与 proxy:proxy 更宽 ⇒ direct 那条从没生效过。
	in := ruleReviewInputFromGuardianRules([]string{"*.a.com"}, []string{"*.a.com"}, false, nil)
	rep := rulereview.Review(in)
	var kinds []string
	for _, f := range rep.Findings {
		kinds = append(kinds, string(f.Class))
	}
	if len(rep.Findings) == 0 {
		t.Fatal("跨表压制这一类没被判出来 —— 它与 china 列表无关,不该受影响")
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, string(rulereview.ClassOverriddenByOppositeKind)) {
		t.Fatalf("findings = %v, want 含跨表压制", kinds)
	}
}

// global 必须如实带过去:它决定 builtin 那一类要不要判(这里已跳过),也进报告。
// **它取自控制 socket 的模式,不是猜的** —— 拿错 mode 与拿错列表是同一形状的事故。
func TestGuardianSourcedInputCarriesGlobalThrough(t *testing.T) {
	if in := ruleReviewInputFromGuardianRules(nil, nil, true, nil); !in.GlobalProxy {
		t.Fatal("global 没带过去")
	}
	if in := ruleReviewInputFromGuardianRules(nil, nil, false, nil); in.GlobalProxy {
		t.Fatal("非 global 被当成了 global")
	}
}

// 死规则那一类靠 Core 的跨重启累计历史,而**它走的是 0666 的控制 socket,
// 非 root 读得到** —— 这条路上不该顺手把它一起丢掉。
func TestGuardianSourcedInputStillReadsCoreRuleHistory(t *testing.T) {
	in := ruleReviewInputFromGuardianRules(nil, nil, false, func() (stats.Report, error) {
		return stats.Report{RuleHistory: &stats.RuleHistorySnapshot{
			UptimeSeconds: 1234, Decisions: 5678, Versions: []string{"dev"},
		}}, nil
	})
	if in.HistoryUptime == 0 || in.HistoryDecisions == 0 {
		t.Fatalf("累计历史没带过来: uptime=%v decisions=%d", in.HistoryUptime, in.HistoryDecisions)
	}
	if in.HistorySkipReason != "" {
		t.Fatalf("读到了历史却还写着跳过理由: %q", in.HistorySkipReason)
	}
}

// Core 没在跑是常态(体检本来就常在保护关着时跑):不报错,如实说没有历史。
func TestGuardianSourcedInputTreatsMissingCoreAsNotChecked(t *testing.T) {
	in := ruleReviewInputFromGuardianRules(nil, nil, false, func() (stats.Report, error) {
		return stats.Report{}, errors.New("dial: no such file")
	})
	if in.HistorySkipReason == "" {
		t.Fatal("Core 读不到时必须给理由,而不是静默当成「查过了、没有」")
	}
}

// **接线守卫。** 上面几条测的是判据,而这个仓库全部事故都在接线:
// 判据写好了、doctor 那条路上没人调,与没写完全一样,且不会有任何东西报错。
//
// 判据是**行为**:给一个读不出来的配置路径 + 一个能给出规则的 Guardian,
// 报告里必须真的出现规则体检的结论。
func TestDoctorFallsBackToGuardianWhenConfigIsUnreadable(t *testing.T) {
	previous := guardianRulesForDoctor
	t.Cleanup(func() { guardianRulesForDoctor = previous })
	// 退路只对**权限**失败生效,且 Guardian 必须读的是同一个文件 —— 故这里
	// 造一个真实存在、但读不动的配置,并让替身报出同一个路径。
	cfgPath := unreadableConfigForTest(t)
	guardianRulesForDoctor = func() ([]string, []string, bool, string, error) {
		// 同一条原文同时在两张表 ⇒ 跨表压制,与 china 列表无关,必然判得出来。
		return []string{"*.a.com"}, []string{"*.a.com"}, false, cfgPath, nil
	}

	rep := collectClientDoctorWith(cfgPath, "", 0, true, false)

	var names []string
	for _, c := range rep.Checks {
		names = append(names, c.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "rule_") {
		t.Fatalf("配置读不到时规则体检整块消失了 —— agent 拿不到任何结论。checks=%v", names)
	}
}

// 退路失败时不许假装成功:Guardian 也读不到就如实说,而不是悄悄少一块。
func TestDoctorSaysSoWhenNeitherConfigNorGuardianCanBeRead(t *testing.T) {
	previous := guardianRulesForDoctor
	t.Cleanup(func() { guardianRulesForDoctor = previous })
	guardianRulesForDoctor = func() ([]string, []string, bool, string, error) {
		return nil, nil, false, "", errors.New("dial guardian: no such file")
	}

	rep := collectClientDoctorWith(unreadableConfigForTest(t), "", 0, true, false)

	var readable *checkReport
	for i := range rep.Checks {
		if rep.Checks[i].Name == "config_readable" {
			readable = &rep.Checks[i]
		}
	}
	if readable == nil {
		t.Fatal("没有 config_readable 这条检查")
	}
	if readable.Status != "fail" {
		t.Fatalf("两条路都读不到,仍该是 fail(真的什么都没查到),got %q", readable.Status)
	}
}

// 退路补回了规则体检,但**别的依赖配置的检查仍然缺席** —— 不点名的话
// ok:true 会被读成「全都查过且没问题」。「没查」与「查了没有」分不开,
// 是这份报告最不能犯的错。
func TestDoctorNamesWhatIsStillMissingOnTheGuardianPath(t *testing.T) {
	previous := guardianRulesForDoctor
	t.Cleanup(func() { guardianRulesForDoctor = previous })
	cfgPath := unreadableConfigForTest(t)
	guardianRulesForDoctor = func() ([]string, []string, bool, string, error) {
		return []string{"*.a.com"}, nil, false, cfgPath, nil
	}

	rep := collectClientDoctorWith(cfgPath, "", 0, true, false)
	for _, c := range rep.Checks {
		if c.Name != "config_readable" {
			continue
		}
		if !strings.Contains(c.Detail, "缺席") {
			t.Fatalf("没说清哪些检查本次没跑: %q", c.Detail)
		}
		return
	}
	t.Fatal("没有 config_readable 这条检查")
}

// unreadableConfigForTest 造一个**存在但读不动**的配置 —— 那正是非 root 的
// 真实处境(0600 root-only)。用「不存在的路径」代替它会让这几条测试测的是
// 另一种失败,而那种失败**不该**走退路。
func unreadableConfigForTest(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("以 root 跑时造不出「读不动」")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: brook://x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	return path
}

// **配置不存在(而不是读不动)时不许走退路。** 那是「这台机器没 setup 过」,
// 是真问题;拿 Guardian 的答案盖住它就是掩盖故障 —— 既有的
// TestClientDoctorJSONReport 当场抓到过这个错,这条把它钉在本功能自己这里。
func TestDoctorDoesNotUseGuardianWhenConfigIsSimplyMissing(t *testing.T) {
	previous := guardianRulesForDoctor
	t.Cleanup(func() { guardianRulesForDoctor = previous })
	called := false
	guardianRulesForDoctor = func() ([]string, []string, bool, string, error) {
		called = true
		return []string{"*.a.com"}, nil, false, "/nonexistent/bx/config.yaml", nil
	}

	rep := collectClientDoctorWith("/nonexistent/bx/config.yaml", "", 0, true, false)
	_ = called // 调不调都行,关键是结论
	for _, c := range rep.Checks {
		if c.Name == "config_readable" && c.Status != "fail" {
			t.Fatalf("配置根本不存在却没报 fail(status=%q)—— 「没 setup 过」被掩盖了", c.Status)
		}
	}
}

// **Guardian 读的必须是同一个文件。** `--config /somewhere/else` 被一份来自
// /etc/bx/config.yaml 的答案冒名顶替,是 wrong-reference-object 的又一处。
func TestDoctorRefusesGuardianAnswerAboutADifferentFile(t *testing.T) {
	previous := guardianRulesForDoctor
	t.Cleanup(func() { guardianRulesForDoctor = previous })
	guardianRulesForDoctor = func() ([]string, []string, bool, string, error) {
		return []string{"*.a.com"}, nil, false, "/etc/bx/config.yaml", nil
	}

	cfgPath := unreadableConfigForTest(t)
	rep := collectClientDoctorWith(cfgPath, "", 0, true, false)
	for _, c := range rep.Checks {
		if c.Name == "config_readable" && c.Status != "fail" {
			t.Fatalf("Guardian 答的是另一个文件,却被当成了这个文件的答案(status=%q)", c.Status)
		}
	}
}
