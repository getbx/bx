package pfreset

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

type fakeDriver struct {
	calls   []string
	loadErr error
}

func (f *fakeDriver) Enable(context.Context) (string, error) {
	f.calls = append(f.calls, "enable")
	return "tok", nil
}

func (f *fakeDriver) Load(_ context.Context, rules string) error {
	f.calls = append(f.calls, "load")
	return f.loadErr
}
func (f *fakeDriver) Flush(context.Context) error { f.calls = append(f.calls, "flush"); return nil }
func (f *fakeDriver) Release(_ context.Context, token string) error {
	f.calls = append(f.calls, "release:"+token)
	return nil
}

func opts(observe func() int, tick <-chan time.Time) Options {
	return Options{
		Device: "en0", RoutedAround: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		Observe: func() (int, bool) { return observe(), true }, Tick: tick, Deadline: 10 * time.Second, Log: func(string, ...any) {},
	}
}

// 「问不出来」不是 0(复审第 2 条):第一次就读不到 socket 表 ⇒ 不碰 pf、报错;循环中途读
// 不到 ⇒ 不许走「清零」那条路,继续等到期,拆的动作照做。
func TestRunNeverTreatsAnUnreadableSocketTableAsZero(t *testing.T) {
	d := &fakeDriver{}
	o := opts(func() int { return 0 }, nil)
	o.Observe = func() (int, bool) { return 0, false }
	out := Run(context.Background(), d, o)
	if out.Attempted || len(d.calls) != 0 || out.Err == nil {
		t.Fatalf("an unreadable table must touch nothing and say so, got %+v calls %v", out, d.calls)
	}

	d = &fakeDriver{}
	tick := make(chan time.Time, 2)
	tick <- time.Time{}
	tick <- time.Time{}
	calls := 0
	o = opts(func() int { return 0 }, tick)
	o.Deadline = 20 * time.Millisecond
	o.Observe = func() (int, bool) {
		calls++
		if calls == 1 {
			return 2, true
		}
		return 0, false // 之后每次都读不到
	}
	out = Run(context.Background(), d, o)
	if out.Remaining != 2 || out.Err == nil || d.calls[len(d.calls)-1] != "release:tok" {
		t.Fatalf("unreadable mid-loop must not claim done; remaining stays 2, err says so, teardown runs: %+v calls %v", out, d.calls)
	}
}

// 常态:没有残留连接,pf 一个字都不碰。
func TestRunTouchesNothingWhenNothingIsStray(t *testing.T) {
	d := &fakeDriver{}
	out := Run(context.Background(), d, opts(func() int { return 0 }, nil))
	if out.Attempted || len(d.calls) != 0 {
		t.Fatalf("no stray connections must mean no pf calls, got %v / %+v", d.calls, out)
	}
}

// 有残留:enable → load → 每 tick 看一次 → 清零即 flush + release。
func TestRunStopsTheMomentTheStrayCountReachesZero(t *testing.T) {
	d := &fakeDriver{}
	tick := make(chan time.Time, 3)
	counts := []int{3, 1, 0}
	i := 0
	observe := func() int {
		c := counts[i]
		if i < len(counts)-1 {
			i++
		}
		return c
	}
	for range counts {
		tick <- time.Time{}
	}
	out := Run(context.Background(), d, opts(observe, tick))
	want := []string{"enable", "load", "flush", "release:tok"}
	if !out.Attempted || out.Initial != 3 || out.Remaining != 0 || out.Err != nil {
		t.Fatalf("outcome = %+v", out)
	}
	if len(d.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", d.calls, want)
	}
	for j := range want {
		if d.calls[j] != want[j] {
			t.Fatalf("calls = %v, want %v", d.calls, want)
		}
	}
}

// ctx 被取消(用户几秒内 bx down):拆的动作照做。
func TestRunTearsDownWhenTheContextIsCancelled(t *testing.T) {
	d := &fakeDriver{}
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	out := Run(ctx, d, opts(func() int { return 2 }, tick))
	if len(d.calls) < 2 || d.calls[len(d.calls)-1] != "release:tok" || d.calls[len(d.calls)-2] != "flush" {
		t.Fatalf("cancelled run must still flush and release, calls = %v", d.calls)
	}
	if out.Remaining != 2 {
		t.Fatalf("remaining must report what was left, got %+v", out)
	}
}

// 装规则失败:仍要 release(引用计数是我们加的)。
func TestRunReleasesTheReferenceWhenLoadingFails(t *testing.T) {
	d := &fakeDriver{loadErr: errors.New("syntax error")}
	out := Run(context.Background(), d, opts(func() int { return 1 }, nil))
	if out.Err == nil || d.calls[len(d.calls)-1] != "release:tok" {
		t.Fatalf("a failed load must still release pf, calls = %v err = %v", d.calls, out.Err)
	}
}

// 封顶:tick 一直不清零,到 Deadline 就拆,Remaining 报还剩几条(交给两段式去点名)。
func TestRunGivesUpAtTheDeadline(t *testing.T) {
	d := &fakeDriver{}
	tick := make(chan time.Time)
	o := opts(func() int { return 2 }, tick)
	o.Deadline = 20 * time.Millisecond
	out := Run(context.Background(), d, o)
	if out.Remaining != 2 || d.calls[len(d.calls)-1] != "release:tok" {
		t.Fatalf("deadline must tear down and report leftovers, got %+v calls %v", out, d.calls)
	}
}
