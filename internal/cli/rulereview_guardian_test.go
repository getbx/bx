package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/rulereview"
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

// **这一段 2026-08-31 被整体取代,记档在此。**
//
// 早先这条路是「客户端拿 Guardian 的规则、自己算」,只能给三类 —— china 那一类
// 诚实跳过,因为客户端无从知道用户有没有指定自己的列表(拿默认表去比就是
// wrong-reference-object)。当天稍晚改成 **Guardian 自己算**:它有 root,读得到
// 配置与 Core 实际在用的那张 china 列表,四类一个不少。
//
// 于是那五条针对客户端简化组装的判据测试连同被测函数一起退场 —— 它们守的
// 那件事(「诚实跳过」)不再是这条路的行为。判据现在由
// internal/guardian/rulereview_test.go 守着,接线由下面几条守着。

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
	guardianRulesForDoctor = func() (*rulereview.Report, string, error) {
		// **用真 Review 造 fixture,不手搓 Report。** 手搓的那份只填了
		// Findings 而没有各类计数,渲染层于是一个字都不说 —— 一份生产里不可能
		// 出现的输入,会让这条守卫测的是另一件事(本仓库记档过的「fixture
		// 真实性是承重的」)。
		report := rulereview.Review(rulereview.Input{
			Direct: []string{"*.a.com"}, Proxy: []string{"*.a.com"},
		})
		return &report, cfgPath, nil
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
	guardianRulesForDoctor = func() (*rulereview.Report, string, error) {
		return nil, "", errors.New("dial guardian: no such file")
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
	guardianRulesForDoctor = func() (*rulereview.Report, string, error) {
		return &rulereview.Report{}, cfgPath, nil
	}

	rep := collectClientDoctorWith(cfgPath, "", 0, true, false)
	for _, c := range rep.Checks {
		if c.Name != "config_readable" {
			continue
		}
		if !strings.Contains(c.Detail, "are absent this round") {
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
	guardianRulesForDoctor = func() (*rulereview.Report, string, error) {
		called = true
		return &rulereview.Report{}, "/nonexistent/bx/config.yaml", nil
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
	guardianRulesForDoctor = func() (*rulereview.Report, string, error) {
		return &rulereview.Report{}, "/etc/bx/config.yaml", nil
	}

	cfgPath := unreadableConfigForTest(t)
	rep := collectClientDoctorWith(cfgPath, "", 0, true, false)
	for _, c := range rep.Checks {
		if c.Name == "config_readable" && c.Status != "fail" {
			t.Fatalf("Guardian 答的是另一个文件,却被当成了这个文件的答案(status=%q)", c.Status)
		}
	}
}
