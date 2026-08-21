package appattr

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestAggregateSplitsOneAppAcrossPaths(t *testing.T) {
	// Chrome 一部分域名直连、一部分走隧道,是常态。压成一行「混合」会把最有用的
	// 那一半信息扔掉 —— 腾讯会议那次要看的恰恰是「它只出现在 tunnel 组里」。
	records := []ConnRecord{
		{SrcPort: 1, Path: PathTunnel, Source: "default"},
		{SrcPort: 2, Path: PathDirect, Source: "user_direct", Rule: "*.qq.com"},
	}
	owners := namedOwners(map[PortKey]string{{Port: 1, UDP: false}: "Google Chrome", {Port: 2, UDP: false}: "Google Chrome"})
	got := Aggregate(AggregateInput{Records: records, Owners: owners,
		BytesUp:   map[PortKey]int64{{Port: 1, UDP: false}: 100, {Port: 2, UDP: false}: 5},
		BytesDown: map[PortKey]int64{{Port: 1, UDP: false}: 900, {Port: 2, UDP: false}: 45}})

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
	owners := namedOwners(map[PortKey]string{{Port: 1, UDP: false}: "Slack"}) // 2 号端口查不出来
	got := Aggregate(AggregateInput{Records: records, Owners: owners})

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
	got := Aggregate(AggregateInput{})
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
	owners := namedOwners(map[PortKey]string{{Port: 1, UDP: false}: "Aardvark", {Port: 2, UDP: false}: "Zebra", {Port: 3, UDP: false}: "Middle"})
	got := Aggregate(AggregateInput{Records: records, Owners: owners,
		BytesUp: map[PortKey]int64{{Port: 1, UDP: false}: 1, {Port: 2, UDP: false}: 1000, {Port: 3, UDP: false}: 500}})
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
	got := Aggregate(AggregateInput{
		Records:   records,
		Owners:    namedOwners(map[PortKey]string{{Port: 7, UDP: false}: "Slack"}),
		BytesUp:   map[PortKey]int64{{Port: 7, UDP: false}: 100},
		BytesDown: map[PortKey]int64{{Port: 7, UDP: false}: 900},
	})
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
	owners := namedOwners(map[PortKey]string{
		{Port: 443, UDP: false}: "Google Chrome",
		{Port: 443, UDP: true}:  "quic-app",
	})
	bytesUp := map[PortKey]int64{
		{Port: 443, UDP: false}: 10,
		{Port: 443, UDP: true}:  20,
	}
	bytesDown := map[PortKey]int64{
		{Port: 443, UDP: false}: 100,
		{Port: 443, UDP: true}:  200,
	}
	got := Aggregate(AggregateInput{Records: records, Owners: owners, BytesUp: bytesUp, BytesDown: bytesDown})

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
	owners := namedOwners(map[PortKey]string{{Port: 5000, UDP: false}: "Slack"})
	got := Aggregate(AggregateInput{Records: records, Owners: owners,
		BytesUp:   map[PortKey]int64{{Port: 5000, UDP: false}: 100},
		BytesDown: map[PortKey]int64{{Port: 5000, UDP: false}: 900}})

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

// **图标要的是路径,报告此前只有显示名。** `NSWorkspace.icon(forFile:)` 认路径,
// 而 owners 那张表本来就是从可执行路径推出显示名的(`DisplayName`)—— 路径当时
// 被丢掉了,于是菜单侧只能画纯文字。
//
// 这条钉住的是「**代表值**」这个语义:AppRow 是按 (路径, 应用名) 聚合的,同一个
// 显示名可能来自多个 PID(Chrome 的 helper 进程各有各的可执行路径),ExecPath
// 只保证是其中**某一个**,不是全集。断言因此是「非空且属于贡献者之一」,不是
// 某个具体值 —— 断言具体值等于把「谁是代表」这个无关紧要的选择钉成契约。
func TestAggregateCarriesARepresentativeExecutablePath(t *testing.T) {
	records := []ConnRecord{
		{SrcPort: 1, Path: PathTunnel, Source: "default"},
		{SrcPort: 2, Path: PathTunnel, Source: "default"},
	}
	owners := map[PortKey]Owner{
		{Port: 1}: {Name: "Google Chrome", ExecPath: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"},
		{Port: 2}: {Name: "Google Chrome", ExecPath: "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper"},
	}
	got := Aggregate(AggregateInput{Records: records, Owners: owners})
	rows := got.Groups[0].Rows
	if len(rows) != 1 {
		t.Fatalf("tunnel 组 %d 行, want 1", len(rows))
	}
	if rows[0].ExecPath != owners[PortKey{Port: 1}].ExecPath &&
		rows[0].ExecPath != owners[PortKey{Port: 2}].ExecPath {
		t.Fatalf("ExecPath = %q,既不是两个贡献端口里的哪一个 —— 菜单侧据此画图标", rows[0].ExecPath)
	}
}

// 有一个贡献端口带路径就必须带出来:代表值可以是任意一个,但「明明有却空着」
// 会让那一行无声地失去图标,而这正是加这个字段要买的东西。
func TestAggregatePrefersAContributingPathOverEmptiness(t *testing.T) {
	records := []ConnRecord{
		{SrcPort: 1, Path: PathDirect, Source: "china"},
		{SrcPort: 2, Path: PathDirect, Source: "china"},
	}
	owners := map[PortKey]Owner{
		{Port: 1}: {Name: "Safari"}, // 路径读不出来(kern.procargs2 可能失败)
		{Port: 2}: {Name: "Safari", ExecPath: "/Applications/Safari.app/Contents/MacOS/Safari"},
	}
	got := Aggregate(AggregateInput{Records: records, Owners: owners})
	rows := got.Groups[1].Rows
	if len(rows) != 1 {
		t.Fatalf("direct 组 %d 行, want 1", len(rows))
	}
	if rows[0].ExecPath != "/Applications/Safari.app/Contents/MacOS/Safari" {
		t.Fatalf("ExecPath = %q —— 有贡献端口带着路径,这一行却空着", rows[0].ExecPath)
	}
}

// unknown 那一行不许凭空长出路径:它整个存在的意义就是「问不出来是谁」。
func TestAggregateGivesUnknownRowsNoExecutablePath(t *testing.T) {
	records := []ConnRecord{{SrcPort: 9, Path: PathTunnel, Source: "default"}}
	got := Aggregate(AggregateInput{Records: records, Owners: map[PortKey]Owner{}})
	rows := got.Groups[0].Rows
	if len(rows) != 1 || rows[0].App != "" {
		t.Fatalf("want 一行 unknown, got %#v", rows)
	}
	if rows[0].ExecPath != "" {
		t.Fatalf("unknown 那一行带了路径 %q", rows[0].ExecPath)
	}
}

// namedOwners 把「端口 → 显示名」这种老写法升成 Owner。
//
// 绝大多数用例不关心可执行路径(它只喂图标),让它们保持原来一眼看得懂的形状;
// 关心路径的那几条直接写 map[PortKey]Owner 字面量。
func namedOwners(m map[PortKey]string) map[PortKey]Owner {
	out := make(map[PortKey]Owner, len(m))
	for k, name := range m {
		out[k] = Owner{Name: name}
	}
	return out
}

// ---- 报告窗口(2026-08-20)----

// 窗口边界必须是「now - ReportWindow 之后(含)」。**边界上那一条要留住** ——
// 判反了会让每次刷新都恰好丢掉一条最旧的记录,而那种丢法在界面上完全看不出来。
func TestInReportWindowKeepsOnlyRecentRecords(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"刚刚", now, true},
		{"窗口内", now.Add(-ReportWindow + time.Second), true},
		{"正好在边界上", now.Add(-ReportWindow), true},
		{"刚出窗口", now.Add(-ReportWindow - time.Millisecond), false},
		{"远远出窗口", now.Add(-10 * time.Minute), false},
	}
	for _, c := range cases {
		got := InReportWindow(ConnRecord{At: c.at}, now)
		if got != c.want {
			t.Errorf("%s: InReportWindow=%v,想要 %v", c.name, got, c.want)
		}
	}
}

