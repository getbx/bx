package supervisor

import (
	"runtime"
	"testing"
)

// 非 darwin 上整条链必须报「不支持」而不是空数据:空 map 会被上层读成
// 「查过了,一个应用都没有」,而那是句自洽的假话。
func TestAppSourceIsUnsupportedOffDarwin(t *testing.T) {
	owners, err := newAppSource().OwnersByPort()
	if runtime.GOOS == "darwin" {
		if err != nil {
			t.Fatalf("darwin 上应能取到端口映射: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("非 darwin 必须报错,不许返回空 map 冒充「查过了没有」")
	}
	if owners != nil {
		t.Fatalf("非 darwin 不许返回 map,得到 %v", owners)
	}
}
