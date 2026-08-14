// Package observe 向系统现问 bx 的保护是否真的生效。
//
// 本包只读:它从不改动系统任何状态。它存在的理由是——bx 的生命周期层长期用
// 内存记忆表达"保护装没装",而记忆与内核里的事实会分叉(真实事故:Guardian 被
// bootout 后内存记录蒸发,内核里的 /2 reject 路由却留着,整机断网且无人能删)。
package observe

import (
	"time"

	"github.com/getbx/bx/internal/tristate"
)

// Tristate 区分「观测到是」「观测到否」「观测不到」;零值必须是 Unknown。
//
// **类型本身住在 internal/tristate 那个叶子包里**,这里只是别名再导出,于是
// `observe.Tristate` / `observe.Unknown` 这些既有写法一个字都不用改。
//
// 沉下去的理由:本包为了做观测要 import install / supervisor,任何**只想要这个
// 枚举**的包都会被拖进整条依赖链 —— 实测 internal/leakcheck 因此传递依赖了 23 个
// 内部包(含 install、supervisor、provision、tun、dialer),全部代价只为一个三值
// 枚举;而它那条「本包不碰控制面」的守卫只查直接 import,于是守卫是绿的、事实
// 不是。与 internal/barriercidr 当初被拆出去是同一个形状、同一个修法。
type Tristate = tristate.Tristate

const (
	Unknown = tristate.Unknown
	True    = tristate.True
	False   = tristate.False
)

// FromBool 把一个确定的布尔判定转成三态。仅在观测成功时使用。
func FromBool(value bool) Tristate { return tristate.FromBool(value) }

// ObserveError 记录单项观测为何失败。观测失败不让调用方失败,只让该项为 Unknown。
type ObserveError struct {
	Item string `json:"item"`
	Err  string `json:"err"`
}

// ObservedState 是某一时刻向系统现问得到的事实,不含任何记忆。
type ObservedState struct {
	ObservedAt       time.Time `json:"observed_at"`
	CaptureInterface string    `json:"capture_interface,omitempty"`
	CaptureOK        Tristate  `json:"capture_ok"`
	BarrierPresent   Tristate  `json:"barrier_present"`
	DNSServers       []string  `json:"dns_servers,omitempty"`
	DNSManaged       Tristate  `json:"dns_managed"`
	CoreSocket       Tristate  `json:"core_socket"`
	TunnelHealthy    Tristate  `json:"tunnel_healthy"`
	// DirectEgressOK 是「bx 自己的直连出得去吗」。**它与隧道健康正交**:
	// 真机上出现过隧道满速而直连全死(IP_BOUND_IF 找不到 scoped 路由),
	// 那时其余四项全绿,只有这一项能说出问题。
	DirectEgressOK Tristate       `json:"direct_egress"`
	Errors         []ObserveError `json:"errors,omitempty"`
	// NotApplicable 是**这个平台上根本不成立的问题**,由采集方声明。
	//
	// 它与 Unknown 是两回事,而此前只有后者可表达:Linux 上 bx 不改系统 DNS
	// (它在 TUN 里拦 UDP:53),于是 `dns_managed` 那个问题在那里不成立 ——
	// 报 False 像「明明受保护却说没接管」一样撒谎,报 Unknown 则会让每一台健康的
	// Linux 机器每次观测都吐一条永久 divergence。而满屏「无法观测」会把 divergence
	// 训练成用户和 agent 学会忽略的东西,正好毁掉它唯一的价值。
	//
	// **不适用的项不进 UnobservableItems,也不产生 divergence;真正没问出来的照旧报。**
	NotApplicable []string `json:"not_applicable,omitempty"`
}

// notApplicable 判断某一项在这个平台上是否根本不成立。
func (s ObservedState) notApplicable(item string) bool {
	for _, na := range s.NotApplicable {
		if na == item {
			return true
		}
	}
	return false
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
		{"direct_egress", s.DirectEgressOK},
	} {
		if check.value == Unknown && !s.notApplicable(check.item) {
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
