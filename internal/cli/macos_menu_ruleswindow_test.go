package cli

import (
	"os"
	"path/filepath"
	"regexp"
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

// **体检缺席要在窗口上说出来,不能摆一排看起来干净的行。**
//
// 这个窗口的词汇表里「一行没有副标题」读作「查过了,健康」;旧 Guardian 不发
// `review`,配置读不出来时它也发 nil —— 不说这句话,窗口就替一份从没收到过的
// 体检报告签了字。判据在 `ruleReviewUnavailableNote`(纯模型,已有 Swift 测试
// 钉住「缺席与空报告不是同一个渲染」),这条守卫钉的是**接线**:那句话真的
// 被算出来、真的传进窗口、真的摆进了视图树。
func TestMacMenuRulesWindowAnnouncesAnAbsentReview(t *testing.T) {
	window, _ := menuRulesWindowSource(t)
	render, ok := swiftFunctionBody(window, "private func render(")
	if !ok {
		t.Fatal("读不出 RulesWindow.render 的函数体 —— 守卫已失效,先修守卫")
	}
	if !strings.Contains(render, "if let reviewNote {") {
		t.Error("render 里没有为体检缺席那句话留的分支")
	}
	if !strings.Contains(render, "labelWithString: reviewNote") ||
		!strings.Contains(render, "stack.addArrangedSubview(note)") {
		t.Error("那句话没有被摆进视图树 —— 算出来没人看见,与没算一模一样")
	}

	// 判据不许在窗口里重算一份:窗口只能拿到别人算好的那句话。
	if strings.Contains(window, "review ==") || strings.Contains(window, "ruleReviewUnavailableNote(") {
		t.Error("窗口自己判了体检在不在 —— 判据该在 RulesModel 里")
	}

	// main.swift 那一跳:每一次摆这张表都要带上它,漏一处就是那一条路上的
	// 窗口重新变回「看起来干净」。
	main := menuMainSwiftCode(t)
	for _, call := range []string{"rulesWindow.show(", "rulesWindow.refreshIfVisible("} {
		n := strings.Count(main, call)
		if n == 0 {
			t.Fatalf("main.swift 里找不到 %s —— 守卫已失效,先修守卫", call)
		}
	}
	if got, want := strings.Count(main, "ruleReviewUnavailableNote("),
		strings.Count(main, "rulesWindow.show(")+strings.Count(main, "rulesWindow.refreshIfVisible("); got != want {
		t.Errorf("%d 处摆这张表,而只有 %d 处带上了体检缺席那句话", want, got)
	}
}

// **规则窗口必须跟着环境刷新走,而且显式打开永不被拦。**
//
// 此前 `RulesWindow` 连 `isVisible` 都没有、`applyRefresh` 也不为它做任何事:
// 每条规则的失败计数冻在打开窗口那一刻,而「哪条在失败」正是这个窗口存在的理由
// (服务器窗口 2026-08-17 就是这么坏过一次的)。
//
// 不对称那一半同样承重:环境刷新设的在飞标志若把紧跟着来的显式打开也拦住,
// 用户点了菜单项、窗口没出现、没有 alert —— 「点了没反应」,那是上一次真实回归。
func TestMacMenuRulesWindowFollowsAmbientRefreshButNeverSuppressesAnExplicitOpen(t *testing.T) {
	source := stripSwiftComments(menuMainSwiftSource(t))

	apply, ok := swiftFunctionBody(source, "private func applyRefresh(")
	if !ok {
		t.Fatal("读不出 applyRefresh 的函数体 —— 守卫已失效,先修守卫")
	}
	gate := strings.Index(apply, "rulesWindow.isVisible")
	if gate < 0 {
		t.Fatal("applyRefresh 不看规则窗口开没开 —— 那张表会冻在打开的那一刻")
	}
	rest := apply[gate:]
	call := strings.Index(rest, "fetchRulesOnDemand(")
	if call < 0 {
		t.Fatal("看了可见性却不拉 —— 接线只接了一半")
	}
	// **只在开着的时候拨**:窗口关着还拨,就把「按需」这件事整个取消掉了。
	if !strings.Contains(rest[:call], "{") {
		t.Error("fetchRulesOnDemand 不在可见性那个分支里")
	}
	if !strings.Contains(rest[call:call+len("fetchRulesOnDemand(forceShow: false)")], "forceShow: false") {
		t.Error("环境刷新走的不是 forceShow: false —— 它会抢焦点、会弹 alert")
	}

	// 显式打开那一路:用户点「Routing Rules…」,forceShow: true。
	open, ok := swiftFunctionBody(source, "@objc private func openRulesWindow()")
	if !ok {
		t.Fatal("读不出 openRulesWindow 的函数体 —— 守卫已失效,先修守卫")
	}
	if !strings.Contains(open, "fetchRulesOnDemand(forceShow: true)") {
		t.Error("点菜单项走的不是 forceShow: true")
	}

	// 不对称在判据里,不在裸 guard 里。`shouldSuppressFetch` 已由 Swift 侧
	// 表驱动测过四种组合;这里钉的是「规则这一路真的用了它」。
	fetch, ok := swiftFunctionBody(source, "private func fetchRulesOnDemand(forceShow: Bool)")
	if !ok {
		t.Fatal("读不出 fetchRulesOnDemand 的函数体 —— 守卫已失效,先修守卫")
	}
	if !strings.Contains(fetch, "shouldSuppressFetch(inFlight: rulesFetchInFlight, explicit: forceShow)") {
		t.Error("规则那一路的在飞守卫不是 shouldSuppressFetch —— 裸 guard 会把显式打开也拦住")
	}
	// 可见性判据住在窗口自己那边(与 ServersWindow 同一个形状)。
	window, _ := menuRulesWindowSource(t)
	if !strings.Contains(window, "var isVisible: Bool") {
		t.Error("RulesWindow 没有 isVisible —— applyRefresh 那一跳无从判断有没有人在看")
	}
}

// **五个 Swift 字面量与 rulereview.Class.String() 必须是同一组词。**
//
// 菜单按字面量分派三件事(排序、上色、那句英文说明),而两侧没有任何东西把它们
// 系在一起:把 `ClassRisky.String()` 改成别的词,`ruleRowNoteIsSevere` 会把去
// 匿名化那一行画成橙色的建议、`ruleVerdictText` 回落成「bx flagged this rule
// (…)」,**而两个套件全绿**。这正是本仓库反复出现的形状:守卫钉住的是缺陷旁边
// 的东西。
//
// 双向:Go 有而 Swift 没有 = 新加的一类在菜单里既不排序也没有说明;Swift 有而
// Go 没有 = 一条永远等不到结论的死分支(它看起来与生效中的分支一模一样)。
//
// 两侧任一读不出来都 t.Fatal —— 正则没匹配到时当成空集合,「集合相等」会静默
// 通过,而那正是这条守卫要挡住的失效形状。
func TestMacMenuRuleClassLiteralsMatchTheGoClassNames(t *testing.T) {
	goNames := readRuleReviewClassNames(t)
	swiftNames := readMenuRuleClassLiterals(t)

	// 自检锚点:正则改坏之后这条守卫不许变成空转。
	if !goNames["risky_direct"] {
		t.Fatal("Go 那侧抽不到 risky_direct —— 要么 ClassRisky 改了名(那就是漂移,菜单会把去匿名化那行画成建议),要么守卫读错了地方")
	}
	if !swiftNames["risky_direct"] {
		t.Fatal("Swift 那侧抽不到 risky_direct —— 要么菜单改了字面量(那就是漂移),要么守卫读错了地方")
	}

	for name := range goNames {
		if !swiftNames[name] {
			t.Errorf("rulereview.Class 有 %q,而菜单一个字面量都没有 —— 那一类在界面上既不排序也没有说明", name)
		}
	}
	for name := range swiftNames {
		if !goNames[name] {
			t.Errorf("菜单里有 %q,而 rulereview.Class 不发这个词 —— 那是一条永远等不到结论的死分支", name)
		}
	}
}

// readRuleReviewClassNames 从 internal/rulereview/verdict.go 的 Class.String()
// 里抽出全部返回的字面量(含 default 那一支 —— 它才是 ClassRisky)。
func readRuleReviewClassNames(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join("..", "rulereview", "verdict.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s:%v —— 守卫已失效,先修守卫", path, err)
	}
	body := regexp.MustCompile(`(?s)func \(c Class\) String\(\) string \{(.*?)\n\}`).FindStringSubmatch(string(src))
	if body == nil {
		t.Fatal("读不出 Class.String() 的函数体 —— 守卫已失效,先修守卫")
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`return "([^"]+)"`).FindAllStringSubmatch(body[1], -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatal("Class.String() 里一个字面量都没抽到 —— 守卫读错了地方")
	}
	return out
}

