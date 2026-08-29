//go:build linux

package guardian

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type scriptedIPRunner struct {
	fail map[string]string // 命令串 → 注入的输出措辞
	ran  []string
}

func (r *scriptedIPRunner) Run(_ context.Context, c Command) error {
	line := c.String()
	r.ran = append(r.ran, line)
	if msg, ok := r.fail[line]; ok {
		return commandOutputError{err: errors.New("exit status 2"), output: msg}
	}
	return nil
}

func linuxExecCtx() BarrierContext {
	return BarrierContext{
		Gateway:      "192.168.1.1",
		ServerBypass: []string{"203.0.113.20/32"},
		BlockIPv6:    true,
	}
}

// 装到已存在的行是幂等(重装/上次没拆干净都会撞到),但真实失败必须中止并
// 点名那条命令 —— 容错把 Operation not permitted 也吞掉的话,屏障装了个寂寞
// 而调用方以为 fail-closed 已就位。
func TestLinuxBarrierInstallToleratesExistsButReportsRealFailures(t *testing.T) {
	runner := &scriptedIPRunner{fail: map[string]string{
		"ip route add throw 10.0.0.0/8 table 90": "RTNETLINK answers: File exists",
	}}
	if err := NewBarrier(runner).Install(context.Background(), linuxExecCtx()); err != nil {
		t.Fatalf("已存在应被容错: %v", err)
	}

	runner = &scriptedIPRunner{fail: map[string]string{
		"ip rule add pref 120 table 90": "RTNETLINK answers: Operation not permitted",
	}}
	err := NewBarrier(runner).Install(context.Background(), linuxExecCtx())
	if err == nil {
		t.Fatal("非 root 的失败被吞掉了")
	}
	if !strings.Contains(err.Error(), "ip rule add pref 120 table 90") {
		t.Fatalf("错误必须点名失败的命令: %v", err)
	}
}

// 拆除对「不存在」容错(busybox 与 iproute2 两种措辞都会出现),
// 逃生口更是无条件可跑 —— 它面对的就是一半在一半不在的残局。
func TestLinuxBarrierRemovalPathsTolerateMissingEntries(t *testing.T) {
	runner := &scriptedIPRunner{fail: map[string]string{
		"ip rule del pref 120 table 90":               "RTNETLINK answers: No such file or directory",
		"ip route del unreachable 0.0.0.0/2 table 90": "RTNETLINK answers: No such process",
		"ip route del 203.0.113.20/32 table 90":       "RTNETLINK answers: No such process",
	}}
	if err := NewBarrier(runner).Remove(context.Background(), linuxExecCtx()); err != nil {
		t.Fatalf("不存在应被容错: %v", err)
	}

	runner = &scriptedIPRunner{fail: map[string]string{
		"ip route flush table 90": "RTNETLINK answers: No such process",
	}}
	if err := RemoveBlockingBarrierRoutes(context.Background(), runner); err != nil {
		t.Fatalf("逃生口清理应无条件可跑: %v", err)
	}
	if len(runner.ran) != len(PlanLinuxBarrierCleanup()) {
		t.Fatalf("逃生口没有跑完全部清理命令: %v", runner.ran)
	}
}
