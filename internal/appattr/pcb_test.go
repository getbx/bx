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
