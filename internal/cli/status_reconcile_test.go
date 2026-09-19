package cli

import (
	"testing"
	"time"

	"github.com/getbx/bx/internal/guardian"
)

// 稳态那一轮**不占一行**;任何值得动作的东西都要让它重新开口。
//
// 真机原文(2026-09-18):
//
//	Loop  last observed 1m35s ago · no divergence (unchanged for 58 rounds) · scanned 1 Core process(es)
//
// 这一行每次都长一个样,用户读不出该做什么、也无事可做 —— 而每次都在的东西会被
// 训练成墙纸,然后把真正要紧的那一次一起淹掉(与当初否掉「Direct rules: N
// unreachable」常驻红字同一条判断)。
//
// **这条测试的两半缺一不可**:少了「安静时不说」缺陷原样回来;少了下面那张
// 「每一种都要重新开口」的表,一个「干脆永远不说」的实现也能全绿 —— 而那会把
// 发霉的报告、被栅栏挡住的轮次、双 Core 一起藏掉。
func TestReconcileLineIsSilentOnlyWhenThereIsNothingToAct(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	quiet := guardian.ReconcileReport{
		At:              now.Add(-90 * time.Second),
		UnchangedRounds: 58,
		CoreScan:        guardian.ReconcileCoreScan{Measured: true, Cores: 1},
	}
	if !reconcileRoundIsQuiet(quiet, now) {
		t.Fatal("一台健康机器的稳态轮次应当安静")
	}
	if _, ok := clientReconcileLine(clientStatusReport{Reconcile: &quiet}, now); ok {
		t.Fatal("稳态轮次不该占一行")
	}

	noisy := map[string]func(r *guardian.ReconcileReport){
		"报告发霉(循环可能停了)": func(r *guardian.ReconcileReport) { r.At = now.Add(-guardian.ReconcileStaleAfter - time.Minute) },
		"被栅栏挡住":        func(r *guardian.ReconcileReport) { r.Held = "core_ownership_uncertain" },
		"提议过动作":        func(r *guardian.ReconcileReport) { r.Actions = []string{"restore_dns"} },
		"真的执行过什么": func(r *guardian.ReconcileReport) {
			r.Executed = &guardian.ReconcileExecution{Action: "restore_dns", Outcome: "ok"}
		},
		"有项目没观测到": func(r *guardian.ReconcileReport) { r.Unobservable = []string{"capture_ok"} },
		"没数出 Core 进程数": func(r *guardian.ReconcileReport) {
			r.CoreScan = guardian.ReconcileCoreScan{Measured: false, Reason: "hidepid"}
		},
		"数到两个 Core": func(r *guardian.ReconcileReport) { r.CoreScan = guardian.ReconcileCoreScan{Measured: true, Cores: 2} },
		"这一轮与上一轮不同": func(r *guardian.ReconcileReport) { r.UnchangedRounds = 0 },
	}
	for name, mutate := range noisy {
		t.Run(name, func(t *testing.T) {
			round := quiet
			mutate(&round)
			if reconcileRoundIsQuiet(round, now) {
				t.Fatalf("%s 时必须重新开口", name)
			}
			if _, ok := clientReconcileLine(clientStatusReport{Reconcile: &round}, now); !ok {
				t.Fatalf("%s 时必须占一行", name)
			}
		})
	}
}
