package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/supervisor"
)

// 菜单那份码清单必须与 Go 常量**逐字相同**,两个方向都查。
//
// 少一个码 = 一段用户永远读不到的话:`coreStartFailureHint` 的 switch 落进
// default、返回 nil、菜单退回一句只有 code= 的话,而两侧测试都不会红。
// 多一个不存在的码 = 一段永远不会被执行、却被测试盖着的死代码(本仓库为这个
// 形状栽过)。
//
// 判据打在 switch 的 **case 字面量**上(去掉注释与字符串之后仍然是字面量 ——
// blankSwiftStringLiterals 会把 case 后面那个串抹白,所以这里读的是原文,
// 但先剥注释:上面那段说明里就写着好几个码名)。
func TestMacMenuStartFailureCodesMatchTheGoConstants(t *testing.T) {
	source := stripSwiftComments(menuToggleControllerSource(t))
	body, ok := swiftFunctionBody(source, "func coreStartFailureHint(code: String?, servers: CoreStartFailureServers) -> String? {")
	if !ok {
		t.Fatal("ToggleController.swift 里找不到 coreStartFailureHint —— 锚点漂了,回来重判,别静默放行")
	}
	caseLiteral := regexp.MustCompile(`case "([a-z0-9_]+)":`)
	var inSwift []string
	for _, match := range caseLiteral.FindAllStringSubmatch(body, -1) {
		inSwift = append(inSwift, match[1])
	}
	if len(inSwift) == 0 {
		t.Fatal("coreStartFailureHint 里一个 case 字面量都没读出来 —— 守卫认不出现在的代码")
	}

	want := map[string]bool{}
	for _, code := range supervisor.StartFailureCodes() {
		want[code] = true
	}
	got := map[string]bool{}
	for _, code := range inSwift {
		if !want[code] {
			t.Errorf("菜单里的 %q 不是 supervisor 的启动失败码 —— 一段永远不会被执行、\n"+
				"却被测试盖着的死代码", code)
		}
		got[code] = true
	}
	var missing []string
	for code := range want {
		if !got[code] {
			missing = append(missing, code)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("菜单没给这些码任何说法:%v —— 它们会落进 default 返回 nil,\n"+
			"用户拿到的还是那句只有 code= 的话,而两侧测试都不会红", missing)
	}
}

// 前缀也不许两边各写一份。
func TestMacMenuStartFailureCodePrefixMatchesGuardian(t *testing.T) {
	source := stripSwiftComments(menuToggleControllerSource(t))
	if !strings.Contains(source, `let coreStartFailureCodePrefix = "core_"`) {
		t.Fatalf("菜单那份前缀常量不再是 %q —— Guardian 那边加的正是它(coreStartFailureLastError),\n"+
			"两边不同就等于这一族码在菜单里全部认不出来", "core_")
	}
	if coreStartFailureCodePrefix != "core_" {
		t.Fatalf("Go 侧前缀是 %q —— 两边必须一致", coreStartFailureCodePrefix)
	}
}

// main.swift 真的把服务器事实喂进去了。
//
// 判据两层,缺一不可:
//   - `toggleResultText(` 那次调用的 `servers:` 实参**不是**一个当场造出来的
//     空 `CoreStartFailureServers()` —— 写死一个空清单会让那句话永远不点名
//     服务器、永远不说「你还配了另一台」,而界面看起来完全正常(本仓库对
//     `probeLanded(probe, true)` 那类「没看答案就先宣布」罚过多次);
//   - 那个实参的值确实来自一次 `listServers()`。
func TestMacMenuFeedsTheStartFailureHintRealServerFacts(t *testing.T) {
	body, ok := swiftFunctionBody(
		stripSwiftComments(menuMainSwiftSource(t)),
		"private func performToggle(_ action: ToggleAction, completion: ((Bool) -> Void)? = nil) {")
	if !ok {
		t.Fatal("main.swift 里找不到 performToggle —— 锚点漂了,回来重判")
	}
	// 判据锚在 **toggleResultText 那一次调用的实参**上,不是「函数体里出现过
	// servers:」—— 后者会被同一个函数里别处的 `servers:` 满足(本仓库
	// 「钉标识符而性质是关于别处的」那一类)。
	calls := swiftCallArguments(body, "toggleResultText")
	if len(calls) == 0 {
		t.Fatal("performToggle 里找不到 toggleResultText( —— 锚点漂了,回来重判")
	}
	call := regexp.MustCompile(`servers:\s*([A-Za-z_][A-Za-z0-9_]*)`)
	match := call.FindStringSubmatch(calls[0])
	if match == nil {
		t.Fatalf("toggleResultText 没有收到 servers: —— 那句话永远说不出是哪台服务器,\n"+
			"也永远不会点名另一台。实参:%s", calls[0])
	}
	binding := match[1]
	if binding == "CoreStartFailureServers" {
		t.Fatal("servers: 传的是一个当场造出来的空清单 —— 那与不传在输出上完全一样,\n" +
			"而界面看起来完全正常")
	}
	if !strings.Contains(body, "listServers()") {
		t.Fatal("performToggle 一次都没去问服务器清单 —— lastServers 只在服务器窗口开着时\n" +
			"才刷新,靠它等于这半边几乎永远说不出地址")
	}
	if !regexp.MustCompile(`\b` + regexp.QuoteMeta(binding) + `\s*=\s*coreStartFailureServers\(`).MatchString(body) {
		t.Fatalf("%s 不是由 coreStartFailureServers(…) 折出来的 —— 判定(谁是当前那台、\n"+
			"host:port 怎么拼)必须在那个被测的纯函数里,不许在 main.swift 里再写一遍", binding)
	}
}

func menuToggleControllerSource(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "ToggleController.swift"))
	if err != nil {
		t.Fatalf("读不到 ToggleController.swift:%v", err)
	}
	return string(source)
}
