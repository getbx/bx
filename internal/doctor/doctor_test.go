package doctor

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/tristate"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/rulereview"
)

func names(r Report) string {
	var out []string
	for _, c := range r.Checks {
		out = append(out, c.Name+":"+c.Status)
	}
	return strings.Join(out, " ")
}

func find(r Report, name string) Check {
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	return Check{}
}

// 配置不存在(不是权限):config_readable fail + setup 提示,udp_policy 按缺省 proxy。
func TestJudgeMissingConfig(t *testing.T) {
	r := Judge(Facts{
		Version: "v", ConfigPath: "/x/config.yaml",
		Config:  FileFact{ReadErr: "open /x/config.yaml: no such file or directory"},
		Service: []Check{{Name: "service_installed", Status: "fail"}},
	})
	if r.OK {
		t.Fatal("缺配置不该 ok")
	}
	if got := names(r); got != "config:info config_readable:fail service_installed:fail status_socket:ok udp_policy:ok traffic_outcomes:not_checked" {
		t.Fatalf("顺序/名字 = %q", got)
	}
	if c := find(r, "config_readable"); c.Hint != "sudo bx setup <client-link>" || !strings.Contains(c.Detail, "no such file") {
		t.Fatalf("config_readable = %+v", c)
	}
	if c := find(r, "udp_policy"); !strings.Contains(c.Detail, "relayed through bx tunnel") {
		t.Fatalf("udp_policy 缺省要按 proxy:%+v", c)
	}
}

// 权限读不到 + Guardian 退路成功:info + Guardian 算的体检行;Guardian 读的不是同一个文件则退路作废。
func TestJudgePermissionDeniedUsesGuardianReviewOnlyForTheSameFile(t *testing.T) {
	review := &rulereview.Report{}
	same := Judge(Facts{
		ConfigPath:    "/etc/bx/config.yaml",
		Config:        FileFact{ReadErr: "permission denied", PermissionDenied: true},
		GuardianRules: GuardianRulesFact{Review: review, ConfigPath: "/etc/bx/config.yaml"},
	})
	if c := find(same, "config_readable"); c.Status != "info" || !strings.Contains(c.Detail, "规则已改经 Guardian 读取") {
		t.Fatalf("同一文件的退路 = %+v", c)
	}
	other := Judge(Facts{
		ConfigPath:    "/tmp/other.yaml",
		Config:        FileFact{ReadErr: "permission denied", PermissionDenied: true},
		GuardianRules: GuardianRulesFact{Review: review, ConfigPath: "/etc/bx/config.yaml"},
	})
	if c := find(other, "config_readable"); c.Status != "fail" {
		t.Fatalf("Guardian 读的是别的文件,退路必须作废:%+v", c)
	}
	nilReview := Judge(Facts{
		ConfigPath:    "/etc/bx/config.yaml",
		Config:        FileFact{ReadErr: "permission denied", PermissionDenied: true},
		GuardianRules: GuardianRulesFact{Review: nil, ConfigPath: "/etc/bx/config.yaml"},
	})
	if c := find(nilReview, "config_readable"); c.Status != "fail" {
		t.Fatalf("Guardian 没发布体检时不许当成「都很健康」:%+v", c)
	}
}

// 读到且解析成功:权限、解析、server_link、transports、udp_transport、probe、体检行、udp_policy 按配置。
func TestJudgeParsedConfigProducesTheFullLadder(t *testing.T) {
	cfg := &config.Config{Server: "bx://abc", Transports: []string{"bx://abc", "bx://def"}}
	cfg.UDP.Mode = "direct-realtime"
	cfg.UDP.Transport = "hysteria2://x"
	probe := Check{Name: "probe", Status: "ok", Detail: "366ms"}
	r := Judge(Facts{
		ConfigPath: "/etc/bx/config.yaml",
		Config:     FileFact{Mode0600: tristate.False}, Parsed: cfg,
		RuleReview: &rulereview.Report{}, Probe: &probe,
		Service:         []Check{{Name: "guardian_installed", Status: "ok"}},
		StatusSocketErr: "dial unix /var/run/bx/core.sock: connect: no such file",
	})
	want := "config:info config_readable:ok config_permissions:warn config_parse:ok server_link:ok transports:ok udp_transport:ok probe:ok guardian_installed:ok status_socket:warn udp_policy:warn traffic_outcomes:not_checked"
	if got := names(r); got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	if c := find(r, "server_link"); c.Detail != "bx://<redacted>" {
		t.Fatalf("链接必须脱敏:%+v", c)
	}
	if c := find(r, "config_permissions"); c.Hint != "chmod 600 /etc/bx/config.yaml" {
		t.Fatalf("权限提示 = %+v", c)
	}
	if c := find(r, "status_socket"); c.Hint != "bx logs" {
		t.Fatalf("status_socket 提示 = %+v", c)
	}
	if c := find(r, "udp_policy"); !strings.Contains(c.Detail, "may expose real network path") {
		t.Fatalf("udp_policy 要按配置里的 direct-realtime:%+v", c)
	}
	if !r.OK {
		t.Fatal("没有 fail 就该 ok")
	}
}

