package supervisor

import "testing"

// shouldBindToDevice 决定一条直连是否额外 SO_BINDTODEVICE 绑物理出口网卡。
// 仅公网目的地绑——绑设备让直连免疫宿主 mangle OUTPUT 的 `CONNMARK --restore-mark`
// 清掉 SO_MARK 的情况(如 QNAP QTS):mark 被清后 fwmark 规则失配,直连包会落回 tun 自环。
// 私网/docker/CGNAT/loopback/link-local 已被策略路由 carve 到主表原生投递(lo/docker0/网关),
// 绑物理口反而连不通,故一律不绑。无法解析的地址保守不绑(退化为仅 SO_MARK,与旧行为一致)。
func TestShouldBindToDevice(t *testing.T) {
	cases := []struct {
		addr string
		want bool
		why  string
	}{
		{"8.8.8.8:443", true, "公网 v4 → 绑"},
		{"114.114.114.114:53", true, "公网 china DNS → 绑(直连也要免疫 mark 清洗)"},
		{"1.2.3.4", true, "无端口的公网 IP → 绑"},
		{"127.0.0.1:8081", false, "loopback(本机 socks)→ 不绑"},
		{"127.0.1.1:53", false, "loopback(dnsmasq/systemd-resolved)→ 不绑"},
		{"10.0.5.2:80", false, "docker 私网 → 不绑"},
		{"172.17.0.1:80", false, "docker 默认池 → 不绑"},
		{"192.168.50.1:80", false, "RFC1918 → 不绑"},
		{"169.254.169.254:80", false, "link-local → 不绑"},
		{"100.64.0.1:80", false, "CGNAT/tailscale overlay → 不绑"},
		{"[2606:4700:4700::1111]:443", true, "公网 v6 → 绑"},
		{"[::1]:443", false, "v6 loopback → 不绑"},
		{"[fe80::1%eno1]:443", false, "v6 link-local(带 zone)→ 不绑"},
		{"[fc00::1]:443", false, "v6 ULA 私网 → 不绑"},
		{"224.0.0.251:5353", false, "v4 组播(mDNS)→ 不绑"},
		{"0.0.0.0:0", false, "未指定地址 → 不绑"},
		{"not-an-ip:80", false, "无法解析 → 保守不绑"},
		{"", false, "空 → 不绑"},
	}
	for _, c := range cases {
		if got := shouldBindToDevice(c.addr); got != c.want {
			t.Errorf("shouldBindToDevice(%q)=%v want %v (%s)", c.addr, got, c.want, c.why)
		}
	}
}
