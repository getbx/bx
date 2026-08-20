package appattr

import (
	"reflect"
	"testing"
)

func TestAggregateSplitsOneAppAcrossPaths(t *testing.T) {
	// Chrome 一部分域名直连、一部分走隧道,是常态。压成一行「混合」会把最有用的
	// 那一半信息扔掉 —— 腾讯会议那次要看的恰恰是「它只出现在 tunnel 组里」。
	records := []ConnRecord{
		{SrcPort: 1, Path: PathTunnel, Source: "default"},
		{SrcPort: 2, Path: PathDirect, Source: "user_direct", Rule: "*.qq.com"},
	}
	owners := map[uint16]string{1: "Google Chrome", 2: "Google Chrome"}
	got := Aggregate(records, owners, map[uint16]int64{1: 100, 2: 5}, map[uint16]int64{1: 900, 2: 45})

	want := Report{Groups: []Group{
		{Path: PathTunnel, Rows: []AppRow{{App: "Google Chrome", Conns: 1, BytesUp: 100, BytesDown: 900}}},
		{Path: PathDirect, Rows: []AppRow{{App: "Google Chrome", Conns: 1, BytesUp: 5, BytesDown: 45, Rules: []string{"*.qq.com"}}}},
		{Path: PathBlocked, Rows: nil},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Aggregate =\n%#v\nwant\n%#v", got, want)
	}
}

// 「问不出来是谁」不许摊进已知应用里,也不许丢弃 —— 它在自己所属的那一组里
// 单独成行。unknown 占比高本身就是「这份数据现在不可信」的信号,那是有用的信息。
func TestAggregateKeepsUnknownAsItsOwnRowInsideItsPath(t *testing.T) {
	records := []ConnRecord{
		{SrcPort: 1, Path: PathTunnel, Source: "default"},
		{SrcPort: 2, Path: PathTunnel, Source: "default"},
	}
	owners := map[uint16]string{1: "Slack"} // 2 号端口查不出来
	got := Aggregate(records, owners, nil, nil)

	tunnel := got.Groups[0]
	if len(tunnel.Rows) != 2 {
		t.Fatalf("tunnel 组 %d 行, want 2(Slack + unknown)", len(tunnel.Rows))
	}
	var sawUnknown bool
	for _, r := range tunnel.Rows {
		if r.App == "" && r.Conns == 1 {
			sawUnknown = true
		}
	}
	if !sawUnknown {
		t.Fatal("unknown 那条被摊进已知应用或被丢弃了")
	}
}

// 三组永远都在,即使为空 —— 消费方按下标取组,组数浮动会让渲染层错位。
func TestAggregateAlwaysEmitsAllThreeGroupsInOrder(t *testing.T) {
	got := Aggregate(nil, nil, nil, nil)
	want := []Path{PathTunnel, PathDirect, PathBlocked}
	if len(got.Groups) != 3 {
		t.Fatalf("组数 = %d, want 3", len(got.Groups))
	}
	for i, p := range want {
		if got.Groups[i].Path != p {
			t.Fatalf("第 %d 组是 %q, want %q", i, got.Groups[i].Path, p)
		}
	}
}

func TestAggregateSortsRowsByBytesThenName(t *testing.T) {
	records := []ConnRecord{
		{SrcPort: 1, Path: PathTunnel}, {SrcPort: 2, Path: PathTunnel}, {SrcPort: 3, Path: PathTunnel},
	}
	owners := map[uint16]string{1: "Aardvark", 2: "Zebra", 3: "Middle"}
	got := Aggregate(records, owners, map[uint16]int64{1: 1, 2: 1000, 3: 500}, nil)
	if got.Groups[0].Rows[0].App != "Zebra" || got.Groups[0].Rows[2].App != "Aardvark" {
		t.Fatalf("排序错了: %#v", got.Groups[0].Rows)
	}
}

// 端口复用:同一个端口在记录里出现多次时,它的字节只许计一次。
// 端口复用是 spec 承认的那个近似的来源,把它放大 N 倍就把一个已知有界的误差
// 变成了不可控误差。
func TestAggregateCountsEachPortsBytesOnce(t *testing.T) {
	records := []ConnRecord{
		{SrcPort: 7, Path: PathTunnel},
		{SrcPort: 7, Path: PathTunnel},
		{SrcPort: 7, Path: PathTunnel},
	}
	got := Aggregate(records, map[uint16]string{7: "Slack"}, map[uint16]int64{7: 100}, map[uint16]int64{7: 900})
	row := got.Groups[0].Rows[0]
	if row.Conns != 3 {
		t.Fatalf("Conns = %d, want 3(连接数按记录数算)", row.Conns)
	}
	if row.BytesUp != 100 || row.BytesDown != 900 {
		t.Fatalf("字节 = %d/%d, want 100/900(每个端口只计一次,不是乘以记录数)", row.BytesUp, row.BytesDown)
	}
}
