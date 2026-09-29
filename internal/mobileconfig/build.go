// Package mobileconfig 把 bx 配置拼成一份完整的 libbox(进程内 sing-box)配置,给 iOS 的
// Packet Tunnel 扩展用。路由规则来自 singboxrules(与桌面判定一致由它的守卫钉住);这里只加
// 桌面同构的外壳:fake-IP DNS(internal/dns/server.go 那一套)、tun 入站、三个出站、v6 拒绝。
//
// 出站由调用方给(tunnel.SingboxOutbound,与桌面同一个生成器),本包因此保持纯判据、将来
// 能原样编进手机端。设计与取舍在 docs/superpowers/plans/2026-09-29-mobile-phase2-ios-tunnel.md。
package mobileconfig

import (
	"encoding/json"
	"errors"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/singboxrules"
)

const tunTag = "tun-in"

// Files 是扩展要的全部文件:配置正文,以及 rule-set 文件(文件名 → 内容,放在 libbox 的
// 工作目录下,配置里按相对路径引用)。
type Files struct {
	Config   []byte
	RuleSets map[string][]byte
}

// Build 拼出完整配置。proxy 是代理出站的主体,tag 一律改成 singboxrules.OutboundProxy。
func Build(cfg *config.Config, lists singboxrules.Lists, proxy map[string]any) (Files, error) {
	if cfg == nil {
		return Files{}, errors.New("nil config")
	}
	if len(proxy) == 0 {
		return Files{}, errors.New("no proxy outbound: a config with no way out is not a config")
	}
	b, err := singboxrules.Translate(cfg, lists)
	if err != nil {
		return Files{}, err
	}
	out := make(map[string]any, len(proxy))
	for k, v := range proxy {
		out[k] = v
	}
	out["tag"] = singboxrules.OutboundProxy

	fakeRange := cfg.DNS.FakeipCIDR
	if fakeRange == "" {
		fakeRange = config.DefaultFakeipCIDR
	}

	// 路由:sniff(拿到协议)→ DNS 交给 fake-IP 处理器 → v6 一律拒(桌面 v6 是 fail-closed
	// 阻断)→ singboxrules 翻出来的那一串 → final 代理。
	rules := []any{
		map[string]any{"action": "sniff"},
		map[string]any{"protocol": "dns", "action": "hijack-dns"},
		map[string]any{"ip_version": 6, "action": "reject"},
	}
	for _, r := range b.Route.Rules {
		rules = append(rules, r)
	}

	doc := map[string]any{
		"log": map[string]any{"level": "info"},
		"dns": map[string]any{
			"servers": []any{
				map[string]any{"type": "fakeip", "tag": "fakeip", "inet4_range": fakeRange},
				map[string]any{"type": "local", "tag": "local"},
			},
			// 与桌面同构:A 答假 IP,其余类型空答(NODATA,逼应用走 v4)。两条都只管 tun 来的
			// 查询 —— 直连出站自己解析域名走 default_domain_resolver,不许拿到假 IP。
			"rules": []any{
				map[string]any{"inbound": []string{tunTag}, "query_type": []string{"A"}, "server": "fakeip"},
				map[string]any{"inbound": []string{tunTag}, "query_type": []string{"A"}, "invert": true, "action": "predefined", "rcode": "NOERROR"},
			},
			"final": "local", // sing-box 1.14:fakeip 不能当 final
		},
		"inbounds": []any{map[string]any{
			"type":         "tun",
			"tag":          tunTag,
			"address":      []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"},
			"mtu":          1500,
			"auto_route":   true,
			"strict_route": true,
		}},
		"outbounds": []any{
			out,
			map[string]any{"type": "direct", "tag": singboxrules.OutboundDirect},
			map[string]any{"type": "block", "tag": singboxrules.OutboundBlock},
		},
		"route": map[string]any{
			"rules":                   rules,
			"rule_set":                b.Route.RuleSet,
			"final":                   b.Route.Final,
			"default_domain_resolver": map[string]any{"server": "local", "strategy": "ipv4_only"},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return Files{}, err
	}
	files := Files{Config: raw, RuleSets: map[string][]byte{}}
	for _, ref := range b.Route.RuleSet {
		rs, err := b.RuleSetJSON(ref.Tag)
		if err != nil {
			return Files{}, err
		}
		files.RuleSets[ref.Path] = rs
	}
	return files, nil
}
