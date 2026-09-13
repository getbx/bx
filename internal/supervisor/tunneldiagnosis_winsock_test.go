package supervisor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// Minor 3:「SYN 没离开本机」那张表在 Windows 上曾经整个是**死的**。
//
// zerrors_windows.go 把 syscall.ENETUNREACH 一族定义成 `APPLICATION_ERROR + iota`
// (≥ 2^29),winsock 一次都不会返回它们;真实的那次失败是 WSAENETUNREACH(10051),
// 而 syscall.Errno.Is 在两者之间什么都不映射。于是 Windows 上每一次「本机自己
// 够不着」都被报成 tunnel_unreachable ——「那台服务器没有应答」,而 SYN 一个都
// 没出去。同一个缺陷(I3)原样活在第三个平台上。
//
// **判据为什么读源码**:Windows 那半的行为测试只在 CI 的 windows runner 上跑,
// 而 verify.sh 对 Windows 只做交叉**编译**(go build 连 _test.go 都不编)。在
// darwin 上开发的人给 posix 那张表加一行时,唯一会当场转红的就是这一条。
// 命名规则是 winsock 自己的:每个 E<X> 的孪生就叫 WSAE<X>。
func TestEveryLocalDialFailureHasAWinsockTwin(t *testing.T) {
	posix := errnoSelectorsInVar(t, "tunneldiagnosis.go", "posixDialFailuresBeforeTheSYNLeaves", "syscall", "E")
	if len(posix) == 0 {
		t.Fatal("posixDialFailuresBeforeTheSYNLeaves 里一个 syscall.E… 都没读出来 —— " +
			"判据认不出现在的写法了,而认不出的时候它恰好什么都不守")
	}
	winsock := errnoSelectorsInVar(t, "tunneldiagnosis_windows.go", "platformDialFailuresBeforeTheSYNLeaves", "windows", "WSAE")
	if len(winsock) == 0 {
		t.Fatal("platformDialFailuresBeforeTheSYNLeaves 里一个 windows.WSAE… 都没读出来 —— " +
			"Windows 上那张表是死的,每一次本机不可达都会被说成「你的 VPS 挂了」")
	}
	for _, name := range posix {
		want := "WSA" + name
		if !slices.Contains(winsock, want) {
			t.Fatalf("posix 那张表里有 syscall.%s,而 Windows 那份没有它的孪生 windows.%s\n"+
				"(现有 %v)—— Windows 的 winsock 永远不返回 syscall.%s,那一行在那边是死的,\n"+
				"于是「SYN 没离开本机」会被报成「那台服务器的端口没有应答」",
				name, want, winsock, name)
		}
	}
}

// errnoSelectorsInVar 读出某个文件里某个 var 的切片字面量,取其中形如
// `<pkg>.<prefix>…` 的选择子名(去掉包名那一半)。
func errnoSelectorsInVar(t *testing.T, file, varName, pkg, prefix string) []string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s:%v", file, err)
	}
	var names []string
	found := false
	ast.Inspect(parsed, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, ident := range spec.Names {
			if ident.Name != varName || i >= len(spec.Values) {
				continue
			}
			found = true
			ast.Inspect(spec.Values[i], func(inner ast.Node) bool {
				sel, ok := inner.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == pkg && strings.HasPrefix(sel.Sel.Name, prefix) {
					names = append(names, sel.Sel.Name)
				}
				return true
			})
		}
		return true
	})
	if !found {
		t.Fatalf("%s 里没有名为 %s 的 var —— 这条守卫的锚点漂了,它此刻什么都不守", file, varName)
	}
	return names
}
