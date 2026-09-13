package setup

import (
	"os"
	"strings"
	"testing"
)

const oneServerConfig = `servers:
    - name: alpha
      link: vless://u@1.1.1.1:443?security=reality
      udp: hysteria2://p@1.1.1.1:443
current: alpha
global: true
killswitch: true
rules:
    - direct:
        # 手写的注释必须活下来
        - '*.icloud.com'
`

// **改 current 是「用户自己切」这件事的全部** —— 而在此之前它只能手改 yaml。
func TestSetCurrentServer(t *testing.T) {
	path := writeTemp(t, oneServerConfig+`    - name: beta
      link: vless://u@2.2.2.2:443?security=reality
`)
	// 上面那段拼接会把 beta 缩进错;这里用真实形状重来一遍。
	path = writeTemp(t, `servers:
    - name: alpha
      link: vless://u@1.1.1.1:443?security=reality
    - name: beta
      link: vless://u@2.2.2.2:443?security=reality
current: alpha
global: true
`)
	if err := SetCurrentServer(path, "beta"); err != nil {
		t.Fatal(err)
	}
	list, current, err := ListServers(path)
	if err != nil {
		t.Fatal(err)
	}
	if current != "beta" {
		t.Fatalf("current = %q, want beta", current)
	}
	if len(list) != 2 {
		t.Fatalf("清单被改动了:%d 台", len(list))
	}
	// **无关配置一个字不许动。**
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "global: true") {
		t.Errorf("改 current 动了无关配置:\n%s", raw)
	}
}

// **名字不在清单里必须报错,并且把可选的名字列出来。**
//
// 静默接受会让用户以为切过去了,而重启之后发现还在原来那台 —— 那正是
// 2026-08-06 那次「以为换了服务器其实没换」的形状。
func TestSetCurrentServerRejectsUnknownName(t *testing.T) {
	path := writeTemp(t, oneServerConfig)
	err := SetCurrentServer(path, "nope")
	if err == nil {
		t.Fatal("接受了不存在的名字")
	}
	if !strings.Contains(err.Error(), "alpha") {
		t.Errorf("错误里没有列出可选的名字,用户无从下手:%v", err)
	}
}

// 名字比较不区分大小写(与 config 层一致),但**存回去的是清单里的原样拼写**。
func TestSetCurrentServerIsCaseInsensitiveButKeepsSpelling(t *testing.T) {
	path := writeTemp(t, `servers:
    - name: Alpha
      link: vless://u@1.1.1.1:443?security=reality
    - name: beta
      link: vless://u@2.2.2.2:443?security=reality
current: beta
`)
	if err := SetCurrentServer(path, "ALPHA"); err != nil {
		t.Fatal(err)
	}
	_, current, _ := ListServers(path)
	if current != "Alpha" {
		t.Fatalf("current = %q,应当存回清单里的原样拼写 Alpha", current)
	}
}

// **加一台:名字已存在就就地更新,否则追加;两种情况都把 current 设成它。**
func TestUpsertServer(t *testing.T) {
	path := writeTemp(t, oneServerConfig)
	added, err := UpsertServer(path, "beta", "vless://u@2.2.2.2:443?security=reality", "hysteria2://p@2.2.2.2:443")
	if err != nil || !added {
		t.Fatalf("added=%v err=%v", added, err)
	}
	list, current, _ := ListServers(path)
	if len(list) != 2 || current != "beta" {
		t.Fatalf("清单=%d current=%q", len(list), current)
	}

	// 同名再来一次:就地更新,不追加。
	added, err = UpsertServer(path, "beta", "vless://u@3.3.3.3:443?security=reality", "")
	if err != nil || added {
		t.Fatalf("同名应当是更新而不是追加:added=%v err=%v", added, err)
	}
	list, _, _ = ListServers(path)
	if len(list) != 2 {
		t.Fatalf("同名追加成了新的一台:%d", len(list))
	}
	for _, s := range list {
		if s.Name == "beta" && !strings.Contains(s.Link, "3.3.3.3") {
			t.Errorf("没有就地更新链接:%s", s.Link)
		}
	}
}

