// Package leakcheck 是泄漏检测的**纯判据**层:给定浏览器上报的那一半事实与本机
// 观测到的那一半,产出一组三态结论。
//
// 本包不做 I/O:没有 net/http、没有 os/exec、没有平台代码(由 purity_test.go 钉住)。
// 理由是这个功能唯一值钱的部分就是判据,而判据必须能被表驱动测试与变异验证覆盖;
// 一旦它与「起服务、开浏览器、跑命令」混在一起,就又变成只能靠人读的代码。
package leakcheck

import "time"

// Verdict 是一条结论的三态。**零值必须是 NotChecked。**
//
// 刻意不复用 internal/tristate.Tristate:那个类型是**谓词**结果(True = 「是」),
// 而这里的两极是「好」与「坏」,把「有泄漏」写成 True 会让每个调用点都要先想一下
// true 是好是坏。零值纪律两者相同,极性不同,所以是两个类型。
//
// 词汇与 apps/macos/BxMenu/Sources/BxMenu/MenuRows.swift 的 MenuRowMark 对齐
// (ok / bad / unknown),那里的 anomalyCount 也只数 bad。
type Verdict uint8

const (
	// NotChecked:没问出来。页面被关掉、STUN 被挡、没网、第三方超时,全在这一格。
	// **绝不因为「没看到泄漏」就升格成 OK。**
	NotChecked Verdict = iota
	OK
	Bad
	// Info:**没有正确答案**的观测。屏幕分辨率、显卡型号、UA —— 它们既不好也不坏,
	// 只是网站读得到。判成 ok 会说成「安全」,判成 bad 会说成「有问题」,
	// 而两者都是在给一件事强加一个它没有的极性。
	//
	// 它不进任何异常计数(NewReport 只数 Bad),所以加多少条都不会把告警稀释掉。
	Info
)

func (v Verdict) String() string {
	switch v {
	case OK:
		return "ok"
	case Bad:
		return "bad"
	case Info:
		return "info"
	default:
		return "not checked"
	}
}

// MarshalJSON 让 JSON 里也是这三个词,而不是 0/1/2。页面直接显示这个串。
func (v Verdict) MarshalJSON() ([]byte, error) {
	return []byte(`"` + v.String() + `"`), nil
}

// Section 是一条结论属于哪一段。**分区不是装饰,它决定了哪个数字会驱动告警。**
//
// 流量路径那一段 bx 负责,漏了就是坏的;身份那一段 bx 大多修不了,而且在一台普通
// Chrome 上几乎不可能全绿。两者合成一个总数时那个数永远不为零,于是会被训练成
// 噪声,连带把真正的泄漏一起淹掉 —— 正是原设计风险二。
//
// **零值是 SectionPath,方向是刻意选的。** 漏填时一条身份结论被算进流量异常数,
// 那是多报;反过来则会让漏填的流量结论不计入告警,那是漏报。代价不对称,零值
// 站在多报那边。
type Section uint8

const (
	// SectionPath:你的流量从哪儿出去。bx(或你正在用的那条隧道)负责。
	SectionPath Section = iota
	// SectionIdentity:你会不会被单独认出来。bx 大多修不了,但你有权知道。
	SectionIdentity
	// SectionSurface:网站读得到什么。**中性,不打勾** —— 这一段里的东西没有
	// 正确答案,列出来是为了让用户看见自己暴露了什么,而不是评判它。
	SectionSurface
)

func (s Section) String() string {
	switch s {
	case SectionIdentity:
		return "identity"
	case SectionSurface:
		return "surface"
	default:
		return "path"
	}
}

// MarshalJSON 让 JSON 里也是词而不是数字 —— 页面直接按它分区。
func (s Section) MarshalJSON() ([]byte, error) {
	return []byte(`"` + s.String() + `"`), nil
}

// Finding 是一条可展开看依据的结论。
//
// Evidence 是**必须**的那一半:一个不肯出示依据的检测工具,用户没有理由信它,
// 而且 bx 判错时用户看得出它是怎么错的。
type Finding struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Section  Section  `json:"section"`
	Verdict  Verdict  `json:"verdict"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence,omitempty"`
}

// Report 是一次检测的全部产出。**没有任何字段是 BrowserReport 或 LocalFacts** ——
// 页面拿到的必须是成品结论,不是可判断的原料(见 Task 12 的守卫)。
type Report struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Endpoints   EndpointDisclosure `json:"endpoints"`
	Findings    []Finding          `json:"findings"`
	Evidence    []string           `json:"evidence,omitempty"`
	// AnomalyCount 只数**流量路径**那一段的 bad。名字保持不变是因为 CLI 摘要
	// 已经在读它,而它的含义(「有几处真的漏了」)一个字没改。
	AnomalyCount int `json:"anomaly_count"`
	// IdentityCount 数身份段的 bad。**单独一个数,永远不并进上面那个。**
	IdentityCount int `json:"identity_count"`
}

// NewReport 组装报告并**按段**算出异常数。只数 Bad。
func NewReport(now time.Time, endpoints EndpointDisclosure, findings []Finding, evidence []string) Report {
	anomalies, identity := 0, 0
	for _, f := range findings {
		if f.Verdict != Bad {
			continue
		}
		if f.Section == SectionIdentity {
			identity++
		} else {
			anomalies++
		}
	}
	return Report{
		GeneratedAt:   now,
		Endpoints:     endpoints,
		Findings:      findings,
		Evidence:      evidence,
		AnomalyCount:  anomalies,
		IdentityCount: identity,
	}
}
