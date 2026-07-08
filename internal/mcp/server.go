// Package mcp 暴露 bx 的 agent 可操作控制面(MCP server over stdio)。
package mcp

import (
	"context"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/getbx/bx/internal/confirm"
)

// version 由构建期注入也可;先硬编码占位。
const serverVersion = "v0.1.0"

type (
	pingIn  struct{}
	pingOut struct {
		OK bool `json:"ok" jsonschema:"always true if the server is alive"`
	}
)

// nopSnapshotter 用于只读/单元场景:不抓真实状态。
type (
	nopSnapshotter struct{}
	nopSnap        struct{}
)

func (nopSnap) ID() string                                { return "nop" }
func (nopSnapshotter) Capture() (confirm.Snapshot, error) { return nopSnap{}, nil }
func (nopSnapshotter) Restore(confirm.Snapshot) error     { return nil }

// newServer 构造已注册 tool 的 MCP server(不连 transport,供测试与 Serve 共用)。
// 便捷封装:使用默认 in-process guard + nopSnapshotter。
func newServer(ops Ops) *mcpsdk.Server {
	g := confirm.New(240*time.Second, time.Now)
	return newServerWithGuard(ops, g, nopSnapshotter{})
}

// newServerWithGuard 构造 MCP server,注入外部 Guard 和 Snapshotter(供测试注入假时钟)。
func newServerWithGuard(ops Ops, g *confirm.Guard, snap confirm.Snapshotter) *mcpsdk.Server {
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
		registerMutating(s, ops, g, snap)
	}
	return s
}

// newSystemSnapshotter 返回一个 nopSnapshotter(真实路由/config 快照实现留 Task 9)。
func newSystemSnapshotter() confirm.Snapshotter { return nopSnapshotter{} }

// Serve 在 stdio 上运行 MCP server,直到客户端断开。
// 内部持有 Guard 并起后台 tickLoop(每 2s Tick),驱动死手到期自动回滚。
func Serve(ctx context.Context, ops Ops) error {
	g := confirm.New(240*time.Second, time.Now)
	srv := newServerWithGuard(ops, g, newSystemSnapshotter())
	go tickLoop(ctx, g)
	return srv.Run(ctx, &mcpsdk.StdioTransport{})
}

// tickLoop 每 2s 调用 g.Tick(),驱动死手到期自动回滚。ctx 取消时退出。
func tickLoop(ctx context.Context, g *confirm.Guard) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = g.Tick()
		}
	}
}
