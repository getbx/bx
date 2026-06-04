package route

import "strings"

// DomainSet 做域名后缀匹配。规则 "a.com" 匹配 a.com 及其子域;
// "*.a.com" 等价处理(去掉前缀 *. 后按后缀匹配,同时覆盖裸域)。
type DomainSet struct {
	suffixes map[string]struct{}
}

func NewDomainSet(patterns []string) *DomainSet {
	s := &DomainSet{suffixes: make(map[string]struct{})}
	for _, p := range patterns {
		p = strings.TrimSpace(strings.ToLower(p))
		p = strings.TrimPrefix(p, "*.")
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		s.suffixes[p] = struct{}{}
	}
	return s
}

// Match 判断域名是否命中:自身或任一父域在集合中。
func (s *DomainSet) Match(domain string) bool {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	for {
		if _, ok := s.suffixes[domain]; ok {
			return true
		}
		i := strings.IndexByte(domain, '.')
		if i < 0 {
			return false
		}
		domain = domain[i+1:]
	}
}
