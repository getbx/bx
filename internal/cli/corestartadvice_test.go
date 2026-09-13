package cli

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/supervisor"
)

// 每一种结局在**渲染出来的整段话**上两两不同。
//
// 判据刻意不是「枚举值不同」:把五个分支映射到同一句「隧道没起来」照样能让
// 一条比枚举的测试全绿,而那正是这一支要消灭的东西 —— 2026-09-12 那天用户
// 读到的是七次同一句关于幻影 Core 的话,真相(VPS 的 443 没有应答)一个字
// 没出现。
func TestEveryStartFailureOutcomeReadsDifferently(t *testing.T) {
	facts := startFailureServers{
		CurrentName:     "vps",
		CurrentHostPort: "195.133.192.92:443",
		Others:          []string{"tokyo(166.1.190.123)"},
	}
	rendered := map[string]string{}
	for _, code := range coreStartFailureCodes() {
		text := coreStartFailureAdvice(code, facts)
		if strings.TrimSpace(text) == "" {
			t.Fatalf("%s 一个字都没说 —— 一个没有出路的失败码比没有码更糟", code)
		}
		for other, seen := range rendered {
			if seen == text {
				t.Fatalf("%s 与 %s 渲染成同一段话:\n%s\n"+
					"—— 它们的下一步不一样,压成一句就是把用户派去修错的东西", code, other, text)
			}
		}
		rendered[code] = text
	}
	if len(rendered) < 5 {
		t.Fatalf("只渲染了 %d 种结局 —— 锚点漂了,回来重判", len(rendered))
	}
}

// 「连不上」与「连得上但没握上」的措辞必须**相反**。
//
// 说反了就是把人派去修一台好机器。本仓库为此付过一次大代价:reality 一度
// 全挂,真因是默认 SNI www.microsoft.com 的证书过大,而当时先误归因成
// sing-box 同机问题、又误归因成网络 MITM。
func TestReachableAndUnreachableSayOppositeThings(t *testing.T) {
	facts := startFailureServers{CurrentName: "vps", CurrentHostPort: "195.133.192.92:443"}
	unreachable := coreStartFailureAdvice("core_"+supervisor.StartFailureTunnelUnreachable, facts)
	handshake := coreStartFailureAdvice("core_"+supervisor.StartFailureTunnelHandshakeFailed, facts)

	if !strings.Contains(handshake, "在应答") {
		t.Fatalf("「连得上但没握上」没说那台机器在应答 —— 那正是它与另一种的分界:\n%s", handshake)
	}
	if strings.Contains(unreachable, "在应答") {
		t.Fatalf("「连不上」那句话里出现了「在应答」:\n%s", unreachable)
	}
	// 反过来那一半:握手失败时**不许**建议去修/换那台机器。
	if strings.Contains(handshake, "挂了") {
		t.Fatalf("对一台正在应答的服务器说它可能挂了:\n%s", handshake)
	}
}

// **只说 bx 观测到什么,绝不断言那台服务器的状态。**
//
// 本机自己没网时同样拨不通(批一 ruling ② 留给批二的那条):一句「那台服务器
// 没有应答」在那种情况下是一句确凿的假话,而用户会照着它去重启一台好好的 VPS。
func TestTheWordingNeverAssertsWhatTheServerIsDoing(t *testing.T) {
	facts := startFailureServers{CurrentName: "vps", CurrentHostPort: "195.133.192.92:443"}
	text := coreStartFailureAdvice("core_"+supervisor.StartFailureTunnelUnreachable, facts)
	for _, forbidden := range []string{"服务器没有应答", "那台机器挂了", "服务器已经挂了", "服务器不在线"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("这句话断言了那台服务器的状态(%q)—— bx 只观测到「这次连不上」,\n"+
				"本机自己没网时同样连不上:\n%s", forbidden, text)
		}
	}
	if !strings.Contains(text, "bx 连不上") {
		t.Fatalf("这句话没有把它说成一次**尝试**的事实:\n%s", text)
	}
}

// 本机拨号失败那一档指着 **bx 自己的直连器**,不指着 VPS。
//
// 2026-08-13 那次真机事故的签名:DirectDialer 用 IP_BOUND_IF 绑物理网卡,而
// IP_BOUND_IF 只查 scoped 路由表 —— 那条 scoped 默认路由由 Hijack 装,比这次
// 判别拨号晚 572 行。那时去查 VPS 是白费力气。
func TestTheLocalDialOutcomeSendsTheUserToBxsOwnDialerNotTheVPS(t *testing.T) {
	facts := startFailureServers{CurrentName: "vps", CurrentHostPort: "195.133.192.92:443"}
	text := coreStartFailureAdvice("core_"+supervisor.StartFailureTunnelUndeterminedLocalDial, facts)
	if !strings.Contains(text, "-ifscope") {
		t.Fatalf("没有给出那条出路(route -n get -ifscope <网卡>):\n%s", text)
	}
	if strings.Contains(text, "nc -z 195.133.192.92") {
		t.Fatalf("把用户派去探那台 VPS —— SYN 根本没离开这台机器,那次探测什么也说明不了:\n%s", text)
	}
}

