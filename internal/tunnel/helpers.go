package tunnel

import (
	"net"
	"time"
)

// pickFreePort 让内核分配一个空闲的本地 TCP 端口。
func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// backoff 指数退避:base<<attempt,封顶 max,溢出也归 max。
func backoff(attempt int, base, max time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 62 {
		return max
	}
	d := base << uint(attempt)
	if d <= 0 || d > max {
		return max
	}
	return d
}
