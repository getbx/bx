package supervisor

import (
	"testing"

	"github.com/getbx/bx/internal/tunnel"
)

func TestLiveTunnelSwap(t *testing.T) {
	// tunnel.New 仅存字段;不 Start 即可读 SocksAddr(确定值)/Stats(零值)/Healthy(false)。
	a := tunnel.New("127.0.0.1:1111", nil, nil)
	b := tunnel.New("127.0.0.1:2222", nil, nil)
	lt := &liveTunnel{}

	lt.set(a)
	if lt.get() != a {
		t.Fatal("set(a) 后 get 应为 a")
	}
	if lt.SocksAddr() != "127.0.0.1:1111" {
		t.Fatalf("SocksAddr 应委派到 a, got %q", lt.SocksAddr())
	}

	lt.set(b) // 原子替换
	if lt.get() != b {
		t.Fatal("set(b) 后 get 应为 b")
	}
	if lt.SocksAddr() != "127.0.0.1:2222" {
		t.Fatalf("替换后 SocksAddr 应委派到 b, got %q", lt.SocksAddr())
	}
	if lt.Healthy() {
		t.Error("未 Start 应不健康")
	}
	_ = lt.Stats() // 不 panic 即可(委派当前隧道)
}
