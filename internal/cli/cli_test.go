package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/stats"
	"github.com/urfave/cli/v2"
)

func TestAppHasVersion(t *testing.T) {
	app := New()
	if strings.TrimSpace(app.Version) == "" {
		t.Fatal("app version should not be empty")
	}
	if !appHasCommand(app, "logs") {
		t.Fatal("app should expose bx logs")
	}
	logs := findAppCommand(app, "logs")
	if !commandHasFlag(logs, "archive") || !commandHasFlag(logs, "dir") {
		t.Fatal("logs should expose --archive and --dir")
	}
	if !appHasCommand(app, "dns") {
		t.Fatal("app should expose bx dns")
	}
	if !appHasCommand(app, "realtime") {
		t.Fatal("app should expose bx realtime")
	}
	if !appHasCommand(app, "webrtc-check") {
		t.Fatal("app should expose bx webrtc-check")
	}
	webrtc := findAppCommand(app, "webrtc-check")
	if !commandHasFlag(webrtc, "browser") || !commandHasFlag(webrtc, "expected-ip") {
		t.Fatal("webrtc-check should expose --browser and --expected-ip")
	}
	status := findAppCommand(app, "status")
	if !commandHasFlag(status, "json") {
		t.Fatal("status should expose --json")
	}
	inspect := findAppCommand(app, "inspect")
	if inspect == nil || !commandHasFlag(inspect, "json") {
		t.Fatal("inspect should expose --json")
	}
	realtime := findAppCommand(app, "realtime")
	if !commandHasSubcommand(realtime, "status") || !commandHasSubcommand(realtime, "on") || !commandHasSubcommand(realtime, "off") {
		t.Fatalf("realtime subcommands = %+v, want status/on/off", realtime.Subcommands)
	}
	if !subcommandHidden(realtime, "on") || !subcommandHidden(realtime, "off") {
		t.Fatal("realtime on/off should stay hidden from the normal help surface")
	}
}

func TestBuildExecStart(t *testing.T) {
	got := buildExecStartForGOOS("linux", "/usr/local/bin/bx", "/etc/bx/config.yaml")
	want := "/usr/local/bin/bx run -c /etc/bx/config.yaml"
	if got != want {
		t.Fatalf("ExecStart 应跑 run, got %q", got)
	}
	got = buildExecStartForGOOS("darwin", "/usr/local/bin/bx", "/etc/bx/config.yaml")
	want = "/usr/local/bin/bx run -c /etc/bx/config.yaml --listen-dns 127.0.0.1:53"
	if got != want {
		t.Fatalf("darwin ExecStart 应监听本地 DNS, got %q", got)
	}
}

func TestBlinkRoundTripThroughCLI(t *testing.T) {
	link := "brook://server?server=1.2.3.4%3A9999&password=pw"
	enc := blink.Encode(link)
	dec, err := blink.Decode(enc)
	if err != nil || dec != link {
		t.Fatalf("round-trip 失败: %q err=%v", dec, err)
	}
}

func TestNormalizeClientLinkAcceptsRawBrook(t *testing.T) {
	raw := "brook://wssserver?wssserver=wss%3A%2F%2Fvps.example.com%3A443&username&password=pw"
	link, configLink, err := normalizeClientLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	if link != raw {
		t.Fatalf("link = %q, want raw brook link", link)
	}
	if !strings.HasPrefix(configLink, "bx://") {
		t.Fatalf("config link should be normalized to bx://, got %q", configLink)
	}
	decoded, err := blink.Decode(configLink)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != raw {
		t.Fatalf("decoded config link = %q, want %q", decoded, raw)
	}
}

