package supervisor

import (
	"context"
	"net"
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
	build         func(link string) (*tunnel.Tunnel, error)
	healthTimeout time.Duration
	ctx           context.Context
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
	s.setLink(link)
	old.Stop() // 停旧 brook(既有连接重置)
	return nil
}

// stop 在 s.mu 下停掉当前隧道,供 Run 退出清理。经锁与 swapTo 串行:任何在飞的
// (死手自动回滚触发的)swapTo 先完成再停,故停的是最终 current,不会漏停换进来的新隧道(修 M3)。
func (s *transportSwapper) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lt.get().Stop()
}

var _ linkSwapper = (*transportSwapper)(nil)
