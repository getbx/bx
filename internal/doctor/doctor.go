// internal/doctor/doctor.go
package doctor

import (
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/rulereview"
)

// Check 与 Report 的 JSON 形状是 `bx doctor --json` 的契约(agent / MCP 按名字取),
// 逐字段与迁移前的 cli.checkReport / cli.doctorReport 相同。
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

type Report struct {
	OK              bool    `json:"ok"`
	Kind            string  `json:"kind"`
	Version         string  `json:"version"`
	SecretsRedacted bool    `json:"secrets_redacted"`
	ChangesSystem   bool    `json:"changes_system"`
	ChangesNetwork  bool    `json:"changes_network"`
	RequiresRoot    bool    `json:"requires_root"`
	Checks          []Check `json:"checks"`
}

func (r *Report) AddCheck(name, status, detail, hint string) {
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Detail: detail, Hint: hint})
}

func (r *Report) AddReport(c Check) { r.Checks = append(r.Checks, c) }

func (r Report) HasFail() bool {
	for _, c := range r.Checks {
		if c.Status == "fail" {
			return true
		}
	}
	return false
}

// FileFact 是「读配置文件」这一步的事实。ReadErr 非空 = 没读到;PermissionDenied
// 单列是因为只有权限失败才走 Guardian 退路(文件不存在是「没 setup 过」,真问题)。
type FileFact struct {
	Bytes            []byte
	ReadErr          string
	PermissionDenied bool
	Mode0600         bool
}

// GuardianRulesFact 是 Guardian /v1/rules 退路拿到的东西:Review 为 nil 表示这一版
// Guardian 没发布体检;ConfigPath 是它读的文件,与要问的不是同一个就作废。
type GuardianRulesFact struct {
	Review     *rulereview.Report
	ConfigPath string
	Err        string
}

// DNS 状态的三个值与 guardian.DNSState 的常量逐字相同(跨包守卫钉在 internal/cli)。
// 本包不能 import guardian —— 它要被 guardian 调,成环。
const (
	DNSStateUnknown   = "unknown"
	DNSStateManaged   = "managed"
	DNSStateNotNeeded = "not_needed"
)

type DNSFact struct {
	State   string
	Managed bool
	Service string
}

type RecoveryFact struct {
	State     string
	Stage     string
	Attempt   int
	ErrorCode string
}

type GuardianFact struct {
	DNS      DNSFact
	Recovery RecoveryFact
}

// Facts 是判据的全部输入:每一项都是采到的事实或「没采到 + 原因」,没有一项是判断。
// **两份采集方**(internal/cli 与 internal/guardian)各自填它,喂进同一个 Judge。
type Facts struct {
	Version       string
	ConfigPath    string
	Config        FileFact
	Parsed        *config.Config
	ParseErr      string
	GuardianRules GuardianRulesFact
	RuleReview    *rulereview.Report
	Probe         *Check
	Service       []Check
	// StatusSocketErr 非空 = Core 控制 socket 没应答。
	StatusSocketErr string
	Darwin          bool
	// Guardian 为 nil = 没问到 ⇒ darwin 上走「保守退路」:DNS unknown、恢复 failed/unknown。
	Guardian *GuardianFact
	Platform []Check
}

