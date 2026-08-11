package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
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
		return exprText(e.Fun) + "()"
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
