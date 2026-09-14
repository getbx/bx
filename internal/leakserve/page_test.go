package leakserve

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/leakcheck"
)

// 页面必须在**联网之前**原样显示要联系谁(设计风险三:第三方暴露是用户明确
// 接受的,但必须可见)。断言打在真实响应体上,不是打在模板文件上。
func TestPageDisclosesEndpointsVerbatim(t *testing.T) {
	srv := newTestServer(t)
	resp := get(t, srv, "/?t="+srv.Token())
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	for _, want := range []string{leakcheck.EchoV4URL, leakcheck.EchoV6URL, leakcheck.STUNURL} {
		if !strings.Contains(html, want) {
			t.Errorf("页面必须原样显示 %q,它是用户可见契约", want)
		}
	}
	// 页面还必须带上 token,否则它自己发不出 POST。
	if !strings.Contains(html, srv.Token()) {
		t.Error("页面里必须带 token,否则它的上报请求会被自己的闸门拒掉")
	}
	// 这一页带着本次运行专属的 token,不该进磁盘缓存。
	//
	// 原表把这条记为「不转红,靠 review」——它其实是可测的,而这个仓库的教训正是
	// 「记在案上靠 review」的东西没人再看第二眼。
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("带 token 的页面必须 no-store,得到 Cache-Control: %q", got)
	}
}

