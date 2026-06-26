package tunnel

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRunner 可控的假子进程。
type fakeRunner struct {
	mu     sync.Mutex
	exitCh chan struct{}
	killed bool
}

func newFakeRunner() *fakeRunner { return &fakeRunner{exitCh: make(chan struct{})} }
func (f *fakeRunner) Wait() error {
	<-f.exitCh
	return nil
}
func (f *fakeRunner) Kill() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.killed {
		f.killed = true
		close(f.exitCh)
	}
	return nil
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timeout: " + msg)
}

func TestTunnelBecomesHealthy(t *testing.T) {
	r := newFakeRunner()
	tn := New("127.0.0.1:11080",
		func(string) (Runner, error) { return r, nil },
		func(string) (int64, error) { return 12, nil }, // 永远健康
	)
	tn.interval = 10 * time.Millisecond
	tn.Start()
	defer tn.Stop()
	waitFor(t, tn.Healthy, "应变健康")
	if tn.Stats().LatencyMS != 12 {
		t.Fatalf("延迟应为 12, got %d", tn.Stats().LatencyMS)
	}
}

func TestTunnelRestartsOnProcessExit(t *testing.T) {
	var n int
	var mu sync.Mutex
	first := newFakeRunner()
	tn := New("127.0.0.1:11080",
		func(string) (Runner, error) {
			mu.Lock()
			defer mu.Unlock()
			n++
			if n == 1 {
				return first, nil
			}
			return newFakeRunner(), nil // 第二次起一个新的
		},
		func(string) (int64, error) { return 5, nil },
	)
	tn.interval = 10 * time.Millisecond
	tn.base, tn.max = 5*time.Millisecond, 20*time.Millisecond
	tn.Start()
	defer tn.Stop()
	waitFor(t, tn.Healthy, "首次应健康")
	first.Kill() // 模拟进程退出
	waitFor(t, func() bool { return tn.Stats().Restarts >= 1 }, "应记录重启")
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return n >= 2 }, "应再次启动子进程")
}

func TestTunnelUnhealthyTriggersRestart(t *testing.T) {
	var healthy = true
	var hmu sync.Mutex
	setH := func(v bool) { hmu.Lock(); healthy = v; hmu.Unlock() }
	tn := New("127.0.0.1:11080",
		func(string) (Runner, error) { return newFakeRunner(), nil },
		func(string) (int64, error) {
			hmu.Lock()
			defer hmu.Unlock()
			if healthy {
				return 5, nil
			}
			return 0, errors.New("unhealthy")
		},
	)
	tn.interval = 10 * time.Millisecond
	tn.base, tn.max = 5*time.Millisecond, 20*time.Millisecond
	tn.Start()
	defer tn.Stop()
	waitFor(t, tn.Healthy, "首次应健康")
	setH(false)
	waitFor(t, func() bool { return !tn.Healthy() }, "不健康应反映到状态")
	waitFor(t, func() bool { return tn.Stats().Restarts >= 1 }, "不健康应触发重启")
}

// 瞬时健康抖动(连续失败 < maxFails)不应拆掉正在工作的隧道(高延迟 wss 上的关键修复)。
func TestTunnelToleratesTransientHealthBlips(t *testing.T) {
	var n int32
	tn := New("127.0.0.1:11080",
		func(string) (Runner, error) { return newFakeRunner(), nil },
		func(string) (int64, error) {
			// 第 1 次(startup)成功进入 monitor;接着第 2、3 次连续失败(2 < maxFails=3),其余成功。
			switch atomic.AddInt32(&n, 1) {
			case 2, 3:
				return 0, errors.New("blip")
			default:
				return 5, nil
			}
		},
	)
	tn.interval = 10 * time.Millisecond
	tn.base, tn.max = 5*time.Millisecond, 20*time.Millisecond
	tn.Start()
	defer tn.Stop()
	waitFor(t, tn.Healthy, "首次应健康")
	// 等若干个 tick 走过那两次抖动并恢复
	waitFor(t, func() bool { return atomic.LoadInt32(&n) >= 5 }, "应继续探测")
	if r := tn.Stats().Restarts; r != 0 {
		t.Fatalf("瞬时抖动(<maxFails)不该重连,实际重启 %d 次", r)
	}
	waitFor(t, tn.Healthy, "抖动后应恢复健康")
}

// 退避(reconnect backoff)应反映「连续」失败,而非隧道生命周期累计的重连次数。
// 一次曾健康的运行结束后退避必须归零 —— 否则长寿命隧道偶发抖动会被退避到 max,
// 配合 kill-switch 造成最长 max(默认 30s) 的全流量黑洞。
func TestTunnelBackoffResetsAfterHealthyRun(t *testing.T) {
	var mu sync.Mutex
	var starts []time.Time
	var probe int32
	tn := New("127.0.0.1:11080",
		func(string) (Runner, error) {
			mu.Lock()
			starts = append(starts, time.Now())
			mu.Unlock()
			return newFakeRunner(), nil
		},
		func(string) (int64, error) {
			// 每次运行:第 1 次探测成功(进入 monitor),随后连续 3 次失败(达到 maxFails)触发重连。
			// 即「健康一阵后死掉」反复循环 —— 每次运行都曾健康过。
			if (atomic.AddInt32(&probe, 1)-1)%4 == 0 {
				return 5, nil
			}
			return 0, errors.New("die")
		},
	)
	tn.probe = 1 * time.Millisecond
	tn.interval = 1 * time.Millisecond
	tn.base, tn.max = 20*time.Millisecond, 5*time.Second
	tn.Start()
	defer tn.Stop()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(starts) >= 7 }, "应多次重连")
	mu.Lock()
	defer mu.Unlock()
	last := starts[len(starts)-1].Sub(starts[len(starts)-2])
	if last > 200*time.Millisecond {
		t.Fatalf("健康运行后退避应归零,末次重连间隔=%v 远超 base(20ms),说明 attempt 未重置 → kill-switch 黑洞", last)
	}
}
