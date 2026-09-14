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
	// SectionReach:这条路能不能到达目标站。**它不是安全问题** ——
	// path/identity 的坏消息是「你泄漏了」,这一段的坏消息是「你用不了」。
	// 两者合成一个数就是让一次连不上稀释掉真正的泄漏告警(spec §6.1)。
	SectionReach
)

func (s Section) String() string {
	switch s {
	case SectionIdentity:
		return "identity"
	case SectionSurface:
		return "surface"
	case SectionReach:
		return "reach"
	default:
		return "path"
	}
}

// MarshalJSON 让 JSON 里也是词而不是数字 —— 页面直接按它分区。
func (s Section) MarshalJSON() ([]byte, error) {
	return []byte(`"` + s.String() + `"`), nil
}

// ReachState 是一次可达性探测的五态。**零值必须是 ReachUndetermined。**
//
// 与 Verdict 分开是因为它们回答不同的问题:Verdict 是给界面的**极性**(好/坏/没查),
// ReachState 是**发生了什么**(到了/被拒/没问出来/到不了/被拦下来问不出来)。
// Refused 与 Unreachable 都映射成 Bad,但给用户的话完全不同 —— 一个是「换服务器」,
// 一个是「这条路不通」。
//
// **Challenged 与 Undetermined 是分开的两态,不合成一个(2026-09-14 review 加)。**
// 此前 CF 人机挑战与「认不出的状态码/重定向」共用同一个 `ReachUndetermined`,而
// `judgeReachTarget` 对这一态无条件说「这是 Cloudflare 的人机挑战,不是你的出口
// 有问题」——那句话只对 CF 那一支为真。一个真实的地区封禁页(HTML,不含 CF 那
// 几个特征串)会落进同一态,读到的却是一句主动否掉坏消息的话,而这个功能存在的
// 唯一理由就是回答「是不是你的出口有问题」,给反了答案比不给更糟。两态措辞相反:
// Challenged 能主动否掉那句坏消息(有真凭据——CF 的特征串),Undetermined 不能
// (认不出就是认不出,不猜)。
type ReachState uint8

const (
	// ReachUndetermined:没问出来,且**不知道拦住它的是什么**——认不出的状态码、
	// 3xx 重定向、连探测记录都没有,全在这一格。**绝不因为「没看到拒绝」就升格成
	// 可达,也绝不替它猜一个具体原因**(那正是 Challenged 与它分开的理由)。
	ReachUndetermined ReachState = iota
	ReachReachable
	ReachRefused
	ReachUnreachable
	// ReachChallenged:命中了 Cloudflare 人机挑战的特征(spec §3.1 步骤②)——
	// 这是**唯一**能主动告诉用户「不是你的出口有问题」的一态,因为判据手里
	// 有真凭据(那几个特征串),不是在猜。
	ReachChallenged
)

func (r ReachState) String() string {
	switch r {
	case ReachReachable:
		return "reachable"
	case ReachRefused:
		return "refused"
	case ReachUnreachable:
		return "unreachable"
	case ReachChallenged:
		return "challenged"
	default:
		return "undetermined"
	}
}

func (r ReachState) MarshalJSON() ([]byte, error) {
	return []byte(`"` + r.String() + `"`), nil
}

// ReachSummary 是第四段的计数。**五态各自一个数,绝不合成** ——
// 合成之后「一条都没查出来」与「查了、全可达」在屏幕上就一样了。
type ReachSummary struct {
	Reachable    int `json:"reachable"`
	Refused      int `json:"refused"`
	Undetermined int `json:"undetermined"`
	Unreachable  int `json:"unreachable"`
	// Challenged 与 Undetermined 分开数——两者的措辞相反(见 ReachState 的注释),
	// 合成一个数会让「查了、是人机挑战」与「查了、什么都认不出」在屏幕上一样。
	Challenged int `json:"challenged"`
}

// Finding 是一条可展开看依据的结论。
//
// Evidence 是**必须**的那一半:一个不肯出示依据的检测工具,用户没有理由信它,
// 而且 bx 判错时用户看得出它是怎么错的。
type Finding struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Section Section `json:"section"`
	Verdict Verdict `json:"verdict"`
	Summary string  `json:"summary"`
	// Reach 只在 Section == SectionReach 时有意义。**其余段一律忽略它** ——
	// ReachUndetermined 恰好是零值,不加这道门就会把每条 path 结论都算成
	// 「一次没问出来的可达性探测」。
	//
	// **刻意不带 omitempty**(2026-09-14 review 修正,brief 原文写反了):`uint8`
	// 的 omitempty 判的是 Go 零值、不走 MarshalJSON,而 `ReachUndetermined` 恰好
	// 是零值——加了 omitempty,这一段唯一必须说清楚的「没问出来」那个状态会从
	// JSON 里整个消失(实测:该 Finding 序列化后没有 `reach` 键)。与
	// `Status.Capabilities`、`ProbeReport.measured` 刻意不带 omitempty 同一条纪律。
	Reach    ReachState `json:"reach"`
	Evidence []string   `json:"evidence,omitempty"`
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
	// Reach 是第四段的五态计数。**与上面两个数并排,永不合并。**
	Reach ReachSummary `json:"reach"`
}

// NewReport 组装报告并**按段**算出异常数。只数 Bad。
func NewReport(now time.Time, endpoints EndpointDisclosure, findings []Finding, evidence []string) Report {
	anomalies, identity := 0, 0
	var reach ReachSummary
	for _, f := range findings {
		// **可达性先分流,且不看 Verdict** —— 它的四态自己就带极性,
		// 而把它塞进下面那个 else 分支正是这个任务要堵的洞。
		if f.Section == SectionReach {
			switch f.Reach {
			case ReachReachable:
				reach.Reachable++
			case ReachRefused:
				reach.Refused++
			case ReachUnreachable:
				reach.Unreachable++
			case ReachChallenged:
				reach.Challenged++
			default:
				reach.Undetermined++
			}
			continue
		}
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
		Reach:         reach,
	}
}
