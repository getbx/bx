package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// —— 散文里点名的测试必须真的存在(2026-08-24;2026-09-02 扩到 CLAUDE.md;
// 2026-09-12 扩到 apps/ 下的 Swift 源码)——
//
// **名字里是「散文」不是「注释」,因为范围在 2026-09-02 扩了**:CLAUDE.md 点名
// 了 40 多个测试而此前一个守卫都没有,而它恰恰是下一个人开工前唯一会通读的
// 东西。改名是刻意的 —— 一条叫 `…InAComment…` 的守卫会让读到它的人以为
// CLAUDE.md 不在保护范围内,而那正是这条守卫本身要消灭的那种陈述。
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
// **2026-09-12:范围扩到 apps/ 下的 Swift 源码。** 起因是一次审计在 main.swift 里
// 抓到一条失效引用,而这条守卫**在结构上看不见它** —— Swift 不在扫描范围里。
// 那一条已经改对了,但洞留着:菜单是全仓测试覆盖最薄、读源码守卫最多的一块
// (Go 测试编不了 Swift、`main.swift` 连 Swift 测试 target 都进不去),于是
// 「这件事由 TestXxx 钉住」在那里**最承重、也最不容易被发现失效**。
// Swift 只贡献**引用**、不贡献定义:这里的 Swift「测试」是 `@main` 结构体加
// 一串 `expect(...)`(见 apps/macos/BxMenu/Tests/),压根没有 `Test…` 开头的
// 函数名,所以不存在「Swift 定义了它、Go 侧扫不到」这种假红。
//
// **判据对「前缀」宽容**:注释里写 `TestRequireStatusWatchCapability*` 这类通配
// 形式很常见,只要有任何一个真实测试以它开头就算数。宁可放过一个,也不要制造
// 假红 —— 一条会误报的守卫会被下一个人删掉。
func TestEveryTestNameMentionedInProseExists(t *testing.T) {
	root := repoRootForTestNameRefs(t)
	defined, refs := scanTestNames(t, root)
	if len(defined) < 500 {
		t.Fatalf("只扫出 %d 个测试函数,少得反常 —— 守卫可能没走到该走的目录", len(defined))
	}
	if len(refs) < 20 {
		t.Fatalf("只扫出 %d 处散文引用,少得反常 —— 提取正则可能读不懂现在的写法了", len(refs))
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
			for _, site := range sites {
				if claim, ok := activeGuardClaim(site.predicate()); ok && !mentionsRetirement(site.window(1, 1)) {
					t.Errorf("%s 在退场名单里(%s),而 %s 拿它当**现在时**用(名字后面紧跟着「%s」),"+
						"周围也没有一句话点明它已经退场。退场名单是给**历史记述**用的逃生口"+
						"(「那条已退场,由 X 接手」),不是给「由 <已删的测试> 钉住」这种断言用的 —— "+
						"后者与一句失效引用的后果完全一样:读到的人据此不再去检查那件事。"+
						"两条出路:① 说清它已经退场(写上「退场 / 已删 / 接手 / 取代 / 曾 / 此前」这类字样);"+
						"② 它其实还该有人守 ⇒ 点名今天真正在守的那条测试",
						name, why, site, claim)
				}
			}
			continue
		}
		if hasPrefixMatch(defined, name) {
			continue
		}
		t.Errorf("散文里点名了 %s,而全仓找不到这个测试(出现在 %s)。"+
			"两条出路:① 它改名了 ⇒ 把话改对;② 它是被**刻意退场**的历史记述 ⇒ "+
			"登记进 retiredTestNames 并写明为什么。一句「这件事有人守着」在失效之后"+
			"会让下一个人据此不再去检查那件事", name, joinSites(sites))
	}
}

