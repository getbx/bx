package cli

import (
	"strings"
	"testing"
)

// 规则窗口那句「Your own rules (N)」必须由**这一轮服务端说的模式**算出来。
//
// 模式改变的是那份列表的含义:global 下用户那几条 direct 规则**就是**全部的直连
// 集合;split 下它们是叠在一万两千条内建 china 列表之上的例外。同一份列表,两种
// 意思 —— 而这个混淆真实造成过一次错判(体检把 22 条正在工作的规则报成「被 china
// 列表覆盖」,而那台机器是 global、那份列表整个不生效,照着删会让 22 个域名改走隧道)。
//
// **判据钉的是到达的值,不是「那个函数被调用过」** —— 第七种失效写法。写死
// true/false 就是没看服务端说什么先宣布模式。
func TestMacMenuRulesWindowHeadingIsFedTheServersMode(t *testing.T) {
	main := stripSwiftComments(menuMainSwiftSource(t))
	for _, want := range []string{"global: list.global"} {
		if !strings.Contains(main, want) {
			t.Fatalf("main.swift 里没有 %q —— 那句标题没有接上服务端说的模式", want)
		}
	}
	for _, forbidden := range []string{"global: true", "global: false"} {
		if strings.Contains(main, forbidden) {
			t.Fatalf("main.swift 里有 %q:模式被写死了", forbidden)
		}
	}
	window := stripSwiftComments(readMenuSwiftSource(t, "RulesWindow.swift"))
	if !strings.Contains(window, "customRulesHeading(count:") {
		t.Fatal("规则窗口没有用 customRulesHeading —— 标题又回到了自己拼")
	}
	if !strings.Contains(window, "global: lastGlobal") {
		t.Fatal("标题没有读这一轮收下的模式")
	}
	// 收下新数据的地方必须把模式一起收下,否则窗口会拿上一轮的模式画这一轮的列表。
	if !strings.Contains(window, "lastGlobal = global") {
		t.Fatal("adoptFreshRules 没有收下模式")
	}
}
