// routesnapshot.go 是 Linux 路由快照器的纯逻辑层(无 build tag,可在任何平台原生单测):
// 解析 `ip rule`/`ip route` 文本、diff 规则、把 spec 重建回 `ip` 命令参数。
// IO 薄壳(真跑 ip 命令)在 systemsnapshot_linux.go。
package supervisor

import (
	"strconv"
	"strings"
)

type ipFamily int

const (
	familyV4 ipFamily = iota
	familyV6
)

// ruleSpec 是一条策略路由规则的可比较表示,足以重建 `ip [-6] rule add/del`。
type ruleSpec struct {
	family ipFamily
	pref   int
	fwmark string // "" 表示无;否则如 "0x162"
	toCIDR string // "" 表示无 to 选择子
	// ipproto 是 `ip rule` 的协议选择子("udp"/"tcp"/…;"" 表示无)。
	//
	// **它必须被记住,否则 Restore 删不掉那条规则。** `ip rule del` 要求所有
	// 选择子都对得上;2026-09-04 加的那条 Tailscale 规则带 `ipproto udp`,
	// 而这里此前不认它 —— 重建出的删除命令少一个选择子,内核里那条匹配不上,
	// 于是**装得上、拆不掉**(2026-09-15 由 netns 集成台抓到)。
	// 一条 bx 自己装上、自己却还原不掉的策略路由,在这个仓库里有前科(孤儿屏障)。
	ipproto string
	table   string // "main"/"local"/"default"/"100"/"52"...
}

// routeSpec 是 table 100 一条路由的可比较表示,足以重建 `ip [-6] route add ... table 100`。
type routeSpec struct {
	family ipFamily
	typ    string // "" 普通;或 "unreachable"
	dst    string // "default" 或 CIDR/IP
	via    string // "" 表示无
	dev    string // "" 表示无
}

// parseRules 解析 `ip [-6] rule list` 输出。无法表示的选择子尽力提取(pref+table 必有)。
func parseRules(out string, fam ipFamily) []ruleSpec {
	var specs []ruleSpec
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 形如 "100:\tfrom all fwmark 0x162 lookup main"
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		pref, err := strconv.Atoi(strings.TrimSpace(line[:colon]))
		if err != nil {
			continue
		}
		rest := strings.Fields(line[colon+1:])
		r := ruleSpec{family: fam, pref: pref}
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "fwmark":
				if i+1 < len(rest) {
					r.fwmark = stripMask(rest[i+1])
					i++
				}
			case "to":
				if i+1 < len(rest) {
					r.toCIDR = rest[i+1]
					i++
				}
			case "ipproto":
				if i+1 < len(rest) {
					r.ipproto = rest[i+1]
					i++
				}
			case "lookup":
				if i+1 < len(rest) {
					r.table = rest[i+1]
					i++
				}
			}
		}
		specs = append(specs, r)
	}
	return specs
}

// stripMask 只去掉**空掩码**后缀(有些 iproute2 对一条不带掩码加进去的规则
// 回显 "0x162/0xffffffff")。
//
// **真掩码不许剥:它是选择子的一部分。** 2026-09-04 那条 Tailscale 规则是带
// 掩码加进去的(`fwmark 0x80000/0xff0000`,只认 Tailscale 打的那几位),
// 剥掉之后 `ip rule del … fwmark 0x80000 …` 与内核里那条匹配不上 ⇒ Restore
// 删不掉它 ⇒ **bx 自己装上、自己却拆不掉**(2026-09-15 netns 集成台抓到,
// 而它是同一个 bug 的第三层:先是不认 ipproto,再是认了不发,最后是掩码被剥)。
func stripMask(s string) string {
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return s
	}
	mask, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(s[i+1:]), "0x"), 16, 64)
	if err != nil || mask != 0xffffffff {
		return s // 认不出、或真的是一个掩码:原样留着
	}
	return s[:i]
}

// parseRoutes 解析 `ip [-6] route show table 100` 输出(只取重建所需:typ/dst/via/dev)。
func parseRoutes(out string, fam ipFamily) []routeSpec {
	var specs []routeSpec
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		r := routeSpec{family: fam}
		i := 0
		if f[0] == "unreachable" || f[0] == "blackhole" || f[0] == "prohibit" {
			r.typ = f[0]
			i = 1
		}
		if i >= len(f) {
			continue
		}
		r.dst = f[i]
		i++
		for ; i < len(f); i++ {
			switch f[i] {
			case "via":
				if i+1 < len(f) {
					r.via = f[i+1]
					i++
				}
			case "dev":
				if i+1 < len(f) {
					r.dev = f[i+1]
					i++
				}
			}
		}
		specs = append(specs, r)
	}
	return specs
}

// diffRules 算集合差:toDel = current 中不在 target 的(需删除),
// toAdd = target 中不在 current 的(需补回)。ruleSpec 全字段可比较,用作 map key。
func diffRules(current, target []ruleSpec) (toDel, toAdd []ruleSpec) {
	inTarget := make(map[ruleSpec]bool, len(target))
	for _, r := range target {
		inTarget[r] = true
	}
	inCurrent := make(map[ruleSpec]bool, len(current))
	for _, r := range current {
		inCurrent[r] = true
	}
	for _, r := range current {
		if !inTarget[r] {
			toDel = append(toDel, r)
		}
	}
	for _, r := range target {
		if !inCurrent[r] {
			toAdd = append(toAdd, r)
		}
	}
	return toDel, toAdd
}

// ruleArgs 把 ruleSpec 重建成 `ip [-6] rule <verb> ...` 的参数(verb: "add"|"del")。
func ruleArgs(verb string, r ruleSpec) []string {
	var a []string
	if r.family == familyV6 {
		a = append(a, "-6")
	}
	a = append(a, "rule", verb)
	switch {
	case r.toCIDR != "":
		a = append(a, "to", r.toCIDR, "pref", strconv.Itoa(r.pref))
	case r.fwmark != "":
		a = append(a, "pref", strconv.Itoa(r.pref), "fwmark", r.fwmark)
	default:
		a = append(a, "pref", strconv.Itoa(r.pref))
	}
	// 解析出来了却不发,与没解析完全一样 —— 内核仍然匹配不上。
	if r.ipproto != "" {
		a = append(a, "ipproto", r.ipproto)
	}
	a = append(a, "table", r.table)
	return a
}

// routeAddArgs 把 table-100 routeSpec 重建成 `ip [-6] route add ... table 100`。
// typed 路由(unreachable/blackhole/prohibit)不带 via/dev:内核回显的 "dev lo" 是内部
// 注解,bx 原始命令里没有,重放时加上会被内核拒绝。
func routeAddArgs(r routeSpec) []string {
	var a []string
	if r.family == familyV6 {
		a = append(a, "-6")
	}
	a = append(a, "route", "add")
	if r.typ != "" {
		// typed 路由:仅 <typ> <dst> table 100,不带 via/dev
		a = append(a, r.typ, r.dst, "table", "100")
		return a
	}
	a = append(a, r.dst)
	if r.via != "" {
		a = append(a, "via", r.via)
	}
	if r.dev != "" {
		a = append(a, "dev", r.dev)
	}
	a = append(a, "table", "100")
	return a
}
