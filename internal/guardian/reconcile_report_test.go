package guardian

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getbx/bx/internal/observe"
)

// sameStringSlices 把 nil 与空切片当同一件事(报告里「没有动作」两种写法都合法)。
func sameStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// 零值绝不能读成「一切正常」。
//
// reconcileDecision 的零值就是一台健康机器的判断,于是「从没跑过一轮」与「跑了、
// 什么差异都没有」在判断值上完全相同 —— 而它们在真机上的意义正好相反。
// At 是唯一分得开二者的东西。
func TestStatusOmitsReconcileReportUntilARoundHasCompleted(t *testing.T) {
	env := newManagerTestEnv(t)
	if got := env.manager.Status().Reconcile; got != nil {
		t.Fatalf("一轮都没跑过时必须整个字段缺席,而不是发布一份「无差异」, got %+v", got)
	}
	// 字段缺席必须一路缺席到线上字节:发布出去的那份 JSON 里若出现一个
	// `"reconcile":{"at":"0001-01-01T00:00:00Z"}`,消费方拿到的就是一份
	// 「跑过、无差异」的假证据 —— 正是本任务要消灭的那句谎话。
	encoded, err := json.Marshal(env.manager.Status())
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, present := decoded["reconcile"]; present {
		t.Fatalf("一轮都没跑过时 reconcile 键必须整个缺席, got %s", encoded)
	}
}

