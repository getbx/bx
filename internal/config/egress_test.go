package config

import (
	"strings"
	"testing"
)

// **加载期把话说死。** 一个悄悄没生效的出口规则,表现与「配错了地址」、与
// 「内网真的不通」在用户眼里完全一样 —— 而那正是这个功能要消灭的困惑。
func TestEgressRejectedAtLoadTime(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want string
	}{
		{
			"出口没名字",
			"server: vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=reality\negress:\n    - socks5: 127.0.0.1:1080\n",
			"name 不能为空",
		},
		{
			"出口没地址",
			"server: vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=reality\negress:\n    - name: office\n",
			"socks5 不能为空",
		},
		{
			"出口用域名(要靠 bx 自己的解析,绕一个不需要的圈)",
			"server: vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=reality\negress:\n    - name: office\n      socks5: proxy.local:1080\n",
			"必须是 IP 字面量",
		},
		{
			"出口不是 loopback(会被 bx 自己抓走)",
			"server: vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=reality\negress:\n    - name: office\n      socks5: 10.0.0.5:1080\n",
			"只支持 loopback",
		},
		{
			"两个同名出口(后一条会静默覆盖前一条)",
			"server: vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=reality\negress:\n    - name: office\n      socks5: 127.0.0.1:1080\n" +
				"    - name: office\n      socks5: 127.0.0.1:1081\n",
			"名字重复",
		},
		{
			"via 指向不存在的出口",
			"server: vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=reality\negress:\n    - name: office\n      socks5: 127.0.0.1:1080\n" +
				"rules:\n    - via: nowhere\n      cidr: ['10.84.0.0/16']\n",
			"不在 egress 清单里",
		},
		{
			"via 没有 cidr(白名单为空 = 什么都不做)",
			"server: vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=reality\negress:\n    - name: office\n      socks5: 127.0.0.1:1080\n" +
				"rules:\n    - via: office\n",
			"必须逐条写出网段",
		},
		{
			"cidr 写错形状",
			"server: vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=reality\negress:\n    - name: office\n      socks5: 127.0.0.1:1080\n" +
				"rules:\n    - via: office\n      cidr: ['10.84.3.239']\n",
			"不是合法网段",
		},
		{
			"有 cidr 却漏了 via(那几段什么都不做)",
			"server: vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=reality\nrules:\n    - cidr: ['10.84.0.0/16']\n",
			"有 cidr 但没有 via",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatal("这份配置被接受了 —— 用户会以为它在生效")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误没说清原因(want 含 %q):%v", tc.want, err)
			}
		})
	}
}

// 正常配置要能通过,否则上面那组可以靠「什么都拒绝」满足。
func TestEgressAccepted(t *testing.T) {
	c, err := Parse([]byte("server: vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=reality\n" +
		"egress:\n    - name: office\n      socks5: 127.0.0.1:1080\n" +
		"rules:\n    - via: office\n      cidr: ['10.84.0.0/16', '10.85.0.0/16']\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Egress) != 1 || c.Egress[0].Socks5 != "127.0.0.1:1080" {
		t.Fatalf("出口没解出来:%+v", c.Egress)
	}
	if len(c.Rules) != 1 || c.Rules[0].Via != "office" || len(c.Rules[0].CIDR) != 2 {
		t.Fatalf("规则没解出来:%+v", c.Rules)
	}
}

// 完全不用这个功能的配置一个字都不受影响 —— 绝大多数配置属于这一类。
func TestConfigsWithoutEgressAreUntouched(t *testing.T) {
	c, err := Parse([]byte("server: vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=reality\nrules:\n    - direct: ['*.icloud.com']\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Egress) != 0 {
		t.Fatalf("凭空多出了出口:%+v", c.Egress)
	}
}