// **旧式配置要能迁过来。** 用户手里绝大多数是 `server:` + `udp.transport:`,
// 加第二台时不该要求他先手工改格式。
func TestUpsertMigratesLegacySingleServerConfig(t *testing.T) {
	path := writeTemp(t, `server: vless://u@1.1.1.1:443?security=reality
udp:
    transport: hysteria2://p@1.1.1.1:443
global: true
`)
	if _, err := UpsertServer(path, "beta", "vless://u@2.2.2.2:443?security=reality", ""); err != nil {
		t.Fatal(err)
	}
	list, current, err := ListServers(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("旧配置没被迁成第一台:清单=%d %+v", len(list), list)
	}
	if current != "beta" {
		t.Fatalf("current = %q", current)
	}
	// 旧的那台必须带着它的 UDP 一起迁过来 —— 丢了它,切回去时 UDP 会静默走主传输。
	var legacy *struct{ udp string }
	for _, s := range list {
		if strings.Contains(s.Link, "1.1.1.1") {
			legacy = &struct{ udp string }{s.UDP}
		}
	}
	if legacy == nil || legacy.udp == "" {
		t.Fatal("迁移丢掉了旧配置的 UDP 传输")
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "\nserver:") {
		t.Errorf("迁移后旧的 server: 还留着 —— 它与 servers: 谁说了算会含混:\n%s", raw)
	}
}

// 删掉一台;**不许删掉当前那台而不说**。
func TestRemoveServer(t *testing.T) {
	path := writeTemp(t, `servers:
    - name: alpha
      link: vless://u@1.1.1.1:443?security=reality
    - name: beta
      link: vless://u@2.2.2.2:443?security=reality
current: beta
`)
	if err := RemoveServer(path, "beta"); err == nil {
		t.Fatal("删掉了当前正在用的那台却没报错")
	}
	if err := RemoveServer(path, "alpha"); err != nil {
		t.Fatal(err)
	}
	list, _, _ := ListServers(path)
	if len(list) != 1 {
		t.Fatalf("清单=%d", len(list))
	}
}

// **加一台不等于换过去。**
//
// 刚部署好一台新 VPS 不构成「把我的出口换到那里」的请求 —— 换出口是有后果的事
// (登录态、风控、正在下载的东西),项目所有者明确要求它必须是人显式的一下,
// 这也正是自动容灾被否掉的理由。顺手改 current,就是用另一个入口把它偷偷做了。
func TestAddServerDoesNotStealTheCurrentExit(t *testing.T) {
	path := writeTemp(t, "servers:\n"+
		"    - name: tokyo\n      link: vless://a@203.0.113.10:443\n"+
		"current: tokyo\n")

	added, err := AddServer(path, "osaka", "vless://b@203.0.113.20:443", "")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("没报告成新加的一台")
	}
	list, current, err := ListServers(path)
	if err != nil {
		t.Fatal(err)
	}
	if current != "tokyo" {
		t.Fatalf("current 被改成了 %q —— 加一台把用户的出口换掉了", current)
	}
	if len(list) != 2 {
		t.Fatalf("清单长度 = %d, want 2", len(list))
	}
	// 更新同名的那一台也一样不许改 current。
	if _, err := AddServer(path, "osaka", "vless://c@203.0.113.21:443", ""); err != nil {
		t.Fatal(err)
	}
	if _, current, _ = ListServers(path); current != "tokyo" {
		t.Fatalf("更新一台之后 current 变成了 %q", current)
	}
}

