package guardian

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/corestartfailure"
	"github.com/getbx/bx/internal/supervisor"
)

// Guardian **等得过** Core 自己那次判别 —— 少了这一层,这一整支修复在它唯一
// 存在的那个场景里一次都不会生效。
//
// 时序是硬事实,不是推演:Guardian 的健康等待 defaultHealthTimeout 是 20 秒,
// Core 的隧道健康窗口(supervisor.Run 的 healthTimeout 兜底 / `bx run
// --health-timeout` 的默认值)也是 20 秒,而 Guardian **从不**给 Core 传
// --health-timeout。两个计时器几乎同时起跑,于是 Guardian 放弃的那一刻,Core
// 才刚开始那次判别拨号(最长 TunnelDiagnosisTimeout),还没写下任何东西 ——
// 而紧接着 Guardian 就把它 SIGKILL 了(强杀路径),它永远没机会开口。
//
// 钉的是**顺序**不是具体秒数(先例:TestCoreCleanupBudgetLeavesRoomAboveWhatItWaitsOn)。
func TestTheRecordGraceOutlastsTheCoresOwnDiagnosis(t *testing.T) {
	if coreStartFailureGrace <= supervisor.TunnelDiagnosisTimeout {
		t.Fatalf("等记录的余量 %v 没有排在 Core 那次判别 %v 之后 ——\n"+
			"Guardian 会在 Core 开口之前就放弃并把它杀掉,于是这支修复在\n"+
			"「隧道起不来」这个它唯一存在的场景里一次都不生效",
			coreStartFailureGrace, supervisor.TunnelDiagnosisTimeout)
	}
}

// 上面那条只钉住常量之间的关系;真正在跑的是那处兜底赋值。
//
// 少了这一条,把 grace 改回一个手写的秒数之后上面那条**照样全绿**:关系还在,
// 只是没有人用它(cleanupbudget_test.go 的
// TestTheDefaultBudgetsActuallyComeFromThoseConstants 是同一个形状,也是变异
// 逼出来的)。
func TestTheGraceActuallyComesFromTheCoresDiagnosisBudget(t *testing.T) {
	source, err := os.ReadFile("corestartfailure.go")
	if err != nil {
		t.Fatalf("读不到 corestartfailure.go:%v", err)
	}
	if !strings.Contains(string(source), "supervisor.TunnelDiagnosisTimeout +") {
		t.Fatal("coreStartFailureGrace 不再从 supervisor.TunnelDiagnosisTimeout 派生 ——\n" +
			"两个进程各写一个秒数,而它们之间的关系没有任何东西在守")
	}
}

