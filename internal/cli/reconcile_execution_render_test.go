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

	// Error 是失败码不是原始错误串(发布面纪律);可行动的线索是那个指路。
	round.Executed = &guardian.ReconcileExecution{Action: "restore_dns", Outcome: "failed", Error: "execute_failed"}
	line = reconcileRoundSummary(round, now)
	if !strings.Contains(line, "失败") || !strings.Contains(line, "execute_failed") {
		t.Fatalf("失败码要说出口: %q", line)
	}
	if !strings.Contains(line, "bx-guard.err.log") {
		t.Fatalf("完整原因在 Guardian 日志,不指路这个码就是死胡同: %q", line)
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

// DNSNotNeeded 的两个人面消费方(状态行标签 + doctor)—— 渲染层按字面枚举、
// 新加一类只会静默消失,这个仓库为它栽过四次;这一类落进 default 的后果是
// 「Status unavailable」+ doctor fail 教健康机器的用户去 sudo bx up
// (2026-08-29 code review 抓到)。
func TestDNSNotNeededRendersAsHealthyNotAsUnavailable(t *testing.T) {
	if got := guardianDNSLabel(guardian.DNSNotNeeded, ""); got != "Handled by bx (data plane)" {
		t.Fatalf("label = %q —— 「查了,无此事」不许说成「没查」", got)
	}
	check := guardianDNSDoctorCheck(guardian.Status{DNSState: guardian.DNSNotNeeded})
	if check.Status != "ok" {
		t.Fatalf("doctor 对健康态判了 %s(hint=%q)", check.Status, check.Hint)
	}
}
