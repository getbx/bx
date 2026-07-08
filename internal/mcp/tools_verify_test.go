package mcp

import (
	"encoding/json"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestVerifyToolPass(t *testing.T) {
	ops := &fakeOps{verify: VerifyOut{
		Pass: true, ExitIP: "203.0.113.9", SelfReach: true, KillSwitchOK: true,
		Note: "WebRTC 需 LAN 客户端浏览器测,未自动化",
	}}
	res := callTool(t, ops, "bx_verify", map[string]any{})
	if res.IsError {
		t.Fatal("不应错误")
	}
	var out VerifyOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Pass || out.ExitIP != "203.0.113.9" {
		t.Fatalf("got %+v", out)
	}
}
