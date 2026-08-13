package leakcheck

import (
	"testing"

	"github.com/getbx/bx/internal/guardian"
)

// **判据里的保护状态字面量必须与 Guardian 的常量对得上。**
//
// 本包是纯判据包(见 purity_test.go),不许在**生产代码**里引 guardian ——
// 那个包做 I/O。代价是那几个状态名只能写成字面量,而字面量会悄悄漂:
// Guardian 改一个常量的值,这里不报错、不转红,只是从此再也匹配不上,
// 于是 bxRunningVerdict 对每一台机器都返回 runningUnknown,整条检查静默失效。
//
// purity 守卫跳过 _test.go,所以这条一致性检查可以引 guardian。
// **它钉的是「白名单覆盖了哪些状态」,不是「白名单里有几个」** —— Guardian 新增
// 一个状态时这条会红,而那正是需要有人来决定它算不算「在跑」的时刻。
func TestProtectionStateLiteralsMatchGuardian(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  runningState
		why   string
	}{
		{guardian.ProtectionProtected, isRunning, "保护开着,流量就该走 bx"},
		{guardian.ProtectionBlocked, isRunning, "kill-switch 生效,此刻更不该有公网流量漏出去"},
		{guardian.ProtectionNeedsAttention, isRunning, "保护本该开着,只是出了问题"},
		{guardian.ProtectionOff, notRunning, "用户显式关掉了"},
		{guardian.ProtectionStarting, runningUnknown, "过渡态,几秒后自己会变"},
		{guardian.ProtectionRecovering, runningUnknown, "过渡态"},
	} {
		if got := bxRunningVerdict(LocalFacts{BXProtection: tc.state}); got != tc.want {
			t.Errorf("guardian 状态 %q → %v,want %v(%s)", tc.state, got, tc.want, tc.why)
		}
	}
}
