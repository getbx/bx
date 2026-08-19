// Package appattr 是「这条连接是哪个应用发起的」这件事的**纯判据**:
// 解析 macOS 的 pcblist 字节、按委托规则选出归属进程、把可执行路径变成显示名、
// 把连接记录聚合成报告。它不读文件、不联网、不跑命令 —— 取数据是调用方的事。
package appattr

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// xgen_n 的 kind。每个块自带长度与种类,所以不必知道全部字段布局。
const (
	kindSocket = 0x001
	kindInpcb  = 0x010
)

// XSO_SOCKET 块里的字段偏移。**实测得来**(2026-08-19,macOS 25.5,与 lsof 逐条对账):
//
//	[64] so_uid   [68] so_last_pid   [72] so_e_pid
//
// 其后还有 so_gencnt(8) / so_flags / so_flags1 / so_usecount / so_retaincnt /
// xso_filter_flags —— 所以**不能**从块尾往回取,那是最初写错的地方,TCP 会全读成 0。
const (
	offSoLastPID = 68
	offSoEPID    = 72
	minSocketLen = offSoEPID + 4
)

// PCB 是一条内核 socket 记录里我们关心的全部内容。
type PCB struct {
	LocalPort  uint16 // 应用侧本地端口 —— 与 route.Meta.SrcPort 的 join 键
	RemotePort uint16
	LastPID    int32 // 最后一个用过这个 socket 的进程
	EPID       int32 // 「替谁干活」;可能指向已退出的进程,取用前必须查活性
}

var errShortPcbList = errors.New("pcblist too short")

// ParsePcbList 解析 net.inet.{tcp,udp}.pcblist_n 的原始字节。
//
// 布局:开头一个 struct xinpgen(自带 xig_len),之后是一串自描述的块
// {u32 len, u32 kind, …};每个 pcb 由连续的若干块组成,XSO_INPCB 打头、
// XSO_SOCKET 紧随,TCP 还会多出 XSO_TCPCB 等。
func ParsePcbList(raw []byte) ([]PCB, error) {
	if len(raw) < 24 {
		return nil, errShortPcbList
	}
	off := int(binary.NativeEndian.Uint32(raw[0:4]))
	if off < 8 || off > len(raw) {
		return nil, fmt.Errorf("pcblist header length %d out of range", off)
	}
	var out []PCB
	var cur PCB
	var haveInpcb bool
	for off+8 <= len(raw) {
		blkLen := int(binary.NativeEndian.Uint32(raw[off : off+4]))
		kind := binary.NativeEndian.Uint32(raw[off+4 : off+8])
		if blkLen < 8 || off+blkLen > len(raw) {
			break // 尾部的 xinpgen,或截断
		}
		blk := raw[off : off+blkLen]
		switch kind {
		case kindInpcb:
			// xi_len(4) xi_kind(4) xi_inpp(8) inp_fport(2) inp_lport(2)…
			// 端口是网络字节序。
			if blkLen >= 20 {
				cur = PCB{
					RemotePort: binary.BigEndian.Uint16(blk[16:18]),
					LocalPort:  binary.BigEndian.Uint16(blk[18:20]),
				}
				haveInpcb = true
			}
		case kindSocket:
			if haveInpcb && blkLen >= minSocketLen {
				cur.LastPID = int32(binary.NativeEndian.Uint32(blk[offSoLastPID : offSoLastPID+4]))
				cur.EPID = int32(binary.NativeEndian.Uint32(blk[offSoEPID : offSoEPID+4]))
				out = append(out, cur)
				haveInpcb = false
			}
		}
		// **块按 8 字节对齐,而 xso_len 不含尾部填充。** TCP 的 XSO_TCPCB 块
		// len=204,下一块其实在 +208 —— 不补齐会读到全 0,整张 TCP 表只解出一条,
		// 而 UDP(没有那个块)看起来完全正常。
		off += (blkLen + 7) &^ 7
	}
	return out, nil
}
