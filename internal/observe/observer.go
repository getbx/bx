package observe

import (
	"context"
	"strings"
	"time"
)

// RouteResult 是本包对一次路由查询所需信息的窄视图。
type RouteResult struct {
	Interface string
	Reject    bool
}

// DNSResult 是本包对系统 DNS 状态所需信息的窄视图。
type DNSResult struct {
	Servers []string
	Enabled bool // resolver 是否为 bx
}

// RuntimeResult 是本包对 Core 自报状态所需信息的窄视图。
type RuntimeResult struct {
	TunnelHealthy bool
}

// Deps 是观测所需的外部能力。用注入而非直接 import,使本包免 root 可测,
// 且不与 supervisor/install/guardian 产生依赖循环。
type Deps struct {
	// TunName 返回 bx 当前 TUN 的名字。它必须能报错:生产接线里这个名字取自
	// Core 的控制 socket,而"问不出来"与"确定没有 TUN"是两个不同的观测结论。
	TunName      func() (string, error)
	BarrierCIDRs func() (ipv4, ipv6 []string)
	LookupRoute  func(ctx context.Context, destination string, ipv6 bool) (RouteResult, error)
	InspectDNS   func(ctx context.Context) (DNSResult, error)
	FetchRuntime func() (RuntimeResult, error)
	Now          func() time.Time
	// NotApplicable 是**这个平台上根本不成立的观测项**,由接线方声明。
	//
	// 它不是「问不出来」(那是 Unknown 的语义):Linux 上 bx 不改系统 DNS,
	// 于是「系统 DNS 归不归 bx」在那里不是正确的问题。两者对用户的含义完全不同,
	// 而把前者伪装成后者会让每台健康机器每次观测都吐一条永久 divergence。
	NotApplicable []string
}

// captureProbes 是用于判定劫持是否生效的探测目的地。两个地址分别落在
// 0.0.0.0/1 与 128.0.0.0/1,覆盖 split-default 的两半。
var captureProbes = []string{"1.1.1.1", "129.1.1.1"}

// Observe 向系统现问,返回某一时刻的事实。
//
// 它绝不改动系统,绝不因某项失败而中断:任一项出错即记为 Unknown 并附原因,
// 继续观测其余项。这是刻意的——观测失败不该让保护中断,也不该让调用方失败。
func Observe(ctx context.Context, deps Deps) ObservedState {
	state := ObservedState{NotApplicable: append([]string(nil), deps.NotApplicable...)}
	if deps.Now != nil {
		state.ObservedAt = deps.Now()
	}

	// **不适用的项不去问。** 问了只会在每一轮的 Errors 里留下同一条永久记录
	// (「本平台不支持 DNS 接管观测」),而那与满屏「无法观测」是同一种噪声:
	// 把一个静态的平台事实伪装成每次调用都新发生的失败。
	if !state.notApplicable("capture_ok") {
		state.CaptureOK, state.CaptureInterface = observeCapture(ctx, deps, &state)
	}
	if !state.notApplicable("barrier_present") {
		state.BarrierPresent = observeBarrier(ctx, deps, &state)
	}
	if !state.notApplicable("dns_managed") {
		state.DNSManaged, state.DNSServers = observeDNS(ctx, deps, &state)
	}
	if !state.notApplicable("core_socket") && !state.notApplicable("tunnel_healthy") {
		state.CoreSocket, state.TunnelHealthy = observeCore(deps, &state)
	}

	return state
}

func observeCapture(ctx context.Context, deps Deps, state *ObservedState) (Tristate, string) {
	if deps.TunName == nil || deps.LookupRoute == nil {
		return Unknown, ""
	}
	tun, err := deps.TunName()
	if err != nil {
		state.Errors = append(state.Errors, ObserveError{Item: "capture_ok", Err: err.Error()})
		return Unknown, ""
	}
	if tun == "" {
		// 没有 TUN 就是确定没有劫持,不是"问不出来"。
		return False, ""
	}
	firstInterface := ""
	for _, destination := range captureProbes {
		selection, err := deps.LookupRoute(ctx, destination, false)
		if err != nil {
			state.Errors = append(state.Errors, ObserveError{Item: "capture_ok", Err: err.Error()})
			return Unknown, firstInterface
		}
		if firstInterface == "" {
			firstInterface = selection.Interface
		}
		if selection.Interface != tun {
			return False, selection.Interface
		}
	}
	return True, firstInterface
}

func observeBarrier(ctx context.Context, deps Deps, state *ObservedState) Tristate {
	if deps.BarrierCIDRs == nil || deps.LookupRoute == nil {
		return Unknown
	}
	ipv4, _ := deps.BarrierCIDRs()
	if len(ipv4) == 0 {
		return Unknown
	}
	// 用首个阻断网段的网络地址代表整组:它们同装同拆,查一条即可判定在位与否。
	selection, err := deps.LookupRoute(ctx, barrierProbeAddress(ipv4[0]), false)
	if err != nil {
		state.Errors = append(state.Errors, ObserveError{Item: "barrier_present", Err: err.Error()})
		return Unknown
	}
	return FromBool(selection.Reject)
}

// barrierProbeAddress 从 CIDR 取出可用于 route -n get 的地址。
func barrierProbeAddress(cidr string) string {
	if index := strings.IndexByte(cidr, '/'); index >= 0 {
		return cidr[:index]
	}
	return cidr
}

func observeDNS(ctx context.Context, deps Deps, state *ObservedState) (Tristate, []string) {
	if deps.InspectDNS == nil {
		return Unknown, nil
	}
	result, err := deps.InspectDNS(ctx)
	if err != nil {
		state.Errors = append(state.Errors, ObserveError{Item: "dns_managed", Err: err.Error()})
		return Unknown, nil
	}
	return FromBool(result.Enabled), result.Servers
}

func observeCore(deps Deps, state *ObservedState) (socket, tunnel Tristate) {
	if deps.FetchRuntime == nil {
		return Unknown, Unknown
	}
	result, err := deps.FetchRuntime()
	if err != nil {
		state.Errors = append(state.Errors, ObserveError{Item: "core_socket", Err: err.Error()})
		// socket 不应答是确定的观测;但隧道健康问不出来,必须是 Unknown。
		return False, Unknown
	}
	return True, FromBool(result.TunnelHealthy)
}
