//go:build linux

package supervisor

import "testing"

// `oif <dev>` 必须在实参里:少了它这条查询就退化成普通路由,而本机视角的整个
// 意义就是「绑网卡的 socket 看到的表」与普通表的差别。
func TestBoundRouteArgsScopeToTheDevice(t *testing.T) {
	got := boundRouteArgs("eno1", "192.0.2.185")
	want := []string{"route", "get", "192.0.2.185", "oif", "eno1"}
	if len(got) != len(want) {
		t.Fatalf("args = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}
}
