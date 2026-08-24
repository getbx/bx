package leakserve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/leakcheck"
)

// —— 页面里那段纯解析 JS 的守卫(2026-08-24)——
//
// 这半个文件此前**一行测试都盖不到** —— page.html 自己那段注释写着「this repo's
// tests cannot see JS — so a drift would be silent」。断言本身由
// scripts/test-page-js.sh 交给 node 跑(与 Swift 那半边同一个形状:Go 进不去的语言
// 单独一个运行器,verify.sh 挂闸门)。**本文件守的是那个运行器够得着、而且它测的
// 东西确实在生产路径上** —— 那两件事 node 自己证明不了。

const (
	pureBegin = "==== BX-PURE-BEGIN ===="
	pureEnd   = "==== BX-PURE-END ===="
)

func pageSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("page.html")
	if err != nil {
		t.Fatalf("读 page.html: %v —— 守卫读不到它要守的东西,如实失败", err)
	}
	return string(b)
}

func pureRegion(t *testing.T) string {
	t.Helper()
	s := pageSource(t)
	i := strings.Index(s, pureBegin)
	j := strings.Index(s, pureEnd)
	if i < 0 || j < 0 || j <= i {
		t.Fatalf("page.html 里找不到成对的 %s / %s —— 抽取脚本会拿到空的一段,"+
			"而空段在 node 里不是语法错、是「什么都没测」", pureBegin, pureEnd)
	}
	return s[i+len(pureBegin) : j]
}

// 区段必须是纯的。纪律与 internal/leakcheck 的 purity_test.go 同源:判据是**禁
// 标识符**,而不是「看起来像不像纯函数」。
//
// 为什么承重:抽取脚本把这段丢给 node 直接跑,而 node 里没有 document/window。
// 一旦有人往里加一句 DOM 操作,断言会在**加载期**就 ReferenceError —— 那是吵的。
// 真正危险的是加一句 `setTimeout`/`fetch` 这类 node 里**恰好也存在**的东西:
// 脚本照跑不误,而那个函数已经不再是纯解析了,它的失败模式重新回到了没人看得见
// 的那一类。
func TestPageJSPureRegionHasNoBrowserOrIODependencies(t *testing.T) {
	// **先剥注释。** 这一段的注释里就写着「不许出现 document / window / fetch」——
	// 不剥就是恒红,而**假红比假绿更致命**:一条永远红的守卫会被下一个人直接删掉,
	// 那等于没有守卫(本仓库为 stripSwiftComments 那次栽过同一个坑)。
	region := stripJSComments(pureRegion(t))
	banned := []string{
		"document", "window", "navigator", "location",
		"fetch(", "XMLHttpRequest", "RTCPeerConnection",
		"setTimeout", "setInterval", "Date.now", "Math.random",
		"localStorage", "sessionStorage", "postMessage",
	}
	for _, id := range banned {
		if strings.Contains(region, id) {
			t.Errorf("BX-PURE 区段里出现了 %q —— 它就不再是纯解析了。"+
				"判定与 I/O 都留在区段外(判定其实全在 Go 里)", id)
		}
	}
	// 模板占位符会让抽出来的那段不是合法 JS(node 直接语法错)。
	if strings.Contains(region, "{{") {
		t.Error("BX-PURE 区段里有模板占位符 —— 抽出来交给 node 会是语法错")
	}
}

