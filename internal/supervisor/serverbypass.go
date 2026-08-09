package supervisor

import "github.com/getbx/bx/internal/config"

// bypassLinks 列出所有必须进 serverBypass + 静态 DNS 的传输链接。
//
// 有服务器清单时是**每一台的两条链接**(不只当前那台):用户随时可能切过去,而
// 切换走的是热切路径、不重装路由。启动时一次把全部铺好,切换就一条路由都不用动。
//
// 没有清单时退回旧语义:transports(主 + 容灾备选)+ udp.transport。
func bypassLinks(cfg *config.Config) []string {
	var links []string
	if len(cfg.Servers) > 0 {
		for _, s := range cfg.Servers {
			links = append(links, s.Link)
			if s.UDP != "" {
				links = append(links, s.UDP)
			}
		}
		return links
	}
	links = append(links, cfg.Transports...)
	if cfg.UDP.Transport != "" {
		links = append(links, cfg.UDP.Transport)
	}
	return links
}
