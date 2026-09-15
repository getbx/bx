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
		"private func performToggle(_ action: ToggleAction, completion: ((Bool) -> Void)? = nil) {",
	)
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

// **那个映射的每一格都要对上。**
//
// reviewer 变异实测(LANDED):`isCurrent: $0.current` → `isCurrent: false`
// ⇒ `go test ./internal/cli -run TestMacMenu` ok、24 个 Swift 套件全过。
// 而它之下菜单**永远说不出是哪台服务器在失败**,并且会把那台刚刚失败的机器
// 当成「你还配了另一台」推给用户去切 —— 一句正好指错方向的建议。
//
// 上面那条守卫钉的是「实参是一个绑定、它来自 listServers()、它由
// coreStartFailureServers 折出来」—— 每一句都成立,而**喂进去的是什么**
// 没人看。这一条钉值:四个标签各自必须取那个条目上同名的字段
// (isCurrent ← current 是唯一一处改名,单列)。
func TestMacMenuStartFailureServerMappingCarriesTheEntrysOwnFields(t *testing.T) {
	body, ok := swiftFunctionBody(
		stripSwiftComments(menuMainSwiftSource(t)),
		"private func performToggle(_ action: ToggleAction, completion: ((Bool) -> Void)? = nil) {",
	)
	if !ok {
		t.Fatal("main.swift 里找不到 performToggle —— 锚点漂了,回来重判")
	}
	calls := swiftCallArguments(body, "CoreStartFailureServer")
	if len(calls) != 1 {
		t.Fatalf("performToggle 里 CoreStartFailureServer( 的调用有 %d 处,want 1 ——\n"+
			"映射只该有一处,多一处就是多一段没人守的接线。实参:%v", len(calls), calls)
	}
	mapping := calls[0]
	for label, field := range map[string]string{
		"name":      "name",
		"host":      "host",
		"port":      "port",
		"isCurrent": "current",
	} {
		want := regexp.MustCompile(regexp.QuoteMeta(label) + `:\s*\$0\.` + regexp.QuoteMeta(field) + `\b`)
		if !want.MatchString(mapping) {
			t.Errorf("映射里 %s: 拿到的不是 $0.%s —— 实参:%s\n"+
				"写死一个值(reviewer 变异用的是 isCurrent: false)会让菜单永远说不出\n"+
				"是哪台服务器在失败,还会把刚刚失败的那台当成「你还能切过去」推给用户,\n"+
				"而 Go 与 Swift 两侧全绿", label, field, mapping)
		}
	}
}

