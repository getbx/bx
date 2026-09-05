package appattr

import (
	"sort"
	"time"
)

type Path string

const (
	PathTunnel  Path = "tunnel"
	PathDirect  Path = "direct"
	PathBlocked Path = "blocked"
)

// orderedPaths 固定组序。**三组永远都在,即使为空** —— 消费方按下标取组,
// 组数浮动会让渲染层错位。
var orderedPaths = [...]Path{PathTunnel, PathDirect, PathBlocked}

// OrderedPaths 把这张表交给跨语言守卫(菜单的 AppTrafficPath 枚举必须与它逐个对上)。
// 每次调用都返回新切片,与 GuardianCapabilities() 同一条纪律。
func OrderedPaths() []Path { return append([]Path(nil), orderedPaths[:]...) }

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

// ReportWindow 是报告覆盖的时间窗:只算**最近这么久**发生的事。
//
// **这是一个产品决定,不是随手取的数。** 窗口回答的问题是「**此刻**谁在连谁」。
// 在它之前报告的语义是「自打开窗口以来的累计」,而 owners 是读取那一刻现问内核
// 得到的「还开着的 socket」快照 —— 两者合起来让每一条已经关掉的连接永远查不回
// 主人,`unknown` 于是只涨不落。真机上它是最大的一行,而且**带着真实速率**
// (959 B/s 上 / 1.8 KB/s 下):速率不为零就说明新的 unknown 还在源源不断进来,
// 不是陈旧累积。
//
// 60 秒的取法:够长,长到一次刷新(5 秒)之间的抖动看不出来、也容得下一条刚
// 结束的连接留在界面上被看见;够短,短到一分钟前就结束的事不再占着「此刻」。
// **改它之前先想清楚窗口回答的是哪个问题**,而不是「多大合适」。
const ReportWindow = time.Minute

// InReportWindow 报告一条记录是否还落在报告窗口内。
//
// **now 由调用方传** —— 本包是纯判据,自己读时钟会让「窗口边界对不对」变成一个
// 只能靠 sleep 去验的东西。边界上那一条**留住**:判反了会让每次刷新恰好丢掉一条
// 最旧的记录,而那种丢法在界面上完全看不出来。
func InReportWindow(rec ConnRecord, now time.Time) bool {
	return !rec.At.Before(now.Add(-ReportWindow))
}

// Dest 是这个包的**第二次**刻意信息面扩大(第一次是上面 Owner 的 ExecPath)。
// 目的地(域名或裸 IP)比可执行路径更敏感 —— 一份目的地列表接近「这台机器在
// 访问什么」。发布面与 ExecPath 完全一致:Core 控制 socket → Guardian owner 门
// → 菜单那个窗口,不进 `bx status --json`、不进日志、不进诊断包,由
// publication_test.go 里独立的 destPublicationAllowlist 守住(与 ExecPath 那张表
// 刻意不合并 —— 两次扩大的理由、边界、白名单成员都不同,合并会让任何一方的
// 放宽悄悄带上另一方)。
//
// 为什么值得担这份风险:订阅之前就建好的长连接(会议媒体流、WebSocket、SSH、
// 常驻守护进程)只靠 Subscribe() 播种才看得见,不带目的地的话,恰恰是最该被
// 检查的那几条连接答不出「它在连谁」——那会把这个 task 自己的用例弄丢。

