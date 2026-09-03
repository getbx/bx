package supervisor

import (
	"context"
	"sync"
	"time"

	"github.com/getbx/bx/internal/stats"
)

// deliveryWindow 是判「隧道载不载得动数据」的采样窗口。
//
// 取 60 秒是两头折中:短了凑不够 20 次样本(一台闲着的机器一分钟未必有 20 条
// 经隧道的连接),长了则一次已经恢复的故障会在窗口里拖着不散。
const deliveryWindow = time.Minute

// deliveryMonitor 按**固定节拍**采样经隧道拨号的成败,给出三态判断。
//
// **固定节拍不是讲究,是判据的一部分** —— 与 sampleThroughput 同一条理由:
// 读状态的间隔由调用方决定(菜单开着 2 秒、关着 30 秒、CLI 一次就走),把采样
// 搭在那条路上,判据就跟着别人的节奏变,而「最近一分钟失败了几次」必须是一个
// 与谁在看无关的事实。
//
// 用的是**窗口内的增量**而不是累计值:累计值会让一台昨天出过问题的机器永远
// 显示不健康 —— 那正好是这个功能要消灭的东西的镜像(另一种不该有的颜色)。
type deliveryMonitor struct {
	mu       sync.Mutex
	verdict  stats.DeliveryVerdict
	attempts int64
	failures int64

	// baseProxy/baseFailed 是窗口起点的累计值。
	baseProxy, baseFailed int64
	started               bool
}

// observe 吃一份快照,推进窗口。**窗口未满时不改判断** —— 半个窗口的数字
// 与一个完整窗口的数字含义不同,拿它下判断就是把门槛悄悄降低。
func (m *deliveryMonitor) observe(proxy, proxyFailed int64, windowElapsed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		m.baseProxy, m.baseFailed, m.started = proxy, proxyFailed, true
		return
	}
	if !windowElapsed {
		return
	}
	// 计数器回退(Core 重启,计数从头开始)会让差值变成负数,而
	// **stats.JudgeDelivery 已经把不可能的计数判成 Unknown** —— 这里不再重复
	// 挡一遍。第一版在这里加过一个「回退就作废」的分支,变异实测证明它**一行
	// 也没做到注释里说的事**:两条路都会走到下面那次重新取基线,而判断本来就
	// 是 Unknown。一段被自己的注释说成承重的死代码,比没有更糟。
	attempts, failures := proxy-m.baseProxy, proxyFailed-m.baseFailed
	m.verdict = stats.JudgeDelivery(attempts, failures)
	m.attempts, m.failures = attempts, failures
	m.baseProxy, m.baseFailed = proxy, proxyFailed
}

// warning 返回当前该说的那句话;没什么可说时返回 nil。
func (m *deliveryMonitor) warning() *stats.Warning {
	m.mu.Lock()
	defer m.mu.Unlock()
	return stats.DeliveryWarning(m.verdict, m.attempts, m.failures)
}

// sampleDelivery 是生产用的采样循环,与 sampleThroughput 并列。
func sampleDelivery(ctx context.Context, c *stats.Counters, m *deliveryMonitor) {
	if c == nil || m == nil {
		return
	}
	ticker := time.NewTicker(deliveryWindow)
	defer ticker.Stop()
	snap := c.Snapshot()
	m.observe(snap.Proxy, snap.ProxyFailed, false) // 立刻取基线,别等第一拍
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s := c.Snapshot()
			m.observe(s.Proxy, s.ProxyFailed, true)
		}
	}
}
