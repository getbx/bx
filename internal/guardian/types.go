package guardian

import (
	"context"
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
