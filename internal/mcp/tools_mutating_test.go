package mcp

import (
	"errors"
	"testing"
	"time"

	"github.com/getbx/bx/internal/confirm"
)

type memSnap struct{ id string }

func (m memSnap) ID() string { return m.id }

type memSnapper struct{ restored int }

func (m *memSnapper) Capture() (confirm.Snapshot, error) { return memSnap{id: "lkg"}, nil }
func (m *memSnapper) Restore(confirm.Snapshot) error     { m.restored++; return nil }

type tclock struct{ t time.Time }

func (c *tclock) now() time.Time { return c.t }

func TestSetupArmsThenCommit(t *testing.T) {
	clk := &tclock{t: time.Unix(0, 0)}
	g := confirm.New(240*time.Second, clk.now)
	snap := &memSnapper{}
	ops := &fakeOps{}
	srv := newServerWithGuard(ops, g, snap)
	res := callToolOn(t, srv, "bx_setup", map[string]any{"link": "vless://x@h:443"})
	if res.IsError {
		t.Fatal("setup 不应错误")
	}
	if g.State() != confirm.StateArmed {
		t.Fatal("setup 后应 Armed")
	}
	res = callToolOn(t, srv, "bx_commit", map[string]any{})
	if res.IsError {
		t.Fatal("commit 不应错误")
	}
	if len(ops.calls) != 2 || ops.calls[1] != "commit" {
		t.Fatalf("calls=%v want setup then commit", ops.calls)
	}
}

func TestSetupNoCommitWouldRevert(t *testing.T) {
	clk := &tclock{t: time.Unix(0, 0)}
	g := confirm.New(240*time.Second, clk.now)
	snap := &memSnapper{}
	srv := newServerWithGuard(&fakeOps{}, g, snap)
	callToolOn(t, srv, "bx_setup", map[string]any{"link": "vless://x@h:443"})
	clk.t = clk.t.Add(241 * time.Second)
	if rev, _ := g.Tick(); !rev || snap.restored != 1 {
		t.Fatalf("未 commit 到期应回滚(rev=%v restored=%d)", rev, snap.restored)
	}
}

func TestRollbackWhenIdle(t *testing.T) {
	g := confirm.New(240*time.Second, (&tclock{t: time.Unix(0, 0)}).now)
	ops := &fakeOps{rollbackErr: ToolError{Code: CodeNothingToRollback, Message: "没有可回滚的改动"}}
	srv := newServerWithGuard(ops, g, &memSnapper{})
	res := callToolOn(t, srv, "bx_rollback", map[string]any{})
	if !res.IsError {
		t.Fatal("idle 时 rollback 应返回错误结果(NOTHING_TO_ROLLBACK)")
	}
}

func TestCommitUsesOps(t *testing.T) {
	ops := &fakeOps{}
	g := confirm.New(240*time.Second, (&tclock{t: time.Unix(0, 0)}).now)
	srv := newServerWithGuard(ops, g, &memSnapper{})
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
	g := confirm.New(240*time.Second, (&tclock{t: time.Unix(0, 0)}).now)
	srv := newServerWithGuard(ops, g, &memSnapper{})
	res := callToolOn(t, srv, "bx_rollback", map[string]any{})
	if res.IsError {
		t.Fatal("rollback 不应错误")
	}
	if len(ops.calls) != 1 || ops.calls[0] != "rollback" {
		t.Fatalf("calls=%v want [rollback]", ops.calls)
	}
}

func TestSetupApplyFailRollsBack(t *testing.T) {
	clk := &tclock{t: time.Unix(0, 0)}
	g := confirm.New(240*time.Second, clk.now)
	snap := &memSnapper{}
	ops := &fakeOps{setupErr: errors.New("apply boom")}
	srv := newServerWithGuard(ops, g, snap)
	res := callToolOn(t, srv, "bx_setup", map[string]any{"link": "vless://x@h:443"})
	if !res.IsError {
		t.Fatal("apply 失败应返回错误结果")
	}
	if g.State() != confirm.StateReverted {
		t.Fatalf("apply 失败后 guard 应已回滚(state=%v)", g.State())
	}
	if snap.restored != 1 {
		t.Fatalf("apply 失败应触发一次 Restore(restored=%d)", snap.restored)
	}
}
