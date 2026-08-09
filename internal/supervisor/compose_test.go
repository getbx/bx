package supervisor

import (
	"errors"
	"testing"
	"time"
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

// 经**真引擎**跑一遍:apply 失败时 mutationEngine.Arm 会再调一次 guard.Rollback,
// 而组合后的 apply 自己已经回滚过第一步。若 undo 不记「第一步还在不在」,
// u1 就会被执行两次 —— 对「拆路由」这类操作,第二次是在已经拆掉的东西上再拆。
func TestComposeMutationsUndoesFirstStepOnlyOnceThroughEngine(t *testing.T) {
	var log []string
	a1 := func() error { log = append(log, "a1"); return nil }
	u1 := func() error { log = append(log, "u1"); return nil }
	a2 := func() error { log = append(log, "a2"); return errors.New("boom") }
	u2 := func() error { log = append(log, "u2"); return nil }

	eng := newTestEngine(&engFakeSnapper{}, &engClock{t: time.Unix(0, 0)})
	apply, undo := composeMutations(a1, u1, a2, u2)
	if err := eng.Arm(apply, undo); err == nil {
		t.Fatal("apply 失败,Arm 必须报错")
	}
	var u1s int
	for _, e := range log {
		if e == "u1" {
			u1s++
		}
	}
	if u1s != 1 {
		t.Fatalf("第一步只该回滚一次, got %v", log)
	}
}

// 但正常路径(apply 成功后 commit 窗口内回滚 / 死手到点)必须照常回滚第一步。
func TestComposeMutationsUndoStillReversesFirstStepAfterSuccessfulApply(t *testing.T) {
	var log []string
	nopf := func() error { return nil }
	u1 := func() error { log = append(log, "u1"); return nil }
	u2 := func() error { log = append(log, "u2"); return nil }
	apply, undo := composeMutations(nopf, u1, nopf, u2)
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[0] != "u2" || log[1] != "u1" {
		t.Fatalf("apply 成功后 undo 必须逆序回滚两步, got %v", log)
	}
}
