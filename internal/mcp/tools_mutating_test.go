package mcp

import (
	"testing"
)

func TestRollbackWhenIdle(t *testing.T) {
	ops := &fakeOps{rollbackErr: ToolError{Code: CodeNothingToRollback, Message: "没有可回滚的改动"}}
	srv := newServer(ops)
	res := callToolOn(t, srv, "bx_rollback", map[string]any{})
	if !res.IsError {
		t.Fatal("idle 时 rollback 应返回错误结果(NOTHING_TO_ROLLBACK)")
	}
}

func TestCommitUsesOps(t *testing.T) {
	ops := &fakeOps{}
	srv := newServer(ops)
	res := callToolOn(t, srv, "bx_commit", map[string]any{})
	if res.IsError {
		t.Fatal("commit 不应错误")
	}
	if len(ops.calls) != 1 || ops.calls[0] != "commit" {
		t.Fatalf("calls=%v want [commit]", ops.calls)
	}
}

func TestRollbackUsesOps(t *testing.T) {
	ops := &fakeOps{}
	srv := newServer(ops)
	res := callToolOn(t, srv, "bx_rollback", map[string]any{})
	if res.IsError {
		t.Fatal("rollback 不应错误")
	}
	if len(ops.calls) != 1 || ops.calls[0] != "rollback" {
		t.Fatalf("calls=%v want [rollback]", ops.calls)
	}
}

func TestSetTransportToolForwardsToOps(t *testing.T) {
	ops := &fakeOps{}
	res := callTool(t, ops, "bx_set_transport", map[string]any{"link": "vless://x@h:443"})
	if res.IsError {
		t.Fatalf("不应错误")
	}
	if ops.lastSetTransportLink != "vless://x@h:443" {
		t.Fatalf("应转发 link 给 ops.SetTransport,得 %q", ops.lastSetTransportLink)
	}
}

func TestRehijackToolForwardsToOps(t *testing.T) {
	ops := &fakeOps{}
	res := callTool(t, ops, "bx_rehijack", map[string]any{})
	if res.IsError {
		t.Fatalf("不应错误")
	}
	if !ops.rehijackCalled {
		t.Fatal("应调用 ops.Rehijack")
	}
}

func TestReconnectSafelyWithoutArming(t *testing.T) {
	ops := &fakeOps{}
	srv := newServer(ops)
	res := callToolOn(t, srv, "bx_reconnect", map[string]any{})
	if res.IsError {
		t.Fatal("safe reconnect should not return an error")
	}
	if len(ops.calls) != 1 || ops.calls[0] != "reconnect" {
		t.Fatalf("calls=%v, want [reconnect]", ops.calls)
	}
}

func TestPolicyApplyIsRegisteredAsDestructive(t *testing.T) {
	ops := &fakeOps{policyApplyOut: PolicyApplyOut{Changed: true, State: "reloaded"}}
	res := callTool(t, ops, "bx_policy_apply", map[string]any{
		"mode": "proxy",
		"add":  []string{"example.com"},
	})
	if res.IsError {
		t.Fatal("policy apply should be available to an approved agent")
	}
	if len(ops.calls) != 1 || ops.calls[0] != "policy_apply" || ops.policyApply.Mode != "proxy" {
		t.Fatalf("policy apply was not forwarded: calls=%v input=%+v", ops.calls, ops.policyApply)
	}
}
