package cli

import (
	"strings"
	"testing"
)

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

// `bx doctor --json` 在 macOS 上的服务三条必须与人读版同源(Guardian),
// 绝不能落回 Core 的 plist / systemd 的 bx.service。
//
// 这个 bug 曾经真实存在于 --json 这条路径上(人读版早就修好了):后果不只是三行
// 难看,还有 `rep.OK = !rep.hasFail()` 让一台**健康的** mac 恒报 ok:false,以及
// doctorNextActions 把 "sudo bx setup <client-link>" 列进 next_actions——建议用户
// 去重跑一个已经跑过的 setup。菜单栏 app 曾经照抄这份错判据(Task 4 复审抓到)。
func TestDarwinServiceChecksAskGuardianNotCore(t *testing.T) {
	installed := darwinServiceChecks(true, true)
	if len(installed) != 3 {
		t.Fatalf("服务三条 = %d 条", len(installed))
	}
	for _, check := range installed {
		if check.Status != "ok" {
			t.Errorf("%s = %q,装好且活跃时不该有非 ok", check.Name, check.Status)
		}
		if strings.Contains(check.Detail, "bx.service") {
			t.Errorf("%s 的 detail 印了 systemd 的 bx.service:%q", check.Name, check.Detail)
		}
	}
	if installed[0].Detail != darwinGuardianServiceName {
		t.Errorf("service_installed 的 detail = %q,want %q", installed[0].Detail, darwinGuardianServiceName)
	}
	// 装好了就不该再提示去跑 setup —— 那正是 doctorNextActions 会转成 next_actions 的东西。
	if installed[0].Hint != "" {
		t.Errorf("装好时 service_installed 不该带 hint,实际 %q", installed[0].Hint)
	}

	missing := darwinServiceChecks(false, false)
	if missing[0].Status != "fail" || missing[0].Hint != "sudo bx setup <client-link>" {
		t.Errorf("没装时应 fail 并指引 setup,实际 %+v", missing[0])
	}
	// launchd 没有 enabled/active 的分离:装上即自启,故 enabled 由 installed 决定。
	if missing[2].Detail != "disabled" || installed[2].Detail != "enabled" {
		t.Errorf("service_enabled 必须由 installed 决定:%q / %q", missing[2].Detail, installed[2].Detail)
	}
}

// doctor 的服务三条在 macOS 上必须接到 **Guardian** 那个生产者。
//
// 这条测的是**接线**,不是生产者。两个生产者各自都有单测,而此前这段判断内联在
// collectClientDoctorWith 里,没有任何测试看着它:变异实测把 darwin 那一支关掉
// (`if false`),整套测试全绿 —— 一台健康的 mac 于是恒报 ok:false,并把
// "sudo bx setup <client-link>" 列进 next_actions。
func TestServiceDoctorChecksAskGuardianOnDarwin(t *testing.T) {
	guardian := []checkReport{{Name: "service_installed", Detail: "guardian"}}
	systemd := []checkReport{{Name: "service_installed", Detail: "systemd"}}
	produce := func(checks []checkReport) func() []checkReport {
		return func() []checkReport { return checks }
	}
	for _, tc := range []struct{ goos, want string }{
		{"darwin", "guardian"},
		{"linux", "systemd"},
		{"windows", "systemd"},
	} {
		got := serviceDoctorChecks(tc.goos, produce(guardian), produce(systemd))
		if len(got) != 1 || got[0].Detail != tc.want {
			t.Errorf("%s 接到了 %+v,want %q —— macOS 上 Core 不是 launchd 服务,"+
				"查 Core 的 plist / systemd 的 bx.service 必然三条 FAIL 而保护好得很", tc.goos, got, tc.want)
		}
	}
}
