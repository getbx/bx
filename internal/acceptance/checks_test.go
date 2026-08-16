package acceptance

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/stats"
)

// **零值必须是 Unknown。**
//
// 漏填一个 Verdict 的代价必须是「多报一个不确定」,而不是「多报一个通过」——
// 一份把没验证的事显示成通过的验收报告,比没有验收更糟。
func TestZeroVerdictIsUnknown(t *testing.T) {
	var zero Verdict
	if zero != Unknown {
		t.Fatalf("零值 = %v,必须是 Unknown", zero)
	}
	var c Check
	if c.Verdict != Unknown {
		t.Fatal("Check 的零值不是 Unknown")
	}
}

// **每一项都带自己的错误:一项问不出来绝不影响其余。**
// 这与 internal/observe「任一项观测失败即记 Unknown 并附原因,绝不中断其余项」同源。
func TestOneFailureDoesNotSinkTheRest(t *testing.T) {
	checks := Run(Facts{
		StatusErr:  errors.New("dial guardian.sock: connection refused"),
		Report:     stats.Report{Snapshot: stats.Snapshot{Direct: 10, Proxy: 20}},
		Servers:    guardian.ServerListResponse{Servers: []guardian.ServerEntry{{Name: "hk", Current: true}}},
		CoreUptime: time.Hour, ThroughputEntries: 2,
	})
	got := map[string]Verdict{}
	for _, c := range checks {
		got[c.Name] = c.Verdict
	}
	if got["Guardian 可达"] != Fail {
		t.Fatal("Guardian 不可达却没报 Fail")
	}
	if got["服务器清单"] != Pass {
		t.Errorf("Guardian 挂了连累了服务器清单:%v", got["服务器清单"])
	}
	if got["流量成败"] != Pass {
		t.Errorf("Guardian 挂了连累了流量检查:%v", got["流量成败"])
	}
}

// **能力键整个缺席 = 跑着的还是旧 Guardian**,这是升级最常见的失败方式:
// 文件换了、进程没换。它必须是 Fail,不是 Unknown。
func TestMissingCapabilitiesMeanTheOldBinaryIsStillRunning(t *testing.T) {
	checks := Run(Facts{Status: guardian.Status{}})
	for _, c := range checks {
		if c.Name != "能力声明" {
			continue
		}
		if c.Verdict != Fail {
			t.Fatalf("能力一个都没声明却判 %v", c.Verdict)
		}
		if !strings.Contains(c.Detail, "升级前") {
			t.Errorf("没说清这多半是没换进程:%q", c.Detail)
		}
		return
	}
	t.Fatal("没有能力声明这一条")
}

// 少一项能力也要 Fail,并**点名少了哪一项** —— 菜单会安静地少掉对应入口,
// 而用户以为这一版就没有这个功能。
func TestPartialCapabilitiesAreNamed(t *testing.T) {
	checks := Run(Facts{Status: guardian.Status{
		Capabilities: []string{guardian.CapabilityRules, guardian.CapabilityMaintenanceHold},
	}})
	for _, c := range checks {
		if c.Name != "能力声明" {
			continue
		}
		if c.Verdict != Fail {
			t.Fatalf("缺能力却判 %v", c.Verdict)
		}
		if !strings.Contains(c.Detail, guardian.CapabilityServers) {
			t.Errorf("没点名缺的那一项:%q", c.Detail)
		}
		return
	}
	t.Fatal("没有能力声明这一条")
}

// **刚启动的机器上,按构造答不出来的检查必须报 Unknown,不能报 Fail。**
// 报 Fail 是在骂一台正常的机器,而那会让人从此忽略这份报告。
func TestFreshlyStartedMachineIsNotBlamed(t *testing.T) {
	checks := Run(Facts{
		Status:            guardian.Status{Capabilities: RequiredCapabilities},
		CoreUptime:        2 * time.Minute,
		ThroughputEntries: 0,
	})
	for _, c := range checks {
		if c.Name == "吞吐历史" {
			if c.Verdict == Fail {
				t.Fatalf("才跑了两分钟就判 Fail:%q", c.Detail)
			}
			if !strings.Contains(c.Why, "才跑了") {
				t.Errorf("没说清是时候未到:%q", c.Why)
			}
			return
		}
	}
	t.Fatal("没有吞吐历史这一条")
}

