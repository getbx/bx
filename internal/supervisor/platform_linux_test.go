//go:build linux

package supervisor

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/route"
)

// 私网/docker 段必须在 bx up 时由 bx 自己装 ip rule 送主表(pref 150 < 全量进 tun 的 200),
// 让宿主机访问 docker 容器/内网的包走内核原路由 native 投递、绕开 tun。
// 这条规则随 bx up 重装,解决「手动 ip rule 在 VPN 重连后丢失」的非持久问题。
func TestUpStepsPrivateToMainTable(t *testing.T) {
	nc := &netConf{
		tunName: "bx0", tunAddr: "198.51.100.1/30",
		gw: "10.0.14.1", gwDev: "eno1",
		bypass:     []string{"10.0.0.0/16"},
		mainLookup: []string{"172.16.0.0/12", "192.168.0.0/16"},
	}
	steps := nc.upSteps()

	want := map[string]bool{
		"rule add to 172.16.0.0/12 pref 150 table main":  false,
		"rule add to 192.168.0.0/16 pref 150 table main": false,
	}
	hasCatchAll := false
	for _, s := range steps {
		j := strings.Join(s, " ")
		if _, ok := want[j]; ok {
			want[j] = true
		}
		if j == "rule add pref 200 table "+itoa(routeTable) {
			hasCatchAll = true
		}
	}
	for rule, ok := range want {
		if !ok {
			t.Errorf("缺少私网→主表规则: %q", rule)
		}
	}
	// 全量进 tun 的 pref 200 仍在;private 用 150 < 200,故先被本地分流命中。
	if !hasCatchAll {
		t.Error("缺少 pref 200 全量进 tun 的兜底规则")
	}
}

// down 必须对称清掉自己装的私网规则(否则残留)。
func TestDownStepsRemovesPrivate(t *testing.T) {
	nc := &netConf{
		tunName: "bx0", mainLookup: []string{"172.16.0.0/12"},
	}
	var found bool
	for _, s := range nc.downSteps() {
		if strings.Join(s, " ") == "rule del to 172.16.0.0/12 pref 150 table main" {
			found = true
		}
	}
	if !found {
		t.Error("down 未清理私网→主表规则")
	}
}

// Run 应把内建私网段(DefaultPrivateCIDRs)灌进 netConf.mainLookup。
func TestDefaultPrivateCIDRsWiredIn(t *testing.T) {
	if len(route.DefaultPrivateCIDRs) == 0 {
		t.Fatal("DefaultPrivateCIDRs 不应为空")
	}
}

// stepSet 把 upSteps/downSteps 拍平成「空格连接的命令字符串」集合,便于断言某条规则在不在。
func stepSet(steps [][]string) map[string]bool {
	m := make(map[string]bool, len(steps))
	for _, s := range steps {
		m[strings.Join(s, " ")] = true
	}
	return m
}

// v6 启用时(blockV6=true),bx up 必须 fail-closed 地阻断全局 IPv6:装 `unreachable` 默认路由
// (回 EHOSTUNREACH,让双栈应用快速回落 v4)、pref 200 全量进阻断表;同时 v6 私网/链路本地
// (mainLookupV6)carve-out 走主表直连、bx 自身 v6 出站经 fwmark 旁路(防自锁)。v6 步骤一律 `-6` 前缀。
func TestUpStepsUnreachableV6Default(t *testing.T) {
	nc := &netConf{
		tunName: "bx0", tunAddr: "198.51.100.1/30",
		gw: "10.0.14.1", gwDev: "eno1",
		bypass:       []string{"10.0.0.0/16"},
		mainLookup:   []string{"172.16.0.0/12"},
		blockV6:      true,
		mainLookupV6: []string{"fc00::/7", "fe80::/10"},
	}
	got := stepSet(nc.upSteps())

	want := []string{
		"-6 rule add pref 100 fwmark " + fmtMark(fwMark) + " table main", // bx 自身 v6 防环旁路
		"-6 rule add to fc00::/7 pref 150 table main",                    // v6 私网 carve-out
		"-6 rule add to fe80::/10 pref 150 table main",                   // v6 链路本地 carve-out
		"-6 route add unreachable default table " + itoa(routeTable),     // 全局 v6 → 不可达(EHOSTUNREACH)
		"-6 rule add pref 200 table " + itoa(routeTable),                 // 全量 v6 进阻断表
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("upSteps 缺少 v6 阻断规则: %q", w)
		}
	}
	// 选 unreachable 而非 blackhole:blackhole 回 EINVAL,部分应用不回落 v4。
	if got["-6 route add blackhole default table "+itoa(routeTable)] {
		t.Error("v6 默认路由应为 unreachable(回 EHOSTUNREACH),不是 blackhole(回 EINVAL,妨碍 v4 回落)")
	}
	// v4 私网规则零回归。
	if !got["rule add to 172.16.0.0/12 pref 150 table main"] {
		t.Error("v6 改动不应影响 v4 私网→主表规则")
	}
}

