package supervisor

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestParseRulesV4(t *testing.T) {
	out := `0:	from all lookup local
100:	from all fwmark 0x162 lookup main
149:	from all to 100.64.0.0/10 lookup 52
150:	from all to 10.0.0.0/8 lookup main
200:	from all lookup 100
32766:	from all lookup main
32767:	from all lookup default
`
	got := parseRules(out, familyV4)
	want := []ruleSpec{
		{familyV4, 0, "", "", "", "local"},
		{familyV4, 100, "0x162", "", "", "main"},
		{familyV4, 149, "", "100.64.0.0/10", "", "52"},
		{familyV4, 150, "", "10.0.0.0/8", "", "main"},
		{familyV4, 200, "", "", "", "100"},
		{familyV4, 32766, "", "", "", "main"},
		{familyV4, 32767, "", "", "", "default"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRules\n got=%#v\nwant=%#v", got, want)
	}
}

func TestParseRulesV6Empty(t *testing.T) {
	if got := parseRules("", familyV6); len(got) != 0 {
		t.Fatalf("空输入应得 0 条,got=%#v", got)
	}
}

func TestParseRoutesTable100(t *testing.T) {
	out := `default dev bx0
10.1.2.3 via 192.168.1.1 dev eth0
`
	got := parseRoutes(out, familyV4)
	want := []routeSpec{
		{familyV4, "", "default", "", "bx0"},
		{familyV4, "", "10.1.2.3", "192.168.1.1", "eth0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRoutes\n got=%#v\nwant=%#v", got, want)
	}
}

func TestParseRoutesV6Unreachable(t *testing.T) {
	out := "unreachable default dev lo metric 1024 \n"
	got := parseRoutes(out, familyV6)
	want := []routeSpec{{familyV6, "unreachable", "default", "", "lo"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRoutes v6\n got=%#v\nwant=%#v", got, want)
	}
}

func TestDiffRules(t *testing.T) {
	base := []ruleSpec{
		{familyV4, 0, "", "", "", "local"},
		{familyV4, 32766, "", "", "", "main"},
	}
	// 当前 = 基线 + bx 装的 3 条(pref 100/150/200)
	current := append(
		append([]ruleSpec{}, base...),
		ruleSpec{familyV4, 100, "0x162", "", "", "main"},
		ruleSpec{familyV4, 150, "", "10.0.0.0/8", "", "main"},
		ruleSpec{familyV4, 200, "", "", "", "100"},
	)
	toDel, toAdd := diffRules(current, base)
	wantDel := []ruleSpec{
		{familyV4, 100, "0x162", "", "", "main"},
		{familyV4, 150, "", "10.0.0.0/8", "", "main"},
		{familyV4, 200, "", "", "", "100"},
	}
	if !reflect.DeepEqual(toDel, wantDel) {
		t.Fatalf("toDel\n got=%#v\nwant=%#v", toDel, wantDel)
	}
	if len(toAdd) != 0 {
		t.Fatalf("toAdd 应空(基线没被删),got=%#v", toAdd)
	}
}

func TestDiffRulesReAddsDeletedBaseline(t *testing.T) {
	base := []ruleSpec{{familyV4, 150, "", "192.168.0.0/16", "", "main"}}
	current := []ruleSpec{} // 一条基线规则被(异常)删了
	toDel, toAdd := diffRules(current, base)
	if len(toDel) != 0 {
		t.Fatalf("toDel 应空,got=%#v", toDel)
	}
	if !reflect.DeepEqual(toAdd, base) {
		t.Fatalf("toAdd 应重加被删基线,got=%#v want=%#v", toAdd, base)
	}
}

func TestRuleArgs(t *testing.T) {
	cases := []struct {
		name string
		verb string
		r    ruleSpec
		want []string
	}{
		{
			"fwmark-add", "add",
			ruleSpec{familyV4, 100, "0x162", "", "", "main"},
			[]string{"rule", "add", "pref", "100", "fwmark", "0x162", "table", "main"},
		},
		{
			"to-add", "add",
			ruleSpec{familyV4, 150, "", "10.0.0.0/8", "", "main"},
			[]string{"rule", "add", "to", "10.0.0.0/8", "pref", "150", "table", "main"},
		},
		{
			"to-tailscale-add", "add",
			ruleSpec{familyV4, 149, "", "100.64.0.0/10", "", "52"},
			[]string{"rule", "add", "to", "100.64.0.0/10", "pref", "149", "table", "52"},
		},
		{
			"plain-del", "del",
			ruleSpec{familyV4, 200, "", "", "", "100"},
			[]string{"rule", "del", "pref", "200", "table", "100"},
		},
		{
			"v6-add", "add",
			ruleSpec{familyV6, 200, "", "", "", "100"},
			[]string{"-6", "rule", "add", "pref", "200", "table", "100"},
		},
	}
	for _, c := range cases {
		if got := ruleArgs(c.verb, c.r); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s: ruleArgs\n got=%v\nwant=%v", c.name, got, c.want)
		}
	}
}

func TestRouteAddArgs(t *testing.T) {
	cases := []struct {
		name string
		r    routeSpec
		want []string
	}{
		{
			"default-dev",
			routeSpec{familyV4, "", "default", "", "bx0"},
			[]string{"route", "add", "default", "dev", "bx0", "table", "100"},
		},
		{
			"bypass",
			routeSpec{familyV4, "", "10.1.2.3", "192.168.1.1", "eth0"},
			[]string{"route", "add", "10.1.2.3", "via", "192.168.1.1", "dev", "eth0", "table", "100"},
		},
		// 内核回显带 "dev lo",但 bx 原始命令没有它;routeAddArgs 应丢弃 via/dev。
		{
			"v6-unreachable",
			routeSpec{familyV6, "unreachable", "default", "", "lo"},
			[]string{"-6", "route", "add", "unreachable", "default", "table", "100"},
		},
	}
	for _, c := range cases {
		if got := routeAddArgs(c.r); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s: routeAddArgs\n got=%v\nwant=%v", c.name, got, c.want)
		}
	}
}

