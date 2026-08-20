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

// PortKey 是 owners/bytesUp/bytesDown 三张 map 的 join 键。
//
// **不能只用端口号** —— TCP 与 UDP 是两个独立的端口空间,同一个数字完全可能
// 同时被一个 TCP socket 和一个 UDP socket 占用(内核层面它们互不相干)。只用
// uint16 当键会让后写入的那个协议**静默覆盖**先写入的归因:不是两次读之间状态
// 变化的 TOCTOU,而是结构性碰撞 —— 即便两次 sysctl 原子瞬时完成,碰撞依然发生。
type PortKey struct {
	Port uint16
	UDP  bool
}

// Owner 是一个端口背后的应用身份:**显示名 + 可执行路径**。
//
// 路径是 2026-08-20 加的,唯一的消费方是菜单侧的图标绘制
// (`NSWorkspace.icon(forFile:)` 认路径,不认显示名)。
//
// **这是一次刻意的信息面扩大,不是顺手带上的字段**:在此之前离开 Core 的只有
// 显示名(「Google Chrome」),现在是完整可执行路径(「/Applications/Google
// Chrome.app/Contents/MacOS/Google Chrome」)—— 后者能暴露安装位置、用户名
// (`/Users/<name>/…`)、以及从 App Store 之外装了什么。同一台机器、同一个信任
// 边界(报告只经 Core 控制 socket → Guardian owner 门 → 菜单,与显示名走同一条
// 路、同一道门),项目所有者已明确同意。**要再扩大发布面之前,先回头看这一段。**
type Owner struct {
	Name     string
	ExecPath string
}

// ConnRecord 是数据面记下的一条连接:只有源端口和判定,没有应用身份 ——
// 身份是后台 worker 事后 join 出来的。
type ConnRecord struct {
	SrcPort uint16
	UDP     bool // 与 route.Meta.UDP 同源;决定 join 时落在 PortKey 的哪一半
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

	// ExecPath 是这一行的**代表**可执行路径,**不是全集**。AppRow 按
	// (路径, 显示名) 聚合,而同一个显示名可以来自多个 PID(Chrome 的 helper
	// 进程各有各的可执行路径,`DisplayName` 把它们全折成「Google Chrome」)——
	// 这里只保证是**某一个**贡献端口的路径,且只要有任一贡献端口带着路径就不为空。
	// 消费方只拿它去取一个图标,取哪一个 helper 的都一样;**别拿它当「这一行的
	// 全部进程」用**。
	//
	// omitempty:问不出路径(unknown 行、或 kern.procargs2 读失败)时整个键缺席,
	// 与「查过了、是空串」不必区分 —— 两者对图标绘制是同一件事:不画。
	ExecPath string `json:"exec_path,omitempty"`
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
func Aggregate(records []ConnRecord, owners map[PortKey]Owner, bytesUp, bytesDown map[PortKey]int64) Report {
	type key struct {
		path Path
		app  string
	}
	acc := map[key]*AppRow{}
	seenRule := map[key]map[string]bool{}
	counted := make(map[PortKey]bool, len(records))
	for i := len(records) - 1; i >= 0; i-- { // 倒序:最近的记录先拿到这个端口的字节
		rec := records[i]
		pk := PortKey{Port: rec.SrcPort, UDP: rec.UDP}
		owner := owners[pk]                       // 查不到 → 零值 = unknown
		k := key{path: rec.Path, app: owner.Name} // 空名字 = unknown
		row := acc[k]
		if row == nil {
			row = &AppRow{App: k.app}
			acc[k] = row
			seenRule[k] = map[string]bool{}
		}
		// 代表路径:第一个带路径的贡献端口胜出(倒序遍历 ⇒ 通常是最近的那条)。
		// **不覆盖已有值** —— 谁当代表无所谓,但「明明有贡献端口带着路径,这一行
		// 却空着」会让那一行无声地失去图标。unknown 行的 owner 是零值,故恒空。
		if row.ExecPath == "" && k.app != "" {
			row.ExecPath = owner.ExecPath
		}
		row.Conns++
		if !counted[pk] {
			counted[pk] = true
			row.BytesUp += bytesUp[pk]
			row.BytesDown += bytesDown[pk]
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
