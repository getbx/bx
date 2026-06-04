package tunnel

import (
	"errors"
	"sync"
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