// **区段里定义的每一个函数,都必须从区段外面**可达**。**
//
// 这条是本文件里最要紧的一条:一个被测试盖住、而生产路径上没人调用的纯函数,
// 与没有测试**在输出上完全一样**,而它看起来更让人放心 —— 这个仓库为
// 「守卫钉住的是缺陷旁边的东西」栽过六次,这正是那个形状。
//
// **判据是「可达」而不是「被外面直接调用」**(2026-08-24 改)。第一版要求直接调用,
// 而那会逼出一个坏的分解:纯函数之间互相组合恰恰是对的(bxTraceOutcome 调
// bxParseTrace),按直接调用判会把这种写法判成假红,于是下一个人要么把组合拆开、
// 要么把守卫删掉 —— 两条路都比现在差。可达性是那个真正要守的性质:从页面的
// 生产路径出发,顺着调用能不能走到它。
func TestPageJSCallsEveryPureFunctionItDefines(t *testing.T) {
	region := stripJSComments(pureRegion(t))
	page := pageSource(t)
	outside := stripJSComments(strings.Replace(page, pureRegion(t), "", 1))

	defined := map[string]string{} // 函数名 -> 函数体(粗略取到下一个定义为止)
	var order []string
	lines := strings.Split(region, "\n")
	cur := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		const kw = "function "
		if strings.HasPrefix(trimmed, kw) {
			name := trimmed[len(kw):]
			if i := strings.Index(name, "("); i >= 0 {
				name = name[:i]
			}
			if name != "" {
				cur = name
				defined[cur] = ""
				order = append(order, cur)
				continue
			}
		}
		if cur != "" {
			defined[cur] += line + "\n"
		}
	}
	if len(order) == 0 {
		t.Fatal("BX-PURE 区段里一个函数都没解析出来 —— 守卫读不懂现在的写法了,先修守卫")
	}

	// 从外面直接调到的那些开始,顺着区段内部的调用往下走。
	reachable := map[string]bool{}
	var queue []string
	for _, name := range order {
		if strings.Contains(outside, name+"(") {
			reachable[name] = true
			queue = append(queue, name)
		}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, other := range order {
			if reachable[other] || other == name {
				continue
			}
			if strings.Contains(defined[name], other+"(") {
				reachable[other] = true
				queue = append(queue, other)
			}
		}
	}
	for _, name := range order {
		if !reachable[name] {
			t.Errorf("%s 在区段里定义了,而从页面的生产路径**走不到它** —— "+
				"一个没人调用而测试盖着的函数,与没有测试在输出上完全一样", name)
		}
	}
}

// 抽取脚本与 verify.sh 的闸门都必须真的存在并指向这里。判据是**清单**(哪个文件、
// 哪个哨兵),不是语义,所以文本匹配在这里是恰当的 —— 与
// internal/cli 那条「Swift 测试文件清单」守卫同一条理由。
func TestPageJSGateIsWiredIntoVerify(t *testing.T) {
	root := repoRootForPageJS(t)
	runner := filepath.Join(root, "scripts", "test-page-js.sh")
	b, err := os.ReadFile(runner)
	if err != nil {
		t.Fatalf("读 %s: %v —— 断言那一半没人跑了", runner, err)
	}
	script := string(b)
	for _, want := range []string{pureBegin, pureEnd, "page.html", "page js tests passed"} {
		if !strings.Contains(script, want) {
			t.Errorf("%s 里没有 %q —— 抽取或收尾横幅对不上", runner, want)
		}
	}

	vb, err := os.ReadFile(filepath.Join(root, "scripts", "verify.sh"))
	if err != nil {
		t.Fatalf("读 verify.sh: %v", err)
	}
	verify := string(vb)
	if !strings.Contains(verify, "test-page-js.sh") {
		t.Error("verify.sh 没挂这个闸门 —— 它就只会在有人手动跑的时候跑")
	}
	// 收尾横幅那半:退出码证明「没失败」,横幅证明「真跑过」。这个仓库实测过
	// 「脚本提前 exit 0 仍然退 0」,只靠退出码会一路绿灯。
	if !strings.Contains(verify, "page js tests passed") {
		t.Error("verify.sh 没检查收尾横幅 —— 脚本被清空或提前 exit 0 时它照样绿")
	}
}

func repoRootForPageJS(t *testing.T) string {
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

// stripJSComments 去掉 // 与 /* */ 注释,**保留字符串字面量原样**。
//
// 保留字符串是刻意的保守方向:一个把 "document" 写进字符串的写法会被判红。
// 今天这一段里没有那种字符串;真出现时,红是「多报」——而这条守卫要挡的是
// 「悄悄多了一次 I/O」,漏报的代价大得多。
func stripJSComments(src string) string {
	var out strings.Builder
	for i := 0; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], "//"):
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				return out.String()
			}
			out.WriteByte('\n')
			i += j + 1
		case strings.HasPrefix(src[i:], "/*"):
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				return out.String()
			}
			i += 2 + j + 2
		default:
			out.WriteByte(src[i])
			i++
		}
	}
	return out.String()
}