// 窗口长度是个**产品决定**,不是随手取的数:报告回答的是「此刻谁在连谁」。
// 钉住它是为了让改动它的人先去读那段注释,而不是顺手调一个常量。
func TestReportWindowIsOneMinute(t *testing.T) {
	if ReportWindow != time.Minute {
		t.Fatalf("报告窗口是 %v —— 它是产品决定(「此刻谁在连谁」),"+
			"改之前先读 report.go 里 ReportWindow 头上那段", ReportWindow)
	}
}

// **本次修复的核心证据**:归因存在记录里,连接关掉之后它仍然在。
//
// owners map 是**读取那一刻**现问内核得到的「还开着的 socket」快照 —— 这里
// 刻意传空,模拟「那条连接已经关了、端口再也查不到主人」。记录里存着 Owner
// 的那一条必须仍然算在 Slack 头上,而不是塌进 unknown。
func TestAggregateUsesTheOwnerStoredOnTheRecord(t *testing.T) {
	records := []ConnRecord{
		{
			SrcPort: 7, Path: PathTunnel, Source: "default",
			Owner: Owner{Name: "Slack", ExecPath: "/Applications/Slack.app/Contents/MacOS/Slack"},
		},
	}
	rep := Aggregate(AggregateInput{Records: records, Owners: map[PortKey]Owner{}, BytesUp: map[PortKey]int64{{Port: 7}: 100}})
	rows := rep.Groups[0].Rows
	if len(rows) != 1 {
		t.Fatalf("tunnel 组该有 1 行,得 %d 行:%#v", len(rows), rows)
	}
	if rows[0].App != "Slack" {
		t.Fatalf("连接关掉之后归因就丢了(App=%q)—— 记录里存的 Owner 没被用上,"+
			"这正是短连接全部塌进 unknown 的原因", rows[0].App)
	}
	if rows[0].ExecPath == "" {
		t.Error("记录里存着可执行路径,聚合之后却空了 —— 那一行会无声地失去图标")
	}
}

