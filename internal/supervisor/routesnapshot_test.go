package supervisor

import (
	"reflect"
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
		{familyV4, 0, "", "", "local"},
		{familyV4, 100, "0x162", "", "main"},
		{familyV4, 149, "", "100.64.0.0/10", "52"},
		{familyV4, 150, "", "10.0.0.0/8", "main"},
		{familyV4, 200, "", "", "100"},
		{familyV4, 32766, "", "", "main"},
		{familyV4, 32767, "", "", "default"},
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
		{familyV4, 0, "", "", "local"},
		{familyV4, 32766, "", "", "main"},
	}
	// 当前 = 基线 + bx 装的 3 条(pref 100/150/200)
	current := append(append([]ruleSpec{}, base...),
		ruleSpec{familyV4, 100, "0x162", "", "main"},
		ruleSpec{familyV4, 150, "", "10.0.0.0/8", "main"},
		ruleSpec{familyV4, 200, "", "", "100"},
	)
	toDel, toAdd := diffRules(current, base)
	wantDel := []ruleSpec{
		{familyV4, 100, "0x162", "", "main"},
		{familyV4, 150, "", "10.0.0.0/8", "main"},
		{familyV4, 200, "", "", "100"},
	}
	if !reflect.DeepEqual(toDel, wantDel) {
		t.Fatalf("toDel\n got=%#v\nwant=%#v", toDel, wantDel)
	}
	if len(toAdd) != 0 {
		t.Fatalf("toAdd 应空(基线没被删),got=%#v", toAdd)
	}
}

func TestDiffRulesReAddsDeletedBaseline(t *testing.T) {
	base := []ruleSpec{{familyV4, 150, "", "192.168.0.0/16", "main"}}
	current := []ruleSpec{} // 一条基线规则被(异常)删了
	toDel, toAdd := diffRules(current, base)
	if len(toDel) != 0 {
		t.Fatalf("toDel 应空,got=%#v", toDel)
	}
	if !reflect.DeepEqual(toAdd, base) {
		t.Fatalf("toAdd 应重加被删基线,got=%#v want=%#v", toAdd, base)
	}
}