// 但**一份清单必须有一台在用**:本来就没有 current 时,新加的那台就是 current,
// 否则配置指向一个空名字,下次启动直接起不来。
func TestAddServerFillsAnEmptyCurrent(t *testing.T) {
	path := writeTemp(t, "global: true\n")
	if _, err := AddServer(path, "tokyo", "vless://a@203.0.113.10:443", ""); err != nil {
		t.Fatal(err)
	}
	if _, current, _ := ListServers(path); current != "tokyo" {
		t.Fatalf("current = %q,空清单加第一台之后它必须有值", current)
	}
}

// UpsertServer 仍然会把 current 换过去 —— 它服务的是 `bx setup`(「用这一台」),
// 与 AddServer 是两个意图。合并它们会让其中一个悄悄改变行为。
func TestUpsertStillSwitchesBecauseThatIsItsJob(t *testing.T) {
	path := writeTemp(t, "servers:\n"+
		"    - name: tokyo\n      link: vless://a@203.0.113.10:443\n"+
		"current: tokyo\n")
	if _, err := UpsertServer(path, "osaka", "vless://b@203.0.113.20:443", ""); err != nil {
		t.Fatal(err)
	}
	if _, current, _ := ListServers(path); current != "osaka" {
		t.Fatalf("current = %q, want osaka", current)
	}
}

// **ReplaceServerLink 任何情况下都不动 current,连「本来是空的」也不填。**
//
// 这是它与 AddServer 唯一的区别:一份没有 current: 的清单**照样在跑**
// (config.resolveServers 回落 servers[0]),顺手把它填上就是多写了一个用户
// 没写过的键 —— 从此 RemoveServer 会拒绝删掉那一台,而用户只是换了一条链接。
// 「顺手填上 current」那条理由只对**加一台**成立(那时清单可能是空的,
// 而把此刻在用的那一台写明白是有意义的)。
func TestReplaceServerLinkNeverTouchesCurrent(t *testing.T) {
	for _, tc := range []struct {
		name string
		head string
		want string
	}{
		{"有 current", "current: alpha\n", "alpha"},
		{"没有 current —— 这份配置照样在跑,用的是清单里第一台", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, `servers:
    - name: alpha
      link: vless://u@1.1.1.1:443?security=reality
    - name: beta
      link: vless://u@2.2.2.2:443?security=reality
      udp: hysteria2://p@2.2.2.2:443
`+tc.head)
			if err := ReplaceServerLink(path, "beta", "vless://u@9.9.9.9:8443?security=reality", ""); err != nil {
				t.Fatal(err)
			}
			list, current, err := ListServers(path)
			if err != nil {
				t.Fatal(err)
			}
			if current != tc.want {
				t.Fatalf("current = %q, want %q —— 换链接不构成换出口的请求", current, tc.want)
			}
			if list[1].Link != "vless://u@9.9.9.9:8443?security=reality" {
				t.Fatalf("链接没换:%q", list[1].Link)
			}
			if list[0].Link != "vless://u@1.1.1.1:443?security=reality" {
				t.Fatalf("另一台被连累了:%q", list[0].Link)
			}
		})
	}
}

// 名字不在清单里必须**报错**,绝不「顺手加一台」—— 敲错一个字母就凭空多出
// 一台顶着新链接的服务器,而调用方只会看到成功。
func TestReplaceServerLinkRefusesAnUnknownName(t *testing.T) {
	path := writeTemp(t, `servers:
    - name: alpha
      link: vless://u@1.1.1.1:443?security=reality
current: alpha
`)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReplaceServerLink(path, "beta", "vless://u@9.9.9.9:443", ""); err == nil {
		t.Fatal("换了一台不存在的服务器却没报错")
	}
	after, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(before) != string(after) {
		t.Fatalf("被拒绝的替换动了盘上的配置:\n%s", after)
	}
	// 空链接同样是坏输入:写进去之后那台服务器再也连不上,而错误发生在读配置时。
	if err := ReplaceServerLink(path, "alpha", "   ", ""); err == nil {
		t.Fatal("空链接被接受了")
	}
}