func TestJudgeParseFailureAndEmptyServer(t *testing.T) {
	bad := Judge(Facts{Config: FileFact{Mode0600: tristate.True}, ParseErr: "yaml: boom"})
	if got := names(bad); got != "config:info config_readable:ok config_permissions:ok config_parse:fail status_socket:ok udp_policy:ok traffic_outcomes:not_checked" {
		t.Fatalf("解析失败阶梯 = %q", got)
	}
	empty := Judge(Facts{Config: FileFact{Mode0600: tristate.True}, Parsed: &config.Config{}})
	if c := find(empty, "server_link"); c.Status != "fail" || c.Hint != "sudo bx setup <client-link>" {
		t.Fatalf("server 为空 = %+v", c)
	}
}

// darwin:Guardian 没问到走退路(DNS unknown ⇒ fail;recovery failed/unknown/recovery_unavailable ⇒ warn)。
func TestJudgeDarwinGuardianChecksWithAndWithoutGuardian(t *testing.T) {
	noGuardian := Judge(Facts{Config: FileFact{ReadErr: "x"}, Darwin: true})
	if c := find(noGuardian, "guardian_dns"); c.Status != "fail" || c.Detail != "state=unknown managed=false" {
		t.Fatalf("没问到 Guardian 的 DNS 行 = %+v", c)
	}
	if c := find(noGuardian, "network_recovery"); c.Status != "warn" || !strings.Contains(c.Detail, "error_code=recovery_unavailable") {
		t.Fatalf("没问到 Guardian 的恢复行 = %+v", c)
	}
	healthy := Judge(Facts{
		Config: FileFact{ReadErr: "x"}, Darwin: true,
		Guardian: &GuardianFact{
			DNS:      DNSFact{State: DNSStateManaged, Managed: true, Service: "Wi-Fi"},
			Recovery: RecoveryFact{State: "idle", Stage: "idle"},
		},
	})
	if c := find(healthy, "guardian_dns"); c.Status != "ok" || c.Detail != "state=managed managed=true service=Wi-Fi" {
		t.Fatalf("健康 DNS 行 = %+v", c)
	}
	if c := find(healthy, "network_recovery"); c.Status != "ok" || c.Detail != "state=idle stage=idle attempt=0" {
		t.Fatalf("空闲恢复行 = %+v", c)
	}
	notNeeded := Judge(Facts{
		Config: FileFact{ReadErr: "x"}, Darwin: true,
		Guardian: &GuardianFact{DNS: DNSFact{State: DNSStateNotNeeded}},
	})
	if c := find(notNeeded, "guardian_dns"); c.Status != "ok" {
		t.Fatalf("NotNeeded 是健康态:%+v", c)
	}
	linux := Judge(Facts{Config: FileFact{ReadErr: "x"}, Darwin: false})
	if find(linux, "guardian_dns").Name != "" {
		t.Fatal("非 darwin 不产出 guardian_dns")
	}
}

func TestJudgeAppendsPlatformChecksLastAndComputesOK(t *testing.T) {
	r := Judge(Facts{Config: FileFact{ReadErr: "x"}, Platform: []Check{{Name: "terminal_proxy", Status: "ok"}}})
	if !strings.HasSuffix(names(r), " terminal_proxy:ok") {
		t.Fatalf("平台检查要排最后:%q", names(r))
	}
	if r.OK {
		t.Fatal("config_readable fail ⇒ ok=false")
	}
}

// **两条 RuleReviewLines 调用点都必须真的把行接进报告** —— 此前两条都只喂过
// rulereview.Report{}(零值,产出零行),删掉任何一处循环全部六条既有测试仍然
// 全绿。这里造一份**确定会产出至少一行**的报告(OverriddenCount=1 + 一条匹配
// 的 ClassOverriddenByOppositeKind finding),分别经 Facts.RuleReview(解析成功
// 那条路)与 Facts.GuardianRules.Review(权限退路那条路)喂进去,断言两边都能
// 看到 rule_rules_never_in_effect:warn。
func TestJudgeEmitsRuleReviewLinesOnBothPaths(t *testing.T) {
	rep := rulereview.Report{
		Findings: []rulereview.Finding{
			{Kind: "direct", Rule: "*.a.example", Class: rulereview.ClassOverriddenByOppositeKind, Summary: "s", CoveredBy: "*.b.example"},
		},
		OverriddenCount: 1,
	}
	wantName := RuleReviewCheckName("rules never in effect")

	parsed := Judge(Facts{
		ConfigPath: "/etc/bx/config.yaml",
		Config:     FileFact{Mode0600: tristate.True},
		Parsed:     &config.Config{Server: "bx://abc"},
		RuleReview: &rep,
	})
	if c := find(parsed, wantName); c.Status != "warn" {
		t.Fatalf("解析成功路径没能把 RuleReviewLines 的行接进报告:%+v(全部 checks: %s)", c, names(parsed))
	}

	guardianFallback := Judge(Facts{
		ConfigPath:    "/etc/bx/config.yaml",
		Config:        FileFact{ReadErr: "permission denied", PermissionDenied: true},
		GuardianRules: GuardianRulesFact{Review: &rep, ConfigPath: "/etc/bx/config.yaml"},
	})
	if c := find(guardianFallback, wantName); c.Status != "warn" {
		t.Fatalf("Guardian 退路没能把 RuleReviewLines 的行接进报告:%+v(全部 checks: %s)", c, names(guardianFallback))
	}
}

