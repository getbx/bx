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

// ipv6.disable=1 的内核(VPS 与 netns 环境常见):每条 -6 命令都报 address
// family not supported。装/拆/逃生口都必须照常走完 v4 的部分 —— 没有这条
// 豁免时,teardown 里排在前面的 -6 命令会让 v4 的 pref-120 rule 永远清不掉,
// 逃生口对着一台黑洞机器恒失败(2026-08-29 code review)。v6 缺席的内核上
// 无 v6 可堵,跳过不是 fail-open。
func TestLinuxBarrierSurvivesV6DisabledKernel(t *testing.T) {
	v6Down := func() *scriptedIPRunner {
		r := &scriptedIPRunner{fail: map[string]string{}}
		for _, plan := range [][]Command{
			func() []Command { a, _, c, _ := PlanBarrierLinux(linuxExecCtx()); return append(a, c...) }(),
			PlanLinuxBarrierCleanup(),
		} {
			for _, c := range plan {
				if commandIsIPv6(c) {
					r.fail[c.String()] = "RTNETLINK answers: Address family not supported by protocol"
				}
			}
		}
		return r
	}

	if err := NewBarrier(v6Down()).Install(context.Background(), linuxExecCtx()); err != nil {
		t.Fatalf("v6 缺席的内核上 Install 不该失败: %v", err)
	}
	if err := NewBarrier(v6Down()).Remove(context.Background(), linuxExecCtx()); err != nil {
		t.Fatalf("v6 缺席的内核上 Remove 不该失败: %v", err)
	}
	runner := v6Down()
	if err := RemoveBlockingBarrierRoutes(context.Background(), runner); err != nil {
		t.Fatalf("v6 缺席的内核上逃生口不该失败: %v", err)
	}
	// 豁免只对 -6:v4 命令上同样的措辞仍是真实失败。
	runner = &scriptedIPRunner{fail: map[string]string{
		"ip rule del pref 120 table 90": "RTNETLINK answers: Address family not supported by protocol",
	}}
	if err := RemoveBlockingBarrierRoutes(context.Background(), runner); err == nil {
		t.Fatal("v4 命令的失败被 v6 豁免吞掉了")
	}
}
