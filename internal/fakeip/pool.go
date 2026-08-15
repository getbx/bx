package fakeip

import (
	"fmt"
	"net/netip"
	"sync"
)

// Pool 从一段 CIDR 里给域名分配稳定的 fake IP,并支持反查。
type Pool struct {
	mu     sync.Mutex
	prefix netip.Prefix
	next   netip.Addr
	d2ip   map[string]netip.Addr
	ip2d   map[netip.Addr]string
}

func New(cidr string) (*Pool, error) {
	pfx, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, fmt.Errorf("fakeip cidr: %w", err)
	}
	return &Pool{
		prefix: pfx,
		next:   pfx.Addr().Next(), // 跳过网络地址
		d2ip:   make(map[string]netip.Addr),
		ip2d:   make(map[netip.Addr]string),
	}, nil
}

// Alloc 返回域名对应的 fake IP(已存在则复用)。
func (p *Pool) Alloc(domain string) netip.Addr {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ip, ok := p.d2ip[domain]; ok {
		return ip
	}
	ip := p.next
	p.next = ip.Next()
	if !p.prefix.Contains(p.next) {
		// 用尽则回绕(覆盖最早的映射)
		p.next = p.prefix.Addr().Next()
	}
	// 该 IP 若已属于旧域名,先清除旧域名的正向映射,避免两域名共享同一 IP。
	if old, ok := p.ip2d[ip]; ok {
		delete(p.d2ip, old)
	}
	p.d2ip[domain] = ip
	p.ip2d[ip] = domain
	return ip
}

// Domain 反查 fake IP 对应的域名。
func (p *Pool) Domain(ip netip.Addr) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	d, ok := p.ip2d[ip]
	return d, ok
}

// Contains 判断这个地址是不是**本池负责的那段**里的。
//
// 与 Domain 是两个问题:Domain 回答「我给谁发过这个地址」,Contains 回答
// 「这个地址本来就该是我发的」。两者不一致(在段里但反查不到)意味着一次
// **丢失的映射** —— 池重建过、或者应用攥着一个上一轮的地址。
//
// 调用方需要分得开这一态:把「在段里但认不出来」与「一个真实的公网 IP」混作
// 一谈,就等于把「问不出来」报成一个具体答案。
func (p *Pool) Contains(ip netip.Addr) bool {
	return p.prefix.Contains(ip)
}
