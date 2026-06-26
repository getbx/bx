package supervisor

import "testing"

func TestAuthorizeMutation(t *testing.T) {
	cases := []struct {
		name   string
		uid    uint32
		gotUID bool
		want   bool
	}{
		{"linux-root", 0, true, true},              // 提取成功且 root → 放行
		{"linux-nonroot", 1000, true, false},        // 提取成功但非 root → 拒
		{"linux-extract-failed", 0, false, false},   // 提取失败(uid 不可信)→ 拒(fail-closed,本次核心)
		{"no-peercred", 1000, false, false},         // darwin/拿不到 → 拒(uid 被忽略)
	}
	for _, c := range cases {
		if got := authorizeMutation(c.uid, c.gotUID); got != c.want {
			t.Errorf("%s: authorizeMutation(%d,%v)=%v want %v", c.name, c.uid, c.gotUID, got, c.want)
		}
	}
}
