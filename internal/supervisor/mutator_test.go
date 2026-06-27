package supervisor

import "testing"

func TestNopMutator(t *testing.T) {
	var m mutator = nopMutator{}
	apply, undo, err := m.SetTransport("vless://x@h:443")
	if err != nil {
		t.Fatalf("SetTransport err: %v", err)
	}
	if apply == nil || undo == nil {
		t.Fatal("apply/undo 不应为 nil 闭包")
	}
	if err := apply(); err != nil {
		t.Fatalf("nop apply 应 nil: %v", err)
	}
	if err := undo(); err != nil {
		t.Fatalf("nop undo 应 nil: %v", err)
	}
	a2, u2, err := m.Rehijack()
	if err != nil || a2() != nil || u2() != nil {
		t.Fatalf("Rehijack nop 应全 nil: err=%v", err)
	}
}