func TestNormalizeClientLinkAcceptsEncodedBX(t *testing.T) {
	raw := "brook://server?server=1.2.3.4%3A9999&password=pw"
	encoded := blink.Encode(raw)
	link, configLink, err := normalizeClientLink(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if link != raw || configLink != encoded {
		t.Fatalf("link/config = %q/%q, want %q/%q", link, configLink, raw, encoded)
	}
}

func TestNormalizeClientLinkAcceptsVless(t *testing.T) {
	raw := "vless://be625ca6@1.2.3.4:9998?security=reality&pbk=PUB&sid=ab12&sni=www.apple.com&flow=xtls-rprx-vision&fp=chrome"
	link, configLink, err := normalizeClientLink(raw)
	if err != nil {
		t.Fatalf("vless 链接应被接受: %v", err)
	}
	if link != raw {
		t.Fatalf("link = %q, want raw vless link", link)
	}
	if !strings.HasPrefix(configLink, "bx://") {
		t.Fatalf("config link 应换壳成 bx://, got %q", configLink)
	}
	decoded, err := blink.Decode(configLink)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != raw {
		t.Fatalf("decoded config link = %q, want %q", decoded, raw)
	}
}

func TestBXServerLink(t *testing.T) {
	link, err := bxServerLink("example.com", serverConfig{Listen: ":9999", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := blink.Decode(link)
	if err != nil {
		t.Fatal(err)
	}
	want := "brook://server?server=example.com%3A9999&password=pw"
	if raw != want {
		t.Fatalf("raw link = %q, want %q", raw, want)
	}
}

func TestBXServerLinkRejectsHostWithPort(t *testing.T) {
	if _, err := bxServerLink("example.com:8443", serverConfig{Listen: ":9999", Password: "pw"}); err == nil {
		t.Fatal("host 带端口应报错,端口应来自 listen")
	}
}

func TestServerFirewallHint(t *testing.T) {
	got := serverFirewallHint(":9998")
	for _, want := range []string{"TCP 9998", "sudo ufw allow 9998/tcp"} {
		if !strings.Contains(got, want) {
			t.Fatalf("firewall hint = %q, want contains %q", got, want)
		}
	}
	if got := serverFirewallHint("bad-listen"); got != "" {
		t.Fatalf("bad listen should not produce hint, got %q", got)
	}
}

func TestOpenUFWRejectsBadListen(t *testing.T) {
	if err := openUFW("bad-listen"); err == nil {
		t.Fatal("bad listen should fail")
	}
}

func TestDoctorHelpers(t *testing.T) {
	if got := boolStatus(true); got != "ok" {
		t.Fatalf("boolStatus(true)=%q", got)
	}
	if got := boolStatus(false); got != "fail" {
		t.Fatalf("boolStatus(false)=%q", got)
	}
	if got := redactLink("bx://secret"); got != "bx://<redacted>" {
		t.Fatalf("redact bx link = %q", got)
	}
	if got := redactLink("brook://server?password=pw"); got != "internal-link:<redacted>" {
		t.Fatalf("redact internal link = %q", got)
	}
	if got := shareDoctorStatus("active", "listening"); got != "ok" {
		t.Fatalf("shareDoctorStatus active/listening = %q", got)
	}
	if got := shareDoctorStatus("inactive", "listening"); got != "warn" {
		t.Fatalf("shareDoctorStatus inactive/listening = %q", got)
	}
	if got := hintForState("inactive", "sudo bx up", "bx logs"); got != "sudo bx up; bx logs" {
		t.Fatalf("hintForState inactive = %q", got)
	}
}

func TestClientDoctorJSONReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	rep := collectClientDoctor(path, "example.com:443", 0, true)
	if rep.OK {
		t.Fatal("missing client config should not be ok")
	}
	if rep.Kind != "client" || !rep.SecretsRedacted || rep.ChangesSystem || rep.ChangesNetwork || rep.RequiresRoot {
		t.Fatalf("unexpected client report metadata: %+v", rep)
	}
	if got := findCheck(rep.Checks, "config_readable"); got.Status != "fail" {
		t.Fatalf("config_readable = %+v, want fail", got)
	}
	if got := findCheck(rep.Checks, "udp_policy"); got.Status != "ok" || !strings.Contains(got.Detail, "relayed through bx tunnel") {
		t.Fatalf("udp_policy = %+v, want ok relay by default", got)
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	var parsed doctorReport
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("json should be parseable: %v\n%s", err, buf.String())
	}
	if parsed.Kind != "client" {
		t.Fatalf("parsed kind = %q", parsed.Kind)
	}
}

func TestClientDoctorReportsProxyUDPPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: \"brook://x\"\nudp:\n  mode: proxy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := collectClientDoctor(path, "example.com:443", 0, true)
	got := findCheck(rep.Checks, "udp_policy")
	if got.Status != "ok" || !strings.Contains(got.Detail, "relayed through bx tunnel") || got.Hint != "" {
		t.Fatalf("udp_policy = %+v, want ok proxy relay", got)
	}
}

func TestClientDoctorReportsBlockedUDPPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: \"brook://x\"\nudp:\n  mode: block\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := collectClientDoctor(path, "example.com:443", 0, true)
	got := findCheck(rep.Checks, "udp_policy")
	if got.Status != "warn" || !strings.Contains(got.Hint, "Google Meet") || !strings.Contains(got.Hint, "sudo bx realtime on") {
		t.Fatalf("udp_policy = %+v, want block warning with realtime hint", got)
	}
}

func TestClientInspectIncludesDoctorAndStatusError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	rep := collectClientInspect(path, "example.com:443", 0, true)
	if rep.OK {
		t.Fatal("missing config and missing status socket should not be ok")
	}
	if !rep.SecretsRedacted || rep.ChangesSystem || rep.ChangesNetwork {
		t.Fatalf("unexpected inspect metadata: %+v", rep)
	}
	if rep.Capabilities.Product != "bx" {
		t.Fatalf("capabilities product = %q", rep.Capabilities.Product)
	}
	if rep.Doctor.Kind != "client" {
		t.Fatalf("doctor kind = %q", rep.Doctor.Kind)
	}
	if rep.StatusError == "" {
		t.Fatal("inspect should keep status socket failure as data")
	}
	if len(rep.NextActions) == 0 {
		t.Fatal("inspect should include next actions")
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	var parsed inspectReport
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("json should be parseable: %v\n%s", err, buf.String())
	}
}

