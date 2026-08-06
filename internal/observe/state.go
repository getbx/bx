// Package observe 向系统现问 bx 的保护是否真的生效。
//
// 本包只读:它从不改动系统任何状态。它存在的理由是——bx 的生命周期层长期用
// 内存记忆表达"保护装没装",而记忆与内核里的事实会分叉(真实事故:Guardian 被
// bootout 后内存记录蒸发,内核里的 /2 reject 路由却留着,整机断网且无人能删)。
package observe

import "time"

// Tristate 区分「观测到是」「观测到否」「观测不到」。
//
// 零值必须是 Unknown:用 bool 会把"问不出来"压成 false,让未观测的项冒充正常。
type Tristate uint8

const (
	Unknown Tristate = iota
	True
	False
)

func (t Tristate) String() string {
	switch t {
	case True:
		return "true"
	case False:
		return "false"
	default:
		return "unknown"
	}
}

// FromBool 把一个确定的布尔判定转成三态。仅在观测成功时使用。
func FromBool(value bool) Tristate {
	if value {
		return True
	}
	return False
}

// ObserveError 记录单项观测为何失败。观测失败不让调用方失败,只让该项为 Unknown。
type ObserveError struct {
	Item string `json:"item"`
	Err  string `json:"err"`
}

// ObservedState 是某一时刻向系统现问得到的事实,不含任何记忆。
type ObservedState struct {
	ObservedAt       time.Time      `json:"observed_at"`
	CaptureInterface string         `json:"capture_interface,omitempty"`
	CaptureOK        Tristate       `json:"capture_ok"`
	BarrierPresent   Tristate       `json:"barrier_present"`
	DNSServers       []string       `json:"dns_servers,omitempty"`
	DNSManaged       Tristate       `json:"dns_managed"`
	CoreSocket       Tristate       `json:"core_socket"`
	TunnelHealthy    Tristate       `json:"tunnel_healthy"`
	Errors           []ObserveError `json:"errors,omitempty"`
}

// Intent 是用户/agent 声明的意图。观测层只读它,永不改写。
type Intent struct {
	Desired string `json:"desired"` // "on" | "off"
}

// Believed 是生命周期层的内存信念,原样透出以便与观测对照。
type Believed struct {
	Protection string `json:"protection"`
	Phase      string `json:"phase,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}
