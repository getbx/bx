package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/getbx/bx/internal/guardian"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
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

func TestReconnectUsesGuardianRecoveryLifecycle(t *testing.T) {
	ops := &fakeOps{
		recoverySubmitted: guardian.RecoverySnapshot{
			ID: "recovery-mcp-1", State: "accepted", Stage: "queued", Reason: "manual",
		},
		recoveryCurrent: []guardian.RecoverySnapshot{{
			ID: "recovery-mcp-1", State: "succeeded", Stage: "succeeded", Reason: "manual",
		}},
	}
	srv := newServer(ops)
	res := callToolOn(t, srv, "bx_reconnect", map[string]any{})
	if res.IsError {
		t.Fatalf("Guardian recovery should not return an error: %+v", res.Content)
	}
	var out reconnectOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.RecoveryID != "recovery-mcp-1" || out.State != "succeeded" || out.Stage != "succeeded" || out.StillRunning {
		t.Fatalf("reconnect output = %+v", out)
	}
	if got, want := ops.calls, []string{"request_recovery", "current_recovery"}; !equalStrings(got, want) {
		t.Fatalf("calls=%v, want %v", got, want)
	}
}

func TestReconnectReturnsBoundedStillRunningResult(t *testing.T) {
	ops := &fakeOps{
		recoverySubmitted: guardian.RecoverySnapshot{
			ID: "recovery-mcp-2", State: "accepted", Stage: "queued", Reason: "manual",
		},
		recoveryCurrent: []guardian.RecoverySnapshot{{
			ID: "recovery-mcp-2", State: "running", Stage: "transport_health", Reason: "manual",
		}},
		recoveryPollLimit: 2,
		recoveryWait:      func(context.Context, time.Duration) error { return nil },
	}
	res := callToolOn(t, newServer(ops), "bx_reconnect", map[string]any{})
	if res.IsError {
		t.Fatalf("running recovery is an agent-readable result, not a tool error: %+v", res.Content)
	}
	var out reconnectOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.RecoveryID != "recovery-mcp-2" || out.State != "running" || out.Stage != "transport_health" || !out.StillRunning {
		t.Fatalf("bounded reconnect output = %+v", out)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
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