// spawn 之前先删:陈旧记录的**第一层**防线。
//
// 上一次 spawn 留下的记录与这一次的长得一模一样(同一个路径、同一个 schema),
// 而这个仓库为陈旧文件栽过三次。第二层是 PID + 窗口双重匹配,两层都要 ——
// 少了这一层,一次读失败(比如 Core 被杀在写之前)就会让上一轮的答案被这一轮
// 采信,而那正是「五次 spawn」那种场景。
func TestAStaleRecordIsDeletedBeforeTheNextSpawn(t *testing.T) {
	dir := t.TempDir()
	runner, operations := newStartFailureRunner(t, dir)
	recordPath := runner.StartFailurePath

	stale := corestartfailure.Record{
		SchemaVersion: corestartfailure.SchemaVersion,
		PID:           999999,
		At:            time.Now().Add(-time.Hour),
		Code:          supervisor.StartFailureTunnelUnreachable,
	}
	if err := corestartfailure.Write(recordPath, stale); err != nil {
		t.Fatal(err)
	}

	if _, err := runner.Start(context.Background(), CoreStartOptions{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(operations.releaseAll)

	if _, err := os.Stat(recordPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("spawn 之前那份陈旧记录还在(stat=%v)—— 第一层防线没生效", err)
	}
}

// Core 拿得到那个路径,否则它一个字都写不出来。
func TestCoreArgsCarryTheStartFailureFile(t *testing.T) {
	got := coreArgs("/etc/bx/config.yaml", "127.0.0.1:53", "/var/lib/bx/core-start-failure.json")
	want := []string{
		"run", "-c", "/etc/bx/config.yaml", "--listen-dns", "127.0.0.1:53",
		"--start-failure-file", "/var/lib/bx/core-start-failure.json",
	}
	if len(got) != len(want) {
		t.Fatalf("coreArgs() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("coreArgs() = %#v, want %#v", got, want)
		}
	}
	// 路径为空 ⇒ 不加这个 flag。`bx run` 手敲那条路上没人传它,而给它一个空值
	// 会让 Core 拿着一个空路径去判断「要不要写」—— 判据只该有一处。
	if bare := coreArgs("/etc/bx/config.yaml", "127.0.0.1:53", ""); len(bare) != 5 {
		t.Fatalf("没有记录路径时 coreArgs() = %#v,want 不带那个 flag", bare)
	}
}

// 这一次的记录被采信。
func TestTheCoreIsBelievedWhenTheRecordIsThisSpawns(t *testing.T) {
	runner, dir := newReadOnlyStartFailureRunner(t)
	process := Process{PID: 4242}
	since := time.Now().Add(-time.Second)
	writeStartFailureRecord(t, runner.StartFailurePath, corestartfailure.Record{
		SchemaVersion: corestartfailure.SchemaVersion,
		PID:           4242,
		At:            time.Now(),
		Code:          supervisor.StartFailureTunnelUnreachable,
	})
	if got := runner.StartFailureCode(context.Background(), process, since); got != supervisor.StartFailureTunnelUnreachable {
		t.Fatalf("StartFailureCode = %q, want %q", got, supervisor.StartFailureTunnelUnreachable)
	}
	// 读完即删:留着它就是下一次 spawn 的陈旧证据。
	if _, err := os.Stat(runner.StartFailurePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("读过的记录还在(stat=%v)", err)
	}
	_ = dir
}

// 三种「陈旧/读不懂」的形状,一律回落 —— **绝不猜**。
//
// PID 那一条是决定性的:Guardian 在一段故障里会连着 spawn 好几个 Core
// (事故日志里五个)。去掉 PID 匹配之后,**上一次 spawn 写下的码会被这一次
// 采信** —— 那不是「多说了一句」,是一句关于错误对象的、自信的假话。
func TestARecordThatIsNotThisSpawnsIsNotBelieved(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		record corestartfailure.Record
		raw    string
		since  time.Time
	}{
		{
			name: "上一次 spawn 的 PID",
			record: corestartfailure.Record{
				SchemaVersion: corestartfailure.SchemaVersion,
				PID:           4241, At: now, Code: supervisor.StartFailureTunnelUnreachable,
			},
			since: now.Add(-time.Second),
		},
		{
			name: "时间戳落在本次健康窗口之前",
			record: corestartfailure.Record{
				SchemaVersion: corestartfailure.SchemaVersion,
				PID:           4242, At: now.Add(-time.Hour), Code: supervisor.StartFailureTunnelUnreachable,
			},
			since: now.Add(-time.Second),
		},
		{
			name: "时间戳在未来",
			record: corestartfailure.Record{
				SchemaVersion: corestartfailure.SchemaVersion,
				PID:           4242, At: now.Add(time.Hour), Code: supervisor.StartFailureTunnelUnreachable,
			},
			since: now.Add(-time.Second),
		},
		{
			name:  "认不出的 schema",
			raw:   `{"schema_version":99,"pid":4242,"at":"` + now.Format(time.RFC3339Nano) + `","code":"tunnel_unreachable"}`,
			since: now.Add(-time.Second),
		},
		{
			name:  "码不属于这一族",
			raw:   `{"schema_version":1,"pid":4242,"at":"` + now.Format(time.RFC3339Nano) + `","code":"我是被人改出来的"}`,
			since: now.Add(-time.Second),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, _ := newReadOnlyStartFailureRunner(t)
			if tc.raw != "" {
				if err := os.WriteFile(runner.StartFailurePath, []byte(tc.raw), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				writeStartFailureRecord(t, runner.StartFailurePath, tc.record)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			if got := runner.StartFailureCode(ctx, Process{PID: 4242}, tc.since); got != "" {
				t.Fatalf("StartFailureCode = %q —— 对不上的记录必须读作「这一次没说」,"+
					"回落 core_health_failed,绝不猜", got)
			}
		})
	}
}

// 记录根本不在 ⇒ 「这一次没说」,而且**不许为此等满整个余量**当 Core 已经没了。
func TestNoRecordAtAllIsSilenceNotAGuess(t *testing.T) {
	runner, _ := newReadOnlyStartFailureRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if got := runner.StartFailureCode(ctx, Process{PID: 4242}, time.Now().Add(-time.Second)); got != "" {
		t.Fatalf("StartFailureCode = %q,want 空串", got)
	}
}

// 删不掉那份读过的记录**不许升级成失败**:停止/诊断路径不许因为别的事没做成
// 而失败。判据是「码照样交出来了」。
func TestAFailedDeleteDoesNotSwallowTheAnswer(t *testing.T) {
	runner, dir := newReadOnlyStartFailureRunner(t)
	writeStartFailureRecord(t, runner.StartFailurePath, corestartfailure.Record{
		SchemaVersion: corestartfailure.SchemaVersion,
		PID:           4242, At: time.Now(), Code: supervisor.StartFailureTunnelUnreachable,
	})
	// 目录只读 ⇒ unlink 要的是父目录写权限,删不掉。
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if got := runner.StartFailureCode(context.Background(), Process{PID: 4242}, time.Now().Add(-time.Second)); got != supervisor.StartFailureTunnelUnreachable {
		t.Fatalf("StartFailureCode = %q —— 删不掉一份 JSON 不该把已经拿到的答案吞掉", got)
	}
}

// 整机形状:Core 说了,而用户看到的就是它说的那句话对应的码。
//
// 这是这一支的产出本身 —— 2026-09-12 那天用户拿到的是七次
// core_ownership_uncertain,而真相(VPS 的 443 没有应答)从第一秒就在。
func TestUpPublishesWhatTheCoreSaidAboutWhyItCouldNotStart(t *testing.T) {
	env := newManagerTestEnv(t)
	env.health.err = errors.New("bx 隧道健康检查超时(20s): restarts=0")
	env.runner.reportedStartFailure = supervisor.StartFailureTunnelUnreachable

	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("隧道没起来而 Up 报成功了")
	}
	if got := env.manager.Status().LastError; got != "core_tunnel_unreachable" {
		t.Fatalf("LastError = %q, want core_tunnel_unreachable —— Core 说的那句话没有走到用户面前", got)
	}
}

// Core 没说(旧 Core、被杀在开口之前、记录读不动)⇒ 回落 core_health_failed。
// **不许猜**,也不许把「没说」渲染成一个具体的病因。
func TestUpFallsBackWhenTheCoreSaidNothing(t *testing.T) {
	env := newManagerTestEnv(t)
	env.health.err = errors.New("bx 隧道健康检查超时(20s): restarts=0")

	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("隧道没起来而 Up 报成功了")
	}
	if got := env.manager.Status().LastError; got != "core_health_failed" {
		t.Fatalf("LastError = %q, want core_health_failed", got)
	}
}