// 记录里**没有** Owner 时仍然回落到现查的 owners map:后台 resolver 还没来得及
// 解析的那些(以及 Snapshot 那次最后的尝试)靠这条路。两条路都要在。
func TestAggregateFallsBackToTheFreshOwnersMap(t *testing.T) {
	records := []ConnRecord{{SrcPort: 7, Path: PathTunnel, Source: "default"}}
	rep := Aggregate(AggregateInput{Records: records, Owners: map[PortKey]Owner{{Port: 7}: {Name: "Chrome"}}})
	if got := rep.Groups[0].Rows[0].App; got != "Chrome" {
		t.Fatalf("记录里没存 Owner 时该回落到现查的 map,得 %q", got)
	}
}

// ---- 速率(2026-08-20,服务端按端口做差)----

// 规则 4,**这个函数存在的理由**:一个端口从这次样本里消失,它这一拍不贡献,
// 不是复位、不是负数、不是 0 值写入。60 秒滚动窗口下这种消失天天发生(记录
// 滑出窗口),按聚合行做差会把它误读成"计数器复位"从而显示破折号 —— 按端口
// 做差没有这个歧义:消失的端口在输出 map 里干脆不出现。
func TestDiffPortRatesTreatsAVanishedPortAsNoContribution(t *testing.T) {
	prev := map[PortKey]int64{{Port: 1}: 100, {Port: 2}: 500}
	cur := map[PortKey]int64{{Port: 1}: 150} // 2 号端口这一拍消失了
	got := DiffPortRates(prev, cur, 2*time.Second)
	if _, ok := got[PortKey{Port: 2}]; ok {
		t.Fatalf("消失的端口不该出现在速率表里,得到 %#v", got)
	}
	if got[PortKey{Port: 1}] != 25 { // (150-100)/2s
		t.Fatalf("端口 1 的速率 = %v, want 25", got[PortKey{Port: 1}])
	}
}

// 规则 3:cur < prev 是计数器复位(TCP 端口复用清账),delta = cur,不是负数。
func TestDiffPortRatesTreatsACounterResetAsAllNew(t *testing.T) {
	prev := map[PortKey]int64{{Port: 1}: 1000}
	cur := map[PortKey]int64{{Port: 1}: 40} // 端口复用,账被清空重新攒
	got := DiffPortRates(prev, cur, 2*time.Second)
	if got[PortKey{Port: 1}] != 20 { // 40/2s,不是 (40-1000)/2s
		t.Fatalf("计数器复位没有按「delta=cur」处理,得到 %v", got[PortKey{Port: 1}])
	}
}

// 规则 1:key 只在 cur 里(新端口),字节全是这一拍攒的。
func TestDiffPortRatesCountsANewPortEntirely(t *testing.T) {
	cur := map[PortKey]int64{{Port: 9}: 200}
	got := DiffPortRates(nil, cur, 2*time.Second)
	if got[PortKey{Port: 9}] != 100 { // 200/2s
		t.Fatalf("新端口的速率 = %v, want 100", got[PortKey{Port: 9}])
	}
}

// elapsed <= 0 不许拿一个近似的分母硬算,直接返回 nil。
func TestDiffPortRatesWithoutAnIntervalYieldsNothing(t *testing.T) {
	cur := map[PortKey]int64{{Port: 1}: 100}
	if got := DiffPortRates(nil, cur, 0); got != nil {
		t.Fatalf("elapsed=0 时该返回 nil,得到 %#v", got)
	}
	if got := DiffPortRates(nil, cur, -time.Second); got != nil {
		t.Fatalf("elapsed<0 时该返回 nil,得到 %#v", got)
	}
}

