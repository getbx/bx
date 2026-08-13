package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

const tailscaleDERPMapURL = "https://controlplane.tailscale.com/derpmap/default"

var tailscaleBootstrapFallbackCIDRs = []string{
	"64.225.56.166/32",
	"68.183.90.120/32",
	"134.122.94.167/32",
	"144.202.67.195/32",
	"149.28.119.105/32",
	"165.22.33.71/32",
	"178.62.44.132/32",
	"192.73.252.134/32",
	"208.111.34.178/32",
	"216.128.144.130/32",
}

// tailscaleBootstrapBypassCIDRs 取 Tailscale 的 DERP 中继地址,让它们绕开隧道。
//
// **此前它在非 darwin 上直接返回 nil,而门上没有任何理由。** 那不是必要的:三个
// 平台的旁路都吃同一份 CIDR 列表(linux 走 netConf.bypass 的 pref-100 之外那组
// route,windows 走 windowsRoutes 的 winViaGateway,darwin 走 split-default 的
// carve-out),DERP 那几个 /32 与服务器防环旁路是同一种东西。Linux 上跑 bx 的机器
// 同时跑 tailscaled 很常见(VPS、NAS、路由器),而它们此前一条旁路都没有。
//
// **两个已知缺口,都不在这次改动范围内,但必须写下来:**
//
//  1. 这份列表**只在启动时算一次**,之后由一个冻结值的闭包原样带过每一轮刷新
//     (见 run.go 与 bypassrefresh.go)。bx 由开机自启拉起时,那一刻网络往往还没好
//     → 抓 DERP map 失败 → 退回下面那张静态表 → **此后永不重试**。而开机自启正是常态。
//  2. 不管这台机器有没有装 Tailscale,每次启动都会去请求一次 controlplane.tailscale.com。
//     按「有没有 100.64/10 地址」门控会更干净,但那会让缺口 1 更糟 —— bx 先于
//     Tailscale 启动时就永远不会有旁路。要修得先修 1(让它能重算),两件事一起做。
func tailscaleBootstrapBypassCIDRs(ctx context.Context, direct *net.Dialer) []string {
	cidrs, err := tailscaleDERPBypassCIDRs(ctx, direct)
	if err != nil {
		log.Printf("tailscale bootstrap bypass:%v;先用内置 bootstrap 旁路,后台会一直重试", err)
		return mergeBypassCIDRs(tailscaleBootstrapFallbackCIDRs, tailscaleControlplaneFallbackCIDRs())
	}
	log.Printf("tailscale bootstrap bypass:已准备 %d 条 DERP/control IPv4 旁路", len(cidrs))
	return cidrs
}

// tailscaleDERPBypassCIDRs 抓一次 DERP map,**如实回报失败**。
//
// **它与上面那个包装刻意分开。** 包装吞掉错误、退回兜底表,那对启动是对的
// (不能因为抓不到就不装旁路);但重试循环必须分得清「抓到了」与「没抓到」——
// 拿包装当重试源,循环每次都判成功、立刻转成 6 小时周期,重试等于没做。
func tailscaleDERPBypassCIDRs(ctx context.Context, direct *net.Dialer) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tailscaleDERPMapURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构造 DERP map 请求失败: %w", err)
	}
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			Proxy:       nil,
			DialContext: direct.DialContext,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DERP map 获取失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("DERP map 返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 DERP map 失败: %w", err)
	}
	cidrs := tailscaleDERPMapBypassCIDRs(body)
	if len(cidrs) == 0 {
		return nil, errors.New("DERP map 无 IPv4 节点")
	}
	return mergeBypassCIDRs(cidrs, tailscaleControlplaneFallbackCIDRs()), nil
}

