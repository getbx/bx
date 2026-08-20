//go:build darwin

package supervisor

import "testing"

// TestPCBTablesTagProtocolCorrectly 钉住 pcbTables 这张 mib→协议 对照表。
//
// **它为什么存在**:这张表一个位置写反(比如 UDP 那行的 isUDP 误写成
// false),就等于所有 UDP 流量被静默归到 TCP 键上 —— 而这在界面上看不出来,
// 用户只会看到一个应用名,不会看到两个协议的归因被悄悄合并。这条错误此前
// 曾被独立复现过:`go test ./internal/appattr/... ./internal/supervisor/...`
// 在那种写法下整体全绿,一条测试都不红,因为现有测试只验证了消费侧
// (appattr.Aggregate 正确区分两种键),没有任何东西钉住生产侧真正喂给它的
// 那张表有没有写对。
//
// 本测试不碰任何 syscall —— 与 internal/guardian/procscan.go 的
// decideCoreScan 同一个手法:syscall 那一半单测造不出来,能测的部分单独
// 抽成纯函数/纯数据钉住。
func TestPCBTablesTagProtocolCorrectly(t *testing.T) {
	want := map[string]bool{
		"net.inet.tcp.pcblist_n": false,
		"net.inet.udp.pcblist_n": true,
	}
	if len(pcbTables) != len(want) {
		t.Fatalf("pcbTables 有 %d 项, want %d", len(pcbTables), len(want))
	}
	for _, tbl := range pcbTables {
		wantUDP, ok := want[tbl.mib]
		if !ok {
			t.Fatalf("pcbTables 里出现了预料之外的 mib %q", tbl.mib)
		}
		if tbl.isUDP != wantUDP {
			t.Fatalf("mib %q 标成 isUDP=%v, want %v", tbl.mib, tbl.isUDP, wantUDP)
		}
	}
}
