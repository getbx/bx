package supervisor

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getbx/bx/internal/confirm"
)

// 服务器旁路此前**只在启动时解析一次**:staticA 钉住旧 IP、table 100 / split-default
// 里 pin 的是旧 IP,VPS 换 IP 之后隧道子进程拿到的仍是旧答案,永远重连
//(真机 2026-08-06→09-03,NAS 上重连 65638 次,静默断了一个月)。
// 这一支让旁路在隧道持续不健康时**重新跟随 DNS**,并复用切换服务器那条既有的
// 刷新→rehijack 路径,不另造一份「什么必须绕开隧道」。

// 判据:隧道要不健康**够久**(刚抖一下不算),且两次重跟随之间要隔开
// (每次都去问 DNS 是把一条 30 秒的循环变成 DNS 探针)。
func TestShouldRefollowServerBypassOnlyAfterSustainedUnhealth(t *testing.T) {
	cases := []struct {
		name         string
		unhealthyFor time.Duration
		sinceLast    time.Duration // 负数 = 还没试过
		want         bool
	}{
		{"刚抖一下", 30 * time.Second, -1, false},
		{"够久、没试过", bypassRefollowAfter, -1, true},
		{"够久、刚试过", bypassRefollowAfter, time.Minute, false},
		{"够久、上次已经很久", bypassRefollowAfter, bypassRefollowInterval, true},
		{"健康(0)", 0, -1, false},
	}
	for _, c := range cases {
		if got := shouldRefollowServerBypass(c.unhealthyFor, c.sinceLast); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// 循环:隧道一直不健康 → 到点重跟随**一次**,之后按间隔限频;隧道一旦健康,
// 计时归零、一次都不跟随。
func TestWatchServerBypassRefollowsOnceTunnelStaysUnhealthy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tick := make(chan time.Time)
	now := time.Unix(1_700_000_000, 0)
	var healthy atomic.Bool
	var calls atomic.Int32
	// observed:循环每拍读完健康状态就回一声,测试等到这一声才改状态、发下一拍 ——
	// 否则「发完 tick 立刻翻 healthy」与循环读 healthy 是一场赛跑。
	observed := make(chan struct{}, 1)
	healthyFn := func() bool { v := healthy.Load(); observed <- struct{}{}; return v }
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchServerBypass(ctx, healthyFn, func(context.Context) error { calls.Add(1); return nil }, tick)
	}()
	step := func(d time.Duration) {
		now = now.Add(d)
		tick <- now
		<-observed
	}
	// 不健康 30s:不该动。
	step(30 * time.Second)
	// 累计到门槛:动一次。
	step(bypassRefollowAfter)
	// 紧接着几拍:限频,不动。
	step(30 * time.Second)
	step(30 * time.Second)
	// 过了间隔:再动一次。
	step(bypassRefollowInterval)
	// 隧道健康了:计时归零,之后哪怕过很久也不动。
	healthy.Store(true)
	step(bypassRefollowInterval * 3)
	// 再次不健康但还没够久:不动。
	healthy.Store(false)
	step(30 * time.Second)
	cancel()
	<-done
	if n := calls.Load(); n != 2 {
		t.Fatalf("重跟随次数 = %d,want 2(门槛一次 + 间隔后一次)", n)
	}
}

// 动作:在控制面的锁里刷新旁路;**变了才** rehijack + 重建传输(让子进程拿新答案);
// 有待确认的改动时让路(那次改动的刷新是替换语义,交错 = 抹掉刚算进去的那台);
// 刷新失败绝不动路由。
func TestRefollowServerBypassRehijacksThenReconnectsWhenThePinChanged(t *testing.T) {
	var order []string
	rec := &recordingMutator{
		onRehijack:  func() { order = append(order, "rehijack") },
		onReconnect: func() { order = append(order, "reconnect") },
	}
	cs := newTestControlServer(t, rec)
	var gotLinks []string
	cs.refreshBypass = func(links []string) (bool, error) { gotLinks = links; return true, nil }

	out, err := cs.refollowServerBypass(context.Background(), []string{"vless://a", "hysteria2://b"})
	if err != nil {
		t.Fatal(err)
	}
	if out != refollowChanged {
		t.Errorf("outcome = %v want changed", out)
	}
	if len(gotLinks) != 2 {
		t.Errorf("没有把当前链接点名交给刷新: %v", gotLinks)
	}
	if len(order) != 2 || order[0] != "rehijack" || order[1] != "reconnect" {
		t.Errorf("顺序错(先装新路由再重建传输,反过来是成环窗口): %v", order)
	}
}