func TestWebRTCCheckLowRiskWhenUDPRelayed(t *testing.T) {
	cfg := &config.Config{UDP: config.UDP{Mode: "proxy", Transport: "hysteria2://x"}}
	rep := assessWebRTCCheck(cfg, &stats.Report{
		TunnelHealthy: true,
		UDPMode:       "proxy",
		UDPTransport:  "hysteria2@example.com",
	}, nil, install.DNSStatus{Supported: true, Enabled: true, Service: "Wi-Fi"}, nil)
	if !rep.OK || rep.Risk != "low" {
		t.Fatalf("webrtc report = %+v, want ok low", rep)
	}
	if !rep.BrowserVerificationRequired || rep.LeakProof != "not_proven" {
		t.Fatalf("webrtc report should keep browser verification boundary: %+v", rep)
	}
	if got := findCheck(rep.Checks, "udp_path"); got.Status != "ok" || !strings.Contains(got.Detail, "relayed") {
		t.Fatalf("udp_path = %+v, want relayed ok", got)
	}
}

func TestWebRTCCheckHighRiskForDirectUDP(t *testing.T) {
	cfg := &config.Config{UDP: config.UDP{Mode: "direct-realtime"}}
	rep := assessWebRTCCheck(cfg, &stats.Report{
		TunnelHealthy: true,
		UDPMode:       "direct-realtime",
	}, nil, install.DNSStatus{Supported: true, Enabled: true, Service: "Wi-Fi"}, nil)
	if rep.OK || rep.Risk != "high" {
		t.Fatalf("webrtc report = %+v, want high risk", rep)
	}
	if got := findCheck(rep.Checks, "udp_path"); got.Status != "fail" || !strings.Contains(got.Detail, "real network") {
		t.Fatalf("udp_path = %+v, want direct UDP fail", got)
	}
}

func TestApplyBrowserICECandidatesDetectsUnexpectedPublicIP(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 203.0.113.10 55000 typ srflx raddr 192.168.1.5 rport 55000",
		},
	}, []string{"203.0.113.20"})
	if rep.OK || rep.Risk != "high" || rep.LeakProof != "unexpected_public_ip_detected" {
		t.Fatalf("browser candidates should detect unexpected public IP: %+v", rep)
	}
	if got := findCheck(rep.Checks, "browser_unexpected_public_ip"); got.Status != "fail" || !strings.Contains(got.Detail, "203.0.113.10") || strings.Contains(got.Hint, "real public IP") {
		t.Fatalf("browser_unexpected_public_ip = %+v, want unexpected public IP without real-IP overclaim", got)
	}
}

func TestApplyBrowserICECandidatesAllowsExpectedProxyIP(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 203.0.113.20 55000 typ srflx",
		},
	}, []string{"203.0.113.20"})
	if !rep.OK || rep.Risk != "low" || rep.LeakProof != "no_public_leak_detected" {
		t.Fatalf("expected proxy IP should be accepted: %+v", rep)
	}
	if got := findCheck(rep.Checks, "browser_expected_public_ip"); got.Status != "ok" || !strings.Contains(got.Detail, "203.0.113.20") {
		t.Fatalf("browser_expected_public_ip = %+v, want expected proxy IP", got)
	}
}

func TestApplyBrowserICECandidatesWithoutExpectedIPCannotProveSafety(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 203.0.113.10 55000 typ srflx",
		},
	}, nil)
	if rep.OK || rep.Risk != "high" || rep.LeakProof != "public_ip_detected_without_expected" {
		t.Fatalf("public IP without expectation should be inconclusive/high: %+v", rep)
	}
}

func TestApplyBrowserICECandidatesFlagsLANAddress(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 192.168.50.18 55000 typ host",
		},
	}, nil)
	if rep.OK || rep.Risk != "medium" || rep.LeakProof != "local_network_candidate_detected" {
		t.Fatalf("LAN candidate should be medium risk: %+v", rep)
	}
}

func TestApplyBrowserICECandidatesIgnoresUnspecifiedAddress(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 0.0.0.0 9 typ host",
		},
	}, nil)
	if !rep.OK || rep.Risk != "low" || rep.LeakProof != "no_public_leak_detected" {
		t.Fatalf("0.0.0.0 should not count as leak: %+v", rep)
	}
}

func TestArchiveClientLogsRecordsReason(t *testing.T) {
	dir, err := archiveClientLogsWithReason(t.TempDir(), "doctor")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := os.ReadFile(filepath.Join(dir, "meta.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), "reason=doctor") {
		t.Fatalf("meta should include archive reason:\n%s", meta)
	}
	for _, name := range []string{"doctor.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("archive should include %s: %v", name, err)
		}
	}
}

