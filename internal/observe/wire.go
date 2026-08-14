package observe

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/getbx/bx/internal/barriercidr"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/supervisor"
)

// LiveDeps 把生产环境的真实能力适配成 Deps。
//
// 适配层放在本包而非各自包里,是为了让 internal/observe 的核心保持零依赖、可测;
// 只有这一个文件 import 外部包。
//
// Core 的运行时状态(TUN 名字与隧道健康)同出一次控制 socket 往返并缓存:
// 一次观测里问两遍既浪费,也可能给出自相矛盾的两个时刻的答案。
func LiveDeps(socketPath string) Deps {
	runtimeState := cachedRuntimeState(socketPath)
	return Deps{
		// 与 guardian 装屏障时用的是同一个叶子包里的同一份清单。此处刻意不
		// import guardian:guardian 侧的调谐判据要 import 本包的观测类型。
		BarrierCIDRs: barriercidr.Blocking,
		Now:          time.Now,
		TunName: func() (string, error) {
			state, err := runtimeState()
			if err != nil {
				return "", err
			}
			return state.TunName, nil
		},
		FetchRuntime: func() (RuntimeResult, error) {
			state, err := runtimeState()
			if err != nil {
				return RuntimeResult{}, err
			}
			return RuntimeResult{TunnelHealthy: state.TunnelHealthy}, nil
		},
		LookupRoute: func(ctx context.Context, destination string, ipv6 bool) (RouteResult, error) {
			selection, err := supervisor.LookupRoute(ctx, destination, ipv6)
			if err != nil {
				return RouteResult{}, err
			}
			return RouteResult{Interface: selection.Interface, Reject: selection.Reject}, nil
		},
		InspectDNS: func(ctx context.Context) (DNSResult, error) {
			status, err := install.InspectDNSContext(ctx, "")
			if err != nil {
				return DNSResult{}, err
			}
			if !status.Supported {
				// 平台不支持 DNS 接管查询时,Enabled 恒为 false。把它当作
				// "观测到 DNS 不归 bx"就是撒谎:我们根本没问过系统。
				return DNSResult{}, unsupportedDNSError(status.Detail)
			}
			return DNSResult{Servers: status.Servers, Enabled: status.Enabled}, nil
		},
		// 「bx 自己的直连出得去吗」。只读地问内核,不发包 —— 见
		// supervisor.DirectEgressReachable。非 darwin 上恒返回「不适用」,
		// 由 NotApplicableForPlatform 声明,这里不接线。
		DirectEgress: func(ctx context.Context) (Tristate, error) {
			reachable, known, err := supervisor.DirectEgressReachable(ctx)
			if err != nil {
				return Unknown, err
			}
			if !known {
				return Unknown, nil
			}
			if reachable {
				return True, nil
			}
			return False, nil
		},
		// **声明这个平台上不成立的问题。**
		//
		// Linux 上 bx 不改系统 DNS —— 它在 TUN 里拦 UDP:53(见 tun/engine.go)。
		// 「系统 DNS 归不归 bx」在那里根本不是正确的问题:报 False 像「明明受保护却
		// 说没接管」一样撒谎,报 Unknown 会让每台健康机器每次观测都吐一条永久
		// divergence,而那会把 divergence 训练成噪声。
		//
		// 其余四项在 Linux 上都问得出来:capture 与 barrier 走 supervisor.LookupRoute
		// (2026-08-13 归位,三平台都有),core_socket 与 tunnel_healthy 走控制 socket。
		NotApplicable: NotApplicableForPlatform(runtime.GOOS),
	}
}

// notApplicableForPlatform 列出该平台上根本不成立的观测项。
//
// **它按平台而不是按「能不能问出来」分。** 后者是 Unknown 的语义;这里说的是
// 「这个问题在这个平台上没有意义」——两者对用户的含义完全不同,而把前者伪装成
// 后者正是本包存在的理由的反面。
func NotApplicableForPlatform(goos string) []string {
	if goos == "darwin" {
		return nil
	}
	// bx 只在 macOS 上接管系统 DNS(networksetup);别处靠 TUN 拦截。
	// direct_egress 同理:`IP_BOUND_IF` 那套 scoped 路由语义是 macOS 特有的,
	// Linux 用 SO_MARK、Windows 用 IP_UNICAST_IF,都不经 scoped 路由表。
	return []string{"dns_managed", "direct_egress"}
}

func unsupportedDNSError(detail string) error {
	if strings.TrimSpace(detail) == "" {
		return errors.New("本平台不支持 DNS 接管观测")
	}
	return fmt.Errorf("本平台不支持 DNS 接管观测:%s", detail)
}

// cachedRuntimeState 让一次观测内的多个消费者共享同一次控制 socket 往返。
func cachedRuntimeState(socketPath string) func() (supervisor.RuntimeState, error) {
	var once sync.Once
	var state supervisor.RuntimeState
	var err error
	return func() (supervisor.RuntimeState, error) {
		once.Do(func() { state, err = supervisor.FetchRuntimeState(socketPath) })
		return state, err
	}
}