// —— 快照器必须认得 bx 自己装的每一种选择子(2026-09-15,netns 集成台抓到)——
//
// `ip rule del` 要求**所有选择子都对得上**才删得掉。2026-09-04 加的那条
// Tailscale 规则带 `ipproto udp`,而 parseRules 只认 fwmark/to/lookup ——
// 它把 ipproto 丢掉,于是 Restore 重建出的是
// `ip rule del pref 90 fwmark 0x80000 table main`,内核里那条匹配不上,
// 报 exit status 2。
//
// **后果不是一条测试红**:`systemSnapshot.Restore` 正是把机器路由还原回去
// 的那条路。认不出的选择子 ⇒ 删不掉 ⇒ 还原报错**而且规则留在内核里**。
// 一条 bx 自己装上、自己却拆不掉的策略路由,在这个仓库里有前科(孤儿屏障)。

func TestParseRulesKeepsTheIPProtoSelector(t *testing.T) {
	out := "90:\tfrom all fwmark 0x80000 ipproto udp lookup main\n"
	specs := parseRules(out, familyV4)
	if len(specs) != 1 {
		t.Fatalf("解出 %d 条,want 1", len(specs))
	}
	if specs[0].ipproto != "udp" {
		t.Fatalf("ipproto=%q —— 丢掉它,重建出的删除命令就匹配不上内核里那条", specs[0].ipproto)
	}
}

// 重建出来的删除命令必须带上它 —— 解析出来了却不发,与没解析完全一样。
func TestRuleArgsRebuildsTheIPProtoSelector(t *testing.T) {
	args := ruleArgs("del", ruleSpec{family: familyV4, pref: 90, fwmark: "0x80000", ipproto: "udp", table: "main"})
	got := strings.Join(args, " ")
	want := "rule del pref 90 fwmark 0x80000 ipproto udp table main"
	if got != want {
		t.Fatalf("重建出来的是:\n  %s\nwant:\n  %s", got, want)
	}
}

// 没有这个选择子的规则一个字都不许多 —— 多发一个选择子同样匹配不上。
func TestRuleArgsStaysUnchangedWithoutIPProto(t *testing.T) {
	args := ruleArgs("del", ruleSpec{family: familyV4, pref: 200, table: "100"})
	if got := strings.Join(args, " "); got != "rule del pref 200 table 100" {
		t.Fatalf("没有 ipproto 的规则被改了形状:%s", got)
	}
}

// **bx 自己装的每一条规则,快照器都要认得它的选择子。**
//
// 这是防复发那一半:2026-09-04 加 Tailscale 那条规则时,没有任何东西逼着
// 快照器跟上,而两边漂开的后果是「装得上、拆不掉」。判据从**生产那份步骤表**
// 现取,不手抄 —— 手抄一份清单正是这条守卫要消灭的东西。
func TestSnapshotterUnderstandsEverySelectorBxInstalls(t *testing.T) {
	known := map[string]bool{"pref": true, "table": true, "lookup": true, "fwmark": true, "to": true, "ipproto": true}

	src, err := os.ReadFile("platform_linux.go")
	if err != nil {
		t.Fatalf("读不出 platform_linux.go:%v —— 守卫读不懂现在的代码了,先修它", err)
	}
	// 形如 {"rule", "add", "pref", tailscaleUnderlayPref, "fwmark", …, "ipproto", "udp", …}
	// 只取**字面量**那些词:变量名是值(pref 号、掩码),不是选择子的名字。
	lines := regexp.MustCompile(`(?m)^.*\{"(?:-6", ")?rule", "(?:add|del)".*$`).FindAllString(string(src), -1)
	if len(lines) == 0 {
		t.Fatal("一条 ip rule 步骤都没扫到 —— 守卫的锚点漂了,先修它")
	}
	literal := regexp.MustCompile(`"([a-z0-9-]+)"`)
	for _, line := range lines {
		words := literal.FindAllStringSubmatch(line, -1)
		for i, m := range words {
			word := m[1]
			switch word {
			case "rule", "add", "del", "-6":
				continue
			}
			// 选择子后面紧跟的是它的值,跳过一格。
			if i > 0 {
				switch words[i-1][1] {
				case "fwmark", "to", "table", "lookup", "ipproto", "pref":
					continue
				}
			}
			if !known[word] {
				t.Errorf("bx 装的规则里有快照器不认识的选择子 %q —— 认不出就还原不掉,"+
					"而那条规则会留在内核里。整行:%s", word, strings.TrimSpace(line))
			}
		}
	}
}
