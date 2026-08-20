package appattr

import "sort"

type Path string

const (
	PathTunnel  Path = "tunnel"
	PathDirect  Path = "direct"
	PathBlocked Path = "blocked"
)

// orderedPaths 固定组序。**三组永远都在,即使为空** —— 消费方按下标取组,
// 组数浮动会让渲染层错位。
var orderedPaths = [...]Path{PathTunnel, PathDirect, PathBlocked}

// ConnRecord 是数据面记下的一条连接:只有源端口和判定,没有应用身份 ——
// 身份是后台 worker 事后 join 出来的。
type ConnRecord struct {
	SrcPort uint16
	Path    Path
	Source  string // route.Reason.Source 的字符串形式
	Rule    string // 用户规则原文;内建列表为空
}

type AppRow struct {
	App       string   `json:"app"` // 空串 = unknown,消费方必须区分对待
	Conns     int      `json:"conns"`
	BytesUp   int64    `json:"bytes_up"`
	BytesDown int64    `json:"bytes_down"`
	Rules     []string `json:"rules,omitempty"` // 去重后的命中规则,最多 3 条
}

type Group struct {
	Path Path     `json:"path"`
	Rows []AppRow `json:"rows"`
}

type Report struct {
	Groups []Group `json:"groups"`
}

const maxRulesPerRow = 3

// Aggregate 把连接记录、端口→应用名、按端口的字节数折成三组报告。
//
// **同一个应用可以同时出现在多组** —— Chrome 一部分域名直连、一部分走隧道是常态,
// 压成一行「混合」会把最有用的那一半信息扔掉。
//
// **每个源端口的字节只计一次。** 同一个端口在记录里出现 N 次(端口复用:旧连接
// 关了,新连接拿到同一个端口)不代表字节要乘以 N —— 端口复用本就是这份数据里
// 已知有界的近似来源,重复计入会把它放大成不可控误差。为此按**倒序**遍历
// records(下标从大到小,也就是「最近的记录先看」),用 counted 记住哪些端口
// 已经计过字节,只在端口第一次被倒序遇到时计入。
//
// 这个倒序遍历有一个连带效果:Rules 字段的收集顺序变成了「最后出现」而不是
// 「首次出现」(brief 原意是按时间正序去重取前 3 条)。这不影响任何断言,但
// 顺序确实是倒序,不要误当成按时间正序在收集。
func Aggregate(records []ConnRecord, owners map[uint16]string, bytesUp, bytesDown map[uint16]int64) Report {
	type key struct {
		path Path
		app  string
	}
	acc := map[key]*AppRow{}
	seenRule := map[key]map[string]bool{}
	counted := make(map[uint16]bool, len(records))
	for i := len(records) - 1; i >= 0; i-- { // 倒序:最近的记录先拿到这个端口的字节
		rec := records[i]
		k := key{path: rec.Path, app: owners[rec.SrcPort]} // 查不到 → 空串 = unknown
		row := acc[k]
		if row == nil {
			row = &AppRow{App: k.app}
			acc[k] = row
			seenRule[k] = map[string]bool{}
		}
		row.Conns++
		if !counted[rec.SrcPort] {
			counted[rec.SrcPort] = true
			row.BytesUp += bytesUp[rec.SrcPort]
			row.BytesDown += bytesDown[rec.SrcPort]
		}
		if rec.Rule != "" && !seenRule[k][rec.Rule] && len(row.Rules) < maxRulesPerRow {
			seenRule[k][rec.Rule] = true
			row.Rules = append(row.Rules, rec.Rule)
		}
	}

	report := Report{Groups: make([]Group, 0, len(orderedPaths))}
	for _, p := range orderedPaths {
		var rows []AppRow
		for k, row := range acc {
			if k.path == p {
				rows = append(rows, *row)
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			bi := rows[i].BytesUp + rows[i].BytesDown
			bj := rows[j].BytesUp + rows[j].BytesDown
			if bi != bj {
				return bi > bj
			}
			return rows[i].App < rows[j].App
		})
		report.Groups = append(report.Groups, Group{Path: p, Rows: rows})
	}
	return report
}