// readMenuRuleClassLiterals 从 RulesModel.swift 里按 class 分派的那两个函数
// (排序与那句英文说明)抽出全部 `case "..."` 字面量。
//
// **不是全文件扫字符串**:文档注释里逐字列着那五个词,扫全文会让这条守卫在
// 实现里一个都不剩时照样绿。
func readMenuRuleClassLiterals(t *testing.T) map[string]bool {
	t.Helper()
	source := stripSwiftComments(readMenuSwiftSource(t, "RulesModel.swift"))
	out := map[string]bool{}
	for _, sig := range []string{
		"func ruleRowSeverity(_ row: RuleRow) -> Int",
		"func ruleVerdictText(_ finding: RuleFinding) -> String",
	} {
		body, ok := swiftFunctionBody(source, sig)
		if !ok {
			t.Fatalf("读不出 %s 的函数体 —— 守卫已失效,先修守卫", sig)
		}
		found := false
		for _, m := range regexp.MustCompile(`case "([^"]+)"`).FindAllStringSubmatch(body, -1) {
			out[m[1]] = true
			found = true
		}
		if !found {
			t.Fatalf("%s 里一个 case 字面量都没抽到 —— 守卫读错了地方", sig)
		}
	}
	return out
}

// **删完那一行的 Undo 必须活过环境重画 —— 这条守卫钉的就是那件事。**
//
// 起因是一次修复顺手弄坏的东西:规则窗口 2026-09-11 才接上环境刷新
// (`applyRefresh` → `fetchRulesOnDemand(forceShow: false)` → `refreshIfVisible`
// → `render`),而 `render` 从新鲜数据重建整张表,新鲜数据里已经没有刚删掉的
// 那条规则了 —— 于是那个 Undo 在**约 2 秒后**无声消失。删除刻意不弹确认框
// (为十一条冗余点十一次确认是在惩罚正确的行为),Undo 是那个确认框的替身,
// 把它的寿命绑在刷新节拍上没有人做过这个决定。
//
// **判据钉的是「重画之后它还在」这条性质本身,不是它旁边的东西**(本仓库最
// 常复发的失效形状):环境那条路必须经过同一个把挂起插回去的跳板,而且不许
// 有第二条绕开挂起、直接遍历新鲜数据的摆表路径 —— 那样改回去整套照样绿。
func TestMacMenuRulesWindowKeepsTheUndoAcrossAnAmbientRerender(t *testing.T) {
	window, text := menuRulesWindowSource(t)

	// ① 环境那条路(`refreshIfVisible`)与显式打开走同一个跳板:先对账挂起,
	//    再摆表。绕开它就等于绕开这整个修复。
	refresh, ok := swiftFunctionBody(window, "func refreshIfVisible(")
	if !ok {
		t.Fatal("读不出 refreshIfVisible 的函数体 —— 守卫已失效,先修守卫")
	}
	if !strings.Contains(refresh, "adoptFreshRules(") || !strings.Contains(refresh, "render(") {
		t.Error("环境刷新没走「对账挂起 → 摆表」那条路 —— 刚删掉的那一行会在这一拍里消失")
	}

	// ② 摆表只有一条路,而且它经过 `ruleTableEntries`(把挂起的删除插回原位的
	//    那个纯函数)。直接遍历新鲜数据就是这个 bug 的原形。
	render, ok := swiftFunctionBody(window, "private func render(")
	if !ok {
		t.Fatal("读不出 RulesWindow.render 的函数体 —— 守卫已失效,先修守卫")
	}
	if !strings.Contains(render, "ruleTableEntries(rows: lastRuleRows, pending: pendingRemovals)") {
		t.Error("render 没有把挂起的删除插回表里 —— 重画一次那条 Undo 就没了")
	}
	if strings.Contains(render, "for row in ruleRows") || strings.Contains(render, "for row in lastRuleRows") {
		t.Error("render 里还有一条直接遍历规则数据的摆表路径 —— 它绕开挂起,正是这个 bug 的原形")
	}
	// 那一行长什么样只有一份实现:改装某一行活不过下一次 render。
	if !strings.Contains(render, "removedRow(kind: kind, pattern: pattern)") {
		t.Error("render 不摆「Removed … · Undo」那种行 —— 挂起记下了却没人把它画出来")
	}
	if !strings.Contains(text, `NSButton(title: "Undo"`) {
		t.Error("窗口里没有 Undo 按钮")
	}
	removed, ok := swiftFunctionBody(window, "private func removedRow(")
	if !ok {
		t.Fatal("读不出 removedRow 的函数体 —— 守卫已失效,先修守卫")
	}
	if !strings.Contains(removed, "undoRemove(_:)") {
		t.Error("Removed 那一行上的按钮不接 undoRemove —— 一个点不动的 Undo")
	}

	// ③ 对账只有一条判据,而且只在**拿到新鲜数据**时发生。写成无条件清空
	//    (或者在 markRemoved 的就地重画里也对一次账)就等于这个修复从来没生效:
	//    删完那一刻手里还是旧数据,对账会把刚记下的挂起当场抹掉。
	adopt, ok := swiftFunctionBody(window, "private func adoptFreshRules(")
	if !ok {
		t.Fatal("读不出 adoptFreshRules 的函数体 —— 守卫已失效,先修守卫")
	}
	if !strings.Contains(adopt, "survivingRuleRemovals(pendingRemovals, freshRows: ruleRows)") {
		t.Error("挂起的对账判据不是 survivingRuleRemovals —— 判定该在 RulesModel 里")
	}
	if strings.Contains(adopt, "pendingRemovals.removeAll") {
		t.Error("收到新数据就把挂起全清了 —— 那与压根不记它完全一样")
	}
	if strings.Contains(render, "survivingRuleRemovals(") || strings.Contains(render, "pendingRemovals =") {
		t.Error("摆表那一路也在动挂起 —— markRemoved 的就地重画会把刚记下的那条当场抹掉")
	}

	// ④ 删除那一刻真的记了一条挂起并重画;不记的话前面三条全是空转。
	mark, ok := swiftFunctionBody(window, "func markRemoved(kind: RuleKind, pattern: String)")
	if !ok {
		t.Fatal("读不出 markRemoved 的函数体 —— 守卫已失效,先修守卫")
	}
	if !strings.Contains(mark, "pendingRemovals.append(PendingRuleRemoval(") {
		t.Error("markRemoved 没有把这一条记成挂起 —— 它只改了一行的样子,活不过下一次重画")
	}
	if !strings.Contains(mark, "render(") {
		t.Error("markRemoved 记了挂起却不重画 —— 用户点完 Remove 看不到任何变化")
	}

	// ⑤ 挂起不许无边界、也不许活过这扇窗:关掉窗口那些 Undo 就再也点不到了,
	//    留着只会让下次打开摆出一串早已不相干的「Removed …」。
	if !strings.Contains(window, "maxPendingRuleRemovals") {
		t.Error("挂起集合没有上限")
	}
	closed, ok := swiftFunctionBody(window, "func windowWillClose(")
	if !ok {
		t.Fatal("读不出 windowWillClose 的函数体 —— 守卫已失效,先修守卫")
	}
	if !strings.Contains(closed, "pendingRemovals.removeAll()") {
		t.Error("关窗不清挂起 —— 下次打开会摆出一串早已不相干的 Removed 行")
	}

	// ⑥ 判定住在纯模型里(Swift 套件已逐条测过那几条性质);窗口不许自己再算一份。
	model := stripSwiftComments(readMenuSwiftSource(t, "RulesModel.swift"))
	for _, want := range []string{
		"func survivingRuleRemovals(", "func ruleTableEntries(", "struct PendingRuleRemoval",
	} {
		if !strings.Contains(model, want) {
			t.Errorf("RulesModel 里没有 %s —— 判据落进 AppKit 那半就没有测试盯着它了", want)
		}
	}
}

