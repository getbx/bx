package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/guardian"
)

// 栅栏名是给日志与统计脚本用的稳定标识符,**不是给人读的**。
//
// 所有权不确定恰恰是那种「用户不主动撞就不知道自己卡住了」的状态:保护没开、
// Guardian 在正常应答、status 其余部分一切正常,只有这一行在说话。而出路此前
// 只挂在 500 响应上(guardianCodeHints 按失败码索引)—— 也就是说只有主动去
// sudo bx up 撞一次墙的人才看得到怎么脱身。
func TestStatusGivesANextStepForFencesAUserCanActOn(t *testing.T) {
	for _, tc := range []struct{ held, must string }{
		{"ownership_uncertain", "sudo bx up"},
		{"recovery_blocked", "sudo bx down"},
		{"intent_unreadable", "bx-guard"},
	} {
		got := reconcileRoundVerdict(guardian.ReconcileReport{Held: tc.held})
		if !strings.Contains(got, tc.held) {
			t.Errorf("%s:栅栏名本身要留着(日志与 status 得对得上), got %q", tc.held, got)
		}
		if !strings.Contains(got, tc.must) {
			t.Errorf("%s:没有给出下一步(缺 %q), got %q", tc.held, tc.must, got)
		}
	}
}

// **过渡态不给「下一步」**:它们自己会结束,催人动手只会让人去打断一件
// 正在正常进行的事。
func TestStatusGivesNoNextStepForFencesThatEndByThemselves(t *testing.T) {
	for _, held := range []string{"path_recovery_in_flight", "maintenance_hold"} {
		got := reconcileRoundVerdict(guardian.ReconcileReport{Held: held})
		if strings.Contains(got, "sudo") {
			t.Errorf("%s 是过渡态,不该催用户动手, got %q", held, got)
		}
	}
}

// 提示只在**被栅栏挡住**时出现 —— 一台健康机器的那一行不该多出任何噪声。
func TestHealthyRoundCarriesNoFenceHint(t *testing.T) {
	got := reconcileRoundVerdict(guardian.ReconcileReport{At: time.Now(), UnchangedRounds: 7})
	if strings.Contains(got, "sudo") || strings.Contains(got, "bx-guard") {
		t.Errorf("健康机器的那一行不该有排查提示, got %q", got)
	}
}
