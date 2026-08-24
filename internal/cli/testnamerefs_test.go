package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// —— 注释里点名的测试必须真的存在(2026-08-24)——
//
// **这个仓库为「关于代码的陈述没有守卫」栽过很多次**,而其中最常见的一种是
// 注释里写着「由 TestXxx 钉住」而 TestXxx 早已改名或删除。代码有测试盯着,
// 关于代码的陈述没有 —— 于是一句「这件事有人守着」可以在改名之后原样活很久。
//
// 后果不是「不好看」:下一个人读到那句话,会**据此不再去检查那件事**。本轮
// 一次性扫出 7 个失效引用,其中一个是 leakserve 那条 `…CarriesOnlyToken…` 的旧名
// (真名后来加了 `AndSkeleton`,而三处注释都还写着旧名);另一个点名的测试
// **压根不存在**,而它描述的那件事(常驻安全告警的 hint 必须是真敲得动的命令)
// 当时确实无人守 —— 那条守卫按它描述的样子补上了。
//
// (这段注释里的旧名刻意写成 `…CarriesOnlyToken…` 而不是完整标识符:写全了会被
// **这条守卫自己**判成一次失效引用。第一版就是这么写的,当场转红 —— 顺带算是它
// 有效的一次演示。)
//
// **判据对「前缀」宽容**:注释里写 `TestRequireStatusWatchCapability*` 这类通配
// 形式很常见,只要有任何一个真实测试以它开头就算数。宁可放过一个,也不要制造
// 假红 —— 一条会误报的守卫会被下一个人删掉。
func TestEveryTestNameMentionedInACommentExists(t *testing.T) {
	root := repoRootForTestNameRefs(t)
	defined, refs := scanTestNames(t, root)
	if len(defined) < 500 {
		t.Fatalf("只扫出 %d 个测试函数,少得反常 —— 守卫可能没走到该走的目录", len(defined))
	}
	if len(refs) < 20 {
		t.Fatalf("只扫出 %d 处注释引用,少得反常 —— 提取正则可能读不懂现在的写法了", len(refs))
	}

	for name, sites := range refs {
		// **退场名单必须先查。** 第一版把 hasPrefixMatch 放在前面,于是「退场的
		// 名字又变回真测试」那条反向断言**永远走不到** —— 变异实测:随手加一个
		// 同名空测试,整条守卫照样绿。一条在最需要它时恰好不可达的断言,与没有
		// 这条断言完全一样,而它看起来更让人放心。**这正是本守卫要抓的那个形状,
		// 出现在本守卫自己身上。**
		if why, retired := retiredTestNames[name]; retired {
			if defined[name] {
				t.Errorf("%s 在退场名单里(%s),但它现在真的存在 —— 请把这条删掉,"+
					"一份说某个测试已退场而它其实还在的名单,和一句失效的引用一样坏", name, why)
			}
			continue
		}
		if hasPrefixMatch(defined, name) {
			continue
		}
		t.Errorf("注释里点名了 %s,而全仓找不到这个测试(出现在 %s)。"+
			"两条出路:① 它改名了 ⇒ 把注释改对;② 它是被**刻意退场**的历史记述 ⇒ "+
			"登记进 retiredTestNames 并写明为什么。一句「这件事有人守着」在失效之后"+
			"会让下一个人据此不再去检查那件事", name, strings.Join(sites, ", "))
	}
}

// retiredTestNames 是**刻意退场**的测试:注释里点名它们是在讲历史(「这一条已由
// 集成台接手并退场」),不是在声称它们还在。值是理由。
//
// 登记是一次**显式动作**:删掉一条守卫时要在这里说清楚它被什么接手了,
// 而不是让那句注释悄悄变成一句失效引用。
var retiredTestNames = map[string]string{
	"TestRunTakesRuntimeBypassFromWiringNotAFrozenSlice": "已由集成台(-tags integration,真 Run + 真 netns + 真路由表)接手,逐条变异实测后退场;bypassrefresh_test.go 里那段退场说明记着经过",
	"TestRunWiresLiveMutatorToLiveBypassStore":           "同上,一并退场",
	"TestNoPackageWritesToLiveMutatorStore":              "同上,一并退场",
	"TestConfigWarningsReachTheStatusReport":             "它自己在测试函数里 append 一遍再断言那个局部变量,一次都没调用生产的组装逻辑;被 TestStatusReporterIncludesBothGuardAndConfigWarnings 取代,注释里点名它是在讲这段经过",
}

func hasPrefixMatch(defined map[string]bool, name string) bool {
	for d := range defined {
		if strings.HasPrefix(d, name) {
			return true
		}
	}
	return false
}

var (
	testFuncRe = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
	testRefRe  = regexp.MustCompile(`\b(Test[A-Z][A-Za-z0-9_]{6,})\b`)
)

// scanTestNames 走一遍全仓的 .go 文件,收「定义了哪些测试」与「注释里点名了谁」。
//
// **只看以 // 开头的行。** 测试代码里 `t.Run("TestFoo")` 之类的字符串不算引用,
// 而这条守卫要管的恰恰是**散文**里的断言。
func scanTestNames(t *testing.T, root string) (map[string]bool, map[string][]string) {
	t.Helper()
	defined := map[string]bool{}
	refs := map[string][]string{}
	var skipped []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// 走不进去的**非 Go 路径**跳过,但要留痕 —— 仓库里有 root 属主的
			// 日志目录之类的东西,为它整条守卫变红是假红,而假红的守卫会被
			// 下一个人删掉。**.go 文件本身读不到仍然响亮失败**(见下面那处)。
			if strings.HasSuffix(path, ".go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			skipped = append(skipped, rel)
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "vendor" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		for _, m := range testFuncRe.FindAllStringSubmatch(src, -1) {
			defined[m[1]] = true
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(src, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, m := range testRefRe.FindAllStringSubmatch(line, -1) {
				site := rel + ":" + itoa(i+1)
				refs[m[1]] = append(refs[m[1]], site)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("走仓库: %v —— 一个 .go 文件读不到就响亮失败,不静默放行", err)
	}
	if len(skipped) > 0 {
		// 静默跳过的守卫与静默通过的守卫是同一个问题,所以跳过要留痕。
		t.Logf("走不进去、已跳过的非 Go 路径(%d 条):%s", len(skipped), strings.Join(skipped, ", "))
	}
	return defined, refs
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func repoRootForTestNameRefs(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if filepath.Dir(dir) == dir {
			t.Fatal("没找到 go.mod —— 这条守卫无从判断,如实失败")
		}
	}
}