// ConnRecord 是数据面记下的一条连接:源端口 + 判定 + 产生时刻,应用身份是
// 后台 resolver 事后填回来的。
type ConnRecord struct {
	SrcPort uint16
	UDP     bool // 与 route.Meta.UDP 同源;决定 join 时落在 PortKey 的哪一半
	Path    Path
	Source  string // route.Reason.Source 的字符串形式
	Rule    string // 用户规则原文;内建列表为空

	// Dest 是这条连接连的是谁:域名优先,没有域名就落回裸 IP 字面量,两者都没有
	// 就是空串。不带 json tag —— 它不单独发布,只经 Aggregate 聚合进 AppRow.Dests
	// 之后才离开这个包。见 report.go 顶部关于「第二次信息面扩大」的说明。
	Dest string

	// At 是这条记录产生的时刻(建连那一刻,或订阅播种那一刻)。
	// 报告只覆盖最近 ReportWindow;没有它,报告就是「自打开窗口以来的累计」。
	At time.Time

	// Owner 是**事后**由后台 resolver 填回来的应用身份,不是数据面记下的
	// (热路径不做归因,那是这个设计的前提)。零值 = 还没解析出来。
	//
	// **存在记录里、而不是读取时现查,是 2026-08-20 这一版的要点**:连接一关,
	// 内核里那个端口就查不到主人了,现查必然退化成 unknown —— 而「活不过一次
	// 5 秒刷新」的短连接**全是**这种,它们结构性地填满了 unknown 那一行。
	Owner Owner
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

	// BytesUpRate/BytesDownRate 是这一行**此刻**的速率(每秒字节数),由服务端
	// 按端口做差算出(见 DiffPortRates)。**不是客户端拿相邻两次报告的行做差**
	// ——那是这个字段取代的旧做法:60 秒滚动窗口下,一行的累计字节会因为记录
	// 滑出窗口而下降,客户端把"变小"误读成"计数器复位"从而显示成破折号,
	// 而什么都没出错。按端口做差没有这个歧义:端口从采样里消失就是**不贡献**,
	// 不是复位。
	//
	// **用指针,不用 float64。** 0 B/s(应用在,但这一拍没有字节增量)与
	// 「还没有速率可报」(还没采到第二次样)是两件不同的事,压成同一个 0
	// 会让界面把"不知道"显示成"闲着"。指针 + omitempty 让"不知道"表现为
	// **键缺席**,与本仓库 `status_generation`、`BuiltinListChecked` 同一条纪律。
	//
	// **`omitempty` 对指针只看 nil,指向 0 的指针照样会被序列化** ——
	// 这一点由 TestZeroRateSerializesButUnavailableRateIsAbsent 钉住。
	BytesUpRate   *float64 `json:"bytes_up_rate,omitempty"`
	BytesDownRate *float64 `json:"bytes_down_rate,omitempty"`

	// Dests 是这一行**最近**连过的目的地,去重、最多 maxDestsPerRow 条,
	// 「最近」由 Aggregate 倒序遍历自然得出(与 Rules 同一手法)。
	Dests []string `json:"dests,omitempty"`
	// DestsMore 是超出 maxDestsPerRow、没能列出来的**去重后**目的地条数。
	// **它是承重的,不是装饰**:一个应用连了 23 个域名而界面只显示 8 个又不说
	// 还有 15 个,读者会以为它只连了 8 个 —— 而「连了很多个地方」本身就是这个
	// 功能要显形的信号。
	DestsMore int `json:"dests_more,omitempty"`
}

type Group struct {
	Path Path     `json:"path"`
	Rows []AppRow `json:"rows"`
}

type Report struct {
	Groups []Group `json:"groups"`
}

const (
	maxRulesPerRow = 3
	maxDestsPerRow = 8
)

// AggregateInput 是 Aggregate 的入参。**这是唯一的入口** —— 没有为了少改测试
// 而留一个旧签名的薄壳:本仓库反复栽在「同一个判定有两份」上。
//
// 本分支刚把 `serveControlWithPathRecovery` 的 17 个位置参数换成
// `controlServeOptions`;Aggregate 原有 4 个位置参数,这次要加 3 个,再往下就
// 不可读了,照同一个先例改成结构体。
type AggregateInput struct {
	Records   []ConnRecord
	Owners    map[PortKey]Owner
	BytesUp   map[PortKey]int64
	BytesDown map[PortKey]int64
	RateUp    map[PortKey]float64
	RateDown  map[PortKey]float64

	// RatesReady 是个显式的 bool,**不是「map 为 nil 就是不可用」的约定**:
	// 一份可用但全空的速率(所有端口这一拍都没传东西)与「还没得到速率」是
	// 两回事 —— 这正是本仓库「『没查』与『查了没有』要分得开」那条纪律。
	// false ⇒ 每一行的两个速率字段都是 nil。
	RatesReady bool
}

