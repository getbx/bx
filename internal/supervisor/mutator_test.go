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

type fakePlatform struct {
	rehijackCalls int
	gotTun        tunHandle
	gotServer     []string
	gotUser       []string
	rehijackErr   error
}

func (f *fakePlatform) RehijackRoutes(t tunHandle, serverBypass, userBypass []string) error {
	f.rehijackCalls++
	f.gotTun = t
	f.gotServer = serverBypass
	f.gotUser = userBypass
	return f.rehijackErr
}

func TestLiveMutatorRehijack(t *testing.T) {
	fp := &fakePlatform{}
	m := &liveMutator{plat: fp, tunH: tunHandle{Name: "bx0"},
		serverBypass: []string{"1.1.1.1/32"}, userBypass: []string{"2.2.2.2/32"}}

	apply, undo, err := m.Rehijack()
	if err != nil {
		t.Fatalf("Rehijack err: %v", err)
	}
	if fp.rehijackCalls != 0 {
		t.Fatalf("方法体应无副作用: rehijackCalls=%d", fp.rehijackCalls)
	}
	if err := apply(); err != nil {
		t.Fatalf("apply err: %v", err)
	}
	if fp.rehijackCalls != 1 {
		t.Fatalf("apply 应调 RehijackRoutes 一次, got %d", fp.rehijackCalls)
	}
	if fp.gotTun.Name != "bx0" || len(fp.gotServer) != 1 || fp.gotServer[0] != "1.1.1.1/32" ||
		len(fp.gotUser) != 1 || fp.gotUser[0] != "2.2.2.2/32" {
		t.Fatalf("apply 传参不对: tun=%v server=%v user=%v", fp.gotTun, fp.gotServer, fp.gotUser)
	}
	if err := undo(); err != nil {
		t.Fatalf("undo 应 nil: %v", err)
	}
}

func TestLiveMutatorRehijackError(t *testing.T) {
	wantErr := errors.New("boom")
	fp := &fakePlatform{rehijackErr: wantErr}
	m := &liveMutator{plat: fp}
	apply, _, _ := m.Rehijack()
	if err := apply(); !errors.Is(err, wantErr) {
		t.Fatalf("apply 应透传 RehijackRoutes 错误, got %v", err)
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
