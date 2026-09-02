package mcp

import (
	"context"
	"errors"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type emptyIn struct{}

func registerReadOnly(s *mcpsdk.Server, ops Ops) {
	ro := &mcpsdk.ToolAnnotations{ReadOnlyHint: true}

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_capabilities", Description: "host platform, supported transports, installed?", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, CapabilitiesOut, error) {
			out, err := ops.Capabilities()
			if err != nil {
				return errResultTyped[CapabilitiesOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_status", Description: "tunnel health, latency, mode", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, StatusOut, error) {
			out, err := ops.Status()
			if err != nil {
				return errResultTyped[StatusOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_diagnose", Description: "structured findings with remediation", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, DiagnoseOut, error) {
			out, err := ops.Diagnose()
			if err != nil {
				return errResultTyped[DiagnoseOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_inspect", Description: "agent-readable bx inspection bundle from the CLI JSON surface", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in InspectIn) (*mcpsdk.CallToolResult, JSONCommandOut, error) {
			out, err := ops.Inspect(in)
			if err != nil {
				return errResultTyped[JSONCommandOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_leak_check", Description: "network-path leak check from the CLI JSON surface; outbound probes only when network=true. Browser-side checks are deliberately absent here: they need a person in front of the screen, and live in `bx leakcheck` instead", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in LeakCheckIn) (*mcpsdk.CallToolResult, JSONCommandOut, error) {
			out, err := ops.LeakCheck(in)
			if err != nil {
				return errResultTyped[JSONCommandOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_protection", Description: "what bx intends versus what the machine actually is: desired state, observed facts, their divergence, the reconcile loop's latest round, recovery and DNS state. Read-only, no root, no outbound probes. Use this when protection looks wrong but the tunnel looks fine", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, JSONCommandOut, error) {
			out, err := ops.Protection()
			if err != nil {
				return errResultTyped[JSONCommandOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_apps", Description: "which apps go through the tunnel, which go direct, and which are blocked, with the rule that decided it. Samples a short window (collection only runs while someone is looking). Read-only, no root. Executable paths are deliberately not included", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in AppsIn) (*mcpsdk.CallToolResult, JSONCommandOut, error) {
			out, err := ops.Apps(in)
			if err != nil {
				return errResultTyped[JSONCommandOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_explain", Description: "why a specific destination goes where it goes: the effective outcome (tunnel/direct/blocked), the routing decision behind it, the verbatim config rule that decided it, and that rule's success rate this run and across restarts. TCP and UDP answered separately. Read-only, no root, no outbound probes, no DNS. Use this when a request failed and you need to know whether bx is the reason", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in ExplainIn) (*mcpsdk.CallToolResult, JSONCommandOut, error) {
			out, err := ops.Explain(in)
			if err != nil {
				var te ToolError
				if errors.As(err, &te) {
					return errResultTyped[JSONCommandOut](te)
				}
				return errResultTyped[JSONCommandOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_observe", Description: "sample local bx runtime counters over a short window; no outbound probes or network changes", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in ObserveIn) (*mcpsdk.CallToolResult, JSONCommandOut, error) {
			out, err := ops.Observe(in)
			if err != nil {
				return errResultTyped[JSONCommandOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_check", Description: "run the safe bx verification bundle; outbound and browser probes are opt-in", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in CheckIn) (*mcpsdk.CallToolResult, CheckOut, error) {
			out, err := ops.Check(in)
			if err != nil {
				var te ToolError
				if errors.As(err, &te) {
					return errResultTyped[CheckOut](te)
				}
				return errResultTyped[CheckOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_logs", Description: "tail client logs for self-diagnosis", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in LogsIn) (*mcpsdk.CallToolResult, LogsOut, error) {
			out, err := ops.Logs(in)
			if err != nil {
				return errResultTyped[LogsOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})
}
