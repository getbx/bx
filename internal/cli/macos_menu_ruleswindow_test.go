package cli

import (
	"strings"
	"testing"
)

// 这张表的判据全在纯模型里,窗口只摆 —— 窗口自己算一次分类,就没有任何测试
// 盯着它了(这个仓库为「判据落进 AppKit 那半」栽过)。
func TestMacMenuRulesWindowRendersByThePureModel(t *testing.T) {
	window := blankSwiftStringLiterals(stripSwiftComments(readMenuSwiftSource(t, "RulesWindow.swift")))
	for _, want := range []string{"row.detail", "NSButton(title: "} {
		if !strings.Contains(window, want) {
			t.Errorf("规则表缺 %s", want)
		}
	}
	// 窗口不许自己再算一遍排序或分类。
	for _, forbidden := range []string{"ruleRowSeverity(", "sorted("} {
		if strings.Contains(window, forbidden) {
			t.Errorf("窗口里出现了 %s —— 判据该在 RulesModel 里", forbidden)
		}
	}
	// 分类词是**字符串字面量**,而上面那份源码已经把字面量内容抹白了(那是数
	// 括号的前置)—— 在它上面查 "risky_direct" 永远查不到,是一条不可达的断言。
	// 故这一条单独从「只剥注释、保留字面量」的那份上取:注释里提一句不算数,
	// 真的写进代码里才算。
	windowText := stripSwiftComments(readMenuSwiftSource(t, "RulesWindow.swift"))
	if strings.Contains(windowText, "risky_direct") {
		t.Error("窗口里出现了 risky_direct —— 分类的判据该在 RulesModel 里")
	}
}

// 删除走 Guardian 的 remove,并且**删完那一行不消失**:原地留一句 Removed · Undo。
// 不弹确认框是刻意的(为 11 条冗余点 11 次确认是在惩罚正确的行为),但静默且
// 不可逆地毁掉一条手写规则不行。
func TestMacMenuRuleRemovalIsUndoable(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "private func removeRuleFromWindow(_ kind: RuleKind, _ pattern: String)")
	if !ok {
		t.Fatal("读不出 removeRuleFromWindow 的函数体 —— 守卫已失效,先修守卫")
	}
	for _, want := range []string{"changeRule(action: ", "markRemoved(", "showGuardianFailure(title: "} {
		if !strings.Contains(body, want) {
			t.Errorf("removeRuleFromWindow 缺 %s", want)
		}
	}
	window := blankSwiftStringLiterals(stripSwiftComments(readMenuSwiftSource(t, "RulesWindow.swift")))
	if !strings.Contains(window, "onUndoRemove?(") {
		t.Error("Undo 没有出口")
	}
	if !strings.Contains(code, "controller.onRemoveRule = ") || !strings.Contains(code, "controller.onUndoRemove = ") {
		t.Error("窗口的删除/撤销回调没接到 main.swift")
	}
}

// Add Rule… 收到 409 时**不关 sheet**:把用户输入留着让他改窄,才是这道门的
// 意义。「仍然添加」是次要动作,不是主路。
func TestMacMenuAddRuleKeepsTheSheetOnRefusal(t *testing.T) {
	code := menuMainSwiftCode(t)
	// 失败码是字符串字面量,查它必须在**没有抹白字面量**的那份上(见
	// TestMacMenuRulesWindowRendersByThePureModel 里同一条说明);注释仍然
	// 剥掉,一句注释里提到 rules_risky_direct 不算接上了这条路。
	body, ok := swiftFunctionBody(stripSwiftComments(menuMainSwiftSource(t)), "private func addRuleFromWindow()")
	if !ok {
		t.Fatal("读不出 addRuleFromWindow 的函数体")
	}
	for _, want := range []string{"rules_risky_direct", "force: true"} {
		if !strings.Contains(body, want) {
			t.Errorf("addRuleFromWindow 缺 %s —— 风险门的重试路径没接上", want)
		}
	}
	if !strings.Contains(code, "controller.onAddRule = ") || !strings.Contains(code, "self?.addRuleFromWindow()") {
		t.Error("Add Rule… 没接到 main.swift")
	}
}
