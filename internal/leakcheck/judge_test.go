package leakcheck

import "testing"

// **设计风险四。** 空上报 + 空本机事实 = 什么都没问出来。三条结论必须全是
// not checked,一条 ok 都不许有,异常数必须是 0(not checked 不是异常)。
//
// 这份 fixture 是**生产环境造得出来的**:用户点了菜单、浏览器没打开、或者打开了
// 但用户直接关掉标签页,Wait 超时后送进 Judge 的就正是这个零值。本仓库栽过
// 「用生产造不出的输入喂测试」的跟头,这条刻意不是那样。
func TestEmptyBrowserReportYieldsNotChecked(t *testing.T) {
	rep := Judge(fixedTime(), BrowserReport{}, LocalFacts{})

	if len(rep.Findings) != 3 {
		t.Fatalf("结论条数应恒为 3(webrtc / ipv6 / dns),得到 %d", len(rep.Findings))
	}
	for _, f := range rep.Findings {
		if f.Verdict != NotChecked {
			t.Errorf("%s 在什么都没问出来时必须是 not checked,得到 %s(summary=%q)",
				f.ID, f.Verdict, f.Summary)
		}
		if f.Summary == "" {
			t.Errorf("%s 是 not checked 也必须说清为什么没问出来,summary 不能是空串", f.ID)
		}
	}
	if rep.AnomalyCount != 0 {
		t.Fatalf("全 not checked 时异常数必须是 0,得到 %d", rep.AnomalyCount)
	}
}

// 只有浏览器那一半缺席(本机事实齐全)时,依然不许升格成 ok。
// 「本地看着没问题」不是「没有泄漏」——组合规则的另一半根本没到。
func TestMissingBrowserHalfStillNotChecked(t *testing.T) {
	local := LocalFacts{
		DefaultRouteV4:  InterfaceRef{Name: "utun4", Display: "Unidentified VPN (utun4)"},
		DNSServers:      []string{"127.0.0.1"},
		DNSServerEgress: map[string]string{"127.0.0.1": "lo0"},
	}
	rep := Judge(fixedTime(), BrowserReport{}, local)
	for _, f := range rep.Findings {
		if f.ID == FindingDNS {
			continue // DNS 只用本机那一半,由 Task 6 单独覆盖
		}
		if f.Verdict != NotChecked {
			t.Errorf("%s 缺了浏览器那一半就必须是 not checked,得到 %s", f.ID, f.Verdict)
		}
	}
}

// 结论的 ID 与顺序是页面与 CLI 共同依赖的契约,固定次序好让输出可 diff。
func TestFindingIDsAndOrderAreStable(t *testing.T) {
	rep := Judge(fixedTime(), BrowserReport{}, LocalFacts{})
	want := []string{FindingWebRTC, FindingIPv6, FindingDNS}
	if len(rep.Findings) != len(want) {
		t.Fatalf("结论条数应为 %d,得到 %d", len(want), len(rep.Findings))
	}
	for i, id := range want {
		if rep.Findings[i].ID != id {
			t.Errorf("第 %d 条应是 %q,得到 %q", i, id, rep.Findings[i].ID)
		}
		if rep.Findings[i].Title == "" {
			t.Errorf("%s 必须有标题", id)
		}
	}
}

// 报告必须原样带出要联系的第三方,页面才能照原样显示(设计风险三)。
func TestJudgeCarriesEndpointDisclosure(t *testing.T) {
	rep := Judge(fixedTime(), BrowserReport{}, LocalFacts{})
	if rep.Endpoints != Endpoints() {
		t.Fatalf("报告必须带出 Endpoints(),得到 %+v", rep.Endpoints)
	}
}