// 读记录必须发生在**收拾那个 Core 之前**。
//
// 清理走的是强杀(SIGKILL,批一 Task 1)。反过来的顺序在时序上恰好可行、
// 在逻辑上必错:被 SIGKILL 的 Core 不会再写任何东西,于是「读记录」变成一个
// 永远读不到东西的动作,而两侧测试都不会报错 —— 那正是这支修复最容易被
// 悄悄改死的地方。
func TestTheRecordIsReadBeforeTheFailedCoreIsCleanedUp(t *testing.T) {
	env := newManagerTestEnv(t)
	env.health.err = errors.New("bx 隧道健康检查超时(20s): restarts=0")
	env.runner.reportedStartFailure = supervisor.StartFailureTunnelUnreachable

	_ = env.manager.Up(context.Background())

	events := env.events.snapshot()
	read := indexOfEvent(events, "core.read_start_failure")
	killed := indexOfEvent(events, "core.force_stop")
	if read < 0 {
		t.Fatalf("一次都没去读 Core 自报的失败:%v", events)
	}
	if killed < 0 {
		t.Fatalf("失败的 Core 没被收拾:%v", events)
	}
	if read > killed {
		t.Fatalf("先杀了 Core 才去读它写的记录 —— 被 SIGKILL 的 Core 不会再写任何东西,\n"+
			"于是那次读永远读不到东西而没有任何测试会红:%v", events)
	}
}

// --- 台子 ---

func writeStartFailureRecord(t *testing.T, path string, record corestartfailure.Record) {
	t.Helper()
	if err := corestartfailure.Write(path, record); err != nil {
		t.Fatal(err)
	}
}

