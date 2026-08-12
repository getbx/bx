package leakcheck

import (
	"testing"
	"time"
)

// 骨架必须与 Judge 真正产出的东西**逐项对上**:同样的 ID、同样的顺序、同样的标题。
//
// 对不上的后果不是「不好看」:页面按骨架把行先摆出来,再把结论按 ID 塞进去 ——
// 多一行骨架就是一行**永远等不到结论的空壳**,少一行就是一条**没有位置可放的结论**
// 被默默丢掉。两种都是界面在说一件判据没说过的事。
//
// 这条守卫拿的是**真的 Judge 输出**,不是一份手抄的期望值 —— 手抄的那份只能证明
// 我抄得和自己一致。
func TestOutlineMatchesWhatJudgeActuallyEmits(t *testing.T) {
	report := Judge(time.Now(), BrowserReport{}, LocalFacts{})
	outline := Outline()

	if len(outline) != len(report.Findings) {
		t.Fatalf("骨架 %d 行,而 Judge 产出 %d 条 —— 页面会摆出空壳或丢掉结论",
			len(outline), len(report.Findings))
	}
	for i, want := range report.Findings {
		if outline[i].ID != want.ID {
			t.Errorf("第 %d 行:骨架 ID = %q,Judge 产出 %q —— 顺序或 ID 漂了", i, outline[i].ID, want.ID)
		}
		if outline[i].Section != want.Section {
			t.Errorf("第 %d 行(%s):骨架分段 = %v,Judge 产出 %v —— 结论会出现在它不属于的"+
				"那一段标题底下,而那个标题正是在向用户说明这一段谁负责",
				i, want.ID, outline[i].Section, want.Section)
		}
		if outline[i].Title != want.Title {
			t.Errorf("第 %d 行:骨架标题 = %q,Judge 产出 %q —— 用户会看到标题在结论落地时跳变",
				i, outline[i].Title, want.Title)
		}
	}
}

// 骨架声明的探测名必须是真的探测名。
//
// 写错一个字符的后果是**静默的**:页面等一个永远不会落定的探测,那一行的进度
// 就永远停在半路,而没有任何东西会报错。
func TestOutlineOnlyNamesRealProbes(t *testing.T) {
	real := map[string]bool{
		ProbeExitV4: true, ProbeExitV6: true, ProbeSRFLX: true,
		ProbeTrace: true, ProbeSurface: true,
	}
	used := map[string]bool{}
	for _, check := range Outline() {
		for _, in := range check.Inputs {
			if !real[in] {
				t.Errorf("%s 声明了一个不存在的探测 %q —— 那一行会永远等下去", check.ID, in)
			}
			used[in] = true
		}
	}
	// 反过来:每个探测都该有人等。一个没人等的探测意味着页面白跑一次网络请求,
	// 而这个功能的每一次出网都要向用户交代。
	for probe := range real {
		if !used[probe] {
			t.Errorf("探测 %q 没有任何一条结论在等它 —— 那次出网没有用途,而每一次出网都要向用户交代", probe)
		}
	}
}

// **DNS 那条不吃任何浏览器探测,这是有意的,也是一条有用的信息。**
//
// 它让用户看得见:这一项根本不需要联网就能答。把它误加上输入,页面会让它干等
// 一个它不需要的探测;而那个探测失败时,一条本可以照常给出的结论就没了。
func TestDNSNeedsNothingFromTheBrowser(t *testing.T) {
	for _, check := range Outline() {
		if check.ID == FindingDNS && len(check.Inputs) != 0 {
			t.Fatalf("DNS 那条不该等浏览器, got %v", check.Inputs)
		}
	}
}
