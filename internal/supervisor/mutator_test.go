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
	m := &liveMutator{
		plat: fp, tunH: tunHandle{Name: "bx0"},
		serverBypass: []string{"1.1.1.1/32"}, userBypass: []string{"2.2.2.2/32"},
	}

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

type fakeSwapper struct {
	cur       string
	swapCalls []string
	swapErr   error
}

func (f *fakeSwapper) currentLink() string { return f.cur }
func (f *fakeSwapper) swapTo(link string) error {
	f.swapCalls = append(f.swapCalls, link)
	if f.swapErr != nil {
		return f.swapErr
	}
	f.cur = link // 仅成功才更新当前 link
	return nil
}

func TestLiveMutatorSetTransport(t *testing.T) {
	fs := &fakeSwapper{cur: "brook://old"}
	m := &liveMutator{swap: fs}

	apply, undo, err := m.SetTransport("brook://new")
	if err != nil {
		t.Fatalf("SetTransport err: %v", err)
	}
	if len(fs.swapCalls) != 0 {
		t.Fatalf("方法体应无副作用: swapCalls=%v", fs.swapCalls)
	}
	if err := apply(); err != nil {
		t.Fatalf("apply err: %v", err)
	}
	if len(fs.swapCalls) != 1 || fs.swapCalls[0] != "brook://new" {
		t.Fatalf("apply 应 swapTo(new) 一次, got %v", fs.swapCalls)
	}
	if err := undo(); err != nil {
		t.Fatalf("undo err: %v", err)
	}
	if len(fs.swapCalls) != 2 || fs.swapCalls[1] != "brook://old" {
		t.Fatalf("换过后 undo 应 swapTo(old), got %v", fs.swapCalls)
	}
}

func TestLiveMutatorReconnectUsesCurrentLink(t *testing.T) {
	fs := &fakeSwapper{cur: "reality://current"}
	if err := (&liveMutator{swap: fs}).Reconnect(); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	if len(fs.swapCalls) != 1 || fs.swapCalls[0] != "reality://current" {
		t.Fatalf("swap calls=%v", fs.swapCalls)
	}
}

func TestLiveMutatorReconnectFailureKeepsCurrentLink(t *testing.T) {
	wantErr := errors.New("unhealthy")
	fs := &fakeSwapper{cur: "reality://current", swapErr: wantErr}
	err := (&liveMutator{swap: fs}).Reconnect()
	if !errors.Is(err, wantErr) {
		t.Fatalf("Reconnect err=%v want %v", err, wantErr)
	}
	if fs.cur != "reality://current" {
		t.Fatalf("current link changed to %q", fs.cur)
	}
}

func TestLiveMutatorSetTransportApplyFailUndoNop(t *testing.T) {
	wantErr := errors.New("unhealthy")
	fs := &fakeSwapper{cur: "brook://old", swapErr: wantErr}
	m := &liveMutator{swap: fs}

	apply, undo, _ := m.SetTransport("brook://new")
	if err := apply(); !errors.Is(err, wantErr) {
		t.Fatalf("apply 应透传 swapTo 错误, got %v", err)
	}
	before := len(fs.swapCalls)
	if err := undo(); err != nil {
		t.Fatalf("undo 应 nil: %v", err)
	}
	if len(fs.swapCalls) != before {
		t.Fatalf("apply 未换成时 undo 应 nop, swapCalls 多了: %v", fs.swapCalls)
	}
}
