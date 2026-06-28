package supervisor

import (
	"sync/atomic"

	"github.com/getbx/bx/internal/tunnel"
)

// liveTunnel 原子持有当前隧道,供运行期换隧道时一处替换、多消费者(serveControl/refreshLoop/
// dialer transport)跟随。满足 tunnelStatser(Stats+SocksAddr)并加 Healthy。
// 本片 set 仅启动调一次(Slice 2b 才在 swap 时调)。
type liveTunnel struct {
	cur atomic.Pointer[tunnel.Tunnel]
}

func (lt *liveTunnel) set(t *tunnel.Tunnel) { lt.cur.Store(t) }
func (lt *liveTunnel) get() *tunnel.Tunnel  { return lt.cur.Load() }

func (lt *liveTunnel) Stats() tunnel.Stats { return lt.get().Stats() }
func (lt *liveTunnel) SocksAddr() string   { return lt.get().SocksAddr() }
func (lt *liveTunnel) Healthy() bool       { return lt.get().Healthy() }

// 编译期守卫:*liveTunnel 必须满足 serveControl 要的 tunnelStatser。
var _ tunnelStatser = (*liveTunnel)(nil)
