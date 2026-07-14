package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func callToolOn(t *testing.T, srv *mcpsdk.Server, name string, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	ctx := context.Background()
	st, ct := mcpsdk.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "t", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

func callTool(t *testing.T, ops Ops, name string, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	return callToolOn(t, newServer(ops), name, args)
}

func TestCapabilitiesTool(t *testing.T) {
	ops := &fakeOps{caps: CapabilitiesOut{Platform: "linux", Transports: []string{"brook", "reality"}, Installed: true}}
	res := callTool(t, ops, "bx_capabilities", map[string]any{})
	if res.IsError {
		t.Fatal("不应错误")
	}
	var out CapabilitiesOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.Platform != "linux" || !out.Installed {
		t.Fatalf("got %+v", out)
	}
}

func TestDiagnoseTool(t *testing.T) {
	ops := &fakeOps{diagnose: DiagnoseOut{Findings: []Finding{{Severity: "warn", Title: "v6 enabled"}}}}
	res := callTool(t, ops, "bx_diagnose", map[string]any{})
	if res.IsError {
		t.Fatal("不应错误")
	}
}

func TestStatusToolIncludesMutationState(t *testing.T) {
	ops := &fakeOps{status: StatusOut{TunnelHealthy: true, MutationState: "armed"}}
	res := callTool(t, ops, "bx_status", map[string]any{})
	if res.IsError {
		t.Fatal("不应错误")
	}
	var out StatusOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.MutationState != "armed" {
		t.Fatalf("mutation_state=%q want armed", out.MutationState)
	}
}

func TestLogsToolReturnsStructuredReport(t *testing.T) {
	ops := &fakeOps{logs: LogsOut{OK: false, Text: "partial\n", Error: "denied", Hint: "sudo bx logs"}}
	res := callTool(t, ops, "bx_logs", map[string]any{"lines": 5})
	if res.IsError {
		t.Fatal("logs tool should return structured log report, not tool error")
	}
	var out LogsOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.OK || out.Text != "partial\n" || out.Error != "denied" || out.Hint == "" {
		t.Fatalf("logs out = %+v, want structured error report", out)
	}
}

func TestInspectToolReturnsCLIJSONEnvelope(t *testing.T) {
	ops := &fakeOps{inspect: JSONCommandOut{
		OK:      true,
		Command: []string{"bx", "inspect", "--json", "--skip-probe"},
		JSON:    map[string]any{"ok": true, "kind": "inspect"},
	}}
	res := callTool(t, ops, "bx_inspect", map[string]any{"skip_probe": true})
	if res.IsError {
		t.Fatal("inspect tool should be read-only and successful")
	}
	var out JSONCommandOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.JSON["kind"] != "inspect" || out.Command[1] != "inspect" {
		t.Fatalf("inspect out = %+v, want CLI JSON envelope", out)
	}
}

func TestLeakCheckToolReturnsCLIJSONEnvelope(t *testing.T) {
	ops := &fakeOps{leakCheck: JSONCommandOut{
		OK:      true,
		Command: []string{"bx", "leak-check", "--json", "--network"},
		JSON:    map[string]any{"ok": true, "kind": "leak"},
	}}
	res := callTool(t, ops, "bx_leak_check", map[string]any{"network": true, "expected_ips": []string{"203.0.113.10"}})
	if res.IsError {
		t.Fatal("leak_check tool should be read-only and successful")
	}
	var out JSONCommandOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.JSON["kind"] != "leak" || out.Command[1] != "leak-check" {
		t.Fatalf("leak_check out = %+v, want CLI JSON envelope", out)
	}
}

func TestObserveToolReturnsCLIJSONEnvelope(t *testing.T) {
	ops := &fakeOps{observe: JSONCommandOut{
		OK:      true,
		Command: []string{"bx", "observe", "--json", "--duration", "30s"},
		JSON:    map[string]any{"ok": true, "kind": "observe"},
	}}
	res := callTool(t, ops, "bx_observe", map[string]any{"duration": "30s"})
	if res.IsError {
		t.Fatal("observe tool should be read-only and successful")
	}
	var out JSONCommandOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.JSON["kind"] != "observe" || out.Command[1] != "observe" {
		t.Fatalf("observe out = %+v, want CLI JSON envelope", out)
	}
}

func TestCheckToolRunsTheSafeDefaultBundle(t *testing.T) {
	ops := &fakeOps{check: CheckOut{OK: true, Risk: "low"}}
	res := callTool(t, ops, "bx_check", map[string]any{})
	if res.IsError {
		t.Fatal("safe check should be available without a mutation approval")
	}
	if len(ops.calls) != 1 || ops.calls[0] != "check" {
		t.Fatalf("calls=%v want [check]", ops.calls)
	}
	if ops.checkIn.Network || ops.checkIn.Browser || ops.checkIn.Duration != "" {
		t.Fatalf("zero-value check must not opt in to external probes: %+v", ops.checkIn)
	}
}

func TestObserveArgs(t *testing.T) {
	got := observeArgs(ObserveIn{Duration: "30s", Interval: "1s", Scenario: "video"})
	for _, want := range []string{"observe", "--json", "--duration", "30s", "--interval", "1s", "--scenario", "video"} {
		if !stringSliceContains(got, want) {
			t.Fatalf("observe args = %v, missing %s", got, want)
		}
	}
}

func TestInspectArgsDefaultToNoOutboundProbe(t *testing.T) {
	got := inspectArgs("/etc/bx/config.yaml", InspectIn{})
	if !stringSliceContains(got, "--skip-probe") {
		t.Fatalf("inspect args = %v, want --skip-probe by default", got)
	}
	got = inspectArgs("/etc/bx/config.yaml", InspectIn{Probe: true})
	if stringSliceContains(got, "--skip-probe") {
		t.Fatalf("inspect args = %v, did not expect --skip-probe when probe=true", got)
	}
}

func TestLeakCheckBrowserRequiresConfirmation(t *testing.T) {
	out, blocked := browserConfirmationRequired(LeakCheckIn{Browser: true})
	if !blocked || out.OK || !strings.Contains(out.Hint, "browser_confirmed") || len(out.TestSteps) == 0 || len(out.Recommendations) == 0 {
		t.Fatalf("confirmation result = %+v blocked=%v, want blocked guidance", out, blocked)
	}
	_, blocked = browserConfirmationRequired(LeakCheckIn{Browser: true, BrowserConfirmed: true})
	if blocked {
		t.Fatal("browser_confirmed=true should allow browser leak check")
	}
	args := leakCheckArgs("/etc/bx/config.yaml", LeakCheckIn{Browser: true, BrowserConfirmed: true})
	if !stringSliceContains(args, "--browser") {
		t.Fatalf("leak check args = %v, want --browser after confirmation", args)
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
