package supervisor

import (
	"errors"
	"testing"
	"time"

	"github.com/getbx/bx/internal/confirm"
)

type engFakeSnap struct{}

func (engFakeSnap) ID() string { return "fake" }

type engFakeSnapper struct {
	captureErr error
	captures   int
	restores   int
}

func (s *engFakeSnapper) Capture() (confirm.Snapshot, error) {
	s.captures++
	if s.captureErr != nil {
		return nil, s.captureErr
	}
	return engFakeSnap{}, nil
}
func (s *engFakeSnapper) Restore(confirm.Snapshot) error { s.restores++; return nil }

type engClock struct{ t time.Time }

func (c *engClock) now() time.Time { return c.t }

func newTestEngine(snapper confirm.Snapshotter, clk *engClock) *mutationEngine {
	return newMutationEngine(snapper, 240*time.Second, clk.now)
}

func TestEngineArmCaptureFailDoesNotApply(t *testing.T) {
	snapper := &engFakeSnapper{captureErr: errors.New("boom")}
	e := newTestEngine(snapper, &engClock{t: time.Unix(0, 0)})
	applied := false
	err := e.Arm(func() error { applied = true; return nil }, nil)
	if err == nil {
		t.Fatal("capture 失败应返回错误")
	}
	if applied {
		t.Fatal("capture 失败不应调用 apply")
	}
	if e.State() != confirm.StateIdle {
		t.Fatalf("应保持 Idle,得 %v", e.State())
	}
}

func TestEngineArmApplyFailReverts(t *testing.T) {
	snapper := &engFakeSnapper{}
	e := newTestEngine(snapper, &engClock{t: time.Unix(0, 0)})
	undoCalled := false
	err := e.Arm(
		func() error { return errors.New("apply boom") },
		func() error { undoCalled = true; return nil },
	)
	if err == nil {
		t.Fatal("apply 失败应返回错误")
	}
	if !undoCalled {
		t.Fatal("apply 失败应调用 undo")
	}
	if snapper.restores != 1 {
		t.Fatalf("apply 失败应调用快照 Restore 一次,得 %d", snapper.restores)
	}
	if e.State() != confirm.StateReverted {
		t.Fatalf("应 Reverted,得 %v", e.State())
	}
}

func TestEngineArmCommitDisarms(t *testing.T) {
	snapper := &engFakeSnapper{}
	clk := &engClock{t: time.Unix(0, 0)}
	e := newTestEngine(snapper, clk)
	if err := e.Arm(func() error { return nil }, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if e.State() != confirm.StateArmed {
		t.Fatalf("apply 成功应 Armed,得 %v", e.State())
	}
	if err := e.Commit(); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(300 * time.Second)
	if rev, _ := e.tick(); rev {
		t.Fatal("已 commit 不应回滚")
	}
	if snapper.restores != 0 {
		t.Fatalf("已 commit 不应 Restore,得 %d", snapper.restores)
	}
	if e.State() != confirm.StateCommitted {
		t.Fatalf("应 Committed,得 %v", e.State())
	}
}

func TestEngineNoCommitAutoReverts(t *testing.T) {
	snapper := &engFakeSnapper{}
	clk := &engClock{t: time.Unix(0, 0)}
	e := newTestEngine(snapper, clk)
	undoCalled := false
	if err := e.Arm(func() error { return nil }, func() error { undoCalled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(241 * time.Second)
	rev, err := e.tick()
	if err != nil {
		t.Fatal(err)
	}
	if !rev {
		t.Fatal("未 commit 到点应自动回滚")
	}
	if !undoCalled || snapper.restores != 1 {
		t.Fatalf("回滚应调 undo+Restore(undo=%v restores=%d)", undoCalled, snapper.restores)
	}
	if e.State() != confirm.StateReverted {
		t.Fatalf("应 Reverted,得 %v", e.State())
	}
}

func TestEngineRollbackIdle(t *testing.T) {
	e := newTestEngine(&engFakeSnapper{}, &engClock{t: time.Unix(0, 0)})
	if err := e.Rollback(); !errors.Is(err, confirm.ErrNotArmed) {
		t.Fatalf("idle 回滚应 ErrNotArmed,得 %v", err)
	}
}
