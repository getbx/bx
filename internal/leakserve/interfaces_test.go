package leakserve

import (
	"net"
	"testing"

	"github.com/getbx/bx/internal/leakcheck"
)

// 翻译按**实测标志**定(2026-08-12,macOS 与 Linux 各跑一遍真设备)。
func TestClassifyInterfaceFlags(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags net.Flags
		want  leakcheck.InterfaceKind
	}{
		// macOS utun0 / Linux IFF_TUN 都是这个 —— **跨平台一致的隧道正证据**。
		{"点对点(utun / IFF_TUN)", net.FlagUp | net.FlagPointToPoint | net.FlagMulticast, leakcheck.InterfaceTunnel},
		{"以太网(en0 / eth0)", net.FlagUp | net.FlagBroadcast | net.FlagMulticast, leakcheck.InterfacePhysical},
		{"回环", net.FlagUp | net.FlagLoopback | net.FlagMulticast, leakcheck.InterfaceLoopback},
		// **Linux 的 IFF_TAP 落在这里**:二层设备,内核眼里就是一段以太网。
		// 这是标志判据的已知盲区,名字表因此不能退休。
		{"TAP(二层,与物理网卡无法区分)", net.FlagBroadcast | net.FlagMulticast, leakcheck.InterfacePhysical},
		// macOS 的 stf0 就是没有任何标志 —— **不猜**。
		{"什么标志都没有", 0, leakcheck.InterfaceUnknown},
		// 回环优先于其它:lo 也带 broadcast 的系统上不能被判成物理网卡。
		{"回环 + 广播", net.FlagLoopback | net.FlagBroadcast, leakcheck.InterfaceLoopback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyInterfaceFlags(tc.flags); got != tc.want {
				t.Fatalf("= %v, want %v", got, tc.want)
			}
		})
	}
}

// **本机实测一遍:这台机器上必须至少认出一个物理接口和一个回环。**
//
// 一份全是 Unknown 的映射与「没采集」在下游无法区分,而那正是这个判据要消灭的东西。
func TestListInterfaceKindsRecognisesThisMachine(t *testing.T) {
	kinds := listInterfaceKinds()
	if len(kinds) == 0 {
		t.Fatal("一台开着机的电脑不可能没有接口 —— 这里返回空说明采集坏了")
	}
	var physical, loopback int
	for _, k := range kinds {
		switch k {
		case leakcheck.InterfacePhysical:
			physical++
		case leakcheck.InterfaceLoopback:
			loopback++
		}
	}
	if loopback == 0 {
		t.Errorf("没认出回环接口:%v", kinds)
	}
	if physical == 0 {
		t.Errorf("没认出任何物理接口:%v", kinds)
	}
}
