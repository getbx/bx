package supervisor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"
)

// **`RuntimeState` 的每一个字段都必须在 `Run` 里被真的填上。**
//
// 缺一个字段的后果是**静默**的:JSON 编码器发一个零值出去,而每个消费方都有
// 自己的退路 —— `bx explain` 的假 IP 段退回内建默认、`ResolverLabel` 退回一句
// 「问不出来」、`bx status` 少一格。全都不报错,全都看起来正常。
//
// 这条守卫是 2026-09-13 补的,而补它的直接理由是一次变异实测:`FakeipCIDR`
// 刚接上,把 `run.go` 里那一行删掉之后 **`internal/supervisor` 与
// `internal/cli` 两个包全绿** —— 消费方那两条新守卫盯的是「客户端有没有用
// Core 报的值」,盯不到「Core 到底发没发」。**守卫钉住了缺陷旁边的东西**,
// 是这个仓库反复出现的那个形状。
//
// **方向刻意是「默认参与」**:新加一个字段而忘了在 `Run` 里填,这条当场转红;
// 反过来做(白名单式,只查列出来的那几个)会让新字段静默地永远不发。真有某个
// 字段不该在这里填,就把它加进 runtimeStateNotWiredInRun 并**写下理由** ——
// 与 statusdigest 那两张表同一条纪律。
//
// 判据取 AST 不取文本:注释里、字符串里、别的函数里的 `FakeipCIDR:` 都不算。
// 读不出 `Run`、或找不到那个复合字面量时**响亮失败**,不静默放行 —— 一条读不懂
// 现在的代码却还绿着的守卫,与没有这条守卫完全一样。
var runtimeStateNotWiredInRun = map[string]string{
	// 目前为空:`Run` 是 RuntimeState 唯一的生产产地,每一格都该有答案。
}

func TestRunPublishesEveryRuntimeStateField(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "run.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 run.go: %v —— 守卫读不懂现在的代码了,先修守卫", err)
	}

	var assigned map[string]bool
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		ident, ok := lit.Type.(*ast.Ident)
		if !ok || ident.Name != "RuntimeState" {
			return true
		}
		if assigned != nil {
			t.Fatalf("run.go 里有不止一处 RuntimeState{…} 字面量 —— 这条守卫假定只有一处" +
				"(唯一的产地),先修守卫再说")
		}
		assigned = map[string]bool{}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				t.Fatalf("RuntimeState{…} 里有位置式(非 Field: value)的元素 —— " +
					"那种写法漏字段时编译器也不说话,而这条守卫看不见它")
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				t.Fatal("RuntimeState{…} 的键不是标识符 —— 守卫读不懂,先修守卫")
			}
			assigned[key.Name] = true
		}
		return false
	})
	if assigned == nil {
		t.Fatal("在 run.go 里找不到 RuntimeState{…} 字面量 —— 它搬走了还是改写法了?" +
			"守卫在最需要它的时候恰好不可达,与没有这条守卫一样,先修守卫")
	}

	typ := reflect.TypeOf(RuntimeState{})
	if typ.NumField() < 10 {
		t.Fatalf("RuntimeState 只有 %d 个字段,少得反常 —— 守卫可能拿错了类型", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if why, exempt := runtimeStateNotWiredInRun[name]; exempt {
			if assigned[name] {
				t.Errorf("%s 在「不该在 Run 里填」的名单里(%s),但 run.go 现在填了它 —— "+
					"把这条从名单里删掉,一份说某件事没做而它其实做了的名单,和一句失效的注释一样坏",
					name, why)
			}
			continue
		}
		if !assigned[name] {
			t.Errorf("RuntimeState.%s 没有在 run.go 的 RuntimeState{…} 里被填上 —— "+
				"消费方会收到零值,而每个消费方都有自己的退路(退回默认段 / 退回"+
				"「问不出来」/ 少一格),**没有一个会报错**。要么接上它,要么加进 "+
				"runtimeStateNotWiredInRun 并写下理由", name)
		}
	}
}
