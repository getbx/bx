package supervisor

import (
	"errors"
	"strings"
	"testing"
)

func specAdds(specs []darwinRouteSpec) map[string]bool {
	m := map[string]bool{}
	for _, s := range specs {
		m[strings.Join(s.add, " ")] = true
	}
	return m
}

func specDels(specs []darwinRouteSpec) map[string]bool {
	m := map[string]bool{}
	for _, s := range specs {
		m[strings.Join(s.del, " ")] = true
	}
	return m
}

// v6 启用时(blockV6=true),darwin Hijack 必须 fail-closed 阻断全局 IPv6:
// 两个 /1 的 -reject 盖全量(回 EHOSTUNREACH 逼 v4 回落);link-local/ULA/组播/loopback
// 因有更具体的 on-link/本地路由按最长前缀自动直连,无需显式 carve-out。v4 行为零回归。
func TestDarwinRouteSpecsBlocksV6(t *testing.T) {
	specs := darwinRouteSpecs("utun5", "192.168.1.1",
		[]string{"10.0.0.0/8"}, []string{"1.2.3.4/32"}, nil, true)
	adds := specAdds(specs)
	dels := specDels(specs)

	wantAdd := []string{
		"-n add -inet6 -net ::/1 ::1 -reject",     // 全局 v6 下半 → reject
		"-n add -inet6 -net 8000::/1 ::1 -reject", // 全局 v6 上半 → reject
		"-n add -net 0.0.0.0/1 -interface utun5",  // v4 split-default 零回归
		"-n add -net 10.0.0.0/8 192.168.1.1",      // v4 私网经网关零回归
		"-n add -net 1.2.3.4/32 192.168.1.1",      // serverBypass 零回归
	}
	for _, w := range wantAdd {
		if !adds[w] {
			t.Errorf("缺 add 命令: %q", w)
		}
	}
	wantDel := []string{
		"-n delete -inet6 -net ::/1",
		"-n delete -inet6 -net 8000::/1",
	}
	for _, w := range wantDel {
		if !dels[w] {
			t.Errorf("缺对称 del 命令: %q", w)
		}
	}
}

// macOS 单路由表:bx 若把 CGNAT(100.64.0.0/10)route → 物理网关,会和 tailscale 的 overlay
// 路由(100.64.0.0/10 → tailscale utun,同前缀)冲突,从 bx 主机主动连 tailscale peer 的 TCP
// 因此被丢。tailscale 的 100.64/10 比 split-default 的 0/1 更具体,按最长前缀自然抢赢,故
// bx 在 macOS 不该认领 CGNAT —— darwinDirectCIDRs 不含 100.64/10,其余私网段直连零回归。
func TestDarwinDoesNotClaimCGNAT(t *testing.T) {
	specs := darwinRouteSpecs("utun5", "192.168.1.1", darwinDirectCIDRs, nil, nil, false)
	adds := specAdds(specs)
	if adds["-n add -net 100.64.0.0/10 192.168.1.1"] {
		t.Error("macOS 不应把 CGNAT 100.64.0.0/10 route 到物理网关(会和 tailscale overlay 同前缀冲突)")
	}
	for _, w := range []string{
		"-n add -net 10.0.0.0/8 192.168.1.1",
		"-n add -net 172.16.0.0/12 192.168.1.1",
		"-n add -net 192.168.0.0/16 192.168.1.1",
	} {
		if !adds[w] {
			t.Errorf("私网直连零回归丢失: %q", w)
		}
	}
}

// v6 禁用时(blockV6=false),不产任何 -inet6 路由(不连累 v4),v4 split-default 仍在。
func TestDarwinRouteSpecsSkipsV6WhenDisabled(t *testing.T) {
	specs := darwinRouteSpecs("utun5", "192.168.1.1",
		[]string{"10.0.0.0/8"}, nil, nil, false)
	for _, s := range specs {
		if strings.Contains(strings.Join(s.add, " "), "-inet6") {
			t.Errorf("blockV6=false 不应产 v6 路由: %q", strings.Join(s.add, " "))
		}
	}
	if !specAdds(specs)["-n add -net 0.0.0.0/1 -interface utun5"] {
		t.Error("v4 split-default 应仍在")
	}
}

