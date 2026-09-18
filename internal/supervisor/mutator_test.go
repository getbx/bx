package supervisor

import (
	"errors"
	"fmt"
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
	rehijackCalls    int
	gotTun           tunHandle
	gotServer        []string
	gotUser          []string
	rehijackErr      error
	lastServerBypass []string
}

func (f *fakePlatform) RehijackRoutes(t tunHandle, serverBypass, userBypass []string) error {
	f.rehijackCalls++
	f.gotTun = t
	f.gotServer = serverBypass
	f.gotUser = userBypass
	f.lastServerBypass = serverBypass
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

type blockingRehijacker struct {
	started chan struct{}
	release chan error
}

func (f blockingRehijacker) RehijackRoutes(tunHandle, []string, []string) error {
	close(f.started)
	return <-f.release
}

func TestLiveMutatorRehijackMarksRoutesNotReadyDuringMutation(t *testing.T) {
	routes := &routeReadiness{}
	routes.set(true)
	fp := blockingRehijacker{started: make(chan struct{}), release: make(chan error)}
	m := &liveMutator{plat: fp, routes: routes}
	apply, _, err := m.Rehijack()
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- apply() }()
	<-fp.started
	if routes.ready() {
		t.Fatal("routes remained ready while rehijack was mutating them")
	}
	fp.release <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLiveMutatorRehijackMarksRoutesReadyAfterSuccess(t *testing.T) {
	routes := &routeReadiness{}
	m := &liveMutator{plat: &fakePlatform{}, routes: routes}
	apply, _, err := m.Rehijack()
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	if !routes.ready() {
		t.Fatal("routes were not ready after successful rehijack")
	}
}

func TestLiveMutatorRehijackLeavesRoutesNotReadyAfterFailure(t *testing.T) {
	wantErr := errors.New("partial route install")
	routes := &routeReadiness{}
	routes.set(true)
	m := &liveMutator{plat: &fakePlatform{rehijackErr: wantErr}, routes: routes}
	apply, _, err := m.Rehijack()
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(); !errors.Is(err, wantErr) {
		t.Fatalf("apply error = %v, want %v", err, wantErr)
	}
	if routes.ready() {
		t.Fatal("routes were ready after failed rehijack")
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

// 一次**在动任何一条路由之前**就失败的 rehijack,不许把路由就绪位打脏。
//
// 真机代价(2026-09-17,项目所有者的 Mac):13:04:43 `server_bypass_refollow`
// 触发一次 Rehijack,darwin 的 RehijackRoutes 第一句探默认网关就失败
// (`解析默认路由失败: ""`),**一条路由都没碰过** —— 而 apply 已经先把就绪位
// 清成 false,且全仓只有「Hijack 成功」与「Rehijack 成功」两处会把它设回来,
// 于是它一直假到 Core 重启。后果不是显示错一行:
//
//   - 路径恢复自己的 verify 就读这一位(run.go 那个闭包),于是此后每一次恢复
//     都以 verification_failed 告终 —— 那天连败 20 次,而机器全程受保护;
//   - Guardian 的 health 门也读它,于是 `bx update` 永久停在
//     update_runtime_refresh_failed,**在 Core 重启之前升不了级**。
//
// 与下面那条「失败之后就绪位必须是假」是一对,**两条都要**:少了这一条,
// 缺陷原样回来;少了那一条,「apply 干脆不碰这一位」也能全绿,而那会让一次
// 拆到一半的 rehijack 谎报路由完好 —— 方向相反,代价更大。
func TestLiveMutatorRehijackKeepsRoutesReadyWhenNothingWasTouched(t *testing.T) {
	inner := errors.New("probing the default gateway: empty default route")
	routes := &routeReadiness{}
	routes.set(true)
	m := &liveMutator{
		plat:   &fakePlatform{rehijackErr: fmt.Errorf("%w: %w", ErrRehijackNoChange, inner)},
		routes: routes,
	}
	apply, _, err := m.Rehijack()
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(); !errors.Is(err, ErrRehijackNoChange) || !errors.Is(err, inner) {
		t.Fatalf("apply error = %v, want one wrapping both ErrRehijackNoChange and the cause", err)
	}
	if !routes.ready() {
		t.Fatal("一次什么都没改的失败把路由就绪位清掉了 —— 而没有任何东西会把它设回来")
	}
}
