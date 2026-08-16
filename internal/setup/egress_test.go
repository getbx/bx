package setup

import (
	"os"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
)

const egressBaseConfig = `server: vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=reality
global: true
rules:
    - direct:
        # 手写的注释必须活下来
        - '*.icloud.com'
`

// **改完之后配置要能被生产那份解析器读回来。**
//
// 「yaml 能解析」不算数 —— 规则那边栽过:带 `*` 的值不加引号在 YAML 里是别名
// 语法,写出去读回来直接报错。这里一律走 config.Parse 整个读一遍。
func mustReparse(t *testing.T, path string) *config.Config {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("改完的配置读不回来了:%v\n%s", err, raw)
	}
	return cfg
}

func TestAddEgressAndRoute(t *testing.T) {
	path := writeTemp(t, egressBaseConfig)
	if err := AddEgress(path, "office", "127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
	if err := RouteViaEgress(path, "office", "10.84.0.0/16"); err != nil {
		t.Fatal(err)
	}
	cfg := mustReparse(t, path)
	if len(cfg.Egress) != 1 || cfg.Egress[0].Socks5 != "127.0.0.1:1080" {
		t.Fatalf("出口没写进去:%+v", cfg.Egress)
	}
	// **用户手写的注释与既有规则必须活下来**(yaml.Node 外科手术的全部理由)。
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "手写的注释必须活下来") {
		t.Fatalf("注释被冲掉了:\n%s", raw)
	}
	if !strings.Contains(string(raw), "*.icloud.com") {
		t.Fatalf("既有规则被冲掉了:\n%s", raw)
	}

	list, err := ListEgresses(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "office" || len(list[0].CIDR) != 1 || list[0].CIDR[0] != "10.84.0.0/16" {
		t.Fatalf("读回来的清单不对:%+v", list)
	}
}

// 幂等:重复加不报错、也不写出重复项。
func TestAddEgressIsIdempotent(t *testing.T) {
	path := writeTemp(t, egressBaseConfig)
	for i := 0; i < 3; i++ {
		if err := AddEgress(path, "office", "127.0.0.1:1080"); err != nil {
			t.Fatal(err)
		}
		if err := RouteViaEgress(path, "office", "10.84.0.0/16"); err != nil {
			t.Fatal(err)
		}
	}
	cfg := mustReparse(t, path)
	if len(cfg.Egress) != 1 {
		t.Fatalf("出口重复了 %d 条", len(cfg.Egress))
	}
	list, _ := ListEgresses(path)
	if len(list[0].CIDR) != 1 {
		t.Fatalf("网段重复了:%v", list[0].CIDR)
	}
}

// 同名再 add 是**更新地址**,不是加第二条。
func TestAddEgressUpdatesTheAddress(t *testing.T) {
	path := writeTemp(t, egressBaseConfig)
	if err := AddEgress(path, "office", "127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
	if err := AddEgress(path, "office", "127.0.0.1:1081"); err != nil {
		t.Fatal(err)
	}
	cfg := mustReparse(t, path)
	if len(cfg.Egress) != 1 || cfg.Egress[0].Socks5 != "127.0.0.1:1081" {
		t.Fatalf("%+v", cfg.Egress)
	}
}

// **非法输入在写盘之前挡掉** —— 断言盘上文件一个字节没动。
//
// 判据用的是生产那份 config.Egress.Validate,不另写一套:两份会漂,而漂开时
// CLI 会收下一份让 `bx up` 起不来的配置。
func TestBadInputNeverTouchesTheFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(path string) error
	}{
		{"出口地址不是 loopback", func(p string) error { return AddEgress(p, "office", "10.0.0.5:1080") }},
		{"出口地址是域名", func(p string) error { return AddEgress(p, "office", "proxy.local:1080") }},
		{"出口名字非法", func(p string) error { return AddEgress(p, "of fice", "127.0.0.1:1080") }},
		{"网段写错形状", func(p string) error { return RouteViaEgress(p, "office", "10.84.3.239") }},
		{"给不存在的出口加网段", func(p string) error { return RouteViaEgress(p, "nowhere", "10.84.0.0/16") }},
		{"删不存在的出口", func(p string) error { return RemoveEgress(p, "nowhere") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, egressBaseConfig)
			before, _ := os.ReadFile(path)
			if err := tc.run(path); err == nil {
				t.Fatal("非法输入被接受了")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("被拒绝的操作改动了盘上的配置")
			}
		})
	}
}

// **删出口必须连它的网段一起删。**
//
// 只删出口、留下 `via` 规则的话,配置在加载期会被拒(via 指向不存在的出口)——
// 那等于这条命令把用户的 bx 弄成起不来。
func TestRemoveEgressAlsoRemovesItsRoutes(t *testing.T) {
	path := writeTemp(t, egressBaseConfig)
	if err := AddEgress(path, "office", "127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
	if err := RouteViaEgress(path, "office", "10.84.0.0/16"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveEgress(path, "office"); err != nil {
		t.Fatal(err)
	}
	cfg := mustReparse(t, path) // 读不回来就说明留下了孤儿 via
	if len(cfg.Egress) != 0 {
		t.Fatalf("出口没删掉:%+v", cfg.Egress)
	}
	for _, r := range cfg.Rules {
		if strings.TrimSpace(r.Via) != "" {
			t.Fatalf("留下了孤儿 via 规则:%+v", r)
		}
	}
	// 用户原来那条 direct 规则不受连累。
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "*.icloud.com") {
		t.Fatalf("把用户自己的规则也删了:\n%s", raw)
	}
}