// newReadOnlyStartFailureRunner 造一个只用来读记录的 runner(不 spawn 任何东西)。
func newReadOnlyStartFailureRunner(t *testing.T) (*ExecCoreRunner, string) {
	t.Helper()
	dir := t.TempDir()
	runner := NewExecCoreRunner(filepath.Join(dir, "bx"), filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.StartFailurePath = filepath.Join(dir, "core-start-failure.json")
	return runner, dir
}

func newStartFailureRunner(t *testing.T, dir string) (*ExecCoreRunner, *systemProcessOperations) {
	t.Helper()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	operations := newSystemProcessOperations(executable, 500)
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.ControlSocket = filepath.Join(dir, "bx.sock")
	runner.StartFailurePath = filepath.Join(dir, "core-start-failure.json")
	runner.Operations = operations
	runner.ScanRunningCores = operations.runningCores
	return runner, operations
}

// **递过去的那两个值就是新鲜度判据的全部** —— 它们没有守卫过。
//
// `StartFailureCode(ctx, process, since)` 的两个入参各自把一份记录否掉:
// PID 对不上 ⇒ 丢;`record.At.Before(since)` ⇒ 丢。也就是说递一个零值
// Process 和一个「失败之后」的 since 过去,**每一份记录都会被丢掉两遍**,
// LastError 回落 core_health_failed —— 与这支修复不存在时逐字相同。
//
// 复现过:把那一行改成 `m.coreReportedStartFailure(ctx, Process{}, time.Now())`
// (加一句 `_ = spawnedAt` 才编得过),**整个 internal/guardian 全绿**。
// 既有那条 TestTheRecordIsReadBeforeTheFailedCoreIsCleanedUp 只钉「这次调用
// 发生过」,而替身的两个形参连名字都没有 —— 这是本支第三次同一个形状:
// 判据是对的,而把真实输入接上判据的那根线没人守。
//
// 判据因此打在**到达的值**上:进程必须是这一次 fork 出来的那个,
// since 必须严格早于进入 Start 的那一刻。
func TestUpHandsTheReaderThisSpawnsProcessAndAPreForkInstant(t *testing.T) {
	env := newManagerTestEnv(t)
	env.health.err = errors.New("bx 隧道健康检查超时(20s): restarts=0")
	env.runner.reportedStartFailure = supervisor.StartFailureTunnelUnreachable

	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("隧道没起来而 Up 报成功了")
	}

	asks := env.runner.startFailureAsksSnapshot()
	if len(asks) != 1 {
		t.Fatalf("去读 Core 自报失败的次数 = %d,want 1", len(asks))
	}
	ask := asks[0]

	started := env.runner.lastStartedProcess()
	if started.PID == 0 {
		t.Fatal("替身没记下它 fork 出来的那个 Process —— 台子坏了,下面的断言什么都证明不了")
	}
	if ask.process.PID != started.PID {
		t.Fatalf("递给读取方的 PID = %d,而这一次 fork 出来的是 %d ——\n"+
			"PID 对不上时每一份记录都会被丢掉,回落 core_health_failed,\n"+
			"也就是 2026-09-12 那天用户读到的那句话",
			ask.process.PID, started.PID)
	}
	if ask.process.Generation != started.Generation {
		t.Fatalf("递给读取方的 Generation = %q,而这一次 fork 出来的是 %q",
			ask.process.Generation, started.Generation)
	}

	entered := env.runner.startEntryInstant()
	if entered.IsZero() {
		t.Fatal("替身没记下进入 Start 的那一刻 —— 台子坏了")
	}
	if ask.since.IsZero() {
		t.Fatal("递过去的 since 是零值 —— 那不是「fork 之前那一刻」")
	}
	if !ask.since.Before(entered) {
		t.Fatalf("递过去的 since = %s,不早于进入 Start 的那一刻 %s ——\n"+
			"since 是「这份记录属不属于这一次」的下界,取在 fork 之后会把\n"+
			"本次 Core 写下的记录整份判成窗口之外",
			ask.since.Format(time.RFC3339Nano), entered.Format(time.RFC3339Nano))
	}
}