func TestDarwinRoutePlanIsDryRunText(t *testing.T) {
	apply, cleanup := DarwinRoutePlan(DarwinRoutePlanOptions{
		TunName:      "utun9",
		TunAddr:      "198.51.100.1/30",
		Gateway:      "192.168.1.1",
		ServerBypass: []string{"1.2.3.4/32"},
		UserBypass:   []string{"203.0.113.0/24"},
		BlockV6:      true,
	})

	wantApply := []string{
		"ifconfig utun9 inet 198.51.100.1 198.51.100.1 up",
		"route -n add -net 1.2.3.4/32 192.168.1.1",
		"route -n add -net 203.0.113.0/24 192.168.1.1",
		"route -n add -net 0.0.0.0/1 -interface utun9",
		"route -n add -inet6 -net ::/1 ::1 -reject",
	}
	for _, w := range wantApply {
		if !stringSliceContains(apply, w) {
			t.Errorf("apply 缺命令: %q", w)
		}
	}
	if stringSliceContains(apply, "route -n add -net 100.64.0.0/10 192.168.1.1") {
		t.Error("dry-run 计划不应认领 macOS CGNAT")
	}
	if !stringSliceContains(cleanup, "route -n delete -inet6 -net ::/1") {
		t.Error("cleanup 缺 IPv6 reject 清理命令")
	}
	if !stringSliceContains(cleanup, "route -n delete -net 0.0.0.0/1") {
		t.Error("cleanup 缺 split-default 清理命令")
	}
}

func TestDarwinCoreAdoptsAndOwnsPreinstalledServerBypass(t *testing.T) {
	routes := map[string]string{"1.2.3.4/32": "192.168.1.1"}
	var commands []string
	run := func(args ...string) error {
		commands = append(commands, strings.Join(args, " "))
		if len(args) < 4 || args[0] != "-n" {
			return errors.New("malformed route command")
		}
		switch args[1] {
		case "add":
			cidr := args[3]
			if _, exists := routes[cidr]; exists {
				return errors.New("route: writing to routing socket: File exists")
			}
			routes[cidr] = strings.Join(args[4:], " ")
		case "change":
			cidr := args[3]
			if _, exists := routes[cidr]; !exists {
				return errors.New("route: not in table")
			}
			routes[cidr] = strings.Join(args[4:], " ")
		case "delete":
			delete(routes, args[3])
		default:
			return errors.New("unsupported route command")
		}
		return nil
	}

	specs := darwinRouteSpecs("utun5", "192.168.1.1", nil, []string{"1.2.3.4/32"}, nil, false)
	owned, err := applyDarwinRouteSpecs(specs, run)
	if err != nil {
		t.Fatalf("Core rejected Guardian bypass: %v; commands=%#v", err, commands)
	}
	wantPrefix := []string{
		"-n add -net 1.2.3.4/32 192.168.1.1",
		"-n change -net 1.2.3.4/32 192.168.1.1",
	}
	if len(commands) < len(wantPrefix) || !reflectStringPrefix(commands, wantPrefix) {
		t.Fatalf("commands = %#v, want prefix %#v", commands, wantPrefix)
	}
	if got := routes["1.2.3.4/32"]; got != "192.168.1.1" {
		t.Fatalf("adopted bypass target = %q", got)
	}

	cleanupDarwinRouteSpecs(owned, run)
	if len(routes) != 0 {
		t.Fatalf("Core cleanup did not remove owned routes: %#v", routes)
	}
}

func TestDarwinCoreDoesNotAdoptUnownedDuplicateRoute(t *testing.T) {
	run := func(args ...string) error {
		if strings.Join(args, " ") == "-n add -net 0.0.0.0/1 -interface utun5" {
			return errors.New("route: writing to routing socket: File exists")
		}
		return nil
	}
	specs := darwinRouteSpecs("utun5", "192.168.1.1", nil, nil, nil, false)
	if _, err := applyDarwinRouteSpecs(specs, run); err == nil {
		t.Fatal("Core adopted a duplicate route outside the Guardian server handoff")
	}
}

func reflectStringPrefix(got, want []string) bool {
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func stringSliceContains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
