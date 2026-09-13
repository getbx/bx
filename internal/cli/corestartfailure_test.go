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

	"github.com/getbx/bx/internal/corestartfailure"
	"github.com/getbx/bx/internal/supervisor"
)

// 手敲的 `sudo bx run` 一个字节都不许写。
//
// **这是「陈旧记录不许被采信」的第一层,而且它是构造上的那一层**:只有
// Guardian 传 --start-failure-file,所以调试用的 `sudo bx run`(本项目自己
// 文档化的路径)、以及任何别的进程,都不可能往那个位置留下一份看起来像
// 「这一次」的记录。第二层(PID + 时间窗口)在 Guardian 那边。
//
// **判据是「写盘那一步一次都没被调用」,不是「磁盘上没多出文件」。** 后者
// 守不住任何东西:去掉那道门之后 corestartfailure.Write("") 会在当前目录建一个
// 临时文件、rename 到 "" 失败、再由 defer 删掉 —— 磁盘上不留痕迹,而那道门
// 已经没了。写盘因此是一个注入进来的 startFailureWriter。
func TestRunWithoutTheFlagWritesNothing(t *testing.T) {
	calls := 0
	spy := func(string, corestartfailure.Record) error { calls++; return nil }
	err := recordStartFailure("", supervisor.ErrTunnelUnreachable, spy)
	if !errors.Is(err, supervisor.ErrTunnelUnreachable) {
		t.Fatalf("返回的错误变了:%v", err)
	}
	if calls != 0 {
		t.Fatalf("没给 --start-failure-file 却调了 %d 次写盘 —— 手敲的 sudo bx run "+
			"必须一个字节都不留,否则陈旧记录的第一层防线就没了", calls)
	}
}

// 写出去的字节里不许有链接,也不许有配置路径。
//
// 这条不是理论:失败错误里**真的**带着它们 —— diagnoseUnhealthyTunnel 那句
// 「没能从服务器链接里解出 host:port」当初就差点把 *url.Error 原样打印的整条
// vless 链接(uuid 在里面)带出来,批一为此改过一次。而这份记录会被 Guardian
// 读走、它的码会走到用户面前,一条凭据一旦进了会被转发的东西就再也收不回来。
//
// 判据打在**盘上的字节**上,不打在「我们只填了这几个字段」上。
func TestTheRecordCarriesNeitherTheLinkNorTheConfigPath(t *testing.T) {
	const link = "vless://11111111-2222-3333-4444-555555555555@198.51.100.7:443?security=reality"
	const configPath = "/etc/bx/config.yaml"
	path := filepath.Join(t.TempDir(), "core-start-failure.json")

	err := runWithStartFailureRecord(path, func() error {
		return fmt.Errorf("读配置 %s 失败,链接是 %s: %w", configPath, link, supervisor.ErrTunnelUnreachable)
	})
	if err == nil {
		t.Fatal("包装层把错误吞了")
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("记录没写出来:%v", readErr)
	}
	bytes := string(raw)
	for _, secret := range []string{link, "11111111-2222-3333-4444-555555555555", configPath, "vless://"} {
		if strings.Contains(bytes, secret) {
			t.Fatalf("记录里出现了 %q —— 它跨进程走到 Guardian,再从那里走到用户面前:\n%s", secret, bytes)
		}
	}
	record, err := corestartfailure.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if record.Code != supervisor.StartFailureTunnelUnreachable {
		t.Fatalf("码写成了 %q,want %q", record.Code, supervisor.StartFailureTunnelUnreachable)
	}
	if record.PID != os.Getpid() {
		t.Fatalf("记录里的 PID 是 %d,want 本进程 %d —— Guardian 靠它认这份记录是不是这一次的", record.PID, os.Getpid())
	}
}

// 写盘失败**不许改变 Run 返回的那个错误**。
//
// 一个诊断不许把一次故障换成另一次故障:用户的 VPS 不通,而 bx 因为
// /var/lib/bx 写不进去就改口说别的,等于把这一整支修复的产出丢掉,还多编了
// 一个不存在的病因。
func TestAFailedRecordWriteDoesNotChangeTheError(t *testing.T) {
	sentinel := fmt.Errorf("原本那个失败: %w", supervisor.ErrTunnelUnreachable)
	// 目录不存在 ⇒ 临时文件都建不出来。
	unwritable := filepath.Join(t.TempDir(), "没有这个目录", "core-start-failure.json")
	err := runWithStartFailureRecord(unwritable, func() error { return sentinel })
	if err != sentinel { //nolint:errorlint // 要的就是「同一个错误值」,不是「链上有它」
		t.Fatalf("写盘失败之后返回的是 %v —— 必须原样返回 Run 那个错误,一个诊断不许把故障换成另一个故障", err)
	}
}

// 成功启动一次记录都不写。
//
// 记录的存在本身就是「上一次起失败了」这个信号的载体(Guardian spawn 之前会
// 先删),成功那条路上多写一份就是给下一次留一个陈旧文件。判据同上:调用
// 次数,不是磁盘。
func TestASuccessfulRunWritesNothing(t *testing.T) {
	calls := 0
	spy := func(string, corestartfailure.Record) error { calls++; return nil }
	if err := recordStartFailure("/var/lib/bx/core-start-failure.json", nil, spy); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("成功启动也写了 %d 次记录 —— 那是给下一次失败准备的一份陈旧证据", calls)
	}
}

// runAction 的**两个** supervisor.Run 出口都必须盖在同一个包装里。
//
// 它有两条出口:isWindowsService() 那条(经 runAsWindowsService)与常规那条。
// 只盖一条的后果是静默的:Windows 服务里起不来的 Core 什么都不说,而
// Guardian 那边落回 core_health_failed —— 与这一整支修复之前完全一样,
// 没有任何东西会报错。
//
// 判据是 AST:`runAction` 函数体里每一次 `supervisor.Run(` 都必须出现在
// `runWithStartFailureRecord(` 的实参里。**不是「这个文件里提到过包装函数」**
// —— 那样的话把包装挪到只覆盖其中一条出口照样绿,而那正是要防的那件事。
func TestBothRunExitsAreCoveredByTheStartFailureRecorder(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "cli.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 cli.go 失败:%v", err)
	}
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "runAction" && d.Recv == nil {
			fn = d
		}
	}
	if fn == nil {
		// 读不懂现在的代码时必须响亮失败:一条恰好在最需要时不可达的守卫,
		// 与没有这条守卫完全一样,而它看起来更让人放心。
		t.Fatal("cli.go 里找不到 runAction —— 这条守卫的锚点漂了,回来重判,别让它静默放行")
	}

	inside := map[*ast.CallExpr]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "runWithStartFailureRecord" {
			return true
		}
		for _, arg := range call.Args {
			ast.Inspect(arg, func(inner ast.Node) bool {
				if c, ok := inner.(*ast.CallExpr); ok {
					inside[c] = true
				}
				return true
			})
		}
		return true
	})

	total := 0
	uncovered := 0
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Run" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "supervisor" {
			return true
		}
		total++
		if !inside[call] {
			uncovered++
			t.Errorf("runAction 里 %s 那一处 supervisor.Run 不在 runWithStartFailureRecord 的实参里 ——"+
				"这条出口上的 Core 起不来时什么都不会说", fset.Position(call.Pos()))
		}
		return true
	})
	if total < 2 {
		t.Fatalf("runAction 里只找到 %d 处 supervisor.Run,want ≥2(windows service 一条、常规一条)——"+
			"锚点漂了就回来重判这条守卫", total)
	}
	if uncovered > 0 {
		t.FailNow()
	}
}
