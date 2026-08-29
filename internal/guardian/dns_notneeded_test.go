package guardian

import (
	"context"
	"encoding/json"
	"testing"
)

// DNSNotNeeded 是第四态:「本平台没有 DNS 接管这件事」(linux:数据面整机
// 劫持 + engine 拦 UDP:53,resolv.conf 一个字不碰)。它与 DNSManaged(接管
// 成功)、DNSUnmanaged(该接管而没接管 —— darwin 上这是故障)、DNSUnknown
// (问不出来)都不同;把它折进任何一个都是撒谎:折进 Managed 是伪造绿灯,
// 折进 Unmanaged 会让 Up 在一台完全健康的 linux 机器上恒失败。
func TestUpSucceedsWhenDNSNeedsNoTakeoverOnThisPlatform(t *testing.T) {
	env := newManagerTestEnv(t)
	env.dns.ensureFunc = func(context.Context) (DNSStatus, error) {
		return DNSStatus{State: DNSNotNeeded}, nil
	}
	env.dns.inspectFunc = func(context.Context) (DNSStatus, error) {
		return DNSStatus{State: DNSNotNeeded}, nil
	}
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatalf("Up 在「无需接管」的平台上失败: %v", err)
	}
	status := env.manager.Status()
	if status.Protection != ProtectionProtected {
		t.Fatalf("Protection = %q, want protected", status.Protection)
	}
	// dns_managed 如实为 false —— 不许为了绿灯伪造接管。
	if status.DNSManaged {
		t.Fatal("无需接管的平台不许报 dns_managed=true")
	}
	if status.DNSState != DNSNotNeeded {
		t.Fatalf("DNSState = %q, want not_needed", status.DNSState)
	}
}

// **停止路径的那道门**:linux 的 Restore 如实答 NotNeeded,restoreDNS 不许把
// 它当失败 —— 漏掉这道门,linux 的 Down 恒失败、启动恢复把 recoveryBlocked
// 锁上(「开不了升级成关不掉」的机制)。2026-08-29 code review 抓到:第四态
// 教了两道门漏了第三道,而本文件当时恰好只测 Up/Inspect 没测 Restore ——
// 测试输入让缺陷不可见的又一例。
func TestRestoreDNSAcceptsNotNeededAsCleanlyRestored(t *testing.T) {
	env := newManagerTestEnv(t)
	env.dns.restoreResults = []fakeDNSResult{{status: DNSStatus{State: DNSNotNeeded}}}
	if err := env.manager.restoreDNS(context.Background()); err != nil {
		t.Fatalf("NotNeeded 的还原被当成失败: %v", err)
	}
}

// darwin 的既有语义一分不放:Unmanaged(该接管而没接管)仍然是验证失败。
// 这条与 manager_test.go 里既有的 dns_verification_failed 断言互为表里 ——
// 那边守「坏状态仍失败」,这边守「第四态不经过那条失败路径」。
func TestNotNeededStateSurvivesStatusJSONRoundTrip(t *testing.T) {
	// normalizedDNSState 的白名单少了它,发布出去就变成 "unknown" ——
	// 菜单会显示「没查」,而事实是「查了,本平台无此事」。
	raw, err := json.Marshal(Status{SchemaVersion: 1, DNSState: DNSNotNeeded})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		DNSState string `json:"dns_state"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.DNSState != string(DNSNotNeeded) {
		t.Fatalf("dns_state 穿过 JSON 变成了 %q —— 白名单把第四态压回 unknown", back.DNSState)
	}
}
