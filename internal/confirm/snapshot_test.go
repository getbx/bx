package confirm

import (
	"errors"
	"testing"
	"time"
)

type fakeSnap struct{ id string }

func (f fakeSnap) ID() string { return f.id }

type fakeSnapper struct {
	captureErr error
	restored   []string
}

func (f *fakeSnapper) Capture() (Snapshot, error) {
	if f.captureErr != nil {
		return nil, f.captureErr
	}
	return fakeSnap{id: "good-1"}, nil
}
func (f *fakeSnapper) Restore(s Snapshot) error { f.restored = append(f.restored, s.ID()); return nil }

func TestArmWithSnapshotCapturesThenArms(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	g := New(240*time.Second, c.now)
	fs := &fakeSnapper{}
	snap, err := ArmWithSnapshot(g, fs)
	if err != nil || snap.ID() != "good-1" {
		t.Fatalf("snap=%v err=%v", snap, err)
	}
	if g.State() != StateArmed {
		t.Fatal("应已 Armed")
	}
	c.advance(241 * time.Second)
	g.Tick()
	if len(fs.restored) != 1 || fs.restored[0] != "good-1" {
		t.Fatalf("到期应 Restore 到 good-1,得到 %v", fs.restored)
	}
}

func TestCaptureFailDoesNotArm(t *testing.T) {
	g := New(240*time.Second, (&clock{t: time.Unix(0, 0)}).now)
	fs := &fakeSnapper{captureErr: errors.New("boom")}
	if _, err := ArmWithSnapshot(g, fs); err == nil {
		t.Fatal("Capture 失败应报错")
	}
	if g.State() != StateIdle {
		t.Fatal("Capture 失败不应 Arm")
	}
}