// retiredTestNames 是**刻意退场**的测试:注释里点名它们是在讲历史(「这一条已由
// 集成台接手并退场」),不是在声称它们还在。值是理由。
//
// 登记是一次**显式动作**:删掉一条守卫时要在这里说清楚它被什么接手了,
// 而不是让那句注释悄悄变成一句失效引用。
//
// **它曾经是一个比想象中更大的洞,2026-09-12 收窄了一次。** 一次审计发现
// internal/stats/outcome.go 里写着「这是唯一一处跨包按字符串对齐的地方,
// 由 <某条已退场的测试> 钉住」—— 一句**现在时**的断言,而那个名字在这份名单里,
// 于是守卫全绿。也就是说:名字一旦进了这份名单,它就从「必须真的存在」变成了
// 「随便怎么用都行」,而这条守卫存在的全部理由恰恰是「别让一句假的『有人守着』
// 活下去」。那句话已经改对了,让它通过的机制由 activeGuardClaim 收窄。
var retiredTestNames = map[string]string{
	"TestRunTakesRuntimeBypassFromWiringNotAFrozenSlice": "已由集成台(-tags integration,真 Run + 真 netns + 真路由表)接手,逐条变异实测后退场;bypassrefresh_test.go 里那段退场说明记着经过",
	"TestRunWiresLiveMutatorToLiveBypassStore":           "同上,一并退场",
	"TestNoPackageWritesToLiveMutatorStore":              "同上,一并退场",
	"TestRunDaemonDoesNotDiscoverGatewayAtStartup":       "只禁一个函数名(discoverDaemonGateway),而换条路径去探网关它照样全绿(变异实测);由 TestHarnessRunDaemonStartsWithoutADefaultRoute 接手 —— 在没有默认路由的 netns 里真起 daemon,防的是那件事而不是那个名字",
	"TestUserSplitRulesArePrependedBeforeOverlayOnes":    "比较两个 for 循环在 run.go 里的位置,它自己写着「读源码是这里唯一够得着的办法」—— 判据已抽成 buildSplitRoutes,由 TestSplitRoutesPutUserRulesFirst 从行为上钉住(变异实测)",
	"TestUDPSourceNamesMatchTheDialer":                   "它守的是「两份拷贝还一样」;清单下沉到 internal/udpsource 之后漂移在构造上不可能,没有东西可守了。由 TestUDPSourceNamesComeFromTheSharedLeafPackage 部分接手 —— 它只钉 stats 那一侧的别名确实来自叶子包,dialer 那一侧今天没有守卫(理由写在 internal/stats/outcome.go 那几个常量头上)",
	"TestConfigWarningsReachTheStatusReport":             "它自己在测试函数里 append 一遍再断言那个局部变量,一次都没调用生产的组装逻辑;被 TestStatusReporterIncludesBothGuardAndConfigWarnings 取代,注释里点名它是在讲这段经过",
}

// —— 退场名单的现在时之门 ——
//
// **这是一张刻意很窄的网,而「窄」是判据不是妥协。** 本仓库明写:一条会误报的
// 闸门比没有闸门更糟,因为它训练人去删掉它。所以这里只咬**一种**写法:一个已经
// 退场的名字,**同一句话里紧跟着**一个断言「它此刻正在守着某件事」的词,而它
// 那一行前后也没有任何一句话点明它已经退场。
//
// **判定的粒度是「一句话」,不是「一段」—— 这是变异实测逼出来的,不是推演。**
// 第一版把免责词的窗口开到 ±3 行,结果把 outcome.go 那句话按出事那天的原样写
// 回去(`git show 590b5f2^`)之后**照样全绿**:那一段在事后被改对时补上了「当初
// 那条守卫因此退场」,而免责窗口够宽,恰好把新写回去的假话一并赦免了。也就是
// 说,一段同时讲着「它退场了」和「由它钉住」的文字,是这个功能最典型的失效现场
// ——**恰恰是最不该赦免的那一段**。现在动词只在名字**后面同一句话**里找,免责词
// 只在**相邻一行**里找。
//
// 两张表的形状仍然刻意不对称:
//   - activeGuardVerbs 是**极小的白名单**(只有几个真的在宣告「此刻有人守着」的
//     词),漏掉一种说法的代价只是这次没咬到;
//   - retirementMarkers 是**宽松的免责词表**,多认一个词的代价也只是这次没咬到。
//
// 两边都偏向「放过」,合起来是一张只捞那条最典型、后果最重的写法的网。免责词留着
// 是为了「这条**曾经**由 TestXxx 钉住,现在由 TestYyy 接手」这类真历史句子 ——
// 它句内就带着「曾经/接手」,不会被误伤。
//
// **拿本仓库真实的散文校准过(2026-09-12)**:全仓 12 处点名退场测试的地方
// (bypassrefresh_test.go 四处、runwiring_ast_test.go 两处、CLAUDE.md 的退役表
// 三行、harness_daemon_netns_linux_test.go、daemon_test.go、overlay_split_test.go
// 两处、outcome_test.go、control_reporter_test.go)一条都不红 —— 它们无一例外
// 是「那条守的是…」「已由集成台接手并退场」这类叙述,名字后面根本没有断言词。
//
// **明写它看不见什么**,免得下一个人以为这块已经封死:
//   - 动词在名字**前面**(「钉住这件事的是 TestXxx」);
//   - 动词被折到下一行(本仓库的注释换行很密,这是最可能漏掉的一种);
//   - 换一种说法(「有测试盯着」「这条由 X 保证」这类同义改写);
//   - 块注释 `/* … */` 里的散文(与 .go 那半同源:只看以 // 开头的行);
//   - 英文散文只认 "pinned by" 一种写法。
//
// 剩下的那些仍然只能靠人读。这条门要挡的是**最常见也最省事**的那一种:顺手写下
// 「由 TestXxx 钉住」而 TestXxx 早已不在。
var (
	activeGuardVerbs  = []string{"钉住", "钉死", "守着", "守住", "盯着", "pinned by"}
	retirementMarkers = []string{
		"退场", "退役", "已删", "删掉", "接手", "取代", "曾", "此前", "不再", "历史", "记档",
		"retired", "replaced", "superseded",
	}
)