func TestDefaultLogArchiveRootIsAbsolute(t *testing.T) {
	t.Setenv("BX_LOG_ARCHIVE_DIR", "")
	root := defaultLogArchiveRoot()
	if root == "" || !filepath.IsAbs(root) {
		t.Fatalf("default log archive root should be absolute, got %q", root)
	}
}

func TestDefaultLogArchiveRootHonorsEnv(t *testing.T) {
	want := filepath.Join(t.TempDir(), "diagnostics")
	t.Setenv("BX_LOG_ARCHIVE_DIR", want)
	if got := defaultLogArchiveRoot(); got != want {
		t.Fatalf("default log archive root = %q, want env %q", got, want)
	}
}

func TestPruneLogArchivesKeepsNewest(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 15; i++ {
		name := filepath.Join(root, "bx-logs-20260101-1200"+leftPadInt(i, 2))
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneLogArchives(root, 12); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		if entry.IsDir() {
			got = append(got, entry.Name())
		}
	}
	if len(got) != 12 {
		t.Fatalf("dirs after prune = %d, want 12: %v", len(got), got)
	}
	if _, err := os.Stat(filepath.Join(root, "bx-logs-20260101-120000")); !os.IsNotExist(err) {
		t.Fatalf("oldest archive should be pruned, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bx-logs-20260101-120014")); err != nil {
		t.Fatalf("newest archive should be kept: %v", err)
	}
}

func TestPruneLogArchivesAllowsFewerThanKeep(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bx-logs-20260101-120000"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := pruneLogArchives(root, 12); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "bx-logs-20260101-120000")); err != nil {
		t.Fatalf("single archive should be kept: %v", err)
	}
}

