package doctor

import (
	"fmt"
	"io/fs"
	"runtime"
	"strings"

	"github.com/getbx/bx/internal/tristate"

	"github.com/getbx/bx/internal/elevate"

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
	// OK 的含义是「**没有一条 check 是 fail**」,不是「全部查过且都好」。
	//
	// **这次(2026-09-12)刻意没有改它的含义**,理由不对称:
	//   - 让「有一项没查」也把 OK 打成 false,等于宣布一台用户自己 `bx down`
	//     的机器坏了 —— Core 没在跑时流量成败必然查不到,而那正是关闭态该有的
	//     样子。2026-09-10 真机验收上 guardian_dns 就栽过同一形状,不能再犯;
	//   - OK 是 `bx doctor --json` / `bx_inspect` 已在用的契约,悄悄改含义会
	//     让所有既有消费方对同一台机器换一个答案。
	//
	// 代价是 OK=true **不等于**「什么都查过了」。这个代价由 NotChecked 抵掉:
	// 它与 OK 并排发布,读的人(与 agent)在同一层就能看见「还有几项没查」。
	OK bool `json:"ok"`
	// NotChecked 是这份报告里状态为 not_checked 的条数。
	//
	// **它与 OK 并列,绝不合成一个数**(与 leakcheck 的 path/identity/surface
	// 三段计数同一条纪律):一个「异常数为 0」在「一条都没检查成」时同样成立,
	// 合起来那句话就永远是安全的假话。
	//
	// **刻意无 omitempty**:键缺席读作「这一版没说」,`"not_checked": 0` 读作
	// 「查全了」—— 两者是两句不同的话,压成同一个缺席就分不开了。
	NotChecked      int     `json:"not_checked"`
	Kind            string  `json:"kind"`
	Version         string  `json:"version"`
	SecretsRedacted bool    `json:"secrets_redacted"`
	ChangesSystem   bool    `json:"changes_system"`
	ChangesNetwork  bool    `json:"changes_network"`
	RequiresRoot    bool    `json:"requires_root"`
	Checks          []Check `json:"checks"`
}

func (r *Report) AddCheck(name, status, detail, hint string) {
	r.AddReport(Check{Name: name, Status: status, Detail: detail, Hint: hint})
}

// AddReport 是每一条 check 进报告的**唯一入口**(AddCheck 也走它)。
//
// **ok 的行不带 hint。** hint 是「该怎么办」,而一条通过的检查没有什么要办的 ——
// 2026-09-10 真机验收:`ok service_active` 底下挂着「→ sudo bx up」,CLI 文本路径
// 与菜单 Checks 页都是「hint 非空就画」,于是绿色的行在教人去修一件没坏的东西。
// 抹在这一个入口里而不是靠十几个产出点各自自觉 —— 漏一个不会有人发现。
func (r *Report) AddReport(c Check) {
	if c.Status == "ok" {
		c.Hint = ""
	}
	r.Checks = append(r.Checks, c)
}

func (r Report) HasFail() bool {
	for _, c := range r.Checks {
		if c.Status == "fail" {
			return true
		}
	}
	return false
}

// CountNotChecked 数「没查」的条数。**按状态数,不按名字数** —— 将来任何一条
// 结论学会说「没查」都会自动进这个数,不需要谁回来改一张名单。
func (r Report) CountNotChecked() int {
	n := 0
	for _, c := range r.Checks {
		if c.Status == StatusNotChecked {
			n++
		}
	}
	return n
}

// FileFact 是「读配置文件」这一步的事实。ReadErr 非空 = 没读到;PermissionDenied
// 单列是因为只有权限失败才走 Guardian 退路(文件不存在是「没 setup 过」,真问题)。
type FileFact struct {
	ReadErr          string
	PermissionDenied bool
	// Mode0600 三态:True=确实是 0600;False=不是;**Unknown=这个平台没有
	// POSIX 权限位**(Windows:NTFS 靠 ACL,Go 合成 0666)。
	//
	// 2026-09-15 真机实测:Windows 上它恒为「不是 0600」,于是一台完全正常的
	// 机器被报 WARN,还被派去敲 `chmod` —— 那条命令在那儿也不存在。与
	// 2026-09-10 `guardian_dns` 把「用户自己关掉保护」说成故障同一个形状。
	//
	// **零值取 Unknown 是刻意的**:漏填时说「没查」,而不是说「权限不对」。
	Mode0600 tristate.Tristate
}

