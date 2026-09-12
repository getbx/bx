package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/rulereview"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/tristate"
)

// goldenPath 是那份逐字节契约。**它不是「多一条测试」,它是 spec §7 点名的那条**:
// `bx doctor --json` 是 MCP 与脚本在用的契约,而这次重构把判据整个搬了家 ——
// 逐个断言名字与状态的测试挡得住「某一行没了」,挡不住「某个 detail 少了一个字」
// 或者「hint 的 omitempty 掉了」。golden 是唯一会因为后者转红的东西。
const goldenPath = "testdata/judge_golden.json"

// goldenCase 是 golden 文件里的一项。四条路径**都要在**:
//
//   - long:配置读到了、解析成功、有 server / 多传输 / UDP 传输 / 探测 / 规则体检 /
//     服务检查 / socket 报错 / darwin 的 Guardian 两行 / 平台检查 —— 走满整条阶梯;
//
//   - permission_fallback:配置因权限读不到、改经 Guardian 的 /v1/rules 拿体检 ——
//     那条退路的措辞(那句很长的 config_readable info)与规则体检行同样是契约,
//     而它在 long 那条路上一个字都不会出现。
//
//   - desired_off:用户自己关掉了保护 —— DNS 已还给系统、Core socket 没了、
//     流量成败**问不到**。这一份钉住的是「关掉保护会不会被说成坏了」;
//
//   - failing_rules:2026-08-13 那个签名(`*.qq.com` 1291 条失败 1289)+ 直连
//     出不去。它钉住多条规则合并成**恰好一条** check、以及那句改口的 hint。
//
// 少了 permission_fallback,一个把退路措辞改掉的改动照样全绿;少了
// failing_rules,这次修复(把流量判据搬进 Judge)可以整个被撤掉而 golden 不动
// —— 它是唯一一份 traffic 真的查出了东西的报告。
type goldenCase struct {
	Name   string `json:"name"`
	Report Report `json:"report"`
}

func goldenRuleReview() rulereview.Report {
	return rulereview.Report{
		Findings: []rulereview.Finding{
			{Kind: "direct", Rule: "*.a.example", Class: rulereview.ClassOverriddenByOppositeKind, Summary: "被更宽的 proxy 规则压住", CoveredBy: "*.example"},
			{Kind: "direct", Rule: "*.aliyuncs.com", Class: rulereview.ClassRisky, Summary: "公有云开放子域,直连会把你单独认出来"},
		},
		OverriddenCount:    1,
		DeadChecked:        false,
		DeadSkipReason:     "Core 没在跑,拿不到累计判定数",
		BuiltinListChecked: false,
		BuiltinSkipReason:  "global 模式下 china 列表整个不生效",
	}
}

