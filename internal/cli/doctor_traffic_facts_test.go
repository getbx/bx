package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"testing"
)

// **判据不许再长回本包。**
//
// 流量成败那几行 2026-09-12 之前住在这里(doctorOutcomeChecks),而它只有
// `bx doctor` 的**文本**路径在调 —— 菜单的 Checks 页走 /v1/doctor → doctor.Judge,
// 于是一台正在成片失败的机器在页面上看到的是加粗的「0 failed · 0 warnings」。
// 这条守卫钉住那半判据不会被谁顺手搬回来:本包里不许再出现产出流量结论的函数。
func TestTrafficJudgementLivesInTheDoctorPackage(t *testing.T) {
	// **判据打在 AST 上,不打在文本上。** 这个文件的注释里就写着那几个名字
	// (它得解释自己为什么长这样),按文本查会让守卫自己恒红 —— 而一条恒红的
	// 守卫会被下一个人删掉,那等于没有守卫。
	file, err := parser.ParseFile(token.NewFileSet(), "doctor_traffic_facts.go", nil, 0)
	if err != nil {
		t.Fatalf("解析不了 doctor_traffic_facts.go,守卫读不懂现在的代码: %v", err)
	}
	banned := map[string]string{
		"doctorOutcomeChecks": "流量判据的旧入口",
		"FailingRules":        "点名成片失败的规则是判据",
		"UDPNotice":           "UDP 那句话是判据",
		"checkReport":         "产出 check 就是在下判断",
	}
	seen := 0
	ast.Inspect(file, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		seen++
		if why, bad := banned[id.Name]; bad {
			t.Errorf("doctor_traffic_facts.go 里用到了 %s(%s)—— 采集方又开始自己下判断了", id.Name, why)
		}
		return true
	})
	if seen == 0 {
		t.Fatal("一个标识符都没看到 —— 守卫失效了")
	}
}

// **接线是一个表达式,拆不开。**
//
// doctor.Facts.Traffic 的 nil 语义是「这条路径根本没问」,而 doctorTrafficFacts
// 永不返回 nil 且把「问不出来」原样带在 Err 里。写成
// `f.Traffic = doctorTrafficFacts(...)` 之后,想把「没问到」压成一个具体答案
// 就得先把它拆成两句,而那是看得见的 —— 上一版靠 Go 的多返回值当实参列表保住
// 同一个性质。
func TestDoctorTrafficWiringCannotDropTheError(t *testing.T) {
	source := mustReadRepoFile(t, "doctor_facts.go")
	if !regexp.MustCompile(`f\.Traffic = doctorTrafficFacts\(`).MatchString(source) {
		t.Fatal("collectDoctorFacts 不再把流量事实整个交给 Judge —— 「问不出来」会被压成一个具体答案")
	}
}

// **CLI 这一侧真的填了那份事实。** 上一条读源码,这一条读行为:跑一次真正的
// 采集,断言 Traffic 非 nil —— 少了它,`bx doctor` 会对着一台正在成片失败的
// 机器报「这条路径没有采集流量成败」,而两条守卫都不会红。
func TestCollectDoctorFactsAsksAboutTraffic(t *testing.T) {
	f := collectDoctorFacts(mustTempConfig(t), "", 0, true, false)
	if f.Traffic == nil {
		t.Fatal("采集没有填 Traffic —— Judge 只能报「没查」,而那正是这次要修的缺陷")
	}
}

func mustTempConfig(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte("server: brook://example.com:9999?password=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustReadRepoFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读不到 %s:%v —— 守卫已经失效,先修守卫", name, err)
	}
	return string(data)
}
