package config

import "testing"

// —— 内网 DNS 收一组,而不是一台(2026-09-16)——
//
// 起因是真机配置:`dns.split` 指着 10.0.13.23 一台域控,而那个网段里成对存在的
// 10.0.13.24 用不上 —— schema 只收一个字符串。内网 DNS 双机是企业标配(AD 域控
// 几乎必然两台),而所有人对 resolv.conf 能写多个 nameserver 有强预期。
//
// **`server` 保留,`servers` 新增,两个都写是错误。** 一份把同一件事说两遍的配置
// 正是这个仓库反复罚过的形状 —— 它迟早会漂,而漂了之后没有任何东西会红。
func TestSplitAcceptsAListOfServers(t *testing.T) {
	c, err := Parse([]byte(`
server: brook://x@example.com:9999
dns:
  split:
    - domains: ['*.corp.example']
      servers: ['10.0.13.23', '10.0.13.24:5353']
`))
	if err != nil {
		t.Fatal(err)
	}
	got := c.DNS.Split[0].Servers
	want := []string{"10.0.13.23:53", "10.0.13.24:5353"}
	if len(got) != len(want) {
		t.Fatalf("Servers = %v,want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Servers[%d] = %q,want %q(无端口要补 :53,显式端口要留着)", i, got[i], want[i])
		}
	}
}

// **单个 server 的老配置一个字不用改**,而且下游只读 Servers 这一个字段 ——
// 留两条路给下游各自判断,就是留了一处会漂的地方。
func TestSplitSingleServerStillWorksAndNormalizesIntoTheList(t *testing.T) {
	c, err := Parse([]byte(`
server: brook://x@example.com:9999
dns:
  split:
    - domains: ['*.corp.example']
      server: 10.0.13.23
`))
	if err != nil {
		t.Fatal(err)
	}
	r := c.DNS.Split[0]
	if len(r.Servers) != 1 || r.Servers[0] != "10.0.13.23:53" {
		t.Fatalf("Servers = %v —— 单值必须归一化进列表,下游只读这一个字段", r.Servers)
	}
}

// 两个都写 ⇒ **加载期就拒绝**。悄悄挑一个用,用户不会知道自己拿到的是哪种,
// 而「悄悄没生效」正是这个功能存在要消灭的困惑。
func TestSplitRefusesBothServerAndServers(t *testing.T) {
	_, err := Parse([]byte(`
server: brook://x@example.com:9999
dns:
  split:
    - domains: ['*.corp.example']
      server: 10.0.13.23
      servers: ['10.0.13.24']
`))
	if err == nil {
		t.Fatal("同时写 server 与 servers 被接受了 —— 那是一份把同一件事说两遍的配置")
	}
}

// 一个都不写仍然报错,措辞要把两个键都点出来(用户可能只知道其中一个)。
func TestSplitStillRefusesNoServerAtAll(t *testing.T) {
	_, err := Parse([]byte(`
server: brook://x@example.com:9999
dns:
  split:
    - domains: ['*.corp.example']
`))
	if err == nil {
		t.Fatal("没有 server 也没有 servers,应当报错")
	}
}

// 列表里混进空串 ⇒ 拒绝。一个空条目会在运行期变成一次拨向 ":53" 的失败,
// 而那次失败在日志里与「内网 DNS 真的不通」长得一模一样。
func TestSplitRefusesAnEmptyEntryInTheList(t *testing.T) {
	_, err := Parse([]byte(`
server: brook://x@example.com:9999
dns:
  split:
    - domains: ['*.corp.example']
      servers: ['10.0.13.23', '  ']
`))
	if err == nil {
		t.Fatal("列表里的空条目被接受了")
	}
}
