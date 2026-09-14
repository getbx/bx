package cli

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/leakserve"
)

// **`sudo bx leakcheck` 必须被拒绝**,不是「照跑但更强大」。
//
// 以 root 跑会让那个 loopback HTTP 服务变成 root 进程的端口,把「一个隐私工具
// 不该为了做体检而让 root 进程对浏览器开 HTTP」这条理由一笔勾销;而且答案不会
// 更准 —— 需要的本机事实一个都不需要 root。
//
// 判据抽成吃 euid 的纯函数,好让这条测试在任何身份下都跑得起来(CI 里跑测试的
// 不是 root,直接判 os.Geteuid() 的话这条测试在开发机上恒为「没走到那一支」)。
func TestLeakCheckRefusesRoot(t *testing.T) {
	err := guardLeakCheckPrivileges(0)
	if err == nil {
		t.Fatal("以 root 跑 bx leakcheck 必须被拒绝")
	}
	msg := err.Error()
	// 必须告诉用户**换成不带 sudo 再跑**,别让他以为是权限不够 —— 后者会让他
	// 去想办法「提更高的权」,正好走反方向。
	if !strings.Contains(msg, "sudo") {
		t.Errorf("拒绝信息必须提到 sudo,得到 %q", msg)
	}
	for _, want := range []string{"without", "bx leakcheck"} {
		if !strings.Contains(msg, want) {
			t.Errorf("拒绝信息必须指出重新以普通用户身份运行(缺 %q):%q", want, msg)
		}
	}
	if strings.Contains(strings.ToLower(msg), "permission denied") ||
		strings.Contains(strings.ToLower(msg), "requires root") {
		t.Errorf("拒绝信息不得读起来像「权限不够」:%q", msg)
	}
	if err := guardLeakCheckPrivileges(501); err != nil {
		t.Fatalf("普通用户身份必须放行,得到 %v", err)
	}
}

// **闸门装了还得接上。**
//
// 上面那条打在纯函数上,把 leakcheckAction 里那次调用删掉它照样全绿 —— 这个仓库
// 反复栽在这里:证明判定存在 ≠ 证明它被调用了。
//
// 判据走 AST 而不是文本匹配(本仓库的文本守卫被绕过过八次):它认的是**对
// guardLeakCheckPrivileges 的调用**这件事本身,换个空白、拆行、加注释都绕不过去。
// 而且要求它是函数体的**第一句** —— 拒绝必须发生在起 loopback 服务、采集事实、
// 开浏览器之前,排在后面等于以 root 干完了全部实际动作才想起来拒绝。
func TestLeakCheckActionCallsThePrivilegeGuardFirst(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join("leakcheck.go"), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 leakcheck.go 失败(本守卫读不懂现在的代码,请连同它一起重写):%v", err)
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "leakcheckAction" && fn.Body != nil {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("在 leakcheck.go 里找不到 leakcheckAction —— 本守卫读不懂现在的代码,请连同它一起重写")
	}
	if len(body.List) == 0 {
		t.Fatal("leakcheckAction 是空的")
	}
	if !callsGuard(body.List[0]) {
		t.Fatalf("leakcheckAction 的第一句不是 guardLeakCheckPrivileges(...):拒绝必须发生在"+
			"起 loopback 服务、采集事实、开浏览器**之前**,得到 %T", body.List[0])
	}
	// 实参必须是**这个进程真实的 euid**,不是一个常量。写死 501 会让闸门在
	// 每一种身份下都放行,而上面那条纯函数测试照样绿。
	if !strings.Contains(guardCallArgs(body.List[0]), "os.Geteuid()") {
		t.Errorf("guardLeakCheckPrivileges 的实参必须是 os.Geteuid(),得到 %q —— "+
			"喂一个常量进去,闸门就只是个摆设", guardCallArgs(body.List[0]))
	}
}

func callsGuard(stmt ast.Stmt) bool { return strings.Contains(guardCallArgs(stmt), "@found") }

