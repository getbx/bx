package supervisor

import (
	"fmt"
	"log"
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
	// optional 表示这条路由装不上就跳过,**而且不记进待删列表**。
	//
	// 后半句是要害:记进去就会在拆除时把一条**不是我们装的**路由删掉。
	// scoped 默认路由正是这种情形 —— 多网络服务的 Mac 上系统自己就有一条,
	// `route add` 会以 "File exists" 失败,而删掉系统那条会打断用户的网络。
	optional bool
}

// DarwinRoutePlanOptions 是 macOS 路由 dry-run 的输入。它只用于生成命令文本,不执行任何命令。
type DarwinRoutePlanOptions struct {
	TunName string
	TunAddr string
	Gateway string
	// PhysicalDev 是物理出口网卡名。空 = 没探出来,那时不装 scoped 默认路由
	// (拿空字符串去拼一条 route 命令,比不装更糟)。
	PhysicalDev  string
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
	specs := darwinRouteSpecs(opts.TunName, opts.Gateway, opts.PhysicalDev, darwinDirectCIDRs, opts.ServerBypass, opts.UserBypass, opts.BlockV6)

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
func darwinRouteSpecs(tunName, gw, physicalDev string, directCIDRs, serverBypass, userBypass []string, blockV6 bool) []darwinRouteSpec {
	return darwinRouteSpecsWithHandoff(tunName, gw, physicalDev, directCIDRs, serverBypass, userBypass, blockV6, nil)
}

func darwinRouteSpecsWithHandoff(tunName, gw, physicalDev string, directCIDRs, serverBypass, userBypass []string, blockV6 bool, handoffBypasses []string) []darwinRouteSpec {
	var specs []darwinRouteSpec
	// **给物理接口装一条 scoped 默认路由,否则 bx 自己的直连整个到不了公网。**
	//
	// DirectDialer 用 `IP_BOUND_IF` 绑物理网卡防环,而它**只查该接口的 scoped
	// 路由表**。macOS 只在有多个活跃网络服务时才装 per-interface scoped default;
	// 单服务的机器上 `default` 是 GLOBAL 的,scoped 表里没有它 —— 于是所有
	// 用户 direct 规则全部 ENETUNREACH(真机实测,见 darwin_scoped_test.go)。
	//
	// 它只进 scoped 表,不碰全局表:隧道的 0/1+128/1 照旧压过一切,只有显式
	// 用了 IP_BOUND_IF 的 socket(恰好就是 bx 自己的直连/解析/socks)会用到它。
	// **可选**:系统已有同款时 add 会失败,那时跳过且绝不在拆除时删它。
	if dev := strings.TrimSpace(physicalDev); dev != "" && strings.TrimSpace(gw) != "" {
		specs = append(specs, darwinRouteSpec{
			add:      []string{"-n", "add", "-ifscope", dev, "default", gw},
			del:      []string{"-n", "delete", "-ifscope", dev, "default"},
			optional: true,
		})
	}
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
		if err != nil && spec.optional {
			// 跳过,**并且不记进 done** —— 记进去就会在拆除时删掉一条不是我们装的路由。
			log.Printf("可选路由未装上(跳过,不影响保护):route %s: %v", strings.Join(spec.add, " "), err)
			continue
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
