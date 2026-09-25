// Package appattr 是「这条连接是哪个应用发起的」这件事的**纯判据**:
// 解析 macOS 的 pcblist 字节、按委托规则选出归属进程、把可执行路径变成显示名、
// 把连接记录聚合成报告。它不读文件、不联网、不跑命令 —— 取数据是调用方的事。
package appattr

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
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

// XSO_INPCB(struct xinpcb_n)里的字段偏移。**实测得来**(2026-09-25,macOS 26,
// 与 netstat -anv 逐条对账):
//
//	[16] inp_fport  [18] inp_lport  [36] inp_flags  [44] inp_vflag
//	[60] 远端 IPv4(inp_dependfaddr 的 in_addr_4in6 末 4 字节)
//	[76] 本地 IPv4(inp_dependladdr 同上)
//
// **inpBoundIF 是 XNU 的 INP_BOUND_IF(0x4000),不是 0x00800000** —— 后者在所有 socket
// 上都置着,拿它当判据会把每一条连接都判成「绑了网卡」。实测判别:Tailscale 的
// socket 0x804840、Chrome 0x800840,差的正是 0x4000;bx Core 自己的直连(IP_BOUND_IF)
// 也都带着它。
const (
	offInpFlags     = 36
	offInpVflag     = 44
	offInpFaddr4    = 60
	offInpLaddr4    = 76
	minInpcbAddrLen = offInpLaddr4 + 4

	inpBoundIF = 0x4000
	inpIPv4    = 0x1
)

// PCB 是一条内核 socket 记录里我们关心的全部内容。
type PCB struct {
	LocalPort  uint16 // 应用侧本地端口 —— 与 route.Meta.SrcPort 的 join 键
	RemotePort uint16
	LastPID    int32 // 最后一个用过这个 socket 的进程
	EPID       int32 // 「替谁干活」;可能指向已退出的进程,取用前必须查活性

	// LocalAddr / RemoteAddr 只对 IPv4 socket 填(inp_vflag 带 INP_IPV4),否则零值。
	LocalAddr  netip.Addr
	RemoteAddr netip.Addr
	// BoundToInterface:socket 用 IP_BOUND_IF 绑在了某块网卡上(INP_BOUND_IF)。
	BoundToInterface bool
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
				if blkLen >= minInpcbAddrLen {
					cur.BoundToInterface = binary.NativeEndian.Uint32(blk[offInpFlags:offInpFlags+4])&inpBoundIF != 0
					if blk[offInpVflag]&inpIPv4 != 0 {
						cur.RemoteAddr = netip.AddrFrom4([4]byte(blk[offInpFaddr4 : offInpFaddr4+4]))
						cur.LocalAddr = netip.AddrFrom4([4]byte(blk[offInpLaddr4 : offInpLaddr4+4]))
					}
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