// 跑过之后必须发布,且「无差异」也要发布 —— 用户要分得清「循环活着且干净」
// 与「循环死了」,而后者正是 Task 2 修复轮 2 抓到的那个洞。
func TestStatusPublishesACleanReconcileRound(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.recordReconcileRound(reconcileDecision{}, 7)
	got := env.manager.Status().Reconcile
	if got == nil {
		t.Fatal("跑过一轮之后必须发布,哪怕这一轮什么差异都没有")
	}
	if got.At.IsZero() {
		t.Error("At 是「跑过没跑过」的唯一判据,不能是零值")
	}
	if len(got.Actions) != 0 || got.Held != "" {
		t.Errorf("这一轮是干净的, got %+v", got)
	}
	if got.UnchangedRounds != 7 {
		t.Errorf("UnchangedRounds = %d, want 7", got.UnchangedRounds)
	}
	// 线上字节里也必须有 at:消费方(CLI/菜单)判「跑过没跑过」用的正是它,
	// 一个 `json:"-"` 或 omitempty 会让干净的那一轮在 JSON 里退化成零值时刻。
	encoded, err := json.Marshal(env.manager.Status())
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Reconcile *ReconcileReport `json:"reconcile"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Reconcile == nil || decoded.Reconcile.At.IsZero() {
		t.Fatalf("At 必须过得了 JSON 这一跳 —— 它是消费方唯一的判据, got %s", encoded)
	}
}

// 报告不许被别的写 status 的路径顺手抹掉:它存在别的字段里,Status() 组装时附上。
// 变异:把它并进 m.status 结构体 —— 任何一处整体重建 status 的路径都会让这条红。
//
// **两条重建路径都要走一遍。** needsAttention 是先 `status := m.Status()` 再回写,
// 一份并进 m.status 的报告会被它原样带过去(实测:只用这一条,变异②照样绿);
// 真正会抹掉它的是那些**新造一个 Status 字面量**的路径(upLocked/downLocked/
// recoverLocked 都是),所以下面补一次裸 setStatus。
func TestReconcileReportSurvivesAStatusRebuild(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.recordReconcileRound(reconcileDecision{Held: heldOwnershipUncertain}, 0)

	env.manager.needsAttention(DesiredOn, "core_scan_failed")
	got := env.manager.Status().Reconcile
	if got == nil || got.Held != heldOwnershipUncertain {
		t.Fatalf("别的路径重建 status 之后报告丢了, got %+v", got)
	}

	env.manager.setStatus(Status{
		SchemaVersion: 1,
		Desired:       DesiredOn,
		Phase:         PhaseCommitted,
		Protection:    ProtectionProtected,
	})
	got = env.manager.Status().Reconcile
	if got == nil || got.Held != heldOwnershipUncertain {
		t.Fatalf("一次从零构造的 Status 把报告抹掉了, got %+v", got)
	}
	if status := env.manager.Status(); status.Protection != ProtectionProtected {
		t.Fatalf("附报告不该反过来改写别的字段, protection=%q", status.Protection)
	}
}

// 发布出去的那份报告是**副本**:调用方改它不得回头污染 Manager 里的那一份。
// 与 GuardianCapabilities 不共享底层数组同一条纪律 —— Status 会被交给 JSON
// 编码器旁边的任意代码。
func TestStatusReconcileReportIsNotAliased(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.recordReconcileRound(reconcileDecision{Actions: []reconcileAction{actionRestoreDNS}}, 3)

	first := env.manager.Status().Reconcile
	if first == nil || len(first.Actions) != 1 {
		t.Fatalf("前置条件不成立, got %+v", first)
	}
	first.Held = "tampered"
	first.Actions[0] = "tampered"

	second := env.manager.Status().Reconcile
	if second.Held != "" || second.Actions[0] != string(actionRestoreDNS) {
		t.Fatalf("Status() 交出了 Manager 内部那一份的别名, got %+v", second)
	}
}

// 循环每一轮都要记 —— 包括被栅栏挡住的那些轮:soak 要数的正是「有多少轮根本没在工作」。
//
// **判据刻意不只看 At 前进。** 时间戳在低精度时钟上(Windows runner 实测 ~0.5ms)
// 可能两轮相同,而「每轮都记」这条属性用**内容序列**钉得更死:连续无变化的轮数
// 必须逐轮递增,判断一变就归零。变异④(只在有差异时记)会让前两轮根本没有报告,
// 而这条序列立刻对不上。
func TestReconcileLoopRecordsEveryRoundIncludingHeldOnes(t *testing.T) {
	env := newManagerTestEnv(t)

	// 每一轮开跑之前先抓一份「上一轮记下了什么」。观测函数是循环那条 goroutine
	// 自己调的,所以这里读 Status 与改栅栏都不存在竞争。
	var snapshots []*ReconcileReport
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	round := 0
	observer := func(context.Context) observe.ObservedState {
		snapshots = append(snapshots, env.manager.Status().Reconcile)
		round++
		switch round {
		case 1, 2:
			return observe.ObservedState{} // 全 Unknown ⇒ 干净的一轮
		case 3:
			// 第 3 轮撞栅栏:路径恢复在跑。
			env.manager.beginPathRecoveryTransition(pathRecoveryTransitionPreserveGenerated)
			return observe.ObservedState{}
		case 4:
			env.manager.endPathRecoveryTransition()
			// desired 默认是 off,而 DNS 仍归 bx ⇒ 提议还原。
			return observe.ObservedState{DNSManaged: observe.True}
		default:
			cancel()
			return observe.ObservedState{}
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		env.manager.runReconcileLoopWithPacing(ctx, observer, func(int) time.Duration { return time.Millisecond })
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("循环没有随 ctx 结束")
	}

	if len(snapshots) != 5 {
		t.Fatalf("观测被调了 %d 次,这条测试要 5 次(4 轮判断 + 第 5 次负责收尾)", len(snapshots))
	}
	if snapshots[0] != nil {
		t.Fatalf("第一轮观测之前一轮都没跑过,报告必须还缺席, got %+v", snapshots[0])
	}

	// snapshots[i] 是第 i 轮记下的那份报告(i 从 1 数起)。
	for _, tc := range []struct {
		round     int
		held      string
		actions   []string
		unchanged int
	}{
		// previous 的初值就是「干净」,所以第 1 轮起就在累计「连续未变」。
		{round: 1, unchanged: 1},
		{round: 2, unchanged: 2},
		{round: 3, held: heldPathRecoveryBusy, unchanged: 0},
		{round: 4, actions: []string{string(actionRestoreDNS)}, unchanged: 0},
	} {
		got := snapshots[tc.round]
		if got == nil {
			t.Fatalf("第 %d 轮没有被记下来 —— 「有多少轮根本没在工作」正是 soak 要数的东西", tc.round)
		}
		if got.At.IsZero() {
			t.Errorf("第 %d 轮的 At 是零值 —— 那与「从没跑过」无法区分", tc.round)
		}
		if got.Held != tc.held {
			t.Errorf("第 %d 轮 Held = %q, want %q", tc.round, got.Held, tc.held)
		}
		if !sameStringSlices(got.Actions, tc.actions) {
			t.Errorf("第 %d 轮 Actions = %v, want %v", tc.round, got.Actions, tc.actions)
		}
		if got.UnchangedRounds != tc.unchanged {
			t.Errorf("第 %d 轮 UnchangedRounds = %d, want %d", tc.round, got.UnchangedRounds, tc.unchanged)
		}
	}
	// 时间必须往前走(允许同刻:低精度时钟上两轮可能落在同一个 tick)。
	for index := 2; index <= 4; index++ {
		if snapshots[index].At.Before(snapshots[index-1].At) {
			t.Errorf("第 %d 轮的 At 比上一轮还早", index)
		}
	}
}

// 能力声明是消费方判断「对面这一版有没有这条循环」的唯一信号 ——
// 与 bx logs --help 文本探测那次的教训同一条:能力由 daemon 自己声明。
func TestGuardianDeclaresTheReconcileReportCapability(t *testing.T) {
	capabilities := GuardianCapabilities()
	if !contains(capabilities, CapabilityReconcileReport) {
		t.Fatalf("GuardianCapabilities 没声明 %q —— CLI 只能靠它区分「旧版 Guardian」"+
			"与「新版但还没跑完第一轮」, got %v", CapabilityReconcileReport, capabilities)
	}
	// 新增一条不许挤掉老的:菜单靠 diagnostics_archive 决定要不要给 `bx logs --archive`。
	if !contains(capabilities, CapabilityDiagnosticsArchive) {
		t.Fatalf("新能力挤掉了既有能力, got %v", capabilities)
	}
}

// **报告必须走完从 Manager 到 bx status 的那一整跳。**
//
// 上面所有断言证明的是两件各自成立、但连不起来的事:Manager.Status() 里有这个
// 字段,以及 CLI 拿到一份报告时会渲染它。中间那一段没人守 ——
// observableStatus(localapi.go)是 Manager 到客户端**唯一**的通路,而它是逐字段
// 揉这份 Status 的(Recovery 重置、Protection 按恢复态改写、版本与能力补上)。
// 在那里插一句 `status.Reconcile = nil`,guardian 与 cli 两套测试**全绿**。
//
// 这不是假想:setStatus(manager.go)里就**真的**有一句刻意的
// `status.Reconcile = nil`,「在一个揉 Status 的函数里把 Reconcile 赋成 nil」
// 是一行之隔就能抄到的写法;而 observableStatus 恰恰是将来改 Recovery/Protection
// 的人会动的那个函数。
//
// 后果正是这一项存在的理由:报告在路上掉了 ⇒ CLI 看到 nil,而能力由
// applyVersionFields **恒**声明 ⇒ 永远渲染成「尚未完成第一轮观测」,与一条从没
// 跑过的循环逐字节相同。soak 会从一台跑了一整天的机器上读到「刚启动」。
//
// At 也一并断言:一份 At 为零的报告到了 CLI 那边同样渲染成「还没跑过」,
// 所以「字段在」不等于「消息到了」。
func TestGuardianStatusEndpointPublishesTheReconcileReport(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.recordReconcileRound(reconcileDecision{Held: heldOwnershipUncertain}, 5)

	recorder := httptest.NewRecorder()
	NewLocalAPI(env.manager).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /v1/status = %d, body=%s", recorder.Code, recorder.Body.String())
	}

	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Reconcile == nil {
		t.Fatalf("报告没能走到 /v1/status —— CLI 会永远显示「尚未完成第一轮观测」,"+
			"与一条从没跑过的循环完全一样, body=%s", recorder.Body.String())
	}
	if got.Reconcile.At.IsZero() {
		t.Errorf("At 在路上被抹平了 —— 到了 CLI 那边它同样会渲染成「还没跑过」, body=%s",
			recorder.Body.String())
	}
	if got.Reconcile.Held != heldOwnershipUncertain || got.Reconcile.UnchangedRounds != 5 {
		t.Errorf("报告的内容在路上变了, got %+v", got.Reconcile)
	}
	// 同一份响应里能力必须也在:CLI 的三态判断要两样都到齐才成立。
	if !contains(got.Capabilities, CapabilityReconcileReport) {
		t.Errorf("同一份响应里没有能力声明,CLI 分不清「旧版」与「还没跑完第一轮」, got %v",
			got.Capabilities)
	}
}