// 单服务器配置(`bx setup` 写出来的那种)那一半也要喂进去。
//
// 那种配置根本没有 `servers:` 键 ⇒ `/v1/servers` 的 `servers` 是空的,
// 当前那台由 `current_server` 单独带来。**而那是最常见的一种配置** ——
// 靠 `list.servers` 一路等于:一台正常装好 bx 的机器上,这句话永远说不出
// 服务器地址,也就永远给不出那条 `nc -z`。
func TestMacMenuStartFailureFactsIncludeTheSingleServerConfigsServer(t *testing.T) {
	body, ok := swiftFunctionBody(
		stripSwiftComments(menuMainSwiftSource(t)),
		"private func performToggle(_ action: ToggleAction, completion: ((Bool) -> Void)? = nil) {",
	)
	if !ok {
		t.Fatal("main.swift 里找不到 performToggle —— 锚点漂了,回来重判")
	}
	if !strings.Contains(body, "currentServer") {
		t.Fatal("performToggle 一个字都没提 currentServer —— `bx setup` 写出来的配置\n" +
			"(只有 server:、没有 servers: 清单)清单是空的,这句话于是说不出服务器地址")
	}
	// 判据不是「提到过」,是**它真的进了被映射的那个序列**:映射走的是
	// `entries.map { … }`,而 entries 必须由两半拼出来。
	mapped := regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\.map\s*\{\s*\n?\s*CoreStartFailureServer\(`)
	match := mapped.FindStringSubmatch(body)
	if match == nil {
		t.Fatal("找不到「某个序列 .map { CoreStartFailureServer(」—— 锚点漂了,回来重判")
	}
	sequence := match[1]
	if sequence == "list" {
		t.Fatal("映射的是 list.servers —— 单服务器配置下它是空的,current_server 被丢掉了")
	}
	built := regexp.MustCompile(`\b` + regexp.QuoteMeta(sequence) + `\s*=\s*[^\n]*\bcurrentServer\b`)
	if !built.MatchString(body) {
		t.Fatalf("被映射的那个序列 %q 不是由 currentServer 参与拼出来的 ——\n"+
			"单服务器配置下这句话仍然说不出地址", sequence)
	}
	// **另一半也要在**:这条守卫此前只钉了 currentServer 那一半,于是把
	// `list.servers +` 删掉之后 Go 与 24 个 Swift 套件全绿,而一份多服务器
	// 配置(所有者自己那台机器就是)上菜单一台服务器都点不出来、也给不出
	// 「你还配了另一台」。reviewer 变异实测(LANDED)。
	multi := regexp.MustCompile(`\b` + regexp.QuoteMeta(sequence) + `\s*=\s*[^\n]*\.servers\b`)
	if !multi.MatchString(body) {
		t.Fatalf("被映射的那个序列 %q 里没有清单那一半(list.servers)——\n"+
			"多服务器配置下菜单一台都点不出来", sequence)
	}
}

// 那一跳用的是**注进来的那个** GuardianClient,不是当场新造一个。
//
// 同一个闭包上面几行已经在用 self.guardianClient;第二个来源会让一个注进来
// 的客户端被静默绕过,而绕过它的恰好是「说不说得出服务器地址」这一半。
func TestMacMenuStartFailureFactsUseTheSharedGuardianClient(t *testing.T) {
	body, ok := swiftFunctionBody(
		stripSwiftComments(menuMainSwiftSource(t)),
		"private func performToggle(_ action: ToggleAction, completion: ((Bool) -> Void)? = nil) {",
	)
	if !ok {
		t.Fatal("main.swift 里找不到 performToggle —— 锚点漂了,回来重判")
	}
	if !strings.Contains(body, "self.guardianClient.listServers()") {
		t.Fatal("那次取服务器清单没走 self.guardianClient —— 锚点漂了,或者又新造了一个客户端")
	}
	if regexp.MustCompile(`GuardianClient\(\)\s*\.listServers`).MatchString(body) {
		t.Fatal("performToggle 里当场新造了一个 GuardianClient 去取清单 ——\n" +
			"同一个闭包上面几行就在用 self.guardianClient,第二个来源会静默绕过注进来的那个")
	}
}

// **Swift 那份测试自己那张码清单,也必须与 Go 常量逐字相同。**
//
// CoreStartFailureHintTests.swift 头上写着「这份清单由 internal/cli 的一条双向
// 守卫钉住」—— 那句话此前是**假的**:
// TestMacMenuStartFailureCodesMatchTheGoConstants 读的是
// `coreStartFailureHint` 里的 case 字面量,不是这张数组。于是从数组里删掉一个
// 码,Swift 那边就少测一档结局,而两侧全绿。
//
// 这条把那句话变成真的。**一句声称自己被守着、而其实没有的话,比没有话更糟**
// (本仓库为这个形状罚过多次):下一个人读到它,会据此不再去检查那件事。
func TestMacMenuStartFailureHintTestsCoverEveryGoCode(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Tests", "CoreStartFailureHintTests.swift"))
	if err != nil {
		t.Fatalf("读不到 CoreStartFailureHintTests.swift:%v —— 守卫够不着要扫的东西时必须响亮失败", err)
	}
	source := stripSwiftComments(string(raw))
	start := strings.Index(source, "static let codes = [")
	if start < 0 {
		t.Fatal("找不到那张 codes 数组 —— 锚点漂了,回来重判,别静默放行")
	}
	end := strings.Index(source[start:], "]")
	if end < 0 {
		t.Fatal("那张 codes 数组没有收尾的 ] —— 锚点漂了")
	}
	literal := regexp.MustCompile(`"(core_[a-z0-9_]+)"`)
	got := map[string]bool{}
	for _, match := range literal.FindAllStringSubmatch(source[start:start+end], -1) {
		got[match[1]] = true
	}
	if len(got) == 0 {
		t.Fatal("那张 codes 数组里一个码都没读出来 —— 守卫认不出现在的写法")
	}
	want := map[string]bool{}
	for _, code := range supervisor.StartFailureCodes() {
		want[coreStartFailureCodePrefix+code] = true
	}
	var missing, extra []string
	for code := range want {
		if !got[code] {
			missing = append(missing, code)
		}
	}
	for code := range got {
		if !want[code] {
			extra = append(extra, code)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("Swift 那份测试没覆盖这些码:%v —— 它们的那句话从此无人验,而两侧全绿", missing)
	}
	if len(extra) > 0 {
		t.Errorf("Swift 那份测试里的 %v 不是 supervisor 的启动失败码 —— 它测的是一段死代码", extra)
	}
}
