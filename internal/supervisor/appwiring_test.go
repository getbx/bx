package supervisor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/dialer"
	"github.com/getbx/bx/internal/tun"
)

// **两个不同实例会让连接记录和字节账对不上,而两边各自看起来都正常** ——
// 界面上每个应用都有连接数、字节数恒为 0,没有任何一处会报错。
func TestWireAppAttributionGivesDialerAndEngineTheSameInstance(t *testing.T) {
	at := NewAppTraffic(&fakeAppSource{}, nil)
	d := &dialer.Dialer{}
	opt := wireAppAttribution(d, at)

	var e tun.Engine
	opt(&e)

	if d.AppRecorder != dialer.AppRecorder(at) {
		t.Fatalf("dialer 拿到的不是那个 AppTraffic: %#v", d.AppRecorder)
	}
	if tun.ByteAttributorOf(&e) != tun.ByteAttributor(at) {
		t.Fatalf("engine 拿到的不是那个 AppTraffic: %#v", tun.ByteAttributorOf(&e))
	}
	// 两边必须指向同一个对象,而不只是「各自都非 nil」。
	if any(d.AppRecorder) != any(tun.ByteAttributorOf(&e)) {
		t.Fatal("dialer 与 engine 拿到了两个不同的实例")
	}
}

// 上一条测试证明的是「wireAppAttribution 这一个函数不会分叉」;这一条证明的是
// **生产代码里没有第二条绕过它的接线路径** —— 否则前一条测的就只是它自己。
//
// 判据是 AST 而不是文本匹配:禁的是「AppRecorder 被赋值 / WithByteAttribution
// 被调用」这两件事本身,不是它们的某一种拼法。
func TestAppAttributionIsWiredInExactlyOnePlace(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var assigns, options []string // 出现的位置(函数名)
	runCallsWiring := false
	sawWiringFunc := false

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			// **读不懂现在的代码时必须响亮失败**,不许静默放行 ——
			// 一个看不见任何接线的守卫会永远绿。
			t.Fatalf("解析 %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Name.Name == "wireAppAttribution" {
				sawWiringFunc = true
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.AssignStmt:
					for _, lhs := range node.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "AppRecorder" {
							assigns = append(assigns, fn.Name.Name)
						}
					}
				case *ast.CallExpr:
					sel, ok := node.Fun.(*ast.SelectorExpr)
					if ok && sel.Sel.Name == "WithByteAttribution" {
						options = append(options, fn.Name.Name)
					}
					if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "wireAppAttribution" && fn.Name.Name == "Run" {
						runCallsWiring = true
					}
				}
				return true
			})
		}
	}

	if !sawWiringFunc {
		t.Fatal("找不到 wireAppAttribution —— 守卫读不懂现在的代码")
	}
	if len(assigns) != 1 || assigns[0] != "wireAppAttribution" {
		t.Fatalf("AppRecorder 的赋值点 = %v, want 只有 wireAppAttribution", assigns)
	}
	if len(options) != 1 || options[0] != "wireAppAttribution" {
		t.Fatalf("WithByteAttribution 的调用点 = %v, want 只有 wireAppAttribution", options)
	}
	if !runCallsWiring {
		t.Fatal("Run 没有调用 wireAppAttribution —— 数据面根本没接上应用归因")
	}
}
