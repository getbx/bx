package leakcheck

import (
	"strings"
	"testing"
	"time"
)

func identityFinding(t *testing.T, id string, browser BrowserReport, local LocalFacts) Finding {
	t.Helper()
	for _, f := range Judge(fixedTime(), browser, local).Findings {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("报告里没有 %s", id)
	return Finding{}
}

// 内网地址这条走的是**已经采到、此前被丢掉**的数据:ICE gathering 里的 host
// candidate。2019 年之后的浏览器把它们换成了随机的 `<uuid>.local`,正因如此
// 原设计只留 srflx —— 但「有没有被换掉」本身就是一条关于这台机器的事实,
// 而没被换掉的那台机器,局域网结构对每一个网站可见。
func TestLocalAddressRule(t *testing.T) {
	for _, tc := range []struct {
		name        string
		browser     BrowserReport
		want        Verdict
		wantMention string
	}{
		{
			name:        "host candidate 全被 mDNS 遮掉",
			browser:     BrowserReport{HostCandidates: []string{"a1b2c3d4-0000-4000-8000-000000000000.local"}},
			want:        OK,
			wantMention: ".local",
		},
		{
			name:        "内网地址原样暴露",
			browser:     BrowserReport{HostCandidates: []string{"192.168.1.37"}},
			want:        Bad,
			wantMention: "192.168.1.37",
		},
		{
			name: "遮掉了一部分,但漏了一个",
			browser: BrowserReport{HostCandidates: []string{
				"a1b2c3d4-0000-4000-8000-000000000000.local", "10.0.0.9",
			}},
			want:        Bad,
			wantMention: "10.0.0.9",
		},
		{
			name:        "ICE 跑了但一个 host candidate 都没给",
			browser:     BrowserReport{SRFLX: []string{"203.0.113.9"}},
			want:        NotChecked,
			wantMention: "",
		},
		{
			// STUN 整个失败:host candidate 本不需要 STUN,但页面是在同一次
			// gathering 里采的,失败时两者一起没有。**「没拿到」不是「没暴露」。**
			name:        "ICE 整个失败",
			browser:     BrowserReport{STUNErr: "ICE gathering failed"},
			want:        NotChecked,
			wantMention: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := identityFinding(t, FindingLocalAddresses, tc.browser, LocalFacts{})
			if f.Verdict != tc.want {
				t.Fatalf("判定应为 %s,得到 %s(summary=%q)", tc.want, f.Verdict, f.Summary)
			}
			if tc.wantMention != "" &&
				!strings.Contains(f.Summary+strings.Join(f.Evidence, " "), tc.wantMention) {
				t.Errorf("结论必须出示依据 %q,summary=%q evidence=%v", tc.wantMention, f.Summary, f.Evidence)
			}
		})
	}
}

// 浏览器那一半从没到过时,这条与另外两条依赖浏览器的规则处置一致。
func TestLocalAddressesNotCheckedWhenBrowserNeverRan(t *testing.T) {
	f := identityFinding(t, FindingLocalAddresses, BrowserReport{}, LocalFacts{})
	if f.Verdict != NotChecked {
		t.Fatalf("判定 = %v,want NotChecked:%q", f.Verdict, f.Summary)
	}
}

// **分区不是装饰,它决定了哪一个数字会驱动界面上的告警。**
//
// 流量路径那一段 bx 负责,漏了就是坏的;身份那一段 bx 大多修不了,而且在一台
// 普通 Chrome 上几乎不可能全绿。两者合成一个总数时,那个数永远不为零 ——
// 于是它会被训练成噪声,连带把真正的泄漏一起淹掉(原设计风险二)。
func TestAnomalyCountCoversOnlyTheTrafficPath(t *testing.T) {
	rep := NewReport(time.Now(), Endpoints(), []Finding{
		{ID: "a", Section: SectionPath, Verdict: Bad},
		{ID: "b", Section: SectionIdentity, Verdict: Bad},
		{ID: "c", Section: SectionIdentity, Verdict: Bad},
		{ID: "d", Section: SectionPath, Verdict: OK},
	}, nil)

	if rep.AnomalyCount != 1 {
		t.Errorf("AnomalyCount = %d,want 1(只数流量路径那一段的 bad)", rep.AnomalyCount)
	}
	if rep.IdentityCount != 2 {
		t.Errorf("IdentityCount = %d,want 2", rep.IdentityCount)
	}
}

// **每一条结论都必须显式属于某一段。**
//
// 零值是 SectionPath,方向是刻意选的:漏填时一条身份类结论会被算进流量异常数,
// 那是**多报**;反过来(零值为身份段)则会让一条漏填的流量结论不计入告警,
// 那是**漏报**。两个方向的代价不对称,所以零值站在多报那边。
//
// 而这条守卫让「漏填」根本走不到那一步:新增一条结论时必须来这里登记它属于哪段。
func TestEverySectionIsDeclaredOnPurpose(t *testing.T) {
	want := map[string]Section{
		FindingCarrier:        SectionPath,
		FindingWebRTC:         SectionPath,
		FindingIPv6:           SectionPath,
		FindingDNS:            SectionPath,
		FindingLocalAddresses: SectionIdentity,
		FindingTimezone:       SectionIdentity,
		FindingRouteEscape:    SectionPath,
		FindingFingerprint:    SectionIdentity,
		FindingSurface:        SectionSurface,
	}
	report := Judge(fixedTime(), BrowserReport{}, LocalFacts{})
	if len(report.Findings) != len(want) {
		t.Fatalf("Judge 产出 %d 条结论,而这里登记了 %d 条 —— 新增结论必须来这里说明它属于哪一段",
			len(report.Findings), len(want))
	}
	for _, f := range report.Findings {
		expected, ok := want[f.ID]
		if !ok {
			t.Errorf("结论 %s 没有在这里登记分段", f.ID)
			continue
		}
		if f.Section != expected {
			t.Errorf("%s 的分段 = %v,want %v", f.ID, f.Section, expected)
		}
	}
}
