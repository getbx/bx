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
		// 用尽则回绕(覆盖最早的映射);MVP 简单处理
		p.next = p.prefix.Addr().Next()
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
