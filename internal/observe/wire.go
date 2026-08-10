package observe

import (
	"context"
	"errors"
	"fmt"
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
	}
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
