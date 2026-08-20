package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Traffic by App 悬浮窗在菜单里的**接线**守卫。
//
// 判断本身住在 AppTrafficModel.swift 的纯函数里(能力判据、三态文案、那句
// 「近似值」小字),由 Swift 套件钉住;**调不调它们在 main.swift,而 main.swift
// 编不进 scripts/test-macos-menu.sh**(它要 AppKit)。漏接线不会有任何编译错误,
// 也不会有任何 Swift 测试转红 —— 这几条是唯一在 CI 里真正跑着的证明。
//
// 教训在前:这类文本守卫在本仓库被攻破过八次,形状都一样 —— 钉拼法而不是语义,
// 以及**被代码自己的解释性注释兜绿**(Task 8 刚栽过一次:一行 `// ProbeDial:
// direct,` 就把一条读多行块的守卫兜绿了)。所以下面每一条:
//   - 先 stripSwiftComments 剥掉注释再判,行注释与块注释都剥;
//   - 钉的是「某个判据出现在某个位置」而不是某个字符串存在;
//   - **读不懂现在的代码时一律 t.Fatal 响亮失败**,绝不静默放行。

// stripSwiftComments 剥掉一段 Swift 源码里的行注释(`//` 到行尾)与块注释
// (`/* … */`,Swift 里可嵌套,故用深度计数),并对字符串字面量保持原样跳过
// —— 否则 `"https://…"` 会被从 `//` 处截断,而 main.swift 里真有这样的常量。
//
// **两种注释都要处理,漏一种等于没修**(变异分别验证过)。多行字符串 `"""`
// 也要认:main.swift 里有一段,不认它会让后面全部偏移错位,而一个偏移错位的
// 文本守卫是安静地错,不是响亮地错。
//
// 全部分隔符(`"`、`/`、`*`、`\`)在 UTF-8 里都是单字节且不会出现在多字节字符
// 的续字节里,按字节扫描对本仓库夹杂中文注释的源码是安全的。
func stripSwiftComments(src string) string {
	var out strings.Builder
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		switch {
		case c == '"' && i+2 < n && src[i+1] == '"' && src[i+2] == '"':
			start := i
			i += 3
			for i+2 < n && !(src[i] == '"' && src[i+1] == '"' && src[i+2] == '"') {
				i++
			}
			if i+2 < n {
				i += 3
			} else {
				i = n
			}
			out.WriteString(src[start:i])
		case c == '"':
			start := i
			i++
			for i < n {
				if src[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if src[i] == '"' || src[i] == '\n' {
					i++
					break
				}
				i++
			}
			out.WriteString(src[start:i])
		case c == '/' && i+1 < n && src[i+1] == '/':
			for i < n && src[i] != '\n' {
				i++
			}
			// 被剥掉的内容不写入;`\n` 留给下一轮写入,保住行结构。
		case c == '/' && i+1 < n && src[i+1] == '*':
			depth := 0
			for i < n {
				if src[i] == '/' && i+1 < n && src[i+1] == '*' {
					depth++
					i += 2
					continue
				}
				if src[i] == '*' && i+1 < n && src[i+1] == '/' {
					depth--
					i += 2
					if depth == 0 {
						break
					}
					continue
				}
				// 块注释里的换行留着,同样是为了保住行结构。
				if src[i] == '\n' {
					out.WriteByte('\n')
				}
				i++
			}
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

// blankSwiftStringLiterals 把字符串字面量与注释的**内容**抹成空格,保留定界符,
// 并且**逐字节保住偏移** —— 返回串与入参等长,任何在它上面算出来的下标都可以直接
// 拿回原串去切。
//
// **这是本文件全部结构化扫描器(数花括号、数圆括号)的前置。** `stripSwiftComments`
// 刻意保留字符串内容,于是每一个数括号的扫描器都会把**字面量里的括号**当成结构:
//
//	stack.addArrangedSubview(hint("("))
//	let _ = appTrafficApproximateNote      // 小字从窗口消失
//	stack.addArrangedSubview(hint(")"))    // 而守卫全绿
//
// 第一个字面量里的 `(` 让深度停在 1,第二个里的 `)` 才让它归零,于是「第一次调用的
// 实参」把中间整段源码都吞了进去 —— **假绿**。反向同样成立:菜单标签里一个 `}`
// 会让好几条守卫**假红**,而一个会莫名其妙红的闸门比没有闸门更糟(本仓库对此有
// 明确记录),它会被下一个人删掉。
//
// 注释也一并抹掉(而不是删掉),是因为删会挪动偏移;这里要的恰恰是偏移不变。
// 换行原样保留,行结构不受影响。
func blankSwiftStringLiterals(src string) string {
	out := []byte(src)
	n := len(src)
	blank := func(i int) {
		if out[i] != '\n' {
			out[i] = ' '
		}
	}
	for i := 0; i < n; {
		switch {
		case src[i] == '"' && i+2 < n && src[i+1] == '"' && src[i+2] == '"':
			i += 3
			for i+2 < n && !(src[i] == '"' && src[i+1] == '"' && src[i+2] == '"') {
				blank(i)
				i++
			}
			if i+2 < n {
				i += 3
			} else {
				for ; i < n; i++ {
					blank(i)
				}
			}
		case src[i] == '"':
			i++
			for i < n {
				if src[i] == '\\' && i+1 < n {
					blank(i)
					blank(i + 1)
					i += 2
					continue
				}
				if src[i] == '"' {
					i++
					break
				}
				// 未闭合的字面量不许吃掉整个文件:单行字符串到换行为止。
				if src[i] == '\n' {
					break
				}
				blank(i)
				i++
			}
		case src[i] == '/' && i+1 < n && src[i+1] == '/':
			blank(i)
			blank(i + 1)
			i += 2
			for i < n && src[i] != '\n' {
				blank(i)
				i++
			}
		case src[i] == '/' && i+1 < n && src[i+1] == '*':
			depth := 0
			for i < n {
				if src[i] == '/' && i+1 < n && src[i+1] == '*' {
					depth++
					blank(i)
					blank(i + 1)
					i += 2
					continue
				}
				if src[i] == '*' && i+1 < n && src[i+1] == '/' {
					depth--
					blank(i)
					blank(i + 1)
					i += 2
					if depth == 0 {
						break
					}
					continue
				}
				blank(i)
				i++
			}
		default:
			i++
		}
	}
	return string(out)
}

func menuAppTrafficWindowSource(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(
		"..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "AppTrafficWindow.swift"))
	if err != nil {
		t.Fatalf("读不到 AppTrafficWindow.swift:%v —— 守卫已经失效,先修守卫", err)
	}
	return string(source)
}

// swiftBlockRange 返回 marker 之后那一对花括号里内容的字节区间。
//
// **它存在的理由**:「selector 之前最近的那个 if 是能力判据」证明不了 selector
// 在那个 if 的**花括号里** —— `if gate { }` 后面紧跟一句无条件的 addAction,
// 上面那条判据照样成立(审查在隔离副本里实测全绿,菜单项对每一版 Guardian 都
// 无条件画出)。要证明「被它管着」,就得真的去看它管的那段。
func swiftBlockRange(source, marker string) (int, int, bool) {
	// **数花括号一律在抹白副本上做**(见 blankSwiftStringLiterals):
	// 一句 `_ = "{"` 放进门里就能把配平推歪,原样重开被这条守卫堵住的绕法。
	scan := blankSwiftStringLiterals(source)
	start := strings.Index(scan, marker)
	if start < 0 {
		return 0, 0, false
	}
	open := strings.Index(scan[start:], "{")
	if open < 0 {
		return 0, 0, false
	}
	open += start
	depth := 0
	for i := open; i < len(scan); i++ {
		switch scan[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return open + 1, i, true
			}
		}
	}
	return 0, 0, false
}

// swiftCallArguments 返回源码里每一次 `callee(...)` 调用的**实参文本**(按圆括号
// 配平取,故容得下 `f(g(x))` 这种嵌套)。
//
// **它存在的理由**:「文件里出现过这个标识符」与「这个标识符真的被画出来了」是
// 两件事 —— `let _ = appTrafficApproximateNote` 能满足前者而小字从窗口消失
// (审查实测全绿)。要证明它被画出来,判据只能是「它出现在某一次
// addArrangedSubview 的实参里」。
func swiftCallArguments(source, callee string) []string {
	// 同上:**圆括号也只在抹白副本上数**。`hint("(")` 里那个括号不是结构,
	// 把它当结构会让一次调用的实参吞掉后面整段源码(假绿),或者让一个合法的
	// 标签里带 `)` 的改动莫名其妙转红(假红)。
	scan := blankSwiftStringLiterals(source)
	var out []string
	for _, idx := range regexp.MustCompile(regexp.QuoteMeta(callee)+`\s*\(`).FindAllStringIndex(scan, -1) {
		open := strings.IndexByte(scan[idx[0]:idx[1]], '(') + idx[0]
		depth := 0
		for i := open; i < len(scan); i++ {
			switch scan[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					out = append(out, source[open+1:i])
					i = len(scan)
				}
			}
		}
	}
	return out
}

// **能力门控,绝不「试着拨一下看看」。**
//
// 旧 Guardian 不认识 /v1/apps,会回 404 —— 而客户端无从区分「这版不支持」与
// 「这版支持但此刻没数据」。这不是纸面推演:`bx status --watch` 顶着一台旧
// Guardian 跑时,解出的代际号恒 0、与起始值恰好相等,「未变化」分支被命中且没有
// 任何错误可供退避介入,真机实测本机 unix socket 常驻 CPU 26%~46%、吞吐上千
// 次/秒。
//
// **判据是「菜单项在那个 if 的花括号里」,不是「它前面最近的 if 是那一句」。**
// 后者是本仓库既有 servers/rules 守卫的写法,而审查实测它可以被留成空壳:
//
//	if appTrafficAvailable(capabilities: …) { }
//	menu.addAction("Traffic by App…", …, action: #selector(openAppTrafficWindow))
//
// 全绿,菜单项对每一版 Guardian 无条件画出。
func TestMacMenuGatesAppTrafficOnCapability(t *testing.T) {
	body, ok := swiftFunctionBody(stripSwiftComments(menuMainSwiftSource(t)), "private func rebuildMenu()")
	if !ok {
		t.Fatal("读不出 rebuildMenu() 的函数体 —— 守卫已经失效,先修守卫")
	}
	const action = "#selector(openAppTrafficWindow)"
	hits := regexp.MustCompile(regexp.QuoteMeta(action)).FindAllStringIndex(body, -1)
	if len(hits) == 0 {
		t.Fatal("rebuildMenu() 里没有 Traffic by App 入口 —— 用户在菜单里看不到这个窗口")
	}
	// 实参必须是**光秃秃的一次取值**。`capabilities: report?.capabilities ?? []`
	// 这类写法会把「这版压根没声明过能力」压成「声明了、一个都没有」,两者在
	// 这道门上结论相同、在别处不同,而漂开的那天没有任何东西会红。
	const gate = "if appTrafficAvailable(capabilities: maintenanceReport?.capabilities) {"
	start, end, ok := swiftBlockRange(body, gate)
	if !ok {
		t.Fatalf("找不到能力门 %q —— 菜单项要么没有门控,要么实参不是那一次光秃秃的取值", gate)
	}
	// **每一处**入口都必须落在那个门的花括号里。只查第一处会让「门后面再加一句
	// 无条件的 addAction」照样全绿。
	for _, hit := range hits {
		if hit[0] < start || hit[1] > end {
			t.Fatalf("偏移 %d 处的 Traffic by App 入口在能力门的花括号**之外** —— "+
				"它对每一版 Guardian 都会被画出来,而旧版每次点都是 404", hit[0])
		}
	}
}

// **窗口关着就不拨。**
//
// 这是这个功能要买到的收益:没人看时开销精确为零,而且不在这台机器上留下「你
// 开过什么应用」的记录。订阅是靠每一次拉取续期的(Core 侧 30 秒 TTL,惰性
// 结算),所以「不拨」等价于「不采集」。
//
// **锚在类型级入口上,不是包装函数名上。** 上一版白名单锚的是
// `fetchAppTrafficOnDemand(`,而审查在别处直接写
// `DispatchQueue.global().async { _ = try? GuardianClient().appTraffic() }`,
// 整套测试全绿 —— 那是「禁拼法而非禁语义」的教科书形状。真正只有一个的是
// **端点本身**:凡是 main.swift 里以整词提到 `appTraffic` 的地方(`.appTraffic()`
// 调用、`GuardianEndpoint.appTraffic`),都必须落在那一个拨号函数里。
// (`appTrafficWindow` / `appTrafficTimer` / `appTrafficAvailable` 这些后面接着
// 单词字符,`\b` 天然排除;`fetchAppTrafficOnDemand` 里是大写 A,不匹配。)
func TestMacMenuOnlyFetchesAppTrafficWhileWindowVisible(t *testing.T) {
	source := stripSwiftComments(menuMainSwiftSource(t))
	defs := swiftFunctionDefs(source)

	// ① 拨号本身只能从一个函数里长出来。
	//
	// **锚在「真的是拨号」的两个形状上,不是整词 `appTraffic`。** 后者论证今天
	// 成立,但最可能的下一个改动(缓存上一次报告)就会撞上它:
	// `private var appTraffic: AppTrafficReport?` 会被指控成「守卫读不懂代码」,
	// 一个纯访问器会被指控成「直接拨了 /v1/apps」。**一条会对合法新代码假红的
	// 守卫会被下一个人删掉**,那等于没有守卫。
	scan := blankSwiftStringLiterals(source)
	dialShapes := []*regexp.Regexp{
		regexp.MustCompile(`\.appTraffic\s*\(`),              // GuardianClient().appTraffic()
		regexp.MustCompile(`GuardianEndpoint\.appTraffic\b`), // 直接构造端点
	}
	dials := 0
	for _, shape := range dialShapes {
		for _, match := range shape.FindAllStringIndex(scan, -1) {
			fn := enclosingSwiftFunc(defs, match[0])
			if fn == "" {
				t.Fatalf("偏移 %d 处拨了 /v1/apps,却不在任何函数体内 —— "+
					"守卫读不懂现在的代码了,先修守卫", match[0])
			}
			if fn != "fetchAppTrafficOnDemand" {
				t.Errorf("%s 里直接拨了 /v1/apps —— 拨号只许从 fetchAppTrafficOnDemand 长出来,"+
					"否则「窗口关着就不拨」这条不变量在别处被绕开了", fn)
			}
			dials++
		}
	}
	if dials == 0 {
		t.Fatal("main.swift 里一次都没拨过 /v1/apps —— 守卫已经失效,先修守卫")
	}

	// ② 那个拨号函数只能被这三处调用。
	allowed := map[string]bool{
		"openAppTrafficWindow": true, // 用户显式点菜单项
		"applyRefresh":         true, // 环境刷新,且必须先判窗口可见
		"startAppTrafficTimer": true, // 窗口开着时的心跳,随窗口关闭停掉
	}
	callSites := 0
	for _, match := range regexp.MustCompile(`fetchAppTrafficOnDemand\(`).FindAllStringIndex(source, -1) {
		// 定义行自身不是调用点。判据精确到前面紧挨着 `func `,不是「附近有个
		// func」—— 后者会把一次真的调用当成定义放过去。
		if match[0] >= 5 && source[match[0]-5:match[0]] == "func " {
			continue
		}
		fn := enclosingSwiftFunc(defs, match[0])
		if fn == "" {
			t.Fatalf("偏移 %d 处的 fetchAppTrafficOnDemand 调用不在任何函数体内 —— "+
				"守卫读不懂现在的代码了,先修守卫", match[0])
		}
		if !allowed[fn] {
			t.Errorf("%s 里拨了一次应用流量 —— 它不在白名单里,而窗口关着时拨"+
				"就是让 Core 永远开着采集", fn)
		}
		callSites++
	}
	if callSites == 0 {
		t.Fatal("一个 fetchAppTrafficOnDemand 调用点都没有 —— 守卫已经失效,先修守卫")
	}

	// ③ 环境刷新那一路的**每一处**都必须在窗口可见性判断的花括号里。
	// 只查第一处会让「门控之后再加一句裸的 fetchAppTrafficOnDemand」照样全绿。
	refresh, ok := swiftFunctionBody(source, "private func applyRefresh(_ outcome: RefreshOutcome, capturedGeneration: Int)")
	if !ok {
		t.Fatal("读不出 applyRefresh 的函数体 —— 守卫已经失效,先修守卫")
	}
	hits := regexp.MustCompile(`fetchAppTrafficOnDemand\(`).FindAllStringIndex(refresh, -1)
	if len(hits) == 0 {
		t.Fatal("applyRefresh 里没有按需刷新应用流量 —— 打开着的窗口会冻在打开那一刻")
	}
	start, end, ok := swiftBlockRange(refresh, "if appTrafficWindow.isVisible {")
	if !ok {
		t.Fatal("applyRefresh 里没有 `if appTrafficWindow.isVisible {` —— " +
			"环境刷新会在没人看的时候也拨,而拨就是让 Core 采集")
	}
	for _, hit := range hits {
		if hit[0] < start || hit[1] > end {
			t.Fatalf("偏移 %d 处的按需刷新在窗口可见性判断的花括号**之外** —— "+
				"窗口关着也会拨", hit[0])
		}
	}
	if !strings.Contains(refresh[start:end], "fetchAppTrafficOnDemand(forceShow: false)") {
		t.Error("环境刷新那一路没有用 forceShow: false —— 它会每次都抢焦点把窗口推到用户面前")
	}
}

// **心跳不许活得比它的窗口长。**(修复审查的 Critical。)
//
// 窗口是靠 `show(report:)` 才被创建的(`ensureWindow()` 只在 show 里跑)。上一版
// 在菜单点击处**无条件**起心跳,于是首拉失败时:窗口从来没被创建 ⇒
// `windowWillClose` 永不触发 ⇒ `onClose` → `stopAppTrafficTimer()` 永不被调用 ⇒
// 每 5 秒一次失败拨号、永久,而失败分支静默 return,界面上一点痕迹都没有。
//
// **而它恰好发生在最常见的探索场景**:菜单项在 `.off` 状态下照样在场(能力来自
// Guardian 的静态清单,与 Core 死活无关)—— 保护关着时点一下就正好走到这条路,
// 后果是每分钟十几次拨号 + 十几行 guardian_apps_fetch_failed,直到菜单进程被杀;
// 若 Core 之后起来了,订阅会被永久续期、采集永远开着而没有任何窗口。
func TestMacMenuAppTrafficHeartbeatCannotOutliveItsWindow(t *testing.T) {
	source := stripSwiftComments(menuMainSwiftSource(t))
	defs := swiftFunctionDefs(source)

	// 心跳只能从拨号函数的成功分支里起。
	starts := 0
	for _, match := range regexp.MustCompile(`startAppTrafficTimer\(`).FindAllStringIndex(source, -1) {
		if match[0] >= 5 && source[match[0]-5:match[0]] == "func " {
			continue
		}
		fn := enclosingSwiftFunc(defs, match[0])
		if fn == "" {
			t.Fatalf("偏移 %d 处的 startAppTrafficTimer 调用不在任何函数体内 —— "+
				"守卫读不懂现在的代码了,先修守卫", match[0])
		}
		if fn != "fetchAppTrafficOnDemand" {
			t.Errorf("%s 里起了心跳 —— 只有拨号成功、窗口真的开出来之后才许起,"+
				"否则首拉失败会留下一个永远停不下来、也没有窗口可关的定时器", fn)
		}
		starts++
	}
	if starts == 0 {
		t.Fatal("一个 startAppTrafficTimer 调用点都没有 —— 守卫已经失效,先修守卫")
	}

	body, ok := swiftFunctionBody(source, "private func fetchAppTrafficOnDemand(forceShow: Bool)")
	if !ok {
		t.Fatal("读不出 fetchAppTrafficOnDemand 的函数体 —— 守卫已经失效,先修守卫")
	}
	show := strings.Index(body, "appTrafficWindow.show(report:")
	start := strings.Index(body, "startAppTrafficTimer()")
	if show < 0 || start < 0 {
		t.Fatal("读不出 show / 起心跳这两步 —— 守卫已经失效,先修守卫")
	}
	if start < show {
		t.Fatal("心跳起在 show 之前 —— show 是窗口唯一的创建点,起在它之前就可能" +
			"留下一个没有窗口的心跳,而 onClose 永远不会来停它")
	}
	// 失败分支必须自己把心跳停掉:那条路上没有窗口,谁也不会替它停。
	stop, stopEnd, ok := swiftBlockRange(body, "if !self.appTrafficWindow.isVisible {")
	if !ok {
		t.Fatal("失败分支没有 `if !self.appTrafficWindow.isVisible {` —— " +
			"首拉失败会留下一个永远跑下去、界面上完全看不见的心跳")
	}
	if !strings.Contains(body[stop:stopEnd], "stopAppTrafficTimer()") {
		t.Fatal("窗口不可见时没有停掉心跳 —— 首拉失败会让它每 5 秒拨一次,永久")
	}

	// 用户点菜单项那一处**不许**自己起心跳(上一版的 bug 原样)。
	open, ok := swiftFunctionBody(source, "private func openAppTrafficWindow()")
	if !ok {
		t.Fatal("读不出 openAppTrafficWindow 的函数体 —— 守卫已经失效,先修守卫")
	}
	if strings.Contains(open, "startAppTrafficTimer(") {
		t.Fatal("菜单点击处无条件起了心跳 —— 拉取失败时窗口根本没被创建," +
			"onClose 永不触发,心跳永远停不下来")
	}

	// 停心跳的机制本身,以及窗口关闭时那一跳。
	stopBody, ok := swiftFunctionBody(source, "private func stopAppTrafficTimer()")
	if !ok {
		t.Fatal("读不出 stopAppTrafficTimer 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(stopBody, "invalidate()") || !strings.Contains(stopBody, "appTrafficTimer = nil") {
		t.Error("stopAppTrafficTimer 没有真的把定时器停掉并清空")
	}
	startBody, ok := swiftFunctionBody(source, "private func startAppTrafficTimer()")
	if !ok {
		t.Fatal("读不出 startAppTrafficTimer 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(startBody, "commonModeTimer(") {
		t.Error("心跳不是 commonModeTimer 建的 —— Timer.scheduledTimer 只进 .default,菜单展开期间一次都不触发")
	}
	if !regexp.MustCompile(`controller\.onClose = \{[^}]*stopAppTrafficTimer\(\)`).MatchString(source) {
		t.Fatal("窗口关闭没有停掉心跳 —— 窗口关了、采集还开着,而界面上看不出任何异常")
	}
}

// **界面上必须写明字节数是近似值。**
//
// 端口复用会让残留字节算到新连接头上,spec 明写「界面不该把它显示成精确账」。
//
// **判据是「它出现在某一次 addArrangedSubview 的实参里」**,不是「文件里出现过
// 这个标识符」—— 后者被审查实测绕过:把那一行换成 `let _ =
// appTrafficApproximateNote`,小字从窗口消失而守卫全绿。锚在常量标识符而不是那句
// 英文的字面拼法上:换措辞不该让守卫红(内容由 Swift 套件钉住),而把它从视图树
// 里摘掉必须红。
func TestMacMenuAppTrafficWindowSaysByteCountsAreApproximate(t *testing.T) {
	window := stripSwiftComments(menuAppTrafficWindowSource(t))
	args := swiftCallArguments(window, "addArrangedSubview")
	if len(args) == 0 {
		t.Fatal("在 AppTrafficWindow.swift 里一次 addArrangedSubview 都没解析出来 —— " +
			"守卫读不懂现在的代码了,先修守卫")
	}
	shown := false
	for _, arg := range args {
		if strings.Contains(arg, "appTrafficApproximateNote") {
			shown = true
			break
		}
	}
	if !shown {
		t.Fatalf("那句「字节数是近似值」的小字没有出现在任何一次 addArrangedSubview 的实参里 —— "+
			"界面把一笔近似账显示成了精确账(共解析出 %d 次 addArrangedSubview)", len(args))
	}
	// 常量必须来自纯模型那一份,窗口里不许再抄一句自己的。
	model, err := os.ReadFile(filepath.Join(
		"..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "AppTrafficModel.swift"))
	if err != nil {
		t.Fatalf("读不到 AppTrafficModel.swift:%v —— 守卫已经失效,先修守卫", err)
	}
	if !strings.Contains(string(model), "let appTrafficApproximateNote") {
		t.Fatal("appTrafficApproximateNote 不在纯模型里 —— 那句话就没有任何 Swift 测试盯着")
	}
	// **窗口不许自己造一份零值报告。** `AppTrafficReport(subscribed: false)` 渲染
	// 出来的正是 "Not collecting app traffic right now." —— 而那句话是
	// fetchAppTrafficOnDemand 的失败分支明令禁止的那一句(「读不到就说读不到,
	// 不摆一个空报告」:「没问出来」与「没在采集」是两件事)。没有报告就什么都
	// 不画,别替 Core 回答一个它没被问过的问题。
	if strings.Contains(window, "AppTrafficReport(subscribed: false)") {
		t.Error("窗口用零值伪造了一份空报告 —— 它会显示「没在采集」,而事实可能只是" +
			"「这一次没问出来」,那是两件不同的事")
	}
}

// **显式打开永不被在飞标志拦住。**
//
// 两种失败的代价不对称,这是判据:重叠取数的代价是一次多余的本机 socket 往返
// (已判定无害);拦住一次显式动作的代价是「用户点了菜单项、窗口没出现、没有
// alert,什么都没发生」。服务器窗口那次就是这么回归的 —— 环境刷新设的标志把
// 紧跟着来的显式打开一起拦掉。
//
// 判据抽在 shouldSuppressFetch(StatusWatch.swift,已表驱动测过四种组合),
// 这条守卫钉的是**这里真的调了它、而且实参没被做手脚**。
func TestMacMenuAppTrafficExplicitOpenIsNeverSuppressed(t *testing.T) {
	source := stripSwiftComments(menuMainSwiftSource(t))
	body, ok := swiftFunctionBody(source, "private func fetchAppTrafficOnDemand(forceShow: Bool)")
	if !ok {
		t.Fatal("读不出 fetchAppTrafficOnDemand 的函数体 —— 守卫已经失效,先修守卫")
	}
	// 实参必须是光秃秃的一次取值:`explicit: false`、`explicit: forceShow && x`
	// 这类写法都会把显式那一路重新压回可被拦截。
	want := regexp.MustCompile(
		`guard !shouldSuppressFetch\(inFlight: appTrafficFetchInFlight, explicit: forceShow\) else \{ return \}`)
	if !want.MatchString(body) {
		t.Fatal("在飞守卫不是 shouldSuppressFetch(inFlight: appTrafficFetchInFlight, explicit: forceShow) —— " +
			"显式打开会被一次在飞的环境刷新拦掉,用户点了菜单项什么都不会发生")
	}
	// 裸的 `guard !appTrafficFetchInFlight` 正是那次回归的写法,一条都不许有。
	if regexp.MustCompile(`guard !appTrafficFetchInFlight`).MatchString(body) {
		t.Error("出现了裸的 guard !appTrafficFetchInFlight —— 那正是把显式动作一起拦掉的那条写法")
	}
	// 标志必须被设上,也必须被放开。
	if !strings.Contains(body, "appTrafficFetchInFlight = true") {
		t.Error("没有设置在飞标志 —— 窗口开着时连着来的环境刷新会叠起来")
	}
	if !strings.Contains(body, "appTrafficFetchInFlight = false") {
		t.Error("没有放开在飞标志 —— 第一次之后就再也拉不到新数据了")
	}
	// 显式那一路弹窗、环境刷新那一路就地重画。合并会让每次刷新都 NSApp.activate
	// 把窗口推到用户面前。
	open, ok := swiftFunctionBody(source, "private func openAppTrafficWindow()")
	if !ok {
		t.Fatal("读不出 openAppTrafficWindow 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(open, "fetchAppTrafficOnDemand(forceShow: true)") {
		t.Error("菜单点击那一路没有用 forceShow: true —— 它会被在飞标志拦住,点了没反应")
	}
	if !strings.Contains(body, "refreshIfVisible(") || !strings.Contains(body, ".show(") {
		t.Error("两条呈现路径没有分开(show / refreshIfVisible)—— 环境刷新会每次都抢焦点")
	}
}

// **拉不到的时候窗口不许假装那是此刻的事实。**
//
// 窗口开着时 bx 被关掉、Core 重启、Guardian 正忙 —— 上一版的失败分支
// (`guard forceShow else { return }`)静默返回,窗口一直显示上一份快照,而那份
// 快照读起来是「这些应用**此刻**正在走隧道」。与 CLAUDE.md 记的 watch 失效模式
// 同一条:静默失效时界面停在最后一次收到的状态上而看起来完全正常。
//
// 门槛与措辞住在纯模型的 appTrafficStaleNotice 里(Swift 套件钉住),这条守卫钉
// 的是**这里真的去问了它、并且成功时把计数清零**——不清零的话,一次瞬时失败之后
// 那句话会永远挂着,而它挂着的时候数据其实是新的。
func TestMacMenuMarksAppTrafficStaleWhenItCannotRefresh(t *testing.T) {
	source := stripSwiftComments(menuMainSwiftSource(t))
	body, ok := swiftFunctionBody(source, "private func fetchAppTrafficOnDemand(forceShow: Bool)")
	if !ok {
		t.Fatal("读不出 fetchAppTrafficOnDemand 的函数体 —— 守卫已经失效,先修守卫")
	}
	fail, failEnd, ok := swiftBlockRange(body, "guard let fetched else {")
	if !ok {
		t.Fatal("读不出失败分支 —— 守卫已经失效,先修守卫")
	}
	failure := body[fail:failEnd]
	if !strings.Contains(failure, "appTrafficConsecutiveFailures += 1") {
		t.Error("失败没有被计数 —— 界面无从知道自己已经连着几次没拉到了")
	}
	// **实参必须是光秃秃的一次取值**(与同一文件里那道能力门同一条纪律):
	// 写死 `consecutiveFailures: 0` 会让横幅永不出现,而存在性检查照样全绿。
	ask := regexp.MustCompile(
		`if let notice = appTrafficStaleNotice\(\s*consecutiveFailures: self\.appTrafficConsecutiveFailures\)`)
	if !ask.MatchString(failure) {
		t.Fatal("失败分支没有拿**当前**失败计数去问「该不该标陈旧」—— " +
			"写死一个常量就让横幅永不出现,而窗口会静默冻在上一份快照上," +
			"那份快照读起来是「这些应用此刻正在走隧道」")
	}
	// **显示的必须就是纯模型算出来的那一句。** 手写一个串会让纯模型那半连同它
	// 的全部 Swift 断言变成死代码,而窗口上写着另一份没人测过的文案
	// (「判据只长在一条路上」)。
	askAt := ask.FindStringIndex(failure)
	noticeStart, noticeEnd, ok := swiftBlockRange(failure[askAt[0]:], "if let notice =")
	if !ok {
		t.Fatal("读不出 `if let notice = …` 那个块 —— 守卫已经失效,先修守卫")
	}
	shown := failure[askAt[0]+noticeStart : askAt[0]+noticeEnd]
	if !strings.Contains(shown, "markStaleIfVisible(notice)") {
		t.Fatalf("陈旧提示不是把纯模型算出来的 notice 原样送进窗口 —— "+
			"手写文案会让那个纯函数与它的全部断言变成死代码:%s", strings.TrimSpace(shown))
	}
	// 送显示这一步必须在那个 `if let` 的花括号里 —— 挪进 else 或塞进一个恒假的
	// 分支,存在性检查一样看不出来。
	if strings.Count(failure, "markStaleIfVisible(") != 1 {
		t.Errorf("markStaleIfVisible 在失败分支里出现 %d 次 —— 只许有那唯一一次,"+
			"多出来的那次可能挂在别的条件上", strings.Count(failure, "markStaleIfVisible("))
	}
	// 成功必须清零,且清零要发生在把新数据画上去之前/同一路上。
	success := body[failEnd:]
	if !strings.Contains(success, "appTrafficConsecutiveFailures = 0") {
		t.Fatal("成功之后没有把失败计数清零 —— 一次瞬时失败之后那句「已陈旧」会永远挂着," +
			"而它挂着的时候数据其实是新的")
	}
}

// **订阅 TTL 不许在 Go 与 Swift 各写一份。**
//
// `appTrafficSubscriptionTTLSeconds` 是 `internal/supervisor/apptraffic.go` 里
// `appTrafficTTL` 的手抄 —— 正是本仓库反复栽的「判据抄两份」形状。而这条守卫本来
// 就在读 Swift 源码,把两个数当场比对,跨语言那道缝就此关死。
//
// 不做的代价很具体:Go 那边把 TTL 调到 ≤9 秒,没有任何东西转红,而窗口开着会
// 周期性跳回 `Not collecting app traffic right now.` 并把计数清零 —— 正是心跳
// 要防的那个现象。
//
// **上下界一起钉。** 上界(间隔 × 3 ≤ TTL)保证订阅不会在两次刷新之间过期;
// 下界(间隔 ≥ 2 秒)保证没人为了「更跟手」把它调到 0.5 秒 —— 每一拍都是一次
// root 侧 `OwnersByPort()`(两张 pcblist + 逐 PID 解名)。
func TestMenuAppTrafficRefreshIntervalMatchesTheGoTTL(t *testing.T) {
	goSource, err := os.ReadFile(filepath.Join("..", "..", "internal", "supervisor", "apptraffic.go"))
	if err != nil {
		t.Fatalf("读不到 internal/supervisor/apptraffic.go:%v —— 守卫已经失效,先修守卫", err)
	}
	goMatch := regexp.MustCompile(`(?m)^const appTrafficTTL = (\d+) \* time\.Second`).
		FindStringSubmatch(string(goSource))
	if goMatch == nil {
		t.Fatal("在 apptraffic.go 里解不出 appTrafficTTL —— 守卫读不懂现在的代码了,先修守卫")
	}
	goTTL, err := strconv.Atoi(goMatch[1])
	if err != nil || goTTL <= 0 {
		t.Fatalf("appTrafficTTL 解出来是 %q —— 守卫已经失效,先修守卫", goMatch[1])
	}

	swiftSource, err := os.ReadFile(filepath.Join(
		"..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "AppTrafficModel.swift"))
	if err != nil {
		t.Fatalf("读不到 AppTrafficModel.swift:%v —— 守卫已经失效,先修守卫", err)
	}
	swiftTTL, ok := swiftTimeIntervalConstant(t, string(swiftSource), "appTrafficSubscriptionTTLSeconds")
	if !ok {
		t.Fatal("在 AppTrafficModel.swift 里解不出 appTrafficSubscriptionTTLSeconds —— 守卫已经失效,先修守卫")
	}
	interval, ok := swiftTimeIntervalConstant(t, string(swiftSource), "appTrafficRefreshSeconds")
	if !ok {
		t.Fatal("在 AppTrafficModel.swift 里解不出 appTrafficRefreshSeconds —— 守卫已经失效,先修守卫")
	}

	if swiftTTL != goTTL {
		t.Fatalf("Swift 侧记的订阅 TTL 是 %d 秒,Go 侧 appTrafficTTL 是 %d 秒 —— "+
			"两份判据已经漂开,窗口会周期性跳回「没在采集」并把计数清零", swiftTTL, goTTL)
	}
	if interval*3 > goTTL {
		t.Errorf("刷新间隔 %d 秒对 TTL %d 秒没有余量 —— 订阅会在两次刷新之间过期", interval, goTTL)
	}
	if interval < 2 {
		t.Errorf("刷新间隔 %d 秒太密 —— 每一拍都让 root 侧跑一次全量端口扫描", interval)
	}
}

// swiftTimeIntervalConstant 读一个 `let NAME: TimeInterval = N` 的整数值。
// 解不出来时**响亮失败**(而不是当作 0 悄悄通过),理由与本文件其余守卫一致。
func swiftTimeIntervalConstant(t *testing.T, source, name string) (int, bool) {
	t.Helper()
	match := regexp.MustCompile(`(?m)^let ` + regexp.QuoteMeta(name) + `: TimeInterval = (\d+)$`).
		FindStringSubmatch(source)
	if match == nil {
		return 0, false
	}
	value, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return value, true
}

// **一层间接不许重开「窗口关着就不拨」那道门。**
//
// 上一条守卫锚在 main.swift 里的拨号形状上,而那只封住了**一个文件**:在
// `GuardianClient.swift` 里加一句 `func apps() throws -> AppTrafficReport { try
// appTraffic() }`,再从 main.swift 的任意函数调 `GuardianClient().apps()`,
// 端点词一次都不出现在 main.swift 里 —— 整套测试全绿(审查实测)。
//
// 最便宜的闭合法在**客户端这一侧**:`/v1/apps` 只有一个到达点,而那个到达点
// 不许被同文件里的任何函数调用。于是想拨这个端点,只能从 main.swift 调
// `GuardianClient().appTraffic()`,也就必然撞上上一条守卫。
func TestMenuAppTrafficClientExposesExactlyOneWayIn(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(
		"..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "GuardianClient.swift"))
	if err != nil {
		t.Fatalf("读不到 GuardianClient.swift:%v —— 守卫已经失效,先修守卫", err)
	}
	source := string(raw)
	scan := blankSwiftStringLiterals(source)
	defs := swiftFunctionDefs(source)

	// ① 端点只被送进 perform 一次,而且就在那个同名方法里。
	sends := regexp.MustCompile(`perform\(endpoint: \.appTraffic\b`).FindAllStringIndex(scan, -1)
	if len(sends) != 1 {
		t.Fatalf("`perform(endpoint: .appTraffic` 出现 %d 次,应当恰好一次 —— "+
			"多一个入口就是多一条绕开「窗口关着就不拨」的路", len(sends))
	}
	if fn := enclosingSwiftFunc(defs, sends[0][0]); fn != "appTraffic" {
		t.Fatalf("送出 /v1/apps 的是 %q,不是那个同名方法 —— 守卫读不懂现在的代码了,先修守卫", fn)
	}

	// ② 同文件里没有任何函数去调它。一个包装方法(`func apps() { try
	// appTraffic() }`)会让 main.swift 那条链的证明整个失效 —— 与 `typealias
	// CommandRunner = Process` 让 spawn 那条链失效是同一个形状。
	for _, match := range regexp.MustCompile(`\bappTraffic\s*\(`).FindAllStringIndex(scan, -1) {
		if match[0] >= 5 && source[match[0]-5:match[0]] == "func " {
			continue // 定义行自身
		}
		fn := enclosingSwiftFunc(defs, match[0])
		if fn == "" {
			t.Fatalf("偏移 %d 处调了 appTraffic(),却不在任何函数体内 —— "+
				"守卫读不懂现在的代码了,先修守卫", match[0])
		}
		t.Errorf("%s 里包了一层 appTraffic() —— 包装方法会让 main.swift 那条"+
			"「拨号只能从 fetchAppTrafficOnDemand 长出来」的证明整个失效", fn)
	}
}
