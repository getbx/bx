package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// UnderlaySnapshot is the canonical physical path identity used during recovery.
// It deliberately excludes TUN state: capture is validated separately before any
// underlay-dependent route can change.
type UnderlaySnapshot struct {
	Generation string
	Interface  string
	Gateway    netip.Addr
	LocalCIDRs []netip.Prefix
}

// underlayManager owns only observation, capture validation, and physical bypass
// rebinding. It must never construct or remove capture routes.
type underlayManager interface {
	Observe(context.Context) (UnderlaySnapshot, error)
	ValidateCapture(context.Context, tunHandle) error
	Rebind(context.Context, tunHandle, UnderlaySnapshot, UnderlaySnapshot, []string, []string) error
}

// commandRunner keeps route execution injectable. Tests must use a fake runner;
// the Darwin implementation is the only production runner.
type commandRunner interface {
	Run(context.Context, string, ...string) error
}

func newUnderlaySnapshot(interfaceName string, gateway netip.Addr, localCIDRs []netip.Prefix) (UnderlaySnapshot, error) {
	snapshot, err := canonicalUnderlaySnapshot(interfaceName, gateway, localCIDRs)
	if err != nil {
		return UnderlaySnapshot{}, err
	}
	snapshot.Generation = underlayGeneration(snapshot)
	return snapshot, nil
}

func canonicalUnderlaySnapshot(interfaceName string, gateway netip.Addr, localCIDRs []netip.Prefix) (UnderlaySnapshot, error) {
	interfaceName = strings.TrimSpace(interfaceName)
	if !physicalUnderlayInterface(interfaceName) {
		return UnderlaySnapshot{}, fmt.Errorf("underlay interface %q is not physical", interfaceName)
	}

	gateway = gateway.Unmap()
	if !gateway.IsValid() || !gateway.Is4() || gateway.IsLoopback() || gateway.IsUnspecified() || gateway.IsMulticast() {
		return UnderlaySnapshot{}, fmt.Errorf("underlay gateway %q is not a physical IPv4 gateway", gateway)
	}

	prefixes := make([]netip.Prefix, 0, len(localCIDRs))
	seen := make(map[netip.Prefix]struct{}, len(localCIDRs))
	for _, prefix := range localCIDRs {
		prefix, err := canonicalUnderlayPrefix(prefix)
		if err != nil {
			return UnderlaySnapshot{}, err
		}
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	sort.Slice(prefixes, func(i, j int) bool { return prefixes[i].String() < prefixes[j].String() })

	return UnderlaySnapshot{
		Interface:  interfaceName,
		Gateway:    gateway,
		LocalCIDRs: prefixes,
	}, nil
}

// CanonicalUnderlayPrefix 是「一个本机地址在物理路径身份里长什么样」的**唯一**定义。
//
// 导出是因为 guardian 的 NetworkObserver 需要算同一个指纹来决定要不要发起恢复,
// 而它此前手抄了一份 —— 两份判据从不互相比对,任何一方单独改动都静默生效
// (2026-08-19:supervisor 侧修好了保留主机位,而观测者那份仍在 Masked(),
// 结果是恢复根本不会被请求,执行侧改得再对也轮不到它跑)。
func CanonicalUnderlayPrefix(prefix netip.Prefix) (netip.Prefix, error) {
	return canonicalUnderlayPrefix(prefix)
}

func canonicalUnderlayPrefix(prefix netip.Prefix) (netip.Prefix, error) {
	if !prefix.IsValid() {
		return netip.Prefix{}, fmt.Errorf("invalid local underlay prefix %q", prefix)
	}
	if prefix.Addr().Is4In6() {
		if prefix.Bits() < 96 {
			return netip.Prefix{}, fmt.Errorf("invalid mapped IPv4 underlay prefix %q", prefix)
		}
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
	}
	// **IPv4 保留主机位。** 这个字段唯一的消费者是 underlayGeneration —— 它不驱动
	// 任何一条路由,它就是「我此刻在哪条物理路径上」这个身份本身。而对 bx 来说,
	// 本机的 IPv4 地址**是**那个身份的一部分:传输子进程的 socket 绑在它上面,
	// server bypass 那条 /32 也是按它选源。抹掉主机位,同一网段换个 IP 就成了
	// 「什么都没变」,于是 NetworkObserver 不请求恢复、darwinUnderlayPlan 直接短路,
	// 而隧道已经死在一个不存在的源地址上(2026-08-19 真机,详见 underlay_test.go)。
	//
	// **IPv6 仍然只取网段。** macOS 的临时地址按天轮换,让它进指纹等于每天定时
	// 重建一次隧道;而 bx 的 v6 是 fail-closed 阻断的,v6 主机位对出口选路毫无影响。
	// 两个方向不对称是刻意的,由 underlay_test.go 的两条测试各自钉住。
	if prefix.Addr().Is4() {
		return prefix, nil
	}
	return prefix.Masked(), nil
}

func physicalUnderlayInterface(interfaceName string) bool {
	if interfaceName == "" {
		return false
	}
	lower := strings.ToLower(interfaceName)
	return !strings.HasPrefix(lower, "utun") && !strings.HasPrefix(lower, "lo")
}

func underlayGeneration(snapshot UnderlaySnapshot) string {
	parts := make([]string, 0, len(snapshot.LocalCIDRs)+2)
	parts = append(parts, snapshot.Interface, snapshot.Gateway.String())
	for _, prefix := range snapshot.LocalCIDRs {
		parts = append(parts, prefix.String())
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:8])
}
