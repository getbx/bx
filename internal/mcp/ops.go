package mcp

// Ops 是 MCP tools 依赖的操作 port。liveOps(Task 8)绑到现有 internal 包,
// 测试用 fakeOps。这样 tool handler 可纯逻辑测试,免 root。
type Ops interface {
	Capabilities() (CapabilitiesOut, error)
	Status() (StatusOut, error)
	Diagnose() (DiagnoseOut, error)
	Inspect(InspectIn) (JSONCommandOut, error)
	LeakCheck(LeakCheckIn) (JSONCommandOut, error)
	Observe(ObserveIn) (JSONCommandOut, error)
	Logs(LogsIn) (LogsOut, error)
	Plan(PlanIn) (PlanOut, error)
	Verify() (VerifyOut, error)
	Setup(SetupIn) error
	SetTransport(SetTransportIn) error
	RestartTunnel() error
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

type LeakCheckIn struct {
	Network          bool     `json:"network,omitempty" jsonschema:"send outbound IPv4/IPv6/DNS probes"`
	Browser          bool     `json:"browser,omitempty" jsonschema:"open local browser page for WebRTC ICE candidates"`
	BrowserConfirmed bool     `json:"browser_confirmed,omitempty" jsonschema:"must be true after user confirms opening a local browser page"`
	ExpectedIPs      []string `json:"expected_ips,omitempty" jsonschema:"acceptable proxy/VPS public IPs"`
	NetworkTimeout   string   `json:"network_timeout,omitempty" jsonschema:"optional network probe timeout, e.g. 8s"`
	BrowserTimeout   string   `json:"browser_timeout,omitempty" jsonschema:"optional browser ICE timeout, e.g. 20s"`
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

type PlanIn struct {
	Link string `json:"link,omitempty" jsonschema:"optional server link to plan a setup/transport change for"`
}
type PlanOut struct {
	Steps []string `json:"steps" jsonschema:"the route/firewall steps that WOULD run, not applied"`
}

// Task 6: bx_verify 泄漏审计结果
type VerifyOut struct {
	Pass         bool   `json:"pass"`
	ExitIP       string `json:"exit_ip,omitempty" jsonschema:"observed egress IP; should be the VPS"`
	DNSLeak      bool   `json:"dns_leak"`
	IPv6Leak     bool   `json:"ipv6_leak"`
	SelfReach    bool   `json:"self_reach" jsonschema:"agent control channel (SSH) still reachable"`
	KillSwitchOK bool   `json:"killswitch_ok"`
	Note         string `json:"note,omitempty" jsonschema:"e.g. WebRTC requires a LAN-client browser test, not automated here"`
}
type SetupIn struct {
	Link string `json:"link" jsonschema:"server link: brook:// or vless://"`
}
type SetTransportIn struct {
	Link string `json:"link" jsonschema:"new server link to switch transport to"`
}
