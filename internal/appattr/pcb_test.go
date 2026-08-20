package appattr

import (
	"encoding/binary"
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
// (LocalPort/RemotePort/LastPID/EPID)。
//
// **这条测试与下面的 TestParsePcbListOffsetsAreIsolated 各守一半,缺一不可**:
// 本测试证明的是「真实内核在这份布局下,这四个字段的具体取值是什么」——它挡得住
// 真实存在的布局陷阱(8 字节对齐、pid 偏移),因为它用的是真机字节而不是猜测。
// 但它挡不住偏移隔离:这份 fixture 里 EPID 全为 0(见下方诚实记录),而复审
// 逐 4 字节扫描发现块内偏移 44/52/56/60/80/96 上真实数据**恰好也是全零**——
// 若 offSoEPID 被误设成这几个偏移中任意一个,本测试依然全绿,那是这份 fixture
// 零值分布的巧合,不是「offSoEPID=72 是对的」的证明。偏移隔离由
// TestParsePcbListOffsetsAreIsolated 的合成数据负责,那份数据里没有任何巧合的零值。
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

// TestParsePcbListOffsetsAreIsolated 用合成数据把「四个字段各自读的是不同偏移」
// 这件事跟 fixture 的具体字节脱钩验证。
//
// **为什么它必须和 TestParsePcbListMatchesGoldenRecords 分开存在**:golden 测试
// 用真机字节,能证明真实内核布局(那部分只能靠真机、合成数据编不出来);但
// 真机字节里恰好有一堆全零区域(EPID 全为 0,偏移 44/52/56/60/80/96 上也是
// 巧合的全零)—— 一个字段读错偏移、读到另一片全零区域,golden 测试完全看不出来。
// 本测试反过来:构造一个 XSO_SOCKET 块,让块内**每一个 4 字节字都是互不相同的
// 非零哨兵值**,这样任何字段读错偏移都会读到一个跟期望值不同的哨兵,必然失败。
// 它证明不了真实内核布局是否如此(那要靠上面那条),但补上了 golden 测试因为
// 零值巧合而遮住的盲点。两条测试各证明一半,合起来才完整。
func TestParsePcbListOffsetsAreIsolated(t *testing.T) {
	const (
		wantLocalPort  = 4041
		wantRemotePort = 8080

		// 这两个偏移**刻意硬编码**成字面量 68/72,不引用生产代码里的
		// offSoLastPID/offSoEPID 常量 —— 如果引用的是同一个常量,一旦那两个
		// 常量被改坏(变异测试正是这么做的),期望值会跟着一起改坏,变异就
		// 抓不到了。这两个数字来自 pcb.go 顶部注释里「实测得来」的
		// [68] so_last_pid [72] so_e_pid。
		correctLastPIDOffset = 68
		correctEPIDOffset    = 72
	)

	// xinpgen 头(真实大小 24 字节):ParsePcbList 只读前 4 字节的 xig_len 作为
	// 第一个块的起始偏移,其余字段不解析,填 0 即可。
	header := make([]byte, 24)
	binary.NativeEndian.PutUint32(header[0:4], 24)

	// XSO_INPCB 块:xi_len(4) xi_kind(4) xi_inpp(8,未用) inp_fport(2)
	// inp_lport(2) + 4 字节保留位凑成 8 对齐的 24 字节,端口用网络字节序。
	// 端口偏移与生产代码 blk[16:18]/blk[18:20] 对应,同样验证在内。
	inpcb := make([]byte, 24)
	binary.NativeEndian.PutUint32(inpcb[0:4], 24)
	binary.NativeEndian.PutUint32(inpcb[4:8], kindInpcb)
	binary.BigEndian.PutUint16(inpcb[16:18], wantRemotePort)
	binary.BigEndian.PutUint16(inpcb[18:20], wantLocalPort)

	// XSO_SOCKET 块:104 字节(8 对齐,含 xso_len/xso_kind 两个头字 + 24 个
	// 数据字)。从字节偏移 8 起,每个 4 字节字填一个各不相同的哨兵值
	// (第 wordOff 字节处的字 = wordOff/4+1),一路盖过 44/52/56/60/72/80/96
	// 这些真机 fixture 上恰好是零的偏移 —— 这里它们全部非零且互不相同。
	const socketLen = 104
	socket := make([]byte, socketLen)
	binary.NativeEndian.PutUint32(socket[0:4], socketLen)
	binary.NativeEndian.PutUint32(socket[4:8], kindSocket)
	for wordOff := 8; wordOff+4 <= socketLen; wordOff += 4 {
		sentinel := uint32(wordOff/4 + 1)
		binary.NativeEndian.PutUint32(socket[wordOff:wordOff+4], sentinel)
	}

	raw := append(append(append([]byte{}, header...), inpcb...), socket...)

	pcbs, err := ParsePcbList(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcbs) != 1 {
		t.Fatalf("解出 %d 条,want 1 —— 合成数据只装了一条记录", len(pcbs))
	}
	got := pcbs[0]

	wantLastPID := int32(correctLastPIDOffset/4 + 1)
	wantEPID := int32(correctEPIDOffset/4 + 1)

	if got.LocalPort != wantLocalPort {
		t.Errorf("LocalPort = %d, want %d", got.LocalPort, wantLocalPort)
	}
	if got.RemotePort != wantRemotePort {
		t.Errorf("RemotePort = %d, want %d", got.RemotePort, wantRemotePort)
	}
	if got.LastPID != wantLastPID {
		t.Errorf("LastPID = %d, want %d(偏移 %d 处的哨兵值)——偏移取错了",
			got.LastPID, wantLastPID, correctLastPIDOffset)
	}
	if got.EPID != wantEPID {
		t.Errorf("EPID = %d, want %d(偏移 %d 处的哨兵值)——偏移取错了",
			got.EPID, wantEPID, correctEPIDOffset)
	}
}
