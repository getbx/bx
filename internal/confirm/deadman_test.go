package confirm

import (
	"errors"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time         { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestCommitDisarms(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	g := New(240*time.Second, c.now)
	restored := false
	if err := g.Arm(func() error { restored = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := g.Commit(); err != nil {
		t.Fatal(err)
	}
	c.advance(300 * time.Second)
	if rev, _ := g.Tick(); rev {
		t.Fatal("已 commit 不应再回滚")
	}
	if restored {
		t.Fatal("已 commit 不应触发 restore")
	}
	if g.State() != StateCommitted {
		t.Fatalf("state=%v want Committed", g.State())
	}
}

func TestNoCommitAutoReverts(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	g := New(240*time.Second, c.now)
	restored := false
	g.Arm(func() error { restored = true; return nil })

	c.advance(239 * time.Second)
	if rev, _ := g.Tick(); rev {
		t.Fatal("未到期不应回滚")
	}
	c.advance(2 * time.Second) // 越过 240s
	rev, err := g.Tick()
	if err != nil {
		t.Fatal(err)
	}
	if !rev || !restored {
		t.Fatalf("到期应自动回滚(rev=%v restored=%v)", rev, restored)
	}
	if g.State() != StateReverted {
		t.Fatalf("state=%v want Reverted", g.State())
	}
}

func TestDoubleArmRejected(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	g := New(240*time.Second, c.now)
	g.Arm(func() error { return nil })
	if err := g.Arm(func() error { return nil }); !errors.Is(err, ErrAlreadyArmed) {
		t.Fatalf("重复 Arm 应返回 ErrAlreadyArmed,得到 %v", err)
	}
}

func TestCommitWhenIdleRejected(t *testing.T) {
	g := New(240*time.Second, (&clock{t: time.Unix(0, 0)}).now)
	if err := g.Commit(); !errors.Is(err, ErrNotArmed) {
		t.Fatalf("idle 时 Commit 应返回 ErrNotArmed,得到 %v", err)
	}
}
