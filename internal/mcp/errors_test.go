package mcp

import (
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolErrorErrorString(t *testing.T) {
	e := ToolError{Code: CodeTunnelUnhealthy, Message: "443 握手超时"}
	if !strings.Contains(e.Error(), "TUNNEL_UNHEALTHY") {
		t.Fatalf("Error() 应含错误码,得到 %q", e.Error())
	}
}

func TestErrResultIsError(t *testing.T) {
	res, _, err := errResult(ToolError{
		Code: CodeLinkInvalid, Message: "bad link",
		Remediation: "检查 vless:// 链接", Next: []string{"bx_diagnose"},
	})
	if err != nil {
		t.Fatalf("errResult 不应返回 Go error(工具错误走 IsError),得到 %v", err)
	}
	if !res.IsError {
		t.Fatalf("应 IsError=true")
	}
	found := false
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok && strings.Contains(tc.Text, "LINK_INVALID") {
			found = true
		}
	}
	if !found {
		t.Fatalf("错误内容应含 LINK_INVALID")
	}
}
