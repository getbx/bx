package mcp

import (
	"strings"
	"testing"
)

func hasFinding(fs []Finding, sev, substr string) bool {
	for _, f := range fs {
		if f.Severity == sev && strings.Contains(f.Title+f.Remediation, substr) {
			return true
		}
	}
	return false
}

func TestDiagnoseFindings(t *testing.T) {
	if fs := diagnoseFindings(StatusOut{}, false); len(fs) != 1 || fs[0].Severity != "error" || !strings.Contains(fs[0].Title, "未运行") {
		t.Fatalf("不可达应 1 条 error 未运行, got %+v", fs)
	}
	if fs := diagnoseFindings(StatusOut{TunnelHealthy: true}, true); len(fs) != 1 || fs[0].Severity != "info" {
		t.Fatalf("健康应 1 条 info, got %+v", fs)
	}
	if !hasFinding(diagnoseFindings(StatusOut{TunnelHealthy: false}, true), "error", "kill-switch") {
		t.Error("不健康应有 error kill-switch")
	}
	if !hasFinding(diagnoseFindings(StatusOut{TunnelHealthy: true, Restarts: 5}, true), "warn", "重连") {
		t.Error("Restarts=5 应有 warn 重连")
	}
	if !hasFinding(diagnoseFindings(StatusOut{TunnelHealthy: true, MutationState: "armed"}, true), "warn", "待确认") {
		t.Error("armed 应有 warn 待确认")
	}
	if got := diagnoseFindings(StatusOut{TunnelHealthy: false, MutationState: "armed"}, true); len(got) != 2 {
		t.Errorf("不健康+armed 应 2 条, got %d: %+v", len(got), got)
	}
}