// 同一个端口在窗口里出现两条记录(端口复用)时,速率与字节数用同一个 counted
// 去重,只算一次 —— 不是按记录数重复累加。
func TestAggregateCountsEachPortsRateOnce(t *testing.T) {
	records := []ConnRecord{
		{SrcPort: 7, Path: PathTunnel},
		{SrcPort: 7, Path: PathTunnel},
	}
	rep := Aggregate(AggregateInput{
		Records:    records,
		Owners:     namedOwners(map[PortKey]string{{Port: 7}: "Slack"}),
		RateUp:     map[PortKey]float64{{Port: 7}: 1000},
		RateDown:   map[PortKey]float64{{Port: 7}: 2000},
		RatesReady: true,
	})
	row := rep.Groups[0].Rows[0]
	if row.BytesUpRate == nil || *row.BytesUpRate != 1000 {
		t.Fatalf("上行速率算重了或没算:%#v", row.BytesUpRate)
	}
	if row.BytesDownRate == nil || *row.BytesDownRate != 2000 {
		t.Fatalf("下行速率算重了或没算:%#v", row.BytesDownRate)
	}
}

// RatesReady=false ⇒ 两个速率字段都必须是 nil,不管 RateUp/RateDown 里有什么。
func TestAggregateOmitsRatesWhenNotReady(t *testing.T) {
	records := []ConnRecord{{SrcPort: 7, Path: PathTunnel}}
	rep := Aggregate(AggregateInput{
		Records:    records,
		Owners:     namedOwners(map[PortKey]string{{Port: 7}: "Slack"}),
		RateUp:     map[PortKey]float64{{Port: 7}: 999},
		RateDown:   map[PortKey]float64{{Port: 7}: 999},
		RatesReady: false,
	})
	row := rep.Groups[0].Rows[0]
	if row.BytesUpRate != nil || row.BytesDownRate != nil {
		t.Fatalf("RatesReady=false 却带出了速率:%#v", row)
	}
}

// **指向 0 的指针照样会被序列化,与 nil 是两件不同的事。** omitempty 对指针
// 只看 nil —— 这一点必须有测试钉住,否则将来有人把字段改成值类型时没有任何
// 东西会红。0 B/s(应用在但这一拍没传东西)与「还没有速率可报」压成同一个 0
// 会让界面把"不知道"显示成"闲着"。
func TestZeroRateSerializesButUnavailableRateIsAbsent(t *testing.T) {
	rep := Aggregate(AggregateInput{
		Records:    []ConnRecord{{SrcPort: 7, Path: PathTunnel}},
		Owners:     namedOwners(map[PortKey]string{{Port: 7}: "Slack"}),
		RateUp:     map[PortKey]float64{}, // 这个端口这一拍没有条目 ⇒ 累加成 0
		RateDown:   map[PortKey]float64{},
		RatesReady: true,
	})
	row := rep.Groups[0].Rows[0]
	if row.BytesUpRate == nil || *row.BytesUpRate != 0 {
		t.Fatalf("RatesReady=true 时的 0 速率应该是指向 0 的指针,得到 %#v", row.BytesUpRate)
	}
	buf, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(buf, &decoded); err != nil {
		t.Fatalf("unmarshal 失败: %v", err)
	}
	if v, ok := decoded["bytes_up_rate"]; !ok || string(v) != "0" {
		t.Fatalf("指向 0 的指针没有被序列化成 0,got present=%v value=%s", ok, v)
	}

	// nil 那一半:RatesReady=false 时键必须整个缺席,不是 null。
	repUnready := Aggregate(AggregateInput{
		Records: []ConnRecord{{SrcPort: 7, Path: PathTunnel}},
		Owners:  namedOwners(map[PortKey]string{{Port: 7}: "Slack"}),
	})
	rowUnready := repUnready.Groups[0].Rows[0]
	buf2, err := json.Marshal(rowUnready)
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	var decoded2 map[string]json.RawMessage
	if err := json.Unmarshal(buf2, &decoded2); err != nil {
		t.Fatalf("unmarshal 失败: %v", err)
	}
	if _, ok := decoded2["bytes_up_rate"]; ok {
		t.Fatalf("没有速率时 bytes_up_rate 键不该出现,got %s", buf2)
	}
}