func TestCapabilitiesReport(t *testing.T) {
	rep := capabilities()
	if rep.SchemaVersion != 1 || rep.Product != "bx" || !rep.SecretsRedacted {
		t.Fatalf("unexpected capabilities metadata: %+v", rep)
	}
	doctor := findCapability(rep.Commands, "bx doctor --json")
	if !doctor.Stable || doctor.RequiresRoot || doctor.ChangesSystem || doctor.ChangesNetwork || !doctor.ReadsSecrets {
		t.Fatalf("unexpected doctor capability: %+v", doctor)
	}
	if len(doctor.Arguments) == 0 || len(doctor.Examples) == 0 {
		t.Fatalf("doctor capability should include arguments/examples: %+v", doctor)
	}
	inspect := findCapability(rep.Commands, "bx inspect --json")
	if !inspect.Stable || inspect.RequiresRoot || inspect.ChangesSystem || inspect.ChangesNetwork || !inspect.ReadsSecrets {
		t.Fatalf("unexpected inspect capability: %+v", inspect)
	}
	webrtc := findCapability(rep.Commands, "bx webrtc-check --json")
	if !webrtc.Stable || webrtc.RequiresRoot || webrtc.ChangesSystem || webrtc.ChangesNetwork || !webrtc.ReadsSecrets {
		t.Fatalf("unexpected webrtc-check capability: %+v", webrtc)
	}
	notes := strings.Join(webrtc.SafeNotes, " ")
	if !strings.Contains(notes, "ICE candidates") || !strings.Contains(notes, "prove a WebRTC public-IP leak") {
		t.Fatalf("webrtc-check should describe browser ICE proof capability: %+v", webrtc)
	}
	setup := findCapability(rep.Commands, "sudo bx setup <client-link>")
	if setup.Command == "" || !strings.Contains(strings.Join(setup.Arguments, " "), "<client-link>") {
		t.Fatalf("setup capability should use client-link wording: %+v", setup)
	}
	probe := findCapability(rep.Commands, "bx probe <client-link>")
	if probe.Command == "" || !strings.Contains(strings.Join(probe.Examples, " "), "<client-link>") {
		t.Fatalf("probe capability should use client-link wording: %+v", probe)
	}
	up := findCapability(rep.Commands, "sudo bx up")
	if !up.RequiresRoot || !up.ChangesSystem || !up.ChangesNetwork {
		t.Fatalf("unexpected up capability: %+v", up)
	}
	status := findCapability(rep.Commands, "bx status --json")
	if !status.Stable || status.RequiresRoot || status.ChangesSystem || status.ChangesNetwork {
		t.Fatalf("unexpected status json capability: %+v", status)
	}
	if !strings.Contains(strings.Join(status.SafeNotes, " "), "menu bar") {
		t.Fatalf("status json should mention status surfaces: %+v", status)
	}
	logs := findCapability(rep.Commands, "bx logs")
	if !logs.Stable || logs.ChangesSystem || logs.ChangesNetwork {
		t.Fatalf("unexpected logs capability: %+v", logs)
	}
	if !strings.Contains(strings.ToLower(strings.Join(logs.SafeNotes, " ")), "automatic diagnostics") {
		t.Fatalf("logs capability should mention automatic diagnostics archive: %+v", logs)
	}
	udpStatus := findCapability(rep.Commands, "bx realtime status")
	if !udpStatus.Stable || udpStatus.RequiresRoot || udpStatus.ChangesSystem || udpStatus.ChangesNetwork {
		t.Fatalf("unexpected realtime status capability: %+v", udpStatus)
	}
	if !strings.Contains(strings.Join(udpStatus.SafeNotes, " "), "UDP") {
		t.Fatalf("realtime status should mention UDP: %+v", udpStatus)
	}
	realtimeOn := findCapability(rep.Commands, "sudo bx realtime on")
	if !realtimeOn.Stable || !realtimeOn.RequiresRoot || !realtimeOn.ChangesSystem || realtimeOn.ChangesNetwork || !realtimeOn.ReadsSecrets {
		t.Fatalf("unexpected realtime on capability: %+v", realtimeOn)
	}
	if !strings.Contains(strings.Join(realtimeOn.SafeNotes, " "), "Relays non-DNS UDP") {
		t.Fatalf("realtime on should document UDP relay behavior: %+v", realtimeOn)
	}
	realtimeOff := findCapability(rep.Commands, "sudo bx realtime off")
	if !realtimeOff.Stable || !realtimeOff.RequiresRoot || !realtimeOff.ChangesSystem || realtimeOff.ChangesNetwork || !realtimeOff.ReadsSecrets {
		t.Fatalf("unexpected realtime off capability: %+v", realtimeOff)
	}
	dnsOn := findCapability(rep.Commands, "sudo bx dns on")
	if !dnsOn.RequiresRoot || !dnsOn.ChangesSystem || !dnsOn.ChangesNetwork {
		t.Fatalf("unexpected dns on capability: %+v", dnsOn)
	}
	menuInstall := findCapability(rep.Commands, "scripts/install-macos-menu.sh install")
	if menuInstall.Command == "" || menuInstall.RequiresRoot || menuInstall.ChangesNetwork || menuInstall.ChangesSystem {
		t.Fatalf("unexpected macOS menu install capability: %+v", menuInstall)
	}
	if strings.Contains(strings.ToLower(menuInstall.Summary), "companion") {
		t.Fatalf("macOS menu install should describe the app as the default menu bar app, not a companion: %+v", menuInstall)
	}
	if !strings.Contains(strings.Join(menuInstall.SafeNotes, " "), "Does not start protection") {
		t.Fatalf("macOS menu install should clarify it does not start protection: %+v", menuInstall)
	}
	menuStatus := findCapability(rep.Commands, "scripts/install-macos-menu.sh status")
	if menuStatus.Command == "" || !menuStatus.Stable || menuStatus.ChangesSystem || menuStatus.ChangesNetwork {
		t.Fatalf("unexpected macOS menu status capability: %+v", menuStatus)
	}
	menuRestart := findCapability(rep.Commands, "scripts/install-macos-menu.sh restart")
	if menuRestart.Command == "" || menuRestart.RequiresRoot || menuRestart.ChangesSystem || menuRestart.ChangesNetwork {
		t.Fatalf("unexpected macOS menu restart capability: %+v", menuRestart)
	}
	if strings.Contains(strings.ToLower(strings.Join([]string{menuRestart.Summary, strings.Join(menuRestart.SafeNotes, " ")}, " ")), "companion") {
		t.Fatalf("macOS menu restart should describe the menu bar app, not a companion: %+v", menuRestart)
	}
	if !strings.Contains(strings.Join(menuRestart.SafeNotes, " "), "not protection") {
		t.Fatalf("macOS menu restart should clarify it does not restart protection: %+v", menuRestart)
	}
	menuUninstall := findCapability(rep.Commands, "scripts/install-macos-menu.sh uninstall")
	if menuUninstall.Command == "" || menuUninstall.RequiresRoot || menuUninstall.ChangesSystem || menuUninstall.ChangesNetwork {
		t.Fatalf("unexpected macOS menu uninstall capability: %+v", menuUninstall)
	}
	if !strings.Contains(strings.Join(menuUninstall.SafeNotes, " "), "Does not turn off protection") {
		t.Fatalf("macOS menu uninstall should clarify it does not turn off protection: %+v", menuUninstall)
	}
	macRelease := findCapability(rep.Commands, "scripts/package-macos-release.sh")
	if macRelease.Command == "" || !macRelease.Stable || macRelease.RequiresRoot || macRelease.ChangesSystem || macRelease.ChangesNetwork {
		t.Fatalf("unexpected macOS release capability: %+v", macRelease)
	}
	macReleaseVerify := findCapability(rep.Commands, "scripts/verify-macos-release.sh")
	if macReleaseVerify.Command == "" || !macReleaseVerify.Stable || macReleaseVerify.RequiresRoot || macReleaseVerify.ChangesSystem || macReleaseVerify.ChangesNetwork {
		t.Fatalf("unexpected macOS release verify capability: %+v", macReleaseVerify)
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	var parsed capabilitiesReport
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("capabilities json should be parseable: %v\n%s", err, buf.String())
	}
}

