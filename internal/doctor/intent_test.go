package doctor

import "github.com/getbx/bx/internal/tristate"

import "testing"

// **关掉保护不是故障。** 2026-09-10 真机验收:用户自己 `bx down` 之后,doctor 报
// `fail guardian_dns` 并附 hint「sudo bx up」——叫他去打开一件他刚刚亲手关掉的东西,
// 而新的 Checks 页把这条红字顶在最上面、合计句写「1 failed」。判据当时只看
// state/managed,没有「用户想不想要保护」这一项。
//
// 反过来那一半同样要判:关着却仍占着 DNS 是**真的残留**(调谐环的 restore_dns
// 就是为它存在的),不许因为这次修复被一起判成 ok。
func TestDNSCheckReadsTheUserIntent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fact     DNSFact
		desired  string
		status   string
		wantHint string
	}{
		{
			name:    "关着且 DNS 已还给系统 —— 这就是对的状态",
			fact:    DNSFact{State: DNSStateUnmanaged, Managed: false, Service: "Wi-Fi"},
			desired: DesiredOff,
			status:  "ok",
		},
		{
			name:     "关着却仍占着 DNS —— 残留,要说",
			fact:     DNSFact{State: DNSStateManaged, Managed: true, Service: "Wi-Fi"},
			desired:  DesiredOff,
			status:   "warn",
			wantHint: "sudo bx down",
		},
		{
			name:    "开着且 DNS 归 bx",
			fact:    DNSFact{State: DNSStateManaged, Managed: true, Service: "Wi-Fi"},
			desired: DesiredOn,
			status:  "ok",
		},
		{
			name:     "开着而 DNS 不归 bx —— 真故障",
			fact:     DNSFact{State: DNSStateUnmanaged, Managed: false, Service: "Wi-Fi"},
			desired:  DesiredOn,
			status:   "fail",
			wantHint: "sudo bx up; bx logs",
		},
		{
			// 意图问不出来时保持旧答案:这个字段只由 Guardian 填,而 Guardian 总是
			// 知道自己的 desired;空值只出现在旧版事实与测试里。那时宁可多报一次,
			// 也不能把「DNS 被别人接管了」这件事漏掉 —— 两种错的代价不对称。
			name:     "意图不知道 —— 仍按开着判,不因为不知道就说没事",
			fact:     DNSFact{State: DNSStateUnmanaged, Managed: false},
			desired:  "",
			status:   "fail",
			wantHint: "sudo bx up; bx logs",
		},
		{
			// NotNeeded 是「本平台没有这件事」(linux 数据面自己管),与意图无关。
			name:    "not_needed 与意图无关,始终 ok",
			fact:    DNSFact{State: DNSStateNotNeeded},
			desired: DesiredOff,
			status:  "ok",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DNSCheck(tc.fact, tc.desired)
			if got.Name != "guardian_dns" {
				t.Fatalf("名字 = %q", got.Name)
			}
			if got.Status != tc.status {
				t.Fatalf("status = %q, want %q(detail=%q)", got.Status, tc.status, got.Detail)
			}
			if got.Hint != tc.wantHint {
				t.Fatalf("hint = %q, want %q", got.Hint, tc.wantHint)
			}
		})
	}
}

// **一条通过的检查没有什么要办的。** 真机上 `ok service_active` 底下挂着
// 「→ sudo bx up」,读起来是在叫人去修一件没坏的东西;CLI 文本路径与菜单
// Checks 页都是「hint 非空就画」,所以两边一起显形。
//
// 抹在**唯一的入口**里,而不是靠十几个产出点各自自觉 —— 漏一个不会有人发现。
func TestOKChecksNeverCarryAHint(t *testing.T) {
	var rep Report
	rep.AddCheck("via_addcheck", "ok", "yes", "sudo bx up")
	rep.AddReport(Check{Name: "via_addreport", Status: "ok", Detail: "yes", Hint: "sudo bx up"})
	rep.AddCheck("warn_keeps_it", "warn", "hmm", "bx logs")
	for _, c := range rep.Checks {
		if c.Status == "ok" && c.Hint != "" {
			t.Fatalf("%s 是 ok 却带着 hint %q", c.Name, c.Hint)
		}
	}
	if rep.Checks[2].Hint != "bx logs" {
		t.Fatalf("非 ok 的行不该被抹掉 hint:%q", rep.Checks[2].Hint)
	}
}

// 同一条不变量打在**整份判决**上:凡是注入进来的 check(服务三行、平台检查、
// probe)都经同一个入口,所以哪一个产出点将来手滑塞了 hint 都会被这里挡下。
func TestJudgeEmitsNoHintOnAnyOKCheck(t *testing.T) {
	rep := Judge(Facts{
		Version:    "test",
		ConfigPath: "/etc/bx/config.yaml",
		Config:     FileFact{Mode0600: tristate.True},
		Probe:      &Check{Name: "probe", Status: "ok", Detail: "1ms", Hint: "sudo bx up"},
		Service:    DarwinServiceChecks(true, true),
		Darwin:     true,
		Guardian: &GuardianFact{
			DNS:     DNSFact{State: DNSStateManaged, Managed: true},
			Desired: DesiredOn,
		},
		Platform: []Check{{Name: "terminal_proxy", Status: "ok", Detail: "not set", Hint: "unset HTTPS_PROXY"}},
	})
	okSeen := 0
	for _, c := range rep.Checks {
		if c.Status != "ok" {
			continue
		}
		okSeen++
		if c.Hint != "" {
			t.Errorf("%s 是 ok 却带着 hint %q", c.Name, c.Hint)
		}
	}
	if okSeen < 5 {
		t.Fatalf("只看到 %d 条 ok —— 这份输入没有覆盖到该覆盖的产出点", okSeen)
	}
}