// 这一档**不许一边说「与那台服务器无关」、一边叫用户换一台**。
//
// 这个码盖着两种毛病,而它们的出路相反:bx 自己的直连器坏了(2026-08-13 的
// 签名,换服务器帮不上忙)、以及**这台机器解析不出那台服务器的主机名**
// (*net.DNSError 就在这一档里,换一台确实有用)。此前那句话把前一种当成
// 唯一的答案断言下来,而下面紧跟着「你还配了另一台 —— sudo bx server use」,
// 于是同一段话自己打自己 —— 在一条唯一目的就是「别再盯着 VPS 看」的话里。
//
// 判据:那句「换服务器帮不上忙」必须是**有条件的**(挂在那次路由检查的结果
// 上),而且另一种毛病必须被说出来。
func TestTheLocalDialAdviceDoesNotContradictItsOwnSwitchSuggestion(t *testing.T) {
	facts := startFailureServers{
		CurrentName: "vps", CurrentHostPort: "195.133.192.92:443",
		Others: []string{"tokyo(166.1.190.123)"},
	}
	text := coreStartFailureAdvice("core_"+supervisor.StartFailureTunnelUndeterminedLocalDial, facts)
	// 前置自检:那句「你还配了另一台」确实在,否则下面在测一段不存在的矛盾。
	if !strings.Contains(text, "你还配了另一台") {
		t.Fatalf("这一档没给「你还配了另一台」—— 这条断言测的那个矛盾不存在了,回来重判:\n%s", text)
	}
	if !strings.Contains(text, "答 `not in table` 的话") {
		t.Fatalf("「换服务器帮不上忙」不是挂在那次路由检查的结果上的 ——\n"+
			"它成了一句无条件断言,而同一段话下面就叫用户换一台:\n%s", text)
	}
	if !strings.Contains(text, "解析不出那台服务器的主机名") {
		t.Fatalf("没说出这一档里那种**换一台确实有用**的毛病(主机名解析不出来,\n"+
			"*net.DNSError 就在这一档),于是「你还配了另一台」读起来仍然是自相矛盾:\n%s", text)
	}
}

// 每一种「没判出来」都要说成「没判出来」。
//
// 三个码的处置不同(所以各有一句话),但**没有一个可以被读成「那台服务器没事」**。
// 穷举来自 supervisor.StartFailureCodes(),不是这里再抄一份。
func TestEveryUndeterminedOutcomeAdmitsItCouldNotTell(t *testing.T) {
	facts := startFailureServers{CurrentName: "vps", CurrentHostPort: "195.133.192.92:443"}
	checked := 0
	for _, code := range supervisor.StartFailureCodes() {
		if !supervisor.IsTunnelUndeterminedCode(code) {
			continue
		}
		checked++
		text := coreStartFailureAdvice("core_"+code, facts)
		if !strings.Contains(text, "没能判断") {
			t.Fatalf("%s 没有说出「bx 没能判断那台服务器还在不在」:\n%s", code, text)
		}
		if strings.Contains(text, "在应答") {
			t.Fatalf("%s 断言了那台服务器在应答:\n%s", code, text)
		}
	}
	if checked != 3 {
		t.Fatalf("只检查了 %d 个「没判出来」的码,want 3 —— 锚点漂了", checked)
	}
}

// 「你还配了另一台」**只在真有另一台时出现**,而且**绝不出现链接**。
//
// 链接是凭据(vless 的 uuid 就在里面)。同一条纪律在服务器清单那边由
// TestServerListNeverShipsTheLinkItself 守着。
func TestTheOtherServerLineOnlyAppearsWhenThereIsOne(t *testing.T) {
	alone := startFailureServers{CurrentName: "vps", CurrentHostPort: "195.133.192.92:443"}
	text := coreStartFailureAdvice("core_"+supervisor.StartFailureTunnelUnreachable, alone)
	if strings.Contains(text, "还配了另一台") {
		t.Fatalf("只有一台服务器却说「你还配了另一台」:\n%s", text)
	}
	if strings.Contains(text, "bx server use") {
		t.Fatalf("没有别的服务器可切,却给了一条切换命令:\n%s", text)
	}

	pair := alone
	pair.Others = []string{"tokyo(166.1.190.123)"}
	withOther := coreStartFailureAdvice("core_"+supervisor.StartFailureTunnelUnreachable, pair)
	if !strings.Contains(withOther, "tokyo") || !strings.Contains(withOther, "bx server use") {
		t.Fatalf("有另一台却没点名、也没给切换命令:\n%s", withOther)
	}
}

