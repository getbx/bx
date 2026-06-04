package tunnel

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	errProcessExited = errors.New("brook 进程退出")
	errUnhealthy     = errors.New("健康检查失败")
)

// Runner 抽象一个运行中的子进程。
type Runner interface {
	Wait() error // 阻塞直到进程退出
	Kill() error
}

// RunnerFactory 在 socksAddr 上启动 brook socks5 子进程。
type RunnerFactory func(socksAddr string) (Runner, error)

// HealthCheck 经 socks5 测连通,返回延迟(毫秒)或错误。
type HealthCheck func(socksAddr string) (latencyMS int64, err error)

// Stats 是隧道当前状态快照。
type Stats struct {
	Up        bool
	LatencyMS int64
	Restarts  int
}

// Tunnel 监督 brook 子进程:健康检查 + 自动重连。
type Tunnel struct {
	socksAddr string
	factory   RunnerFactory
	health    HealthCheck
	interval  time.Duration
	base, max time.Duration

	mu       sync.Mutex
	up       bool
	lat      int64
	restarts int
	runner   Runner

	cancel context.CancelFunc
	done   chan struct{}
}

// New 构造一个隧道(默认参数;测试可覆盖 interval/base/max)。
func New(socksAddr string, f RunnerFactory, h HealthCheck) *Tunnel {
	return &Tunnel{
		socksAddr: socksAddr,
		factory:   f,
		health:    h,
		interval:  5 * time.Second,
		base:      1 * time.Second,
		max:       30 * time.Second,
		done:      make(chan struct{}),
	}
}

// SocksAddr 返回本地 socks5 监听地址 "127.0.0.1:N"。
func (t *Tunnel) SocksAddr() string { return t.socksAddr }

// Healthy 当前隧道是否健康。
func (t *Tunnel) Healthy() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.up
}

// Stats 返回状态快照。
func (t *Tunnel) Stats() Stats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Stats{Up: t.up, LatencyMS: t.lat, Restarts: t.restarts}
}

func (t *Tunnel) setUp(lat int64) {
	t.mu.Lock()
	t.up, t.lat = true, lat
	t.mu.Unlock()
}

func (t *Tunnel) setDown() {
	t.mu.Lock()
	t.up = false
	t.mu.Unlock()
}

func (t *Tunnel) incRestart() {
	t.mu.Lock()
	t.restarts++
	t.mu.Unlock()
}

// Start 启动监督 goroutine。
func (t *Tunnel) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	go func() {
		defer close(t.done)
		t.supervise(ctx)
	}()
}

// Stop 停止监督并杀掉子进程,阻塞到清理完成。
func (t *Tunnel) Stop() {
	if t.cancel != nil {
		t.cancel()
	}
	t.mu.Lock()
	r := t.runner
	t.mu.Unlock()
	if r != nil {
		r.Kill()
	}
	<-t.done
}

func (t *Tunnel) supervise(ctx context.Context) {
	attempt := 0
	for ctx.Err() == nil {
		err := t.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			t.incRestart()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff(attempt, t.base, t.max)):
		}
		attempt++
	}
}

// runOnce 启动一次子进程并监控,直到进程退出/不健康/ctx 取消才返回。
func (t *Tunnel) runOnce(ctx context.Context) error {
	r, err := t.factory(t.socksAddr)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.runner = r
	t.mu.Unlock()
	defer r.Kill()

	exitCh := make(chan struct{})
	go func() { r.Wait(); close(exitCh) }()

	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exitCh:
			t.setDown()
			return errProcessExited
		case <-ticker.C:
			lat, herr := t.health(t.socksAddr)
			if herr != nil {
				t.setDown()
				return errUnhealthy
			}
			t.setUp(lat)
		}
	}
}
