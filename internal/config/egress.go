package config

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// 「把某些网段交给一个已经存在的出口」。
//
// ## 它不是什么
//
// **不是 mesh、不是内网接入方案。** 那是 Tailscale 的活,而且 bx 不挡它:私网
// 地址不绑物理网卡(走系统路由表),而 subnet router 通告的 /16 比 bx 那条 /8
// 更具体,最长前缀匹配下它赢。能用 Tailscale 的场合都该用 Tailscale。
//
// 这里解决的是另一件事:**bx 占着这台机器的路由表,所以它必须能说「这个网段
// 不归我管」**。出口是什么、对面是不是内网,bx 一概不关心 —— 它只是一个 SOCKS5。
//
// ## 白名单,而且只有白名单(项目所有者定的)
//
// 只有用户**逐条写出来**的网段才走出口。没有推断、没有「看起来像内网就……」。
// 反过来做的后果是 docker、LAN、SSH 会被一起卷进去,而那正是 bx 用一条
// 「私网恒直连」不变量守着的东西。

// Egress 是一个具名出口。
type Egress struct {
	Name string `yaml:"name"`
	// Socks5 是出口的 SOCKS5 地址。**必须是 loopback**,理由见 Validate。
	Socks5 string `yaml:"socks5"`
}

// Validate 在**加载期**把话说死。
//
// 加载期校验是刻意的:一个悄悄没生效的出口规则,表现是「打开那个地址转圈然后
// 失败」——与配错了地址、与内网真的不通,三者在用户眼里完全一样。bx 的 hosts:
// 覆盖也是同一条纪律(「悄悄没生效正是本功能要消灭的困惑」)。
func (e Egress) Validate() error {
	name := strings.TrimSpace(e.Name)
	if name == "" {
		return fmt.Errorf("egress: name 不能为空")
	}
	if err := ValidateServerName(name); err != nil {
		return fmt.Errorf("egress %q: %w", name, err)
	}
	addr := strings.TrimSpace(e.Socks5)
	if addr == "" {
		return fmt.Errorf("egress %q: socks5 不能为空", name)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("egress %q: socks5 %q 不是 host:port", name, addr)
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		// **不接受域名。** 域名要解析,而解析在 bx 自己的 DNS 里 —— 一个出口
		// 的地址依赖 bx 的解析,就绕了一个我们不需要的圈。
		return fmt.Errorf("egress %q: socks5 的主机必须是 IP 字面量,got %q", name, host)
	}
	if !ip.IsLoopback() {
		// **只收 loopback,这是 v1 的硬约束。**
		//
		// 非 loopback 的出口地址会被 bx 自己抓走(它劫持了全部公网路由),于是
		// 要么绕一圈经隧道出去、要么在隧道挂掉时被 kill-switch 封死 —— 而用户
		// 看到的是「内网时好时坏」,原因看起来毫无关联。要支持远端出口就得同时
		// 给它装 bypass,那是另一件事,不该塞进第一版。
		//
		// 而 loopback 恰好是自然形态:`ssh -D 1080` 就在 127.0.0.1。
		return fmt.Errorf("egress %q: socks5 目前只支持 loopback 地址(如 127.0.0.1:1080),got %q", name, host)
	}
	if _, perr := netip.ParseAddrPort(net.JoinHostPort(ip.String(), port)); perr != nil {
		return fmt.Errorf("egress %q: socks5 端口不合法:%q", name, port)
	}
	return nil
}

// ValidateEgresses 校验整份清单,并挡掉重名。
//
// 重名必须挡:两条同名规则里,后一条会静默覆盖前一条,而用户会以为两条都在生效。
func ValidateEgresses(list []Egress) error {
	seen := map[string]bool{}
	for _, e := range list {
		if err := e.Validate(); err != nil {
			return err
		}
		key := strings.ToLower(strings.TrimSpace(e.Name))
		if seen[key] {
			return fmt.Errorf("egress %q: 名字重复", e.Name)
		}
		seen[key] = true
	}
	return nil
}

// validateEgressRules 校验 rules 里那些 `via:` 条目。
//
// **指向一个不存在的出口必须当场报错。** 静默忽略的话,用户会看到「我明明配了
// 10.84/16 走 office」而流量照旧走私网直连 —— 而那正是这个功能要消灭的困惑。
func validateEgressRules(rules []Rule, egresses []Egress) error {
	known := map[string]bool{}
	for _, e := range egresses {
		known[strings.ToLower(strings.TrimSpace(e.Name))] = true
	}
	for i, r := range rules {
		via := strings.TrimSpace(r.Via)
		if via == "" {
			// **没有 via 的规则不许带 cidr。** 带了多半是写漏了 via,而静默忽略
			// 会让那几条网段什么都不做。
			if len(r.CIDR) > 0 {
				return fmt.Errorf("rules[%d]: 有 cidr 但没有 via —— cidr 只在 via 规则里有意义", i)
			}
			continue
		}
		if !known[strings.ToLower(via)] {
			return fmt.Errorf("rules[%d]: via %q 不在 egress 清单里", i, r.Via)
		}
		if len(r.CIDR) == 0 {
			// **白名单为空 = 这条规则什么都不做。** 报错而不是接受:一条不生效的
			// 规则留在配置里,只会让人以为它在生效。
			return fmt.Errorf("rules[%d]: via %q 没有 cidr —— 出口规则是白名单,必须逐条写出网段", i, r.Via)
		}
		for _, c := range r.CIDR {
			if _, err := netip.ParsePrefix(strings.TrimSpace(c)); err != nil {
				return fmt.Errorf("rules[%d]: cidr %q 不是合法网段(要写成 10.84.0.0/16 这种)", i, c)
			}
		}
	}
	return nil
}
