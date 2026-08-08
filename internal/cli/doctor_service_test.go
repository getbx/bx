package cli

import "testing"

// macOS 上 doctor 必须报告 Guardian,而不是 Core 的 legacy LaunchDaemon。
//
// 真机 2026-08-06:bx 保护正常运行(status socket reachable、隧道健康、观测层
// divergence 为空),doctor 却给出三条 FAIL:
//
//	[FAIL] service installed: bx.service
//	[FAIL] service active: inactive
//	[FAIL] service enabled: disabled
//
// 原因有二:① install.UnitInstalled() 在 darwin 上查的是
// /Library/LaunchDaemons/com.getbx.bx.plist(Core 的 legacy plist),而统一布局下
// Core 根本不是 launchd 服务、生命周期归 Guardian,实际存在的是
// com.getbx.bx.guard.plist;② 显示的名字是 install.ServiceName = "bx.service",
// 那是 systemd 的名字。用户和 agent 据此会以为服务坏了,而它好得很。
func TestDarwinDoctorServiceLinesReportGuardian(t *testing.T) {
	lines := darwinServiceDoctorLines(true, true)
	if len(lines) == 0 {
		t.Fatal("必须产出服务相关的 doctor 行")
	}
	if lines[0].Value != darwinGuardianServiceName {
		t.Errorf("服务名 = %q, want %q——不得在 macOS 上印 systemd 的 bx.service", lines[0].Value, darwinGuardianServiceName)
	}
	for _, line := range lines {
		if line.Status == "fail" {
			t.Errorf("Guardian 已安装且活跃时不得有 FAIL,实际 = %+v", line)
		}
		if line.Key == "logs" {
			t.Errorf("一切正常时不该提示看日志,实际 = %+v", line)
		}
	}
}

// Guardian 确实没装/没跑时,仍须如实报 FAIL 并给出看日志的指引。
func TestDarwinDoctorServiceLinesStillFailWhenGuardianAbsent(t *testing.T) {
	lines := darwinServiceDoctorLines(false, false)
	var failures, hints int
	for _, line := range lines {
		if line.Status == "fail" {
			failures++
		}
		if line.Key == "logs" {
			hints++
		}
	}
	if failures == 0 {
		t.Errorf("Guardian 缺失时必须报 FAIL,实际 = %+v", lines)
	}
	if hints == 0 {
		t.Errorf("不活跃时必须给看日志的指引,实际 = %+v", lines)
	}
}
