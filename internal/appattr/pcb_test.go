package appattr

import (
	"os"
	"testing"
)

// fixture 是 2026-08-19 从项目所有者的 Mac 上采的真实 net.inet.tcp.pcblist_n,
// 地址字段已抹零、端口保留。**合成 fixture 挡不住下面两个坑**:8 字节对齐那个
// 只在存在 XSO_TCPCB 块时才出现,而合成数据不会有它。
//
// 采集时 `lsof -nP -i4TCP -sTCP:ESTABLISHED | wc -l` = 42(仅 ESTABLISHED);
// pcblist_n 本身含全部状态(LISTEN/TIME_WAIT/…),ParsePcbList 解出 209 条,
// 209/209 带 PID。这两个数是判断「fixture 是不是太小」的唯一依据。
func loadFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/pcblist_tcp.bin")
	if err != nil {
		t.Fatalf("读不到 fixture,守卫失去意义: %v", err)
	}
	return raw
}

// 坑一:块按 8 字节对齐,而 xso_len **不含**尾部填充。TCP 的 XSO_TCPCB 块 len=204,
// 下一块其实在 +208。不补齐则整张表在第一条之后就走飞,只解出 1 条 —— 而 UDP 没有
// 那个块,看起来完全正常,于是极易被误判成「UDP 能做、TCP 不能」。
func TestParsePcbListWalksPastEightByteAlignmentPadding(t *testing.T) {
	pcbs, err := ParsePcbList(loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(pcbs) < 20 {
		t.Fatalf("只解出 %d 条 —— 真机 fixture 采集时有上百条,块遍历走飞了", len(pcbs))
	}
}

// 坑二:so_last_pid 在偏移 68、so_e_pid 在 72,**不在块尾**(其后还有 so_gencnt /
// so_flags / so_flags1 / so_usecount / so_retaincnt / xso_filter_flags)。
// 按「结构体最后两个字段」从块尾往回取,TCP 全读成 0。
func TestParsePcbListReadsPIDAtTheCorrectOffset(t *testing.T) {
	pcbs, err := ParsePcbList(loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	withPID := 0
	for _, p := range pcbs {
		if p.LastPID > 0 {
			withPID++
		}
	}
	// 真机实测 TCP 209/209 全带 PID。留出余量,但 0 条一定是偏移错了。
	if withPID*2 < len(pcbs) {
		t.Fatalf("%d/%d 条带 PID —— pid 偏移取错了(块尾是 so_retaincnt/xso_filter_flags,不是 pid)",
			withPID, len(pcbs))
	}
}

func TestParsePcbListRejectsTruncatedInput(t *testing.T) {
	if _, err := ParsePcbList([]byte{1, 2, 3}); err == nil {
		t.Fatal("截断输入必须报错 —— 悄悄返回空列表会被读成「没有连接」")
	}
}

// TestParsePcbListMatchesGoldenRecords 钉住 fixture 里若干条记录的确切四元组
// (LocalPort/RemotePort/LastPID/EPID),而不是「解析没崩」这种弱断言。
//
// 存在的必要性:前三条测试只验证了「块遍历没有走飞」与「LastPID 大体上非零」,
// 对 EPID(so_e_pid,偏移 72)完全没有覆盖——把 offSoEPID 改成任何仍落在块内
// 的错误偏移(比如误设成与 offSoLastPID 相同的 68,或 so_gencnt 的偏移),
// 前三条测试会全绿。EPID 不是无关紧要的字段:它是「替谁干活」,Task 3 的
// ChooseOwner 委托规则直接建在这个偏移上,选错偏移会让委托规则用一个
// 看似合理、实则是别的字段的值做判断。
//
// 记录按 ParsePcbList 返回顺序用下标钉死(该顺序由 fixture 字节本身决定,是
// 确定性的);选取时覆盖了不同 LastPID、不同 RemotePort(443 与其余)、
// RemotePort=0 的监听态 socket、以及 LastPID=1(launchd)这种边界值。
//
// **诚实记录**:这份 fixture 采集时机器上没有任何被委托的 socket——209 条
// 记录里 EPID 全部为 0(用一次性程序遍历过,见 task-2-report.md 的修复记录)。
// 所以下面全部 8 条记录的 EPID 都断言为 0;EPID 非零的路径这份 fixture 挡不住,
// 由 Task 3(ChooseOwner)针对委托场景另写的单元测试覆盖。这条测试证明的是
// 「EPID 这个字段本身读的是正确的偏移、且在无委托时稳定读到 0」,不是
// 「EPID 能正确读出非零值」。
func TestParsePcbListMatchesGoldenRecords(t *testing.T) {
	pcbs, err := ParsePcbList(loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}

	golden := map[int]PCB{
		0:   {LocalPort: 53220, RemotePort: 443, LastPID: 10737, EPID: 0},
		14:  {LocalPort: 53183, RemotePort: 5223, LastPID: 42556, EPID: 0},
		35:  {LocalPort: 53092, RemotePort: 5228, LastPID: 1459, EPID: 0},
		58:  {LocalPort: 51718, RemotePort: 27036, LastPID: 2942, EPID: 0},
		82:  {LocalPort: 50294, RemotePort: 8080, LastPID: 25998, EPID: 0},
		107: {LocalPort: 8000, RemotePort: 0, LastPID: 1498, EPID: 0},
		141: {LocalPort: 53, RemotePort: 0, LastPID: 1421, EPID: 0},
		166: {LocalPort: 22, RemotePort: 0, LastPID: 1, EPID: 0},
	}

	maxIdx := 0
	for idx := range golden {
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	if len(pcbs) <= maxIdx {
		t.Fatalf("解出 %d 条,不够覆盖下标 %d —— fixture 是否被替换了?", len(pcbs), maxIdx)
	}

	for idx, want := range golden {
		got := pcbs[idx]
		if got != want {
			t.Errorf("pcbs[%d] = %+v, want %+v", idx, got, want)
		}
	}
}
