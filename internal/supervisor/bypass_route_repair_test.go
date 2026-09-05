package supervisor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getbx/bx/internal/confirm"
)

// 真机 2026-09-04:电量耗尽休眠 4.5 小时,按电源键唤醒。en0 重新关联时 macOS
// 把挂在它网关上的路由全冲掉了 —— 包括 bx 装的服务器 /32 旁路 —— 而 utun9 上
// 的两条 /1 劫持路由不挂在 en0,照旧活着。于是 sing-box 到 VPS 的连接进了 TUN,
// 被 bx 当成一条普通公网连接:隧道不健康 → kill-switch 拦下 → sing-box 在 2 毫秒内
// 收到 EOF(VPS 在 300 毫秒之外,真正的 EOF 不可能 2 毫秒回来)。隧道自己的重连
// 永远走这条坏路;同一个 Wi-Fi,generation 没变,路径恢复的边沿触发不响;唯一的
// 自愈循环(direct_egress)只管 scoped 默认路由,17:51:22 修好了它,却对 /32 一无所知。
// 13 分钟后用户 down/up 才救回来。
//
// 修法照抄 direct_egress:**去问内核**「发往服务器的包此刻走哪个接口」,走的是
// 我们自己的 TUN 就是成环,重新落实路由。

func TestDecideServerBypassIntact(t *testing.T) {
	boom := errors.New("route: exec failed")
	cases := []struct {
		name    string
		lookups []routeLookup
		intact  bool
		known   bool
		wantErr bool
	}{
		{"全部走物理网卡", []routeLookup{{Addr: "203.0.113.92", Interface: "en0"}}, true, true, false},
		{"有一台走了我们的 TUN = 成环", []routeLookup{{Addr: "1.2.3.4", Interface: "en0"}, {Addr: "203.0.113.92", Interface: "utun9"}}, false, true, false},
		{"问不出来不等于坏了", []routeLookup{{Addr: "203.0.113.92", Err: boom}}, false, false, true},
		{"没有服务器可查 = 无从判断", nil, false, false, false},
	}
	for _, c := range cases {
		intact, known, err := decideServerBypassIntact("utun9", c.lookups)
		if intact != c.intact || known != c.known || (err != nil) != c.wantErr {
			t.Errorf("%s: got intact=%v known=%v err=%v", c.name, intact, known, err)
		}
	}
}

// 环路一旦看见就修:探测说「走了 TUN」,循环必须调用修复。
func TestWatchServerBypassRoutesRepairsALoop(t *testing.T) {
	var repairs atomic.Int32
	tick := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		watchServerBypassRoutes(ctx,
			func(context.Context) (bool, bool, error) { return false, true, nil },
			func(context.Context) error { repairs.Add(1); return nil },
			tick)
		close(done)
	}()
	tick <- time.Now()
	waitFor(t, func() bool { return repairs.Load() == 1 }, "看见成环却没有重新落实路由")
	cancel()
	<-done
}

// 修复走控制面那把锁:有待确认的改动时让路;否则 Rehijack 的 apply 必须真的被调。
func TestReassertRoutesAppliesRehijackUnderTheControlLock(t *testing.T) {
	applied := 0
	rec := &recordingMutator{onRehijack: func() { applied++ }}
	cs := newTestControlServer(t, rec)
	if err := cs.reassertRoutes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("Rehijack apply 被调了 %d 次,want 1", applied)
	}
}

func TestReassertRoutesYieldsToAnArmedMutation(t *testing.T) {
	rec := &recordingMutator{onRehijack: func() { t.Error("armed 期间动了路由") }}
	cs := newTestControlServer(t, rec)
	cs.eng.(*fakeControlEngine).state = confirm.StateArmed
	if err := cs.reassertRoutes(context.Background()); err != nil {
		t.Fatalf("让路不该报错: %v", err)
	}
}

func TestReassertRoutesReportsARehijackFailure(t *testing.T) {
	boom := errors.New("route add failed")
	rec := &recordingMutator{rehijackErr: boom}
	cs := newTestControlServer(t, rec)
	if err := cs.reassertRoutes(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}