// 跑够久了还是空的,才是真失败。少了这一条,上面那条可以靠「永远 Unknown」满足。
func TestLongUptimeWithEmptyHistoryIsAFailure(t *testing.T) {
	checks := Run(Facts{
		Status:            guardian.Status{Capabilities: RequiredCapabilities},
		CoreUptime:        6 * time.Hour,
		ThroughputEntries: 0,
	})
	for _, c := range checks {
		if c.Name == "吞吐历史" {
			if c.Verdict != Fail {
				t.Fatalf("跑了六小时还是空的却判 %v", c.Verdict)
			}
			return
		}
	}
	t.Fatal("没有吞吐历史这一条")
}

// **UDP 反事实计数是「新二进制真的在跑」的唯一行为证据。**
// 版本号只证明文件换了,这个证明进程换了。
func TestUDPCountingProvesTheNewBinaryIsLive(t *testing.T) {
	checks := Run(Facts{
		Status: guardian.Status{Capabilities: RequiredCapabilities},
		Report: stats.Report{Snapshot: stats.Snapshot{
			Proxy: 100,
			Rules: []stats.RuleOutcome{{Source: "udp_no_change", Attempts: 42}},
		}},
	})
	for _, c := range checks {
		if c.Name == "UDP 计数已生效" {
			if c.Verdict != Pass {
				t.Fatalf("有反事实桶却判 %v:%q", c.Verdict, detailOf(c))
			}
			return
		}
	}
	t.Fatal("没有这一条")
}

// **判据与 status / doctor 同源。** 三处各写一份阈值会对同一台机器说三种话。
func TestFailingRulesUseTheSharedJudgement(t *testing.T) {
	checks := Run(Facts{
		Status: guardian.Status{Capabilities: RequiredCapabilities},
		Report: stats.Report{Snapshot: stats.Snapshot{
			Direct: 1291, DirectFailed: 1289,
			Rules: []stats.RuleOutcome{
				{Source: "user_direct", Rule: "*.qq.com", Attempts: 1291, Failures: 1289},
			},
		}},
	})
	for _, c := range checks {
		if c.Name == "流量成败" {
			if c.Verdict != Fail || !strings.Contains(c.Detail, "*.qq.com") {
				t.Fatalf("没点名那条规则:%v %q", c.Verdict, c.Detail)
			}
			return
		}
	}
	t.Fatal("没有流量成败这一条")
}

// **只有 Fail 才非零退出。**
//
// 把「没问出来」变成非零退出码,会让人在一台正常但刚启动的机器上以为出了事,
// 而后就再也不跑这个工具了 —— 一个没人跑的验收器等于没有验收器。
func TestOnlyFailuresAffectTheExitCode(t *testing.T) {
	if got := ExitCode([]Check{{Verdict: Pass}, {Verdict: Unknown}}); got != 0 {
		t.Errorf("全是 Pass/Unknown 却退出 %d", got)
	}
	if got := ExitCode([]Check{{Verdict: Pass}, {Verdict: Fail}}); got != 1 {
		t.Errorf("有 Fail 却退出 %d", got)
	}
}

// **失败排最前。** 人打开这份报告十有八九是因为有东西不对,而不对的那条不该
// 排在第七行(与规则失败那一段同一条纪律)。
func TestRenderPutsFailuresFirst(t *testing.T) {
	out := Render([]Check{
		{Name: "甲", Verdict: Pass, Detail: "ok"},
		{Name: "乙", Verdict: Unknown, Why: "没问到"},
		{Name: "丙", Verdict: Fail, Detail: "坏了"},
	}, nil)
	fail := strings.Index(out, "丙")
	unknown := strings.Index(out, "乙")
	pass := strings.Index(out, "甲")
	if !(fail < unknown && unknown < pass) {
		t.Fatalf("顺序不对(fail=%d unknown=%d pass=%d):\n%s", fail, unknown, pass, out)
	}
}

// Unknown 那一行显示的是**为什么没问出来**,不是一句空的 detail ——
// 「UNKNOWN 吞吐历史」后面什么都没有,等于告诉用户「自己猜」。
func TestUnknownRowsExplainThemselves(t *testing.T) {
	out := Render([]Check{{Name: "乙", Verdict: Unknown, Why: "Guardian 不可达"}}, nil)
	if !strings.Contains(out, "Guardian 不可达") {
		t.Fatalf("没给出原因:\n%s", out)
	}
}

