package dns

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/getbx/bx/internal/route"
)

// SplitRoute 是一条编译好的 split 路由:域名匹配器 + 目标内网 DNS(host:port)。
type SplitRoute struct {
	Match  *route.DomainSet
	Server string
}

// Forwarder 把原始 DNS 查询字节转发到指定 server 并返回应答字节。
type Forwarder interface {
	Forward(ctx context.Context, server string, query []byte) ([]byte, error)
}

// contextDialer 是 Forward 拨号所需的最小接口(*net.Dialer 满足;生产注入 DirectDialer 防环)。
type contextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type udpForwarder struct {
	d contextDialer
}

// NewUDPForwarder 用给定拨号器(生产=DirectDialer)构造 UDP DNS 转发器。
func NewUDPForwarder(d contextDialer) Forwarder { return &udpForwarder{d: d} }

func (f *udpForwarder) Forward(ctx context.Context, server string, query []byte) ([]byte, error) {
	conn, err := f.d.DialContext(ctx, "udp", server)
	if err != nil {
		return nil, fmt.Errorf("拨内网 DNS %s: %w", server, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(query); err != nil {
		return nil, fmt.Errorf("发查询: %w", err)
	}
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("读应答: %w", err)
	}
	return buf[:n], nil
}
