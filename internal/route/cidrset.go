package route

import (
	"net/netip"
	"strings"
)

// CIDRSet 是 IP 前缀集合,Contains 判断 IP 是否落入任一前缀。
type CIDRSet struct {
	prefixes []netip.Prefix
}

// NewCIDRSet 从 CIDR 字符串构建;空行、# 注释、非法行自动跳过。
func NewCIDRSet(lines []string) (*CIDRSet, error) {
	s := &CIDRSet{}
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		p, err := netip.ParsePrefix(ln)
		if err != nil {
			continue // 容错:跳过坏行
		}
		s.prefixes = append(s.prefixes, p)
	}
	return s, nil
}

func (s *CIDRSet) Contains(ip netip.Addr) bool {
	for _, p := range s.prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
