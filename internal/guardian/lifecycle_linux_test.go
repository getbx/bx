//go:build linux

package guardian

import (
	"context"
	"testing"
)

type recordingBarrierRunner struct {
	commands []string
}

func (r *recordingBarrierRunner) Run(_ context.Context, c Command) error {
	r.commands = append(r.commands, c.String())
	return nil
}

// **这条断言 2026-08-30 被刻意翻过来了,记档在此。**
//
// 它原本钉的是中间态:「barrier/procscan/peercred 已供货但 DNS 与 observer
// 未齐 ⇒ 门必须还是关的」,防的是「供货一块就顺手开门」。现在六块全部到位、
// 每一块都有 netns 断言背书(屏障四条打在 `ip route get` 的判决上、Manager
// 四条打在真 spawn 的进程上),门按 CLAUDE.md 的移植纪律**最后**开 ——
// 于是同一条测试改成钉住相反的一面:门开着,而且清单必须仍然完整。
//
// **翻转它不等于放宽任何东西**:门开着只让 `bx guardian` 在 linux 跑得起来,
// 而生产 linux 没有任何东西会去装或拉起它(systemd unit 指向 `bx run`)。
// 「产品形态不变」由没有调用方保证,不由这道门保证。
func TestLifecyclePlatformLinuxGateIsOpenNowThatEveryPieceIsSupplied(t *testing.T) {
	if err := newLifecyclePlatform().RequireDaemon(); err != nil {
		t.Fatalf("六块全部供货且有台子背书之后,linux 门该开着,got %v", err)
	}
	// 门开了,清单完整就更是硬性的:少一个字段,使用点是 nil deref,
	// 而 Guardian 在 KeepAlive 下 panic 就是崩溃循环。
	if err := newLifecyclePlatform().validate(); err != nil {
		t.Fatalf("门开着而清单有洞: %v", err)
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