// GuardianRulesFact 是 Guardian /v1/rules 退路拿到的东西:Review 为 nil 表示这一版
// Guardian 没发布体检;ConfigPath 是它读的文件,与要问的不是同一个就作废。
type GuardianRulesFact struct {
	Review     *rulereview.Report
	ConfigPath string
	Err        string
}

// DNS 状态的四个值与 guardian.DNSState 的常量逐字相同(跨包守卫钉在 internal/cli)。
// 本包不能 import guardian —— 它要被 guardian 调,成环。
const (
	DNSStateUnknown   = "unknown"
	DNSStateManaged   = "managed"
	DNSStateUnmanaged = "unmanaged"
	DNSStateNotNeeded = "not_needed"
)

// 用户意图的两个值与 guardian.DesiredState 逐字相同(同一条跨包守卫)。
// **判据需要它**:同一份 DNS 事实,在「用户要保护」与「用户刚把它关掉」之下
// 是两个相反的结论(见 DNSCheck)。
const (
	DesiredOn  = "on"
	DesiredOff = "off"
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
	// Desired 是用户的意图("on"/"off";空 = 这份事实没带意图,见 DNSCheck)。
	// 它不是 DNS 的事实,而是判 DNS 那条结论时**必须**知道的另一半。
	Desired string
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
	// Traffic 为 nil = **这条采集路径根本没问流量成败**(不是「问了、一切正常」)。
	// Judge 会为它产出一条 not_checked,绝不让缺席冒充健康 —— 见 TrafficFact。
	Traffic *TrafficFact
}

