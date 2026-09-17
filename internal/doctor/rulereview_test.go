package doctor

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/elevate"

	"github.com/getbx/bx/internal/rulereview"
)

// **真机抓到的缺陷(2026-08-17)**:riskyRuleFinding 的 Hint 点名了真实存在的子
// 命令(`direct rm`,不是笔误的 `direct remove`),但没带 sudo——editRuleAction
// (internal/cli/direct.go)对 `/etc/bx/config.yaml` 直接 os.ReadFile/
// os.WriteFile,不自提权、也不走 Guardian 的 peer-cred 鉴权。而这份配置是
// `sudo bx setup` 建的,0600 属主 root(项目所有者机器上真机验证:`bx doctor`
// 非 root 跑得动、照着 hint 敲的 `bx direct rm` 却 permission denied)。跟
// TestRiskyRuleHintUsesARealSubcommand(internal/supervisor/riskyrules_test.go)
// 断言同一个子命令名的思路——只钉性质(带 sudo、点名真实子命令),不钉整句
// 字面量,命令措辞调整时不会跟着变红。
func TestRiskyRuleFindingHintNeedsElevation(t *testing.T) {
	rep := rulereview.Report{Findings: []rulereview.Finding{
		{Kind: "direct", Rule: "*.myqcloud.com", Class: rulereview.ClassRisky, Summary: "an open-subdomain cloud platform"},
	}}
	f := riskyRuleFinding(rep)
	if f == nil {
		t.Fatal("前置断言失败:命中 ClassRisky 却没产出 finding")
	}
	if !strings.Contains(f.Hint, ""+elevate.Prefix+"bx direct rm") {
		t.Errorf("hint 没有带提权前缀,而 bx direct rm 需要管理员才写得了配置:%q", f.Hint)
	}
	// down/up 也要带:这条 hint 跨平台共用,Linux/Windows 上从不经 Guardian 的
	// owner-uid 鉴权、始终需要 root;macOS 上未配置 owner_uid 时同样退化成
	// root-only。本项目主平台是 Linux,漏了会在那里必定 permission denied。
	if !strings.Contains(f.Hint, ""+elevate.Prefix+"bx down") {
		t.Errorf("hint 的 down 没有带 sudo:%q", f.Hint)
	}
	if !strings.Contains(f.Hint, ""+elevate.Prefix+"bx up") {
		t.Errorf("hint 的 up 没有带 sudo:%q", f.Hint)
	}
	if strings.Contains(f.Hint, "direct remove") {
		t.Errorf("hint 用了不存在的子命令 `direct remove`:%q", f.Hint)
	}
}

// **`ClassShadowedByBuiltinList` 的每一条 finding 都必须落进恰好一条渲染线。**
//
// `builtinListLines` 按 `Kind` 的**字面量**分支(只认 "direct" 与 "proxy"),而
// `NewReport` 的计数走的是 `Class`、**根本不看 Kind** —— 于是一条 Kind 为空、或
// 将来多出第三种 Kind 的 finding 会:计数照涨(`ShadowedByBuiltinCount`),而两条
// 渲染线**一条都不匹配** ⇒ 它在文本与 --json 两条路径上**同时静默消失**。
// 用户看到的是「N 条与内建列表相关」而下面只列出 N-1 条,没有任何一处报错。
//
// 今天不可达(`review.go` 只产出 direct/proxy),所以这条守卫钉的是**将来**:
// 谁加第三种 Kind、或让 Kind 漏填,会在这里被拦下,而不是在某个用户的 doctor
// 输出里少一行。判据刻意是「计数 == 落进渲染线的条数」而不是「认得这两个字面量」
// —— 后者会随着新增 Kind 一起被改绿,前者不会。
func TestEveryBuiltinListFindingLandsOnExactlyOneLine(t *testing.T) {
	for _, kind := range []string{"direct", "proxy", "", "egress"} {
		rep := rulereview.NewReport([]rulereview.Finding{{
			Class: rulereview.ClassShadowedByBuiltinList,
			Kind:  kind,
			Rule:  "*.example.com",
		}}, true, "")

		if rep.ShadowedByBuiltinCount != 1 {
			t.Fatalf("kind=%q:前置条件不成立,计数 = %d", kind, rep.ShadowedByBuiltinCount)
		}
		lines := builtinListLines(rep)
		if len(lines) != 1 {
			t.Errorf("kind=%q:计数说有 1 条,而渲染出 %d 条 —— 这条 finding 在文本与 "+
				"--json 两条路径上同时静默消失,用户看到「1 条」而下面一条都没有", kind, len(lines))
		}
	}
}
