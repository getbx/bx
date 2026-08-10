package guardian

import (
	"testing"

	"github.com/getbx/bx/internal/barriercidr"
)

// 屏障装的清单必须**就是** barriercidr 那一份,不是「碰巧一样」的第二份。
//
// 这条守的是下沉成叶子包之后新出现的一个单向缝隙:装屏障的 guardian 在包初始化时
// 取的是一份**副本**,而问内核「屏障在不在」的 internal/observe 直接读源头。于是往
// barrier.go 里手写第五条网段时,guardian 侧的期望值测试会照常转红逼你改对,
// **而观测侧对此一无所知、也不会有任何一条失败信息提到它** —— 屏障装了五条,
// 观测只去问四条,然后信心十足地回答「屏障不在」。
//
// 这正是把清单下沉到叶子包要消灭的那种分叉,只不过换了个方向。
func TestGuardianBlocksComeFromTheSharedSource(t *testing.T) {
	ipv4, ipv6 := barriercidr.Blocking()
	assertSameBlocks(t, "IPv4", publicIPv4Blocks, ipv4)
	assertSameBlocks(t, "IPv6", publicIPv6Blocks, ipv6)
}

func assertSameBlocks(t *testing.T, family string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s 网段数 = %d, want %d —— 屏障与观测层已经在看两份不同的清单:%v vs %v",
			family, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %q, want %q —— 屏障装的与观测层问的不是同一个网段",
				family, i, got[i], want[i])
		}
	}
}