// **JS 愚蠢的结构性保证之一**:页面拿到的响应里,只有已经判完的结论,
// 没有任何可供它自己判断的原料。顶层键集合逐字锁定。
func TestReportResponseCarriesFinishedConclusions(t *testing.T) {
	srv := newTestServer(t)
	req, err := http.NewRequest(http.MethodPost,
		"http://"+srv.Addr().String()+"/report?t="+srv.Token(),
		strings.NewReader(`{"exit_v4":"5.6.7.8","srflx":["1.2.3.4"]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", srv.Origin())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var top map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&top); err != nil {
		t.Fatalf("上报响应必须是 JSON: %v", err)
	}
	want := map[string]bool{
		"generated_at": true, "endpoints": true,
		"findings": true, "evidence": true, "anomaly_count": true,
		// identity_count 是**成品结论**(身份段有几条 bad),与 anomaly_count 同类,
		// 不是可判断的原料。两个数刻意分开:合成一个总数时它永远不为零,
		// 于是会被训练成噪声,连带把真正的泄漏一起淹掉。
		"identity_count": true,
		// reach 是第四段(可达性)的五态计数,与上面两个数同类 —— 同样是
		// **成品结论**,不是原料。它刻意与 anomaly_count/identity_count 并排
		// 而不合并:可达性的坏消息是「你用不了」,path/identity 的坏消息是
		// 「你泄漏了」,后者才是安全问题;合成一个数就是让一次连不上稀释掉
		// 真正的泄漏告警。
		"reach": true,
	}
	for key := range top {
		if !want[key] {
			t.Errorf("上报响应多了顶层键 %q:页面只能拿到成品结论,多一个原料键就是"+
				"给 JS 递上了可判断的东西,而这个仓库的测试覆盖不到 JS", key)
		}
	}
	for key := range want {
		if _, ok := top[key]; !ok {
			t.Errorf("上报响应缺了 %q", key)
		}
	}

	// findings 里的 verdict 必须已经是三个词之一,不是数字/布尔。
	var report struct {
		Findings []struct {
			Verdict string `json:"verdict"`
			Summary string `json:"summary"`
		} `json:"findings"`
	}
	raw, _ := json.Marshal(top)
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) == 0 {
		t.Fatal("响应里没有 findings")
	}
	for _, f := range report.Findings {
		switch f.Verdict {
		case "ok", "bad", "not checked":
		default:
			t.Errorf("verdict 必须是三个词之一,得到 %q —— 数字/布尔会逼 JS 自己映射", f.Verdict)
		}
		if f.Summary == "" {
			t.Error("summary 不能为空:页面除了显示它没有别的事可做")
		}
	}
}

// **JS 愚蠢的结构性保证之二**:Report 的字段类型里不许出现 BrowserReport 或
// LocalFacts。本机事实那一半从不下发 —— 没有它,「srflx ≠ 出口」这类结论在 JS
// 里不可能算得出来,缺的不是代码,是数据。
func TestReportTypeCarriesNoRawMaterial(t *testing.T) {
	banned := map[reflect.Type]string{
		reflect.TypeOf(leakcheck.BrowserReport{}): "浏览器原始上报",
		reflect.TypeOf(leakcheck.LocalFacts{}):    "本机事实",
	}
	typ := reflect.TypeOf(leakcheck.Report{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		ft := field.Type
		for ft.Kind() == reflect.Ptr || ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		if why, bad := banned[ft]; bad {
			t.Errorf("Report.%s 的类型是 %s(%s):页面拿到 Report,把原料塞进去就等于"+
				"把判断权交给了测不到的 JS", field.Name, ft, why)
		}
	}
}

// **本机事实那一半从不下发**这条论证的支点,是页面能拿到的东西只有 token 与
// 第三方披露 —— 所以这里穷举 pageData 的字段名,照 TestOptionsHasNoListenAddressField
// 的形状写。
//
// 这条是变异验证逼出来的:给 pageData 加一个 `Local leakcheck.LocalFacts` 字段
// 并在模板里注入,上面三条守卫**一条都不会红**(它们守的是 Report 的类型与
// /report 的响应,不是页面模板拿到的数据)。而那一步正好把判断权交给了这个仓库
// 测不到的 JS —— Task 12 的全部论证都建立在「页面拿不到原料」上。
// **ChecksJSON 的论证(2026-08-11 加字段时,被这条守卫当场拦下后写的)**:
//
// 它是**结构**,不是原料。内容只有三条结论的 id、标题,以及每条等哪几个浏览器
// 探测的名字 —— 没有地址、没有接口名、没有任何一条本机观测。页面拿它只能把行
// 先摆出来并在探测落定时点亮对应的格,**推不出任何结论**;绿色仍然只在 Go 判完
// 之后由 /report 的响应带来。
//
// 反过来问一句更有用:为什么骨架也非得从 Go 来?因为「哪一项吃哪几个探测」是
// **判据的知识**。页面自己抄一份的话,加第四条规则时要改两个地方,而其中一个
// (JS)这个仓库测不到 —— 两边悄悄不一致时,页面会摆出一行永远等不到结论的空壳,
// 或者收到一条没有位置可放的结论,而没有任何东西会红。
// leakcheck.TestOutlineMatchesWhatJudgeActuallyEmits 拿真实的 Judge 输出钉住这一点。
func TestPageDataCarriesOnlyTokenDisclosureAndSkeleton(t *testing.T) {
	typ := reflect.TypeOf(pageData{})
	want := map[string]bool{"Token": true, "Endpoints": true, "ChecksJSON": true}
	if typ.NumField() != len(want) {
		t.Fatalf("pageData 现在有 %d 个字段,守卫只认识 %d 个 —— 加字段请连同这条守卫"+
			"一起论证:页面多拿到一样原料,判断就可能搬进测不到的 JS 里",
			typ.NumField(), len(want))
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !want[field.Name] {
			t.Errorf("pageData 多了字段 %q(类型 %s):页面只该拿到 token、第三方披露与结论骨架,"+
				"本机事实那一半从不下发 —— 新字段请在上面的注释里论证它为什么不是可判断的原料",
				field.Name, field.Type)
		}
	}
	// 字段名对了、类型换成原料也一样致命(把 Endpoints 换成 LocalFacts 不会撞上
	// 上面那条数量断言)。
	banned := map[reflect.Type]string{
		reflect.TypeOf(leakcheck.BrowserReport{}): "浏览器原始上报",
		reflect.TypeOf(leakcheck.LocalFacts{}):    "本机事实",
	}
	for i := 0; i < typ.NumField(); i++ {
		ft := typ.Field(i).Type
		for ft.Kind() == reflect.Ptr || ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		if why, bad := banned[ft]; bad {
			t.Errorf("pageData.%s 的类型是 %s(%s):这正是「页面拿不到原料」这条论证的支点",
				typ.Field(i).Name, ft, why)
		}
	}
}

// pageScriptFunction 取出页面脚本里一个具名函数的函数体。读不出来必须**响亮
// 失败** —— 一个读不懂现在代码的守卫,静默放行比没有守卫更糟。
func pageScriptFunction(t *testing.T, name string) string {
	t.Helper()
	head := "function " + name + "("
	start := strings.Index(pageHTML, head)
	if start < 0 {
		t.Fatalf("页面里找不到 %s —— 本守卫读不懂现在的代码,请连同它一起重写", name)
	}
	rest := pageHTML[start:]
	// 函数体缩进两格,收尾就是行首两格加右花括号。
	end := strings.Index(rest, "\n  }\n")
	if end < 0 {
		t.Fatalf("找不到 %s 的收尾 —— 本守卫读不懂现在的代码,请连同它一起重写", name)
	}
	return rest[:end]
}

// **每一个出网探测都必须有自己的上限,而且共用同一个预算。**
//
// 这条守卫是 JS 唯一能被 CI 看到的地方(本仓库的测试与变异验证覆盖不到页面)。
// 没有超时的 fetch 在一台没有 IPv6 的机器上会卡在 OS 的 TCP 超时(macOS 约 75 秒),
// 而 `Promise.all` 等最慢的那个 —— 整次检测于是被推向 bx 的 2 分钟硬超时,
// 也就是「浏览器那一半从来没到过」。**一个放弃的探测会说明原因;一个挂住的探测
// 让整次检测什么都说不出来。**
func TestPageProbesShareOneBudgetAndCannotHang(t *testing.T) {
	const decl = "var PROBE_TIMEOUT_MS = "
	if strings.Count(pageHTML, decl) != 1 {
		t.Fatalf("页面必须**恰好**声明一次探测预算 %q —— 两份预算会各自漂移", decl)
	}
	var budget int
	if _, err := fmt.Sscanf(pageHTML[strings.Index(pageHTML, decl)+len(decl):], "%d", &budget); err != nil {
		t.Fatalf("读不出探测预算:%v", err)
	}
	if budget <= 0 || budget > 15000 {
		t.Fatalf("探测预算 %dms 不合理:必须为正,而且要远小于 2 分钟的硬超时", budget)
	}

	echo := pageScriptFunction(t, "fetchEcho")
	if !strings.Contains(echo, "AbortController") || !strings.Contains(echo, "signal:") {
		t.Errorf("fetchEcho 必须给 fetch 带上一个会被 abort 的 signal,否则它没有上限:\n%s", echo)
	}
	if !strings.Contains(echo, "PROBE_TIMEOUT_MS") {
		t.Errorf("fetchEcho 的上限必须来自那个共用预算,不许自带一个数字:\n%s", echo)
	}

	ice := pageScriptFunction(t, "gatherSrflx")
	if !strings.Contains(ice, "PROBE_TIMEOUT_MS") {
		t.Errorf("ICE 收集的上限也必须来自那个共用预算 —— 两个探测各写一个数字,"+
			"改一个忘改另一个不会有任何报错:\n%s", ice)
	}
}

// 页面必须把「本机服务已经没了」与别的失败**分开**说。
//
// **这是真机用出来的**(2026-08-11):一次 leakcheck 超时自关之后,那个标签页
// 还开着;用户点「Run the check」,探测都跑完了,而结果 POST 回一个早已关闭的
// 端口 —— 浏览器对此只有一句 `TypeError: Failed to fetch`,页面照着说
// 「bx will report this as "not checked"」。
//
// **那是假话,而且是这个功能存在的全部意义要消灭的那一类**:bx 根本不在跑,
// 什么都不会被报告。页面在替一个已经退出的进程做承诺。
//
// 一次性是刻意的(拿到结果即关 + 2 分钟硬超时),所以标签页活得比服务久是
// **常态**,不是边角情况。
func TestPageDistinguishesAnExpiredServiceFromOtherFailures(t *testing.T) {
	page := string(pageHTML)

	// **POST 那一处**的失败必须被单独接住,而不是掉进通用文案。
	//
	// 只查「页面里出现过 EXPIRED」是不够的 —— 实测:把 POST 后面那个 .catch 拿掉,
	// EXPIRED 仍然出现在别处(变量声明与 !resp.ok 分支),守卫照样绿。守卫必须钉
	// 那个**具体的落点**,而不是那个词。
	post := strings.Index(page, `fetch("/report?t="`)
	if post < 0 {
		t.Fatal("找不到回传那次 fetch,这条守卫已经读不懂页面了 —— 请连它一起更新")
	}
	// 从 POST 起到它这条表达式收尾(紧接着的 .then)为止。
	tailAfterPost := page[post:]
	stop := strings.Index(tailAfterPost, "}).then(")
	if stop < 0 {
		t.Fatal("回传那段的形状变了,守卫读不懂 —— 请连它一起更新")
	}
	if !strings.Contains(tailAfterPost[:stop], "throw new Error(EXPIRED)") {
		t.Error("回传失败没有被单独接住 —— 端口已关时浏览器只会说 Failed to fetch," +
			"而页面会照着通用文案声称「bx will report this as not checked」," +
			"可 bx 根本不在跑。标签页活得比一次性服务久是常态,不是边角情况")
	}
	// 过期分支必须说清三件事:过期了、什么都没上报、怎么重来。
	for _, must := range []string{
		"expired",
		"Nothing was reported",
		"bx leakcheck again",
	} {
		if !strings.Contains(page, must) {
			t.Errorf("过期文案里缺 %q —— 用户需要知道「什么都没发生」以及「怎么重来」", must)
		}
	}
	// 而且**过期时绝不能**说 bx 会把它记成 not checked:bx 不在跑。
	idx := strings.Index(page, "This check has expired")
	if idx < 0 {
		t.Fatal("找不到过期文案,这条守卫已经读不懂页面了")
	}
	tail := page[idx:]
	end := strings.Index(tail, "return;")
	if end < 0 {
		t.Fatal("过期分支的形状变了,守卫读不懂 —— 请连它一起更新")
	}
	if strings.Contains(tail[:end], "not checked") {
		t.Error("过期分支里出现了「not checked」—— bx 根本不在跑,它什么都不会报告")
	}
}

// **页面对认不出的分段是静默回落到 titles.path** —— 与 CLI 那个 default 兜底
// 同一个形状。第四段一加进来,四行可达性结论就会出现在一个写着「你的流量去
// 哪儿」的标题底下,而页面这一侧**两边测试都绿**:骨架行摆出来了、ID 对得上、
// 结论也贴回去了,只有那个标题在说一件判据没说过的事。
//
// 判据按 Outline() 现有的分段穷举:每一段都必须在 titles 里有**自己**的条目,
// 而且标题两两不同。
func TestPageGivesEverySectionItsOwnHeading(t *testing.T) {
	block := pageTitlesBlock(t)
	entry := regexp.MustCompile(`(?m)^\s*(\w+)\s*:\s*\[\s*"((?:[^"\\]|\\.)*)"`)
	titles := map[string]string{}
	for _, m := range entry.FindAllStringSubmatch(block, -1) {
		titles[m[1]] = m[2]
	}
	if len(titles) == 0 {
		t.Fatal("在 page.html 的 skeleton() 里读不出任何分段标题 —— 本守卫读不懂现在的代码,请连同它一起重写")
	}
	seenHeading := map[string]string{}
	seenSection := map[string]bool{}
	for _, o := range leakcheck.Outline() {
		sec := o.Section.String()
		if seenSection[sec] {
			continue
		}
		seenSection[sec] = true
		head, ok := titles[sec]
		if !ok {
			t.Fatalf("分段 %q 在页面里没有自己的标题,会静默回落到 titles.path —— "+
				"「连不上」于是被画在「你的流量去哪儿」底下,读起来就是一次泄漏", sec)
		}
		if other, dup := seenHeading[head]; dup {
			t.Fatalf("分段 %q 与 %q 共用同一个标题 %q", sec, other, head)
		}
		seenHeading[head] = sec
	}
}

// 第四段那几行不是「只看本机就答得出来」的:bx 为它们联系了第三方。页面对不吃
// 浏览器探测的行统一写「Judged from this machine alone.」,在这一段里那是一句
// 与它自己的分段标题刚刚披露的事实相矛盾的话。
func TestPageDoesNotSayTheReachRowsNeededNoNetwork(t *testing.T) {
	raw := pageFunctionBody(t, "function skeleton(")
	blanked := blankPageNoise(raw)

	// ① 措辞本身:第四段那几行必须有自己的说法,而且它认的是分段不是行号。
	if !strings.Contains(raw, `"reach"`) || !strings.Contains(raw, "Probed by bx from this machine") {
		t.Fatalf("skeleton() 没有为第四段单独措辞 —— 它会说这几行「只看本机」,"+
			"而 bx 为它们联系了 Anthropic/OpenAI/Google:\n%s", raw)
	}

	// ② **算出来了还得摆进去。** 判据钉的此前是「这个串出现在函数体里」——
	// 把末尾改成无条件用 "Judged from this machine alone."(local 算了不用),
	// 那条守卫照样全绿,而页面会对四行「bx 刚联系过三家 AI 厂商」的结论说
	// 「只看本机就答出来了」。这半页 JS 没有第二层覆盖(Inputs 为 nil ⇒ 它是
	// 活路径),所以判据必须从那个绑定出发,一路跟到它真的进了 DOM。
	//
	// 形状照 internal/cli 那边 swiftValueReachesViewTree:从种子标识符出发,
	// 要求它出现在 text(...) / appendChild(...) 的**实参**里。
	seed := jsBindingName(t, "Probed by bx from this machine", raw)
	if !jsIdentifierReachesCall(blanked, seed, []string{"text", "appendChild"}) {
		t.Fatalf("第四段那句话被算了出来却没有进 DOM(%q 没有出现在任何 text(...)/"+
			"appendChild(...) 的实参里)—— 页面仍然会说这几行只看本机:\n%s", seed, raw)
	}
}

// jsBindingName 找到把含 needle 的那个字面量绑上去的那个 `var X =`,返回 X。
func jsBindingName(t *testing.T, needle, raw string) string {
	t.Helper()
	for _, line := range strings.Split(raw, "\n") {
		if strings.Contains(line, needle) {
			m := regexp.MustCompile(`var\s+(\w+)\s*=`).FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("含 %q 的那一行不是一个 `var X =` 绑定,本守卫读不懂现在的代码,"+
					"请连同它一起重写:%q", needle, line)
			}
			return m[1]
		}
	}
	t.Fatalf("在 skeleton() 里找不到 %q —— 本守卫读不懂现在的代码,请连同它一起重写", needle)
	return ""
}