// **Guardian 退路必须要求「真的是权限问题」,不能只要求路径匹配 + Review 非
// nil。** `!f.Config.PermissionDenied` 这个条件此前没有单独的守卫:
// TestJudgeMissingConfig 的 PermissionDenied 是 false,但它的 Guardian 路径同时
// 不匹配,两个理由中的任何一个都能让退路作废,删掉 PermissionDenied 判断本身
// 测试也不会红。这里把「路径匹配 + Review 非 nil」都摆对,只翻转
// PermissionDenied 一个变量。
func TestJudgeGuardianFallbackRequiresAPermissionFailure(t *testing.T) {
	base := Facts{
		ConfigPath:    "/etc/bx/config.yaml",
		Config:        FileFact{ReadErr: "read-only filesystem"},
		GuardianRules: GuardianRulesFact{Review: &rulereview.Report{}, ConfigPath: "/etc/bx/config.yaml"},
	}

	notPermission := base
	notPermission.Config.PermissionDenied = false
	r := Judge(notPermission)
	if c := find(r, "config_readable"); c.Status != "fail" || c.Hint != "sudo bx setup <client-link>" {
		t.Fatalf("不是权限失败时,即使路径匹配、Review 非 nil,也不该走 Guardian 退路:%+v", c)
	}

	permission := base
	permission.Config.PermissionDenied = true
	r2 := Judge(permission)
	if c := find(r2, "config_readable"); c.Status != "info" {
		t.Fatalf("权限失败 + 路径匹配 + Review 非 nil 应该走 Guardian 退路:%+v", c)
	}
}

// —— NTFS 没有 POSIX 权限位,而 bx 在 Windows 上为此报了 WARN(2026-09-15 真机)——
//
// `030-SJWJ-GSR-B` 上实测:
//
//	[WARN] config permissions: not 0600
//	[HINT] config permissions: chmod 600 C:\ProgramData\bx\config.yaml
//
// 两件事都错:NTFS 靠 ACL,Go 在 Windows 上合成 0666,于是**一台完全正常的机器
// 被说成有问题**;而给出的动作是一条那儿不存在的命令。与 2026-09-10
// `guardian_dns` 把「用户自己关掉保护」说成故障是同一个形状。
//
// 三态因此是必需的:**「不是 0600」与「这个平台没有这件事」不是同一句话。**
func TestConfigPermissionsAreNotCheckedWherePOSIXModesDoNotExist(t *testing.T) {
	facts := Facts{ConfigPath: `C:\ProgramData\bx\config.yaml`}
	facts.Config.Mode0600 = tristate.Unknown

	rep := Judge(facts)
	var found *Check
	for i := range rep.Checks {
		if rep.Checks[i].Name == "config_permissions" {
			found = &rep.Checks[i]
		}
	}
	if found == nil {
		t.Fatal("config_permissions 整条不见了 —— 缺席与「查过没问题」在输出上一样")
	}
	if found.Status != StatusNotChecked {
		t.Errorf("status = %q,want %q —— 本平台没有 POSIX 权限位不是一次失败", found.Status, StatusNotChecked)
	}
	if strings.Contains(found.Hint, "chmod") {
		t.Errorf("还在教人敲 chmod:%q —— Windows 上没有这条命令", found.Hint)
	}
}

// **两头都不许被这次改动连累**:真的是 0600 仍然 ok,真的不是仍然 warn 并给
// 那条命令 —— 少了这一半,「干脆永远报 not_checked」也能廉价满足上一条。
func TestConfigPermissionsStillJudgeWhereModesDoExist(t *testing.T) {
	ok := Facts{ConfigPath: "/etc/bx/config.yaml"}
	ok.Config.Mode0600 = tristate.True
	bad := Facts{ConfigPath: "/etc/bx/config.yaml"}
	bad.Config.Mode0600 = tristate.False

	pick := func(rep Report) Check {
		for _, c := range rep.Checks {
			if c.Name == "config_permissions" {
				return c
			}
		}
		t.Fatal("config_permissions 不见了")
		return Check{}
	}
	if got := pick(Judge(ok)); got.Status != "ok" {
		t.Errorf("0600 被判成了 %q", got.Status)
	}
	got := pick(Judge(bad))
	if got.Status != "warn" {
		t.Errorf("不是 0600 被判成了 %q —— 配置里有服务器链接,权限松是真问题", got.Status)
	}
	if !strings.Contains(got.Hint, "chmod") {
		t.Errorf("不是 0600 却没给出那条命令:%q", got.Hint)
	}
}
