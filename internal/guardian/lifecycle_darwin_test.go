//go:build darwin

package guardian

import "testing"

// darwin 清单接的必须是放行的门——这不是重复 daemon_darwin.go 的测试,
// 是钉住「bundle 字段指向的确实是本平台的那份实现」:把字段接错到 stub 上,
// 编译器不会抗议(签名相同),只有行为测试抓得到。
func TestLifecyclePlatformAllowsDaemonOnDarwin(t *testing.T) {
	if err := newLifecyclePlatform().RequireDaemon(); err != nil {
		t.Fatalf("darwin 上 RequireDaemon 必须放行,got %v", err)
	}
}
