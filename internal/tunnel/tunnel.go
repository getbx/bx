package tunnel

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	errProcessExited = errors.New("传输进程退出")
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
	LastError string
}

// Tunnel 监督 brook 子进程:健康检查 + 自动重连。
type Tunnel struct {
	socksAddr string
	factory   RunnerFactory
	health    HealthCheck
	interval  time.Duration
	probe     time.Duration // 启动期健康探测间隔(进程刚拉起到首次健康之间)
	base, max time.Duration
	maxFails  int // 连续健康检查失败多少次才判定隧道挂掉并重连(容忍瞬时探测抖动)

	mu       sync.Mutex
	up       bool
	lat      int64
	restarts int
	lastErr  string
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
		probe:     200 * time.Millisecond,
		base:      1 * time.Second,
		max:       30 * time.Second,
		maxFails:  3, // 一次探测抖动(高延迟 wss 上常见)不该拆掉正在工作的隧道;连续 3 次(~15s)才重连
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
	return Stats{Up: t.up, LatencyMS: t.lat, Restarts: t.restarts, LastError: t.lastErr}
}

func (t *Tunnel) setUp(lat int64) {
	t.mu.Lock()
	t.up, t.lat, t.lastErr = true, lat, ""
	t.mu.Unlock()
}

func (t *Tunnel) setDown(err error) {
	t.mu.Lock()
	t.up = false
	if err != nil {
		t.lastErr = err.Error()
	}
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
		healthy, err := t.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			t.incRestart()
		}
		// 本次运行曾成功带起隧道 → 退避归零。退避只针对「起不来」的连续失败,
		// 不随隧道生命周期累计,避免长寿命隧道偶发重连被退避到 max 而触发 kill-switch 黑洞。
		if healthy {
			attempt = 0
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
// 返回值 healthy 表示本次运行是否曾进入健康(monitor)状态 —— 供退避归零判定。
func (t *Tunnel) runOnce(ctx context.Context) (healthy bool, _ error) {
	r, err := t.factory(t.socksAddr)
	if err != nil {
		t.setDown(err)
		return false, err
	}
	t.mu.Lock()
	t.runner = r
	t.mu.Unlock()
	defer r.Kill()

	exitCh := make(chan struct{})
	go func() { r.Wait(); close(exitCh) }()

	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	startup := time.NewTimer(10 * time.Second)
	defer startup.Stop()
	startupTick := time.NewTicker(t.probe)
	defer startupTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-exitCh:
			t.setDown(errProcessExited)
			return false, errProcessExited
		case <-startup.C:
			t.setDown(errUnhealthy)
			return false, errUnhealthy
		case <-startupTick.C:
			lat, herr := t.health(t.socksAddr)
			if herr == nil {
				t.setUp(lat)
				goto monitor
			}
			t.setDown(herr)
		}
	}

monitor:
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-exitCh:
			t.setDown(errProcessExited)
			return true, errProcessExited
		case <-ticker.C:
			lat, herr := t.health(t.socksAddr)
			if herr != nil {
				// 容忍瞬时探测失败:高延迟 wss 上单次探测偶尔超时,但隧道(及 tun)仍在工作。
				// 一次失败就 Kill+重连会拆掉正常连接,造成秒级断流抖动。连续 maxFails 次才重连。
				fails++
				if fails >= t.maxFails {
					t.setDown(herr)
					return true, errUnhealthy
				}
				continue
			}
			fails = 0
			t.setUp(lat)
		}
	}
}