// **滚动位置只在环境重画时保住,显式打开永远从头开始。**
//
// 这个窗口每 2 秒把整个 stack 拆掉重填,不保位置的话用户每翻到一半就被拽回
// 顶部(App Traffic 那扇窗至今就是这么坏的);而他刚点开「Routing Rules…」
// 的那一次,顶上那几行才是他要看的。两种重画的正确答案相反,压成一个就必错一半。
func TestMacMenuRulesWindowKeepsScrollOnlyOnAmbientRerender(t *testing.T) {
	window, _ := menuRulesWindowSource(t)

	show, ok := swiftFunctionBody(window, "func show(rows: [RuleGroupRow]")
	if !ok {
		t.Fatal("读不出 show 的函数体 —— 守卫已失效,先修守卫")
	}
	if !strings.Contains(show, "render(preservingScroll: false)") {
		t.Error("显式打开也保滚动位置 —— 用户点开窗口第一眼看到的会是他上次停的地方")
	}
	refresh, ok := swiftFunctionBody(window, "func refreshIfVisible(")
	if !ok {
		t.Fatal("读不出 refreshIfVisible 的函数体 —— 守卫已失效,先修守卫")
	}
	if !strings.Contains(refresh, "render(preservingScroll: true)") {
		t.Error("环境重画不保滚动位置 —— 菜单开着时每 2 秒把用户拽回顶部一次")
	}

	render, ok := swiftFunctionBody(window, "private func render(")
	if !ok {
		t.Fatal("读不出 render 的函数体 —— 守卫已失效,先修守卫")
	}
	// 位置必须在**拆视图之前**取:拆完再取就是 0,保了个寂寞。
	grab := strings.Index(render, "scroll?.contentView.bounds.origin")
	tear := strings.Index(render, "view.removeFromSuperview()")
	if grab < 0 || tear < 0 {
		t.Fatal("render 里找不到取位置或拆视图那两步 —— 守卫已失效,先修守卫")
	}
	if grab > tear {
		t.Error("滚动位置是在拆完视图之后取的 —— 那时它已经是 0 了")
	}
	// 先布局再滚:少了这一步滚的是按旧内容算出来的坐标(Diagnostics 两页同款)。
	layout := strings.Index(render, "layoutSubtreeIfNeeded()")
	restore := strings.Index(render, "contentView.scroll(to: offset)")
	if layout < 0 || restore < 0 {
		t.Fatal("render 里找不到布局或回滚那两步 —— 守卫已失效,先修守卫")
	}
	if layout > restore {
		t.Error("先滚后布局 —— 滚的是按旧内容算出来的坐标,表一变长位置照样跳")
	}
}
