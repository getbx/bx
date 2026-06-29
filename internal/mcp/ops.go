package mcp

// Ops 是 MCP tools 依赖的操作 port。liveOps(Task 8)绑到现有 internal 包,
// 测试用 fakeOps。这样 tool handler 可纯逻辑测试,免 root。
type Ops interface {
	Capabilities() (CapabilitiesOut, error)
	Status() (StatusOut, error)
	Diagnose() (DiagnoseOut, error)
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

type LogsIn struct {
	Lines int    `json:"lines,omitempty" jsonschema:"how many trailing lines (default 100)"`
	Since string `json:"since,omitempty" jsonschema:"optional time filter, e.g. 10m"`
}
type LogsOut struct {
	Text string `json:"text"`
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
