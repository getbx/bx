// Package mcp 暴露 bx 的 agent 可操作控制面(MCP server over stdio)。
package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// version 由构建期注入也可;先硬编码占位。
const serverVersion = "v0.1.0"

type pingIn struct{}
type pingOut struct {
	OK bool `json:"ok" jsonschema:"always true if the server is alive"`
}

// newServer 构造已注册 tool 的 MCP server(不连 transport,供测试与 Serve 共用)。
func newServer(ops Ops) *mcpsdk.Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "bx", Version: serverVersion}, nil)
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "bx_ping",
		Description: "liveness probe; returns ok=true",
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ pingIn) (*mcpsdk.CallToolResult, pingOut, error) {
		return nil, pingOut{OK: true}, nil
	})
	if ops != nil {
		registerReadOnly(s, ops)
		registerVerify(s, ops)
	}
	return s
}

// Serve 在 stdio 上运行 MCP server,直到客户端断开。
func Serve(ctx context.Context, ops Ops) error {
	return newServer(ops).Run(ctx, &mcpsdk.StdioTransport{})
}
