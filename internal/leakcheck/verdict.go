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
// 刻意不复用 internal/observe.Tristate:那个类型是**谓词**结果(True = 「是」),
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
)

func (v Verdict) String() string {
	switch v {
	case OK:
		return "ok"
	case Bad:
		return "bad"
	default:
		return "not checked"
	}
}

// MarshalJSON 让 JSON 里也是这三个词,而不是 0/1/2。页面直接显示这个串。
func (v Verdict) MarshalJSON() ([]byte, error) {
	return []byte(`"` + v.String() + `"`), nil
}

// Finding 是一条可展开看依据的结论。
//
// Evidence 是**必须**的那一半:一个不肯出示依据的检测工具,用户没有理由信它,
// 而且 bx 判错时用户看得出它是怎么错的。
type Finding struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Verdict  Verdict  `json:"verdict"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence,omitempty"`
}

// Report 是一次检测的全部产出。**没有任何字段是 BrowserReport 或 LocalFacts** ——
// 页面拿到的必须是成品结论,不是可判断的原料(见 Task 12 的守卫)。
type Report struct {
	GeneratedAt  time.Time          `json:"generated_at"`
	Endpoints    EndpointDisclosure `json:"endpoints"`
	Findings     []Finding          `json:"findings"`
	Evidence     []string           `json:"evidence,omitempty"`
	AnomalyCount int                `json:"anomaly_count"`
}

// NewReport 组装报告并算出异常数。**只数 Bad。**
func NewReport(now time.Time, endpoints EndpointDisclosure, findings []Finding, evidence []string) Report {
	anomalies := 0
	for _, f := range findings {
		if f.Verdict == Bad {
			anomalies++
		}
	}
	return Report{
		GeneratedAt:  now,
		Endpoints:    endpoints,
		Findings:     findings,
		Evidence:     evidence,
		AnomalyCount: anomalies,
	}
}
