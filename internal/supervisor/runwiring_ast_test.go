package supervisor

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// 这个文件是接线守卫的**判据**那一半。
//
// 为什么不能只用字符串匹配:复审拿两个能编译、`go test ./...` 全绿的改动
// 分别打穿了原来那两条守卫 ——
//
//	① runtimeBypass := bypassWire.runtimeServerBypass   // 原字面量原样留着
//	   cachedBypass := runtimeBypass()
//	   runtimeBypass = func() []string { return cachedBypass }   // 冻住了
//
//	② bypass: newBypassStore(bypassState.cidrs(), bypassState.staticEntries(),
//	          bypassState.serverEntries()),   // 含 "bypass:" 与 "bypassState"
//
// 两处都恢复了守卫名字里写着的那个 bug(屏障开口/路径恢复用冻结拷贝 = 静默成环),
// 而守卫要的子串一个不少。子串守的是**拼写**;这里守的是**身份** ——
// 那个位置上必须是一个光秃秃的标识符,不带调用、不带包装、不带运算符,
// 且它在 Run() 里**只被赋值过一次**(②是换初始化式,①是事后重新赋值,
// 光看初始化式抓不到)。
//
// CLAUDE.md 已经记着「文本守卫天生是漏的」。AST 判据把「漏」的面积从
// 「任何相邻文本」缩到「必须换一个同名变量、且那个变量本身也只赋值一次」。

func runFuncDecl(t *testing.T) *ast.FuncDecl {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "run.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 run.go: %v", err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == "Run" {
			return fn
		}
	}
	t.Fatal("run.go 里找不到 func Run(接线守卫失效,请更新本测试)")
	return nil
}

// assignmentsTo 收集 fn 里**每一次**对名为 name 的标识符的赋值,返回各自的右值。
// 包含 `:=`、`=`、`var x = …`、以及 range 的赋值;嵌套闭包里的同名赋值一并计入
// —— 在闭包里 shadow 一个同名变量,效果与事后重新赋值一样是冻住。
// 右值取不到(多返回值解构等)时以 nil 占位:调用方会因此判失败,不会误判成合格。
func assignmentsTo(fn ast.Node, name string) []ast.Expr {
	var out []ast.Expr
	record := func(lhs, rhs []ast.Expr) {
		for i, l := range lhs {
			id, ok := l.(*ast.Ident)
			if !ok || id.Name != name {
				continue
			}
			if len(rhs) == len(lhs) {
				out = append(out, rhs[i])
			} else {
				out = append(out, nil) // 解构赋值:右值无法一一对应
			}
		}
	}
	ast.Inspect(fn, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			record(s.Lhs, s.Rhs)
		case *ast.ValueSpec:
			var lhs []ast.Expr
			for _, id := range s.Names {
				lhs = append(lhs, id)
			}
			record(lhs, s.Values)
		case *ast.RangeStmt:
			record([]ast.Expr{s.Key, s.Value}, nil)
		}
		return true
	})
	return out
}

// requireNoFieldWrites 断言 fn 里没有 `name.字段 = …` 这种写法。
//
// 为什么单独有这一条:只盯变量本身的赋值,挡不住换个门进来的同一件事 ——
//
//	cachedBypass := bypassWire.runtimeServerBypass()
//	bypassWire.runtimeServerBypass = func() []string { return cachedBypass }
//	runtimeBypass := bypassWire.runtimeServerBypass   // 仍然只赋值一次
//
// 实测:补这条之前它能编译、`go test ./...` 全绿,而屏障开口已经冻死。
// 这几个接线变量都是「接好之后只读」的,任何字段写入都不该出现在 Run() 里。
func requireNoFieldWrites(t *testing.T, fn ast.Node, name, why string) {
	t.Helper()
	var bad []string
	ast.Inspect(fn, func(n ast.Node) bool {
		s, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, l := range s.Lhs {
			sel, ok := l.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == name {
				bad = append(bad, name+"."+sel.Sel.Name)
			}
		}
		return true
	})
	if len(bad) > 0 {
		t.Fatalf("Run() 里给 %v 赋了值 —— 这几个接线变量接好之后只读。\n%s\n"+
			"(改字段与改变量是同一件事,而只盯变量的守卫抓不到它 —— 复审实测过)", bad, why)
	}
}

// requireNoWritesToFieldNamed 断言 fn 里没有任何 `X.field = …`,**不管 X 叫什么**。
//
// 按接收者名字禁(上面那条)还留着两个门:
//
//	p := &bypassWire; p.runtimeServerBypass = frozen   // 换个名字持有同一个东西
//	mut.store = newBypassStore(…)                     // 构造时接对了,事后改掉
//
// 后者尤其像正常代码 —— run.go 里本来就有 `mut.udpSwap = udpSwapper`,
// 所以不能笼统禁掉 mut 的字段写入,只能按**字段名**禁。这三个字段
//(store / bypass / runtimeServerBypass)在 Run() 里一次合法写入都没有。
func requireNoWritesToFieldNamed(t *testing.T, fn ast.Node, field, why string) {
	t.Helper()
	var bad []string
	ast.Inspect(fn, func(n ast.Node) bool {
		s, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, l := range s.Lhs {
			if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == field {
				bad = append(bad, exprText(sel))
			}
		}
		return true
	})
	if len(bad) > 0 {
		t.Fatalf("Run() 里给 %v 赋了值 —— 这个字段接好之后只读。\n%s", bad, why)
	}
}