// v6 内核禁用时(blockV6=false),upSteps 必须一条 `-6` 都不产 —— 否则 `ip -6` 在这类机器上
// 报错,连累整个 bx up(含能用的 v4)起不来。这是对 v6-disabled 主机的零回归保证。
func TestUpStepsSkipsV6WhenDisabled(t *testing.T) {
	nc := &netConf{
		tunName: "bx0", tunAddr: "198.51.100.1/30",
		gw: "10.0.14.1", gwDev: "eno1",
		mainLookup:   []string{"172.16.0.0/12"},
		blockV6:      false,
		mainLookupV6: []string{"fc00::/7"}, // 即便填了,blockV6=false 也不该产 v6 步骤
	}
	for _, s := range nc.upSteps() {
		if len(s) > 0 && s[0] == "-6" {
			t.Errorf("blockV6=false 时不应产出 v6 步骤,却有: %q", strings.Join(s, " "))
		}
	}
	// v4 步骤仍在。
	if !stepSet(nc.upSteps())["rule add to 172.16.0.0/12 pref 150 table main"] {
		t.Error("v6 禁用不应影响 v4 私网规则")
	}
}

// down 必须对称清掉自己装的 v6 阻断规则并 flush v6 阻断表(否则残留 / 锁死 v6);
// 同样仅 blockV6=true 时产 v6 还原步骤。
func TestDownStepsRemovesV6(t *testing.T) {
	nc := &netConf{
		tunName:      "bx0",
		mainLookup:   []string{"172.16.0.0/12"},
		blockV6:      true,
		mainLookupV6: []string{"fc00::/7", "fe80::/10"},
	}
	got := stepSet(nc.downSteps())

	want := []string{
		"-6 rule del pref 200 table " + itoa(routeTable),
		"-6 rule del to fc00::/7 pref 150 table main",
		"-6 rule del to fe80::/10 pref 150 table main",
		"-6 rule del pref 100 fwmark " + fmtMark(fwMark) + " table main",
		"-6 route flush table " + itoa(routeTable),
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("downSteps 缺少 v6 还原规则: %q", w)
		}
	}
}

// Hijack 应把内建 v6 私网段(DefaultPrivateV6CIDRs)灌进 netConf.mainLookupV6(v6 启用时)。
func TestDefaultPrivateV6CIDRsWiredIn(t *testing.T) {
	if len(route.DefaultPrivateV6CIDRs) == 0 {
		t.Fatal("DefaultPrivateV6CIDRs 不应为空(v6 阻断需私网 carve-out)")
	}
}

