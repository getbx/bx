package appattr

import (
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"strings"
	"testing"
)

// 真机 2026-09-25 抓下来的两个 XSO_INPCB 块(`netstat -anv` 逐条对过),**只换了地址**
// (公网远端换成文档保留段,仓库守卫不许真实地址):
//   - tailscale:inp_flags 0x804840 —— 带 INP_BOUND_IF(0x4000);
//   - chrome:   inp_flags 0x800840 —— 不带。
//
// 两者都带 0x00800000,那一位不是判据。
const (
	realTailscaleInpcb = "6800000010000000a2814f70dd1cca4b01bbc17a7c76eeb71e2d4cf02a4c4e0000000000404880000000000001400000000000000000000000000000cb00710a000000000000000000000000c0a8320f00000000000000000000000000000000b5c0b8af16400000"
	realChromeInpcb    = "68000000100000002929e672464cd9c001bbc1781a2da2df26468d9c244c4e0000000000400880000000000001400000000000000000000000000000cb00710b000000000000000000000000c0a8320f00000000000000000000000000000000a769df8716400000"
)

func pcbListFromInpcbs(t *testing.T, pids []int32, inpcbHex ...string) []byte {
	t.Helper()
	raw := make([]byte, 24)
	binary.NativeEndian.PutUint32(raw[0:4], 24)
	for i, h := range inpcbHex {
		blk, err := hex.DecodeString(strings.ReplaceAll(h, " ", ""))
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, blk...)
		for len(raw)%8 != 0 {
			raw = append(raw, 0)
		}
		sock := make([]byte, minSocketLen)
		binary.NativeEndian.PutUint32(sock[0:4], uint32(len(sock)))
		binary.NativeEndian.PutUint32(sock[4:8], kindSocket)
		binary.NativeEndian.PutUint32(sock[offSoLastPID:], uint32(pids[i]))
		raw = append(raw, sock...)
		for len(raw)%8 != 0 {
			raw = append(raw, 0)
		}
	}
	return raw
}

func TestParsePcbListReadsAddressesAndTheBoundInterfaceBit(t *testing.T) {
	pcbs, err := ParsePcbList(pcbListFromInpcbs(t, []int32{100, 200}, realTailscaleInpcb, realChromeInpcb))
	if err != nil {
		t.Fatal(err)
	}
	if len(pcbs) != 2 {
		t.Fatalf("parsed %d pcbs, want 2", len(pcbs))
	}
	local := netip.MustParseAddr("192.168.50.15")
	for i, want := range []struct {
		remote string
		bound  bool
	}{{"203.0.113.10", true}, {"203.0.113.11", false}} {
		p := pcbs[i]
		if p.LocalAddr != local || p.RemoteAddr != netip.MustParseAddr(want.remote) || p.BoundToInterface != want.bound || p.RemotePort != 443 {
			t.Fatalf("pcb %d = %+v, want local %s remote %s:443 bound %v", i, p, local, want.remote, want.bound)
		}
	}
}

// 真机那次:Chrome 在 bx 关掉的 14 秒里开的连接,bx 重新开起来 36 分钟后仍从物理网卡出去。
// 判据要把它挑出来,而放过每一种有意为之的直出。
func TestStrayConnectionsFindsOnlyTheConnectionsThatBypassBx(t *testing.T) {
	en0 := netip.MustParseAddr("192.168.50.15")
	pub := netip.MustParseAddr("203.0.113.11")
	const self, child, app = 10, 11, 42
	ours := func(pid int32) bool { return pid == self || pid == child }
	stray := PCB{LocalPort: 49528, LocalAddr: en0, RemoteAddr: pub, LastPID: app}
	pcbs := []PCB{
		stray,
		{LocalAddr: en0, RemoteAddr: pub, BoundToInterface: true, LastPID: 99},          // Tailscale / Core 直连规则:绑了网卡
		{LocalAddr: en0, RemoteAddr: pub, LastPID: child},                               // 隧道子进程到服务器
		{LocalAddr: en0, RemoteAddr: netip.MustParseAddr("192.168.50.2"), LastPID: app}, // 私网恒直连
		{LocalAddr: en0, RemoteAddr: netip.MustParseAddr("100.100.1.1"), LastPID: app},  // CGNAT(overlay)
		{LocalAddr: netip.MustParseAddr("198.51.100.1"), RemoteAddr: pub, LastPID: app}, // 本地地址在 TUN 上
		{LocalAddr: en0, LastPID: app},                                                  // 没连接的 UDP socket
		{LocalAddr: en0, RemoteAddr: netip.MustParseAddr("198.18.0.16"), LastPID: app},  // bx 的 fake IP:到不了真实主机
		{LocalAddr: en0, RemoteAddr: netip.MustParseAddr("224.0.0.251"), LastPID: app},  // 组播
	}
	got := StrayConnections(pcbs, []netip.Addr{en0}, ours)
	if len(got) != 1 || got[0] != stray {
		t.Fatalf("stray = %+v, want only the unbound app connection %+v", got, stray)
	}
	if got := StrayConnections(pcbs, nil, ours); len(got) != 0 {
		t.Fatalf("without knowing the physical addresses nothing can be judged stray, got %+v", got)
	}
}