// **验不了的必须列出来,而且要写明它不是通过。**
// 省略会让人以为这份清单是完整的 —— 那是一份验收报告最坏的失败方式。
func TestNotCheckableItemsAreShownAndNotCountedAsPassing(t *testing.T) {
	items := NotCheckableReadOnly()
	if len(items) == 0 {
		t.Fatal("一条都没列 —— 那等于宣称这份清单是完整的")
	}
	out := Render(nil, items)
	if !strings.Contains(out, "不是通过") {
		t.Errorf("没写明这些不是通过:\n%s", out)
	}
	for _, item := range items {
		if !strings.Contains(out, item) {
			t.Errorf("漏了一项:%q", item)
		}
	}
}

// **采集这一半只读。** 项目所有者的机器是生产环境,而这个工具的全部前提就是
// 「跑它没有任何风险」。守卫钉的是语义:采集里不许出现任何写、任何 POST、
// 任何执行外部命令。读不懂现在的代码时响亮失败。
func TestCollectorNeverMutatesAnything(t *testing.T) {
	source, err := os.ReadFile("collect.go")
	if err != nil {
		t.Fatalf("读不到 collect.go:%v —— 守卫已经失效,先修守卫", err)
	}
	text := string(source)
	for _, banned := range []string{
		"os.WriteFile", "os.Create", "os.Remove", "os.Rename", "os.Mkdir", "os.Chmod",
		"http.MethodPost", "exec.Command", "ProbeServers", "SwitchServer", "AddServer",
	} {
		if strings.Contains(text, banned) {
			t.Errorf("采集里出现了 %q —— 这个工具的前提是跑它没有任何风险", banned)
		}
	}
	// 反面:它确实在读东西,否则上面那条可以靠「什么都不做」满足。
	for _, want := range []string{"client.Status(", "client.ListServers(", "os.ReadFile("} {
		if !strings.Contains(text, want) {
			t.Errorf("采集里没有 %s —— 守卫已经失效,先修守卫", want)
		}
	}
}

// **不许拿一个编出来的运行时长把真失败豁免掉。**
//
// coreUptime 问不出来时返回 0,而 0 必须表示「不知道」而不是「刚起来」——
// 反过来的话,任何一台问不出运行时长的机器都会永远豁免掉「跑了很久还是空的」
// 那条判断。
func TestUnknownUptimeDoesNotExcuseFailures(t *testing.T) {
	checks := Run(Facts{
		Status:            guardian.Status{Capabilities: RequiredCapabilities},
		CoreUptime:        0, // 问不出来
		ThroughputEntries: 0,
	})
	for _, c := range checks {
		if c.Name == "吞吐历史" {
			if c.Verdict != Fail {
				t.Fatalf("运行时长问不出来就把失败豁免了:%v %q", c.Verdict, detailOf(c))
			}
			return
		}
	}
	t.Fatal("没有吞吐历史这一条")
}

// **Core 没在跑的机器不许被判失败。**
//
// 吞吐要 Core 观测、调谐环记录 —— Core 不在的机器上按构造攒不出任何东西,
// 而「跑了这么久还是空的」是一句冤枉话。这份报告的全部价值在于它说的每一句
// 都作数;冤枉一次,人就再也不信它了。
//
// 2026-08-15 review 实测两种形状都会误判:socket 残留而 mtime 很旧(算出一个
// 很长的运行时长),以及 socket 根本不在(运行时长问不出来 → 按规矩不豁免)。
func TestCoreDownIsNotBlamedForAnEmptyThroughputHistory(t *testing.T) {
	for _, uptime := range []time.Duration{24 * time.Hour, 0} {
		checks := Run(Facts{
			Status:     guardian.Status{Capabilities: RequiredCapabilities},
			ReportErr:  errors.New("dial core.sock: no such file"),
			CoreUptime: uptime,
		})
		var found bool
		for _, c := range checks {
			if c.Name != "吞吐历史" {
				continue
			}
			found = true
			if c.Verdict == Fail {
				t.Errorf("uptime=%v:Core 没在跑却判 Fail(%q)—— 一句冤枉话", uptime, c.Detail)
			}
			if !strings.Contains(c.Why, "Core 没在跑") {
				t.Errorf("uptime=%v:没说清是 Core 不在:%q", uptime, c.Why)
			}
		}
		if !found {
			t.Fatal("没有吞吐历史这一条")
		}
	}
}