// guardCallArgs 在一条语句里找对 guardLeakCheckPrivileges 的调用,返回
// "@found" + 实参的源码形状;没找到返回空串。
func guardCallArgs(stmt ast.Stmt) string {
	found := ""
	ast.Inspect(stmt, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || ident.Name != "guardLeakCheckPrivileges" {
			return true
		}
		args := make([]string, 0, len(call.Args))
		for _, arg := range call.Args {
			args = append(args, exprText(arg))
		}
		found = "@found" + strings.Join(args, ",")
		return false
	})
	return found
}

func exprText(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.CallExpr:
		// **实参要渲染出来。** 此前这里折成 `f()`,于是 `c.Bool("no-reach")` 与
		// `c.Bool("json")` 在守卫眼里一模一样 —— 一条钉「读的是哪个 flag」的断言
		// 就此平凡成立。
		args := make([]string, 0, len(e.Args))
		for _, a := range e.Args {
			args = append(args, exprText(a))
		}
		return exprText(e.Fun) + "(" + strings.Join(args, ",") + ")"
	case *ast.SelectorExpr:
		return exprText(e.X) + "." + e.Sel.Name
	case *ast.Ident:
		return e.Name
	case *ast.BasicLit:
		return e.Value
	default:
		return "?"
	}
}

// 命令必须注册,而且与既有的 leak-check 并存、用途可区分。
func TestLeakCheckCommandIsRegisteredAlongsideLeakCheck(t *testing.T) {
	app := New()
	if !appHasCommand(app, "leakcheck") {
		t.Fatal("app 必须暴露 bx leakcheck")
	}
	if !appHasCommand(app, "leak-check") {
		t.Fatal("既有的 bx leak-check 不许被这一期删掉:MCP 只读工具依赖它")
	}
	newCmd := findAppCommand(app, "leakcheck")
	oldCmd := findAppCommand(app, "leak-check")
	if newCmd.Usage == oldCmd.Usage {
		t.Fatal("两条命令只差一个连字符,Usage 必须能让用户分辨它们")
	}
	if !strings.Contains(newCmd.Usage, "浏览器") {
		t.Errorf("bx leakcheck 的 Usage 应点明它开浏览器页面,得到 %q", newCmd.Usage)
	}
}

// 渲染:三态必须逐字出现,not checked 不许被写成 ok,异常数要说出来。
func TestRenderLeakCheckReport(t *testing.T) {
	rep := leakcheck.Judge(time.Unix(0, 0).UTC(),
		leakcheck.BrowserReport{ExitV4: "5.6.7.8", SRFLX: []string{"1.2.3.4"}},
		leakcheck.LocalFacts{})
	lines := renderLeakCheckReport(rep)
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "bad") {
		t.Errorf("渲染必须写出 bad:\n%s", out)
	}
	if !strings.Contains(out, "not checked") {
		t.Errorf("渲染必须写出 not checked:\n%s", out)
	}

	// **每条结论都要带着它自己的三态**,不是「整篇里出现过这几个词」。
	//
	// 这一段是变异验证逼出来的:把渲染改成「只在 bad 时打印 verdict」,上面两条
	// 断言**照样全绿** —— 因为末尾那句「N finding(s) marked bad, M not checked.」
	// 里两个词都在。而那次改动的实际后果正是 not checked 从每条结论上消失,
	// 读起来就是「一切正常」。
	if len(rep.Findings) < 2 {
		t.Fatalf("这份 fixture 应该同时有 bad 与 not checked 两种结论,得到 %d 条", len(rep.Findings))
	}
	sawNotChecked := false
	for _, f := range rep.Findings {
		if !lineCarriesVerdict(lines, f.Verdict.String(), f.Title) {
			t.Errorf("结论 %q 的那一行没带上它的三态 %q:\n%s", f.Title, f.Verdict, out)
		}
		if f.Verdict == leakcheck.NotChecked {
			sawNotChecked = true
		}
	}
	if !sawNotChecked {
		t.Fatal("这份 fixture 里应当有 not checked 的结论,否则上面那条断言什么都没证明")
	}
	for _, want := range []string{leakcheck.EchoV4URL, leakcheck.STUNURL} {
		if !strings.Contains(out, want) {
			t.Errorf("渲染必须列出联系过的第三方 %q:\n%s", want, out)
		}
	}
}

