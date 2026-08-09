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

// GuardianCapabilities 是这一版 Guardian 声明支持的能力集合。
//
// 每次调用都返回新切片:它会被塞进 Status 交给 JSON 编码,共享一份底层数组等于
// 把一个包级可变状态发布出去。
func GuardianCapabilities() []string {
	return []string{CapabilityDiagnosticsArchive}
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
