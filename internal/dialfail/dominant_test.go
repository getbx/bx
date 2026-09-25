package dialfail

import "testing"

// —— 这一族测试守的是「一个百分比答不出该不该管」的下半句 ——
//
// 上半句(分类)2026-09-01 就做完了:`*.qq.com 15.4% 失败` 现在会拆成
// `[路由不可达×410 对端不应答×15]`。**处置**(每一类指向谁、该做什么)印在
// `bx explain` 的 Blame 那一行,措辞住在 internal/cli 的 explainVerdictText,
// 逐类穷举的守卫也在那边(TestEveryDialfailKindGetsADeliberateVerdict)。
//
// 这里曾经有过一张 Blame 四态表(BlameFor)与它的穷举守卫 —— 它替代的
// LooksLikeOurFault 是个零生产调用方的 bool,而它自己**也**从没有生产调用方:
// explain 的措辞一直是逐类写的,从没问过它。2026-09-25 删掉,守卫搬到了真正
// 印给用户看的那个函数上。

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
