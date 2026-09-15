package dialfail

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// —— 这一族测试守的是「一个百分比答不出该不该管」的下半句 ——
//
// 上半句(分类)2026-09-01 就做完了:`*.qq.com 15.4% 失败` 现在会拆成
// `[路由不可达×410 对端不应答×15]`。而**处置**仍然留给读的人自己推 ——
// 而那句推理恰恰写在本包每个常量的注释里,从来没被印出来过。
//
// BlameFor 就是把它印出来的那半。它替代了 LooksLikeOurFault:后者是个
// **零生产调用方**的 bool(本仓库记着的第三种守卫失效 ——「一个没人调用而
// 测试盖着的函数,与没有测试在输出上完全一样」),而且两态不够用 ——
// `Other`(认不出)在 bool 下与 `Timeout`(对端的问题)返回同一个 false。

// **零值必须是「说不出」。** 与 observe.Tristate、leakcheck.ReachUndetermined、
// rulereview 的 Class 同一条:漏填时多报好过漏报,而这里「漏报」的具体形状是
// 把一个认不出来的失败说成「不是 bx 的问题」,于是没有人再去查。
func TestUndeterminedIsTheZeroValue(t *testing.T) {
	var zero Blame
	if zero != BlameUndetermined {
		t.Fatalf("Blame 的零值是 %v,应当是 BlameUndetermined —— 漏填一类时它会被读成一个确定的答案", zero)
	}
}

// 每一类指向谁,逐条钉死。**方向判错的代价不对称**:把对端的问题说成 bx 的,
// 会让人去改一个没坏的东西;把 bx 的问题说成对端的,会让人去重启一台好好的 VPS。
func TestEveryKindIsClassifiedAndTheTableCoversThemAll(t *testing.T) {
	want := map[string]Blame{
		// 指向本机/bx 这一侧。
		Unreachable: BlameLocal, // 2026-08-13 那个 DirectDialer 故障的签名
		DNS:         BlameLocal, // net.Resolver 把「连不上 223.5.5.5」也包成 DNSError
		// 具名出口写在 config 里而运行时没接上 —— 该改的是配置,但它确实在本机这一侧。
		EgressUnwired: BlameLocal,

		// 指向对端 / 路上。改规则一个字都没用。
		Timeout: BlameRemote,
		Refused: BlameRemote,
		Reset:   BlameRemote,

		// 压根不是「这条路走不通」。
		DNSNotFound: BlameNotAFailure, // 应用在查一批不存在的主机名(微信这类客户端会)
		Canceled:    BlameNotAFailure, // 调用方自己走了

		// 认不出来。**绝不许落到上面任何一档。**
		Other: BlameUndetermined,
	}

	for kind, expect := range want {
		if got := BlameFor(kind); got != expect {
			t.Errorf("BlameFor(%q) = %v,应当是 %v", kind, got, expect)
		}
	}

	// **穷举守卫**:本包新加一个类别而忘了分类时,它在这里红一次。
	// 少了这一半,新类别会静默落进零值 BlameUndetermined —— 而那读起来
	// 像是「我们想过了,判不出来」,与「根本没人想过」完全无法区分。
	// 这个仓库为「渲染层按字面枚举,新加一类只会静默消失」栽过四次。
	for _, kind := range declaredKinds(t) {
		if _, ok := want[kind]; !ok {
			t.Errorf("常量 %q 没有出现在这张分类表里 —— 它会静默落进零值,"+
				"而零值的意思是「想过了但判不出来」,不是「没人想过」", kind)
		}
	}

	// 反向:表里不许有指向已不存在常量的陈旧条目。陈旧条目看起来与生效中的
	// 一模一样而什么也不守(与 statusdigest 那两张表同一条)。
	declared := map[string]bool{}
	for _, kind := range declaredKinds(t) {
		declared[kind] = true
	}
	for kind := range want {
		if !declared[kind] {
			t.Errorf("分类表里的 %q 已经不是本包的常量了 —— 陈旧条目什么也不守", kind)
		}
	}
}

// 空串不是一类失败(Classify 对 nil 返回它),不许被当成某种判决。
func TestTheEmptyKindIsNotAVerdict(t *testing.T) {
	if got := BlameFor(""); got != BlameUndetermined {
		t.Errorf("BlameFor(\"\") = %v —— 空串是「没有失败」,不是一种判决", got)
	}
}

// —— Dominant:什么时候**才**允许说出一句判决 ——

// 一类占了绝对多数,才说得出一句话。
func TestDominantNamesTheKindOnlyWhenItIsAMajority(t *testing.T) {
	kind, ok := Dominant(map[string]int64{Unreachable: 6, Timeout: 4})
	if !ok || kind != Unreachable {
		t.Errorf("6/10 的 unreachable 没被认成主因:kind=%q ok=%v", kind, ok)
	}
}

// **这条是决定性的**:最多的那一类只有 40%,说「主因是它」就是编答案。
// 一份三类各占三分之一的失败,可行动的信息恰恰是「它不是一个原因造成的」。
func TestDominantRefusesAPluralityThatIsNotAMajority(t *testing.T) {
	if kind, ok := Dominant(map[string]int64{Unreachable: 4, Timeout: 3, Reset: 3}); ok {
		t.Errorf("4/10 被当成了主因(%q)—— 那是个复数原因的失败,不该被说成一个", kind)
	}
}

// 正好一半也不算 —— 「多数」是严格多数。5 个路由不可达 + 5 个对端不应答
// 是两句处置完全相反的话,挑一句说出来就有一半概率把人送错方向。
func TestDominantRefusesAnExactTie(t *testing.T) {
	if kind, ok := Dominant(map[string]int64{Unreachable: 5, Timeout: 5}); ok {
		t.Errorf("5/10 被当成了主因(%q)—— 严格多数才算", kind)
	}
}

// 只有一类时它当然是主因。
func TestDominantAcceptsASingleKind(t *testing.T) {
	kind, ok := Dominant(map[string]int64{Timeout: 3})
	if !ok || kind != Timeout {
		t.Errorf("唯一的一类没被认成主因:kind=%q ok=%v", kind, ok)
	}
}

// 没有失败就没有判决。**空表与「各类都是 0」是同一个答案**,但都不许返回
// 一个看起来确定的 kind。
func TestDominantSaysNothingWithoutFailures(t *testing.T) {
	for name, kinds := range map[string]map[string]int64{
		"nil":  nil,
		"空表":   {},
		"全零":   {Timeout: 0, Unreachable: 0},
		"负数":   {Timeout: -3},
		"零值键名": {"": 9},
	} {
		if kind, ok := Dominant(kinds); ok {
			t.Errorf("%s 产出了主因 %q —— 没有失败的时候不该有判决", name, kind)
		}
	}
}

// declaredKinds 读本包源码里 Classify 会产出的那些字符串常量。
//
// **判据取 AST 不取记忆**:手抄一份清单正是这条守卫要消灭的东西。
// 读不出来时响亮失败 —— 一条安静地扫了零个常量的守卫,与没有这条守卫
// 在输出上完全一样,而它看起来更让人放心。
func declaredKinds(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dialfail.go", nil, 0)
	if err != nil {
		t.Fatalf("读不出 dialfail.go:%v", err)
	}
	var kinds []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				kinds = append(kinds, lit.Value[1:len(lit.Value)-1])
			}
		}
	}
	if len(kinds) == 0 {
		t.Fatal("一个字符串常量都没扫到 —— 守卫读不懂现在的代码了,这时候它必须响亮失败")
	}
	return kinds
}
