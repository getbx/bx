package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// serverUFWRules 由 server 配置推导需要放行的 ufw 规则("443/tcp" 形式)。
// reality 默认附带 hysteria2(tcp+udp 同端口);--tcp-only 只 tcp(cfg.UDPLink 为空即 tcp-only,
// 见 buildServerConfig 的 withHys2 逻辑);hysteria2 只 udp;brook 按 listen 端口 tcp。
// port<=0 时按 443 缺省(与 serverFirewallHintFor 的缺省一致)。
func serverUFWRules(cfg serverConfig) []string {
	switch cfg.Type {
	case "reality":
		port := serverUFWPort(cfg.Port)
		rules := []string{port + "/tcp"}
		if cfg.UDPLink != "" {
			rules = append(rules, port+"/udp")
		}
		return rules
	case "hysteria2":
		return []string{serverUFWPort(cfg.Port) + "/udp"}
	default: // brook
		port := listenPort(cfg.Listen)
		if port == "" {
			port = "443"
		}
		return []string{port + "/tcp"}
	}
}

// serverUFWPort 缺省/非法端口(<=0)按 443,否则原样转字符串。
func serverUFWPort(port int) string {
	if port <= 0 {
		port = 443
	}
	return strconv.Itoa(port)
}

// openUFWRules 逐条 exec `ufw allow <rule>`(rule 形如 "443/tcp"),任一失败即返错、停止后续。
func openUFWRules(rules []string) error {
	for _, rule := range rules {
		cmd := exec.Command("ufw", "allow", rule)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("ufw allow %s: %w", rule, err)
		}
	}
	return nil
}
