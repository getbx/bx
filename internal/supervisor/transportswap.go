package supervisor

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/getbx/bx/internal/dialer"
	"github.com/getbx/bx/internal/tunnel"
)

// transportSwapper 是 linkSwapper 的真实现:运行期把隧道换到某 link(同服务器)。
// build 复用 run.go 的 buildTunnel(含按需 sing-box)。硬换:换成后立即停旧隧道
//(既有 TCP 连接重置)。健康失败则停新、不换,旧隧道无损。
type transportSwapper struct {
	mu            sync.Mutex
	lt            *liveTunnel
	d             *dialer.Dialer
	build         func(link string) (*tunnel.Tunnel, error)
	healthTimeout time.Duration
	ctx           context.Context
	curLink       string
}

func (s *transportSwapper) currentLink() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.curLink
}

func (s *transportSwapper) swapTo(link string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	newTun, err := s.build(link)
	if err != nil {
		return err
	}
	newTun.Start()
	if err := waitTunnelHealthy(s.ctx, newTun, s.healthTimeout); err != nil {
		newTun.Stop() // 新隧道没起来:停掉、不换,旧隧道仍在服务
		return err
	}
	px, err := socksProxy(newTun.SocksAddr(), &net.Dialer{Timeout: 10 * time.Second})
	if err != nil {
		newTun.Stop()
		return err
	}
	old := s.lt.get()
	s.lt.set(newTun)                                                        // serveControl/refreshLoop 跟随
	s.d.SetTransport(&dialer.Transport{Proxy: px, Healthy: newTun.Healthy}) // 新连接走新隧道
	s.curLink = link
	old.Stop() // 停旧 brook(既有连接重置)
	return nil
}

var _ linkSwapper = (*transportSwapper)(nil)