func TestRouteStepsExcludeDeviceSteps(t *testing.T) {
	nc := &netConf{
		tunName: "bx0", tunAddr: "198.51.100.1/30", gw: "192.168.1.1", gwDev: "eth0",
		bypass: []string{"1.2.3.4/32"}, mainLookup: route.DefaultPrivateCIDRs,
	}
	for _, s := range nc.routeUpSteps() {
		j := strings.Join(s, " ")
		if strings.HasPrefix(j, "addr add") || strings.HasPrefix(j, "link set") || strings.HasPrefix(j, "link del") {
			t.Errorf("routeUpSteps 不应含设备步骤: %q", j)
		}
	}
	for _, s := range nc.routeDownSteps() {
		j := strings.Join(s, " ")
		if strings.HasPrefix(j, "link del") || strings.HasPrefix(j, "link set") || strings.HasPrefix(j, "addr") {
			t.Errorf("routeDownSteps 不应含设备步骤: %q", j)
		}
	}
	if !stepSet(nc.routeUpSteps())["route add default dev bx0 table "+itoa(routeTable)] {
		t.Error("routeUpSteps 缺 default dev bx0")
	}
	if !stepSet(nc.routeUpSteps())["rule add pref 200 table "+itoa(routeTable)] {
		t.Error("routeUpSteps 缺 pref 200")
	}
}

func TestUpDownStepsStillCompose(t *testing.T) {
	nc := &netConf{
		tunName: "bx0", tunAddr: "198.51.100.1/30", gw: "192.168.1.1", gwDev: "eth0",
		mainLookup: route.DefaultPrivateCIDRs,
	}
	up := nc.upSteps()
	if strings.Join(up[0], " ") != "addr add 198.51.100.1/30 dev bx0" || strings.Join(up[1], " ") != "link set bx0 up" {
		t.Fatalf("upSteps 前两步应为设备步骤, got %v / %v", up[0], up[1])
	}
	if len(up) != len(nc.deviceUpSteps())+len(nc.routeUpSteps()) {
		t.Error("upSteps 应 = deviceUpSteps + routeUpSteps 步数之和")
	}
	if !stepSet(nc.downSteps())["link del bx0"] {
		t.Error("downSteps 应含 link del bx0")
	}
	if stepSet(nc.routeDownSteps())["link del bx0"] {
		t.Error("routeDownSteps 不应含 link del bx0")
	}
}

// CGNAT 段(100.64.0.0/10)是 tailscale overlay 网段,其 peer 路由在 tailscale 的 table 52,
// 不在 main。若只 carve 到 main(pref 150),从 bx 主机主动连 tailscale peer 的 TCP 会因 main
// 无路由而漏到物理网卡被丢(ping 走用户态故不暴露)。故 CGNAT 需在 pref 149(< 150)额外送
// table 52:tailscale 在则命中 peer 路由走 tailscale0;tailscale 不在则 table 52 空、内核回落
// 到 pref 150 → main(运营商 CGNAT 直连)。两种场景都正确。
func TestUpStepsCGNATToTailscaleTable(t *testing.T) {
	nc := &netConf{
		tunName: "bx0", tunAddr: "198.51.100.1/30",
		gw: "10.0.14.1", gwDev: "eno1",
		mainLookup: []string{"172.16.0.0/12", "100.64.0.0/10"},
	}
	got := stepSet(nc.upSteps())

	// CGNAT 先送 tailscale table 52(pref 149 < 150 先命中)
	if !got["rule add to 100.64.0.0/10 pref 149 table "+itoa(tailscaleTable)] {
		t.Error("缺少 CGNAT→tailscale table 52 规则(pref 149)")
	}
	// 仍保留 →main 兜底(tailscale 不在 / table 52 无路由时回落)
	if !got["rule add to 100.64.0.0/10 pref 150 table main"] {
		t.Error("应保留 CGNAT→main 兜底规则(pref 150)")
	}
	// 非 CGNAT 私网段不该产 149/table 52 规则
	if got["rule add to 172.16.0.0/12 pref 149 table "+itoa(tailscaleTable)] {
		t.Error("非 CGNAT 段不应送 tailscale table 52")
	}
}

// down 必须对称清掉 CGNAT→table 52 规则(否则残留)。
func TestDownStepsRemovesCGNATTailscale(t *testing.T) {
	nc := &netConf{
		tunName: "bx0", mainLookup: []string{"100.64.0.0/10"},
	}
	if !stepSet(nc.downSteps())["rule del to 100.64.0.0/10 pref 149 table "+itoa(tailscaleTable)] {
		t.Error("down 未清理 CGNAT→tailscale table 52 规则")
	}
}