// jsIdentifierReachesCall 问:标识符 ident 是不是出现在某个 names 里的调用的实参中。
// 输入必须是**注释与字符串都抹白过**的那一份 —— 否则一句注释里提到这个名字就
// 能让守卫平凡成立(本仓库为这个形状吃过假绿)。
func jsIdentifierReachesCall(blanked, ident string, names []string) bool {
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(ident) + `\b`)
	for _, name := range names {
		call := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*\(`)
		for _, loc := range call.FindAllStringIndex(blanked, -1) {
			open := loc[1] - 1
			depth := 0
			for i := open; i < len(blanked); i++ {
				switch blanked[i] {
				case '(':
					depth++
				case ')':
					depth--
					if depth == 0 {
						if word.MatchString(blanked[open+1 : i]) {
							return true
						}
						i = len(blanked)
					}
				}
			}
		}
	}
	return false
}

// pageTitlesBlock 抠出 skeleton() 里那个 `var titles = { … }` 的对象字面量。
func pageTitlesBlock(t *testing.T) string {
	t.Helper()
	src := blankPageNoise(pageHTML)
	anchor := strings.Index(src, "var titles = {")
	if anchor < 0 {
		t.Fatal("page.html 里找不到 `var titles = {` —— 本守卫读不懂现在的代码,请连同它一起重写")
	}
	open := strings.Index(src[anchor:], "{") + anchor
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				// 抹白只是为了数括号,取内容仍从原串取(字面量里的括号已被抹掉,
				// 偏移逐字节对齐)。
				return pageHTML[open : i+1]
			}
		}
	}
	t.Fatal("page.html 里 titles 对象的括号配不平 —— 本守卫读不懂现在的代码,请连同它一起重写")
	return ""
}

// pageFunctionBody 抠出一个以 header 开头的 JS 函数体(含花括号)。
func pageFunctionBody(t *testing.T, header string) string {
	t.Helper()
	src := blankPageNoise(pageHTML)
	anchor := strings.Index(src, header)
	if anchor < 0 {
		t.Fatalf("page.html 里找不到 %q —— 本守卫读不懂现在的代码,请连同它一起重写", header)
	}
	open := strings.Index(src[anchor:], "{") + anchor
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return pageHTML[open : i+1]
			}
		}
	}
	t.Fatalf("%q 的括号配不平 —— 本守卫读不懂现在的代码,请连同它一起重写", header)
	return ""
}

// **已知缺口(记档,今天安全)**:它不认反引号模板串,也不认正则字面量 ——
// `/}/` 里那个花括号会被数进去。今天 page.html 里两者都没有;真出现时后果是
// **括号配不平 ⇒ 上面那几个 helper 一律 t.Fatal 响亮失败**,不是静默放行,
// 所以留着这个缺口是安全的那一边。下一个往 page.html 里写模板串或正则的人,
// 会先被这条守卫的 Fatal 拦下来,那正是回来把它补上的时刻。
//
// blankPageNoise 把 page.html 里 <script> 那一段的注释与字符串字面量抹白,
// 而**保住每个字节偏移** —— 数括号时不许把注释或字面量里的 `{`/`}` 数进去
// (本仓库为这个根因同时吃过假绿与假红:`stripSwiftComments` 保留字符串内容,
// 而随后每个扫描器都在数括号)。
//
// 先把 <script> 之前那半 HTML 整个抹掉:那里的散文里全是 `bx's` 这样的撇号,
// 单引号状态机会从那儿一路吃到文件末尾。
func blankPageNoise(src string) string {
	out := []byte(src)
	begin := strings.Index(src, "<script>")
	if begin < 0 {
		begin = 0
	}
	for i := 0; i < begin; i++ {
		if out[i] != '\n' {
			out[i] = ' '
		}
	}
	const (
		code = iota
		lineComment
		blockComment
		dquote
		squote
	)
	state := code
	blank := func(i int) {
		if out[i] != '\n' {
			out[i] = ' '
		}
	}
	for i := begin; i < len(out); i++ {
		c := out[i]
		switch state {
		case code:
			switch {
			case c == '/' && i+1 < len(out) && out[i+1] == '/':
				state = lineComment
				blank(i)
				blank(i + 1)
				i++
			case c == '/' && i+1 < len(out) && out[i+1] == '*':
				state = blockComment
				blank(i)
				blank(i + 1)
				i++
			case c == '"':
				state = dquote
			case c == '\'':
				state = squote
			}
		case lineComment:
			if c == '\n' {
				state = code
			} else {
				blank(i)
			}
		case blockComment:
			if c == '*' && i+1 < len(out) && out[i+1] == '/' {
				blank(i)
				blank(i + 1)
				i++
				state = code
			} else {
				blank(i)
			}
		case dquote, squote:
			quote := byte('"')
			if state == squote {
				quote = '\''
			}
			if c == '\\' && i+1 < len(out) {
				blank(i)
				blank(i + 1)
				i++
				continue
			}
			if c == quote {
				state = code
				continue
			}
			blank(i)
		}
	}
	return string(out)
}
