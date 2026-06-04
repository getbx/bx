package tunnel

import (
	"testing"
	"time"
)

func TestPickFreePort(t *testing.T) {
	p, err := pickFreePort()
	if err != nil || p <= 0 || p > 65535 {
		t.Fatalf("pickFreePort=%d err=%v", p, err)
	}
}

func TestBackoff(t *testing.T) {
	base, max := 1*time.Second, 30*time.Second
	if backoff(0, base, max) != base {
		t.Fatal("attempt 0 应等于 base")
	}
	if backoff(2, base, max) != 4*time.Second {
		t.Fatalf("attempt 2 应为 4s, got %v", backoff(2, base, max))
	}
	if backoff(100, base, max) != max {
		t.Fatal("大 attempt 应封顶到 max")
	}
}
