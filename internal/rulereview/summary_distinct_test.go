package rulereview

import (
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/route"
)

// —— 每一类必须说一句**属于它自己**的话(2026-09-17 从 Swift 搬过来的)——
//
// 这条不变量此前只活在菜单的 `RulesModelTests`(那时界面按 class 在客户端另写一份
// 英文,守卫钉的是那份)。判据文案改成英文之后,客户端那份被删掉了 ——
// **判据搬了家,守卫必须跟着搬**,否则这条不变量会随着那次删除一起静默消失。
//
// 它守的是什么:两类说出同一句话时,用户读到的建议对其中一类是**错的**。
// 最贵的那一对已经有专门的守卫(TestProxyRuleHittingChinaListIsCalledAnException:
// 冗余 vs 生效中的例外,说反了就是叫人删掉一条正在工作的规则);这一条是它的
// 全称版本,防的是将来新加一类时顺手照抄了旁边那句。
func TestEveryClassSaysSomethingOfItsOwn(t *testing.T) {
	// **穷举 Class,不手抄名单** —— 加一类而忘了给它写话时这里会红。
	// 上界取「最后一个已知常量」,与 verdict.go 那句「加在最后」的约定同源。
	seen := map[string]Class{}
	for c := ClassRisky; c <= ClassDead; c++ {
		s := strings.TrimSpace(summaryForClassInTests(t, c))
		if s == "" {
			t.Errorf("%v 没有话说 —— 一条没有结论的 finding 与没有这条 finding 在界面上一样", c)
			continue
		}
		if prev, dup := seen[s]; dup {
			t.Errorf("%v 与 %v 说了同一句话:%q —— 两类共用一句,其中一类的建议必然是错的", c, prev, s)
		}
		seen[s] = c
	}
	if len(seen) == 0 {
		t.Fatal("一类都没扫到 —— 守卫读不懂现在的代码了,先修它")
	}
}

// summaryForClassInTests 造一份只含该类的最小输入,取出它的 summary。
//
// **走生产的 Review/NewReport,不在测试里重抄一份措辞** —— 抄一份的话这条守卫
// 守的就是它自己那份(本仓库编号的第五种失效写法)。
func summaryForClassInTests(t *testing.T, c Class) string {
	t.Helper()
	var in Input
	switch c {
	case ClassRisky:
		in = Input{Direct: []string{"*.s3.amazonaws.com"}}
	case ClassShadowedByUserRule:
		in = Input{Direct: []string{"a.example", "*.example"}}
	case ClassOverriddenByOppositeKind:
		in = Input{Direct: []string{"a.example"}, Proxy: []string{"*.example"}}
	case ClassShadowedByBuiltinList:
		in = Input{Direct: []string{"cn.example"}, China: route.NewDomainSet([]string{"cn.example"})}
	case ClassDead:
		in = Input{
			Direct:           []string{"*.dead.example"},
			History:          map[RuleKey]RuleCounts{},
			HistoryUptime:    30 * 24 * time.Hour,
			HistoryDecisions: 100_000,
		}
	default:
		t.Fatalf("Class %v 没有对应的最小输入 —— 新加一类时要在这里补一个,"+
			"否则这条守卫会安静地跳过它", c)
	}
	for _, f := range Review(in).Findings {
		if f.Class == c {
			return f.Summary
		}
	}
	t.Errorf("造不出 %v 这一类的 finding —— 要么输入不对,要么这一类已经产不出来了", c)
	return ""
}
