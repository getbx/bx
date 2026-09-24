package supervisor

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getbx/bx/internal/confirm"
)

// —— 路由就绪位自愈(known-gaps B2,2026-09-23)——
//
// 一次拆到一半才失败的 rehijack 把 RoutesInstalled 永久清成 false,此后没有任何东西
// 会把它设回来:路径恢复的 verify 验不过、Guardian 的 health 门拦住升级,直到 Core
// 重启。修法**不是**「去内核看一眼、看着没问题就置真」(那是凭观测放行),而是
// **重新完整装一遍路由**:就绪位只在一次真的成功安装之后才回到 true。

func TestReinstallRoutesIfNotReadyLeavesReadyRoutesAlone(t *testing.T) {
	rec := &recordingMutator{onRehijack: func() { t.Error("路由已就绪,却又装了一遍") }}
	cs := newTestControlServer(t, rec)
	err := cs.reinstallRoutesIfNotReady(context.Background(), func() bool { return true })
	if !errors.Is(err, errRoutesAlreadyReady) {
		t.Fatalf("err = %v, want errRoutesAlreadyReady(不许报成「重装成功」)", err)
	}
}

func TestReinstallRoutesIfNotReadyYieldsToAnArmedMutation(t *testing.T) {
	rec := &recordingMutator{onRehijack: func() { t.Error("有待确认的改动时动了路由") }}
	cs := newTestControlServer(t, rec)
	cs.eng.(*fakeControlEngine).state = confirm.StateArmed
	err := cs.reinstallRoutesIfNotReady(context.Background(), func() bool { return false })
	if !errors.Is(err, errRouteRepairDeferred) {
		t.Fatalf("err = %v, want errRouteRepairDeferred —— 让路必须说出来,不许报成「重装成功」", err)
	}
}

func TestReinstallRoutesIfNotReadyReinstalls(t *testing.T) {
	var ready atomic.Bool
	applied := 0
	rec := &recordingMutator{onRehijack: func() { applied++; ready.Store(true) }}
	cs := newTestControlServer(t, rec)
	if err := cs.reinstallRoutesIfNotReady(context.Background(), ready.Load); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("Rehijack apply 被调了 %d 次,want 1", applied)
	}
}

// apply 报成功而就绪位仍是假(比如接的是不记账的 mutator):不许宣称修好了。
func TestReinstallRoutesIfNotReadyDoesNotClaimSuccessItDidNotSee(t *testing.T) {
	rec := &recordingMutator{onRehijack: func() {}}
	cs := newTestControlServer(t, rec)
	if err := cs.reinstallRoutesIfNotReady(context.Background(), func() bool { return false }); err == nil {
		t.Fatal("apply 之后就绪位仍是 false,却报了成功")
	}
}

func TestReinstallRoutesIfNotReadyReportsARehijackFailure(t *testing.T) {
	boom := errors.New("route add failed")
	cs := newTestControlServer(t, &recordingMutator{rehijackErr: boom})
	if err := cs.reinstallRoutesIfNotReady(context.Background(), func() bool { return false }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestWatchRoutesReadyRepairsOnlyAFalseBit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ready bool
		want  int32
	}{
		{"就绪位为假 ⇒ 重装", false, 1},
		{"就绪位为真 ⇒ 一次都不动", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var repairs atomic.Int32
			tick := make(chan time.Time)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				watchRoutesReady(ctx, func() bool { return tc.ready },
					func(context.Context) error { repairs.Add(1); return nil }, tick)
				close(done)
			}()
			tick <- time.Now()
			tick <- time.Now() // 第二拍送得进去 ⇒ 第一拍已经处理完
			cancel()
			<-done
			if got := repairs.Load(); got != tc.want {
				t.Fatalf("repair 被调了 %d 次,want %d", got, tc.want)
			}
		})
	}
}

// **接线的承重部分是顺序**:这个工人必须在 Hijack 成功之后才起,且在拆除台账里
// 排在「还原路由」之后 push(⇒ LIFO 下先停)。反过来的话,正常关机时就绪位被
// 「restore default route」置成 false 的那一刻,这个循环还活着 —— 它会把刚拆掉的
// 劫持路由装回去,在 bx 退出的半路。从 OnControlReady 里起(那在 Hijack 之前)
// 正是那种写法。
func TestRunStartsTheRoutesReadyRepairAfterHijackAndStopsItBeforeRestore(t *testing.T) {
	src, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	s := stripGoComments(string(src))
	hijack := strings.Index(s, "plat.Hijack(")
	restore := strings.Index(s, `teardowns.push("restore default route"`)
	start := strings.Index(s, `workers.start(readyCtx, "routes-ready-repair"`)
	stop := strings.Index(s, `teardowns.push("stop routes-ready repair"`)
	for name, at := range map[string]int{
		"plat.Hijack(": hijack, "restore default route": restore,
		"routes-ready-repair 工人": start, "stop routes-ready repair": stop,
	} {
		if at < 0 {
			t.Fatalf("run.go 里找不到 %s —— 就绪位自愈没有接线(或锚点改了,守卫读不懂现在的代码)", name)
		}
	}
	if start < hijack {
		t.Fatal("就绪位自愈在 Hijack 之前就起了 —— 它的停止会排在还原路由之后,关机半路会把路由装回去")
	}
	if stop < restore {
		t.Fatal("「stop routes-ready repair」push 得比「restore default route」早 —— LIFO 下它会晚于还原路由才停")
	}
	if !strings.Contains(s, "hooks.ReinstallRoutesIfNotReady") {
		t.Fatal("run.go 没有从控制面拿到 ReinstallRoutesIfNotReady —— 重装不在控制面那把锁里")
	}
	ctl, err := os.ReadFile("control.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(strings.Fields(string(ctl)), " "),
		"ReinstallRoutesIfNotReady: cs.reinstallRoutesIfNotReady,") {
		t.Fatal("control.go 没把 reinstallRoutesIfNotReady 交出去")
	}
}