func leftPadInt(v, width int) string {
	s := strconv.Itoa(v)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

func TestRenderRealtimeStatusFallback(t *testing.T) {
	out := renderRealtimeStatus(nil)
	for _, want := range []string{
		"realtime supported: true",
		"udp mode: proxy",
		"udp blocked: unknown",
		"relayed through bx tunnel",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("realtime fallback missing %q:\n%s", want, out)
		}
	}
}

func TestRenderRealtimeStatusFromReport(t *testing.T) {
	out := renderRealtimeStatus(&stats.Report{
		Snapshot: stats.Snapshot{UDPBlocked: 42},
		UDPMode:  "block",
		UDPNote:  "custom udp note",
	})
	for _, want := range []string{
		"udp mode: block",
		"udp blocked: 42",
		"custom udp note",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("realtime report missing %q:\n%s", want, out)
		}
	}
}

func TestSetRealtimeModeUpdatesClientConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: \"brook://x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setRealtimeMode(path, "proxy"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UDP.Mode != "proxy" {
		t.Fatalf("udp mode after on = %q, want proxy", cfg.UDP.Mode)
	}
	if err := setRealtimeMode(path, "block"); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UDP.Mode != "block" {
		t.Fatalf("udp mode after off = %q, want block", cfg.UDP.Mode)
	}
}

func TestPlanRealtimePostChange(t *testing.T) {
	tests := []struct {
		name          string
		noRestart     bool
		unitInstalled bool
		activeState   string
		wantRestart   bool
		wantContains  string
	}{
		{"active restarts", false, true, "active", true, "已重启"},
		{"no restart flag", true, true, "active", false, "重启 bx 生效"},
		{"not installed", false, false, "inactive", false, "sudo bx up"},
		{"inactive installed", false, true, "inactive", false, "下次 sudo bx up"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planRealtimePostChange(tt.noRestart, tt.unitInstalled, tt.activeState)
			if got.Restart != tt.wantRestart || !strings.Contains(got.Message, tt.wantContains) {
				t.Fatalf("plan = %+v, want restart=%v message containing %q", got, tt.wantRestart, tt.wantContains)
			}
		})
	}
}

func TestSetRealtimeModePreservesBXLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	link := blink.Encode("brook://server?server=example.com%3A443&password=pw")
	if err := os.WriteFile(path, []byte("server: \""+link+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setRealtimeMode(path, "direct-realtime"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, link) {
		t.Fatalf("setRealtimeMode should preserve bx link, got:\n%s", text)
	}
	if strings.Contains(text, "brook://server?") {
		t.Fatalf("setRealtimeMode should not rewrite bx link to internal link:\n%s", text)
	}
}

func TestRealtimeReportFromConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: \"brook://x\"\nudp:\n  mode: proxy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := realtimeReportFromConfig(path)
	if rep == nil {
		t.Fatal("expected realtime report from config")
	}
	if rep.UDPMode != "proxy" || !strings.Contains(rep.UDPNote, "relayed through bx tunnel") {
		t.Fatalf("report = %+v, want proxy relay note", rep)
	}
}

func TestServerDoctorJSONReport(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "server.yaml")
	sharesDir := filepath.Join(dir, "shares")
	if err := writeServerConfig(cfgPath, serverConfig{Listen: ":10998", Password: "secret"}, false); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sharesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rep := collectServerDoctor(cfgPath, sharesDir)
	if rep.Kind != "server" || !rep.SecretsRedacted || !rep.RequiresRoot {
		t.Fatalf("unexpected server report metadata: %+v", rep)
	}
	if got := findCheck(rep.Checks, "config_parse"); got.Status != "ok" {
		t.Fatalf("config_parse = %+v, want ok", got)
	}
	if got := findCheck(rep.Checks, "shares"); got.Status != "info" || got.Detail != "none" {
		t.Fatalf("shares = %+v, want none info", got)
	}
}

func TestShareJSONViewsExposeOnlyOperationalFields(t *testing.T) {
	shares := []shareInfo{{
		Name:   "alice",
		Config: serverConfig{Listen: ":10001", Password: "pw"},
	}}
	views := shareViews(shares)
	if len(views) != 1 || views[0].Name != "alice" || views[0].Listen != ":10001" {
		t.Fatalf("share views = %+v", views)
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, sharesReport{OK: true, SecretsRedacted: true, Shares: views}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "pw") {
		t.Fatalf("shares json should not expose password: %s", buf.String())
	}
}

func findCheck(checks []checkReport, name string) checkReport {
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	return checkReport{}
}

func findCapability(commands []commandCapability, command string) commandCapability {
	for _, item := range commands {
		if item.Command == command {
			return item
		}
	}
	return commandCapability{}
}

func appHasCommand(app *cli.App, name string) bool {
	return findAppCommand(app, name) != nil
}

func findAppCommand(app *cli.App, name string) *cli.Command {
	for _, command := range app.Commands {
		if command.Name == name {
			return command
		}
	}
	return nil
}

