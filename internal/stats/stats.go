// Package stats 是线程安全的流量计数器,被引擎/Dialer 写、被 bx status 读。
// 生命周期 readiness 不存放在这里,由 supervisor 的独立 runtime handoff 负责。
package stats

import (
	"sync"
	"sync/atomic"
)

// Counters 是并发安全计数器(零值可用)。
type Counters struct {
	active  atomic.Int64
	proxy   atomic.Int64
	direct  atomic.Int64
	blocked atomic.Int64
	udpBlk  atomic.Int64
	up      atomic.Int64
	down    atomic.Int64

	// **结果**计数,与上面的**决策**计数分开。
	// bx 就在数据面上,这些失败它每一次都看见 —— 此前只是扔掉了。
	directFail atomic.Int64
	proxyFail  atomic.Int64

	// 按规则归因。用 mutex 而不是 atomic:它是个 map,且写频率与连接数同阶
	// (每条连接一到两次),远低于收发包。
	ruleMu sync.Mutex
	rules  map[ruleKey]*RuleOutcome
}

func (c *Counters) ConnOpen()       { c.active.Add(1) }
func (c *Counters) ConnClose()      { c.active.Add(-1) }
func (c *Counters) Proxy()          { c.proxy.Add(1) }
func (c *Counters) Direct()         { c.direct.Add(1) }
func (c *Counters) Blocked()        { c.blocked.Add(1) }
func (c *Counters) UDPBlocked()     { c.udpBlk.Add(1) }
func (c *Counters) DirectFailed()   { c.directFail.Add(1) }
func (c *Counters) ProxyFailed()    { c.proxyFail.Add(1) }
func (c *Counters) AddUp(n int64)   { c.up.Add(n) }
func (c *Counters) AddDown(n int64) { c.down.Add(n) }

func (c *Counters) Snapshot() Snapshot {
	return Snapshot{
		Active:     c.active.Load(),
		Proxy:      c.proxy.Load(),
		Direct:     c.direct.Load(),
		Blocked:    c.blocked.Load(),
		UDPBlocked: c.udpBlk.Load(),
		BytesUp:    c.up.Load(),
		BytesDown:  c.down.Load(),

		DirectFailed: c.directFail.Load(),
		ProxyFailed:  c.proxyFail.Load(),
		Rules:        c.ruleSnapshot(),
	}
}

// Snapshot 是某一刻的计数快照(可 JSON 序列化)。
type Snapshot struct {
	Active     int64 `json:"active"`
	Proxy      int64 `json:"proxy"`
	Direct     int64 `json:"direct"`
	Blocked    int64 `json:"blocked"`
	UDPBlocked int64 `json:"udp_blocked"`

	// **结果**,与上面的决策分开。`direct 26186` 与 `direct_failed 8113`
	// 是两件事,而 bx 此前只答得出前者。
	DirectFailed int64 `json:"direct_failed"`
	ProxyFailed  int64 `json:"proxy_failed"`

	// Rules 把判定与失败归因到**做出判定的那条规则**,用户据此知道该删哪一行。
	Rules     []RuleOutcome `json:"rules,omitempty"`
	BytesUp   int64         `json:"bytes_up"`
	BytesDown int64         `json:"bytes_down"`
}

// ProxyRatio 返回代理连接占(代理+直连)的比例,无连接时为 0。
func (s Snapshot) ProxyRatio() float64 {
	t := s.Proxy + s.Direct
	if t == 0 {
		return 0
	}
	return float64(s.Proxy) / float64(t)
}