// **一份全 not checked 的报告不许渲染成一句好听的总结。**
func TestRenderBlindReportDoesNotClaimHealth(t *testing.T) {
	rep := leakcheck.Judge(time.Unix(0, 0).UTC(), leakcheck.BrowserReport{}, leakcheck.LocalFacts{})
	out := strings.ToLower(strings.Join(renderLeakCheckReport(rep), "\n"))
	for _, forbidden := range []string{"no leaks", "all good", "everything is fine", "一切正常"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("全 not checked 的报告不得渲染出 %q:\n%s", forbidden, out)
		}
	}
	if !strings.Contains(out, "not checked") {
		t.Errorf("全 not checked 的报告必须如实说明:\n%s", out)
	}
}

// lineCarriesVerdict 找一行同时带着这条结论的标题与它的三态。
func lineCarriesVerdict(lines []string, verdict, title string) bool {
	for _, line := range lines {
		if strings.Contains(line, title) && strings.Contains(line, verdict) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 第四段(SectionReach)的渲染与接线
// ---------------------------------------------------------------------------

// **可达性结论不许被画在「流量去哪儿」那个标题底下。**
//
// 分段标题此前是 `switch { case identity … case surface … default: 流量路径 }`,
// 而 SectionReach 落进 default —— 于是「bx 连不上 claude.ai」出现在一个通篇在
// 讲「你泄漏了没有」的标题下面,用户会把「连不上」读成「流量泄漏」(spec §6.1
// 要避免的正是这件事,只是它发生在渲染层)。
//
// 判据打在**用户看得见的东西**上:那一行标题在屏幕上必须与流量路径那条不同。
func TestReachConclusionsAreNotFiledUnderTheTrafficPathHeading(t *testing.T) {
	rep := leakcheck.Report{Findings: []leakcheck.Finding{
		{ID: "carrier", Title: "Who carries your traffic", Section: leakcheck.SectionPath, Verdict: leakcheck.OK},
		{
			ID: "reach_anthropic_api", Title: "Anthropic API", Section: leakcheck.SectionReach,
			Verdict: leakcheck.OK, Reach: leakcheck.ReachReachable,
		},
	}}
	lines := renderLeakCheckReport(rep)
	pathHead := headingAbove(lines, "Who carries your traffic")
	reachHead := headingAbove(lines, "Anthropic API")
	if pathHead == "" || reachHead == "" {
		t.Fatalf("找不到分段标题(path=%q reach=%q):\n%s", pathHead, reachHead, strings.Join(lines, "\n"))
	}
	if reachHead == pathHead {
		t.Fatalf("可达性结论被画在流量路径那个标题底下(%q)—— 用户会把「连不上」"+
			"读成「流量泄漏」:\n%s", reachHead, strings.Join(lines, "\n"))
	}
	// 它也不许反过来借用一句读着像安全问题的话。
	if strings.Contains(strings.ToUpper(reachHead), "TRAFFIC GOES") {
		t.Fatalf("第四段的标题读起来仍像流量泄漏:%q", reachHead)
	}
}

// 上面那条只钉住今天这一段。**下一段加进来时没有任何东西会替它问同样的问题** ——
// 而 default 兜底吞掉新分段在这一支里已经是第三次(NewReport 的 else、
// notChecked 那个循环、这个 switch)。故按 Outline() 现有的分段穷举:每一段都
// 必须拿到**属于自己**的标题,两段共用一句就是其中一段在冒充另一段的责任人。
func TestEverySectionOutlineEmitsGetsItsOwnHeading(t *testing.T) {
	seen := map[leakcheck.Section]bool{}
	sections := []leakcheck.Section{}
	for _, o := range leakcheck.Outline() {
		if !seen[o.Section] {
			seen[o.Section] = true
			sections = append(sections, o.Section)
		}
	}
	if len(sections) < 4 {
		t.Fatalf("Outline() 只产出 %d 个分段 —— 本守卫读不懂现在的代码,请连同它一起重写", len(sections))
	}
	byHeading := map[string]leakcheck.Section{}
	for _, sec := range sections {
		title := "row-" + sec.String()
		lines := renderLeakCheckReport(leakcheck.Report{Findings: []leakcheck.Finding{
			{ID: "x", Title: title, Section: sec, Verdict: leakcheck.OK},
		}})
		head := headingAbove(lines, title)
		if head == "" {
			t.Fatalf("分段 %q 渲染不出标题:\n%s", sec, strings.Join(lines, "\n"))
		}
		if other, dup := byHeading[head]; dup {
			t.Fatalf("分段 %q 与 %q 共用同一个标题 %q —— 其中一段在冒充另一段的责任人",
				sec, other, head)
		}
		byHeading[head] = sec
	}
}

// **现有的 notChecked 数的是所有 Findings 里 Verdict==NotChecked 的**,而第四段
// 的 Undetermined 与 Challenged 两态都映射成 NotChecked ⇒ 它们会被算进**泄漏
// 检测**那一格。这是 Task 1 在 NewReport 里堵过的同一种静默合并,在渲染层又出现
// 一次;CLAUDE.md 记的真机基线「6 not checked」会静默变成 10。
func TestReachNotCheckedDoesNotInflateTheLeakNotCheckedCount(t *testing.T) {
	rep := leakcheck.Report{
		Findings: []leakcheck.Finding{
			{ID: "p", Title: "path row", Section: leakcheck.SectionPath, Verdict: leakcheck.OK},
			{
				ID: "reach_a", Title: "reach a", Section: leakcheck.SectionReach,
				Verdict: leakcheck.NotChecked, Reach: leakcheck.ReachUndetermined,
			},
			{
				ID: "reach_b", Title: "reach b", Section: leakcheck.SectionReach,
				Verdict: leakcheck.NotChecked, Reach: leakcheck.ReachChallenged,
			},
		},
		Reach: leakcheck.ReachSummary{Undetermined: 1, Challenged: 1},
	}
	out := strings.Join(renderLeakCheckReport(rep), "\n")
	if !strings.Contains(out, "0 not checked.") {
		t.Fatalf("泄漏检测那一格应是 0(没有一条 path/identity 结论没查成),"+
			"而第四段那两条被算了进去:\n%s", out)
	}
	// 同时它们不许就此消失:第四段自己那行要说出来。
	if !strings.Contains(out, "1 challenged") || !strings.Contains(out, "1 undetermined") {
		t.Fatalf("第四段那两条既没进泄漏那一格、也没在自己那行出现 —— 它们被吞了:\n%s", out)
	}
}

// 五态并排,而且 **undetermined / unreachable / challenged 为零也要打印**:
// 否则「一条都没查出来」与「查了、全可达」在屏幕上长得一样(spec §6.2)。
func TestLeakCheckSummaryPrintsReachCountsSeparately(t *testing.T) {
	allReachable := strings.Join(renderLeakCheckReport(leakcheck.Report{
		Reach: leakcheck.ReachSummary{Reachable: 4},
	}), "\n")
	for _, want := range []string{"4 reachable", "0 refused", "0 unreachable", "0 challenged", "0 undetermined"} {
		if !strings.Contains(allReachable, want) {
			t.Fatalf("摘要缺 %q —— 为零的档也必须打印:\n%s", want, allReachable)
		}
	}
	// **决定性的一条**:「四条全可达」与「四条一条都没问出来」在屏幕上必须不同。
	noneChecked := strings.Join(renderLeakCheckReport(leakcheck.Report{
		Reach: leakcheck.ReachSummary{Undetermined: 4},
	}), "\n")
	if noneChecked == allReachable {
		t.Fatalf("「全可达」与「一条都没查出来」渲染出同一份输出:\n%s", allReachable)
	}
	// 而且它绝不能与泄漏那一格合成一句话(合起来之后「0 not checked」到底在说
	// 哪一段就再也表达不出来了)。
	if strings.Contains(allReachable, "identifying trait(s), 4 reachable") {
		t.Fatalf("第四段被并进了泄漏那句话里:\n%s", allReachable)
	}
}

// headingAbove 返回屏幕上离这条结论最近的那个分段标题(全大写、顶格的那种)。
func headingAbove(lines []string, title string) string {
	idx := -1
	for i, l := range lines {
		if strings.Contains(l, title) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ""
	}
	for i := idx; i >= 0; i-- {
		if isSectionHeadingLine(lines[i]) {
			return lines[i]
		}
	}
	return ""
}

func isSectionHeadingLine(line string) bool {
	if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "[") {
		return false
	}
	return line == strings.ToUpper(line) && strings.ContainsAny(line, "ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}

// **判据造好了不等于功能通电。** ProbeReach 在这一支里一路做到 Task 5 都还是
// 零生产调用方:LocalFacts.ReachProbes 从来没被填过,于是四条可达性结论在真机上
// 恒为「这一轮没有检查」,而两个包的测试全绿。
//
// 判据打在**到达 LocalFacts 的那个值**上,不打在「调用发生过」(第七种失效写法):
// 探测记录必须一个目标一条、标着 current 那条路径。
func TestCollectLeakCheckFactsCarriesTheReachProbes(t *testing.T) {
	dialed := 0
	reach := leakserve.ReachDeps{
		CurrentDial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed++
			return nil, errors.New("这条测试不联网")
		},
	}
	facts := collectLeakCheckFacts(context.Background(), leakserve.FactDeps{}, reach)
	targets := leakcheck.ReachTargets()
	if len(facts.ReachProbes) != len(targets) {
		t.Fatalf("ReachProbes 有 %d 条,要 %d 条 —— 探测结果没有到达 LocalFacts,"+
			"第四段在真机上会恒为「这一轮没有检查」", len(facts.ReachProbes), len(targets))
	}
	if dialed == 0 {
		t.Fatal("一次都没拨号 —— 记录是凭空造出来的,不是探出来的")
	}
	for i, tgt := range targets {
		got := facts.ReachProbes[i]
		if got.TargetID != tgt.ID {
			t.Errorf("第 %d 条记的是 %q,要 %q", i, got.TargetID, tgt.ID)
		}
		if got.Path != leakcheck.ReachPathCurrent {
			t.Errorf("第 %d 条的路径是 %q,要 %q(常量,不是手抄的字面量)",
				i, got.Path, leakcheck.ReachPathCurrent)
		}
	}
}

// 接线那一半:生产那条路必须真的走 collectLeakCheckFacts,而且喂给它的是
// **生产的**两份 deps。喂一个零值 ReachDeps 进去,上面那条行为测试照样全绿,
// 而真机上一个探测都不会发(CollectReach 对两个拨号器全 nil 返回 nil)。
func TestLeakCheckActionCollectsFactsThroughTheReachWiring(t *testing.T) {
	body := leakCheckActionBody(t)
	args := callArgsInBody(body, "collectLeakCheckFacts")
	if args == "" {
		t.Fatal("leakcheckAction 没有调用 collectLeakCheckFacts —— 可达性探测没有接上," +
			"第四段在真机上是死的")
	}
	if !strings.Contains(args, "leakserve.LiveFactDeps()") {
		t.Errorf("collectLeakCheckFacts 的实参里没有 leakserve.LiveFactDeps()(得到 %q)", args)
	}
	// 第三个实参不是字面量,而是一个从 reachDepsFor(...) 绑出来的名字 ——
	// 喂一个 leakserve.ReachDeps{} 字面量进去,真机上一个探测都不会发,而行为
	// 那半的测试(它直接调 collectLeakCheckFacts)照样全绿。
	depsName := assignedNameOfCall(body, "reachDepsFor")
	if depsName == "" {
		t.Fatal("leakcheckAction 没有经 reachDepsFor 取拨号器 —— --no-reach 那道开关没有接上")
	}
	if !strings.Contains(args, depsName) {
		t.Fatalf("collectLeakCheckFacts 拿到的不是 reachDepsFor 的结果(%q 不在 %q 里)",
			depsName, args)
	}
	// 而 reachDepsFor 吃的必须是**真实的 flag**,不是一个常量:写死 false 的话
	// --no-reach 就只是个摆设,而每一条测试照样绿。
	if flagArgs := callArgsInBody(body, "reachDepsFor"); !strings.Contains(flagArgs, `c.Bool("no-reach")`) {
		t.Errorf("reachDepsFor 的实参不是 c.Bool(\"no-reach\")(得到 %q)—— 喂一个常量进去,"+
			"那道开关就只是个摆设", flagArgs)
	}
	// 披露与探测必须由**同一个值**驱动:分成两个判据,两者漂开的方向分别是
	// 「探了没说」与「说了没探」,都是假话。
	if announceArgs := callArgsInBody(body, "announceReachTargets"); !strings.Contains(announceArgs, depsName) {
		t.Fatalf("披露读的不是探测那份 deps(%q 不在 %q 里)", depsName, announceArgs)
	}
	// 结果必须真的成为喂给 Judge 的那份事实,不是算完就丢。
	lhs := assignedNameOfCall(body, "collectLeakCheckFacts")
	if lhs == "" {
		t.Fatal("collectLeakCheckFacts 的结果没有被赋给任何变量 —— 算完就丢")
	}
	judgeArgs := callArgsInBody(body, "leakcheck.Judge")
	if !strings.Contains(judgeArgs, lhs) {
		t.Fatalf("leakcheck.Judge 拿到的不是 collectLeakCheckFacts 的结果(%q 不在 %q 里)",
			lhs, judgeArgs)
	}
}

// **联网之前先说要联系谁。** 这条契约此前整个由页面兑现,而可达性探测是 bx 自己
// 发的、页面一个字节都不经手 —— 于是这句话只能由 CLI 说,且必须排在第一个请求
// **之前**。判据同时钉住顺序:披露那一句在源码里要出现在采集那一句前面。
func TestLeakCheckAnnouncesTheReachTargetsBeforeContactingThem(t *testing.T) {
	body := leakCheckActionBody(t)
	announce, collect := -1, -1
	for i, stmt := range body.List {
		if announce < 0 && callArgsInBody(&ast.BlockStmt{List: []ast.Stmt{stmt}}, "announceReachTargets") != "" {
			announce = i
		}
		if collect < 0 && callArgsInBody(&ast.BlockStmt{List: []ast.Stmt{stmt}}, "collectLeakCheckFacts") != "" {
			collect = i
		}
	}
	if announce < 0 {
		t.Fatal("leakcheckAction 没有披露可达性探测目标 —— bx 在没打招呼的情况下联系了" +
			"Anthropic / OpenAI / Google")
	}
	if collect < 0 {
		t.Fatal("找不到 collectLeakCheckFacts —— 本守卫读不懂现在的代码,请连同它一起重写")
	}
	if announce > collect {
		t.Fatal("披露排在探测之后 —— 事后补一句不是「联网之前先说」")
	}
	// 清单必须现取,不许手抄第二份:少报一个第三方不是排版问题。
	out := captureStdout(t, func() { announceReachTargets(leakserve.LiveReachDeps(), false) })
	for _, tgt := range leakcheck.ReachTargets() {
		if !strings.Contains(out, tgt.URL) {
			t.Errorf("披露里没有 %q:\n%s", tgt.URL, out)
		}
	}
	// --json 那一份 stdout 必须干净:它是机器读的。
	if got := captureStdout(t, func() { announceReachTargets(leakserve.LiveReachDeps(), true) }); got != "" {
		t.Errorf("--json 模式往 stdout 写了东西,会把那份 JSON 弄脏:%q", got)
	}
}

func leakCheckActionBody(t *testing.T) *ast.BlockStmt {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "leakcheck.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 leakcheck.go 失败(本守卫读不懂现在的代码,请连同它一起重写):%v", err)
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "leakcheckAction" && fn.Body != nil {
			return fn.Body
		}
	}
	t.Fatal("找不到 leakcheckAction —— 本守卫读不懂现在的代码,请连同它一起重写")
	return nil
}