// Judge 把事实折成报告。**check 的名字、顺序、措辞是 --json 契约**,与迁移前的
// collectClientDoctorWith 逐字节相同。
//
// **这份等价今天由三层钉住**:本包自己的 Judge 测试(逐个阶梯的名字与状态)、
// internal/cli 的 TestClientDoctorJSONReport(它经 collectClientDoctorWith →
// 本函数,自 Task 6 起真的走到这里)、以及 TestJudgeGolden 那份逐字节 golden
// (testdata/judge_golden.json,长路径与权限退路各一份)。改判据必然让 golden
// 转红,那正是回来确认「--json 契约是不是真的要动」的时刻。
func Judge(f Facts) Report {
	rep := Report{Kind: "client", Version: f.Version, SecretsRedacted: true}
	udpMode := "proxy"
	rep.AddCheck("config", "info", f.ConfigPath, "")
	if f.Config.ReadErr != "" {
		rulesErr := f.GuardianRules.Err
		if rulesErr == "" && !f.Config.PermissionDenied {
			rulesErr = "the config is unreadable for some reason other than permissions, so the Guardian fallback does not apply"
		}
		if rulesErr == "" && f.GuardianRules.ConfigPath != f.ConfigPath {
			rulesErr = fmt.Sprintf("Guardian read %s, which is not the file being asked about (%s)", f.GuardianRules.ConfigPath, f.ConfigPath)
		}
		if rulesErr == "" && f.GuardianRules.Review == nil {
			rulesErr = "this version of Guardian does not publish the rule review"
		}
		if rulesErr != "" {
			rep.AddCheck("config_readable", "fail", f.Config.ReadErr, ""+elevate.Prefix+"bx setup <client-link>")
		} else {
			rep.AddCheck("config_readable", "info",
				f.Config.ReadErr+"; rules were read through Guardian instead (owner-authorized, no root needed); "+
					"the other config-dependent checks (permissions, parsing, server link, udp policy) are absent this round — run with sudo to get them", "")
			for _, l := range RuleReviewLines(*f.GuardianRules.Review) {
				rep.AddCheck(RuleReviewCheckName(l.Key), l.Status, l.Value, l.Hint)
			}
		}
	} else {
		rep.AddCheck("config_readable", "ok", "yes", "")
		switch f.Config.Mode0600 {
		case tristate.True:
			rep.AddCheck("config_permissions", "ok", "0600", "")
		case tristate.False:
			rep.AddCheck("config_permissions", "warn", "not 0600", "chmod 600 "+f.ConfigPath)
		default:
			// **「这个平台没有这件事」不是一次失败,也不是「查过没问题」。**
			rep.AddCheck("config_permissions", StatusNotChecked, "this platform has no POSIX permission bits (it uses ACLs)", "")
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
				rep.AddCheck("server_link", "fail", "empty", ""+elevate.Prefix+"bx setup <client-link>")
			} else {
				rep.AddCheck("server_link", "ok", RedactLink(cfg.Server), "")
				if len(cfg.Transports) > 1 {
					rep.AddCheck("transports", "ok", fmt.Sprintf("%d transports (automatic failover)", len(cfg.Transports)), "")
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
		rep.AddReport(DNSCheck(g.DNS, g.Desired))
		rep.AddReport(RecoveryCheck(g.Recovery))
	}
	// 流量成败排在平台检查**之前**:平台检查排最后是既有的不变量
	// (TestJudgeAppendsPlatformChecksLastAndComputesOK 钉着),而这一段的位置
	// 本身没有承重的理由 —— 页面按严重度重排,文本路径按顺序打。
	for _, c := range trafficChecks(f.Traffic) {
		rep.AddReport(c)
	}
	for _, c := range f.Platform {
		rep.AddReport(c)
	}
	rep.OK = !rep.HasFail()
	rep.NotChecked = rep.CountNotChecked()
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
		return "warn", "non-DNS UDP direct; may expose real network path", "Use " + elevate.Prefix + "bx realtime on to relay UDP through bx, or " + elevate.Prefix + "bx realtime off to block it"
	default:
		return "warn", "non-DNS UDP blocked", "Google Meet/WebRTC may stutter; use " + elevate.Prefix + "bx realtime on"
	}
}

// DNSCheck 判 DNS 归谁。**desired 是必需的输入,不是可选的上下文**:同一份
// 「DNS 不归 bx」的事实,在用户要保护时是故障,在用户刚把保护关掉时恰恰是正确
// 状态。2026-09-10 真机验收踩的就是这个 —— 关掉保护之后这条报 fail、hint 叫人
// `sudo bx up`,而新的 Checks 页把它顶在最上面写「1 failed」,一台完全正常的
// 机器被说成坏的(与 Tailscale advisory 当初那次同一个形状)。
func DNSCheck(d DNSFact, desired string) Check {
	state := d.State
	if state == "" {
		state = DNSStateUnknown
	}
	detail := fmt.Sprintf("state=%s managed=%t", state, d.Managed)
	if d.Service != "" {
		detail += " service=" + d.Service
	}
	// NotNeeded 是「本平台没有这件事」(linux:数据面自己管,dns_managed 如实为
	// false),与用户意图无关,先判。
	if state == DNSStateNotNeeded {
		return Check{Name: "guardian_dns", Status: "ok", Detail: detail}
	}
	if desired == DesiredOff {
		// 关着却仍占着 DNS 是**真的残留**(调谐环的 restore_dns 正是为它存在的),
		// 不许被这条豁免一起判成 ok。
		if d.Managed {
			return Check{
				Name: "guardian_dns", Status: "warn",
				Detail: detail + " — bx is off, but DNS still belongs to bx",
				Hint:   "" + elevate.Prefix + "bx down",
			}
		}
		return Check{Name: "guardian_dns", Status: "ok", Detail: detail + " — bx is off and DNS has been handed back to the system"}
	}
	if state == DNSStateManaged && d.Managed {
		return Check{Name: "guardian_dns", Status: "ok", Detail: detail}
	}
	// 意图问不出来(desired 为空)时按「要保护」判:这个字段只由 Guardian 填,
	// 而 Guardian 总是知道自己的 desired,空值只出现在旧版事实与测试里。那时
	// 宁可多报一次,也不能把「DNS 被别人接管了」漏掉 —— 两种错的代价不对称。
	return Check{Name: "guardian_dns", Status: "fail", Detail: detail, Hint: "" + elevate.Prefix + "bx up; bx logs"}
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

// ConfigMode0600 把一次文件权限观测折成三态。**两个采集方共用一份** ——
// `internal/cli` 与 `internal/guardian` 各写一遍的话,一处跟上、另一处没跟上
// 的失败方式是静默的(那一条 check 的状态悄悄换了一个平台的含义)。
//
// **Windows 一律 Unknown**:NTFS 靠 ACL,Go 在那儿把权限合成 0666 —— 拿它
// 判「不是 0600」就是把一台完全正常的机器说成有问题,还要派用户去敲一条
// 那儿不存在的 `chmod`(2026-09-15 真机实测)。这是纯函数,不做任何 I/O。
func ConfigMode0600(perm fs.FileMode) tristate.Tristate {
	if runtime.GOOS == "windows" {
		return tristate.Unknown
	}
	return tristate.FromBool(perm == 0o600)
}
