package tun

import (
	"net/netip"
	"testing"

	"github.com/getbx/bx/internal/route"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// metaFromID 把 netstack 的连接 ID 转成分流脑要的 Meta:
// TUN 入站连接里,LocalAddress/LocalPort 就是程序要连的目标。
func TestMetaFromID_TCP(t *testing.T) {
	id := stack.TransportEndpointID{
		LocalAddress:  tcpip.AddrFrom4([4]byte{1, 2, 3, 4}),
		LocalPort:     443,
		RemoteAddress: tcpip.AddrFrom4([4]byte{10, 0, 0, 2}),
		RemotePort:    51000,
	}

	got := metaFromID(id, false)

	want := route.Meta{IP: netip.AddrFrom4([4]byte{1, 2, 3, 4}), Port: 443, UDP: false}
	if got != want {
		t.Fatalf("metaFromID = %+v, want %+v", got, want)
	}
}

func TestMetaFromID_UDP_IPv6(t *testing.T) {
	raw := [16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x35}
	id := stack.TransportEndpointID{
		LocalAddress: tcpip.AddrFrom16(raw),
		LocalPort:    53,
	}

	got := metaFromID(id, true)

	if !got.UDP {
		t.Errorf("UDP = false, want true")
	}
	if got.Port != 53 {
		t.Errorf("Port = %d, want 53", got.Port)
	}
	wantIP := netip.AddrFrom16(raw)
	if got.IP != wantIP {
		t.Errorf("IP = %v, want %v", got.IP, wantIP)
	}
}
