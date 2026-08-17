package guardian

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// 代际号只在**内容**变了的时候动。
func TestGenerationMovesOnlyWhenContentChanges(t *testing.T) {
	var version atomic.Int64
	p := newStatusPublisher(func() Status {
		s := representativeStatus()
		s.CoreVersion = "v" + string(rune('a'+version.Load()))
		return s
	})

	_, first := p.current()
	p.poke()
	_, again := p.current()
	if again != first {
		t.Fatalf("内容没变而代际号从 %d 动到了 %d —— 这会让 watch 永久自激", first, again)
	}

	version.Add(1)
	p.poke()
	_, third := p.current()
	if third == first {
		t.Fatalf("内容变了而代际号没动(仍是 %d)—— 客户端永远收不到这次变化", third)
	}
}

// 易变字段变了不算变化:这一条把 Task 1 的投影与本层的代际号连起来。
func TestVolatileChangeDoesNotMoveTheGeneration(t *testing.T) {
	var latency atomic.Int64
	latency.Store(390)
	p := newStatusPublisher(func() Status {
		s := representativeStatus()
		s.Core.LatencyMS = latency.Load()
		return s
	})
	_, first := p.current()
	for _, ms := range []int64{412, 388, 401} {
		latency.Store(ms)
		p.poke()
	}
	_, last := p.current()
	if last != first {
		t.Fatalf("只有 latency 在抖,代际号却从 %d 动到了 %d —— watch 会比 30 秒轮询更频", first, last)
	}
}

// Status.StatusGeneration 必须与 current() 单独返回的那个 uint64 一致 ——
// 这一条盯的是 unchanged 分支:compute() 不填 StatusGeneration,若 recomputeLocked
// 只在 changed 分支盖代际号,稳态下(绝大多数重算都是 unchanged)发布出去的
// Status 会一直带 status_generation: 0。Task 3 的 handler 直接把这个 Status
// 序列化成 JSON,客户端读到 0 就会回发 wait=0,长轮询因此退化成满速轮询。
func TestUnchangedStatusStillCarriesTheCurrentGeneration(t *testing.T) {
	p := newStatusPublisher(representativeStatus) // 内容恒定,每次重算都落 unchanged 分支
	_, gen := p.current()
	if gen == 0 {
		t.Fatalf("初始代际号是 0,测试前提不成立(应从 1 起)")
	}

	p.poke()                    // 绕过合并窗口,强制重算 —— current() 会被合并挡住、根本进不了 recomputeLocked 的主体
	status, gen2 := p.current() // 第二次:内容没变,走 unchanged 分支
	if gen2 != gen {
		t.Fatalf("两次 current() 的代际号不同(%d vs %d),测试前提不成立", gen, gen2)
	}
	if status.StatusGeneration != gen2 {
		t.Fatalf("unchanged 分支返回的 Status.StatusGeneration=%d,与 current() 单独返回的代际号 %d 不一致 —— "+
			"客户端会拿到 status_generation:%d、回发 wait=%d,而服务端真实代际号是 %d,"+
			"两者一比\"不同\"就立刻返回,长轮询退化成满速轮询",
			status.StatusGeneration, gen2, status.StatusGeneration, status.StatusGeneration, gen2)
	}
}

// 同上,但盯 changed 分支:内容变化之后,Status.StatusGeneration 也必须与
// 单独返回的代际号一致(这一条本来就该过,补上是为了让两条分支对称受测)。
func TestChangedStatusCarriesTheNewGeneration(t *testing.T) {
	var version atomic.Int64
	p := newStatusPublisher(func() Status {
		s := representativeStatus()
		s.CoreVersion = "v" + string(rune('a'+version.Load()))
		return s
	})
	_, before := p.current()

	version.Add(1)
	p.poke() // 绕过合并窗口,强制重算(否则紧跟着的 current() 会命中缓存,测不到 changed 分支)
	status, after := p.current()
	if after == before {
		t.Fatalf("内容变了但代际号没动(仍是 %d),测试前提不成立", before)
	}
	if status.StatusGeneration != after {
		t.Fatalf("changed 分支返回的 Status.StatusGeneration=%d,与 current() 单独返回的代际号 %d 不一致",
			status.StatusGeneration, after)
	}
}

// 客户端手上的代际号与当前不同 ⇒ **立刻**返回,一秒都不挂。
func TestWaitReturnsImmediatelyWhenGenerationDiffers(t *testing.T) {
	p := newStatusPublisher(representativeStatus)
	_, gen := p.current()

	start := time.Now()
	_, got := p.wait(context.Background(), gen+1, watchMaxHold)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("代际号不同却挂了 %v", elapsed)
	}
	if got != gen {
		t.Fatalf("返回的代际号是 %d,want %d", got, gen)
	}
}