// activeGuardClaim 判断这一小段散文里有没有「此刻有人守着」的断言,并把命中的
// 那个词报出来 —— 报出来是为了让失败信息指得动:一句「附近出现『钉住』」比
// 「这里有问题」省下下一个人一次通读。
func activeGuardClaim(prose string) (string, bool) {
	for _, v := range activeGuardVerbs {
		if strings.Contains(prose, v) {
			return v, true
		}
	}
	return "", false
}

// mentionsRetirement 判断这一小段散文有没有点明「这条已经不在了」。
//
// 窗口比 activeGuardClaim 那个**宽一点**(相邻一行 vs 同一句话),方向是刻意的:
// 免责要比判定容易命中。但它**不再是一整段** —— 见上面那段为什么。
func mentionsRetirement(prose string) bool {
	for _, m := range retirementMarkers {
		if strings.Contains(prose, m) {
			return true
		}
	}
	return false
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

// swiftProseRoots 是要扫的 Swift 源码根,相对仓库根。
//
// **它是个变量而不是内联字面量,是为了让「守卫自己瞎了」这条路可被变异验证** ——
// 把它指到一个不存在的目录,scanSwiftProse 必须 t.Fatal 而不是安静地扫出零个文件。
var swiftProseRoots = []string{"apps"}

// minSwiftProseFiles 是「确实扫到了 Swift」的下限。
//
// 今天 apps/ 下(除去 .build)有 60 多个 .swift。取 25 是因为这条数字要挡的是
// 「一个文件都没扫到」而不是「少了几个文件」:一条会因为正常删文件而变红的下限
// 是假红的来源,而假红的守卫会被下一个人删掉。
const minSwiftProseFiles = 25

// scanTestNames 走一遍全仓的 .go 文件 + CLAUDE.md + apps/ 下的 Swift,
// 收「定义了哪些测试」与「散文里点名了谁」。
//
// **只看以 // 开头的行(.go 与 .swift 同款,`///` 也算)。** 测试代码里
// `t.Run("TestFoo")` 之类的字符串不算引用,而这条守卫要管的恰恰是**散文**里的断言。
func scanTestNames(t *testing.T, root string) (map[string]bool, map[string][]proseSite) {
	t.Helper()
	defined := map[string]bool{}
	refs := map[string][]proseSite{}
	var skipped []string
	lessonFiles := 0
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
			if skippedScanDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		isGo := strings.HasSuffix(path, ".go")
		// **CLAUDE.md 也算「关于代码的陈述」,而且是最重的那一份。**
		//
		// 它点名了 40 多个测试,此前**一个守卫都没有** —— 而它恰恰是下一个人
		// (或下一个 agent)开工前唯一会通读的东西。一句「这件事由 TestXxx 钉住」
		// 在改名之后原样活着,后果与注释里那种完全一样:读到的人据此不再去检查
		// 那件事。范围刻意只加这一份,不含 docs/superpowers/{specs,plans} ——
		// 与 TestDocumentedFilePathsExist 同一条:计划书是**有日期的意图记录**,
		// 它点名的是「将要建的东西」,失效是预期的,拉进来只会制造假红。
		//
		// **2026-09-13:范围扩到 `docs/lessons/`。** 那天把 CLAUDE.md 里一个 79KB
		// 的单行列表项(占全文 22%)拆开,把八节施工日志搬进 docs/lessons/ ——
		// **而搬迁本身就制造了一个盲区**:那 27k 字符里点名的每一条测试,从此不再
		// 被任何东西检查。变异实测过:往 lessons 里写一句「由 <某个不存在的测试名>
		// 钉住」,扩范围前整条守卫全绿、扩范围后当场转红。(这里不敢写出那个名字 ——
		// 这条守卫连注释里的都认,实测被自己咬过一次,而那正是它在正常工作。)
		//
		// lessons 与 specs/plans 的区别是**时态**,不是位置:计划书写的是「将要建
		// 的东西」,失效是预期的;lessons 写的是**已经发生的事实**,和 CLAUDE.md
		// 一样承重,只是按「判据 / 过程」分了家。一份搬出去就没人看管的过程记录,
		// 比留在 CLAUDE.md 里更糟 —— 它同样会被读到,却不再会被证伪。
		// **2026-09-23:任何目录下的 CLAUDE.md 都算。** 此前只认根目录那一份;判据
		// 开始下沉到代码目录之后,只认根目录就等于让下沉出去的那一份无人看管。
		isDoc := filepath.Base(path) == "CLAUDE.md"
		//
		// **2026-09-13 同日再扩一次:`docs/` 下除 `superpowers/` 外的 .md 全进来。**
		// 起因是那天又长出 `docs/acceptance-pending.md`(待人工验收清单)—— 它同样
		// 点名测试、点名文件,同样会被人当作现状读。**按目录白名单逐个加,漏掉的
		// 那一份就是下一个无人看管的文档**;改成「除 superpowers 之外」之后,
		// 以后新加的文档自动进范围,不需要有人记得回来改这里。
		if !isDoc && strings.HasSuffix(path, ".md") {
			rel, relErr := filepath.Rel(filepath.Join(root, "docs"), path)
			inDocs := relErr == nil && !strings.HasPrefix(rel, "..")
			isPlan := strings.HasPrefix(rel, "superpowers"+string(filepath.Separator))
			if inDocs && !isPlan {
				isDoc = true
				lessonFiles++
			}
		}
		if !isGo && !isDoc {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		if isGo {
			for _, m := range testFuncRe.FindAllStringSubmatch(src, -1) {
				defined[m[1]] = true
			}
		}
		rel, _ := filepath.Rel(root, path)
		// .go 里只看注释行(`t.Run("TestFoo")` 那种字符串不算引用);
		// CLAUDE.md 整份都是散文,每一行都算。
		collectProseRefs(refs, rel, src, isGo)
		return nil
	})
	if err != nil {
		t.Fatalf("走仓库: %v —— 一个 .go 文件读不到就响亮失败,不静默放行", err)
	}
	if len(skipped) > 0 {
		// 静默跳过的守卫与静默通过的守卫是同一个问题,所以跳过要留痕。
		t.Logf("走不进去、已跳过的非 Go 路径(%d 条):%s", len(skipped), strings.Join(skipped, ", "))
	}
	// **docs/lessons/ 存在却一个 .md 都没扫到 ⇒ 响亮失败**,与 scanSwiftProse
	// 那两道同一条纪律:一条安静地扫了零个文件的守卫,与没有这条守卫在输出上完全
	// 一样,而它看起来更让人放心。目录**不存在**是另一回事(没搬过东西),放行。
	if fi, statErr := os.Stat(filepath.Join(root, "docs")); statErr == nil && fi.IsDir() && lessonFiles == 0 {
		t.Fatal("docs/ 在,却一个 .md 都没被收进散文引用表 —— " +
			"**这条守卫自己坏了,先修它**:搬出 CLAUDE.md 的过程记录从此无人看管")
	}
	t.Logf("docs 散文(不含 superpowers):扫了 %d 个 .md", lessonFiles)
	scanSwiftProse(t, root, refs)
	return defined, refs
}

