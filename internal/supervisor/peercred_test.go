package supervisor

import "testing"

func TestAuthorizeMutation(t *testing.T) {
	cases := []struct {
		name   string
		uid    uint32
		gotUID bool
		owner  uint32
		want   bool
	}{
		{"root-with-owner", 0, true, 1000, true},   // root 永远放行
		{"root-no-owner", 0, true, 0, true},        // 无业主时 root 仍放行
		{"owner", 1000, true, 1000, true},          // 业主放行(本片核心)
		{"nonroot-no-owner", 1000, true, 0, false}, // 无业主 → 退回 root-only(核心)
		{"other-user", 1001, true, 1000, false},    // 非 root 非业主 → 拒
		{"extract-failed", 0, false, 1000, false},  // 取 uid 失败 → fail-closed
	}
	for _, c := range cases {
		if got := authorizeMutation(c.uid, c.gotUID, c.owner); got != c.want {
			t.Errorf("%s: authorizeMutation(%d,%v,%d)=%v want %v", c.name, c.uid, c.gotUID, c.owner, got, c.want)
		}
	}
}
