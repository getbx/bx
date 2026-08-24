package leakcheck

// 浏览器那半的探测项。页面用它们标记「哪一个探测刚落定」;Go 用它们
// 声明每条结论吃哪几个探测。
//
// **这里曾经写着「两边用同一组常量,免得页面自己抄一份」—— 那是假话。**
// 页面拿到的骨架(`CHECKS[].inputs`)确实来自这几个常量,但页面自己调用
// `probeLanded("srflx", …)` / `fetchEcho(ECHO4, "exit_v4")` 用的是**手抄的
// 字面量**:`leakserve.pageData` 里根本没有这几个常量(那份字段集合由
// `TestPageDataCarriesOnlyTokenAndDisclosure` 穷举钉住)。
//
// 漂移的后果是静默的:`skeleton()` 按 `c.inputs` 建 `cells`,而 `probeLanded`
// 按页面那份查表 —— 对不上时 `cells[name]` 是 undefined,
// `(cells[name] || []).forEach` 什么也不做,那一格**永远停在「还在等」**,
// 而这里的测试照样绿(它们用的是常量),页面也不知道 Go 改过名。
// 现由 `leakserve.TestPageProbeNamesMatchTheGoConstants` **双向**钉住:页面用的
// 每个名字都必须是真常量,每个常量都必须在页面里被用到。改名字要改两处。
const (
	ProbeExitV4  = "exit_v4"
	ProbeExitV6  = "exit_v6"
	ProbeSRFLX   = "srflx"
	ProbeTrace   = "trace"
	ProbeSurface = "surface"
)

// CheckOutline 是一条结论的**骨架**:它是什么、它吃哪几个浏览器探测。
//
// **它不含任何判断**,只含结构。页面拿它把三行先摆出来,并在每个探测落定时
// 点亮对应的那一格 —— 于是用户看得见「两半在到齐」这件事本身,而不是盯着一个
// 转圈的图标。
//
// 为什么结构也由 Go 给:「哪一项吃哪几个探测」是**判据的知识**。让页面自己抄
// 一份,加第四条规则时就要改两个地方,而其中一个(JS)这个仓库测不到 ——
// 于是两边悄悄不一致时,没有任何东西会红。
type CheckOutline struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Section 让页面把行分组。**它也必须从 Go 来** —— 页面自己抄一份分组规则时,
	// 一条结论会出现在它不属于的那一段下面,而那一段的标题正是在向用户说明
	// 「这一段谁负责」。
	Section Section `json:"section"`
	// Inputs 是这条结论要等的浏览器探测。**空表示它只吃本机事实** ——
	// 那不是缺陷,而是一条有用的信息:DNS 那条根本不需要浏览器,页面照实显示。
	Inputs []string `json:"inputs"`
}

// Outline 返回三条结论的骨架,顺序与 Judge 产出的顺序一致。
//
// 顺序与 ID 都由 TestOutlineMatchesWhatJudgeActuallyEmits 钉住:骨架与结果一旦
// 对不上,页面就会摆出一行永远等不到结论的空骨架,或者收到一条没有位置可放的
// 结论 —— 两种都是「界面在说一件判据没说过的事」。
func Outline() []CheckOutline {
	return []CheckOutline{
		// 只吃本机事实:路由表说了算,浏览器一个包都不用发。
		{ID: FindingCarrier, Title: "Who carries your traffic", Section: SectionPath, Inputs: nil},
		{ID: FindingWebRTC, Title: "WebRTC vs HTTP exit", Section: SectionPath, Inputs: []string{ProbeSRFLX, ProbeExitV4}},
		{ID: FindingIPv6, Title: "IPv6 exposure", Section: SectionPath, Inputs: []string{ProbeExitV6}},
		// 只吃本机事实:默认路由归谁、解析器走哪个接口。浏览器一个包都不用发。
		{ID: FindingDNS, Title: "DNS path", Section: SectionPath, Inputs: nil},
		// 同样只吃本机事实:纯路由表观测,一个包都不用发。
		{ID: FindingRouteEscape, Title: "Routes around the tunnel", Section: SectionPath, Inputs: nil},
		// **与 WebRTC 那条共用同一次 ICE gathering**:host candidate 和 srflx 是
		// 一次采集的两半,页面不会为它多发一个包。共用探测名是如实的 ——
		// 两行确实在等同一件事落定。
		{ID: FindingLocalAddresses, Title: "Local network addresses", Section: SectionIdentity, Inputs: []string{ProbeSRFLX}},
		{ID: FindingTimezone, Title: "Clock vs exit location", Section: SectionIdentity, Inputs: []string{ProbeTrace}},
		{ID: FindingLanguage, Title: "Language vs exit location", Section: SectionIdentity, Inputs: []string{ProbeTrace}},
		{ID: FindingFingerprint, Title: "Fingerprint defences", Section: SectionIdentity, Inputs: []string{ProbeSurface}},
		{ID: FindingSurface, Title: "What sites can read", Section: SectionSurface, Inputs: []string{ProbeSurface}},
	}
}
