package route

import "net/netip"

// Decision 是分流判定结果。
type Decision int

const (
	Direct      Decision = iota // 直连
	Proxy                       // 走 brook 代理
	Block                       // kill-switch 阻断
	NeedResolve                 // 有域名但未命中规则,需解析后用 DecideIP 再判
	// Via 交给一个具名出口(config 里的 egress[].name,规则在 Reason.Rule 里)。
	//
	// **它排在 Direct/Proxy 之外自成一档,而不是复用 Proxy**:Proxy 的失败语义
	// 由 kill-switch 定(隧道挂了就阻断),而具名出口有自己的一套 —— 它走
	// loopback、与主隧道无关,主隧道挂了不该连累它。混进 Proxy 会让两套语义
	// 在同一个分支里打架。
	Via
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
	case Via:
		return "via"
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
