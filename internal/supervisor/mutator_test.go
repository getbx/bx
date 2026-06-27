package supervisor

import (
	"errors"
	"testing"
)

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

// fakePlatform 只实现 liveMutator 依赖的窄接口 rehijacker(免 gVisor 依赖)。
type fakePlatform struct {
	hijackCalls int
	hijackErr   error
	newTD       func() // Hijack 成功时返回的新 teardown
}

func (f *fakePlatform) Hijack(tunHandle, []string, []string) (func(), error) {
	f.hijackCalls++
	if f.hijackErr != nil {
		return nil, f.hijackErr
	}
	return f.newTD, nil
}

func TestLiveMutatorRehijack(t *testing.T) {
	var oldCalls, newCalls int
	oldTD := func() { oldCalls++ }
	newTD := func() { newCalls++ }
	teardown := oldTD
	fp := &fakePlatform{newTD: newTD}
	m := &liveMutator{plat: fp, teardown: &teardown}

	apply, undo, err := m.Rehijack()
	if err != nil {
		t.Fatalf("Rehijack err: %v", err)
	}
	// A2 无副作用契约:方法体不得触发任何改动
	if fp.hijackCalls != 0 || oldCalls != 0 {
		t.Fatalf("方法体应无副作用: hijackCalls=%d oldCalls=%d", fp.hijackCalls, oldCalls)
	}

	if err := apply(); err != nil {
		t.Fatalf("apply err: %v", err)
	}
	if oldCalls != 1 {
		t.Fatalf("apply 应拆旧劫持一次, got %d", oldCalls)
	}
	if fp.hijackCalls != 1 {
		t.Fatalf("apply 应调 plat.Hijack 一次, got %d", fp.hijackCalls)
	}
	// 收养:此刻 *teardown 应已是 newTD —— 调一次看新计数器涨、旧的不涨
	(*m.teardown)()
	if newCalls != 1 || oldCalls != 1 {
		t.Fatalf("apply 后应收养新 teardown: newCalls=%d oldCalls=%d", newCalls, oldCalls)
	}

	if err := undo(); err != nil {
		t.Fatalf("undo 应 nil: %v", err)
	}
}

func TestLiveMutatorRehijackHijackError(t *testing.T) {
	var oldCalls int
	oldTD := func() { oldCalls++ }
	teardown := oldTD
	wantErr := errors.New("boom")
	fp := &fakePlatform{hijackErr: wantErr}
	m := &liveMutator{plat: fp, teardown: &teardown}

	apply, _, _ := m.Rehijack()
	if err := apply(); !errors.Is(err, wantErr) {
		t.Fatalf("apply 应透传 Hijack 错误, got %v", err)
	}
	// 失败不覆盖 teardown:仍指向旧值(交快照网接管)。
	// apply 内已调旧 teardown 一次(oldCalls=1);此处再手调一次应到 2,证明仍是旧的。
	(*m.teardown)()
	if oldCalls != 2 {
		t.Fatalf("Hijack 失败应保持旧 teardown 不被覆盖: oldCalls=%d", oldCalls)
	}
}

func TestLiveMutatorSetTransportNop(t *testing.T) {
	m := &liveMutator{}
	apply, undo, err := m.SetTransport("vless://x@h:443")
	if err != nil {
		t.Fatalf("SetTransport err: %v", err)
	}
	if apply() != nil || undo() != nil {
		t.Fatal("liveMutator.SetTransport 应为 nop(经嵌入 nopMutator)")
	}
}
