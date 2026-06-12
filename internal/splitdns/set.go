// Package splitdns 提供 split-DNS 的「强制直连 IP」旁路集:DNS 层把内网 DNS 解析出的
// 真实 IP 注册进来,dialer 在分流时查它命中即强制 Direct。共享、跨 Router 热重载存活。
package splitdns

import (
	"net/netip"
	"sync"
)

// Set 是并发安全的 netip.Addr 集合(不淘汰:内网 IP 少而稳)。
type Set struct {
	mu sync.RWMutex
	m  map[netip.Addr]struct{}
}

func NewSet() *Set {
	return &Set{m: make(map[netip.Addr]struct{})}
}

func (s *Set) Add(ip netip.Addr) {
	s.mu.Lock()
	s.m[ip] = struct{}{}
	s.mu.Unlock()
}

func (s *Set) Contains(ip netip.Addr) bool {
	s.mu.RLock()
	_, ok := s.m[ip]
	s.mu.RUnlock()
	return ok
}