func TestNoRenderedAdviceEverCarriesALink(t *testing.T) {
	const link = "vless://11111111-2222-3333-4444-555555555555@198.51.100.7:443"
	facts := startFailureServers{
		CurrentName:     "vps",
		CurrentHostPort: "195.133.192.92:443",
		Others:          []string{"tokyo(166.1.190.123)"},
	}
	for _, code := range coreStartFailureCodes() {
		text := coreStartFailureAdvice(code, facts)
		for _, secret := range []string{"vless://", "bx://", "hysteria2://", link} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s 的那段话里出现了链接片段 %q —— 链接就是凭据:\n%s", code, secret, text)
			}
		}
	}
}

// 认不出的码一个字都不许编。
func TestAnUnknownCodeGetsNoInventedAdvice(t *testing.T) {
	facts := startFailureServers{CurrentName: "vps", CurrentHostPort: "195.133.192.92:443"}
	for _, code := range []string{"", "core_ownership_uncertain", "guardian_busy", "core_我是新来的"} {
		if got := coreStartFailureAdvice(code, facts); got != "" {
			t.Fatalf("对 %q 编了一句话:\n%s", code, got)
		}
	}
}

// 问不出服务器地址时,那句话仍然成立 —— 只是不点名。
//
// **不许伪造一个占位主机**:一句指着 `<unknown>:0` 的排查命令比不给更糟。
func TestAdviceStillWorksWhenTheServerCannotBeNamed(t *testing.T) {
	text := coreStartFailureAdvice("core_"+supervisor.StartFailureTunnelUnreachable, startFailureServers{})
	if strings.TrimSpace(text) == "" {
		t.Fatal("读不到配置就一个字都不说了")
	}
	for _, forbidden := range []string{":0", "<", "unknown"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("编了一个占位地址(%q):\n%s", forbidden, text)
		}
	}
}

// 事实从配置里来,而**当前那台与其余那几台分得开**。
func TestServerFactsComeFromTheConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	const doc = `servers:
  - name: vps
    link: "vless://11111111-2222-3333-4444-555555555555@195.133.192.92:443?security=reality&sni=www.cloudflare.com&fp=chrome&pbk=aaa"
  - name: tokyo
    link: "vless://22222222-2222-3333-4444-555555555555@166.1.190.123:8443?security=reality&sni=www.cloudflare.com&fp=chrome&pbk=bbb"
current: vps
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	facts := readStartFailureServers(path)
	if facts.CurrentName != "vps" || facts.CurrentHostPort != "195.133.192.92:443" {
		t.Fatalf("当前那台解错了:%+v", facts)
	}
	if len(facts.Others) != 1 || !strings.Contains(facts.Others[0], "tokyo") || !strings.Contains(facts.Others[0], "166.1.190.123") {
		t.Fatalf("另一台没被点名:%+v", facts)
	}
	// 事实里也不许夹带链接:它会被拼进给用户看的那段话。
	joined := facts.CurrentName + facts.CurrentHostPort + strings.Join(facts.Others, " ")
	if strings.Contains(joined, "vless://") || strings.Contains(joined, "11111111-2222-3333-4444-555555555555") {
		t.Fatalf("事实里夹带了链接/凭据:%+v", facts)
	}
}

// 读不到配置(不存在、坏了、非 root)⇒ 空事实,不报错、不猜。
func TestUnreadableConfigYieldsNoFactsRatherThanAGuess(t *testing.T) {
	facts := readStartFailureServers(filepath.Join(t.TempDir(), "没有这个文件"))
	if facts.CurrentHostPort != "" || facts.CurrentName != "" || len(facts.Others) != 0 {
		t.Fatalf("读不到配置却给出了事实:%+v", facts)
	}
}

// 接线:`bx up` 那条路真的把这段话接了上去,而且**取的是应答里的码**,
// 不是从消息文本里抠出来的字符串。
//
// 判据打在**渲染出来的错误**上:少了这一跳,用户拿到的还是那句
// 「Guardian /v1/up returned 500 …(code=core_tunnel_unreachable)」,
// 而 code= 后面那个词对他毫无意义。
func TestUpAnnotatesAGuardianStartFailureWithTheActionableSentence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	const doc = `servers:
  - name: vps
    link: "vless://11111111-2222-3333-4444-555555555555@195.133.192.92:443?security=reality&sni=www.cloudflare.com&fp=chrome&pbk=aaa"
  - name: tokyo
    link: "vless://22222222-2222-3333-4444-555555555555@166.1.190.123:8443?security=reality&sni=www.cloudflare.com&fp=chrome&pbk=bbb"