// scanSwiftProse 把 apps/ 下的 Swift 散文收进同一张引用表。
//
// **它够不着自己要扫的东西时必须响亮失败,这是第 1 要求不是讲究。** 一条安静地
// 扫了零个文件的守卫,与没有这条守卫在输出上完全一样,而它看起来更让人放心 ——
// 本仓库为这个形状栽过不止一次(一条永远走不到的反向断言绿了很久)。
// 故这里有两道:走不进去 ⇒ t.Fatal;走进去了但一个 .swift 都没见着 ⇒ 也 t.Fatal。
func scanSwiftProse(t *testing.T, root string, refs map[string][]proseSite) {
	t.Helper()
	seen := 0
	for _, sub := range swiftProseRoots {
		swiftRoot := filepath.Join(root, sub)
		err := filepath.Walk(swiftRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				// 根本身走不进去 ⇒ 守卫瞎了,直接把错误抬上去。
				if path == swiftRoot {
					return err
				}
				// .swift 读不到同样不许静默放行。
				if strings.HasSuffix(path, ".swift") {
					return err
				}
				// 别的(权限古怪的构建产物之类)跳过并留痕:为它整条守卫变红
				// 是假红,而这一趟的下限由 minSwiftProseFiles 另外兜着。
				rel, _ := filepath.Rel(root, path)
				t.Logf("Swift 扫描:走不进去、已跳过 %s(%v)", rel, err)
				if info != nil && info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if info.IsDir() {
				// .build 里是 SwiftPM 的生成物(DerivedSources 之类),
				// 它不是任何人写的散文,扫它只会制造噪声。
				if skippedScanDir(info.Name()) || info.Name() == ".build" || info.Name() == ".swiftpm" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".swift") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			seen++
			rel, _ := filepath.Rel(root, path)
			collectProseRefs(refs, rel, string(b), true)
			return nil
		})
		if err != nil {
			t.Fatalf("扫 Swift 散文根 %s 失败:%v —— **这条守卫自己坏了,先修它**。"+
				"够不着要扫的源码时安静通过,与没有这条守卫在输出上完全一样,"+
				"而它看起来更让人放心", sub, err)
		}
	}
	if seen < minSwiftProseFiles {
		t.Fatalf("apps/ 下只扫到 %d 个 .swift(下限 %d)—— **这条守卫自己坏了,先修它**。"+
			"菜单是全仓测试覆盖最薄、读源码守卫最多的一块,「这件事由 TestXxx 钉住」"+
			"在那里最承重;扫不到它等于这半边根本没有守卫", seen, minSwiftProseFiles)
	}
	t.Logf("Swift 散文:扫了 %d 个文件(根:%s)", seen, strings.Join(swiftProseRoots, ", "))
}