// callArgsInBody 在一段函数体里找对 name 的调用,返回它实参的源码形状;
// 没找到返回空串。name 可以带包名(如 "leakcheck.Judge")。
func callArgsInBody(body *ast.BlockStmt, name string) string {
	found := ""
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found != "" {
			return found == ""
		}
		if exprText(call.Fun) != name {
			return true
		}
		args := make([]string, 0, len(call.Args))
		for _, arg := range call.Args {
			args = append(args, exprText(arg))
		}
		found = "(" + strings.Join(args, ",") + ")"
		return false
	})
	return found
}

// assignedNameOfCall 返回 `x := name(...)` 里那个 x;不是赋值语句就返回空串。
func assignedNameOfCall(body *ast.BlockStmt, name string) string {
	got := ""
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || got != "" {
			return got == ""
		}
		for _, rhs := range assign.Rhs {
			call, ok := rhs.(*ast.CallExpr)
			if !ok || exprText(call.Fun) != name {
				continue
			}
			if len(assign.Lhs) == 1 {
				if id, ok := assign.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
					got = id.Name
				}
			}
		}
		return got == ""
	})
	return got
}

// **用户可见的那几行里不许出现 markdown 的 `**`。**
//
// 与 corestartadvice_test.go 的 TestNoRenderedAdviceCarriesMarkdown 同一条,
// 而那一条只罩 coreStartFailureAdvice —— 它的头注释说的正是这次的机制:
// 「这个文件的注释里 `**` 满天飞,下一个人从注释里顺手抄一句进字符串是最自然的
// 动作,而没有任何东西拦着」。announceReachTargets 就是那个下一个人,判据因此
// 跟着扩到 leakcheck 这两个出口。终端不渲染 markdown,用户读到的是字面星号。
func TestNoLeakCheckOutputCarriesMarkdown(t *testing.T) {
	out := captureStdout(t, func() { announceReachTargets(leakserve.LiveReachDeps(), false) })
	if strings.Contains(out, "**") {
		t.Errorf("披露那几行渲染出了 markdown 的 `**`,终端不认它:\n%s", out)
	}
	facts := leakcheck.LocalFacts{}
	for _, tgt := range leakcheck.ReachTargets() {
		facts.ReachProbes = append(facts.ReachProbes, leakcheck.ReachProbe{
			TargetID: tgt.ID, Path: leakcheck.ReachPathCurrent, State: leakcheck.ReachReachable,
		})
	}
	// 两种输入各跑一遍:全 not checked 的那份与四条 reach 都有答案的那份,
	// 走的是不同的措辞分支。
	for _, f := range []leakcheck.LocalFacts{{}, facts} {
		rep := leakcheck.Judge(time.Unix(0, 0).UTC(), leakcheck.BrowserReport{}, f)
		for _, line := range renderLeakCheckReport(rep) {
			if strings.Contains(line, "**") {
				t.Errorf("报告渲染出了 markdown 的 `**`:%q", line)
			}
		}
	}
}

