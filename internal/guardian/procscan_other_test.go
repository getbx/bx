//go:build !darwin

package guardian

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// 非 darwin 上这扇门是**焊死**的:scanRunningCores 恒返回错误 ⇒
// confirmNoCoreForRelease 恒「没能确认」⇒ 重新求证在这些平台上是 no-op,
// 锁存永远不会被它释放。谁要把 Guardian 移植过来,必须先实现
// scanRunningCores —— 否则得到的不是「行为不变」,而是一个永远解不开的锁存。
func TestCoreScanIsUnsupportedOffDarwin(t *testing.T) {
	cores, err := scanRunningCores(coreScanLifecycle)
	if err == nil {
		t.Fatal("非 darwin 的进程扫描桩必须报错 —— 它一旦「成功」返回空,重新求证就会在一个从没查过的平台上放行")
	}
	if len(cores) != 0 {
		t.Fatalf("桩返回了 %d 个进程", len(cores))
	}
}

// 上一条钉的是桩本身;这一条把桩接到**真实的 ExecCoreRunner** 上,再走一次
// 用户发起的 Up —— 也就是本期唯一放宽准入的那条路。
//
// 它守的是移植者最容易误读的一句话:「重新求证只是每次多问一遍系统」。在没有
// scanRunningCores 实现的平台上,那一问恒返回错误,而「问不出来」一律保持拒绝 ——
// 所以这条路在这里**一个 Core 都不会放行**,门与改动之前一样焊死。谁把
// requireDaemonPlatform 放开却没实现 scanRunningCores,得到的不是「行为不变」,
// 而是一个 bx up 永远起不来的 Guardian。
//
// 刻意用真 runner 而不是替身:替身自带一个「干净机器」的答案,那恰好绕开了
// 这条测试唯一想证明的东西。
func TestUserInitiatedUpStaysWeldedShutOffDarwin(t *testing.T) {
	env := newManagerTestEnv(t)
	runner := NewExecCoreRunner(filepath.Join(t.TempDir(), "bx"), filepath.Join(t.TempDir(), "config.yaml"), "127.0.0.1:53")
	// 拒绝发生在读记录之前,这里改路径是为了万一将来顺序变了也绝不去碰
	// 真机上的 /var/lib/bx。
	runner.StatePath = filepath.Join(t.TempDir(), "core-process.json")
	env.manager.runner = runner
	env.manager.current = Process{PID: 4242, Uncertain: true}

	if err := env.manager.Up(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Up = %v, want 仍然拒绝(这个平台上扫描恒失败)", err)
	}
	if !env.manager.current.Uncertain {
		t.Fatal("一个从没实现过的扫描把锁存清掉了 —— 那是在一个从没查过的平台上放行")
	}
}
