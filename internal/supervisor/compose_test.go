package supervisor

import (
	"errors"
	"testing"
)

func TestComposeMutationsRollsBackFirstWhenSecondFails(t *testing.T) {
	var log []string
	a1 := func() error { log = append(log, "a1"); return nil }
	u1 := func() error { log = append(log, "u1"); return nil }
	a2 := func() error { log = append(log, "a2"); return errors.New("boom") }
	u2 := func() error { log = append(log, "u2"); return nil }

	apply, _ := composeMutations(a1, u1, a2, u2)
	if err := apply(); err == nil {
		t.Fatal("第二步失败时整体必须失败")
	}
	want := []string{"a1", "a2", "u1"}
	if len(log) != len(want) {
		t.Fatalf("要回滚已执行的第一步, got %v", log)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Fatalf("顺序不对, got %v want %v", log, want)
		}
	}
}

func TestComposeMutationsUndoRunsInReverse(t *testing.T) {
	var log []string
	nopf := func() error { return nil }
	u1 := func() error { log = append(log, "u1"); return nil }
	u2 := func() error { log = append(log, "u2"); return nil }
	_, undo := composeMutations(nopf, u1, nopf, u2)
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[0] != "u2" || log[1] != "u1" {
		t.Fatalf("undo 必须逆序, got %v", log)
	}
}
