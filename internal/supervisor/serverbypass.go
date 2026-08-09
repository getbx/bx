package supervisor

import "github.com/getbx/bx/internal/config"

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
