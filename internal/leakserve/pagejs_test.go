package leakserve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// **区段里定义的每一个函数,页面都必须真的在用。**
//
// 这条是本文件里最要紧的一条:一个被测试盖住、而生产路径上没人调用的纯函数,
// 与没有测试**在输出上完全一样**,而它看起来更让人放心 —— 这个仓库为
// 「守卫钉住的是缺陷旁边的东西」栽过六次,这正是那个形状。
func TestPageJSCallsEveryPureFunctionItDefines(t *testing.T) {
	region := pureRegion(t)
	page := pageSource(t)
	outside := strings.Replace(page, region, "", 1)

	var defined []string
	for _, line := range strings.Split(region, "\n") {
		line = strings.TrimSpace(line)
		const kw = "function "
		if !strings.HasPrefix(line, kw) {
			continue
		}
		name := line[len(kw):]
		if i := strings.Index(name, "("); i >= 0 {
			name = name[:i]
		}
		if name != "" {
			defined = append(defined, name)
		}
	}
	if len(defined) == 0 {
		t.Fatal("BX-PURE 区段里一个函数都没解析出来 —— 守卫读不懂现在的写法了,先修守卫")
	}
	for _, name := range defined {
		if !strings.Contains(outside, name+"(") {
			t.Errorf("%s 在区段里定义了,页面别处却从不调用它 —— "+
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
	wired := false
	for _, arg := range calls {
		got := strings.TrimSpace(arg)
		if got == "true" {
			t.Errorf("probeLanded 的第二个实参是字面量 true —— " +
				"「落地了」必须从答案里算出来。字面量 false 可以(那只出现在 catch 里," +
				"什么都没到达),true 不行:那正是空 body 那个 bug 的一般形式")
		}
		if strings.Contains(got, ".landed") {
			wired = true
		}
	}
	if !wired {
		t.Error("没有一处 probeLanded 用的是纯函数算出的 .landed —— " +
			"node 那边测的极性没接到界面上,测了等于没测")
	}
}

// probeLandedArgs 取出每一次 probeLanded 调用的**第二个**实参原文。
// 读不懂就让调用方响亮失败,不静默返回空列表(空列表会让上面那条守卫自动通过)。
func probeLandedArgs(t *testing.T, src string) []string {
	t.Helper()
	const call = "probeLanded("
	var out []string
	for i := 0; ; {
		j := strings.Index(src[i:], call)
		if j < 0 {
			return out
		}
		start := i + j + len(call)
		depth, comma, end := 0, -1, -1
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
				if depth == 0 && comma < 0 {
					comma = k
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			t.Fatalf("在偏移 %d 处的 probeLanded 调用没有闭合括号 —— 守卫读不懂它", start)
		}
		// 只收真正的**调用**:定义那一行 `function probeLanded(name, ok)` 的第二个
		// 形参恰好也叫得出名字,把它算进来会让「至少四处」这条计数变松。
		if comma > 0 && comma < end && !strings.HasSuffix(strings.TrimSpace(src[:i+j]), "function ") {
			out = append(out, src[comma+1:end])
		}
		i = end
	}
}
