package mcp

import "context"

// Ops 是 MCP tools 依赖的操作 port。liveOps(Task 8)绑到现有 internal 包,
// 测试用 fakeOps。这样 tool handler 可纯逻辑测试,免 root。
type Ops interface {
	Capabilities() (CapabilitiesOut, error)
	Status() (StatusOut, error)
	Diagnose() (DiagnoseOut, error)
	Inspect(InspectIn) (JSONCommandOut, error)
	LeakCheck(LeakCheckIn) (JSONCommandOut, error)
	Observe(ObserveIn) (JSONCommandOut, error)
	// Protection 报告 Guardian 那一侧的**意图 / 事实 / 差异**:desired(用户
	// 要什么)、observed(系统实际是什么)、divergence(二者之差)、reconcile
	// (调谐环报告)、recovery、protection_state。
	//
	// **它刻意返回 JSONCommandOut 而不是一个结构体**:bx_status 当年手挑了
	// 六个字段,之后 Guardian 那半长出十几个键而投影没跟上 —— 漏掉的字段
	// 不会有任何东西报错,agent 只是安静地看不见。原样转发就没有这个病。
	Protection() (JSONCommandOut, error)
	// Apps 报告「哪个应用走哪条路」。**采样一小段**而不是拉一次:控制面
	// /v0/apps 的第一次调用只是订阅,采集从那一刻才开始,拉完就返回必然是
	// 空报告 —— 而空报告与「真的没有连接」在输出上完全一样。
	//
	// **它不带可执行路径**:那是一次记档在案的信息面扩大,今天的发布面只有
	// 菜单窗口一条(internal/appattr/publication_test.go)。agent 要回答的是
	// 「哪个应用走哪条路」,不是「它装在哪儿」。
	Apps(AppsIn) (JSONCommandOut, error)
	Explain(ExplainIn) (JSONCommandOut, error)
	Check(CheckIn) (CheckOut, error)
	Logs(LogsIn) (LogsOut, error)
	ApplyPolicy(PolicyApplyIn) (PolicyApplyOut, error)
	SetTransport(SetTransportIn) error
	Reconnect(context.Context) (reconnectOut, error)
	Rehijack() error
	Commit() error
	Rollback() error
}

type CapabilitiesOut struct {
	Platform   string   `json:"platform" jsonschema:"linux or darwin"`
	Transports []string `json:"transports" jsonschema:"supported transport schemes, e.g. brook,reality"`
	Installed  bool     `json:"installed" jsonschema:"whether bx is installed on this host"`
}

type StatusOut struct {
	TunnelHealthy bool   `json:"tunnel_healthy"`
	LatencyMS     int64  `json:"latency_ms"`
	Restarts      int    `json:"restarts"`
	Mode          string `json:"mode" jsonschema:"host or router"`
	UDPMode       string `json:"udp_mode"`
	MutationState string `json:"mutation_state,omitempty" jsonschema:"idle, armed, committed, or reverted"`
}

type Finding struct {
	Severity    string `json:"severity" jsonschema:"info|warn|error"`
	Title       string `json:"title"`
	Remediation string `json:"remediation,omitempty"`
}
type DiagnoseOut struct {
	Findings []Finding `json:"findings"`
}

type InspectIn struct {
	Probe     bool   `json:"probe,omitempty" jsonschema:"allow outbound link probe; default false"`
	SkipProbe bool   `json:"skip_probe,omitempty" jsonschema:"avoid outbound link probe"`
	Target    string `json:"target,omitempty" jsonschema:"optional probe target host:port"`
	Timeout   string `json:"timeout,omitempty" jsonschema:"optional probe timeout, e.g. 8s"`
}

// ExplainIn 问「现在向这个目标发一条连接会发生什么、为什么」。
type ExplainIn struct {
	Target string `json:"target" jsonschema:"domain, IP, or host:port to ask about, e.g. steamstatic.com or 198.18.0.7"`
}

type AppsIn struct {
	For string `json:"for,omitempty" jsonschema:"sampling window, e.g. 8s; the report cannot answer about traffic outside it"`
}

