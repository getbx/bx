package route

import "strings"

// DomainSet 做域名后缀匹配。规则 "a.com" 匹配 a.com 及其子域;
// "*.a.com" 等价处理(去掉前缀 *. 后按后缀匹配,同时覆盖裸域)。
type DomainSet struct {
	suffixes map[string]struct{}
	// original 记住每个后缀**在配置里写成什么样**,供归因点名到具体那一行。
	original map[string]string
}

func NewDomainSet(patterns []string) *DomainSet {
	s := &DomainSet{suffixes: make(map[string]struct{}), original: make(map[string]string)}
	for _, p := range patterns {
		raw := strings.TrimSpace(p)
		p = strings.ToLower(raw)
		p = strings.TrimPrefix(p, "*.")
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		s.suffixes[p] = struct{}{}
		// 同一后缀被写过多次时保留第一条 —— 报哪一条都对,但要稳定。
		if _, seen := s.original[p]; !seen {
			s.original[p] = raw
		}
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

// MatchRule 与 Match 判定相同,并额外返回**命中的规则原文**。
//
// 原文而不是内部归一化后的形式:内部把 `*.a.com` 存成 `a.com`(见 NewDomainSet),
// 而归因要点名的是用户 config 里那一行 —— 报 `steamstatic.com` 而他写的是
// `'*.steamstatic.com'`,他会去搜一个搜不到的串。
func (s *DomainSet) MatchRule(domain string) (string, bool) {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	for {
		if _, ok := s.suffixes[domain]; ok {
			return s.original[domain], true
		}
		i := strings.IndexByte(domain, '.')
		if i < 0 {
			return "", false
		}
		domain = domain[i+1:]
	}
}