// UDP 那一格由参数说了算:给了就换,给空就删掉 —— 「省略即保留」是**调用方**
// 的判断(Guardian 的 replace 就是这么做的),不该藏在这一层里。
func TestReplaceServerLinkTakesTheUDPArgumentLiterally(t *testing.T) {
	path := writeTemp(t, `servers:
    - name: alpha
      link: vless://u@1.1.1.1:443?security=reality
      udp: hysteria2://p@1.1.1.1:443
current: alpha
`)
	if err := ReplaceServerLink(path, "alpha", "vless://u@9.9.9.9:443", "hysteria2://p@9.9.9.9:443"); err != nil {
		t.Fatal(err)
	}
	if list, _, _ := ListServers(path); list[0].UDP != "hysteria2://p@9.9.9.9:443" {
		t.Fatalf("给了 UDP 却没换:%q", list[0].UDP)
	}
	if err := ReplaceServerLink(path, "alpha", "vless://u@9.9.9.9:443", ""); err != nil {
		t.Fatal(err)
	}
	if list, _, _ := ListServers(path); list[0].UDP != "" {
		t.Fatalf("给了空 UDP 却没删:%q", list[0].UDP)
	}
}

// **加一台不许改变「现在在用哪一台」—— 包括 current 那一格本来是空的时候。**
//
// 这条与 TestAddServerDoesNotStealTheCurrentExit 不是同一件事,而差别正是缺陷
// 藏身的地方:那条测的是 current 已经写着名字的清单,这条测的是 current **空着**
// 的两种真实形状 —— ①`bx setup` 写出来的 legacy `server:`(CLAUDE.md 称它
// 「最常见那种配置」),迁移建清单时不写 current;②手改出来的、只有 servers: 没有
// current: 的清单(replaceServerLink 的注释点名它「恰好就是这个功能的受众」)。
// 两种都**照样在跑**,用的是 config.resolveServers 的回落:servers[0]。
//
// 所以 current 空着时要填的是**此刻实际在用的那一台**,不是刚加的那一台。填错
// 不会当场报错、也不热切,要到下一次 `bx up` 才发作 —— 比立刻切更难归因。
func TestAddServerNeverMovesTheServerInUseWhenCurrentIsBlank(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string // 加完之后 current 必须是它
	}{
		{
			name: "bx setup 写的 legacy server:(迁移建清单时不写 current)",
			body: "global: true\nkillswitch: true\nserver: vless://a@203.0.113.10:443\n" +
				"udp:\n    transport: hysteria2://a@203.0.113.10:443\n",
			want: "203.0.113.10", // config.DeriveServerName 从链接取的主机名
		},
		{
			name: "手改出来的清单:有 servers 没有 current",
			body: "servers:\n" +
				"    - name: tokyo\n      link: vless://a@203.0.113.10:443\n" +
				"    - name: paris\n      link: vless://c@203.0.113.30:443\n",
			want: "tokyo",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, tc.body)
			if _, err := AddServer(path, "osaka", "vless://b@203.0.113.20:443", ""); err != nil {
				t.Fatal(err)
			}
			list, current, err := ListServers(path)
			if err != nil {
				t.Fatal(err)
			}
			if current != tc.want {
				t.Fatalf("current = %q, want %q —— 加一台把用户的出口挪走了", current, tc.want)
			}
			// 出口那台的链接也要原样在:换掉它同样是换出口,只是换了个形状。
			if list[0].Name != tc.want {
				t.Fatalf("清单第一台是 %q, want %q", list[0].Name, tc.want)
			}
			if list[0].Link != "vless://a@203.0.113.10:443" {
				t.Fatalf("在用那台的链接被动了:%q", list[0].Link)
			}
			if len(list) < 2 || list[len(list)-1].Name != "osaka" {
				t.Fatalf("新加的那台没落在清单末尾:%+v", list)
			}
		})
	}
}
