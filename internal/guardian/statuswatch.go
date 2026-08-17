package guardian

import (
	"context"
	"log"
	"sync"
	"time"
)

const (
	// watchRecomputeInterval 是**重算兵底**:parked 的 waiter 每隔这么久自己
	// 醒一次、重算一遍、比一次投影。
	//
	// 它存在的理由不是「怕漏广播」这么抽象 —— 有一个具体的、没有任何广播点
	// 会覆盖的变化:**维护挂起到期**。挂起是读取时判过期的(沿用
	// internal/toolkeys 那个不设定时器的先例),到期那一刻没有任何代码在跑,
	// 只有下一次重算会发现 MaintenanceHold 从非 nil 变成了 nil。
	watchRecomputeInterval = 3 * time.Second
	// watchMaxHold 是服务端挂住的上限,也是「通道还活着」那条心跳的节拍。
	// **客户端超时必须比它长**(菜单侧 40 秒),否则客户端拿到的永远是自己的
	// 超时,而服务端这个上限一次都不会生效(switchServer/probeServers 的注释
	// 里已经踩过并写下过同一个坑)。
	watchMaxHold = 25 * time.Second
	// watchMinHold 是 timeout 参数的下限。0 会让客户端把 watch 变成满速轮询。
	watchMinHold = time.Second
)

// statusPublisher 是**唯一**发布 Status 与代际号的地方。
//
// 代际号由内容派生:重算 → 取投影(statusDigest)→ 与上次比 → 不同才 ++。
// 于是「新加一条改状态的路、忘了 bump」在构造上不存在 —— 而那正是这个仓库
// 反复出现的形状(判据只长在一条路上,而它要保护的状态可以从别的路进来)。
//
// 广播(poke)**不携带任何数据**,只是「现在就重算,别等下一个兵底拍」。
// 所以一次广播不可能是错的,漏一个的代价只是慢到下一个兵底拍。
type statusPublisher struct {
	compute func() Status

	mu         sync.Mutex
	generation uint64
	digest     string
	status     Status
	computedAt time.Time
	// changed 每次 bump 时被 close 并换一条新的 —— Go 里的标准广播手法。
	// parked 的 waiter 在解锁前先抓住当前这一条。
	changed chan struct{}

	shutdownOnce sync.Once
	shutdown     chan struct{}
}

func newStatusPublisher(compute func() Status) *statusPublisher {
	return &statusPublisher{
		compute:  compute,
		changed:  make(chan struct{}),
		shutdown: make(chan struct{}),
	}
}

// current 返回当前 Status 与代际号,必要时重算。
// 「必要」= 距上次重算已超过一个兵底间隔(合并,见 recomputeLocked)。
func (p *statusPublisher) current() (Status, uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recomputeLocked(false)
	return p.status, p.generation
}

// poke 强制重算并在内容变了时唤醒全部 waiter。**绕过合并** ——
// 用户敲完 bx down 正等着看图标变,不能让「刚算过」把它挡一个兵底间隔。
func (p *statusPublisher) poke() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recomputeLocked(true)
}

// recomputeLocked 重算一次并按投影决定要不要 bump。调用方必须持锁。
//
// force=false 时做**合并**:距上次重算不足一个兵底间隔就直接返回缓存。
// 没有这一条,N 个并发 waiter 就是 N 倍的 compute,而 compute 里有一次 Core
// socket 往返、/v1/status 又是无鉴权的读端点 —— 一百条 watch 能把它放大一百倍。
func (p *statusPublisher) recomputeLocked(force bool) {
	if !force && !p.computedAt.IsZero() && time.Since(p.computedAt) < watchRecomputeInterval {
		return
	}
	status := p.compute()
	p.computedAt = time.Now()

	digest, err := statusDigest(status)
	if err != nil {
		// 当作「变了」:那意味着 Status 里有编不了码的东西,是编程错误。
		// 当作「变了」会让 watch 每个兵底拍都触发 —— 吵、且这里有日志;
		// 当作「没变」会让菜单**静默冻住**。同一条不对称(见 statusDigest 的注释)。
		log.Printf("guardian_status_digest_failed err=%v", err)
		digest = ""
	}
	if err == nil && digest == p.digest {
		p.status = status
		return
	}
	p.digest = digest
	p.generation++
	status.StatusGeneration = p.generation
	p.status = status
	// 广播:close 当前这一条,换一条新的。
	close(p.changed)
	p.changed = make(chan struct{})
}

// wait 是长轮询的核心。
//
// **比较用 `!=` 而不是 `>`。** Guardian 重启后代际号从头开始,客户端手上那个
// 较大的值在 `>` 下永远不满足,请求会永久挂住。
func (p *statusPublisher) wait(ctx context.Context, clientGen uint64, timeout time.Duration) (Status, uint64) {
	if timeout < watchMinHold {
		timeout = watchMinHold
	}
	if timeout > watchMaxHold {
		timeout = watchMaxHold
	}
	deadline := time.Now().Add(timeout)
	for {
		p.mu.Lock()
		p.recomputeLocked(false)
		status, generation, changed := p.status, p.generation, p.changed
		p.mu.Unlock()

		if generation != clientGen {
			return status, generation
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return status, generation
		}
		// 兵底不是一条独立的 goroutine,也不需要订阅者计数:每个 waiter 的
		// select 自带这个分支。于是「没人 watch 时开销精确为零」是构造出来的,
		// 不是维护出来的。
		nap := watchRecomputeInterval
		if remaining < nap {
			nap = remaining
		}
		timer := time.NewTimer(nap)
		select {
		case <-changed:
			timer.Stop()
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return status, generation
		case <-p.shutdown:
			timer.Stop()
			return status, generation
		}
	}
}

// beginShutdown 立刻唤醒全部 parked waiter。
//
// **这是不变量,不是性能。** http.Server.Shutdown 会等在跑的 handler 返回,
// 而升级时 Guardian 要被 launchctl bootout —— 一个挂 25 秒的 watch 会让关机慢
// 25 秒。这个项目在「关机慢」上栽过 71 分钟(2026-08-04),纪律是:
// 停止路径不许因为别的事没做完而变慢或失败。
//
// 用 sync.Once 是因为 Daemon.Shutdown 可能被调用多次(它自己有
// shutdownStarted 保护,但这一层不该依赖调用方的纪律)。
func (p *statusPublisher) beginShutdown() {
	p.shutdownOnce.Do(func() { close(p.shutdown) })
}
