package supervisor

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// **只在明确观测到「出不去」时才动手改路由。**
//
// 「问不出来」时我们对这件事一无所知,而基于「不知道」去动真实网络,是拿一个
// 诊断功能去改系统 —— 与 Tristate 那条纪律同源。
func TestShouldRepairEgress(t *testing.T) {
	const never = time.Duration(-1)
	for _, tc := range []struct {
		name      string
		reachable bool
		known     bool
		since     time.Duration
		want      bool
	}{
		{"出不去、还没修过", false, true, never, true},
		{"出得去", true, true, never, false},
		{"问不出来", false, false, never, false},
		{"问不出来但看起来能通", true, false, never, false},
		{"刚修过,还在冷静期", false, true, time.Minute, false},
		{"冷静期到了", false, true, egressRepairCooldown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRepairEgress(tc.reachable, tc.known, tc.since); got != tc.want {
				t.Fatalf("shouldRepairEgress = %v, want %v", got, tc.want)
			}
		})
	}
}

// 循环在观测到「出不去」时重装一次。
func TestWatchRepairsWhenEgressIsDown(t *testing.T) {
	var repairs atomic.Int32
	tick := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		watchDirectEgress(ctx,
			func(context.Context) (bool, bool, error) { return false, true, nil },
			func(context.Context) error { repairs.Add(1); return nil },
			tick)
		close(done)
	}()
	tick <- time.Now()
	waitFor(t, func() bool { return repairs.Load() == 1 }, "没有重装那条路由")
	cancel()
	<-done
}

// **出得去时一个字都不动。** 这个循环在健康机器上一天跑 2880 次,
// 每次都去改一次路由是不可接受的。
func TestWatchNeverTouchesAHealthyMachine(t *testing.T) {
	var repairs atomic.Int32
	tick := make(chan time.Time, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go watchDirectEgress(ctx,
		func(context.Context) (bool, bool, error) { return true, true, nil },
		func(context.Context) error { repairs.Add(1); return nil },
		tick)
	for i := 0; i < 4; i++ {
		tick <- time.Now()
	}
	time.Sleep(50 * time.Millisecond)
	if repairs.Load() != 0 {
		t.Fatalf("健康机器上改了 %d 次路由", repairs.Load())
	}
}

// **「问不出来」不许触发改动。** 探不到物理网卡名时报 False 会让每台机器
// 都被反复改路由,而我们其实一无所知。
func TestWatchDoesNotActOnUnknown(t *testing.T) {
	for _, probe := range []egressProbe{
		func(context.Context) (bool, bool, error) { return false, false, nil },
		func(context.Context) (bool, bool, error) { return false, false, errors.New("route: not found") },
		// 报了错却仍声称知道答案 —— 不该发生,但要当作不知道。
		func(context.Context) (bool, bool, error) { return false, true, errors.New("boom") },
	} {
		var repairs atomic.Int32
		tick := make(chan time.Time, 2)
		ctx, cancel := context.WithCancel(context.Background())
		go watchDirectEgress(ctx, probe, func(context.Context) error { repairs.Add(1); return nil }, tick)
		tick <- time.Now()
		tick <- time.Now()
		time.Sleep(50 * time.Millisecond)
		cancel()
		if repairs.Load() != 0 {
			t.Errorf("「问不出来」却改了 %d 次路由", repairs.Load())
		}
	}
}

// 修不好时**不放弃,但也不每拍重试** —— 冷静期让日志与 fork 都保持稀疏,
// 而网络可能自己回来。
func TestWatchBacksOffButNeverGivesUp(t *testing.T) {
	var repairs atomic.Int32
	tick := make(chan time.Time, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go watchDirectEgress(ctx,
		func(context.Context) (bool, bool, error) { return false, true, nil },
		func(context.Context) error { repairs.Add(1); return errors.New("route add failed") },
		tick)
	for i := 0; i < 8; i++ {
		tick <- time.Now()
	}
	waitFor(t, func() bool { return repairs.Load() >= 1 }, "一次都没试")
	time.Sleep(50 * time.Millisecond)
	if got := repairs.Load(); got != 1 {
		t.Fatalf("八拍里试了 %d 次 —— 冷静期没生效,日志与 fork 都会刷屏", got)
	}
}

// nil 的探测或修复(非 darwin)时循环直接返回,不空转。
func TestWatchIsANoOpWithoutPrimitives(t *testing.T) {
	done := make(chan struct{})
	go func() {
		watchDirectEgress(context.Background(), nil, func(context.Context) error { return nil }, nil)
		watchDirectEgress(context.Background(), func(context.Context) (bool, bool, error) { return false, true, nil }, nil, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("没有原语时循环没有立刻返回 —— 它会拿着一个 nil channel 空转")
	}
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
	t.Fatal(msg)
}

// **「路由已经存在」是好消息,不是失败。**
//
// 多服务的 Mac 上系统自带一条同款;探测与修复之间路由也可能自己回来了。
// 把它当成失败,会让日志每 5 分钟报一次假警报,而路由其实是好的。
func TestEgressRepairOutcome(t *testing.T) {
	if err := egressRepairOutcome(nil, "", "en0", "192.168.1.1"); err != nil {
		t.Errorf("成功却报错:%v", err)
	}
	if err := egressRepairOutcome(errors.New("exit status 1"),
		"route: writing to routing socket: File exists\n", "en0", "192.168.1.1"); err != nil {
		t.Errorf("「已存在」被当成了失败:%v", err)
	}
	err := egressRepairOutcome(errors.New("exit status 1"),
		"route: writing to routing socket: Network is unreachable\n", "en0", "192.168.1.1")
	if err == nil {
		t.Fatal("真失败却报成功")
	}
	// 失败要带上够排查的上下文。
	for _, want := range []string{"en0", "192.168.1.1", "unreachable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误里没有 %q:%v", want, err)
		}
	}
}
