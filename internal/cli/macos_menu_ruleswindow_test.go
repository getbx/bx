package cli

import (
	"strings"
	"testing"
)

// menuRulesWindowSource 读窗口那份源码,两种视图各一次:
//   - blanked:字符串字面量内容被抹白(数括号的前置),查**标识符**用它;
//   - text:只剥注释、字面量原样,查**字面量**用它 —— 在 blanked 上查一个
//     字符串常量永远查不到,那是一条不可达的断言(本仓库明写过:一条在最需要
//     它时恰好不可达的断言,与没有这条断言完全一样,而它看起来更让人放心)。
func menuRulesWindowSource(t *testing.T) (blanked, text string) {
	t.Helper()
	text = stripSwiftComments(readMenuSwiftSource(t, "RulesWindow.swift"))
	return blankSwiftStringLiterals(text), text
}

// 这张表的判据全在纯模型里,窗口只摆 —— 窗口自己算一次分类或轻重,就没有
// 任何测试盯着它了(这个仓库为「判据落进 AppKit 那半」栽过)。
func TestMacMenuRulesWindowRendersByThePureModel(t *testing.T) {
	window, text := menuRulesWindowSource(t)
	// 副标题说什么、那句话轻还是重,都由纯函数给。
	for _, want := range []string{"row.detail", "ruleRowNoteIsSevere("} {
		if !strings.Contains(window, want) {
			t.Errorf("规则表缺 %s —— 判据该在 RulesModel 里,窗口只读它", want)
		}
	}
	// 窗口不许自己再算一遍排序或分类。
	for _, forbidden := range []string{"ruleRowSeverity(", "sorted("} {
		if strings.Contains(window, forbidden) {
			t.Errorf("窗口里出现了 %s —— 判据该在 RulesModel 里", forbidden)
		}
	}
	if strings.Contains(text, "risky_direct") {
		t.Error("窗口里出现了 risky_direct —— 分类的判据该在 RulesModel 里")
	}
	// **按钮要点名。** 只查 `NSButton(title: ` 的话,早就存在的 Show Config
	// 一个人就能满足它 —— 那样这条守卫对「这张表根本没长出来」毫无反应。
	for _, button := range []string{`NSButton(title: "Add Rule…"`, `NSButton(title: "Remove"`, `NSButton(title: "Undo"`} {
		if !strings.Contains(text, button) {
			t.Errorf("窗口里没有 %s", button)
		}
	}
}

// 删除走 Guardian 的 remove,**不弹确认框**(为十一条冗余点十一次确认是在惩罚
// 正确的行为),但删完那一行**不消失**:原地留一句 Removed · Undo —— 静默且
// 不可逆地毁掉一条手写规则不行。
func TestMacMenuRuleRemovalIsUndoable(t *testing.T) {
	code := menuMainSwiftCode(t)
	source := stripSwiftComments(menuMainSwiftSource(t))
	body, ok := swiftFunctionBody(source, "private func removeRuleFromWindow(_ kind: RuleKind, _ pattern: String)")
	if !ok {
		t.Fatal("读不出 removeRuleFromWindow 的函数体 —— 守卫已失效,先修守卫")
	}
	for _, want := range []string{
		`changeRule(action: "remove"`, "markRemoved(", "showGuardianFailure(title: ",
		// 小按钮被双击会发两次删除,第二次为一条**确实删成功了**的规则弹一句失败。
		"ruleRemovalsInFlight",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("removeRuleFromWindow 缺 %s", want)
		}
	}
	// **不弹确认框是刻意的**,而此前没有任何东西守着它:加一个确认框整套照样绿。
	if strings.Contains(body, "NSAlert(") {
		t.Error("removeRuleFromWindow 里建了 NSAlert —— 删一条规则不该弹确认框(批量清理会变成一行一个框)")
	}
	window, _ := menuRulesWindowSource(t)
	if !strings.Contains(window, "onUndoRemove?(") {
		t.Error("Undo 没有出口")
	}
	if !strings.Contains(code, "controller.onRemoveRule = ") || !strings.Contains(code, "controller.onUndoRemove = ") {
		t.Error("窗口的删除/撤销回调没接到 main.swift")
	}
}

// Add Rule… 收到 409 时**不拆表单**:同一组视图再摆一次,用户输入原样留着让他
// 改窄 —— 那才是这道门的意义。「Add Anyway」是次要出口,而且**只对刚刚被拒的
// 那一条有效**:为一把钥匙开的门对下一把也开着,正是这类门失效的形状。
func TestMacMenuAddRuleKeepsTheSheetOnRefusal(t *testing.T) {
	code := menuMainSwiftCode(t)
	source := stripSwiftComments(menuMainSwiftSource(t))
	entry, ok := swiftFunctionBody(source, "private func addRuleFromWindow()")
	if !ok {
		t.Fatal("读不出 addRuleFromWindow 的函数体")
	}
	if !strings.Contains(entry, "askForNewRule(") {
		t.Error("Add Rule… 的入口没接到 askForNewRule")
	}
	body, ok := swiftFunctionBody(source, "private func askForNewRule(")
	if !ok {
		t.Fatal("读不出 askForNewRule 的函数体 —— 守卫已失效,先修守卫")
	}
	refusal := strings.Index(body, `"rules_risky_direct"`)
	if refusal < 0 {
		t.Fatal("askForNewRule 认不出风险门的失败码 —— 那一支根本没接上")
	}
	// **要害在这里**:被拒之后必须把同一组视图再摆一次。只断言那个失败码出现过
	// 的话,把这一步删掉(于是 sheet 关了、输入全没了、什么也不说)照样全绿。
	represent := strings.Index(body[refusal:], "self.askForNewRule(")
	if represent < 0 {
		t.Error("409 之后没有再摆一次表单 —— 用户敲的那串连同这道门要他做的事一起没了")
	} else if !strings.Contains(body[refusal:], "accessory: accessory") {
		t.Error("409 之后摆的不是同一组视图 —— 新建一组等于把输入清空")
	}
	// Add Anyway 只放行刚刚被拒的那一条。
	if !strings.Contains(body, "pattern == refused") {
		t.Error("force 没有钉在被拒的那个模式上 —— 改敲另一个危险模式再按 Add Anyway 就能直接过")
	}
	if !strings.Contains(body, "force: force") {
		t.Error("force 没有传给 changeRule")
	}
	if !strings.Contains(code, "controller.onAddRule = ") || !strings.Contains(code, "self?.addRuleFromWindow()") {
		t.Error("Add Rule… 没接到 main.swift")
	}
}
