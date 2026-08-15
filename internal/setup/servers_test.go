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