// **预算分离此前有三处注释、零条断言。**
//
// 现有的探测测试用的是**立刻返回错误**的拨号器 —— 5 秒上限在它眼里看不出任何
// 区别,又一次「测试输入让待守属性不可见」。一次看起来无辜的整理(把 CollectReach
// 折进 CollectFactsWithBudget 那份 5 秒预算里)会全绿通过,而真机上后面几个目标
// 静默变成 undetermined,与「这条路真的不通」在屏幕上一模一样。
//
// 两条断言:① 这一轮的预算必须真的比本机采集那份宽;② 喂一个**第一个目标就阻塞
// 得比那份预算还久**的拨号器,后面的目标仍然必须被拨到。
func TestReachProbesDoNotShareTheLocalFactsBudget(t *testing.T) {
	if leakserve.ReachBudget() <= leakserve.DefaultFactsBudget {
		t.Fatalf("可达性探测的预算 %v 不比本机采集那份 %v 宽 —— 它被塞回同一份预算里了",
			leakserve.ReachBudget(), leakserve.DefaultFactsBudget)
	}

	targets := leakcheck.ReachTargets()
	blockFor := leakserve.DefaultFactsBudget + time.Second
	var mu sync.Mutex
	dialed := 0
	reach := leakserve.ReachDeps{
		CurrentDial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			n := dialed
			dialed++
			mu.Unlock()
			if n == 0 {
				// 第一个目标把本机采集那份预算整个吃掉还有余。
				select {
				case <-time.After(blockFor):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nil, errors.New("这条测试不联网")
		},
	}
	facts := collectLeakCheckFacts(context.Background(), leakserve.FactDeps{}, reach)
	mu.Lock()
	got := dialed
	mu.Unlock()
	if got != len(targets) {
		t.Fatalf("只拨到 %d 个目标(共 %d)—— 第一个目标花了 %v 就把后面的掐掉了,"+
			"那正是把探测塞进本机采集那份 %v 预算的后果",
			got, len(targets), blockFor, leakserve.DefaultFactsBudget)
	}
	if len(facts.ReachProbes) != len(targets) {
		t.Fatalf("ReachProbes 只有 %d 条,要 %d 条", len(facts.ReachProbes), len(targets))
	}
}

// **`--no-reach` 必须真的关掉探测,而那四条结论不许就此消失。**
//
// 关掉时它们如实报「这一轮没有检查」—— 让它们消失会让用户以为 bx 压根没有这一段,
// 而「没查」与「查了、没问题」正是这个工具存在的理由要分开的两件事。
func TestNoReachTurnsOffTheProbesWithoutHidingTheConclusions(t *testing.T) {
	app := New()
	cmd := findAppCommand(app, "leakcheck")
	hasFlag := false
	for _, f := range cmd.Flags {
		for _, n := range f.Names() {
			if n == "no-reach" {
				hasFlag = true
			}
		}
	}
	if !hasFlag {
		t.Fatal("bx leakcheck 没有 --no-reach —— 路径 A 同样是 bx 从用户真实出口" +
			"向四家 AI 厂商发的请求,两条路径一个有开关一个没有,不对称")
	}

	off := reachDepsFor(true)
	if off.WillProbe() {
		t.Fatal("--no-reach 之下仍然会发探测")
	}
	if on := reachDepsFor(false); !on.WillProbe() {
		t.Fatal("不加 --no-reach 时反而不探测了 —— 上面那条断言于是靠「一律不探」平凡成立")
	}
	// 关掉时**一个字都不披露**:披露的意义是「我接下来要联系他们」。
	if out := captureStdout(t, func() { announceReachTargets(off, false) }); out != "" {
		t.Errorf("--no-reach 之下仍然披露了要联系谁:%q", out)
	}

	facts := collectLeakCheckFacts(context.Background(), leakserve.FactDeps{}, off)
	if len(facts.ReachProbes) != 0 {
		t.Fatalf("--no-reach 之下仍然产出了 %d 条探测记录", len(facts.ReachProbes))
	}
	rep := leakcheck.Judge(time.Unix(0, 0).UTC(), leakcheck.BrowserReport{}, facts)
	reachFindings := 0
	for _, f := range rep.Findings {
		if f.Section != leakcheck.SectionReach {
			continue
		}
		reachFindings++
		if f.Verdict != leakcheck.NotChecked {
			t.Errorf("%s 在没探测的情况下给出了 %q 的结论", f.ID, f.Verdict)
		}
	}
	if reachFindings != len(leakcheck.ReachTargets()) {
		t.Fatalf("--no-reach 之下第四段只剩 %d 条结论(要 %d 条)—— 它们不许消失,"+
			"消失会让用户以为 bx 压根没有这一段", reachFindings, len(leakcheck.ReachTargets()))
	}
	// 而且屏幕上必须说得出「没查」。
	out := strings.Join(renderLeakCheckReport(rep), "\n")
	if !strings.Contains(out, "not checked in this run") {
		t.Errorf("--no-reach 之下第四段没说出「这一轮没有检查」:\n%s", out)
	}
	if !strings.Contains(out, "4 undetermined") {
		t.Errorf("--no-reach 之下摘要没把四条都算进「没问出来」:\n%s", out)
	}
}
