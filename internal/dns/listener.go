package dns

import (
	"fmt"
	"net"
)

// Responder 处理一条 DNS 查询并返回应答字节。
type Responder interface {
	Respond(query []byte) ([]byte, error)
}

// ListenUDP 在本地 UDP 地址上提供 DNS 服务,复用传入的 Responder。
func ListenUDP(addr string, r Responder) (net.PacketConn, error) {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("监听 DNS %s: %w", addr, err)
	}
	go serveUDP(pc, r)
	return pc, nil
}

func serveUDP(pc net.PacketConn, r Responder) {
	buf := make([]byte, 4096)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		query := append([]byte(nil), buf[:n]...)
		go func(query []byte, from net.Addr) {
			resp, err := r.Respond(query)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(resp, from)
		}(query, from)
	}
}
