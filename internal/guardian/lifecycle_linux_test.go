//go:build linux

package guardian

import (
	"context"
	"errors"
	"testing"
)

type recordingBarrierRunner struct {
	commands []string
}

func (r *recordingBarrierRunner) Run(_ context.Context, c Command) error {
	r.commands = append(r.commands, c.String())
	return nil
}

// linux 清单的中间态,两半都要钉住:barrier/procscan/peercred 已供货,
// 但 DNS 与 observer 未齐 —— **门必须还是关的**。「供货一块就顺手开门」正是
// CLAUDE.md 里「requireDaemonPlatform 最后放开,顺序不许反」要防的那个动作。
func TestLifecyclePlatformLinuxGateStaysShutWhileSupplyIsPartial(t *testing.T) {
	if err := newLifecyclePlatform().RequireDaemon(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("linux 门在供货齐之前必须保持关闭,got %v", err)
	}
}

// linux 的 DNS 语义是「本平台无此事」:三个方法全部 NotNeeded、nil error。
// 报 Unmanaged 会让 Up 在健康机器上恒失败于 dns_verification_failed,
// 报 Managed 是伪造绿灯 —— 两个方向的撒谎都在 manager 侧有守卫,这里钉住
// 清单接的确实是那份诚实实现。
func TestLifecyclePlatformLinuxDNSNeedsNoTakeover(t *testing.T) {
	dns := newLifecyclePlatform().NewDNSManager("")
	status, err := dns.EnsureManaged(context.Background())
	if err != nil || status.State != DNSNotNeeded {
		t.Fatalf("EnsureManaged = %+v, %v; want not_needed, nil", status, err)
	}
	status, err = dns.Restore(context.Background())
	if err != nil || status.State != DNSNotNeeded {
		t.Fatalf("Restore = %+v, %v; want not_needed, nil(停止路径不许因无此事而失败)", status, err)
	}
}

// linux 首期显式无观测:构造器返回 nil,daemon 对 nil 不装 observer 生命周期
// (startRecoveredDaemon 的既有分支)。一个假装在观测的桩比没有更糟 ——
// 「观测不到 ≠ 观测到没有」。
func TestLifecyclePlatformLinuxHasExplicitlyNoNetworkObserver(t *testing.T) {
	if obs := newLifecyclePlatform().NewNetworkObserver(nil); obs != nil {
		t.Fatalf("linux 首期不该有 network observer,got %T", obs)
	}
}

// 清单接到的必须是真 barrier(不再是 unsupported 桩):装一次合法屏障,
// 命令要真的经 runner 流出,且最后武装的是 pref-120 rule。
func TestLifecyclePlatformLinuxBarrierIsWiredToTheRealPlanner(t *testing.T) {
	runner := &recordingBarrierRunner{}
	barrier := newLifecyclePlatform().NewBarrier(runner)
	err := barrier.Install(context.Background(), BarrierContext{
		Gateway:      "192.168.1.1",
		ServerBypass: []string{"203.0.113.20/32"},
		BlockIPv6:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) == 0 {
		t.Fatal("Install 没有产生任何命令")
	}
	if got := runner.commands[len(runner.commands)-1]; got != "ip -6 rule add pref 120 table 90" {
		t.Fatalf("最后武装的不是 rule: %s", got)
	}
}
