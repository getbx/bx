package supervisor

import (
	"errors"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/elevate"
)

var errSwitchTestBoom = errors.New("dial /run/bx/core.sock: connection refused")

// 四种结局要分得开。今天 Guardian 对四种都回同一个常量,于是菜单对
// 「已生效但确认失败」说「没切过去」(错的),对「回滚也失败了」轻描淡写一次断网。
func TestSwitchServerOutcomesAreDistinguishable(t *testing.T) {
	okArm := func(link, udp string) error { return nil }
	failArm := func(link, udp string) error { return errSwitchTestBoom }
	healthy := func() bool { return true }
	unhealthy := func() bool { return false }
	okRollback := func() error { return nil }
	failRollback := func() error { return errSwitchTestBoom }
	failCommit := func() error { return errSwitchTestBoom }
	neverCalled := func() bool { t.Fatal("不该问健康,却问了"); return false }
	neverRollback := func() error { t.Fatal("不该回滚,却回滚了"); return nil }
	neverCommit := func() error { t.Fatal("不该确认,却确认了"); return nil }

	for _, tc := range []struct {
		name string
		deps SwitchDeps
		want error
	}{
		{"武装失败", SwitchDeps{Arm: failArm, Healthy: neverCalled, Rollback: neverRollback, Commit: neverCommit}, ErrSwitchArmFailed},
		{"不健康已回滚", SwitchDeps{Arm: okArm, Healthy: unhealthy, Rollback: okRollback, Commit: neverCommit}, ErrSwitchRolledBack},
		{"不健康且回滚失败", SwitchDeps{Arm: okArm, Healthy: unhealthy, Rollback: failRollback, Commit: neverCommit}, ErrSwitchRollbackFailed},
		{"已生效但确认失败", SwitchDeps{Arm: okArm, Healthy: healthy, Rollback: neverRollback, Commit: failCommit}, ErrSwitchCommitFailed},
	} {
		err := SwitchServer(tc.deps, "vps", "link", "")
		if !errors.Is(err, tc.want) {
			t.Errorf("%s:errors.Is 认不出 %v,拿到 %v", tc.name, tc.want, err)
		}
	}
}

// 哨兵是**贴在原话外面的标签**,不是新的措辞。
//
// `bx server use` 打的是 `%v` 的完整原话,而它今天比 GUI 诚实 —— 这四句是
// 人在终端里唯一读得到的解释。把哨兵的文字印进去(比如在 fmt.Errorf 里
// 多写一个 %w)会悄悄改掉它们,而没有任何东西会报错。
func TestSwitchServerKeepsTheHumanWordingIntact(t *testing.T) {
	okArm := func(link, udp string) error { return nil }
	for _, tc := range []struct {
		name string
		deps SwitchDeps
		want string
		deny string
	}{
		{
			"武装失败",
			SwitchDeps{Arm: func(link, udp string) error { return errSwitchTestBoom }},
			"switching to vps failed (nothing took effect, you are still on the previous server): dial /run/bx/core.sock: connection refused",
			"",
		},
		{
			"不健康已回滚",
			SwitchDeps{Arm: okArm, Healthy: func() bool { return false }, Rollback: func() error { return nil }},
			"switching to vps could not bring the tunnel up; it was ROLLED BACK to the previous server",
			"",
		},
		{
			"不健康且回滚失败",
			SwitchDeps{Arm: okArm, Healthy: func() bool { return false }, Rollback: func() error { return errSwitchTestBoom }},
			"switching to vps left the tunnel unhealthy, and the rollback failed too (dial /run/bx/core.sock: connection refused) — " +
				"the dead-man timer will still restore everything when it fires, or you can run " + elevate.Prefix + "bx down && " + elevate.Prefix + "bx up",
			"",
		},
		{
			"已生效但确认失败",
			SwitchDeps{Arm: okArm, Healthy: func() bool { return true }, Commit: func() error { return errSwitchTestBoom }},
			"switching to vps took effect but could not be confirmed (dial /run/bx/core.sock: connection refused) — the dead-man timer may undo it when it fires, so " +
				"run " + elevate.Prefix + "bx down && " + elevate.Prefix + "bx up to make the choice in the config take hold",
			"",
		},
	} {
		err := SwitchServer(tc.deps, "vps", "link", "")
		if err == nil {
			t.Fatalf("%s:没报错", tc.name)
		}
		if got := err.Error(); got != tc.want {
			t.Errorf("%s:措辞变了\n拿到 %q\n想要 %q", tc.name, got, tc.want)
		}
		// 哨兵的文字一个字都不许出现在人看的那句话里。
		for _, sentinel := range []error{
			ErrSwitchArmFailed, ErrSwitchRolledBack, ErrSwitchRollbackFailed, ErrSwitchCommitFailed,
		} {
			if strings.Contains(err.Error(), sentinel.Error()) {
				t.Errorf("%s:哨兵的文字印进了原话:%q", tc.name, err.Error())
			}
		}
	}
}

// 一次成功的切换不许带任何哨兵 —— 否则 Guardian 那边的 errors.Is 会给
// 一次成功贴上失败的码。
func TestSwitchServerSucceedsWithoutASentinel(t *testing.T) {
	err := SwitchServer(SwitchDeps{
		Arm:      func(link, udp string) error { return nil },
		Healthy:  func() bool { return true },
		Commit:   func() error { return nil },
		Rollback: func() error { t.Fatal("成功路径不该回滚"); return nil },
	}, "vps", "link", "")
	if err != nil {
		t.Fatalf("成功却报错:%v", err)
	}
}