// **「落地了」必须是从答案里挣来的,绝不能直接断言。**
//
// 这条守卫钉的是 2026-08-24 修掉的那个 bug 的**形状**,不是它的一个实例:
// `fetchEcho` 的空 body 分支早退时跳过了 `probeLanded`,而 `.catch` 只对 throw
// 生效 —— 于是那个格子永远停在「还在等」的样子(`data-got` 缺席既不是 yes 也
// 不是 no),挨着一份已经发出并渲染完的报告。
//
// 判据是**不对称的,而这个不对称是要点**:
//   - 第二个实参写成字面量 `true` 一律禁 —— 那是在没看答案的情况下宣布探针
//     落地了,正是上面那个 bug 的一般形式。
//   - 字面量 `false` **允许**:它只出现在 `.catch` 里,那里什么都没到达,
//     说「没落地」不是对内容的判断,是对「一次异常」的如实陈述。
//
// 另一半同样承重:必须真的有人把纯函数算出的 `landed` 传进去。少了这一句,
// 把 `probeLanded(probe, o.landed)` 改回 `probeLanded(probe, true)` 之后
// node 那边测的 `bxEchoOutcome` 照样全绿 —— 被测的极性根本没接到界面上,
// 而这个仓库为「守卫钉住的是缺陷旁边的东西」栽过六次。
func TestPageJSNeverAssertsThatAProbeLanded(t *testing.T) {
	page := stripJSComments(pageSource(t))
	calls := probeLandedArgs(t, page)
	if len(calls) < 4 {
		t.Fatalf("只解析出 %d 处 probeLanded 调用,少得反常 —— 守卫可能读不懂现在的写法了,先修守卫", len(calls))
	}
	// **每一处「落地了」都必须来自纯区段。** 2026-08-24 起四个探针
	// (echo / trace / srflx / surface)全部转过来了,所以判据从「至少有一处」
	// 收紧成「每一处都是」—— 前者在三处内联表达式旁边照样绿,而那三处恰恰是
	// 没有任何测试盯着的地方。
	fromPure := 0
	for _, arg := range calls {
		got := strings.TrimSpace(arg)
		switch {
		case got == "false":
			// 允许:只出现在 catch 里,什么都没到达。说「没落地」不是对内容的
			// 判断,是对一次异常的如实陈述。
		case strings.Contains(got, ".landed") || strings.HasPrefix(got, "bx"):
			// 来自纯区段(直接返回的 landed,或一个 bxXxxLanded 判据)。
			fromPure++
		default:
			t.Errorf("probeLanded 的第二个实参是 %q —— 「落地了」必须在 BX-PURE "+
				"区段里算出来(那里有 node 的断言盯着)。内联一个表达式在这里,"+
				"它就回到了没有任何测试覆盖的状态;字面量 true 更是直接断言,"+
				"正是空 body 那个 bug 的一般形式", got)
		}
	}
	if fromPure < 4 {
		t.Errorf("只有 %d 处 probeLanded 用的是纯区段算出的极性,想要至少 4 处"+
			"(echo / trace / srflx / surface)—— 少的那个探针的极性没人测", fromPure)
	}
}

// probeLandedArgs 取出每一次 probeLanded 调用的**第二个**实参原文。
// 读不懂就让调用方响亮失败,不静默返回空列表(空列表会让上面那条守卫自动通过)。
func probeLandedArgs(t *testing.T, src string) []string {
	return callArgN(t, src, "probeLanded", 1)
}

