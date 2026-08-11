//go:build !darwin

package guardian

import "testing"

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
