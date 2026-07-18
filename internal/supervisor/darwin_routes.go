package supervisor

import (
	"fmt"
	"net/netip"
	"strings"
)

// darwin_routes.go 是 macOS Hijack 的**纯路由命令构造**(无 build tag、不执行 route,
// 故可在任意平台免 root 单测)。真正调用 `route`/检测 v6 的部分在 platform_darwin.go。

// darwinDirectCIDRs:macOS 下经物理网关直连的私网段。单表平台与 windows 同源,
// 详见 singleTableDirectCIDRs(directcidrs.go)。
var darwinDirectCIDRs = singleTableDirectCIDRs

// darwinRouteSpec 是一条 macOS 路由:add 命令与对称的 del 命令(均为 `route` 的参数,不含 "route" 本身)。
type darwinRouteSpec struct {
	add   []string
	adopt []string
	del   []string
}

// DarwinRoutePlanOptions 是 macOS 路由 dry-run 的输入。它只用于生成命令文本,不执行任何命令。
type DarwinRoutePlanOptions struct {
	TunName      string
	TunAddr      string
	Gateway      string
	ServerBypass []string
	UserBypass   []string
	BlockV6      bool
}

// DarwinRoutePlan 生成 macOS Hijack 将执行的命令和对称清理命令,供真机验证前审计。
func DarwinRoutePlan(opts DarwinRoutePlanOptions) (apply []string, cleanup []string) {
	tunIP := opts.TunAddr
	if i := strings.IndexByte(tunIP, '/'); i >= 0 {
		tunIP = tunIP[:i]
	}
	apply = append(apply, commandString("ifconfig", opts.TunName, "inet", tunIP, tunIP, "up"))
	specs := darwinRouteSpecs(opts.TunName, opts.Gateway, darwinDirectCIDRs, opts.ServerBypass, opts.UserBypass, opts.BlockV6)

	for _, s := range specs {
		apply = append(apply, commandString("route", s.add...))
	}
	for i := len(specs) - 1; i >= 0; i-- {
		cleanup = append(cleanup, commandString("route", specs[i].del...))
	}
	return apply, cleanup
}

func commandString(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

// darwinRouteSpecs 纯构造 macOS Hijack 的全部 route 命令序列:
//   - v4:directCIDRs(私网/docker)+ serverBypass + userBypass 经物理网关 gw 旁路;
//     split-default(0.0.0.0/1 + 128.0.0.0/1)把默认流量劫进 tunName(utun)。
//   - v6(仅 blockV6=true):两个 /1 的 `-reject` 盖全量全局 v6 —— fail-closed 阻断,
//     本地发送者得 EHOSTUNREACH(逼双栈应用快速回落 v4),与 Linux 的 `unreachable` 决策一致。
//     link-local(fe80::/10)、ULA on-link、组播(ff00::/8)、loopback(::1)因有更具体的
//     on-link/本地路由,按最长前缀匹配自动抢赢直连,无需显式 carve-out(亦绝不可改写本地路由)。
//
// ⚠️ `-reject` 的确切 route 语法(dummy gateway `::1`)与本地 errno 需在真实 macOS 上验证。
func darwinRouteSpecs(tunName, gw string, directCIDRs, serverBypass, userBypass []string, blockV6 bool) []darwinRouteSpec {
	return darwinRouteSpecsWithHandoff(tunName, gw, directCIDRs, serverBypass, userBypass, blockV6, nil)
}

func darwinRouteSpecsWithHandoff(tunName, gw string, directCIDRs, serverBypass, userBypass []string, blockV6 bool, handoffBypasses []string) []darwinRouteSpec {
	var specs []darwinRouteSpec
	authorized := make(map[string]struct{}, len(handoffBypasses))
	for _, cidr := range handoffBypasses {
		authorized[cidr] = struct{}{}
	}
	viaGW := func(cidr string, adopt bool) darwinRouteSpec {
		spec := darwinRouteSpec{
			add: []string{"-n", "add", "-net", cidr, gw},
			del: []string{"-n", "delete", "-net", cidr},
		}
		if adopt {
			spec.adopt = []string{"-n", "change", "-net", cidr, gw}
		}
		return spec
	}
	viaTun := func(cidr string) darwinRouteSpec {
		return darwinRouteSpec{
			add: []string{"-n", "add", "-net", cidr, "-interface", tunName},
			del: []string{"-n", "delete", "-net", cidr},
		}
	}
	for _, c := range directCIDRs {
		specs = append(specs, viaGW(c, false))
	}
	for _, c := range serverBypass {
		_, adopt := authorized[c]
		specs = append(specs, viaGW(c, adopt))
	}
	for _, c := range userBypass {
		specs = append(specs, viaGW(c, false))
	}
	for _, c := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		specs = append(specs, viaTun(c))
	}
	if blockV6 {
		for _, c := range []string{"::/1", "8000::/1"} {
			specs = append(specs, darwinRouteSpec{
				add: []string{"-n", "add", "-inet6", "-net", c, "::1", "-reject"},
				del: []string{"-n", "delete", "-inet6", "-net", c},
			})
		}
	}
	return specs
}

func applyDarwinRouteSpecs(specs []darwinRouteSpec, run func(...string) error) ([]darwinRouteSpec, error) {
	done := make([]darwinRouteSpec, 0, len(specs))
	for _, spec := range specs {
		err := run(spec.add...)
		if err != nil && len(spec.adopt) != 0 && darwinRouteAlreadyExists(err) {
			err = run(spec.adopt...)
		}
		if err != nil {
			return done, fmt.Errorf("route %s: %w", strings.Join(spec.add, " "), err)
		}
		done = append(done, spec)
	}
	return done, nil
}

func cleanupDarwinRouteSpecs(specs []darwinRouteSpec, run func(...string) error) {
	for i := len(specs) - 1; i >= 0; i-- {
		_ = run(specs[i].del...)
	}
}

func darwinRouteAlreadyExists(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "file exists") || strings.Contains(message, "already exists")
}

func parseGuardianBypassHandoff(value string) []string {
	if value == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var bypasses []string
	for _, candidate := range strings.Split(value, ",") {
		prefix, err := netip.ParsePrefix(candidate)
		if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 32 || prefix != prefix.Masked() {
			return nil
		}
		canonical := prefix.String()
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		bypasses = append(bypasses, canonical)
	}
	return bypasses
}
