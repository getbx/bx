package supervisor

import "testing"

func TestAuthorizeMutation(t *testing.T) {
	cases := []struct {
		uid   uint32
		known bool
		want  bool
	}{
		{0, true, true},    // root 放行
		{1000, true, false}, // 非 root 拒绝
		{0, false, true},   // 平台无 peer-cred(darwin 开发态)宽松放行
	}
	for _, c := range cases {
		if got := authorizeMutation(c.uid, c.known); got != c.want {
			t.Fatalf("authorizeMutation(%d,%v)=%v want %v", c.uid, c.known, got, c.want)
		}
	}
}