current: vps
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	original := &guardian.HTTPError{
		Message: "Guardian /v1/up returned 500(code=core_tunnel_unreachable)",
		Code:    "core_" + supervisor.StartFailureTunnelUnreachable,
	}
	annotated := annotateCoreStartFailure(original, path)
	text := annotated.Error()
	if !strings.Contains(text, "195.133.192.92:443") {
		t.Fatalf("那句话里没有出现服务器地址:\n%s", text)
	}
	if !strings.Contains(text, "tokyo") {
		t.Fatalf("没有点名另一台可用的服务器:\n%s", text)
	}
	if !strings.Contains(text, original.Message) {
		t.Fatalf("原始错误被吞掉了 —— 码与 Guardian 日志的指引都在里面:\n%s", text)
	}
	var httpErr *guardian.HTTPError
	if !errors.As(annotated, &httpErr) {
		t.Fatal("包装之后 errors.As 认不出 *guardian.HTTPError —— 别的判定还挂在它上面")
	}

	// 与它无关的失败原样返回,一个字不加。
	unrelated := fmt.Errorf("别的失败")
	if got := annotateCoreStartFailure(unrelated, path); got != unrelated { //nolint:errorlint // 要的就是同一个值
		t.Fatalf("给一个不相干的失败加了话:%v", got)
	}
	if got := annotateCoreStartFailure(nil, path); got != nil {
		t.Fatalf("给 nil 加了话:%v", got)
	}
}

// macOSUpAction 真的走了那一跳。
//
// 判据是 AST:失败返回的那个值必须经过 annotateCoreStartFailure —— 而不是
// 「这个文件里提到过它」。少了这条,把接线删掉之后上面那条纯函数测试照样
// 全绿,而用户拿到的仍是那句只有码的话(本仓库反复罚过的形状)。
func TestMacOSUpActionRoutesTheGuardianFailureThroughTheAdvice(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "guardian.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 guardian.go:%v", err)
	}
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "macOSUpAction" && d.Recv == nil {
			fn = d
		}
	}
	if fn == nil {
		t.Fatal("guardian.go 里找不到 macOSUpAction —— 锚点漂了,回来重判,别静默放行")
	}
	annotated := false
	ast.Inspect(fn, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, result := range ret.Results {
			call, ok := result.(*ast.CallExpr)
			if !ok {
				continue
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "annotateCoreStartFailure" {
				annotated = true
			}
		}
		return true
	})
	if !annotated {
		t.Fatal("macOSUpAction 没有把 Guardian 的失败经 annotateCoreStartFailure **返回出去** ——\n" +
			"用户拿到的还是那句只有 code= 的话,而那个词对他毫无意义")
	}
}

// **每一种结局都要给出「完整原因在哪儿」。**
//
// 应答体只带一个码,而真正的那句话(事故那次是 `dial tcp <server>:443:
// i/o timeout`)只在 root-only 的 Core 日志里 —— 这条指引是两者之间唯一的桥。
// `tunnel_unreachable` 此前是唯一没有它的一种,而**它恰恰就是事故那一种**。
func TestEveryOutcomeSaysWhereTheFullReasonIs(t *testing.T) {
	facts := startFailureServers{CurrentName: "vps", CurrentHostPort: "195.133.192.92:443"}
	for _, code := range coreStartFailureCodes() {
		text := coreStartFailureAdvice(code, facts)
		if !strings.Contains(text, coreLogPathForAdvice()) {
			t.Errorf("%s 没告诉用户完整原因在哪儿:\n%s\n"+
				"—— 应答体只有一个码,那句真正的原因只在 root-only 的 Core 日志里", code, text)
		}
	}
}