// **Guardian 重启的形状:客户端手上的号比当前大。**
// 用 `>` 比较会让这个请求永久挂住 —— 重启后代际号从头开始。
func TestWaitReturnsImmediatelyWhenClientGenerationIsAhead(t *testing.T) {
	p := newStatusPublisher(representativeStatus)
	_, gen := p.current()

	done := make(chan uint64, 1)
	go func() {
		_, g := p.wait(context.Background(), gen+1000, watchMaxHold)
		done <- g
	}()
	select {
	case g := <-done:
		if g != gen {
			t.Fatalf("返回 %d, want %d", g, gen)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("客户端代际号比当前大时挂住了 —— 比较用的是 `>` 而不是 `!=`," +
			"Guardian 重启后这个请求永远不会返回")
	}
}

// 相同 ⇒ 挂住;poke ⇒ 醒。
func TestWaitParksAndWakesOnPoke(t *testing.T) {
	var version atomic.Int64
	p := newStatusPublisher(func() Status {
		s := representativeStatus()
		s.CoreVersion = "v" + string(rune('a'+version.Load()))
		return s
	})
	_, gen := p.current()

	woke := make(chan uint64, 1)
	go func() {
		_, g := p.wait(context.Background(), gen, watchMaxHold)
		woke <- g
	}()

	// 给 waiter 一点时间真的挂上去。
	time.Sleep(200 * time.Millisecond)
	select {
	case g := <-woke:
		t.Fatalf("内容没变却提前返回了(代际号 %d)", g)
	default:
	}

	version.Add(1)
	p.poke()

	select {
	case g := <-woke:
		if g == gen {
			t.Fatalf("醒了但代际号没变(%d)", g)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("poke 之后 waiter 没醒")
	}
}

// 超时 ⇒ 返回当前 Status,代际号**不变**。这一条同时是「通道还活着」的证据,
// 并且直接验证 watchMinHold 的钳位:传入的 timeout(300ms)比 watchMinHold(1s)
// 短,若钳位生效,实际耗时应接近 watchMinHold 而不是接近 300ms —— 只断言
// ">=200ms" 无论钳没钳、钳到哪个值都会通过,验证不到钳位本身。
func TestWaitTimesOutWithUnchangedGeneration(t *testing.T) {
	p := newStatusPublisher(representativeStatus)
	_, gen := p.current()

	start := time.Now()
	_, got := p.wait(context.Background(), gen, 300*time.Millisecond)
	elapsed := time.Since(start)
	if got != gen {
		t.Fatalf("超时返回的代际号是 %d,want %d(不变)", got, gen)
	}
	lower := watchMinHold - 200*time.Millisecond
	upper := watchMinHold + 700*time.Millisecond
	if elapsed < lower || elapsed > upper {
		t.Fatalf("耗时 %v,不在 [%v,%v] 区间 —— timeout=300ms 应被钳到 watchMinHold=%v,"+
			"这条断言直接验证钳位钳到了正确的值,而不是随便找一个比 300ms 短的下限",
			elapsed, lower, upper, watchMinHold)
	}
}

// **关机必须立刻唤醒全部 parked waiter。**
//
// server.Shutdown 会等在跑的 handler 返回,而升级时 Guardian 要被 bootout ——
// 一个挂 25 秒的 watch 会让关机慢 25 秒。这个项目在「关机慢」上栽过 71 分钟,
// 所以这一条钉的是不变量,不是性能。
func TestBeginShutdownWakesParkedWaitersImmediately(t *testing.T) {
	p := newStatusPublisher(representativeStatus)
	_, gen := p.current()

	done := make(chan struct{})
	go func() {
		p.wait(context.Background(), gen, watchMaxHold)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	p.beginShutdown()
	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("关机后 waiter 过了 %v 才返回", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("beginShutdown 没有唤醒 parked 的 waiter —— Guardian 关机会被它拖住 25 秒")
	}
}

// ctx 取消(客户端断开)⇒ 立刻返回,别把 goroutine 漏在那里。
func TestWaitReturnsWhenContextIsCanceled(t *testing.T) {
	p := newStatusPublisher(representativeStatus)
	_, gen := p.current()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		p.wait(ctx, gen, watchMaxHold)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后 waiter 没返回")
	}
}

// **合并:多个 waiter 不该把重算放大。**
//
// /v1/status 是无鉴权的读端点,而 compute 里有一次 Core socket 往返 ——
// 没有合并,任何本地进程都能开一百条 watch 把往返放大一百倍。
func TestConcurrentWaitersDoNotMultiplyRecomputes(t *testing.T) {
	var computes atomic.Int64
	p := newStatusPublisher(func() Status {
		computes.Add(1)
		return representativeStatus()
	})
	_, gen := p.current()
	before := computes.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for range 20 {
		go p.wait(ctx, gen, watchMaxHold)
	}
	// 一个兵底间隔多一点:20 个 waiter 各自醒一次,但合并之后重算次数应当
	// 与「一个 waiter」同量级,而不是 20 倍。
	time.Sleep(watchRecomputeInterval + 500*time.Millisecond)
	cancel()

	if extra := computes.Load() - before; extra > 6 {
		t.Fatalf("20 个 waiter 在一个兵底间隔里触发了 %d 次重算 —— 合并没生效,"+
			"一百条 watch 就能把 Core 往返放大一百倍", extra)
	}
}

// poke 必须绕过合并:真事件要立刻见效,不能被「刚算过」挡住。
func TestPokeBypassesCoalescing(t *testing.T) {
	var version atomic.Int64
	p := newStatusPublisher(func() Status {
		s := representativeStatus()
		s.CoreVersion = "v" + string(rune('a'+version.Load()))
		return s
	})
	_, gen := p.current() // 刚算过,合并窗口里

	version.Add(1)
	p.poke()
	_, got := p.current()
	if got == gen {
		t.Fatal("poke 被合并挡住了 —— 用户敲完 bx down 要等一个兵底间隔才看到变化," +
			"而 poke 存在的全部理由就是消掉那段等待")
	}
}
