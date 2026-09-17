package dns

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/getbx/bx/internal/route"
)

// SplitRoute 是一条编译好的 split 路由:域名匹配器 + 一组内网 DNS(host:port)。
//
// **一组而不是一台,而且语义是并发查、先到先用。** AD 域控几乎必然成对,
// 而顺序回退对最常见的故障形态(挂了 = 不应答)无效:第一台要先耗满预算,
// 系统 resolver 早就放弃了。内网 DNS 就在局域网(真机实测往返 14ms),
// 并发问两台的成本可以忽略。
type SplitRoute struct {
	Match   *route.DomainSet
	Servers []string
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

// forwarderFallbackTimeout 是调用方没给 deadline 时的兜底。
//
// **调用方给了就听调用方的** —— 原先这里无条件 `SetDeadline(now+5s)`,于是
// 上层无论把预算压到多短都不起作用(ctx 取消也不会叫醒一个阻塞的 Read)。
// 一个悄悄盖掉调用方预算的超时,与没有预算这个概念是同一回事。
const forwarderFallbackTimeout = 5 * time.Second

func (f *udpForwarder) Forward(ctx context.Context, server string, query []byte) ([]byte, error) {
	conn, err := f.d.DialContext(ctx, "udp", server)
	if err != nil {
		return nil, fmt.Errorf("拨内网 DNS %s: %w", server, err)
	}
	defer conn.Close()
	// ctx **取消**(不是到期)时把 conn 关掉叫醒 Read —— 先到先用会取消其余几路,
	// 少了这一句它们会一直阻塞到自己的 deadline,goroutine 与 fd 一起挂着。
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.Now().Add(forwarderFallbackTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
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