// **这段等待绝不许花清理那份预留。**
//
// reserveCleanup 存在的全部意义是「无论上面那些事花了多久,收拾这个 Core 的
// 预算还在」;它交出来的 operationCtx 的 deadline 就是那条线。宽限坐在
// reserveCleanup 与 cleanupCoreAfterFailedStart 中间,裸拿外层 ctx 去等就是
// 从预留里扣 —— 三个 restartTimeout=25s 的调用点上(崩溃重启、调谐环
// start_core、**以及 Manager.Down 里 DNS 还原失败之后那次补偿重启,一条停止
// 路径**)清理会从 12.5s 掉到 4.5s ⇒ 超时 ⇒ retainUncertain ⇒
// core_ownership_uncertain,正是这一整支要消灭的那条。
//
// **钉的是顺序不是秒数**(先例:TestCoreCleanupBudgetLeavesRoomAboveWhatItWaitsOn):
// 递给「读记录」那一跳的 deadline,不许晚于递给 Start 的那一个 —— 后者正是
// reserveCleanup 扣掉清理预算之后剩下的东西。
func TestTheStartFailureGraceNeverEatsTheCleanupReserve(t *testing.T) {
	env := newManagerTestEnv(t)
	env.health.err = errors.New("bx 隧道健康检查超时(20s): restarts=0")
	env.runner.reportedStartFailure = supervisor.StartFailureTunnelUnreachable

	// 25s 正是那三个调用点传的 m.restartTimeout。
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := env.manager.Up(ctx); err == nil {
		t.Fatal("隧道没起来而 Up 报成功了")
	}

	deadlines := env.runner.startDeadlinesSnapshot()
	if len(deadlines) != 1 || deadlines[0].IsZero() {
		t.Fatalf("替身没记下递给 Start 的那个 deadline(%v)—— 台子坏了,下面的断言什么都证明不了", deadlines)
	}
	operationDeadline := deadlines[0]

	asks := env.runner.startFailureAsksSnapshot()
	if len(asks) != 1 {
		t.Fatalf("去读 Core 自报失败的次数 = %d,want 1", len(asks))
	}
	if !asks[0].hasDeadline {
		t.Fatal("递给「读记录」那一跳的 ctx 没有 deadline —— 它会一直等到宽限跑满,\n" +
			"而那段时间是从收拾这个 Core 的预留里扣的")
	}
	if asks[0].deadline.After(operationDeadline) {
		t.Fatalf("读记录的 deadline %s 晚于 Start 拿到的那个 %s ——\n"+
			"多出来的 %v 花的是 reserveCleanup 给清理留的预算:清理超时 ⇒\n"+
			"retainUncertain ⇒ core_ownership_uncertain,而 Manager.Down 的\n"+
			"DNS 还原补偿就走这条路(停止路径不许因为别的事没做完而变慢或失败)",
			asks[0].deadline.Format(time.RFC3339Nano),
			operationDeadline.Format(time.RFC3339Nano),
			asks[0].deadline.Sub(operationDeadline))
	}
}

// 外层没有 deadline(菜单/CLI 直接 Up)⇒ 宽限拿满,一秒不少。
//
// 少了这一条,把上面那条修成「一律不等」也能全绿 —— 而那会让这支修复在
// /v1/up 那条最常见的路上一次都不生效。
func TestWithNoOuterDeadlineTheGraceIsTheFullOne(t *testing.T) {
	ctx, cancel := startFailureReadContext(context.Background(), context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("宽限没有 deadline —— 它必须有界,否则一次失败的 up 会无限押着 mutation 槽")
	}
	if got := time.Until(deadline); got < coreStartFailureGrace-time.Second || got > coreStartFailureGrace {
		t.Fatalf("宽限 = %v,want ≈ %v", got, coreStartFailureGrace)
	}
}

// 余量已经是零(那三条 25s 的路)⇒ **仍然读一次**,只是一拍都不等。
//
// config / provision / tun_open / hijack 那几种约两秒就写完退出了,记录早在
// 盘上;「不等」不等于「不看」。反过来直接返回空串,会把这四种也一起丢掉。
func TestZeroGraceStillReadsOnce(t *testing.T) {
	runner, _ := newReadOnlyStartFailureRunner(t)
	writeStartFailureRecord(t, runner.StartFailurePath, corestartfailure.Record{
		SchemaVersion: corestartfailure.SchemaVersion,
		PID:           4242, At: time.Now(), Code: supervisor.StartFailureProvision,
	})
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancelExpired()
	readCtx, cancel := startFailureReadContext(context.Background(), expired)
	defer cancel()
	// 两半都要断言。少了这一半,「余量为零时照样给满宽限」的实现也全绿 ——
	// 而那正是这条修复要消灭的东西(它花的是清理那份预留)。
	if readCtx.Err() == nil {
		t.Fatal("余量为零而那份 ctx 还没到期 —— 「一拍都不等」是假的,\n" +
			"这段等待仍然在花收拾这个 Core 的预留")
	}
	if got := runner.StartFailureCode(readCtx, Process{PID: 4242}, time.Now().Add(-time.Second)); got != supervisor.StartFailureProvision {
		t.Fatalf("StartFailureCode = %q, want %q —— 余量为零时仍要读一次,\n"+
			"早早死掉的那几种记录两秒前就写好了", got, supervisor.StartFailureProvision)
	}
}