func commandHasSubcommand(command *cli.Command, name string) bool {
	for _, sub := range command.Subcommands {
		if sub.Name == name {
			return true
		}
	}
	return false
}

func subcommandHidden(command *cli.Command, name string) bool {
	for _, sub := range command.Subcommands {
		if sub.Name == name {
			return sub.Hidden
		}
	}
	return false
}

func commandHasFlag(command *cli.Command, name string) bool {
	for _, flag := range command.Flags {
		for _, flagName := range flag.Names() {
			if flagName == name {
				return true
			}
		}
	}
	return false
}

func TestIsListening(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if !isListening(port) {
		t.Fatalf("port %s should be detected as listening", port)
	}
}

func TestCopyIfExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.log")
	dst := filepath.Join(dir, "copy.log")
	if err := os.WriteFile(src, []byte("raw log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyIfExists(src, dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "raw log\n" {
		t.Fatalf("copy = %q", b)
	}
	if err := copyIfExists(filepath.Join(dir, "missing.log"), filepath.Join(dir, "missing-copy.log")); err != nil {
		t.Fatalf("missing source should be ignored: %v", err)
	}
}

func TestWriteReadServerConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	cfg := serverConfig{Listen: ":9999", Password: "pw"}
	if err := writeServerConfig(path, cfg, false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("server config perm = %o, want 0600", fi.Mode().Perm())
	}
	got, err := readServerConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != cfg {
		t.Fatalf("config = %+v, want %+v", got, cfg)
	}
}

func TestWriteServerConfigForceResetsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeServerConfig(path, serverConfig{Listen: ":9999", Password: "pw"}, true); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("server config perm = %o, want 0600", fi.Mode().Perm())
	}
}