// Judge 把事实折成报告。**check 的名字、顺序、措辞是 --json 契约**,与迁移前的
// collectClientDoctorWith 逐字节相同。这份等价此刻由本包自己的测试钉住;
// internal/cli 的 TestClientDoctorJSONReport 等**要等 Task 6 把
// collectClientDoctorWith 改走 Judge 之后才够得到这里**——今天它们只跑旧的
// collectClientDoctorWith,不经过本函数,别把它们当成已经在守这份等价。
func Judge(f Facts) Report {
	rep := Report{Kind: "client", Version: f.Version, SecretsRedacted: true}
	udpMode := "proxy"
	rep.AddCheck("config", "info", f.ConfigPath, "")
	if f.Config.ReadErr != "" {
		rulesErr := f.GuardianRules.Err
		if rulesErr == "" && !f.Config.PermissionDenied {
			rulesErr = "配置不是因为权限读不到,不走 Guardian 退路"
		}
		if rulesErr == "" && f.GuardianRules.ConfigPath != f.ConfigPath {
			rulesErr = fmt.Sprintf("Guardian 读的是 %s,与要问的 %s 不是同一个文件", f.GuardianRules.ConfigPath, f.ConfigPath)
		}
		if rulesErr == "" && f.GuardianRules.Review == nil {
			rulesErr = "这一版 Guardian 没有发布规则体检"
		}
		if rulesErr != "" {
			rep.AddCheck("config_readable", "fail", f.Config.ReadErr, "sudo bx setup <client-link>")
		} else {
			rep.AddCheck("config_readable", "info",
				f.Config.ReadErr+";规则已改经 Guardian 读取(业主授权,无需 root);"+
					"其余依赖配置的检查(权限/解析/server link/udp 策略)本次缺席,要它们请用 sudo", "")
			for _, l := range RuleReviewLines(*f.GuardianRules.Review) {
				rep.AddCheck(RuleReviewCheckName(l.Key), l.Status, l.Value, l.Hint)
			}
		}
	} else {
		rep.AddCheck("config_readable", "ok", "yes", "")
		if f.Config.Mode0600 {
			rep.AddCheck("config_permissions", "ok", "0600", "")
		} else {
			rep.AddCheck("config_permissions", "warn", "not 0600", "chmod 600 "+f.ConfigPath)
		}
		if f.ParseErr != "" || f.Parsed == nil {
			// f.Parsed == nil 而 f.ParseErr == "" 在 Task 6 的采集方那条路上不可达
			// (config.Parse 出错时总有非空 err.Error());留一个不为空的兜底纯粹是
			// 防御性的,不是 JSON 契约的一部分——它不会在正常调用里被触发。
			detail := f.ParseErr
			if detail == "" {
				detail = "config did not parse"
			}
			rep.AddCheck("config_parse", "fail", detail, "")
		} else {
			cfg := f.Parsed
			rep.AddCheck("config_parse", "ok", "yes", "")
			udpMode = cfg.UDP.Mode
			if cfg.Server == "" {
				rep.AddCheck("server_link", "fail", "empty", "sudo bx setup <client-link>")
			} else {
				rep.AddCheck("server_link", "ok", RedactLink(cfg.Server), "")
				if len(cfg.Transports) > 1 {
					rep.AddCheck("transports", "ok", fmt.Sprintf("%d 个传输(自动容灾)", len(cfg.Transports)), "")
				}
				if cfg.UDP.Transport != "" {
					rep.AddCheck("udp_transport", "ok", RedactLink(cfg.UDP.Transport), "")
				}
				if f.Probe != nil {
					rep.AddReport(*f.Probe)
				}
				if f.RuleReview != nil {
					for _, l := range RuleReviewLines(*f.RuleReview) {
						rep.AddCheck(RuleReviewCheckName(l.Key), l.Status, l.Value, l.Hint)
					}
				}
			}
		}
	}
	for _, c := range f.Service {
		rep.AddReport(c)
	}
	if f.StatusSocketErr != "" {
		rep.AddCheck("status_socket", "warn", f.StatusSocketErr, "bx logs")
	} else {
		rep.AddCheck("status_socket", "ok", "reachable", "")
	}
	status, detail, hint := UDPPolicy(udpMode)
	rep.AddCheck("udp_policy", status, detail, hint)
	if f.Darwin {
		g := f.Guardian
		if g == nil {
			// 与迁移前 guardianStatusFallback(darwin) 相同:Protection NeedsAttention 不进
			// doctor,进的只有 DNS(空 ⇒ unknown)与 Recovery(failed/unknown/recovery_unavailable)。
			g = &GuardianFact{Recovery: RecoveryFact{State: "failed", Stage: "unknown", ErrorCode: "recovery_unavailable"}}
		}
		rep.AddReport(DNSCheck(g.DNS))
		rep.AddReport(RecoveryCheck(g.Recovery))
	}
	for _, c := range f.Platform {
		rep.AddReport(c)
	}
	rep.OK = !rep.HasFail()
	return rep
}

func RedactLink(link string) string {
	switch {
	case strings.HasPrefix(link, "bx://"):
		return "bx://<redacted>"
	case strings.HasPrefix(link, "blink://"):
		return "blink://<legacy-redacted>"
	case strings.HasPrefix(link, "brook://"):
		return "internal-link:<redacted>"
	default:
		return "<redacted>"
	}
}

func UDPPolicy(mode string) (status, detail, hint string) {
	switch mode {
	case "proxy":
		return "ok", "non-DNS UDP relayed through bx tunnel", ""
	case "direct-realtime":
		return "warn", "non-DNS UDP direct; may expose real network path", "Use sudo bx realtime on to relay UDP through bx, or sudo bx realtime off to block it"
	default:
		return "warn", "non-DNS UDP blocked", "Google Meet/WebRTC may stutter; use sudo bx realtime on"
	}
}

func DNSCheck(d DNSFact) Check {
	state := d.State
	if state == "" {
		state = DNSStateUnknown
	}
	detail := fmt.Sprintf("state=%s managed=%t", state, d.Managed)
	if d.Service != "" {
		detail += " service=" + d.Service
	}
	if state == DNSStateManaged && d.Managed {
		return Check{Name: "guardian_dns", Status: "ok", Detail: detail}
	}
	// NotNeeded 是健康态(linux:数据面自己管,dns_managed 如实为 false)。
	if state == DNSStateNotNeeded {
		return Check{Name: "guardian_dns", Status: "ok", Detail: detail}
	}
	return Check{Name: "guardian_dns", Status: "fail", Detail: detail, Hint: "sudo bx up; bx logs"}
}

func RecoveryCheck(r RecoveryFact) Check {
	status := "ok"
	hint := ""
	switch r.State {
	case "accepted", "running":
		status = "info"
	case "failed":
		status = "warn"
		hint = "bx logs --json; bx reconnect (troubleshooting only)"
	}
	detail := fmt.Sprintf("state=%s stage=%s attempt=%d", r.State, r.Stage, r.Attempt)
	if r.ErrorCode != "" {
		detail += " error_code=" + r.ErrorCode
	}
	return Check{Name: "network_recovery", Status: status, Detail: detail, Hint: hint}
}