func tailscaleDERPMapBypassCIDRs(data []byte) []string {
	var m struct {
		Regions map[string]struct {
			Nodes []struct {
				IPv4 string `json:"IPv4"`
			} `json:"Nodes"`
		} `json:"Regions"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, region := range m.Regions {
		for _, node := range region.Nodes {
			addr, err := netip.ParseAddr(node.IPv4)
			if err != nil || !addr.Is4() {
				continue
			}
			cidr := netip.PrefixFrom(addr, 32).String()
			if seen[cidr] {
				continue
			}
			out = append(out, cidr)
			seen[cidr] = true
		}
	}
	return out
}

func tailscaleControlplaneFallbackCIDRs() []string {
	out := make([]string, 0, 16)
	for i := 101; i <= 116; i++ {
		out = append(out, netip.PrefixFrom(netip.AddrFrom4([4]byte{192, 200, 0, byte(i)}), 32).String())
	}
	return out
}

func mergeBypassCIDRs(groups ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, group := range groups {
		for _, cidr := range group {
			if cidr == "" || seen[cidr] {
				continue
			}
			out = append(out, cidr)
			seen[cidr] = true
		}
	}
	return out
}

// decideBypassUpdate 决定一次重探之后旁路该是什么。
//
// **失败绝不缩小。** 这条纪律本来靠「压根不重探」实现,而不重探正是缺陷本身:
// bx 由开机自启拉起时网络往往还没好,抓不到就退回内置兜底表且此后永不重试。
// 现在会重试,于是这条纪律必须由代码保证。
//
// 成功时**整份替换**而不是合并:兜底表是十个写死的 IP,可能早已不属于 Tailscale;
// 把它们永久留在旁路里,等于给几个陌生 IP 常开一条绕过隧道的路 —— 那是泄漏面,
// 不是冗余。抓到的答案权威,哪怕更短。
func decideBypassUpdate(previous, fetched []string, err error) []string {
	if err != nil || len(fetched) == 0 {
		return previous
	}
	return fetched
}

const (
	bypassProbeFirstDelay = 20 * time.Second
	bypassProbeMaxDelay   = 10 * time.Minute
	bypassProbeSettled    = 6 * time.Hour
)

// nextBypassProbeDelay 给出下一次探测前要等多久。
//
// 拿到第一份权威答案**之前**密集重试 —— 开机那几十秒正是网络还没好的时候,而那
// 恰恰是这个缺陷每天发生的场景。之后转成长周期复查:DERP 节点变动很慢,而每一次
// 请求都是发给 Tailscale 控制面的。
//
// **移位前先封顶。** 直接 `<<attempt` 在 attempt 大到一定程度会溢出回绕成 0,
// 而本仓库正因此让一条退避上限断言长期为错误的理由通过。
func nextBypassProbeDelay(attempt int, everSucceeded bool) time.Duration {
	if everSucceeded {
		return bypassProbeSettled
	}
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 16 { // 2^16 * 20s 已远超上限,再大只会溢出
		return bypassProbeMaxDelay
	}
	d := bypassProbeFirstDelay << uint(attempt)
	if d > bypassProbeMaxDelay {
		return bypassProbeMaxDelay
	}
	return d
}

// tailscaleBypassSource 持有「Tailscale 中继该走哪些旁路」这个答案,并在拿到权威
// 答案之前一直重试。
//
// **它刻意不触发 rehijack。** 重装路由有一个先拆后装的 ~ms 直连窗口,而
// platform_linux.go 那条注释正是用「仅在 agent/运维**主动** rehijack 时发生」来
// 正当化它的。为一次后台改进自动拓宽一个有文档的泄漏窗口,代价大于收益。
// 新值发布进 store 之后,**下一次合法的重装**(切服务器、Wi-Fi 切换触发的路径恢复、
// 或下次启动)自然会用上它 —— 而在那之前,旁路仍是启动时那份,行为不比今天更差。
type tailscaleBypassSource struct {
	mu    sync.RWMutex
	cidrs []string
	// everSucceeded 区分「兜底表」与「抓到过的权威答案」,退避节奏据此分岔。
	everSucceeded bool
}

func newTailscaleBypassSource(initial []string) *tailscaleBypassSource {
	return &tailscaleBypassSource{cidrs: append([]string(nil), initial...)}
}

// CIDRs 返回此刻的旁路集合。**每轮刷新现读,不冻** —— 冻结正是原来的缺陷。
func (s *tailscaleBypassSource) CIDRs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.cidrs...)
}

// apply 收下一次探测结果,回报是否有变化。
func (s *tailscaleBypassSource) apply(fetched []string, err error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := decideBypassUpdate(s.cidrs, fetched, err)
	if err == nil && len(fetched) > 0 {
		s.everSucceeded = true
	}
	if equalStringSets(s.cidrs, next) {
		return false
	}
	s.cidrs = next
	return true
}

func (s *tailscaleBypassSource) succeeded() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.everSucceeded
}

// Run 一直探测,直到 ctx 结束。fetch 由调用方注入(生产里是抓 DERP map),
// 好让这个循环在测试里不出网。
func (s *tailscaleBypassSource) Run(ctx context.Context, fetch func(context.Context) ([]string, error)) {
	attempt := 0
	for {
		delay := nextBypassProbeDelay(attempt, s.succeeded())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		fetched, err := fetch(ctx)
		if ctx.Err() != nil {
			return
		}
		changed := s.apply(fetched, err)
		if err != nil || len(fetched) == 0 {
			attempt++
			continue
		}
		attempt = 0
		if changed {
			// 只在真的变了时说话:每 6 小时打一行「没变」是噪声,而这一行的价值
			// 在于它平时不出现。
			log.Printf("tailscale bootstrap bypass:中继旁路已更新(%d 条);"+
				"新集合会在下一次路由重装时生效", len(fetched))
		}
	}
}
