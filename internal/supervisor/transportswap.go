package supervisor

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getbx/bx/internal/dialer"
	"github.com/getbx/bx/internal/tunnel"
)

// transportSwapper 是 linkSwapper 的真实现:运行期把隧道换到某 link(同/不同服务器均可——
// 各传输 server 已统一进 serverBypass+静态 DNS,见 run.go)。
// build 复用 run.go 的 buildTunnel(含按需 sing-box)。硬换:换成后立即停旧隧道
// (既有 TCP 连接重置)。健康失败则停新、不换,旧隧道无损。
type transportSwapper struct {
	mu            sync.Mutex
	lt            *liveTunnel
	d             *dialer.Dialer
	build         func(link, recoveryID string) (*tunnel.Tunnel, error)
	healthTimeout time.Duration
	ctx           context.Context
	udp           bool
	prepared      sync.Map
	sequence      atomic.Uint64
	curLink       atomic.Pointer[string] // 无锁读:swapTo 持 mu 跨健康等待(可达 healthTimeout)时,status 读不被卡
}

// setLink 原子记录当前活跃传输链接(link 形参是新副本,存其地址安全)。
func (s *transportSwapper) setLink(link string) { s.curLink.Store(&link) }

func (s *transportSwapper) currentLink() string {
	if p := s.curLink.Load(); p != nil {
		return *p
	}
	return ""
}

func (s *transportSwapper) swapTo(link string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	recoveryID := fmt.Sprintf("swap-%d", s.sequence.Add(1))
	newTun, err := s.prepare(ctx, link, recoveryID)
	if err != nil {
		return err
	}
	s.commit(newTun, link)
	return nil
}

func (s *transportSwapper) prepare(ctx context.Context, link, recoveryID string) (*tunnel.Tunnel, error) {
	newTun, err := s.build(link, recoveryID)
	if err != nil {
		return nil, err
	}
	newTun.Start()
	if err := waitTunnelHealthy(ctx, newTun, s.healthTimeout); err != nil {
		newTun.Stop() // 新隧道没起来:停掉、不换,旧隧道仍在服务
		return nil, err
	}
	px, err := socksProxy(newTun.SocksAddr(), &net.Dialer{Timeout: 10 * time.Second})
	if err != nil {
		newTun.Stop()
		return nil, err
	}
	s.prepared.Store(newTun, px)
	return newTun, nil
}

func (s *transportSwapper) commit(candidate *tunnel.Tunnel, link string) {
	old := s.activate(candidate, link)
	old.Stop()
}

func (s *transportSwapper) activate(candidate *tunnel.Tunnel, link string) *tunnel.Tunnel {
	proxyValue, ok := s.prepared.LoadAndDelete(candidate)
	if !ok {
		panic("transport candidate committed without successful prepare")
	}
	px := proxyValue.(dialer.ContextDialer)
	old := s.lt.get()
	s.lt.set(candidate)
	transport := &dialer.Transport{Proxy: px, Healthy: candidate.Healthy}
	if s.udp {
		s.d.SetUDPTransport(transport)
	} else {
		s.d.SetTransport(transport)
	}
	s.setLink(link)
	return old
}

func (s *transportSwapper) abort(candidate *tunnel.Tunnel) {
	if candidate == nil {
		return
	}
	s.prepared.Delete(candidate)
	candidate.Stop()
}

// stop 在 s.mu 下停掉当前隧道,供 Run 退出清理。经锁与 swapTo 串行:任何在飞的
// (死手自动回滚触发的)swapTo 先完成再停,故停的是最终 current,不会漏停换进来的新隧道(修 M3)。
func (s *transportSwapper) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lt.get().Stop()
}

func (s *transportSwapper) transportSetSlot(name transportSlotName) transportSetSlot {
	return transportSetSlot{
		name: name,
		link: s.currentLink,
		prepare: func(ctx context.Context, link, recoveryID string) (transportCandidate, error) {
			return s.prepare(ctx, link, recoveryID)
		},
		commit: func(candidate transportCandidate, link string) transportCandidate {
			return s.activate(candidate.(*tunnel.Tunnel), link)
		},
		abort: func(candidate transportCandidate) {
			s.abort(candidate.(*tunnel.Tunnel))
		},
	}
}

func transportConfigPath(dataDir, kind, recoveryID string) string {
	name := "sing-box.json"
	switch kind {
	case "hysteria2":
		name = "sing-box-hy2.json"
	case "trojan":
		name = "sing-box-trojan.json"
	case "shadowsocks":
		name = "sing-box-ss.json"
	case "vmess":
		name = "sing-box-vmess.json"
	}
	if recoveryID == "" {
		return filepath.Join(dataDir, name)
	}
	recoveryID = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, recoveryID)
	extension := filepath.Ext(name)
	base := strings.TrimSuffix(name, extension)
	return filepath.Join(dataDir, base+"."+recoveryID+extension)
}

var _ linkSwapper = (*transportSwapper)(nil)
