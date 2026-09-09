package doctor

import (
	"strings"
	"testing"

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
	if got := names(r); got != "config:info config_readable:fail service_installed:fail status_socket:ok udp_policy:ok" {
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
		Config:     FileFact{Bytes: []byte("server: x"), Mode0600: false}, Parsed: cfg,
		RuleReview: &rulereview.Report{}, Probe: &probe,
		Service:         []Check{{Name: "guardian_installed", Status: "ok"}},
		StatusSocketErr: "dial unix /var/run/bx/core.sock: connect: no such file",
	})
	want := "config:info config_readable:ok config_permissions:warn config_parse:ok server_link:ok transports:ok udp_transport:ok probe:ok guardian_installed:ok status_socket:warn udp_policy:warn"
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
	bad := Judge(Facts{Config: FileFact{Bytes: []byte("x"), Mode0600: true}, ParseErr: "yaml: boom"})
	if got := names(bad); got != "config:info config_readable:ok config_permissions:ok config_parse:fail status_socket:ok udp_policy:ok" {
		t.Fatalf("解析失败阶梯 = %q", got)
	}
	empty := Judge(Facts{Config: FileFact{Bytes: []byte("x"), Mode0600: true}, Parsed: &config.Config{}})
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