// callArgN 取出每一次 `fn(...)` 调用的第 n 个实参原文(n 从 0 起)。
// 读不懂就让调用方响亮失败,不静默返回空列表 —— 空列表会让上层守卫自动通过。
func callArgN(t *testing.T, src, fn string, n int) []string {
	t.Helper()
	call := fn + "("
	var out []string
	for i := 0; ; {
		j := strings.Index(src[i:], call)
		if j < 0 {
			return out
		}
		start := i + j + len(call)
		depth, end := 0, -1
		var commas []int
		for k := start; k < len(src); k++ {
			switch src[k] {
			case '(', '[':
				depth++
			case ')':
				if depth == 0 {
					end = k
				} else {
					depth--
				}
			case ']':
				depth--
			case ',':
				if depth == 0 {
					commas = append(commas, k)
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			t.Fatalf("在偏移 %d 处的 %s 调用没有闭合括号 —— 守卫读不懂它", start, fn)
		}
		// 只收真正的**调用**:定义那一行 `function probeLanded(name, ok)` 的第二个
		// 形参恰好也是个标识符,把它算进来会让计数变松。
		//
		// **判据是 HasSuffix(…, "function") 而不是 "function "** —— TrimSpace 已经
		// 把尾空格去掉了,带空格的那版**永远不成立**,于是定义行一直被当成一次调用。
		// 它此前无害只是因为旧判据只认字面量 true 与 .landed,而形参名 `ok` 两个
		// 都不是;判据一收紧它就当场显形。
		if !strings.HasSuffix(strings.TrimSpace(src[:i+j]), "function") {
			lo, hi := start, end
			if n > 0 {
				if len(commas) < n {
					i = end
					continue
				}
				lo = commas[n-1] + 1
			}
			if len(commas) >= n+1 {
				hi = commas[n]
			}
			if lo <= hi {
				out = append(out, src[lo:hi])
			}
		}
		i = end
	}
}

// **探测名是一条跨语言契约,而在 2026-08-24 之前没有任何东西在核对它。**
//
// `internal/leakcheck/outline.go` 头上那句「**两边用同一组常量**,免得页面自己抄
// 一份」曾经是**假话**:页面拿到的 `CHECKS`(骨架)里的 inputs 确实来自 Go 常量,
// 但页面自己调用 `probeLanded("srflx", …)` / `fetchEcho(ECHO4, "exit_v4")` 用的是
// **手抄的字面量** —— `pageData` 里根本没有这几个常量(由
// TestPageDataCarriesOnlyTokenDisclosureAndSkeleton 穷举钉住)。
//
// 漂移的后果是**静默的**:`skeleton()` 按 `c.inputs`(Go 那份)建 `cells`,而
// `probeLanded` 按页面那份查表 —— 对不上时 `cells[name]` 是 undefined,
// `(cells[name] || []).forEach` 什么也不做,那一格于是**永远停在「还在等」**,
// 而 Go 侧全部测试照样绿(它们用的是常量),页面侧也不知道 Go 改过名。
//
// 两个方向都要查:
//   - 页面用的每一个名字都必须是真的常量(写错一个字符 ⇒ 那一格永不点亮);
//   - 每一个常量都必须在页面里被用到(没人点亮 ⇒ 同样是永远等下去的那一格)。
func TestPageProbeNamesMatchTheGoConstants(t *testing.T) {
	page := stripJSComments(pageSource(t))

	names := map[string]bool{}
	for _, arg := range callArgN(t, page, "probeLanded", 0) {
		if lit, ok := jsStringLiteral(arg); ok {
			names[lit] = true
		}
	}
	// fetchEcho 的探测名在**调用点**上,不在 probeLanded 那一行(那里传的是形参)。
	for _, arg := range callArgN(t, page, "fetchEcho", 1) {
		if lit, ok := jsStringLiteral(arg); ok {
			names[lit] = true
		}
	}
	if len(names) == 0 {
		t.Fatal("页面里一个探测名字面量都没解析出来 —— 守卫读不懂现在的写法了,先修守卫")
	}

	real := map[string]bool{
		leakcheck.ProbeExitV4: true, leakcheck.ProbeExitV6: true,
		leakcheck.ProbeSRFLX: true, leakcheck.ProbeTrace: true,
		leakcheck.ProbeSurface: true,
	}
	for name := range names {
		if !real[name] {
			t.Errorf("页面用了探测名 %q,而 Go 侧没有这个常量 —— "+
				"cells[%q] 是 undefined,那一格永远停在「还在等」,两侧都不会报错", name, name)
		}
	}
	for name := range real {
		if !names[name] {
			t.Errorf("常量 %q 在页面里没人点亮 —— 骨架会为它摆出一格,而那一格"+
				"永远等不到落定", name)
		}
	}
}

// jsStringLiteral 把一个实参原文解成 JS 字符串字面量。不是字面量(变量、表达式)
// 就返回 false —— 那种实参这条守卫管不了,交给它自己的调用点去查。
func jsStringLiteral(arg string) (string, bool) {
	s := strings.TrimSpace(arg)
	if len(s) < 2 {
		return "", false
	}
	q := s[0]
	if (q != '"' && q != '\'') || s[len(s)-1] != q {
		return "", false
	}
	inner := s[1 : len(s)-1]
	if strings.ContainsRune(inner, rune(q)) || strings.Contains(inner, "\\") {
		// 带转义的字面量本守卫不解 —— 探测名是简单标识符,出现转义说明写法变了,
		// 与其猜,不如让它落到「不是字面量」而由上面的双向计数抓住。
		return "", false
	}
	return inner, true
}
