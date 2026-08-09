package supervisor

import (
	"fmt"
	"log"
	"net/netip"
	"time"

	"github.com/getbx/bx/internal/config"
)

// bypassLink 是一条需要进 serverBypass + 静态 DNS 的传输链接。
//
// Required 决定「解析不出 IP 时是拒绝启动还是跳过」。这个区分是必要的:
// 清单是会**攒**的 —— 用户加过的服务器会一直留在里面,其中难免有退役的、
// 换了 IP 的、DNS 偶尔抖的。让其中任意一台解析失败就把整台机器的保护堵死,
// 等于用「一台我今天根本没在用的服务器」勒索用户。
//
// 但当前那台不一样:它的 IP 进不了 bypass,隧道自己连服务器的流量就会被劫进
// TUN —— 成环,而且是静默的(连得上、status 显绿、流量绕圈)。那必须拒绝启动。
type bypassLink struct {
	Link     string
	Required bool
}

// bypassLinks 列出所有需要进 serverBypass + 静态 DNS 的传输链接。
//
// 有服务器清单时是**每一台的两条链接**(不只当前那台):用户随时可能切过去,而
// 切换走的是热切路径、不重装路由。启动时一次把全部铺好,切换就一条路由都不用动。
// 非当前那些标 Required=false —— 解析不出就跳过、留待切换时再算(切换路径会重新
// 刷新 bypass 并按需 rehijack),而不是连累启动。
//
// 没有清单时退回旧语义:transports(主 + 容灾备选)+ udp.transport,**全部必需**
// —— 那是一份为自动容灾精挑的短表,里面每一条都随时可能被切上去。
func bypassLinks(cfg *config.Config) []bypassLink {
	if len(cfg.Servers) > 0 {
		current, _ := cfg.CurrentServer()
		var links []bypassLink
		for _, s := range cfg.Servers {
			required := s.Name == current.Name
			links = append(links, bypassLink{Link: s.Link, Required: required})
			if s.UDP != "" {
				links = append(links, bypassLink{Link: s.UDP, Required: required})
			}
		}
		return links
	}
	var links []bypassLink
	for _, l := range cfg.Transports {
		links = append(links, bypassLink{Link: l, Required: true})
	}
	// 旧路径保持原样:只有 mode=proxy 时 udp.transport 才会被挂载,也只有那时才进 bypass。
	if cfg.UDP.Transport != "" && cfg.UDP.Mode == "proxy" {
		links = append(links, bypassLink{Link: cfg.UDP.Transport, Required: true})
	}
	return links
}

// bypassRefreshTimeout 封顶「切换前刷新 bypass」的整轮耗时(DNS + tailscale 探测)。
// 这段跑在控制面锁里,不封顶就等于给 /v0/commit、/v0/transport、/v0/rehijack
// 一个无限期的连坐窗口。
const bypassRefreshTimeout = 5 * time.Second

// resolveServerBypass 解析清单里全部服务器的 host → IP,产出静态 DNS 表与地址集合。
// 启动与「切换前刷新」共用它:两条路径分头实现,迟早有一条会漏掉某类链接,
// 而漏掉的那一条就是成环。
func resolveServerBypass(cfg *config.Config) (map[string][]netip.Addr, []netip.Addr, error) {
	return resolveServerBypassWith(cfg, hostToAddrs)
}

// resolveServerBypassWith 是 resolveServerBypass 的可注入版本:resolve 默认是
// hostToAddrs(真实系统解析),单测注入假解析器才走得到「解析失败」那两条分支
// —— 它们此前活在 run.go 的闭包里,要真发生一次 DNS 失败才可达,故一直没有测试。
func resolveServerBypassWith(cfg *config.Config, resolve func(string) []netip.Addr) (map[string][]netip.Addr, []netip.Addr, error) {
	return resolveServerBypassRequiring(cfg, nil, resolve)
}

// resolveServerBypassRequiring 在配置清单之外再**点名**一批必需链接。
//
// 为什么需要点名:`/v0/server` 的请求体里就带着目标链接,而 spec 是「先热切、
// 成功后才落盘 current」—— 切换那一刻目标往往还不是配置文件里的 current,甚至
// 可能还不在文件里。若 Required 只从文件的 current 推断,目标解析失败就会被当成
// 「一台今天没在用的服务器」静默跳过,然后不装 bypass 直接切过去 = 成环
// (静默:连得上、status 显绿、流量绕圈)。**知道答案的一方直接说出来,不要猜。**
//
// Required 语义(见 bypassLink):当前那台、以及被点名的那些,解析不了 = 拒绝;
// 其余解析不了 = 跳过(清单是会攒的,一台今天没在用的机器不该堵死启动/切换)。
func resolveServerBypassRequiring(cfg *config.Config, requiredLinks []string, resolve func(string) []netip.Addr) (map[string][]netip.Addr, []netip.Addr, error) {
	return resolveServerBypassRetaining(cfg, requiredLinks, resolve, nil)
}

