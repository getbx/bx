package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/guardian"
)

// ③b 起报告里有 Executed(上轮实际执行了什么)。渲染层按 Class 字面枚举、
// 新类静默消失 —— 这个仓库为这个形状栽过四次,这条测试在字段落地的同一批
// 就把渲染路径钉上,而不是等真机上「报告里有、面板上没有」。
func TestReconcileLineShowsLastExecutionOnlyWhenPresent(t *testing.T) {
	now := time.Now()
	round := guardian.ReconcileReport{At: now.Add(-time.Minute), UnchangedRounds: 2}
	if line := reconcileRoundSummary(round, now); strings.Contains(line, "上轮执行") {
		t.Fatalf("没执行过任何东西的健康机器必须静默: %q", line)
	}

	round.Executed = &guardian.ReconcileExecution{Action: "clear_orphan_barrier", Outcome: "ok"}
	line := reconcileRoundSummary(round, now)
	if !strings.Contains(line, "上轮执行 clear_orphan_barrier") || !strings.Contains(line, "成功") {
		t.Fatalf("执行成功要说出口: %q", line)
	}

	round.Executed = &guardian.ReconcileExecution{Action: "restore_dns", Outcome: "failed", Error: "networksetup: boom"}
	line = reconcileRoundSummary(round, now)
	if !strings.Contains(line, "失败") || !strings.Contains(line, "networksetup: boom") {
		t.Fatalf("失败原因是唯一可行动的线索,不许丢: %q", line)
	}

	// skipped 不是故障,是让路 —— 措辞不许长得像出了事。
	round.Executed = &guardian.ReconcileExecution{Action: "restore_dns", Outcome: "skipped", Error: "mutation_busy"}
	line = reconcileRoundSummary(round, now)
	if !strings.Contains(line, "让路") || !strings.Contains(line, "mutation_busy") {
		t.Fatalf("让路要与失败分得开: %q", line)
	}
	if strings.Contains(line, "失败") {
		t.Fatalf("让路不是失败: %q", line)
	}
}