func goldenCases() []goldenCase {
	review := goldenRuleReview()

	cfg := &config.Config{Server: "bx://longpath", Transports: []string{"bx://longpath", "vless://backup"}}
	cfg.UDP.Mode = "direct-realtime"
	cfg.UDP.Transport = "hysteria2://udp"

	long := Facts{
		Version:    "test",
		ConfigPath: "/etc/bx/config.yaml",
		Config:     FileFact{Mode0600: true},
		Parsed:     cfg,
		RuleReview: &review,
		Probe:      &Check{Name: "probe", Status: "ok", Detail: "366ms"},
		Service: []Check{
			{Name: "service_installed", Status: "ok", Detail: "com.getbx.bx.guard"},
			{Name: "service_active", Status: "ok", Detail: "active"},
		},
		StatusSocketErr: "dial unix /var/run/bx/core.sock: connect: no such file or directory",
		Darwin:          true,
		Guardian: &GuardianFact{
			DNS:      DNSFact{State: DNSStateManaged, Managed: true, Service: "Wi-Fi"},
			Recovery: RecoveryFact{State: "idle", Stage: "idle"},
		},
		Platform: []Check{{Name: "terminal_proxy", Status: "ok", Detail: "none"}},
		// 流量查过了、一切正常:那一行的数字也是契约(菜单 Checks 页与文本
		// 路径都照它渲染)。
		Traffic: &TrafficFact{Report: stats.Report{
			ConfigPath: "/etc/bx/config.yaml",
			Snapshot:   stats.Snapshot{Direct: 45, DirectFailed: 0, Proxy: 300, ProxyFailed: 2},
		}},
	}

	fallback := Facts{
		Version:    "test",
		ConfigPath: "/etc/bx/config.yaml",
		Config:     FileFact{ReadErr: "open /etc/bx/config.yaml: permission denied", PermissionDenied: true},
		GuardianRules: GuardianRulesFact{
			Review:     &review,
			ConfigPath: "/etc/bx/config.yaml",
		},
		Service:  []Check{{Name: "service_installed", Status: "ok", Detail: "com.getbx.bx.guard"}},
		Darwin:   true,
		Platform: []Check{{Name: "terminal_proxy", Status: "ok", Detail: "none"}},
	}

	// off 是 2026-09-10 真机验收暴露的那一态:用户自己关掉保护。DNS 还给系统、
	// Core socket 没了、探测没做成 —— 三件都不是故障,而修复前这里会报一条
	// `fail guardian_dns` 并叫他 `sudo bx up`。把它钉进 golden,是为了让「关掉
	// 保护会不会被说成坏了」以后每次都被逐字节问一遍。
	off := Facts{
		Version:    "test",
		ConfigPath: "/etc/bx/config.yaml",
		Config:     FileFact{Mode0600: true},
		Parsed:     cfg,
		Probe: &Check{
			Name: "probe", Status: "warn",
			Detail: "tcp 203.0.113.10:443 not probed: dial unix /var/run/bx/core.sock: connect: no such file or directory",
		},
		Service:         DarwinServiceChecks(true, true),
		StatusSocketErr: "dial unix /var/run/bx/core.sock: connect: no such file or directory",
		Darwin:          true,
		Guardian: &GuardianFact{
			DNS:      DNSFact{State: DNSStateUnmanaged, Managed: false, Service: "Wi-Fi"},
			Recovery: RecoveryFact{State: "ignored", Stage: "off"},
			Desired:  DesiredOff,
		},
		Platform: []Check{{Name: "terminal_proxy", Status: "info", Detail: "not set"}},
		// Core 没在跑 ⇒ 流量成败**问不到**。这一行是这次修复的要点:它必须是
		// not_checked 而不是缺席,也不是 ok —— 关闭态下「没查」是正常的,但
		// 「没查」与「查了、没问题」在用户眼里必须分得开。
		Traffic: &TrafficFact{Err: "dial unix /var/run/bx/core.sock: connect: no such file or directory"},
	}

	// failing_rules 钉住 2026-08-13 那个签名的**渲染**:多条规则合并成恰好一条
	// check(按名字取的消费方不会丢结论),而直连出不去时那句 hint 改口说
	// 「不是你的规则」—— 这两件事此前只长在文本路径上,升级到菜单 Checks 页
	// 之后整类消失,正是这次要修的缺陷。
	failing := Facts{
		Version:    "test",
		ConfigPath: "/etc/bx/config.yaml",
		Config:     FileFact{Mode0600: true},
		Parsed:     cfg,
		Traffic: &TrafficFact{
			Report: stats.Report{
				ConfigPath: "/etc/bx/config.yaml",
				Snapshot: stats.Snapshot{
					Direct: 1322, DirectFailed: 1308,
					Rules: []stats.RuleOutcome{
						{Source: "user_direct", Rule: "*.qq.com", Attempts: 1291, Failures: 1289},
						{Source: "user_direct", Rule: "*.icloud.com", Attempts: 6, Failures: 6},
					},
				},
			},
			DirectEgress: tristate.False,
		},
	}

	return []goldenCase{
		{Name: "long", Report: Judge(long)},
		{Name: "permission_fallback", Report: Judge(fallback)},
		{Name: "desired_off", Report: Judge(off)},
		{Name: "failing_rules", Report: Judge(failing)},
	}
}

// TestJudgeGolden 把四条固定 Facts 的判决逐字节钉住。
//
// 重新生成:UPDATE_GOLDEN=1 go test ./internal/doctor/ -run TestJudgeGolden
// —— 只有在**确实要改 --json 契约**时才跑它,并把 diff 一起提交上去。
func TestJudgeGolden(t *testing.T) {
	got, err := json.MarshalIndent(goldenCases(), "", "  ")
	if err != nil {
		t.Fatalf("序列化判决失败: %v", err)
	}
	got = append(got, '\n')

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("建 testdata 目录失败: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("写 golden 失败: %v", err)
		}
		t.Logf("已重写 %s(%d 字节)", goldenPath, len(got))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("读不到 %s —— 用 UPDATE_GOLDEN=1 go test ./internal/doctor/ -run TestJudgeGolden 生成: %v", goldenPath, err)
	}
	if string(got) == string(want) {
		return
	}
	// 逐行报第一处差异:整份贴出来在 --json 契约这种长报告上读不出所以然。
	gotLines, wantLines := splitLines(string(got)), splitLines(string(want))
	for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
		g, w := "", ""
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			t.Fatalf("判决与 %s 第 %d 行起不同 —— `bx doctor --json` 是 MCP 与脚本在用的契约,\n"+
				"这一行变了就是契约变了。确实要改就跑 UPDATE_GOLDEN=1 go test ./internal/doctor/ -run TestJudgeGolden。\n"+
				" got: %s\nwant: %s", goldenPath, i+1, g, w)
		}
	}
	t.Fatalf("判决与 %s 不同但逐行比不出来(长度 %d vs %d)", goldenPath, len(got), len(want))
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
