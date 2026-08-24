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
