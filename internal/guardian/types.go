package guardian

import (
	"context"
	"encoding/json"
	"time"
)

type DesiredState string

const (
	DesiredOn  DesiredState = "on"
	DesiredOff DesiredState = "off"
)

type Phase string

const (
	PhaseIdle           Phase = "idle"
	PhasePrepared       Phase = "prepared"
	PhaseBarrierActive  Phase = "barrier_active"
	PhaseActivating     Phase = "activating"
	PhaseRollingBack    Phase = "rolling_back"
	PhaseCommitted      Phase = "committed"
	PhaseRolledBack     Phase = "rolled_back"
	PhaseNeedsAttention Phase = "needs_attention"
)

type Paths struct {
	Desired, Transaction, Receipt, Staging, Snapshots string
	// UpgradeIntent 是**已退休**的升级欠条(/var/lib/bx/upgrade-intent.json)。
	//
	// 没有任何代码再写它。留着这个路径只为一件事:
	// MigrateLegacyUpgradeIntent —— 一台正处在升级中途、跨过这次切换的机器,
	// 盘上那张旧欠条要被翻成「desired=on + 一次已武装的挂起」再删掉。
	// 只兼容一个版本;删掉这条读取路径的时机另记。
	UpgradeIntent string
	// MaintenanceHold 记录「此刻不该有保护,但用户想要」。
	//
	// **它绝不进 Desired 那个文件。** guardian-state.json 的内容字面就是 `"on"`
	// 或 `"off"`(裸 JSON 字符串,没有信封、没有 schema_version、没有迁移机制),
	// 往里加字段的后果不是「旧版本忽略未知字段」,而是旧 Guardian 的 LoadDesired
	// 报错 → recoverLocked 置 recoveryBlocked=true → Manager.Down 第一句就返回
	// errRecoveryIncomplete,**永久**。而升级恰恰是新旧两版共存的那一刻。
	MaintenanceHold string
}

type DNSState string

const (
	DNSUnknown   DNSState = "unknown"
	DNSManaged   DNSState = "managed"
	DNSUnmanaged DNSState = "unmanaged"
	// DNSNotNeeded:本平台没有「DNS 接管」这件事(linux:数据面整机劫持 +
	// engine 拦 UDP:53,resolv.conf 一个字不碰)。它与 Unmanaged(该接管而
	// 没接管,darwin 上是故障)语义相反,折进任何既有态都是撒谎:折进
	// Managed 是伪造绿灯,折进 Unmanaged 让健康的 linux 机器 Up 恒失败,
	// 折进 Unknown 把「查了,无此事」说成「没查」。
	DNSNotNeeded DNSState = "not_needed"
)

type DNSStatus struct {
	State   DNSState
	Service string
	// Servers 是系统此刻实际配置的解析器地址。install.DNSStatus 一直采着它,
	// 只是从没发布出去 —— 于是菜单只能说「谁在管」,说不出「现在是谁」,
	// 而后者恰恰在**没被接管**时才是用户要看的那个值。
	Servers []string
}

type DNSManager interface {
	EnsureManaged(context.Context) (DNSStatus, error)
	Inspect(context.Context) (DNSStatus, error)
	Restore(context.Context) (DNSStatus, error)
}