// LeakCheckIn 刻意**没有 browser 选项**:浏览器那半要人在屏幕前点一下才产生数据,
// 那从来就不适合由 agent 代劳。人用 `bx leakcheck`,它会开页面并把两半事实对起来。
//
// (这段话此前**没有空行**地贴在 ExplainIn 的文档注释上面,于是 godoc 把它整块
// 挂给了 ExplainIn —— 一个连 leak 都不沾的类型。)
type LeakCheckIn struct {
	Network        bool     `json:"network,omitempty" jsonschema:"send outbound IPv4/IPv6/DNS probes"`
	ExpectedIPs    []string `json:"expected_ips,omitempty" jsonschema:"acceptable proxy/VPS public IPs"`
	NetworkTimeout string   `json:"network_timeout,omitempty" jsonschema:"optional network probe timeout, e.g. 8s"`
}

type JSONCommandOut struct {
	OK              bool           `json:"ok"`
	Command         []string       `json:"command"`
	JSON            map[string]any `json:"json,omitempty"`
	Error           string         `json:"error,omitempty"`
	Hint            string         `json:"hint,omitempty"`
	TestSteps       []string       `json:"test_steps,omitempty"`
	Recommendations []string       `json:"recommendations,omitempty"`
}

type ObserveIn struct {
	Duration string `json:"duration,omitempty" jsonschema:"observation window, e.g. 30s"`
	Interval string `json:"interval,omitempty" jsonschema:"status sampling interval, e.g. 2s"`
	Scenario string `json:"scenario,omitempty" jsonschema:"general, video, or realtime"`
}

// CheckIn controls the optional verification portions of the safe check
// bundle. With zero values it only inspects bx and samples its local counters.
type CheckIn struct {
	Network     bool     `json:"network,omitempty" jsonschema:"perform opt-in outbound egress and DNS probes"`
	ExpectedIPs []string `json:"expected_ips,omitempty" jsonschema:"acceptable proxy/VPS public IPs for optional network checks"`
	Duration    string   `json:"duration,omitempty" jsonschema:"local observation window, default 15s"`
	Interval    string   `json:"interval,omitempty" jsonschema:"local observation sample interval"`
	Scenario    string   `json:"scenario,omitempty" jsonschema:"general, video, or realtime"`
}

type CheckOut struct {
	OK          bool            `json:"ok"`
	Risk        string          `json:"risk" jsonschema:"low, medium, or high"`
	Inspect     JSONCommandOut  `json:"inspect"`
	Observe     JSONCommandOut  `json:"observe"`
	Leak        *JSONCommandOut `json:"leak,omitempty"`
	NextActions []string        `json:"next_actions,omitempty"`
}

type LogsIn struct {
	Lines int    `json:"lines,omitempty" jsonschema:"how many trailing lines (default 100)"`
	Since string `json:"since,omitempty" jsonschema:"optional time filter, e.g. 10m"`
}
type LogsOut struct {
	OK    bool   `json:"ok"`
	Text  string `json:"text"`
	Error string `json:"error,omitempty"`
	Hint  string `json:"hint,omitempty"`
}

type SetTransportIn struct {
	Link string `json:"link" jsonschema:"new server link to switch transport to"`
}

type PolicyApplyIn struct {
	Mode      string   `json:"mode" jsonschema:"direct or proxy"`
	Add       []string `json:"add,omitempty" jsonschema:"domains to add to the selected mode"`
	Remove    []string `json:"remove,omitempty" jsonschema:"domains to remove from the selected mode"`
	AllowRisk bool     `json:"allow_risk,omitempty" jsonschema:"explicitly permit a risky direct domain"`
}

type PolicyApplyOut struct {
	Changed  bool     `json:"changed"`
	State    string   `json:"state" jsonschema:"unchanged, reloaded, or pending_start"`
	Warnings []string `json:"warnings,omitempty"`
}

// Legacy internal request/result types are retained while liveOps still owns
// their former implementation. They are intentionally absent from Ops and no
// longer registered as MCP tools.
type PlanIn struct {
	Link string `json:"link,omitempty"`
}
type PlanOut struct {
	Steps []string `json:"steps"`
}
type VerifyOut struct {
	Pass bool `json:"pass"`
}
type SetupIn struct {
	Link string `json:"link"`
}
