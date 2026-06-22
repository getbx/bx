//go:build linux

// router_linux.go 实现 mode=router 的劫持:只把「源在 LANCIDRs 内」的转发流量
// 经策略路由导进 tun(fail-closed blackhole 兜底),并装 fw4 转发放行/IPv6 阻断。
// 路由器自身流量(源是路由器 IP)永不匹配源规则 → 正常路由 → 直连(tailscale 等不受影响)。
package supervisor

import (
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"

	"github.com/getbx/bx/internal/gateway"
)

// hijackRouter 装路由器模式的策略路由 + 防火墙,返回还原函数。
func (linuxPlatform) hijackRouter(t tunHandle) (func(), error) {
	cidrs := t.LANCIDRs
	if len(cidrs) == 0 {
		cidrs = detectLANCIDRs() // 未配 lan_cidrs:从 br-* 私网桥自动探测
		if len(cidrs) > 0 {
			log.Printf("router mode 自动探测到 LAN 网段: %v", cidrs)
		}
	}
	if len(cidrs) == 0 {
		return nil, fmt.Errorf("router mode 需要 router.lan_cidrs(自动探测未找到 br-* 私网桥)")
	}
	if !nftFw4Present() {
		return nil, fmt.Errorf("router mode 目前需要 OpenWrt fw4(nft inet fw4 表缺失)")
	}
	rp := gateway.DefaultRoutePlan(t.Name, cidrs)
	ifaces := lanIfacesFor(cidrs)
	if len(ifaces) == 0 {
		return nil, fmt.Errorf("router mode 未能从 lan_cidrs 探测到 LAN 接口: %v", cidrs)
	}
	fp := gateway.DefaultFirewallPlan(t.Name, ifaces)

	// 接口地址 + up
	if err := runIP("addr", "add", t.Addr, "dev", t.Name); err != nil {
		return nil, err
	}
	if err := runIP("link", "set", t.Name, "up"); err != nil {
		return nil, err
	}
	// 策略路由(含 fail-closed blackhole)
	for _, s := range rp.InstallArgs() {
		if err := runIP(s...); err != nil {
			cleanupRouter(rp, fp, t.Name)
			return nil, err
		}
	}
	// fw4 转发放行 + IPv6 阻断
	for _, r := range fp.InstallRules() {
		if err := runNft(r...); err != nil {
			cleanupRouter(rp, fp, t.Name)
			return nil, err
		}
	}
	log.Printf("router 模式已接管:LAN=%v → %s(其余直连),fail-closed", t.LANCIDRs, t.Name)
	down := func() { cleanupRouter(rp, fp, t.Name) }
	return down, nil
}

// cleanupRouter 尽力还原(忽略单步错误):删源规则 + flush 表 + 按 comment 删 fw4 规则 + 删 tun。
func cleanupRouter(rp gateway.RoutePlan, fp gateway.FirewallPlan, tun string) {
	for _, s := range rp.TeardownArgs() {
		_ = runIPQuiet(s...)
	}
	tblToks := strings.Fields(fp.Table) // 与 nftHandles 用同一张表,避免删错表导致规则残留
	for _, h := range nftHandles(fp.Table, fp.Chain, fp.Comment) {
		args := append([]string{"delete", "rule"}, tblToks...)
		args = append(args, fp.Chain, "handle", itoa(h))
		_ = runNftQuiet(args...)
	}
	_ = runIPQuiet("link", "del", tun)
}

// nftFw4Present 报告 nft inet fw4 表是否存在(OpenWrt 标志)。
func nftFw4Present() bool {
	return exec.Command("nft", "list", "table", "inet", "fw4").Run() == nil
}

// nftHandles 列出指定链中带 comment 的规则 handle(用 `nft -a`)。
func nftHandles(table, chain, comment string) []int {
	out, err := exec.Command("nft", "-a", "list", "chain", table, chain).Output()
	if err != nil {
		return nil
	}
	return parseNftHandles(string(out), comment)
}

// parseNftHandles 纯解析:从 `nft -a list chain` 输出里取带 comment 的行的 handle 号。
func parseNftHandles(listOut, comment string) []int {
	var hs []int
	for _, line := range strings.Split(listOut, "\n") {
		if !strings.Contains(line, "\""+comment+"\"") {
			continue
		}
		i := strings.Index(line, "# handle ")
		if i < 0 {
			continue
		}
		n := 0
		for _, ch := range strings.TrimSpace(line[i+len("# handle "):]) {
			if ch < '0' || ch > '9' {
				break
			}
			n = n*10 + int(ch-'0')
		}
		if n > 0 {
			hs = append(hs, n)
		}
	}
	return hs
}

// detectLANCIDRs 枚举本机接口,交给 gateway.SelectLANCIDRs 选出 LAN 网段
// (br-* 私网桥),用于未配 router.lan_cidrs 时自动探测。
func detectLANCIDRs() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var cands []gateway.IfaceCIDR
	for _, ifc := range ifs {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				cands = append(cands, gateway.IfaceCIDR{Name: ifc.Name, CIDR: ipn.String()})
			}
		}
	}
	return gateway.SelectLANCIDRs(cands)
}

// lanIfacesFor 把每个 lan_cidr 映射到「本机持有该网段内地址」的接口名(如 192.168.8.0/24 → br-lan)。
func lanIfacesFor(cidrs []string) []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, cidr := range cidrs {
		p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
		if err != nil {
			continue
		}
		for _, ifc := range ifs {
			addrs, _ := ifc.Addrs()
			for _, a := range addrs {
				ipn, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				if ad, ok2 := netip.AddrFromSlice(ipn.IP); ok2 && p.Contains(ad.Unmap()) && !seen[ifc.Name] {
					seen[ifc.Name] = true
					out = append(out, ifc.Name)
				}
			}
		}
	}
	return out
}

func runNft(args ...string) error {
	cmd := exec.Command("nft", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nft %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func runNftQuiet(args ...string) error { return exec.Command("nft", args...).Run() }