func TestRefollowServerBypassLeavesRoutesAloneWhenNothingChanged(t *testing.T) {
	rec := &recordingMutator{
		onRehijack:  func() { t.Error("没变却 rehijack 了") },
		onReconnect: func() { t.Error("没变却重建传输了 —— 隧道自己的重连循环已经在跑") },
	}
	cs := newTestControlServer(t, rec)
	cs.refreshBypass = func([]string) (bool, error) { return false, nil }
	out, err := cs.refollowServerBypass(context.Background(), []string{"vless://a"})
	if err != nil || out != refollowUnchanged {
		t.Fatalf("got %v %v", out, err)
	}
}

func TestRefollowServerBypassYieldsToAnArmedMutation(t *testing.T) {
	rec := &recordingMutator{onRehijack: func() { t.Error("armed 期间动了路由") }}
	cs := newTestControlServer(t, rec)
	cs.eng.(*fakeControlEngine).state = confirm.StateArmed
	cs.refreshBypass = func([]string) (bool, error) {
		t.Error("armed 期间刷新了 —— 替换语义会抹掉待确认那台的旁路")
		return true, nil
	}
	out, err := cs.refollowServerBypass(context.Background(), []string{"vless://a"})
	if err != nil || out != refollowYielded {
		t.Fatalf("got %v %v", out, err)
	}
}

func TestRefollowServerBypassDoesNotTouchRoutesWhenRefreshFails(t *testing.T) {
	rec := &recordingMutator{onRehijack: func() { t.Error("刷新失败还动了路由") }}
	cs := newTestControlServer(t, rec)
	boom := errors.New("dns down")
	cs.refreshBypass = func([]string) (bool, error) { return false, boom }
	if _, err := cs.refollowServerBypass(context.Background(), []string{"vless://a"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestRefollowServerBypassIsANoOpWithoutARefresher(t *testing.T) {
	rec := &recordingMutator{onRehijack: func() { t.Error("没有刷新器却动了路由") }}
	cs := newTestControlServer(t, rec)
	cs.refreshBypass = nil
	if out, err := cs.refollowServerBypass(context.Background(), []string{"vless://a"}); err != nil || out != refollowYielded {
		t.Fatalf("got %v %v", out, err)
	}
}

// 接线守卫:这条循环真的从 Run 起来了,而且它拿的是**主传输**的健康和控制面那把
// 锁里的重跟随(不是自己另起一份刷新)。读源码是这里唯一够得着的办法 ——
// serveControlWithPathRecovery 非 root 起不来,netns 台子造不出「VPS 换 IP」。
// 锚点找不到时响亮失败,不静默放行。
func TestRunWiresTheServerBypassRefollowLoop(t *testing.T) {
	runSrc, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	ctlSrc, err := os.ReadFile("control.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`workers.start(ctx, "server-bypass-refollow"`,
		`watchServerBypass(c, lt.Healthy,`,
	} {
		if !strings.Contains(string(runSrc), want) {
			t.Fatalf("run.go 里找不到 %q —— 服务器旁路重跟随没有接线(或锚点改了,守卫读不懂现在的代码)", want)
		}
	}
	// 对齐空格不算数:gofumpt 在结构体里加一个更长的字段名就会重排对齐,而这条
	// 守卫要的是「接上了」,不是「对齐成几个空格」。
	flat := strings.Join(strings.Fields(string(ctlSrc)), " ")
	for _, want := range []string{
		"RefollowServerBypass: cs.refollowServerBypass,",
		"ReassertRoutes: cs.reassertRoutes,",
	} {
		if !strings.Contains(flat, want) {
			t.Fatalf("control.go 里找不到 %q —— 后台循环拿不到控制面那把锁里的入口", want)
		}
	}
	// 旁路路由自愈那条也从这里起(与 refollow 共用同一个 hook)。
	for _, want := range []string{
		`workers.start(ctx, "server-bypass-route-repair"`,
		`watchServerBypassRoutes(c,`,
		`hooks.ReassertRoutes, routeTicker.C)`,
	} {
		if !strings.Contains(string(runSrc), want) {
			t.Fatalf("run.go 里找不到 %q —— 服务器旁路路由自愈没有接线", want)
		}
	}
}
