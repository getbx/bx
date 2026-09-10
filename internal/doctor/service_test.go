package doctor

import "testing"

// 三行服务检查是 --json 契约的一部分(名字/状态/detail/hint),搬家不许改一个字。
func TestDarwinServiceChecksThreeRows(t *testing.T) {
	up := DarwinServiceChecks(true, true)
	if len(up) != 3 {
		t.Fatalf("要三行,实际 %d", len(up))
	}
	if up[0] != (Check{Name: "service_installed", Status: "ok", Detail: DarwinGuardianServiceName}) {
		t.Fatalf("installed = %+v", up[0])
	}
	if up[1] != (Check{Name: "service_active", Status: "ok", Detail: "active", Hint: "sudo bx up"}) {
		t.Fatalf("active = %+v", up[1])
	}
	if up[2] != (Check{Name: "service_enabled", Status: "ok", Detail: "enabled", Hint: "sudo bx up"}) {
		t.Fatalf("enabled = %+v", up[2])
	}
	down := DarwinServiceChecks(false, false)
	if down[0].Status != "fail" || down[0].Hint != "sudo bx setup <client-link>" {
		t.Fatalf("没装 = %+v", down[0])
	}
	if down[1].Status != "fail" || down[1].Detail != "inactive" || down[1].Hint != "sudo bx up; bx logs" {
		t.Fatalf("没跑 = %+v", down[1])
	}
	if down[2].Status != "fail" || down[2].Detail != "disabled" {
		t.Fatalf("没启用 = %+v", down[2])
	}
}
