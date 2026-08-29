package guardian

import "testing"

// 平台清单不许有洞:每个字段都必须被本 OS 接线(接到真实现或 fail-closed stub
// 都行,nil 不行——nil 在使用点是 panic,而 panic 在 daemon 里是崩溃循环)。
// 这条测试在 CI 的 ubuntu/macos/windows 三条腿上各跑一遍,分别证明三个 OS
// 的清单完整;新加字段忘了某个平台,先红的是这里,不是生产的 nil deref。
func TestLifecyclePlatformHasNoHoles(t *testing.T) {
	if err := newLifecyclePlatform().validate(); err != nil {
		t.Fatalf("平台清单有洞: %v", err)
	}
}
