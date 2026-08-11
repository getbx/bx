//go:build darwin

package leakserve

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/supervisor"
)

// LiveFactDeps 把生产环境的真实能力接上。**全部只读,全部不需要 root。**
func LiveFactDeps() FactDeps {
	return FactDeps{
		LookupRoute: func(ctx context.Context, dest string, ipv6 bool) (string, error) {
			selection, err := supervisor.LookupRoute(ctx, dest, ipv6)
			if err != nil {
				// route -n get 对「没有到达该目的地的路由」也是报错退出。
				// 把它翻成 ErrNoRoute,让「确知没有」与「问不出来」分开。
				if isNoRouteError(err) {
					return "", ErrNoRoute
				}
				return "", err
			}
			if selection.Reject {
				// 内核明说这条路被 reject(bx 自己的 v6 黑洞就是这个形状):
				// 那是**确知**没有通路,不是问不出来。
				return "", ErrNoRoute
			}
			return selection.Interface, nil
		},
		InspectDNS: func(ctx context.Context) ([]string, error) {
			status, err := install.InspectDNSContext(ctx, "")
			if err != nil {
				return nil, err
			}
			if !status.Supported {
				// **「没问过」不是「不归 bx」**(照 internal/observe/wire.go 的
				// unsupportedDNSError):Supported=false 时其余字段恒为零值,
				// 当成事实用就是拿一个没问过的答案冒充观测。
				return nil, unsupportedDNSError(status.Detail)
			}
			return status.Servers, nil
		},
		GuardianStatus:  guardianTunAndProtection,
		ListVPNServices: listDarwinVPNServices,
	}
}

func unsupportedDNSError(detail string) error {
	if strings.TrimSpace(detail) == "" {
		return errors.New("本平台不支持 DNS 观测")
	}
	return fmt.Errorf("本平台不支持 DNS 观测:%s", detail)
}

// guardianTunAndProtection 问 bx 自己那一半。
//
// Guardian 的 socket 是 0666,**普通用户读 /v1/status 返回 200**(真机已验,
// uid 501)。这是这个功能不需要 root 的关键一环。
//
// **TUN 名字不在 Guardian 的 Status 里**(CoreRuntime 只有 Reachable/健康度/
// 服务器/传输),它只有 Core 自己的控制 socket 知道 —— 那条路同样不需要 root。
// 于是分两跳:
//   - Guardian 拨不通 ⇒ 什么都不知道,报错(归因据此不敢把 utun 认成别人的);
//   - Guardian 说 Core 不可达 ⇒ **确知** bx 现在没有 TUN,返回空串 + nil,
//     这正是「保护关着时把 utun4 认成 Work VPN」那个主用途所依赖的答案;
//   - Core 可达但问不出它的 TUN ⇒ 报错。**不许退回空串**:那会让「有一条属于
//     bx 的 utun,只是不知道是哪条」被读成「bx 没有 TUN」,于是 bx 自己的接口
//     可能被贴上另一个 VPN 的名字。
func guardianTunAndProtection(ctx context.Context) (string, string, error) {
	status, err := guardian.NewClient(guardian.SocketPath).Status(ctx)
	if err != nil {
		return "", "", err
	}
	if status.Core == nil || !status.Core.Reachable {
		return "", status.Protection, nil
	}
	state, err := supervisor.FetchRuntimeState(supervisor.SockPath)
	if err != nil {
		return "", "", fmt.Errorf("core is running but its TUN name could not be read: %w", err)
	}
	return state.TunName, status.Protection, nil
}

var noRouteRe = regexp.MustCompile(`(?i)no route to host|host is down|not in table|network is unreachable`)

func isNoRouteError(err error) bool {
	return err != nil && noRouteRe.MatchString(err.Error())
}

// scutil --nc list 的行形如:
//
//   - (Disconnected)   6E1B…  PPP (L2TP)   "Work VPN"        [OnDemand]
//   - (Connected)      A2C4…  IPSec        "Home VPN"
var scutilLineRe = regexp.MustCompile(`^\s*\*?\s*\((\w+)\)\s+\S+\s+.*?"([^"]+)"`)

func listDarwinVPNServices(parent context.Context) ([]leakcheck.VPNService, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--nc", "list").Output()
	if err != nil {
		return nil, err
	}
	return parseSCUtilNCList(string(out)), nil
}

// parseSCUtilNCList 是纯解析,单测覆盖(免 root、不跑 scutil)。
func parseSCUtilNCList(out string) []leakcheck.VPNService {
	var services []leakcheck.VPNService
	for _, line := range strings.Split(out, "\n") {
		m := scutilLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		services = append(services, leakcheck.VPNService{
			Name:      m[2],
			Connected: strings.EqualFold(m[1], "Connected"),
		})
	}
	return services
}
