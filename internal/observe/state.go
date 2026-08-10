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

// UnobservableItems 返回这一轮**没能问出来**的项的名字,固定次序。
//
// 判据就是 Tristate 为 Unknown —— 本包对「观测不到」只有这一个说法,不另立
// 第二套。刻意**不**改从 Errors 推:Errors 记的是「问了、失败了」,而依赖缺席
// (Deps 某个函数为 nil)同样问不出来却一条错误都不会留下,两者对调用方是同一
// 件事。名字与 ObserveError.Item、Divergence.Field 用的是同一套词汇,好让日志、
// 分歧与这份清单指的是同一个东西。
//
// 存在的理由:一轮**全盲**的观测(三项皆 Unknown)与一台健康机器在下游读起来
// 一模一样 —— 判据对 Unknown 一律「什么都不做」,于是两者产出的动作集合都是空。
// 调谐环的验收恰恰是「健康机器上提议过几次动作」,而 0 正是一个什么都没看见的
// 循环给出的答案。没有这份清单,那个 0 无从分辨。
func (s ObservedState) UnobservableItems() []string {
	var items []string
	for _, check := range []struct {
		item  string
		value Tristate
	}{
		{"capture_ok", s.CaptureOK},
		{"barrier_present", s.BarrierPresent},
		{"dns_managed", s.DNSManaged},
		{"core_socket", s.CoreSocket},
		{"tunnel_healthy", s.TunnelHealthy},
	} {
		if check.value == Unknown {
			items = append(items, check.item)
		}
	}
	return items
}

// Intent 是用户/agent 声明的意图。观测层只读它,永不改写。
type Intent struct {
	Desired string `json:"desired"` // "on" | "off"
	// Hold 是正在生效的维护挂起(升级等)。非 nil 且未过期时,「保护此刻不在」
	// 是**预期之内**的,不是分歧。
	Hold *HoldIntent `json:"maintenance_hold,omitempty"`
}

// HoldIntent 是 guardian.MaintenanceHoldStatus 在本包里的镜像。
//
// 本包不 import internal/guardian:那会把一个只读观测层挂到控制面上,而观测层
// 存在的全部理由就是不依赖控制面自己的记账。翻译由调用方(internal/cli)做。
type HoldIntent struct {
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Believed 是生命周期层的内存信念,原样透出以便与观测对照。
type Believed struct {
	Protection string `json:"protection"`
	Phase      string `json:"phase,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}
