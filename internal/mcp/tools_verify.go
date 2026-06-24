package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerVerify(s *mcpsdk.Server, ops Ops) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "bx_verify",
		Description: "leak audit: exit IP, DNS leak, IPv6 leak, self-reachability, kill-switch",
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, VerifyOut, error) {
		out, err := ops.Verify()
		if err != nil {
			return errResultTyped[VerifyOut](ToolError{Code: CodeLeakDetected, Message: err.Error(),
				Remediation: "检查隧道健康与路由劫持", Next: []string{"bx_diagnose", "bx_status"}})
		}
		return nil, out, nil
	})
}
