//go:build darwin

package supervisor

import (
	"errors"
	"strings"
	"testing"
)

// **真机诊断(2026-08-13):bx 在 macOS 上的 DirectDialer 到不了公网。**
//
// `DirectDialer` 用 `IP_BOUND_IF` 绑物理网卡防环,而 **`IP_BOUND_IF` 只查该接口的
// scoped 路由表**。macOS 只在有多个活跃网络服务时才装 per-interface scoped default;
// 单服务(只有 Wi-Fi)的机器上 `default` 的标志是 GLOBAL,scoped 表里没有它。
//
// 真机实证(项目所有者的 Mac):
//
//	route -n get -ifscope en0 8.8.8.8   → not in table
//	bound-en0 udp 223.5.5.5:53          → network is unreachable
//	bound-en0 tcp 166.1.190.123:443     → 通(bx 自己装了这条 /32 的 en0 路由)
//
// 于是**所有用户 direct 规则全死**,而隧道正常 —— 因为隧道的 server bypass 恰好
// 是一条显式 en0 路由。这一直是坏的,只是 bx 从来数不出失败,所以没人知道;
// 结果计数(本轮加的)第一次让它显形:`*.qq.com 1291 条,失败 1289`。
//
// 修法:Hijack 顺手给物理接口装一条 scoped 默认路由。它**只进 scoped 表**,
// 不碰全局表,所以隧道的 0/1+128/1 照旧压过一切 —— 只有用了 IP_BOUND_IF 的
// socket(恰好就是 bx 自己的直连/解析/socks)会用到它。
func TestHijackPlanInstallsAScopedDefaultForThePhysicalInterface(t *testing.T) {
	specs := darwinRouteSpecs("utun0", "192.168.50.2", "en0", []string{"10.0.0.0/8"}, nil, nil, false)
	var found *darwinRouteSpec
	for i := range specs {
		if strings.Contains(strings.Join(specs[i].add, " "), "-ifscope") {
			found = &specs[i]
		}
	}
	if found == nil {
		t.Fatalf("路由计划里没有 scoped 默认路由 —— DirectDialer 仍然到不了公网:\n%v", dump(specs))
	}
	add := strings.Join(found.add, " ")
	for _, want := range []string{"-ifscope", "en0", "default", "192.168.50.2"} {
		if !strings.Contains(add, want) {
			t.Errorf("scoped 路由缺 %q:%s", want, add)
		}
	}
	if del := strings.Join(found.del, " "); !strings.Contains(del, "-ifscope") || !strings.Contains(del, "en0") {
		t.Errorf("拆除不对称:%s", del)
	}
	// **必须是可选的。** 多服务的 Mac 上系统自己就有 scoped default,`route add`
	// 会失败 —— 那时既不能让整个 Hijack 失败,更不能在拆除时删掉系统自己的路由。
	if !found.optional {
		t.Error("scoped 默认路由不是可选的 —— 多服务的 Mac 上会让 Hijack 整个失败")
	}
}

// 探不出物理接口时**不装**,而不是拿空字符串拼一条命令。
func TestNoScopedDefaultWithoutAPhysicalInterface(t *testing.T) {
	for _, dev := range []string{"", "   "} {
		for _, spec := range darwinRouteSpecs("utun0", "192.168.50.2", dev, nil, nil, nil, false) {
			if strings.Contains(strings.Join(spec.add, " "), "-ifscope") {
				t.Fatalf("接口名为 %q 时仍装了 scoped 路由:%v", dev, spec.add)
			}
		}
	}
}

// **可选的路由装不上时:跳过、继续、而且不进待删列表。**
//
// 最后半句是要害:记进去就会在拆除时 `route delete -ifscope en0 default` ——
// 删掉的是**系统自己的**那条,而那会打断用户的网络,且 bx 无从恢复。
func TestOptionalSpecFailureIsSkippedAndNotRecordedForDeletion(t *testing.T) {
	specs := []darwinRouteSpec{
		{add: []string{"add", "a"}, del: []string{"delete", "a"}},
		{add: []string{"add", "-ifscope", "en0", "default", "gw"}, del: []string{"delete", "-ifscope", "en0", "default"}, optional: true},
		{add: []string{"add", "b"}, del: []string{"delete", "b"}},
	}
	done, err := applyDarwinRouteSpecs(specs, func(args ...string) error {
		if strings.Contains(strings.Join(args, " "), "-ifscope") {
			return errors.New("route: writing to routing socket: File exists")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("可选路由失败拖垮了整个 Hijack:%v", err)
	}
	if len(done) != 2 {
		t.Fatalf("待删列表 = %d 条,want 2(可选那条不该记进去)", len(done))
	}
	for _, spec := range done {
		if strings.Contains(strings.Join(spec.del, " "), "-ifscope") {
			t.Fatal("装失败的 scoped 路由进了待删列表 —— 拆除会删掉系统自己的那条")
		}
	}
}

// 装成功的可选路由**要**记进待删列表,否则我们自己装的那条会留在系统里。
func TestSucceedingOptionalSpecIsStillRecorded(t *testing.T) {
	specs := []darwinRouteSpec{
		{add: []string{"add", "-ifscope", "en0", "default", "gw"}, del: []string{"delete", "-ifscope", "en0", "default"}, optional: true},
	}
	done, err := applyDarwinRouteSpecs(specs, func(...string) error { return nil })
	if err != nil || len(done) != 1 {
		t.Fatalf("done=%d err=%v —— 装成功的路由必须能被对称拆除", len(done), err)
	}
}

// 非可选的路由失败仍然必须让 Hijack 失败并回滚 —— 那些是保护本身。
func TestRequiredSpecFailureStillAborts(t *testing.T) {
	specs := []darwinRouteSpec{{add: []string{"add", "0/1", "-interface", "utun0"}, del: []string{"delete", "0/1"}}}
	if _, err := applyDarwinRouteSpecs(specs, func(...string) error { return errors.New("boom") }); err == nil {
		t.Fatal("split-default 装不上却没让 Hijack 失败")
	}
}

func dump(specs []darwinRouteSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, strings.Join(s.add, " "))
	}
	return out
}