// 用户可见的那几行里不许出现 markdown 的 `**`。
//
// 终端与 NSAlert 都不渲染它,用户读到的是字面上的星号。三条曾经就这么发出去
// 过(4fbf829 才清掉),而这个文件的注释里 `**` 满天飞 —— 下一个人从注释里
// 顺手抄一句进字符串是最自然的动作,而没有任何东西拦着。
func TestNoRenderedAdviceCarriesMarkdown(t *testing.T) {
	for _, facts := range []startFailureServers{
		{CurrentName: "vps", CurrentHostPort: "195.133.192.92:443", Others: []string{"tokyo(166.1.190.123)"}},
		{},
	} {
		for _, code := range coreStartFailureCodes() {
			text := coreStartFailureAdvice(code, facts)
			if strings.Contains(text, "**") {
				t.Errorf("%s 渲染出了 markdown 的 `**`,终端不认它、用户读到的是星号:\n%s", code, text)
			}
		}
	}
}

// 端口解不出来时,那条 `nc -z` 整条不给 —— 不许渲染出一个尾巴上空着的端口。
//
// joinHostPortForAdvice 在端口 <= 0 时只写主机(那是对的:绝不编一个 `:0`),
// 于是 portOf 返回空串,而那条指引此前拼成 `nc -z 195.133.192.92 ` ——
// 一条粘贴过去就报错的命令,出现在一条唯一目的就是「照着做」的话里。
func TestNoNCCommandIsRenderedWithAnEmptyPort(t *testing.T) {
	// 前置自检:这确实是「解得出主机、解不出端口」那一种形状。
	facts := startFailureServers{CurrentName: "vps", CurrentHostPort: joinHostPortForAdvice("195.133.192.92", 0)}
	if facts.CurrentHostPort != "195.133.192.92" {
		t.Fatalf("台子造出来的不是「只有主机」那一种:%q", facts.CurrentHostPort)
	}
	// 判据是「端口未知时**一条 nc 命令都不许出现**」,不是「不许出现某个具体
	// 的坏拼法」—— 后者会放过 `nc -z <别的主机> `,而那是同一个缺陷换了个地址。
	for _, code := range coreStartFailureCodes() {
		text := coreStartFailureAdvice(code, facts)
		for _, line := range strings.Split(text, "\n") {
			if !strings.Contains(line, "nc -z") {
				continue
			}
			t.Errorf("%s 在端口未知时仍然给了一条 nc 命令:%q —— 它渲染成的是\n"+
				"`nc -z <主机> `,尾巴上一个空端口,粘贴过去就报错", code, strings.TrimSpace(line))
		}
	}
	// 反面:端口问得出来时那条命令仍然要给,否则「一律不给」也能满足上面。
	withPort := startFailureServers{CurrentHostPort: joinHostPortForAdvice("195.133.192.92", 443)}
	if !strings.Contains(coreStartFailureAdvice(
		coreStartFailureCodePrefix+supervisor.StartFailureTunnelUnreachable, withPort),
		"nc -z 195.133.192.92 443") {
		t.Error("端口问得出来时反而不给 nc 命令了 —— 上面那条断言于是靠「一律不给」平凡成立")
	}
}

// **隧道那一族的每一句话,在 bx 知道地址时都必须点名那台服务器。**
//
// 这条是 I1 逼出来的:`local_dial` 从前是这一族里唯一一句连 host:port 都没有
// 的话,而 2026-08-13 那种机器上(scoped 表里根本没有默认路由)最容易落进它
// 的恰恰是「VPS 真的挂了」那一次 —— 用户读到的是一句既不说哪台机器、又先派
// 他去查 bx 自己路由的话,spec §8 的真机验收因此复现不出它要验的那个场景。
//
// 判据做成**穷举整族**而不是单点:族由 supervisor 那张表派生,将来加一档
// 「没判出来」的新来由时它自动进范围,漏给地址当场转红。
func TestEveryTunnelOutcomeNamesTheServerWhenBxKnowsIt(t *testing.T) {
	const host = "195.133.192.92"
	facts := startFailureServers{CurrentName: "vps", CurrentHostPort: host + ":443"}
	family := 0
	for _, bare := range supervisor.StartFailureCodes() {
		isTunnel := bare == supervisor.StartFailureTunnelUnreachable ||
			bare == supervisor.StartFailureTunnelHandshakeFailed ||
			supervisor.IsTunnelUndeterminedCode(bare)
		if !isTunnel {
			continue
		}
		family++
		text := coreStartFailureAdvice(coreStartFailureCodePrefix+bare, facts)
		if !strings.Contains(text, host) {
			t.Errorf("%s 那句话里没有那台服务器的地址 —— bx 明明知道它:\n%s", bare, text)
		}
	}
	if family < 5 {
		t.Fatalf("只走到 %d 档隧道结局(want ≥5)—— 族的判据认不出现在的码了", family)
	}
}