// requireNoPackageWritesToFieldNamed 把上一条扩到**整个包的非测试文件**。
//
// 还剩最后一个门:冻结发生在 Run() 之外 ——
//
//	func freezeWiringForNow(w *bypassWiring) {
//	    cached := w.runtimeServerBypass()
//	    w.runtimeServerBypass = func() []string { return cached }
//	}
//
// Run() 里只多一行 `freezeWiringForNow(&bypassWire)`,接线一个字没改。
// 实测:补这条之前它能编译、`go test ./...` 全绿。
//
// 只对 runtimeServerBypass 这个字段名做包级扫描,因为它在生产代码里**从不被赋值**
// (只在 wireBypass 的复合字面量里出现一次,那不是 AssignStmt)。
// store / bypass 做不到同样的事:`bypassStore.set` 里的 `s.bypass = …`、
// mutator 里的字段都是合法写入,按名字禁会误伤 —— 那两个字段的同形攻击仍是已知缺口,
// 已写进报告。
func requireNoPackageWritesToFieldNamed(t *testing.T, field, why string) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("解析 supervisor 包: %v", err)
	}
	var bad []string
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				s, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, l := range s.Lhs {
					if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == field {
						bad = append(bad, fmt.Sprintf("%s:%d", name, fset.Position(sel.Pos()).Line))
					}
				}
				return true
			})
		}
	}
	if len(bad) > 0 {
		t.Fatalf("包里给 .%s 赋了值(%v)—— 这个字段在生产代码里从不被赋值,\n"+
			"出现赋值就意味着有人在 Run() 之外把它换掉了。\n%s", field, bad, why)
	}
}

func exprText(sel *ast.SelectorExpr) string {
	if id, ok := sel.X.(*ast.Ident); ok {
		return id.Name + "." + sel.Sel.Name
	}
	return "?." + sel.Sel.Name
}

// soleAssignment 断言 name 在 fn 里只被赋值过一次,并返回那次的右值。
func soleAssignment(t *testing.T, fn ast.Node, name, why string) ast.Expr {
	t.Helper()
	got := assignmentsTo(fn, name)
	if len(got) != 1 {
		t.Fatalf("%s 在 Run() 里被赋值了 %d 次,必须恰好 1 次。\n%s\n"+
			"(第二次赋值就能在保留原初始化式的前提下把它换成冻结快照 —— 复审实测过)",
			name, len(got), why)
	}
	return got[0]
}

// requireSelector 断言表达式就是 `x.sel` 本身,没有调用、没有包装。
func requireSelector(t *testing.T, e ast.Expr, x, sel, why string) {
	t.Helper()
	s, ok := e.(*ast.SelectorExpr)
	if !ok {
		t.Fatalf("期望 %s.%s 本身(不带调用/包装),实际是 %T。\n%s", x, sel, e, why)
	}
	id, ok := s.X.(*ast.Ident)
	if !ok || id.Name != x || s.Sel.Name != sel {
		t.Fatalf("期望 %s.%s,实际是别的选择器。\n%s", x, sel, why)
	}
}

// requireCallOf 断言表达式是对名为 name 的函数的直接调用(`name(…)`)。
func requireCallOf(t *testing.T, e ast.Expr, name, why string) {
	t.Helper()
	call, ok := e.(*ast.CallExpr)
	if !ok {
		t.Fatalf("期望 %s(…) 的调用结果,实际是 %T。\n%s", name, e, why)
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok || id.Name != name {
		t.Fatalf("期望调用 %s(…),实际调用的是别的东西。\n%s", name, why)
	}
}

// requireBareIdent 断言表达式是一个光秃秃的标识符,且名字就是 want。
// `nil`、`newBypassStore(...)`、`&x`、`x.y` 全部落选 —— 这正是复审那个攻击的形状。
func requireBareIdent(t *testing.T, e ast.Expr, want, why string) {
	t.Helper()
	id, ok := e.(*ast.Ident)
	if !ok {
		t.Fatalf("这里必须是变量 %s 本身(不带调用、不带包装、不带运算符),实际是 %T。\n%s", want, e, why)
	}
	if id.Name != want {
		t.Fatalf("这里必须是变量 %s 本身,实际是 %q。\n%s", want, id.Name, why)
	}
}

// compositeField 取 fn 里第一个 typeName 复合字面量的 field 字段值。
func compositeField(t *testing.T, fn ast.Node, typeName, field string) ast.Expr {
	t.Helper()
	var found ast.Expr
	var sawLit bool
	ast.Inspect(fn, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		id, ok := lit.Type.(*ast.Ident)
		if !ok || id.Name != typeName {
			return true
		}
		sawLit = true
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == field {
				found = kv.Value
				return false
			}
		}
		return true
	})
	if !sawLit {
		t.Fatalf("Run() 里找不到 %s{…}(接线守卫失效,请更新本测试)", typeName)
	}
	if found == nil {
		t.Fatalf("%s{…} 里没有 %s 字段 —— 缺席等于用零值,而这里的零值就是那个 bug", typeName, field)
	}
	return found
}
