package supervisor

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/getbx/bx/internal/overlay"
)

// overlayRelayDoH 解析 overlay 中继主机名(ZeroTier 根节点)用的 DoH 端点。
//
// **必须是 TLS 的 DoH,不许是明文 UDP:53**:解析结果会变成**绕过隧道**的 /32 旁路。明文
// 应答被投毒(国内 DNS 对境外域名常见)时,那个冒牌地址 —— 典型是某个 Facebook 的 IP ——
// 会让**所有应用**发往它的流量以真实 IP 直出。TLS 校验证书,投不了毒;连不上就解析失败,
// 退回写死的兜底表,与今天一样。走 DirectDialer(此刻旁路还没装,不能进隧道)。
// 代价如实记下:Cloudflare 会从真实 IP 看到一次「root-*.zerotier.com」查询 —— 而这台机器
// 随后本来就要从真实 IP 直连那几台根节点。
const overlayRelayDoH = "https://1.1.1.1/dns-query"

// overlayRelayResolveBudget 是一次解析(一个租户的全部主机名,并发)的总时限。
// 它坐在启动路径上(initialOverlayBypass),DoH 被封时不许把 bx 的启动拖慢几十秒。
const overlayRelayResolveBudget = 4 * time.Second

func newOverlayRelayResolver(direct *net.Dialer) func(hosts []string) []netip.Addr {
	r := newDoHResolver(overlayRelayDoH, direct)
	return func(hosts []string) []netip.Addr {
		ctx, cancel := context.WithTimeout(context.Background(), overlayRelayResolveBudget)
		defer cancel()
		var (
			mu  sync.Mutex
			out []netip.Addr
			wg  sync.WaitGroup
		)
		for _, host := range hosts {
			wg.Add(1)
			go func(host string) {
				defer wg.Done()
				addrs, err := r.ResolveAll(ctx, host)
				if err != nil {
					return
				}
				mu.Lock()
				out = append(out, addrs...)
				mu.Unlock()
			}(host)
		}
		wg.Wait()
		return out
	}
}

// overlayRelayBypass 是 overlay 中继旁路的唯一取数口:解析得出来用解析结果,不然用兜底。
func overlayRelayBypass(present []overlay.Tenant, resolve func([]string) []netip.Addr) []string {
	return overlay.RelayBypassCIDRs(present, resolve)
}