func skippedScanDir(base string) bool {
	return base == ".git" || base == "vendor" || base == "node_modules"
}

// collectProseRefs 把一份源码里散文中点名的测试收进 refs。
// commentsOnly 为真时只看以 // 开头的行(.go 与 .swift),否则整份都算(CLAUDE.md)。
func collectProseRefs(refs map[string][]proseSite, rel, src string, commentsOnly bool) {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if commentsOnly && !strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		// 用 …Index 而不是 …Submatch:退场名单那道门要看「名字**后面**同一句话里
		// 说了什么」,所以必须记住这次命中落在行内哪个位置。同一行点名两次时,
		// 两次的判据各归各的。
		for _, m := range testRefRe.FindAllStringSubmatchIndex(line, -1) {
			name := line[m[2]:m[3]]
			refs[name] = append(refs[name], proseSite{rel: rel, line: i + 1, after: m[3], lines: lines})
		}
	}
}

// proseSite 是一次点名的落点。它带着**整份文件的行**(共享同一个切片,不复制),
// 因为退场名单那道门要看邻近几行 —— 只记 "文件:行号" 的话,判「这句话是不是
// 现在时」就得再读一遍盘,而那正是判据与取数分家的开始。
type proseSite struct {
	rel   string
	line  int // 1 起
	after int // 名字在这一行里结束的字节位置
	lines []string
}

func (s proseSite) String() string { return s.rel + ":" + itoa(s.line) }

// predicate 取名字后面**同一句话**的那一小段 —— 即「关于这个名字,这里断言了什么」。
//
// 在第一个句读符号处截断,是为了别把下一句话的断言按到这个名字头上
// (「提到了 TestX。另一件事由 TestY 钉住」不该判 TestX 有罪);再加一个字符上限,
// 是为了别让一行很长的散文把一个隔了半行远的动词也算进来。
func (s proseSite) predicate() string {
	line := s.lines[s.line-1]
	if s.after >= len(line) {
		return ""
	}
	tail := line[s.after:]
	if i := strings.IndexAny(tail, "。;;!?!?"); i >= 0 {
		tail = tail[:i]
	}
	const maxPredicateRunes = 40
	if r := []rune(tail); len(r) > maxPredicateRunes {
		tail = string(r[:maxPredicateRunes])
	}
	return tail
}

// window 取这次点名前后各若干行拼成的一段散文。
func (s proseSite) window(before, after int) string {
	lo := s.line - 1 - before
	if lo < 0 {
		lo = 0
	}
	hi := s.line + after
	if hi > len(s.lines) {
		hi = len(s.lines)
	}
	return strings.Join(s.lines[lo:hi], "\n")
}

func joinSites(sites []proseSite) string {
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		out = append(out, s.String())
	}
	return strings.Join(out, ", ")
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
