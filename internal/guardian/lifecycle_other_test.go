//go:build !darwin && !linux

package guardian

import (
	"context"
	"errors"
	"testing"
)

// !darwin 的清单必须保持 fail-closed:门是关的、屏障是 unsupported。
// 「谁移植 Guardian 必须先实现 scanRunningCores,不能只放开 requireDaemonPlatform」
// ——本清单不许成为绕开那条纪律的新入口。
func TestLifecyclePlatformStaysFailClosedOffDarwin(t *testing.T) {
	p := newLifecyclePlatform()
	if err := p.RequireDaemon(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("!darwin 上 RequireDaemon 必须拒绝,got %v", err)
	}
	if err := p.NewBarrier(nil).Install(context.Background(), BarrierContext{}); err == nil {
		t.Fatal("!darwin 屏障必须 fail-closed 拒绝安装")
	}
}
