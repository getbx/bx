//go:build linux

// router_linux.go 实现 mode=router 的劫持:代理「路由器自身 + LAN 转发」的流量,
// catch-all 优先级(6600)落在 tailscale(5210-5270)与 GL mark 规则(6000/6500)之后,
// 让 tailscale 的 0x80000 传输先绕过(直连),其余(含 tailscale 控制面)走代理——与 mihomo 一致。
// 配 fw4 转发放行 + IPv6 阻断 + fail-closed blackhole。
package supervisor

import (
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/getbx/bx/internal/gateway"
	"github.com/getbx/bx/internal/route"
)

// hijackRouter 装路由器模式的策略路由 + 防火墙,返回还原函数。
func (linuxPlatform) hijackRouter(t tunHandle, serverBypass, userBypass []string) (func(), error) {
	cidrs := t.LANCIDRs
	if len(cidrs) == 0 {
		cidrs = detectLANCIDRs() // 未配 lan_cidrs:从 br-* 私网桥自动探测(仅用于防火墙接口识别)
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
	ifaces := lanIfacesFor(cidrs)
	if len(ifaces) == 0 {
		return nil, fmt.Errorf("router mode 未能从 lan_cidrs 探测到 LAN 接口: %v", cidrs)
	}
	var v6 []string
	if ipv6Enabled() {
		// 路由器自身全局 v6 → unreachable,逼 tailscaled 等回落 v4(其控制面解析成 v6);
		// 私网 v6 + on-link 全局前缀 carve 直连,保邻居发现。
		v6 = append(append([]string{}, route.DefaultPrivateV6CIDRs...), onLinkV6Prefixes()...)
	}
	rp := gateway.DefaultRoutePlan(t.Name, serverBypass, userBypass, route.DefaultPrivateCIDRs, v6)
	fp := gateway.DefaultFirewallPlan(t.Name, ifaces)

	// 自愈:清掉外来代理残留的「抢 LAN」源规则(如 mihomo 的 from <LAN> lookup 1001 pref 6500),
	// 否则它在 catch-all(6600)之前命中,把 LAN 流量导进死表 → 断网/绕过 bx。重启每次都查,幂等。
	clearShadowingLANRules(cidrs)

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
	// fw4 转发放行 + IPv6 阻断。先清掉同名残留(上次 insert / fw4 reload 把 include 加回的),保证幂等不重复
	deleteNftByComment(fp)
	for _, r := range fp.InstallRules() {
		if err := runNft(r...); err != nil {
			cleanupRouter(rp, fp, t.Name)
			return nil, err
		}
	}
	// 把同一组规则落成 fw4 chain-pre include:`fw4 reload` 会 flush inet fw4 把上面 insert 的规则冲掉
	// (→ IPv6 阻断没了=泄漏,LAN→tun accept 没了=断网),include 让其在每次 reload 重建时自动加回。
	writeFw4Include(fp)
	log.Printf("router 模式已接管:路由器自身 + LAN(ifaces=%v)→ %s,tailscale 绕过,fail-closed,抗 fw4 reload", ifaces, t.Name)
	down := func() { cleanupRouter(rp, fp, t.Name) }
	return down, nil
}

// cleanupRouter 尽力还原(忽略单步错误):删源规则 + flush 表 + 删 fw4 规则 + 删 include + 删 tun。
func cleanupRouter(rp gateway.RoutePlan, fp gateway.FirewallPlan, tun string) {
	for _, s := range rp.TeardownArgs() {
		_ = runIPQuiet(s...)
	}
	deleteNftByComment(fp) // 删运行期 insert 的规则
	removeFw4Include(fp)   // 删持久 include(否则下次 fw4 reload 又把规则加回)
	_ = runIPQuiet("link", "del", tun)
}

// deleteNftByComment 按 comment 删指定链里所有带标记的规则(运行期 insert 或 reload 加回的 include 规则)。
func deleteNftByComment(fp gateway.FirewallPlan) {
	tblToks := strings.Fields(fp.Table) // 与 nftHandles 用同一张表,避免删错表导致规则残留
	for _, h := range nftHandles(fp.Table, fp.Chain, fp.Comment) {
		args := append([]string{"delete", "rule"}, tblToks...)
		args = append(args, fp.Chain, "handle", itoa(h))
		_ = runNftQuiet(args...)
	}
}

// fw4IncludePath 返回 fw4 chain-pre include 的落盘路径:
// /usr/share/nftables.d/chain-pre/<chain>/10-bxr.nft —— fw4 会把它拼到该链最前面。
func fw4IncludePath(chain string) string {
	return filepath.Join("/usr/share/nftables.d/chain-pre", chain, "10-bxr.nft")
}

// writeFw4Include 把 fail-closed 规则写成 fw4 chain-pre include,使其在 `fw4 reload` 重建 ruleset
// 时自动重新加入。写失败不致命:运行期 insert 仍生效,只是丢失「抗 fw4 reload」的持久性。
func writeFw4Include(fp gateway.FirewallPlan) {
	path := fw4IncludePath(fp.Chain)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("router 模式:建 fw4 include 目录失败(规则仍由运行期 insert 生效,但不抗 fw4 reload): %v", err)
		return
	}
	body := "# bx router-mode fail-closed rules — auto re-applied at top of " + fp.Chain +
		" on every fw4 reload. Managed by bx; do not edit.\n" +
		strings.Join(fp.IncludeRules(), "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		log.Printf("router 模式:写 fw4 include %s 失败(规则仍由运行期 insert 生效,但不抗 fw4 reload): %v", path, err)
		return
	}
	log.Printf("router 模式:已装 fw4 chain-pre include %s(抗 fw4 reload)", path)
}

// removeFw4Include 删掉 chain-pre include 文件(teardown 时,避免下次 reload 又加回规则)。
func removeFw4Include(fp gateway.FirewallPlan) { _ = os.Remove(fw4IncludePath(fp.Chain)) }

// clearShadowingLANRules 删掉所有「在 catch-all 之前抢 LAN 流量」的外来源规则(见 gateway.ShadowingLANRules)。
func clearShadowingLANRules(lanCIDRs []string) {
	out, err := exec.Command("ip", "rule", "show").Output()
	if err != nil {
		return
	}
	for _, del := range gateway.ShadowingLANRules(string(out), lanCIDRs) {
		if runIPQuiet(del...) == nil {
			log.Printf("router 模式:清掉抢 LAN 的残留路由规则: ip %s", strings.Join(del, " "))
		}
	}
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