// Aggregate 把连接记录、端口→应用名、按端口的字节数与速率折成三组报告。
//
// **同一个应用可以同时出现在多组** —— Chrome 一部分域名直连、一部分走隧道是常态,
// 压成一行「混合」会把最有用的那一半信息扔掉。
//
// **每个源端口的字节(与速率)只计一次。** 同一个端口在记录里出现 N 次(端口
// 复用:旧连接关了,新连接拿到同一个端口)不代表字节要乘以 N —— 端口复用本就
// 是这份数据里已知有界的近似来源,重复计入会把它放大成不可控误差。为此按
// **倒序**遍历 records(下标从大到小,也就是「最近的记录先看」),用 counted
// 记住哪些端口已经计过字节,只在端口第一次被倒序遇到时计入 —— **速率复用
// 同一个 counted 去重**,不另开一遍循环。
//
// 这个倒序遍历有一个连带效果:Rules 字段的收集顺序变成了「最后出现」而不是
// 「首次出现」(brief 原意是按时间正序去重取前 3 条)。这不影响任何断言,但
// 顺序确实是倒序,不要误当成按时间正序在收集。
func Aggregate(in AggregateInput) Report {
	type key struct {
		path Path
		app  string
	}
	type accRow struct {
		AppRow
		rateUp   float64
		rateDown float64
	}
	acc := map[key]*accRow{}
	seenRule := map[key]map[string]bool{}
	seenDest := map[key]map[string]bool{}
	counted := make(map[PortKey]bool, len(in.Records))
	for i := len(in.Records) - 1; i >= 0; i-- { // 倒序:最近的记录先拿到这个端口的字节
		rec := in.Records[i]
		pk := PortKey{Port: rec.SrcPort, UDP: rec.UDP}
		// **先用记录里存着的归因,查不到才回落到现查的 map。**
		// 顺序反过来(无条件现查)就等于把这一版的修复整个撤销:连接关掉之后
		// owners 里再也没有那个端口,一条本来已经解析出来的记录会塌回 unknown。
		owner := rec.Owner
		if owner.Name == "" {
			owner = in.Owners[pk] // 还没解析出来的,给一次最后的机会;仍查不到 → unknown
		}
		k := key{path: rec.Path, app: owner.Name} // 空名字 = unknown
		row := acc[k]
		if row == nil {
			row = &accRow{AppRow: AppRow{App: k.app}}
			acc[k] = row
			seenRule[k] = map[string]bool{}
			seenDest[k] = map[string]bool{}
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
			row.BytesUp += in.BytesUp[pk]
			row.BytesDown += in.BytesDown[pk]
			// **已知的、有界的近似(修复轮 1 补记,不改设计):`in.RateUp[pk]`
			// 对一个 map 里没有的键返回零值 0,不是「没有」。** 一个端口若是
			// 在上一次采样**之后**才开始有流量(还没被下一次 sampleRatesLocked
			// 采到),它这一拍就会拿到 0、渲染成 `0 B/s`,而不是"这个端口的
			// 速率还不知道"——这与本功能反复强调的"nil ≠ 0"在**端口粒度**上
			// 不一致(行粒度仍然一致:RatesReady=false 时整行是 nil,这里说的
			// 是 RatesReady=true 时单个端口的边缘情况)。上界只有一个采样区间
			// `rateSampleInterval`(2 秒)——真机上它长得跟真的空闲一模一样,
			// 用户分不出这两种"0"。不修,因为要修就要在 AppRow 粒度之下再带
			// 一层「这个端口有没有被采到」的标记,收益(消掉一个 2 秒窗口内的
			// 视觉误差)配不上那份复杂度。
			if in.RatesReady {
				row.rateUp += in.RateUp[pk]
				row.rateDown += in.RateDown[pk]
			}
		}
		if rec.Rule != "" && !seenRule[k][rec.Rule] && len(row.Rules) < maxRulesPerRow {
			seenRule[k][rec.Rule] = true
			row.Rules = append(row.Rules, rec.Rule)
		}
		// **目的地按行去重,不是按端口只算一次**——同一行内不同目的地各占一条,
		// 与字节/速率那个按 PortKey 去重的 counted 是两件不同的事,别混用。
		// 空 Dest 不进列表、也不计入 DestsMore:它不是「一个没列出来的目的地」,
		// 是「这条连接没有目的地可报」。
		if rec.Dest != "" && !seenDest[k][rec.Dest] {
			seenDest[k][rec.Dest] = true
			// **满了之后继续数,别 break。** 只是不再 append —— DestsMore 要数
			// 出「还有多少去重后的目的地没被列出来」,break 会让它恒为 0 而没有
			// 任何测试或界面看得出来。
			if len(row.Dests) < maxDestsPerRow {
				row.Dests = append(row.Dests, rec.Dest)
			} else {
				row.DestsMore++
			}
		}
	}

	report := Report{Groups: make([]Group, 0, len(orderedPaths))}
	for _, p := range orderedPaths {
		var rows []AppRow
		for k, row := range acc {
			if k.path != p {
				continue
			}
			out := row.AppRow
			if in.RatesReady {
				up, down := row.rateUp, row.rateDown
				out.BytesUpRate = &up
				out.BytesDownRate = &down
			}
			rows = append(rows, out)
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

// DiffPortRates 把两次按端口的字节快照做差,折成每秒速率。
//
// **按端口做差,而不是按聚合后的行做差,是这个 task 存在的全部理由。** 报告
// 语义是 60 秒滚动窗口,一行的累计字节会因为记录滑出窗口而下降;客户端此前
// 拿相邻两次报告的行做差,把"变小"误读成"计数器复位",于是那一格不停闪成
// 破折号,而什么都没出错。端口从这次样本里消失 ⇒ 它这一拍**不贡献**,不是
// 「复位」—— 按端口做差没有这个歧义。
//
// 四条规则:
//  1. key 在 cur、不在 prev ⇒ delta = cur[k](新端口,字节全是这一拍攒的)。
//  2. 两边都有且 cur >= prev ⇒ delta = cur - prev。
//  3. 两边都有但 cur < prev ⇒ 计数器复位(TCP 端口复用时上游把该键的账整个
//     删掉,新连接从 0 重新攒),delta = cur。
//  4. key 在 prev、不在 cur ⇒ **不贡献,跳过**。不是复位,不是负数,不是 0
//     值写入 —— 这一条是这个函数存在的理由。
//
// elapsed <= 0 返回 nil:没有区间就没有速率,不许拿一个近似的分母硬算。
func DiffPortRates(prev, cur map[PortKey]int64, elapsed time.Duration) map[PortKey]float64 {
	if elapsed <= 0 {
		return nil
	}
	seconds := elapsed.Seconds()
	out := make(map[PortKey]float64, len(cur))
	for k, c := range cur {
		p, ok := prev[k]
		delta := c
		if ok && c >= p {
			delta = c - p
		}
		out[k] = float64(delta) / seconds
	}
	return out
}