type Transaction struct {
	ID                   string    `json:"transaction_id"`
	FromVersion          string    `json:"from_version"`
	ToVersion            string    `json:"to_version"`
	Phase                Phase     `json:"phase"`
	BarrierInstallIntent bool      `json:"barrier_install_intent,omitempty"`
	AssetDigest          string    `json:"asset_digest"`
	SnapshotPath         string    `json:"snapshot_path"`
	StartedAt            time.Time `json:"started_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	LastError            string    `json:"last_error,omitempty"`
}

type Receipt struct {
	TransactionID string    `json:"transaction_id"`
	FromVersion   string    `json:"from_version"`
	ToVersion     string    `json:"to_version"`
	AssetDigest   string    `json:"asset_digest"`
	Outcome       Phase     `json:"outcome"`
	CompletedAt   time.Time `json:"completed_at"`
}

// CoreRuntime 是 Core 的运行时统计,由 Guardian 代取并随 Status 一起发布。
//
// Guardian 是菜单唯一的数据源(见控制面架构设计),所以这些字段必须从这里拿得到,
// 而不是让 UI 自己去 spawn 一个 CLI 把两个源合起来。
type CoreRuntime struct {
	// Reachable 区分「问到了」与「问不出来」。Core 拨不通时其余字段全为零值,
	// 不得用 TunnelHealthy=false 冒充 —— 那是把「没问到」压成「答案是坏的」。
	Reachable     bool `json:"reachable"`
	TunnelHealthy bool `json:"tunnel_healthy"`
	// RoutesInstalled / DNSListening / UDPRequired / UDPReady 是 Core 那边路径
	// 恢复 verify 阶段要看的那几项(run.go 的 verify 闭包),原样搬过来。
	// 它们来自 RuntimeState,问不出来时保持 false —— 「没问到」不许读成
	// 「满足」。用途见 recoverySupersededByCore:一份失败的恢复快照,在 Core
	// 此刻已满足 verify 的每一项时是历史,不是现状。
	RoutesInstalled bool   `json:"routes_installed"`
	DNSListening    bool   `json:"dns_listening"`
	UDPRequired     bool   `json:"udp_required"`
	UDPReady        bool   `json:"udp_ready"`
	LatencyMS       int64  `json:"latency_ms"`
	Server          string `json:"server,omitempty"`
	Transport       string `json:"transport,omitempty"`
	UDPMode         string `json:"udp_mode,omitempty"`
	// UDPTransport 是「UDP 走另一条隧道」那个配置的当前值。泄漏检测靠它把
	// **bx 自己的按类分流**与**真的漏了**分开 —— 两者在 srflx 那个地址上
	// 长得一模一样。
	UDPTransport string `json:"udp_transport,omitempty"`
	// DNSUpstream 已渲染成可直接显示的一行(见 ResolverLabel):直连域名的查询
	// 交给谁。空串表示没问出来 —— **不是「没有上游」**。
	DNSUpstream string `json:"dns_upstream,omitempty"`
	// FailingRules 是**正在成片失败**的用户规则。空 = 没有值得报的
	// (判据在 stats.FailingRules:同时看绝对数与比例)。
	//
	// 它是规则编辑界面存在的理由:列出规则本身没什么价值(用户自己写的),
	// 有价值的是「这一条把 8113 条连接逼上了一条不通的路」—— 那点名了该删哪一行。
	FailingRules []FailingRule `json:"failing_rules,omitempty"`
}

// FailingRule 是一条正在成片失败的用户规则。
type FailingRule struct {
	// Kind 是 direct / proxy —— 与 /v1/rules 的 kind 同一套取值,
	// 好让界面把它和列表里那一行对上。
	Kind     string `json:"kind"`
	Rule     string `json:"rule"`
	Attempts int64  `json:"attempts"`
	Failures int64  `json:"failures"`
}

// UpdateAvailability 是「有没有可装的新版」这一个问题的答案,由 Guardian 代查后
// 随 GET /v1/update-check 发布。
//
// 字段与 `bx update --check --json` 的 updateCheckReport 逐字同形,因为它就是同一
// 件事的同一个答案 —— 菜单此前是 spawn 那条命令来问的。**Verified 与 Available 分开**:
// Verified=false 意味着这份答案没经过 manifest 签名校验,消费方(菜单)据此拒绝
// 显示「有新版」入口,绝不把一个未经校验的版本号推给用户去装。
type UpdateAvailability struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Verified  bool   `json:"verified"`
}

// CapabilityDiagnosticsArchive 表示这一版运行时的 `bx logs` 支持 --archive/--dir。
//
// 菜单此前是 spawn `bx logs --help` 再**在帮助文本里找这两个 flag** 来判断的 ——
// 一个 UI 靠解析另一个程序的帮助文本来做特性探测,是整份控制面架构诊断里最直白
// 的症状。能力应当由 daemon 自己声明:Guardian 与 CLI 出自同一次构建、装在同一个
// runtime 目录下,它对「这一版支持什么」有第一手知识,而被探测的那个二进制恰恰
// 可能是旧版(此前那次探测正是拿 /usr/local/bin/bx 去问的)。
const CapabilityDiagnosticsArchive = "diagnostics_archive"

// CapabilityReconcileReport 表示这一版 Guardian 跑着阶段③a 那条只观察的调谐环,
// 因而 Status.Reconcile 这个字段是**它会填的**。
//
// 消费方靠它把两件事分开:
//   - 「新版 Guardian,循环在跑,只是还没跑完第一轮」⇒ 声明了能力、报告还缺席;
//   - 「旧版 Guardian(或没有循环的平台)」⇒ 连能力都没声明。
//
// 二者若不分开,CLI 只能在「对每一台机器都常驻一行『尚未观测』」与「对刚起来的
// 新版一个字都不说」之间二选一 —— 前者正是 observerForPlatform 那道门在防的噪声,
// 后者则让「循环死了」重新变得看不见。
//
// 值用下划线,与同一个数组里的 CapabilityDiagnosticsArchive 一致:消费方是
// **逐字**比对的(apps/macos/BxMenu/Sources/BxMenu/StatusReport.swift),一个数组里
// 两种写法迟早会有人照着旁边那条抄错。今天还没有消费方依赖它,阶段③b 之后它就是契约。
const CapabilityReconcileReport = "reconcile_report"

// CapabilityMaintenanceHold 表示这一版 Guardian 认识维护挂起,因而
// Status.MaintenanceHold 这个键**是它会填的**。
//
// 与 CapabilityReconcileReport 同一机制、同一理由:消费方要分得开「这一版没有
// 挂起这个概念」(没声明能力)与「有这个概念,此刻没有挂起」(声明了、键缺席)。
// 前者下菜单不该说「保护已关闭」,因为它根本不知道是不是维护窗口。
const CapabilityMaintenanceHold = "maintenance_hold"

// CapabilityRules 表示这一版 Guardian 提供 /v1/rules,菜单据此决定要不要显示
// 规则编辑入口。**键缺席 = 旧版 Guardian**,与上面两个同一机制:
// 少了它而菜单照样把编辑界面画出来,用户会对着一个每次点都失败的按钮。
const CapabilityRules = "rules"

// CapabilityServers 表示这一版 Guardian 提供 /v1/servers(列清单 + 换一台)。
// **键缺席 = 旧版 Guardian**:菜单据此决定要不要画出服务器入口,否则用户对着
// 一个每次点都失败的按钮 —— 与 CapabilityRules 同一机制同一理由。
const CapabilityServers = "servers"

// CapabilityServersEdit 表示这一版 Guardian 的 POST /v1/servers 认得 remove /
// replace 这些**改清单**的动词(add 与切换由 CapabilityServers 覆盖)。
//
// **它必须与 CapabilityServers 分开,因为后者早于这些动词。** 一台只声明
// `servers` 的旧 Guardian 收到 `{"action":"remove","name":"osaka"}` 时,走的是
// 那一版唯一的兼容行为:空/未知 action = **换到 Name 那一台** —— 于是用户点
// 一下 Delete,出口 IP 与国家就换到了他想删掉的那一台。2026-08-09 的
// multi-server 设计里明写「只有用户可以换服务器」,这正是它唯一禁止的事。
// 而「文件换了、进程没换」在本仓库是记录在案的真实升级窗口(2026-08-16 那次
// 验收就是靠能力声明才认出跑着的还是旧 Guardian,版本号认不出来)。
//
// **绝不「试着拨一下看看」**(与 status_watch、logs、doctor 同一条门规):
// 旧 Guardian 对这些动词回的是 200 + 一次已经发生的切换,客户端事后无从分辨,
// 而代价已经付掉了。
const CapabilityServersEdit = "servers_edit"

// CapabilityApps 表示这一版 Guardian 提供 /v1/apps(应用流量归因报告)。
// **键缺席 = 旧版 Guardian**,与 CapabilityRules/CapabilityServers 同一机制:
// 菜单据此决定要不要画出这个功能入口,否则用户对着一个每次点都失败的按钮。
const CapabilityApps = "apps"

// CapabilityStatusWatch 表示这一版 Guardian 的 GET /v1/status 认 `wait=<generation>`
// 长轮询,于是客户端可以在状态真的变了的那一刻收到,而不是靠一个轮询常量。
//
// 菜单**靠这个键决定走 watch 还是降级轮询,绝不去试拨** ——
// 旧 Guardian 会忽略未知 query 参数、回一份普通应答,而那与「立刻返回因为状态
// 变了」在客户端看来一模一样,于是 watch 循环会退化成一个满速轮询。
// (与 /v1/rules、/v1/servers 同一条门控纪律。)
const CapabilityStatusWatch = "status_watch"

// MaintenanceHoldStatus 是**正在生效**的那次挂起,随 Status 发布。
//
// 过期的挂起不出现在这里:键缺席的意思是「此刻没有挂起」。它与 MaintenanceHold
// (盘上那份)刻意分成两个类型 —— 发布出去的这份不带 SchemaVersion,那是存储
// 格式的事,消费方不该看见,更不该照着它去解盘上的文件。
type MaintenanceHoldStatus struct {
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
}

// GuardianCapabilities 是这一版 Guardian 声明支持的能力集合。
//
// 每次调用都返回新切片:它会被塞进 Status 交给 JSON 编码,共享一份底层数组等于
// 把一个包级可变状态发布出去。
func GuardianCapabilities() []string {
	return []string{CapabilityDiagnosticsArchive, CapabilityReconcileReport, CapabilityMaintenanceHold, CapabilityRules, CapabilityServers, CapabilityServersEdit, CapabilityStatusWatch, CapabilityApps, CapabilityLogs, CapabilityDoctor}
}

// ReconcileReport 是只观察调谐环**最近一轮**的判断,随 Status 一起发布。
//
// 存在的理由:今天要回答「循环提议过什么」只能 root 去 tail
// /var/log/bx-guard.log,而真机 soak 是阶段③a 唯一真正的验收 —— 它得能从
// `bx status` 读到。
//
// **At 是这份报告里最重要的字段,而且它的重要性不来自「时间好看」。**
// reconcileDecision 的零值(无动作、无栅栏)恰恰就是一台**健康机器**的判断,
// 循环也正是靠这一点在健康机器上保持静默。于是「循环从没跑过一轮」(没挂上、
// 刚启动、每轮都 panic 被 recover 掉)与「循环跑了、什么差异都没有」在判断值上
// **完全相同**,而它们在真机上的意义正好相反。区分二者的唯一办法是「有没有
// 一份带时刻的报告」:一轮都没跑过时 Status.Reconcile 整个为 nil、字段缺席,
// 而不是发布一份读起来像「一切正常」的零值。
type ReconcileReport struct {
	// At 是记下这一轮的时刻。**永远非零**:recordReconcileRound 是唯一的写入口,
	// 它一定盖时间戳。见类型头。
	At time.Time `json:"at"`
	// Actions 是这一轮的提议。③b 起其中被授权的至多一项会被执行,结果在
	// Executed 里 —— 两个字段并列,「提议了什么」与「做了什么」不合并。
	Actions []string `json:"actions,omitempty"`
	// Held 非空时 Actions 必为空,内容是被哪道栅栏挡住的。
	//
	// **读 soak 统计的人必须知道:这里报的不一定是「排序最高」的那道栅栏。**
	// reconcileOnce 先判便宜的栅栏(维护挂起、意图读不出),命中就短路返回,
	// 那一轮根本没去读 recoveryBlocked 与 ownership_uncertain —— 于是
	// 「挂起 + 所有权不确定同时成立」只会报 maintenance_hold。
	// 后果是 Held 的直方图会**系统性少数** ownership_uncertain,而维护窗口
	// 恰恰是它最容易同时升起的时候。要数「有多少轮根本没在工作」用 Held 非空,
	// 别拿单个栅栏的计数当那道栅栏的真实发生频次。
	Held string `json:"held,omitempty"`
	// UnchangedRounds 是判断连续多少轮没变。它同时是退避的输入,所以一个持续
	// 增长的数字意味着「循环活着且这段时间一直是同一个判断」。
	UnchangedRounds int `json:"unchanged_rounds"`
	// Unobservable 是这一轮**没能问出来**的观测项(observe.ObservedState.UnobservableItems)。
	//
	// **没有它,一台全盲的机器与一台健康的机器发布的是同一份报告。** 判据对
	// Unknown 一律「什么都不做」,所以三项探测全失败时 Actions 为空、Held 为空、
	// UnchangedRounds 一路涨 —— 与「跑了、什么差异都没有」逐字节相同,而后者
	// 正是 soak 想要的结论。这个字段是二者唯一的区别。
	Unobservable []string `json:"unobservable,omitempty"`
	// CoreScan 是这一轮**只读**进程扫描的测量结果。见 ReconcileCoreScan ——
	// 它是测量,不参与判断。
	CoreScan ReconcileCoreScan `json:"core_scan"`
	// Executed 是上一轮实际执行的动作(阶段③b 起,仅 desired=off 的清理
	// 动作有执行权,白名单见 reconcile_execute.go)。nil = 这一轮没有执行
	// 任何东西 —— 健康机器的常态。
	Executed *ReconcileExecution `json:"executed,omitempty"`
}

// ReconcileExecution 是一次调谐执行的结果。Outcome 三值:ok / failed /
// skipped(skipped 的 Error 说明为什么 —— mutation_busy 或
// preconditions_changed,两者都不是故障,是让路)。
type ReconcileExecution struct {
	Action  string `json:"action"`
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
}

// ReconcileCoreScan 是一轮里对 looksLikeCore 的**只读**测量。
//
// 存在的理由是设计里的第二样交付:looksLikeCore(basename(argv[0])=="bx" &&
// argv[1]=="run" && uid==0)的误报率**至今从未测量**,而阶段③b 要在它之上再叠
// 一层准入。它今天只在 Existing/Start/confirmCoreStopped 这三条**改动**路径上被
// 调用,所以只观察的循环跑上几天也攒不出任何证据。
//
// **它绝不参与判断**:decide 的输入一个字段都没加,这里只是把答案记下来。
type ReconcileCoreScan struct {
	// Measured 为假时 Cores 无意义。「枚举不出来」不是「一个都没有」——
	// 与 decideCoreScan 那条 fail-closed 下限同一条原则,只是这里不做拒绝、
	// 只做记录:把扫描失败记成 0,正好会让误报率算出来偏低,即这份测量的反面。
	Measured bool `json:"measured"`
	// Cores 是本轮扫到的、看起来像 Core 的进程数。健康机器上应当恒为 1。
	Cores int `json:"cores"`
	// Reason 只在 Measured 为假时非空,说明为什么没测成。
	Reason string `json:"reason,omitempty"`
}

// 没能测量时的原因。原样进日志与 JSON,所以是稳定标识符而不是给人看的句子。
const (
	coreScanUnsupported = "scan_unsupported"
	coreScanFailed      = "scan_failed"
	coreScanPanicked    = "scan_panicked"
)

// clone 返回一份深拷贝。Status 会被交给 JSON 编码器旁边的任意代码,交出内部
// 那一份的别名等于把 Manager 的状态开放给调用方去改(与 GuardianCapabilities
// 不共享底层数组同一条纪律)。
func (r *ReconcileReport) clone() *ReconcileReport {
	if r == nil {
		return nil
	}
	copied := *r
	copied.Actions = append([]string(nil), r.Actions...)
	copied.Unobservable = append([]string(nil), r.Unobservable...)
	return &copied
}

type Status struct {
	SchemaVersion int `json:"schema_version"`

	// StatusGeneration 是**内容派生**的单调计数器:Guardian 每次发现自己发布的
	// Status(减去易变字段的投影,见 statusDigest)与上一次不同,它就 +1。
	//
	// 客户端把手上这个值经 `GET /v1/status?wait=<gen>` 发回来,Guardian 在
	// `current != wait` 时立刻应答、相同则挂住。**比较用 `!=` 而不是 `>`**:
	// Guardian 重启后这个计数器从头开始,`>` 会让客户端手上那个较大的值
	// 永久挂住。
	//
	// **刻意没有 omitempty。** 键缺席 = 这一版 Guardian 没有 watch 这个概念
	// (升级窗口里的旧 Guardian);键在而值为 0 = 有这个概念、还没发布过。
	// 两者对客户端意味着不同的行为(降级轮询 vs 正常 watch),而 omitempty 会
	// 把它们压成同一个形状(与 Capabilities 同一条纪律)。
	StatusGeneration uint64 `json:"status_generation"`

	Desired           DesiredState     `json:"desired"`
	Phase             Phase            `json:"phase"`
	CorePID           int              `json:"core_pid,omitempty"`
	CoreVersion       string           `json:"core_version,omitempty"`
	Protection        string           `json:"protection_state"`
	NetworkGeneration string           `json:"network_generation"`
	Recovery          RecoverySnapshot `json:"recovery"`
	LastError         string           `json:"last_error,omitempty"`
	GuardianVersion   string           `json:"guardian_version,omitempty"`
	RuntimeVersion    string           `json:"runtime_version,omitempty"`
	DNSState          DNSState         `json:"dns_state"`
	DNSManaged        bool             `json:"dns_managed"`
	DNSService        string           `json:"dns_service,omitempty"`
	DNSServers        []string         `json:"dns_servers,omitempty"`
	// Core 只在 LocalAPIOptions 注入了取数函数时才填(既有调用方不受影响、
	// 也不凭空造字段)。取不到时仍会填,但 Reachable=false、其余字段零值。
	Core *CoreRuntime `json:"core,omitempty"`

	// Capabilities 是这一版 Guardian 声明支持的能力(见 GuardianCapabilities)。
	//
	// **刻意没有 omitempty。** 消费方要分得开两件事:「声明过、这个能力不在里面」
	// (键在、数组里没有)与「这一版压根没声明过能力」(键缺席 —— 例如升级窗口里
	// 还没换掉的旧 Guardian)。omitempty 会把空集合与从未声明压成同一个形状,而
	// 菜单正是靠这个区分决定要不要提示用户升级。
	Capabilities []string `json:"capabilities"`

	// Reconcile 是只观察调谐环最近一轮的判断(见 ReconcileReport)。
	//
	// **omitempty 在这里是契约的一部分,不是省字节。** 一轮都没跑过时这个键必须
	// 整个缺席:一份 `{"at":"0001-01-01T00:00:00Z"}` 的零值报告读起来与「跑过、
	// 什么差异都没有」一模一样,而后者是 soak 想要的结果、前者说明那份干净日志
	// 毫无意义。「这一版有没有这条循环」由 Capabilities 里的
	// CapabilityReconcileReport 回答,不靠这个键的有无。
	Reconcile *ReconcileReport `json:"reconcile,omitempty"`

	// MaintenanceHold 非 nil 表示此刻有一次维护挂起在生效:用户要保护(desired
	// 仍是 on),但此刻不能有。**omitempty 是契约的一部分**:键缺席 = 没有挂起。
	// 「这一版认不认识挂起」由 Capabilities 里的 CapabilityMaintenanceHold 回答。
	//
	// 一台挂起武装着的机器,在没有这个字段之前与「用户自己关掉了保护」长得一模
	// 一样 —— 这正是 desired 不再撒谎之后剩下的最后一处含混。
	MaintenanceHold *MaintenanceHoldStatus `json:"maintenance_hold,omitempty"`

	// LastErrorGeneration is a monotonic counter bumped every time
	// needsAttention actually runs (see Manager.needsAttention). It exists so
	// LocalAPI failure handlers can tell "this call really did set LastError"
	// apart from "LastError happens to already hold this value from an
	// earlier, unrelated failure" — a value comparison on LastError itself
	// cannot distinguish a repeated failure with the *same* code (the
	// exact scenario this whole feature exists for) from a stale one.
	// Deliberately excluded from the public JSON contract: it is an internal
	// freshness signal, not an observable status field.
	LastErrorGeneration uint64 `json:"-"`
}

// MarshalJSON keeps externally observable DNS state within the Guardian
// contract while existing lifecycle paths have not supplied a DNS result yet.
func (s Status) MarshalJSON() ([]byte, error) {
	s.DNSState = normalizedDNSState(s.DNSState)
	type statusJSON Status
	return json.Marshal(statusJSON(s))
}

func normalizedDNSState(state DNSState) DNSState {
	switch state {
	case DNSManaged, DNSUnmanaged, DNSUnknown, DNSNotNeeded:
		return state
	default:
		return DNSUnknown
	}
}

type UpdateResult struct {
	FromVersion     string `json:"from_version"`
	ToVersion       string `json:"to_version"`
	Phase           Phase  `json:"phase"`
	CoreActivated   bool   `json:"core_activated"`
	RolledBack      bool   `json:"rolled_back"`
	ProtectionState string `json:"protection_state"`
}

type RecoveryRequest struct {
	Reason     string `json:"reason"`
	Generation string `json:"generation,omitempty"`
}

type RecoverySnapshot struct {
	ID         string    `json:"recovery_id"`
	State      string    `json:"state"`
	Stage      string    `json:"stage"`
	Reason     string    `json:"reason"`
	Generation string    `json:"generation,omitempty"`
	ErrorCode  string    `json:"last_error_code,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	Attempt    int       `json:"attempt"`
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (s DesiredState) valid() bool {
	return s == DesiredOn || s == DesiredOff
}

func (p Phase) valid() bool {
	switch p {
	case PhaseIdle, PhasePrepared, PhaseBarrierActive, PhaseActivating, PhaseRollingBack, PhaseCommitted, PhaseRolledBack, PhaseNeedsAttention:
		return true
	default:
		return false
	}
}

func (p Phase) terminal() bool {
	return p == PhaseCommitted || p == PhaseRolledBack || p == PhaseNeedsAttention
}