func TestRotateServerConfigPreservesListenAndResetsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := writeServerConfig(path, serverConfig{Listen: ":9999", Password: "old"}, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := rotateServerConfig(path, "new")
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != ":9999" || got.Password != "new" {
		t.Fatalf("rotated config = %+v", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("server config perm = %o, want 0600", fi.Mode().Perm())
	}
}

func TestShareHelpers(t *testing.T) {
	if got, err := cleanShareName("alice-1"); err != nil || got != "alice-1" {
		t.Fatalf("cleanShareName = %q, %v", got, err)
	}
	for _, bad := range []string{"", "../x", "a b", "x/y"} {
		if _, err := cleanShareName(bad); err == nil {
			t.Fatalf("bad share name %q should fail", bad)
		}
	}
	dir := t.TempDir()
	if got := shareConfigPath(dir, "alice"); got != filepath.Join(dir, "alice.yaml") {
		t.Fatalf("shareConfigPath = %q", got)
	}
}

func TestStringFlagReadsPostArgFlags(t *testing.T) {
	args := []string{"alice", "--host", "example.com", "--listen=:10077"}
	if got := stringFlagFromArgs(args, "host"); got != "example.com" {
		t.Fatalf("host = %q", got)
	}
	if got := stringFlagFromArgs(args, "listen"); got != ":10077" {
		t.Fatalf("listen = %q", got)
	}
}

func TestReadSharesSorted(t *testing.T) {
	dir := t.TempDir()
	for _, item := range []struct {
		name   string
		listen string
	}{
		{"bob", ":10002"},
		{"alice", ":10001"},
	} {
		if err := writeServerConfig(shareConfigPath(dir, item.name), serverConfig{Listen: item.listen, Password: "pw"}, false); err != nil {
			t.Fatal(err)
		}
	}
	got, err := readShares(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "alice" || got[1].Name != "bob" {
		t.Fatalf("shares = %+v", got)
	}
}

func TestNextShareListenSkipsExistingShares(t *testing.T) {
	dir := t.TempDir()
	if err := writeServerConfig(shareConfigPath(dir, "alice"), serverConfig{Listen: ":10000", Password: "pw"}, false); err != nil {
		t.Fatal(err)
	}
	got, err := nextShareListen(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != ":10001" {
		t.Fatalf("nextShareListen = %q, want :10001", got)
	}
}

func TestResolveConfigPathKeepsExplicitMissingPath(t *testing.T) {
	// 用户显式传入的不存在路径应原样返回(不偷偷回退),便于错误信息指向用户路径
	p := "/nonexistent/explicit/whoami-bx-test.yaml"
	if got := resolveConfigPath(p); got != p {
		t.Fatalf("显式缺失路径应原样返回, got %q", got)
	}
}

func TestMCPInstallText(t *testing.T) {
	out := mcpInstallText("/usr/local/bin/bx")
	for _, want := range []string{
		"claude mcp add --scope user bx -- /usr/local/bin/bx mcp",
		`"command": "/usr/local/bin/bx"`,
		`"args": ["mcp"]`,
		"AI agent",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mcpInstallText 缺 %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestRawLinkRisk(t *testing.T) {
	// 裸凭据链接 → 提示
	for _, raw := range []string{
		"vless://uuid@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com",
		"brook://server?server=1.2.3.4%3A9999&password=pw",
		"  vless://x@h:1 ", // 带空白也认
	} {
		if rawLinkRisk(raw) == "" {
			t.Errorf("裸链接应提示风险: %q", raw)
		}
	}
	// 已换壳 / 非链接 → 不提示
	for _, wrapped := range []string{
		"bx://eyJ2IjoxfQ",
		"blink://abc",
		"",
		"garbage",
	} {
		if rawLinkRisk(wrapped) != "" {
			t.Errorf("已换壳/非裸链接不该提示: %q", wrapped)
		}
	}
}

func TestProtocolAdvisory(t *testing.T) {
	// 弱协议(对当今强 DPI/探测易识别)→ 建议换 REALITY
	for _, weak := range []string{
		"trojan://pw@1.2.3.4:443?sni=x.com",
		"ss://YWVzLTI1Ni1nY206cHc@1.2.3.4:8388",
		"vmess://eyJhZGQiOiIxLjIuMy40IiwicG9ydCI6IjQ0MyIsImlkIjoieCIsIm5ldCI6InRjcCJ9",
	} {
		a := protocolAdvisory(weak)
		if a == "" || !strings.Contains(a, "REALITY") {
			t.Errorf("弱协议应提示换 REALITY: %q → %q", weak, a)
		}
	}
	// hysteria2 缺 obfs → 提示加 salamander
	if a := protocolAdvisory("hysteria2://pw@1.2.3.4:8443?sni=x.com"); !strings.Contains(a, "obfs") {
		t.Errorf("裸 hysteria2 应提示加 obfs: %q", a)
	}
	// hysteria2 已带 obfs → 不提示
	if a := protocolAdvisory("hysteria2://pw@1.2.3.4:8443?sni=x.com&obfs=salamander&obfs-password=p"); a != "" {
		t.Errorf("带 obfs 的 hysteria2 不该提示: %q", a)
	}
	// reality / brook(一等公民/默认)→ 不提示
	for _, ok := range []string{
		"vless://uuid@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com",
		"brook://server?server=1.2.3.4%3A9999&password=pw",
	} {
		if a := protocolAdvisory(ok); a != "" {
			t.Errorf("reality/brook 不该提示: %q → %q", ok, a)
		}
	}
}

func TestResolveConfigLinksBundle(t *testing.T) {
	l1 := "vless://u@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com"
	l2 := "brook://server?server=1.2.3.4%3A9999&password=pw"
	bundle := blink.EncodeMulti([]string{l1, l2})
	probe, configLinks, err := resolveConfigLinks(bundle)
	if err != nil {
		t.Fatalf("resolve bundle: %v", err)
	}
	if probe != l1 {
		t.Fatalf("probe 应=主传输 %q, got %q", l1, probe)
	}
	if len(configLinks) != 2 {
		t.Fatalf("应 2 个 configLink, got %d", len(configLinks))
	}
	// 各自换壳,解回应等于原 link
	for i, want := range []string{l1, l2} {
		got, err := blink.Decode(configLinks[i])
		if err != nil || got != want {
			t.Fatalf("configLink[%d] 解回=%q want=%q err=%v", i, got, want, err)
		}
	}
}

func TestResolveConfigLinksRawSingle(t *testing.T) {
	raw := "vless://u@h:1?security=reality&pbk=K&sid=a&sni=s"
	probe, configLinks, err := resolveConfigLinks(raw)
	if err != nil || probe != raw || len(configLinks) != 1 {
		t.Fatalf("裸单链接: probe=%q n=%d err=%v", probe, len(configLinks), err)
	}
}

func TestClientDoctorVlessServerLinkOK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("server: \"vless://u@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com\"\n"), 0o600)
	rep := collectClientDoctor(path, "x:443", 0, true)
	got := findCheck(rep.Checks, "server_link")
	if got.Status != "ok" {
		t.Fatalf("vless server_link 应 ok,实得 %+v", got)
	}
}

func TestVlessUUIDHelpers(t *testing.T) {
	link := "vless://old-uuid-1234@1.2.3.4:443?security=reality&pbk=P&sid=ab&sni=www.cloudflare.com"
	if got := uuidFromVlessLink(link); got != "old-uuid-1234" {
		t.Errorf("extract uuid: got %q", got)
	}
	swapped := swapVlessUUID(link, "new-uuid-5678")
	if uuidFromVlessLink(swapped) != "new-uuid-5678" {
		t.Errorf("swap uuid 失败: %q", swapped)
	}
	// 其余部分(host/port/query)不变
	if !strings.Contains(swapped, "@1.2.3.4:443?security=reality&pbk=P&sid=ab&sni=www.cloudflare.com") {
		t.Errorf("swap 不该动其余部分: %q", swapped)
	}
	// 非 vless 链接
	if uuidFromVlessLink("brook://x") != "" {
		t.Error("非 vless 应返回空")
	}
}
