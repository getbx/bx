package cli

import (
	"os"
	"path/filepath"
	"regexp"
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

func menuAppTrafficWindowSource(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(
		"..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "AppTrafficWindow.swift"))
	if err != nil {
		t.Fatalf("读不到 AppTrafficWindow.swift:%v —— 守卫已经失效,先修守卫", err)
	}
	return string(source)
}

// **能力门控,绝不「试着拨一下看看」。**
//
// 旧 Guardian 不认识 /v1/apps,会回 404 —— 而客户端无从区分「这版不支持」与
// 「这版支持但此刻没数据」。这不是纸面推演:`bx status --watch` 顶着一台旧
// Guardian 跑时,解出的代际号恒 0、与起始值恰好相等,「未变化」分支被命中且没有
// 任何错误可供退避介入,真机实测本机 unix socket 常驻 CPU 26%~46%、吞吐上千
// 次/秒。
//
// 判据必须是 appTrafficAvailable(它认的是 "apps" 这个键),不是别的能力判据 ——
// 用 rulesEditingAvailable 会在「装了带规则、不带应用归因的那一版」上把入口画
// 出来,而那正是这道门要挡的情形。
func TestMacMenuGatesAppTrafficOnCapability(t *testing.T) {
	body, ok := swiftFunctionBody(stripSwiftComments(menuMainSwiftSource(t)), "private func rebuildMenu()")
	if !ok {
		t.Fatal("读不出 rebuildMenu() 的函数体 —— 守卫已经失效,先修守卫")
	}
	const action = "#selector(openAppTrafficWindow)"
	idx := strings.Index(body, action)
	if idx < 0 {
		t.Fatal("rebuildMenu() 里没有 Traffic by App 入口 —— 用户在菜单里看不到这个窗口")
	}
	// 往回找**最近**的一个 if,它必须就是这个能力判据。固定字节窗口在本仓库被
	// 邻近函数满足过,所以这里找的是「包着它的那个条件」,不是「附近有没有出现
	// 过这个字符串」。
	before := body[:idx]
	gate := strings.LastIndex(before, "if appTrafficAvailable(")
	other := strings.LastIndex(before, "if ")
	if gate < 0 || gate != other {
		t.Fatalf("Traffic by App 入口不是由 appTrafficAvailable 直接门控的 —— "+
			"最近的条件在 %d,能力判据在 %d", other, gate)
	}
	// 实参必须是**光秃秃的一次取值**。`capabilities: report?.capabilities ?? []`
	// 这类写法会把「这版压根没声明过能力」压成「声明了、一个都没有」,两者在
	// 这道门上结论相同、在别处不同,而漂开的那天没有任何东西会红。
	gateLine := body[gate:]
	if end := strings.IndexByte(gateLine, '\n'); end >= 0 {
		gateLine = gateLine[:end]
	}
	if !regexp.MustCompile(`if appTrafficAvailable\(capabilities: maintenanceReport\?\.capabilities\) \{`).
		MatchString(gateLine) {
		t.Errorf("能力实参不是 maintenanceReport?.capabilities 这一次光秃秃的取值:%s", gateLine)
	}
}

// **窗口关着就不拨。**
//
// 这是这个功能要买到的收益:没人看时开销精确为零,而且不在这台机器上留下「你
// 开过什么应用」的记录。订阅是靠每一次拉取续期的(Core 侧 30 秒 TTL,惰性
// 结算),所以「不拨」等价于「不采集」—— 反过来,任何一条不看窗口可见性就拨的
// 路径,都会让 Core 永远开着采集。
func TestMacMenuOnlyFetchesAppTrafficWhileWindowVisible(t *testing.T) {
	source := stripSwiftComments(menuMainSwiftSource(t))
	defs := swiftFunctionDefs(source)

	// ① 全部调用点必须落在白名单里。**从调用点倒着锁,不是枚举「刷新路径有哪些
	// 函数」** —— 后者每加一个 helper 都要有人记得回来改,而漏掉不会有任何症状
	// (spawn 那条链就是这么被攻破的)。
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

	// ② 环境刷新那一路必须先判窗口可见。**最近的那个 if 就得是它**。
	refresh, ok := swiftFunctionBody(source, "private func applyRefresh(_ outcome: RefreshOutcome, capturedGeneration: Int)")
	if !ok {
		t.Fatal("读不出 applyRefresh 的函数体 —— 守卫已经失效,先修守卫")
	}
	idx := strings.Index(refresh, "fetchAppTrafficOnDemand(")
	if idx < 0 {
		t.Fatal("applyRefresh 里没有按需刷新应用流量 —— 打开着的窗口会冻在打开那一刻")
	}
	before := refresh[:idx]
	gate := strings.LastIndex(before, "if appTrafficWindow.isVisible {")
	other := strings.LastIndex(before, "if ")
	if gate < 0 || gate != other {
		t.Fatalf("环境刷新那一路不是由 appTrafficWindow.isVisible 直接门控的 —— "+
			"最近的条件在 %d,可见性判据在 %d", other, gate)
	}
	if !strings.Contains(refresh[idx:], "fetchAppTrafficOnDemand(forceShow: false)") {
		t.Error("环境刷新那一路没有用 forceShow: false —— 它会每次都抢焦点把窗口推到用户面前")
	}

	// ③ 心跳定时器必须随窗口关闭停掉。窗口关了而定时器还在跑,订阅就永远续着,
	// 「关掉就完全停」这句话就是假的 —— 而界面上看不出任何异常。
	start, ok := swiftFunctionBody(source, "private func startAppTrafficTimer()")
	if !ok {
		t.Fatal("读不出 startAppTrafficTimer 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(start, "commonModeTimer(") {
		t.Error("心跳不是 commonModeTimer 建的 —— Timer.scheduledTimer 只进 .default,菜单展开期间一次都不触发")
	}
	stop, ok := swiftFunctionBody(source, "private func stopAppTrafficTimer()")
	if !ok {
		t.Fatal("读不出 stopAppTrafficTimer 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(stop, "invalidate()") || !strings.Contains(stop, "appTrafficTimer = nil") {
		t.Error("stopAppTrafficTimer 没有真的把定时器停掉并清空")
	}
	// 窗口关闭的回调必须接到它上面。
	closeWiring := regexp.MustCompile(`controller\.onClose = \{[^}]*stopAppTrafficTimer\(\)`)
	if !closeWiring.MatchString(source) {
		t.Fatal("窗口关闭没有停掉心跳 —— 窗口关了、采集还开着,而界面上看不出任何异常")
	}
}

// **界面上必须写明字节数是近似值。**
//
// 端口复用会让残留字节算到新连接头上,spec 明写「界面不该把它显示成精确账」。
// 这句话属于窗口本身:一个只在 CLI 里说、界面上不说的免责声明等于没说。
//
// 守卫锚在**常量标识符**上而不是那句英文的字面拼法 —— 换个措辞不该让守卫红,
// 而把整行删掉必须红。那句话的内容由 Swift 套件钉住
// (testApproximateNoteSaysWhichNumbersAreApproximate)。
func TestMacMenuAppTrafficWindowSaysByteCountsAreApproximate(t *testing.T) {
	window := stripSwiftComments(menuAppTrafficWindowSource(t))
	idx := strings.Index(window, "appTrafficApproximateNote")
	if idx < 0 {
		t.Fatal("悬浮窗里没有那句「字节数是近似值」的小字 —— " +
			"界面把一笔近似账显示成了精确账")
	}
	// 光提到它不算数:必须真的摆进那个窗口的视图树里。
	if !regexp.MustCompile(`appTrafficApproximateNote`).MatchString(window) ||
		!strings.Contains(window, "addArrangedSubview") {
		t.Fatal("那句小字没有被摆进视图树 —— 守卫已经失效,先修守卫")
	}
	tail := window[idx:]
	head := window[:idx]
	if !strings.Contains(head, "addArrangedSubview") && !strings.Contains(tail, "addArrangedSubview") {
		t.Error("那句小字没有出现在任何一次 addArrangedSubview 附近 —— 它没有被真的显示出来")
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
