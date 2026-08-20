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
	owners := map[PortKey]string{{Port: 1, UDP: false}: "Google Chrome", {Port: 2, UDP: false}: "Google Chrome"}
	got := Aggregate(records, owners,
		map[PortKey]int64{{Port: 1, UDP: false}: 100, {Port: 2, UDP: false}: 5},
		map[PortKey]int64{{Port: 1, UDP: false}: 900, {Port: 2, UDP: false}: 45})

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
	owners := map[PortKey]string{{Port: 1, UDP: false}: "Slack"} // 2 号端口查不出来
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
	owners := map[PortKey]string{{Port: 1, UDP: false}: "Aardvark", {Port: 2, UDP: false}: "Zebra", {Port: 3, UDP: false}: "Middle"}
	got := Aggregate(records, owners,
		map[PortKey]int64{{Port: 1, UDP: false}: 1, {Port: 2, UDP: false}: 1000, {Port: 3, UDP: false}: 500}, nil)
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
	got := Aggregate(records,
		map[PortKey]string{{Port: 7, UDP: false}: "Slack"},
		map[PortKey]int64{{Port: 7, UDP: false}: 100},
		map[PortKey]int64{{Port: 7, UDP: false}: 900})
	row := got.Groups[0].Rows[0]
	if row.Conns != 3 {
		t.Fatalf("Conns = %d, want 3(连接数按记录数算)", row.Conns)
	}
	if row.BytesUp != 100 || row.BytesDown != 900 {
		t.Fatalf("字节 = %d/%d, want 100/900(每个端口只计一次,不是乘以记录数)", row.BytesUp, row.BytesDown)
	}
}

// 核心回归:TCP 与 UDP 端口空间相互独立,同一个数字完全可能同时被一个 TCP
// socket 和一个 UDP socket 占用。owners/bytesUp/bytesDown 必须按 (端口,协议)
// 联合键分别归因,任何把两者合并成同一个 uint16 键的实现都会让后写入的那个
// 协议静默覆盖先写入的归因 —— 这不是本测试要验证的实现细节,而是它存在的理由。
func TestAggregateKeepsTCPAndUDPPortsApart(t *testing.T) {
	records := []ConnRecord{
		{SrcPort: 443, UDP: false, Path: PathTunnel}, // TCP:443 属于 Chrome
		{SrcPort: 443, UDP: true, Path: PathTunnel},  // UDP:443(QUIC)属于 quic-app
	}
	owners := map[PortKey]string{
		{Port: 443, UDP: false}: "Google Chrome",
		{Port: 443, UDP: true}:  "quic-app",
	}
	bytesUp := map[PortKey]int64{
		{Port: 443, UDP: false}: 10,
		{Port: 443, UDP: true}:  20,
	}
	bytesDown := map[PortKey]int64{
		{Port: 443, UDP: false}: 100,
		{Port: 443, UDP: true}:  200,
	}
	got := Aggregate(records, owners, bytesUp, bytesDown)

	rows := got.Groups[0].Rows
	if len(rows) != 2 {
		t.Fatalf("tunnel 组 %d 行, want 2(TCP:443 与 UDP:443 必须分成两行), got %#v", len(rows), rows)
	}
	byApp := map[string]AppRow{}
	for _, r := range rows {
		byApp[r.App] = r
	}
	chrome, ok := byApp["Google Chrome"]
	if !ok {
		t.Fatalf("Google Chrome(TCP:443)那一行不见了: %#v", rows)
	}
	if chrome.BytesUp != 10 || chrome.BytesDown != 100 {
		t.Fatalf("Google Chrome 字节 = %d/%d, want 10/100(不能被 UDP:443 覆盖或合并)", chrome.BytesUp, chrome.BytesDown)
	}
	quic, ok := byApp["quic-app"]
	if !ok {
		t.Fatalf("quic-app(UDP:443)那一行不见了: %#v", rows)
	}
	if quic.BytesUp != 20 || quic.BytesDown != 200 {
		t.Fatalf("quic-app 字节 = %d/%d, want 20/200(不能被 TCP:443 覆盖或合并)", quic.BytesUp, quic.BytesDown)
	}
}

// **倒序遍历是承重的,这一条钉的就是它。**
//
// 端口复用时,同一个 PortKey 会在 records 里出现多次、且可能分属不同的路径与
// 应用;counted 让字节只计一次,而**倒序**决定了那一次落在谁头上 —— 规则是
// 「同一个键最近的那条记录赢」(records 是时间序,下标越大越新)。改成正序,
// 字节会落到那个已经关掉的旧连接头上,界面于是把「Slack 正在走隧道下载」
// 报成「Slack 在直连」。
//
// 这条测试之所以必须住在 **appattr 自己的包里**:改成正序时,全仓唯一会转红的
// 是 internal/supervisor 的 TestAppTrafficKeepsTimeOrderAcrossRingBoundaries,
// 而 `go test ./internal/appattr/` 全绿 —— 只改这个包的人会看到绿灯放行。
// (这正是 CLAUDE.md 里反复记的那个形状:守卫钉住的是缺陷旁边的东西,或者
// 干脆住在别的包里。)
func TestAggregateAttributesReusedPortBytesToTheMostRecentRecord(t *testing.T) {
	// 5000 号端口先被一条直连连接用过,关掉之后被一条走隧道的连接复用;
	// 字节账是这个端口在窗口里的总量,按规则应全部记给**最近**那条(tunnel)。
	records := []ConnRecord{
		{SrcPort: 5000, Path: PathDirect, Source: "user_direct", Rule: "*.qq.com"}, // 旧
		{SrcPort: 5000, Path: PathTunnel, Source: "default"},                       // 新
	}
	owners := map[PortKey]string{{Port: 5000, UDP: false}: "Slack"}
	got := Aggregate(records, owners,
		map[PortKey]int64{{Port: 5000, UDP: false}: 100},
		map[PortKey]int64{{Port: 5000, UDP: false}: 900})

	tunnel, direct := got.Groups[0], got.Groups[1]
	if len(tunnel.Rows) != 1 || len(direct.Rows) != 1 {
		t.Fatalf("两条记录应分成两组各一行:tunnel=%#v direct=%#v", tunnel.Rows, direct.Rows)
	}
	if tunnel.Rows[0].BytesUp != 100 || tunnel.Rows[0].BytesDown != 900 {
		t.Fatalf("字节应记给**最近**那条记录(tunnel),得到 tunnel=%d/%d direct=%d/%d —— Aggregate 不再倒序遍历",
			tunnel.Rows[0].BytesUp, tunnel.Rows[0].BytesDown, direct.Rows[0].BytesUp, direct.Rows[0].BytesDown)
	}
	if direct.Rows[0].BytesUp != 0 || direct.Rows[0].BytesDown != 0 {
		t.Fatalf("旧记录不该分到任何字节,得到 %d/%d", direct.Rows[0].BytesUp, direct.Rows[0].BytesDown)
	}
}
