package mcp

import (
	"context"
	"encoding/json"
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
