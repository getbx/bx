package route

import (
	"fmt"
	"net/netip"
	"strings"
)

// EgressSet 把网段映射到具名出口。**白名单**:没写进来的地址一律不匹配。
//
// 它与 CIDRSet 的区别只有一个:命中之后还要答「交给哪个出口」。做成独立类型
// 而不是给 CIDRSet 加个字段,是因为它的语义不同 —— CIDRSet 回答「在不在里面」,
// 这个回答「归谁」,而后者在 Reason.Rule 里要报给用户看。
type EgressSet struct {
	entries []egressEntry
}

type egressEntry struct {
	prefix netip.Prefix
	name   string
}

// NewEgressSet 按 (出口名 → 网段) 构造。
//
// **顺序即优先级**:先加的先匹配。两条规则覆盖同一个地址时,用户写在前面的那条
// 生效 —— 与 rules 在配置文件里的阅读顺序一致,不给人惊喜。
func NewEgressSet(byName [][2]string) (*EgressSet, error) {
	set := &EgressSet{}
	for _, pair := range byName {
		name, cidr := strings.TrimSpace(pair[0]), strings.TrimSpace(pair[1])
		if name == "" {
			return nil, fmt.Errorf("egress set: 网段 %q 没有出口名", cidr)
		}
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, fmt.Errorf("egress set: %q 不是合法网段: %w", cidr, err)
		}
		set.entries = append(set.entries, egressEntry{prefix: prefix.Masked(), name: name})
	}
	return set, nil
}

// Lookup 回答「这个地址归哪个出口」。空集合恒不匹配。
func (s *EgressSet) Lookup(ip netip.Addr) (string, bool) {
	if s == nil || len(s.entries) == 0 || !ip.IsValid() {
		return "", false
	}
	// 去掉 v4-in-v6 映射,否则 ::ffff:10.84.3.239 会匹配不上 10.84.0.0/16
	// (与 CIDRSet 同一处理;不做的话「有时通有时不通」而毫无规律)。
	ip = ip.Unmap().WithZone("")
	for _, e := range s.entries {
		if e.prefix.Contains(ip) {
			return e.name, true
		}
	}
	return "", false
}

// Names 返回配置里出现过的出口名(去重,保持顺序),供接线与诊断使用。
func (s *EgressSet) Names() []string {
	if s == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range s.entries {
		if seen[e.name] {
			continue
		}
		seen[e.name] = true
		out = append(out, e.name)
	}
	return out
}