// parseOnLinkV6Prefixes 从 `ip -6 route show` 输出提取需 carve-out 的 on-link 全局 v6 前缀:
// 有 dev 无 via(连接路由)、属 2000::/3 全局单播、非 default。link-local(fe80)/ULA(fc00)
// 已由 DefaultPrivateV6CIDRs 静态 carve,不重复;default / via 网关路由 / loopback 一律排除。
// 这样同链路用 GUA 寻址的邻居在 bx 阻断全局 v6 时仍可直连(消掉 on-link GUA 局限)。
func TestParseOnLinkV6Prefixes(t *testing.T) {
	out := `2001:db8:1::/64 dev eno1 proto ra metric 100 pref medium
fe80::/64 dev eno1 proto kernel metric 1024 pref medium
default via fe80::1 dev eno1 proto ra metric 1024 pref medium
2001:db8:2::/64 via 2001:db8:1::1 dev eno1 metric 100 pref medium
::1 dev lo proto kernel metric 256 pref medium
2400:abcd::/48 dev enp3s0 proto kernel metric 256 pref medium`

	got := parseOnLinkV6Prefixes(out)
	want := map[string]bool{"2001:db8:1::/64": true, "2400:abcd::/48": true}

	if len(got) != len(want) {
		t.Fatalf("应只提取 2 条 on-link 全局前缀,实得 %v", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("不该包含 %q(应为 on-link 全局单播)", p)
		}
	}
}

// 多 WAN(路由器/双网卡)有多条 default,必须选 metric 最低的内核首选,
// 而非输出里最后一条 —— 否则把隧道走上高 metric 的烂链路(SIM/备份口)。
func TestParseDefaultRouteLowestMetric(t *testing.T) {
	// Mudi 真实输出:wlan4(metric 20,首选) + SIM rmnet_data0(metric 40)
	out := "default via 10.0.6.1 dev wlan4 proto static src 10.0.6.176 metric 20 \n" +
		"default via 10.99.118.45 dev rmnet_data0 proto static src 10.99.118.44 metric 40 mtu 1500 \n"
	gw, dev, err := parseDefaultRoute(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if gw != "10.0.6.1" || dev != "wlan4" {
		t.Fatalf("应选 metric 最低的 wlan4,实得 gw=%s dev=%s", gw, dev)
	}
}

// metric 最低的在后面也要选对(证明是按 metric 不是按顺序)。
func TestParseDefaultRouteLowestMetricReversed(t *testing.T) {
	out := "default via 10.99.118.45 dev rmnet_data0 metric 40 \n" +
		"default via 10.0.6.1 dev wlan4 metric 20 \n"
	gw, dev, _ := parseDefaultRoute(out)
	if gw != "10.0.6.1" || dev != "wlan4" {
		t.Fatalf("逆序下仍应选 metric 20 的 wlan4,实得 gw=%s dev=%s", gw, dev)
	}
}

// 无 metric 字段 = metric 0(内核最优),应压过有 metric 的。
func TestParseDefaultRouteNoMetricIsZero(t *testing.T) {
	out := "default via 10.0.0.1 dev eth0 \n" +
		"default via 10.0.0.2 dev eth1 metric 100 \n"
	gw, dev, _ := parseDefaultRoute(out)
	if gw != "10.0.0.1" || dev != "eth0" {
		t.Fatalf("无 metric(=0)应最优,实得 gw=%s dev=%s", gw, dev)
	}
}

// 单 default(笔记本常态)照常选中。
func TestParseDefaultRouteSingle(t *testing.T) {
	gw, dev, err := parseDefaultRoute("default via 192.168.1.1 dev eno1 proto dhcp metric 100 \n")
	if err != nil || gw != "192.168.1.1" || dev != "eno1" {
		t.Fatalf("单 default 应选中: gw=%s dev=%s err=%v", gw, dev, err)
	}
}

// 无 default → 报错。
func TestParseDefaultRouteNone(t *testing.T) {
	if _, _, err := parseDefaultRoute("10.0.0.0/24 dev eth0 scope link\n"); err == nil {
		t.Fatal("无 default 路由应报错")
	}
}
