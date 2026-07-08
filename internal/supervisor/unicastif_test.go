package supervisor

import (
	"encoding/binary"
	"testing"
)

// IPv4 IP_UNICAST_IF 的选项取值须在(小端的 Windows 目标)内存里排成 big-endian(index)。
// 这是施工图点名的「头号坑」:直接传 index(忘字节序)会绑错网卡、直连绕不开 TUN。
// 与主机端序无关地校验不变量——把返回值按 Windows 目标的小端布局写回,字节必须等于
// index 的大端编码。
func TestUnicastIfV4ValueBigEndianLayout(t *testing.T) {
	for _, idx := range []uint32{1, 2, 5, 13, 0x01020304, 0xdeadbeef} {
		got := unicastIfV4Value(idx)
		var gotBytes [4]byte
		binary.LittleEndian.PutUint32(gotBytes[:], got) // Windows 目标均小端:模拟其选项缓冲内存布局
		var want [4]byte
		binary.BigEndian.PutUint32(want[:], idx)
		if gotBytes != want {
			t.Errorf("unicastIfV4Value(%#x): 小端内存字节=% x, 期望大端布局=% x", idx, gotBytes, want)
		}
	}
}

// 具体值回归:index=1 必须变成 0x01000000(字节交换),而不是原样 1。
func TestUnicastIfV4ValueByteSwap(t *testing.T) {
	if got := unicastIfV4Value(1); got != 0x01000000 {
		t.Errorf("unicastIfV4Value(1) 应字节交换为 0x01000000,实得 %#x(忘了网络字节序?)", got)
	}
}
