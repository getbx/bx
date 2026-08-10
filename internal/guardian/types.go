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
	// UpgradeIntent 记录「上一次升级欠用户一次『把保护起回来』」。
	// 它与 Desired 是兄弟状态、同住 /var/lib/bx,所以由本 Store 拥有:写它的是
	// app-install,而销它的必须是**每一条**「用户说 off」的路径,那条路径的汇合
	// 点是 Manager.Down(菜单走 socket → /v1/down → controller.Down;CLI 干净
	// 路径走 client.Down 到同一个 handler),在 internal/guardian 里。
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
)

type DNSStatus struct {
	State   DNSState
	Service string
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
	Reachable     bool   `json:"reachable"`
	TunnelHealthy bool   `json:"tunnel_healthy"`
	LatencyMS     int64  `json:"latency_ms"`
	Server        string `json:"server,omitempty"`
	Transport     string `json:"transport,omitempty"`
	UDPMode       string `json:"udp_mode,omitempty"`
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

// GuardianCapabilities 是这一版 Guardian 声明支持的能力集合。
//
// 每次调用都返回新切片:它会被塞进 Status 交给 JSON 编码,共享一份底层数组等于
// 把一个包级可变状态发布出去。
func GuardianCapabilities() []string {
	return []string{CapabilityDiagnosticsArchive, CapabilityReconcileReport}
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
	// Actions 是这一轮**本来会做**的事(阶段③a 一项都不会执行)。
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
	SchemaVersion     int              `json:"schema_version"`
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
	case DNSManaged, DNSUnmanaged, DNSUnknown:
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
