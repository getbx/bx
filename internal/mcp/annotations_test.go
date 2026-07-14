package mcp

import (
	"context"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestToolAnnotations 断言每个 MCP tool 的 ReadOnlyHint / DestructiveHint 与 spec §9 一致。
// Agent 依赖 DestructiveHint 决策是否需要人工确认,若注解缺失或错误则本测试失败。
func TestToolAnnotations(t *testing.T) {
	ctx := context.Background()
	srv := newServer(&fakeOps{})

	st, ct := mcpsdk.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-annot", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	// 建名称→工具的索引,方便逐一断言。
	byName := make(map[string]*mcpsdk.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}

	// 改动类工具:必须有 DestructiveHint == true。
	destructive := []string{"bx_set_transport", "bx_reconnect", "bx_rehijack", "bx_policy_apply"}
	for _, name := range destructive {
		tool, ok := byName[name]
		if !ok {
			t.Errorf("工具 %s 不在 ListTools 结果中", name)
			continue
		}
		if tool.Annotations == nil {
			t.Errorf("%s: Annotations 为 nil,期望 DestructiveHint=true", name)
			continue
		}
		if tool.Annotations.DestructiveHint == nil {
			t.Errorf("%s: DestructiveHint 为 nil(指针),期望 true", name)
			continue
		}
		if !*tool.Annotations.DestructiveHint {
			t.Errorf("%s: DestructiveHint = false,期望 true", name)
		}
	}

	// 只读工具:必须有 ReadOnlyHint == true。
	readonly := []string{"bx_capabilities", "bx_status", "bx_diagnose", "bx_inspect", "bx_leak_check", "bx_observe", "bx_logs"}
	for _, name := range readonly {
		tool, ok := byName[name]
		if !ok {
			t.Errorf("工具 %s 不在 ListTools 结果中", name)
			continue
		}
		if tool.Annotations == nil {
			t.Errorf("%s: Annotations 为 nil,期望 ReadOnlyHint=true", name)
			continue
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s: ReadOnlyHint = false,期望 true", name)
		}
	}

	// 不注册尚未接线的占位工具。agent 只能看到真实可执行的能力。
	for _, name := range []string{"bx_setup", "bx_plan", "bx_verify", "bx_restart_tunnel"} {
		if _, ok := byName[name]; ok {
			t.Errorf("占位或旧工具 %s 不应暴露给 agent", name)
		}
	}
}
