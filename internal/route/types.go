package route

import "net/netip"

// Decision 是分流判定结果。
type Decision int

const (
	Direct      Decision = iota // 直连
	Proxy                       // 走 brook 代理
	Block                       // kill-switch 阻断
	NeedResolve                 // 有域名但未命中规则,需解析后用 DecideIP 再判
)

func (d Decision) String() string {
	switch d {
	case Direct:
		return "direct"
	case Proxy:
		return "proxy"
	case Block:
		return "block"
	case NeedResolve:
		return "need-resolve"
	default:
		return "unknown"
	}
}

// Meta 描述一条待判定的连接。Domain 可能为空(裸 IP 连接)。
type Meta struct {
	Domain string
	IP     netip.Addr
	Port   uint16
	UDP    bool
}