// resolveServerBypassRetaining 在解析失败时回落到 retain 里上一轮已解析成功的地址。
//
// 为什么需要:刷新是**替换**不是并集,一台已知服务器这轮 DNS 抖一下就会掉出
// bypass,而 runFailover 随时可能切到它 —— 那时它没有旁路 = 成环。
// 但保留只发生在**本轮仍然被遍历到**的主机上(即 fresh 配置仍写着、或调用方点名的),
// 用户删掉的服务器不会被查到、因而真的消失 —— 留着它等于它的流量绕开隧道。
//
// **调用方点名的主机一律不保留。** `/v0/server` 把目标点名出来,正是为了让
// 「落实不了新服务器的 bypass 就绝不切过去」有个判据;沿用上一轮的地址回报成功,
// 等于让一次「我不知道这台服务器今天在哪」的切换照常进行 —— 那就是本函数全部
// 注释都在防的那个静默成环。禁用按 **host** 而不是按 link:同一个 host 可能同时
// 以「清单里的非必需链接」和「被点名的链接」两种身份出现,前者先被遍历到就会替
// 后者把保留用掉,后者再撞上去重直接返回 nil —— 于是点名那条根本走不到判定。
func resolveServerBypassRetaining(cfg *config.Config, requiredLinks []string, resolve func(string) []netip.Addr, retain map[string][]netip.Addr) (map[string][]netip.Addr, []netip.Addr, error) {
	noRetain := make(map[string]bool, len(requiredLinks))
	for _, l := range requiredLinks {
		if l == "" {
			continue
		}
		if h, err := serverHostFromLink(l); err == nil {
			noRetain[h] = true
		}
	}
	staticA := map[string][]netip.Addr{}
	var addrs []netip.Addr
	add := func(link string) error {
		h, err := serverHostFromLink(link)
		if err != nil {
			return fmt.Errorf("取传输服务器: %w", err)
		}
		if _, ok := staticA[h]; ok {
			return nil // 去重(多传输同 server)
		}
		a := resolve(h)
		if len(a) == 0 {
			if kept := retain[h]; len(kept) > 0 && !noRetain[h] {
				log.Printf("bypass 刷新:%q 本轮解析失败,沿用上一轮已解析的地址(掉出 bypass 会成环)", h)
				staticA[h] = append([]netip.Addr(nil), kept...)
				addrs = append(addrs, kept...)
				return nil
			}
			return fmt.Errorf("无法解析传输服务器 %q 为 IP(bypass 必需,否则成环)", h)
		}
		staticA[h] = a
		addrs = append(addrs, a...)
		return nil
	}
	for _, l := range unionRequired(bypassLinks(cfg), requiredLinks) {
		if err := add(l.Link); err != nil {
			if l.Required {
				return nil, nil, err
			}
			// 跳过的那台此刻不在 bypass 里,故也切不过去 —— 切换路径会重新刷新
			// bypass 并按需 rehijack,那时再解析一次;在那之前它对数据面不存在,
			// 不会有半装状态。
			log.Printf("跳过服务器 bypass(非当前选中,暂不可切换): %v", err)
		}
	}
	if len(addrs) == 0 {
		return nil, nil, fmt.Errorf("无法解析任何传输服务器 IP(bypass 必需)")
	}
	return staticA, addrs, nil
}

// unionRequired 把点名的链接并进清单:已在清单里的**提升**为 Required,
// 不在清单里的(目标还没落盘时就是这种)追加为 Required。
// 只提升被点名的那些 —— 不把整张清单变成必需,否则一台退役服务器又能堵死切换。
func unionRequired(links []bypassLink, requiredLinks []string) []bypassLink {
	if len(requiredLinks) == 0 {
		return links
	}
	required := make(map[string]bool, len(requiredLinks))
	for _, l := range requiredLinks {
		if l != "" {
			required[l] = true
		}
	}
	out := make([]bypassLink, 0, len(links)+len(required))
	seen := make(map[string]bool, len(links))
	for _, l := range links {
		seen[l.Link] = true
		if required[l.Link] {
			l.Required = true
		}
		out = append(out, l)
	}
	for _, l := range requiredLinks {
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, bypassLink{Link: l, Required: true})
	}
	return out
}

// equalStringSets 判两个 CIDR 列表是不是同一个集合(顺序无关)。
// 用它决定「要不要重装路由」:相等就别装 —— 重装会重探网关、拆装真实路由,
// 是纯粹的风险;不等就必须装 —— 漏判等于新服务器没进 bypass = 成环。
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
}
