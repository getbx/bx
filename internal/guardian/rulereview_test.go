package guardian

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/rulereview"
	"github.com/getbx/bx/internal/stats"
)

// Guardian 自己做规则体检并发布 —— 终局形状。
//
// **为什么是它而不是 CLI**:体检要同时读三样东西 —— 配置(0600 root-only)、
// **Core 实际在用的那张 china 列表**(/var/lib/bx,drwx------)、以及 Core 的
// 累计历史。非 root 的 `bx doctor` 三样里读得到一样;Guardian 三样全读得到。
//
// 此前非 root 那条路(2026-08-31 早些时候做的 Guardian /v1/rules 退路)只能给
// 三类 + 死规则,**china 那一类诚实跳过** —— 因为客户端无从知道用户有没有指定
// 自己的列表。让 Guardian 来算,这一类也能给了:它读得到 lists.china_domain。

func writeGuardianConfigForReview(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// 判定与 CLI 那条路**同源**:两边都调 rulereview.Review,组装都走
// rulereviewsrc.Assemble。这条测试钉住 Guardian 真的产出了结论,而不是
// 发一个空壳 —— 「接了但没算」与「没接」在应答上完全一样。
func TestGuardianReviewsRulesItCanRead(t *testing.T) {
	// 同一条原文同时在 direct 与 proxy ⇒ 跨表压制,与 china 列表无关,必判得出。
	cfg := writeGuardianConfigForReview(t, `
server: brook://x
rules:
  - direct: ["*.a.com"]
  - proxy: ["*.a.com"]
`)
	report := reviewRulesAt(cfg, func() (stats.Report, error) {
		return stats.Report{}, errNoCoreForReview
	})
	if report == nil {
		t.Fatal("Guardian 没有产出体检结论")
	}
	var classes []string
	for _, f := range report.Findings {
		classes = append(classes, string(f.Class))
	}
	if !strings.Contains(strings.Join(classes, ","), string(rulereview.ClassOverriddenByOppositeKind)) {
		t.Fatalf("跨表压制没判出来: %v", classes)
	}
}

// **配置读不出来时不许发一份空报告。** 空报告与「你的规则都很健康」在应答上
// 完全一样,而前者是「没查」—— 这个功能最贵的教训就是这两者必须分得开。
func TestGuardianReviewIsAbsentRatherThanEmptyWhenConfigIsUnreadable(t *testing.T) {
	if report := reviewRulesAt(filepath.Join(t.TempDir(), "nope.yaml"), nil); report != nil {
		t.Fatalf("读不到配置却发出了一份报告(会被读成「都很健康」): %+v", report)
	}
}

// Guardian 读得到用户指定的列表,所以 **china 那一类不该再被跳过** ——
// 那正是这一步存在的全部理由。这里给一个真实存在的自定义列表,断言比对真的发生。
func TestGuardianReviewComparesAgainstTheUsersOwnChinaList(t *testing.T) {
	dir := t.TempDir()
	listPath := filepath.Join(dir, "my_china.txt")
	if err := os.WriteFile(listPath, []byte("a.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := writeGuardianConfigForReview(t, `
server: brook://x
lists:
  china_domain: `+listPath+`
rules:
  - direct: ["*.a.com"]
`)
	report := reviewRulesAt(cfg, nil)
	if report == nil {
		t.Fatal("没有产出结论")
	}
	if !report.BuiltinListChecked {
		t.Fatalf("china 那一类仍然没有比对 —— Guardian 读得到那张表,不该跳过(skip=%q)",
			report.BuiltinSkipReason)
	}
}

// 接线守卫:体检必须真的出现在 /v1/rules 的应答里。
// 算好了没发出去,与没算完全一样 —— 而这个仓库全部事故都在接线。
func TestRulesEndpointPublishesTheReview(t *testing.T) {
	cfg := writeGuardianConfigForReview(t, `
server: brook://x
rules:
  - direct: ["*.a.com"]
  - proxy: ["*.a.com"]
`)
	recorder := httptest.NewRecorder()
	serveRuleList(recorder, cfg)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", recorder.Code)
	}
	var body struct {
		Review *rulereview.Report `json:"review"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Review == nil {
		t.Fatal("应答里没有体检结论 —— 算好了没发出去,与没算完全一样")
	}
	if len(body.Review.Findings) == 0 {
		t.Fatal("发出去的是一份空报告(会被读成「都很健康」)")
	}
}
