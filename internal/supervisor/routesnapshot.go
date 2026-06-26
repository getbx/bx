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
	table  string // "main"/"local"/"default"/"100"/"52"...
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

// stripMask 去掉 fwmark 的掩码后缀(有些 iproute2 打 "0x162/0xffffffff")。
func stripMask(s string) string {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return s
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
	a = append(a, "table", r.table)
	return a
}

// routeAddArgs 把 table-100 routeSpec 重建成 `ip [-6] route add ... table 100`。
func routeAddArgs(r routeSpec) []string {
	var a []string
	if r.family == familyV6 {
		a = append(a, "-6")
	}
	a = append(a, "route", "add")
	if r.typ != "" {
		a = append(a, r.typ)
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
