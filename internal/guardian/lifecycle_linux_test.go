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
