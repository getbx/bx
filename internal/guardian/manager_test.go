package guardian

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/getbx/bx/internal/version"
)

func TestManagerUpStartsOneCoreAndPersistsOn(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.runner.startCount(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
	if got, _ := env.store.LoadDesired(); got != DesiredOn {
		t.Fatalf("desired = %q, want %q", got, DesiredOn)
	}
	status := env.manager.Status()
	if status.Protection != ProtectionProtected || status.CorePID == 0 {
		t.Fatalf("status = %+v, want protected Core", status)
	}
}

func TestManagerOptionsRequiresDNSWithoutLegacyRestorerAlias(t *testing.T) {
	if _, ok := reflect.TypeOf(ManagerOptions{}).FieldByName("Restorer"); ok {
		t.Fatal("ManagerOptions still exposes legacy Restorer alias")
	}
}

// 失败码只存在内存里等于没有——排查时既看不到日志也拿不到返回。
func TestNeedsAttentionLogsTheFailureCode(t *testing.T) {
	var buf bytes.Buffer
	restore := swapGuardianLogOutput(&buf)
	defer restore()

	env := newManagerTestEnv(t)
	env.manager.needsAttention(DesiredOn, "core_ownership_uncertain")

	logged := buf.String()
	if !strings.Contains(logged, "core_ownership_uncertain") {
		t.Errorf("失败码必须写进日志,实际日志:%q", logged)
	}
	if env.manager.Status().LastError != "core_ownership_uncertain" {
		t.Error("失败码仍应保留在 status.LastError")
	}
}

// needsAttention 用同一个失败码连续调用两次时,LastErrorGeneration 仍必须
// 真的往前走——这是 LocalAPI 层区分"这次真的失败了"与"上次的陈旧码"的唯一
// 可靠信号,不能退化成对 LastError 值的比较(值比较在连续同因失败时会把第二次
// 真实失败误判为陈旧,导致回传的 code 时有时无)。
func TestNeedsAttentionIncrementsGenerationEvenWithSameCode(t *testing.T) {
	env := newManagerTestEnv(t)

	env.manager.needsAttention(DesiredOn, "core_ownership_uncertain")
	first := env.manager.Status().LastErrorGeneration
	if first == 0 {
		t.Fatal("第一次 needsAttention 后 LastErrorGeneration 不应是零值")
	}

	env.manager.needsAttention(DesiredOn, "core_ownership_uncertain")
	second := env.manager.Status().LastErrorGeneration
	if second <= first {
		t.Fatalf("第二次调用(同一失败码)后代际号必须严格递增: first=%d second=%d", first, second)
	}
	if got := env.manager.Status().LastError; got != "core_ownership_uncertain" {
		t.Fatalf("LastError = %q, want core_ownership_uncertain", got)
	}
}

// setStatus 的绝大多数调用点(Up/Down/Migrate 等状态转换)都会构造一个不带
// LastErrorGeneration 字段的全新 Status{} 字面量;若不保留旧代际号,这些无关
// 的状态转换会把代际号悄悄清零,使它失去"needsAttention 是否真的跑过"的
// 判别力(handler 层比较 before/after 代际号就会失真)。
func TestSetStatusPreservesLastErrorGenerationAcrossUnrelatedTransitions(t *testing.T) {
	env := newManagerTestEnv(t)

	env.manager.needsAttention(DesiredOn, "core_ownership_uncertain")
	generation := env.manager.Status().LastErrorGeneration
	if generation == 0 {
		t.Fatal("needsAttention 后 LastErrorGeneration 不应是零值")
	}

	// 模拟一次与失败码无关的状态转换(不经 needsAttention 的普通 setStatus 调用)。
	env.manager.setStatus(Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseActivating, Protection: ProtectionStarting})

	if got := env.manager.Status().LastErrorGeneration; got != generation {
		t.Fatalf("无关的状态转换不应改变 LastErrorGeneration: got=%d want=%d", got, generation)
	}
}

// swapGuardianLogOutput 替换标准 log 包的输出目标,返回还原函数。测试用它捕获
// Guardian 通过 log.Printf 记录的完整错误,断言"落日志"这一不变量。
func swapGuardianLogOutput(w io.Writer) func() {
	previous := log.Writer()
	log.SetOutput(w)
	return func() { log.SetOutput(previous) }
}

// acquireMutation 因锁被另一变更操作占用而超时,是真实并发场景(1 分钟的
// guardianMutationTimeout 完全够真机上撞见)——它在触碰 LastError 之前就直接
// return err。回传给调用方的 code 绝不能是更早一次不相关失败遗留的陈旧值。
func TestManagerAcquireMutationTimeoutDoesNotTouchLastError(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.needsAttention(DesiredOn, "stale_unrelated_code")

	if err := env.manager.acquireMutation(context.Background()); err != nil {
		t.Fatalf("hold mutation lock: %v", err)
	}
	defer env.manager.releaseMutation()

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := env.manager.Up(expired); err == nil {
		t.Fatal("Up 应在锁被占用且 ctx 已过期时失败")
	}
	if got := env.manager.Status().LastError; got != "stale_unrelated_code" {
		t.Errorf("acquireMutation 超时不应触碰 LastError,实际 = %q", got)
	}
}

// recoveryBlocked 为真时 Up/Down 直接 return errRecoveryIncomplete,从不触碰
// LastError。回传给调用方的 code 绝不能是更早一次不相关失败遗留的陈旧值。
func TestManagerRecoveryBlockedDoesNotTouchLastError(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.needsAttention(DesiredOn, "stale_unrelated_code")
	env.manager.recoveryBlocked = true

	err := env.manager.Up(context.Background())
	if !errors.Is(err, errRecoveryIncomplete) {
		t.Fatalf("err = %v, want errRecoveryIncomplete", err)
	}
	if got := env.manager.Status().LastError; got != "stale_unrelated_code" {
		t.Errorf("recoveryBlocked 短路不应触碰 LastError,实际 = %q", got)
	}
}

// Down 的 DNS-restore-失败但恢复成功分支(装屏障时的 setStatus 字面量已把
// LastError 清空为 "",随后 core 重启与屏障拆除都成功)全程不调用
// needsAttention 就 return restoreErr。回传给调用方的 code 绝不能是更早一次
// 不相关失败遗留的陈旧值——此时 LastError 已被清空,该回传空,不该回传陈旧值。
func TestManagerDownRestoreFailureButRecoverySuccessLeavesLastErrorEmpty(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.manager.needsAttention(DesiredOn, "stale_unrelated_code")
	env.dns.restoreErr = errors.New("dns restore failed")

	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("Down 应在 DNS 还原失败时返回错误")
	}
	if got := env.manager.Status().LastError; got != "" {
		t.Errorf("恢复成功后 LastError 应被清空,不应残留陈旧值,实际 = %q", got)
	}
}

func TestManagerUpVerifiesDNSBeforeProtected(t *testing.T) {
	env := newManagerTestEnv(t)
	env.dns.record = true
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"desired.on", "core.start", "dns.ensure", "dns.inspect"}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	status := env.manager.Status()
	if status.Protection != ProtectionProtected || status.DNSState != DNSManaged || !status.DNSManaged {
		t.Fatalf("status = %+v", status)
	}
}

func TestManagerUpDNSFailureCannotClaimProtected(t *testing.T) {
	env := newManagerTestEnv(t)
	env.dns.ensureErr = errors.New("resolver change failed")
	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("Up succeeded")
	}
	status := env.manager.Status()
	if status.Protection == ProtectionProtected || status.LastError != "dns_takeover_failed" {
		t.Fatalf("status = %+v", status)
	}
	if !env.manager.barrierProven() {
		t.Fatal("DNS takeover failure did not retain a proven barrier")
	}
}

func TestManagerUpDNSInspectionMustConfirmManaged(t *testing.T) {
	env := newManagerTestEnv(t)
	env.dns.inspectResults = []fakeDNSResult{{status: DNSStatus{State: DNSUnmanaged, Service: "Wi-Fi"}}}
	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("Up succeeded with unmanaged DNS")
	}
	status := env.manager.Status()
	if status.Protection == ProtectionProtected || status.LastError != "dns_verification_failed" || status.DNSState != DNSUnmanaged {
		t.Fatalf("status = %+v", status)
	}
	if !env.manager.barrierProven() {
		t.Fatal("DNS verification failure did not retain a proven barrier")
	}
}

func TestManagerDNSContextFailureUsesBoundedBarrierCleanupContext(t *testing.T) {
	dnsFailures := []struct {
		name      string
		configure func(*fakeDNSManager, func(context.Context) error)
		wantCode  string
	}{
		{
			name: "ensure",
			configure: func(dns *fakeDNSManager, fail func(context.Context) error) {
				dns.ensureFunc = func(ctx context.Context) (DNSStatus, error) {
					return DNSStatus{State: DNSUnknown, Service: "Wi-Fi"}, fail(ctx)
				}
			},
			wantCode: "dns_takeover_failed",
		},
		{
			name: "inspect",
			configure: func(dns *fakeDNSManager, fail func(context.Context) error) {
				dns.inspectFunc = func(ctx context.Context) (DNSStatus, error) {
					return DNSStatus{State: DNSUnknown, Service: "Wi-Fi"}, fail(ctx)
				}
			},
			wantCode: "dns_verification_failed",
		},
	}
	contextFailures := []struct {
		name string
		new  func() (context.Context, context.CancelFunc, func(context.Context) error)
	}{
		{
			name: "canceled",
			new: func() (context.Context, context.CancelFunc, func(context.Context) error) {
				ctx, cancel := context.WithCancel(context.Background())
				return ctx, cancel, func(ctx context.Context) error {
					cancel()
					return ctx.Err()
				}
			},
		},
		{
			name: "deadline",
			new: func() (context.Context, context.CancelFunc, func(context.Context) error) {
				ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
				return ctx, cancel, func(ctx context.Context) error {
					<-ctx.Done()
					return ctx.Err()
				}
			},
		},
	}

	for _, dnsFailure := range dnsFailures {
		for _, contextFailure := range contextFailures {
			t.Run(dnsFailure.name+"/"+contextFailure.name, func(t *testing.T) {
				env := newManagerTestEnv(t)
				env.manager.cleanupTimeout = 100 * time.Millisecond
				env.barrier.failIfContextDone = true
				ctx, cancel, fail := contextFailure.new()
				t.Cleanup(cancel)
				dnsFailure.configure(env.dns, fail)

				if err := env.manager.Up(ctx); err == nil {
					t.Fatal("Up succeeded after DNS request context failure")
				}
				if !env.manager.barrierProven() {
					t.Fatal("DNS context failure did not leave a proven barrier")
				}
				contextErr, deadline := env.barrier.lastInstallCallContext()
				if contextErr != nil {
					t.Fatalf("barrier context error = %v, want independent live context", contextErr)
				}
				if deadline.IsZero() || time.Until(deadline) <= 0 || time.Until(deadline) > env.manager.cleanupTimeout {
					t.Fatalf("barrier deadline = %v, want live deadline within %s", deadline, env.manager.cleanupTimeout)
				}
				if got := env.manager.Status().LastError; got != dnsFailure.wantCode {
					t.Fatalf("LastError = %q, want %q", got, dnsFailure.wantCode)
				}
			})
		}
	}
}

func TestManagerDownTransitionsBehindBarrier(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.dns.record = true
	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"barrier.install", "core.stop", "dns.restore", "desired.off", "barrier.remove"}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if got := env.manager.Status(); got.Desired != DesiredOff || got.Protection != ProtectionOff {
		t.Fatalf("status = %+v, want off", got)
	}
	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("repeated Down() error = %v", err)
	}
	want = append(want, "dns.restore")
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("repeated Down events = %#v, want stale-DNS restore %#v", got, want)
	}
}

func TestManagerDownRestoresDNSBeforeBarrierRelease(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.dns.record = true
	env.events.reset()
	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"barrier.install", "core.stop", "dns.restore", "desired.off", "barrier.remove"}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	status := env.manager.Status()
	if status.Protection != ProtectionOff || status.DNSState != DNSUnmanaged || status.DNSManaged {
		t.Fatalf("status = %+v", status)
	}
}

func TestManagerDownRestoresStaleDNSWhenAlreadyOff(t *testing.T) {
	env := newManagerTestEnv(t)
	env.dns.record = true
	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := env.events.snapshot(), []string{"dns.restore"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	status := env.manager.Status()
	if status.Protection != ProtectionOff || status.DNSState != DNSUnmanaged {
		t.Fatalf("status = %+v", status)
	}
}

func TestManagerDownCancelsPathRecoveryBeforeWaitingForMutation(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	accepted, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	core.waitForRequest(t)

	downDone := make(chan error, 1)
	go func() { downDone <- env.manager.Down(context.Background()) }()
	select {
	case <-core.canceled:
	case <-time.After(time.Second):
		t.Fatal("Down waited behind path recovery without canceling it")
	}
	select {
	case err := <-downDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Down did not finish after canceling path recovery")
	}
	if got := env.manager.Status(); got.Desired != DesiredOff || got.Protection != ProtectionOff {
		t.Fatalf("status after Down = %+v", got)
	}
	eventually(t, func() bool { return env.manager.pathRecoveryActiveCount() == 0 })
	if got := env.manager.CurrentPathRecovery(); got.ID != accepted.ID || got.State != "ignored" || got.Stage != "off" {
		t.Fatalf("interrupted recovery after Down = %+v, want %q ignored/off", got, accepted.ID)
	}
	if got := core.callCount(); got != 1 {
		t.Fatalf("Core calls after Down = %d, want interrupted attempt only", got)
	}
}

func TestManagerDownFencesPathRecoveryAdmissionUntilTransitionCompletes(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	if err := env.manager.acquireMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	released := false
	downObserved := false
	downDone := make(chan error, 1)
	defer func() {
		if !released {
			env.manager.releaseMutation()
		}
		if !downObserved {
			select {
			case <-downDone:
			case <-time.After(time.Second):
			}
		}
	}()

	if _, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-a"}); err != nil {
		t.Fatal(err)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		downDone <- env.manager.Down(ctx)
	}()
	eventually(t, func() bool { return env.manager.pathRecoveryActiveCount() == 0 })

	accepted, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-b"})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.State != "accepted" || accepted.ID == "" {
		t.Fatalf("gap admission = %+v, want accepted", accepted)
	}
	if got := env.manager.pathRecoveryActiveCount(); got != 0 {
		t.Fatalf("gap admission started %d path recovery worker(s) before Down owned mutation", got)
	}
	if got := core.callCount(); got != 0 {
		t.Fatalf("Core calls before Down transition = %d, want 0", got)
	}

	env.manager.releaseMutation()
	released = true
	select {
	case err := <-downDone:
		downObserved = true
		if err != nil {
			t.Fatalf("Down exhausted its bounded context: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Down deadlocked with path recovery admitted during cancellation gap")
	}
	current := env.manager.CurrentPathRecovery()
	if current.ID != accepted.ID || current.State != "ignored" || current.Stage != "off" {
		t.Fatalf("gap recovery after Down = %+v, want accepted ID resolved Off", current)
	}
	if got := core.callCount(); got != 0 {
		t.Fatalf("Core calls after Down = %d, want 0", got)
	}
}

func TestManagerFailedDownReplaysGeneratedPathRecoveryWhileDesiredRemainsOn(t *testing.T) {
	tests := []struct {
		name            string
		prepare         func(*testing.T, *managerTestEnv)
		runDown         func(*managerTestEnv) error
		firstCoreStarts bool
	}{
		{
			name: "mutation timeout",
			prepare: func(t *testing.T, env *managerTestEnv) {
				t.Helper()
				if err := env.manager.acquireMutation(context.Background()); err != nil {
					t.Fatal(err)
				}
			},
			runDown: func(env *managerTestEnv) error {
				expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
				err := env.manager.Down(expired)
				env.manager.releaseMutation()
				return err
			},
		},
		{
			name: "barrier install failure",
			prepare: func(_ *testing.T, env *managerTestEnv) {
				env.barrier.installErr = errors.New("barrier unavailable")
			},
			runDown: func(env *managerTestEnv) error {
				return env.manager.Down(context.Background())
			},
			firstCoreStarts: true,
		},
		{
			name: "Core stop failure",
			prepare: func(_ *testing.T, env *managerTestEnv) {
				env.runner.stopErr = errors.New("Core stop failed")
			},
			runDown: func(env *managerTestEnv) error {
				return env.manager.Down(context.Background())
			},
			firstCoreStarts: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newProtectedManagerTestEnv(t)
			core := newFakeCorePathClient(false)
			env.manager.corePath = core
			tt.prepare(t, env)

			accepted, err := env.manager.RequestPathRecovery(RecoveryRequest{
				Reason:     "underlay_changed",
				Generation: "wifi-b",
			})
			if err != nil {
				t.Fatal(err)
			}
			if tt.firstCoreStarts {
				if got := core.waitForRequest(t); got.Generation != "wifi-b" {
					t.Fatalf("first Core request = %+v, want wifi-b", got)
				}
			}

			if err := tt.runDown(env); err == nil {
				t.Fatal("Down succeeded despite injected failure")
			}
			if got := env.manager.Status().Desired; got != DesiredOn {
				t.Fatalf("desired state after failed Down = %q, want On", got)
			}

			if got := core.waitForRequest(t); got.Generation != "wifi-b" {
				t.Fatalf("replayed Core request = %+v, want wifi-b", got)
			}
			core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{
				State: "succeeded", Stage: "succeeded",
			}})
			eventually(t, func() bool {
				current := env.manager.CurrentPathRecovery()
				return current.ID == accepted.ID && current.State == "succeeded"
			})
			wantCalls := 1
			if tt.firstCoreStarts {
				wantCalls = 2
			}
			if got := core.callCount(); got != wantCalls {
				t.Fatalf("Core calls after failed Down = %d, want %d", got, wantCalls)
			}
		})
	}
}

func TestDaemonShutdownCancelsAndDrainsPathRecovery(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	socketPath := filepath.Join(shortSocketDir(t), "guard.sock")
	daemon, err := StartDaemon(context.Background(), DaemonOptions{
		SocketPath: socketPath,
		Handler:    NewLocalAPI(env.manager),
		OwnerUID:   uint32(os.Geteuid()),
		PeerCredentials: func(net.Conn) (uint32, bool) {
			return 0, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := NewClient(socketPath).RequestRecovery(context.Background(), RecoveryRequest{Reason: "manual"}); err != nil {
		_ = daemon.Close()
		t.Fatal(err)
	}
	core.waitForRequest(t)
	closeDone := make(chan error, 1)
	go func() { closeDone <- daemon.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not cancel and drain path recovery")
	}
	if got := env.manager.pathRecoveryActiveCount(); got != 0 {
		t.Fatalf("active path recoveries after shutdown = %d", got)
	}
	if got := env.manager.recoveryActiveCount(); got != 0 {
		t.Fatalf("unexpected-Core recovery count after path drain = %d", got)
	}
}

func TestManagerMigrateTransitionsLegacyCoreBehindValidatedBarrier(t *testing.T) {
	env := newManagerTestEnv(t)
	request := MigrationRequest{
		Gateway:      "192.0.2.1",
		ServerBypass: []string{"198.51.100.10/32", "2001:db8::10/128"},
	}
	if err := env.manager.Migrate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"desired.on",
		"barrier.install",
		"legacy.stop",
		"barrier.reassert",
		"core.start",
		"barrier.release",
		"legacy.remove",
	}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("migration events = %#v, want %#v", got, want)
	}
	barrierContext := env.barrier.lastInstallContext()
	if barrierContext.Gateway != request.Gateway || !barrierContext.BlockIPv6 {
		t.Fatalf("migration barrier context = %+v", barrierContext)
	}
	if !reflect.DeepEqual(barrierContext.ServerBypass, []string{"198.51.100.10/32"}) {
		t.Fatalf("migration IPv4 barrier bypass = %#v", barrierContext.ServerBypass)
	}
	if got := env.manager.Status(); got.Protection != ProtectionProtected || got.Desired != DesiredOn {
		t.Fatalf("migration status = %+v, want protected/on", got)
	}
}

func TestManagerMigrateRejectsUnsafeMetadataBeforeMutation(t *testing.T) {
	env := newManagerTestEnv(t)
	err := env.manager.Migrate(context.Background(), MigrationRequest{
		Gateway:      "192.0.2.1",
		ServerBypass: []string{"198.51.100.0/24"},
	})
	if err == nil {
		t.Fatal("unsafe migration bypass accepted")
	}
	if got := env.events.snapshot(); len(got) != 0 {
		t.Fatalf("unsafe metadata caused mutation: %#v", got)
	}
	if env.legacy.stopCount != 0 || env.runner.startCount() != 0 {
		t.Fatal("unsafe metadata stopped old Core or started a second Core")
	}
}

func TestManagerMigrateBarrierFailureLeavesLegacyCoreUntouchedAndFailsClosed(t *testing.T) {
	env := newManagerTestEnv(t)
	env.barrier.installErr = errors.New("partial barrier install failed")
	err := env.manager.Migrate(context.Background(), MigrationRequest{
		Gateway:      "192.0.2.1",
		ServerBypass: []string{"198.51.100.10/32"},
	})
	if err == nil {
		t.Fatal("barrier failure accepted")
	}
	if got, want := env.events.snapshot(), []string{"desired.on", "barrier.install"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("migration events = %#v, want %#v", got, want)
	}
	if desired, err := env.store.LoadDesired(); err != nil || desired != DesiredOn {
		t.Fatalf("desired after pre-barrier failure = %q, %v; want on for gated recovery", desired, err)
	}
	if env.legacy.stopCount != 0 || env.runner.startCount() != 0 {
		t.Fatal("barrier failure stopped old Core or started a second Core")
	}
	if env.manager.barrierProven() || env.manager.barrierOwnership.proof != barrierInstallAttempted {
		t.Fatal("failed barrier install was not retained as attempted and unproven")
	}
	if got := env.manager.Status(); got.Protection != ProtectionNeedsAttention || got.LastError != "barrier_install_failed" {
		t.Fatalf("migration status = %+v, want unproven barrier_install_failed", got)
	}
}

func TestManagerMigrateLegacyRemovalFailureRetainsBarrier(t *testing.T) {
	env := newManagerTestEnv(t)
	env.legacy.removeErr = errors.New("read-only filesystem")
	err := env.manager.Migrate(context.Background(), MigrationRequest{
		Gateway:      "192.0.2.1",
		ServerBypass: []string{"198.51.100.10/32"},
	})
	if err == nil {
		t.Fatal("legacy plist removal failure accepted")
	}
	want := []string{"desired.on", "barrier.install", "legacy.stop", "barrier.reassert", "core.start", "barrier.release", "legacy.remove", "barrier.install"}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("migration events = %#v, want %#v", got, want)
	}
	if !env.manager.barrierProven() {
		t.Fatal("migration barrier released before legacy ownership was removed")
	}
	status := env.manager.Status()
	if status.Protection != ProtectionBlocked || status.Phase != PhaseNeedsAttention || status.LastError != "legacy_unit_remove_failed" {
		t.Fatalf("migration status = %+v, want blocked legacy_unit_remove_failed", status)
	}
}

func TestManagerUpAndRecoverRefuseLegacyOwnership(t *testing.T) {
	for _, operation := range []struct {
		name string
		run  func(*Manager) error
	}{
		{name: "up", run: func(manager *Manager) error { return manager.Up(context.Background()) }},
		{name: "recover", run: func(manager *Manager) error { return manager.Recover(context.Background()) }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			env := newManagerTestEnv(t)
			env.legacy.present = true
			if operation.name == "recover" {
				if err := env.store.SaveDesired(DesiredOn); err != nil {
					t.Fatal(err)
				}
				env.events.reset()
			}
			if err := operation.run(env.manager); err == nil {
				t.Fatal("legacy ownership was accepted")
			}
			if env.runner.startCount() != 0 {
				t.Fatal("Guardian started a second Core while legacy ownership remained")
			}
			status := env.manager.Status()
			if status.Protection != ProtectionNeedsAttention || status.LastError != "legacy_core_migration_pending" {
				t.Fatalf("status = %+v", status)
			}
		})
	}
}

func TestManagerRecoverRestoresStaleDNSBeforePublishingOff(t *testing.T) {
	env := newManagerTestEnv(t)
	env.dns.record = true

	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := env.events.snapshot(), []string{"dns.restore"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	status := env.manager.Status()
	if status.Protection != ProtectionOff || status.DNSState != DNSUnmanaged || status.DNSManaged {
		t.Fatalf("status = %+v, want Off with unmanaged DNS", status)
	}
}

func TestManagerRecoverDNSRestoreFailureCannotPublishOff(t *testing.T) {
	env := newManagerTestEnv(t)
	env.dns.restoreErr = errors.New("resolver restore failed")

	if err := env.manager.Recover(context.Background()); err == nil {
		t.Fatal("Recover succeeded despite stale DNS restore failure")
	}
	status := env.manager.Status()
	if status.Protection == ProtectionOff || status.LastError != "dns_restore_failed" {
		t.Fatalf("status = %+v, want non-Off dns_restore_failed", status)
	}
	if !env.manager.recoveryBlocked {
		t.Fatal("startup recovery unblocked mutations after DNS restore failure")
	}
}

// TestBeginStartupRecoveryFencesUpBeforeRecoverRuns is the regression guard
// for the fail-closed window fd1fd90 opened: the daemon reorder makes the
// LocalAPI socket observable before the startup Recover call actually runs
// (it moved to a background goroutine), so a client racing to connect the
// instant the socket exists could win Manager's single-slot mutation channel
// ahead of that goroutine. Before this fix, recoveryBlocked only became true
// once Recover itself started running — so a mutation winning that race
// would see recoveryBlocked still false and proceed to start Core ahead of
// recoverUpdateLocked ever inspecting the crash-mid-update journal. This
// simulates the winning side of that race directly: BeginStartupRecovery
// raises recoveryBlocked synchronously, so Up must fail closed even though
// nothing has attempted RunStartupRecovery yet.
func TestBeginStartupRecoveryFencesUpBeforeRecoverRuns(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	env.events.reset()

	env.manager.BeginStartupRecovery()

	if err := env.manager.Up(context.Background()); !errors.Is(err, errRecoveryIncomplete) {
		t.Fatalf("Up before RunStartupRecovery ran = %v, want errRecoveryIncomplete", err)
	}
	if env.runner.startCount() != 0 {
		t.Fatal("Core started ahead of startup recovery inspecting the crash-recovery journal")
	}

	if err := env.manager.RunStartupRecovery(context.Background()); err != nil {
		t.Fatalf("RunStartupRecovery: %v", err)
	}
	if env.runner.startCount() != 1 {
		t.Fatalf("Core start count after recovery = %d, want 1", env.runner.startCount())
	}
	if env.manager.pathRecoveryFences != 0 {
		t.Fatalf("pathRecoveryFences = %d after RunStartupRecovery, want 0 (leaked fence would permanently queue recoveries)", env.manager.pathRecoveryFences)
	}

	// Startup recovery has genuinely completed now: Up must no longer be
	// fenced.
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatalf("Up after recovery completed: %v", err)
	}
}

// TestAbortStartupRecoveryReleasesFenceWithoutRunning proves the balancing
// half of BeginStartupRecovery used on the daemon-start-failure path: it must
// release the path-recovery fence without performing any recovery work, and
// must leave recoveryBlocked set (no Recover actually ran, so nothing should
// assume the crash journal was inspected).
func TestAbortStartupRecoveryReleasesFenceWithoutRunning(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.BeginStartupRecovery()

	env.manager.AbortStartupRecovery()

	if env.manager.pathRecoveryFences != 0 {
		t.Fatalf("pathRecoveryFences = %d after AbortStartupRecovery, want 0", env.manager.pathRecoveryFences)
	}
	if !env.manager.recoveryBlocked {
		t.Fatal("recoveryBlocked was cleared by AbortStartupRecovery; no Recover ever ran")
	}
	if err := env.manager.Up(context.Background()); !errors.Is(err, errRecoveryIncomplete) {
		t.Fatalf("Up after AbortStartupRecovery = %v, want errRecoveryIncomplete (no recovery ever ran)", err)
	}
}

func TestManagerAdoptsMatchingHealthyCore(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.existing = Process{PID: 42, Executable: install.BinPath, UID: 0}
	env.health.runtime = healthyRuntime(42)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("unexpected starts = %d", got)
	}
	if got := env.manager.Status(); got.CorePID != 42 || got.Protection != ProtectionProtected {
		t.Fatalf("status = %+v, want adopted protected Core", got)
	}
}

func TestManagerAdoptsHealthyCoreOnlyAfterDNSVerification(t *testing.T) {
	env := newManagerTestEnv(t)
	env.dns.record = true
	env.runner.existing = Process{PID: 42, Executable: install.BinPath, UID: 0}
	env.health.runtime = healthyRuntime(42)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"desired.on", "dns.ensure", "dns.inspect"}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if got := env.manager.Status(); got.CorePID != 42 || got.Protection != ProtectionProtected || got.DNSState != DNSManaged {
		t.Fatalf("status = %+v, want adopted protected Core with managed DNS", got)
	}
}

func TestManagerRepeatedAdoptionHealthFailureDoesNotStartWatchers(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	env := newManagerTestEnv(t)
	env.manager.runner = runner
	env.health.err = errors.New("adopted Core unhealthy")
	t.Cleanup(func() {
		operations.setAlive(false)
		time.Sleep(3 * runner.InspectInterval)
	})

	for attempt := 0; attempt < 2; attempt++ {
		if err := env.manager.Up(context.Background()); err == nil {
			t.Fatalf("Up attempt %d succeeded despite adoption health failure", attempt+1)
		}
	}
	time.Sleep(4 * runner.InspectInterval)
	if got := operations.inspectCount(); got != 2 {
		t.Fatalf("repeated failed adoption accumulated watchers: inspections=%d, want 2", got)
	}
}

func TestManagerBarrierRemovalRetryReusesAcceptedWatcher(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.Store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	existing := Process{PID: 42, Executable: install.BinPath, UID: 0, Generation: "adopted:1"}
	env.runner.existing = existing
	env.runner.watchExit = make(chan error, 1)
	env.manager.barrierOwnership = barrierOwnership{proof: barrierProven, context: cloneBarrierContext(env.manager.barrierContext)}
	env.manager.setStatus(Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseNeedsAttention, Protection: ProtectionBlocked})
	env.barrier.removeErr = errors.New("barrier remove failed")
	t.Cleanup(func() { cleanupManagerWatchers(env) })

	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("first Up succeeded despite barrier removal failure")
	}
	if got := env.runner.watchCount(); got != 1 {
		t.Fatalf("accepted watcher starts after first Up = %d, want 1", got)
	}
	env.barrier.removeErr = nil
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.runner.watchCount(); got != 1 {
		t.Fatalf("barrier retry accumulated watchers: starts=%d", got)
	}
	if got := env.manager.Status(); got.Protection != ProtectionProtected {
		t.Fatalf("status = %+v, want Protected after successful retry", got)
	}
}

func TestManagerAcceptedAdoptionObservesExit(t *testing.T) {
	env := newManagerTestEnv(t)
	existing := Process{PID: 42, Executable: install.BinPath, UID: 0, Generation: "adopted:1"}
	env.runner.existing = existing
	env.runner.watchExit = make(chan error, 1)
	t.Cleanup(func() { cleanupManagerWatchers(env) })
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.runner.watchCount(); got != 1 {
		t.Fatalf("accepted watcher starts = %d, want 1", got)
	}

	env.runner.exitWatched(errors.New("adopted Core exited"))
	eventually(t, func() bool { return env.runner.startCount() == 1 })
	eventually(t, func() bool { return env.manager.Status().Protection == ProtectionProtected })
}

func cleanupManagerWatchers(env *managerTestEnv) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_ = env.manager.Down(ctx)
	cancel()
	env.runner.exitWatched(errors.New("test cleanup"))
	current := env.runner.currentProcess()
	env.runner.exit(current.PID, errors.New("test cleanup"))
	time.Sleep(10 * time.Millisecond)
}

func TestManagerRejectsUnverifiableExistingPID(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.existing = Process{PID: 42, Executable: "/tmp/not-bx", UID: 501}
	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("unverifiable process adopted")
	}
	if got := env.runner.signalCount(); got != 0 {
		t.Fatalf("unrelated process was signalled %d times", got)
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("second Core started beside unverifiable PID: starts=%d", got)
	}
}

func TestManagerRejectsRuntimePIDMismatchWithoutSignalling(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.existing = Process{PID: 42, Executable: install.BinPath, UID: 0}
	env.health.runtime = healthyRuntime(43)
	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("runtime PID mismatch was adopted")
	}
	if got := env.runner.signalCount(); got != 0 {
		t.Fatalf("existing process was signalled %d times", got)
	}
}

func TestManagerUpInspectionFailureDoesNotStartSecondCore(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.existingErr = errors.New("inspect permission denied")
	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("Up succeeded despite ambiguous process inspection")
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("second Core started after inspection failure: starts=%d", got)
	}
	if got := env.manager.Status(); got.Protection != ProtectionNeedsAttention {
		t.Fatalf("status = %+v, want needs_attention", got)
	}
}

func TestManagerUpBlocksSameAndReconstructedDaemonAfterUncertainLaunch(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := newUncertainStartTestProcess(52)
	operations := &startTestProcessOperations{
		started: started,
		process: Process{PID: 52, Executable: executable, UID: 0, Generation: "darwin:123:456"},
	}
	statePath := filepath.Join(dir, "core-process.json")
	newRunner := func() *ExecCoreRunner {
		runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
		runner.StatePath = statePath
		runner.Operations = operations
		runner.LaunchCleanupTimeout = 10 * time.Millisecond
		runner.SaveProcessRecord = func(path string, record processRecord) error {
			if record.State == processRecordLaunching {
				return saveProcessRecord(path, record)
			}
			return errors.New("normal process record write failed")
		}
		return runner
	}
	env := newManagerTestEnv(t)
	env.manager.runner = newRunner()
	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("initial Up succeeded despite unproven launch cleanup")
	}
	if err := env.manager.Up(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("same-daemon retry error = %v, want uncertain ownership", err)
	}
	// 同一 daemon 的重试保护来自内存态 m.current.Uncertain(upLocked 顶部的
	// short-circuit),从不重新调用 Existing()/Start():只应有第一次 Up() 那一次
	// 真实启动尝试。
	if got := operations.startCount(); got != 1 {
		t.Fatalf("same-daemon retry attempted a duplicate spawn: starts = %d, want 1", got)
	}

	reconstructed, err := NewManager(ManagerOptions{
		Store: env.store, Runner: newRunner(), Health: env.health, Barrier: env.barrier, DNS: env.dns,
		BarrierContext: env.manager.barrierContext, CoreVersion: version.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 重建的 Manager 没有内存态可以短路:它读到的仍是那个 PID=0 的 launching 标
	// 记,与 Guardian 真的崩溃后什么都没留下的孤儿标记字节完全相同——本轮修复
	// 让 Existing() 按既定取舍(PID==0 结构上无法验证归属任何进程)自愈它,于是
	// reconstructed.Up() 会真的重新尝试起一次 Core,而不是被陈旧标记提前拦下
	// (这正是本轮修复的目的)。安全性没有被削弱:同一个持久化失败会再次发生,
	// Start() 仍然以 ErrProcessOwnershipUncertain 收场——只是从"看见旧文件就拒
	// 绝"变成了"真尝试、真失败",所以这里预期一次新增的启动尝试。
	if err := reconstructed.Up(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("reconstructed Up error = %v, want uncertain ownership", err)
	}
	if got := operations.startCount(); got != 2 {
		t.Fatalf("reconstructed retry starts = %d, want 2 (genuine second attempt after orphan self-heal, same underlying failure recurs)", got)
	}
}

func TestManagerHealthFailureUsesLiveBoundedCleanupAndBlocksRetryWhenExitUnproven(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.cleanupTimeout = 20 * time.Millisecond
	env.health.err = errors.New("Core unhealthy")
	env.runner.stopErr = errors.New("cooperative shutdown could not prove exit")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := env.manager.Up(ctx); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Up error = %v, want uncertain ownership", err)
	}
	if got := env.runner.stopEntryContextError(); got != nil {
		t.Fatalf("cleanup inherited expired health context: %v", got)
	}
	if err := env.manager.Up(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("retry error = %v, want uncertain ownership", err)
	}
	if got := env.runner.startCount(); got != 1 {
		t.Fatalf("retry started duplicate Core: starts=%d", got)
	}
}

func TestManagerReservesCleanupWithinAcceptedMutationDeadline(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.cleanupTimeout = 30 * time.Millisecond
	env.health.waitForContext = true
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()

	if err := env.manager.Up(ctx); err == nil {
		t.Fatal("Up succeeded despite health deadline")
	}
	healthDeadline := env.health.lastContextDeadline()
	if healthDeadline.IsZero() || healthDeadline.After(deadline.Add(-20*time.Millisecond)) {
		t.Fatalf("health deadline = %s, want cleanup reserved before %s", healthDeadline, deadline)
	}
	if got := env.runner.stopEntryContextError(); got != nil {
		t.Fatalf("cleanup inherited expired operation context: %v", got)
	}
	cleanupDeadline := env.runner.stopEntryDeadline()
	if cleanupDeadline.IsZero() || cleanupDeadline.After(deadline) {
		t.Fatalf("cleanup deadline = %s, want no later than accepted deadline %s", cleanupDeadline, deadline)
	}
}

func TestManagerUncertainExitDoesNotRestartCore(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	process := env.runner.currentProcess()
	env.events.reset()
	env.runner.exit(process.PID, uncertainOwnership(process, errors.New("owned record removal failed")))
	eventually(t, func() bool { return env.manager.Status().LastError == "core_ownership_uncertain" })
	if got := env.runner.startCount(); got != 1 {
		t.Fatalf("uncertain exit restarted Core: starts=%d", got)
	}
	if !env.manager.current.Uncertain {
		t.Fatal("uncertain exit did not retain blocking ownership state")
	}
}

func TestManagerLateLaunchCleanupProofClearsUncertaintyForRetry(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := newUncertainStartTestProcess(57)
	operations := &startTestProcessOperations{
		started: first,
		process: Process{PID: 57, Executable: executable, UID: 0, Generation: "darwin:123:461"},
	}
	statePath := filepath.Join(dir, "core-process.json")
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = statePath
	runner.Operations = operations
	runner.LaunchCleanupTimeout = 10 * time.Millisecond
	runner.SaveProcessRecord = func(path string, record processRecord) error {
		if record.State == processRecordLaunching {
			return saveProcessRecord(path, record)
		}
		return errors.New("normal process record write failed")
	}
	env := newManagerTestEnv(t)
	env.manager.runner = runner
	if err := env.manager.Up(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("initial Up error = %v, want uncertain ownership", err)
	}
	if !env.manager.current.Uncertain {
		t.Fatal("initial failed launch did not retain uncertainty")
	}

	first.release()
	eventually(t, func() bool { return env.manager.Status().LastError != "core_ownership_uncertain" })
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker after late proof = %v, want removed", err)
	}
	second := newStartTestProcess(58)
	operations.setStarted(second, Process{PID: 58, Executable: executable, UID: 0, Generation: "darwin:123:462"})
	runner.SaveProcessRecord = nil
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatalf("same-daemon retry after durable proof: %v", err)
	}
	if got := operations.startCount(); got != 2 {
		t.Fatalf("starts = %d, want late-proven retry", got)
	}
}

func TestManagerPostForkCleanupHonorsAcceptedDeadlineAndLateProofClearsUncertainty(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := newUncertainStartTestProcess(59)
	operations := &startTestProcessOperations{
		started: first,
		process: Process{PID: 59, Executable: executable, UID: 0, Generation: "darwin:123:463"},
	}
	statePath := filepath.Join(dir, "core-process.json")
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = statePath
	runner.Operations = operations
	runner.LaunchCleanupTimeout = 200 * time.Millisecond
	runner.SaveProcessRecord = func(path string, record processRecord) error {
		if record.State == processRecordLaunching {
			return saveProcessRecord(path, record)
		}
		return errors.New("normal process record write failed")
	}
	env := newManagerTestEnv(t)
	env.manager.runner = runner
	env.manager.cleanupTimeout = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	started := time.Now()
	if err := env.manager.Up(ctx); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("Up error = %v, want uncertain ownership", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("post-fork cleanup exceeded accepted deadline: elapsed=%s", elapsed)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("accepted context error = %v, want deadline exceeded", ctx.Err())
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("marker disappeared before delayed Wait proof: %v", err)
	}

	first.release()
	eventually(t, func() bool {
		_, err := os.Stat(statePath)
		return errors.Is(err, os.ErrNotExist) && env.manager.Status().LastError != "core_ownership_uncertain"
	})
}

func TestManagerUpFailureDoesNotClaimBarrierProtection(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.startErr = errors.New("start failed")
	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("Up succeeded despite start failure")
	}
	if got := env.manager.Status(); got.Protection != ProtectionNeedsAttention {
		t.Fatalf("protection = %q, want %q without an installed barrier", got.Protection, ProtectionNeedsAttention)
	}
}

func TestManagerSerializesMutations(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.blockStart = make(chan struct{})
	firstDone := make(chan error, 1)
	go func() { firstDone <- env.manager.Up(context.Background()) }()
	select {
	case <-env.runner.startEntered:
	case <-time.After(time.Second):
		t.Fatal("Up did not enter Core start")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- env.manager.Down(context.Background()) }()
	select {
	case err := <-secondDone:
		t.Fatalf("Down overlapped Up: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(env.runner.blockStart)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestManagerQueuedExpiredMutationPerformsNoWrites(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.blockStart = make(chan struct{})
	firstDone := make(chan error, 1)
	go func() { firstDone <- env.manager.Up(context.Background()) }()
	select {
	case <-env.runner.startEntered:
	case <-time.After(time.Second):
		t.Fatal("Up did not enter Core start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() { secondDone <- env.manager.Down(ctx) }()
	<-ctx.Done()
	close(env.runner.blockStart)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired queued Down error = %v, want deadline exceeded", err)
	}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, []string{"desired.on", "core.start"}) {
		t.Fatalf("expired queued Down mutated state: events=%#v", got)
	}
	if got, err := env.store.LoadDesired(); err != nil || got != DesiredOn {
		t.Fatalf("desired after expired Down = %q, %v; want on", got, err)
	}
	if got := env.manager.Status(); got.Protection != ProtectionProtected {
		t.Fatalf("status after expired Down = %+v, want protected", got)
	}
}

func TestManagerDownRestoreFailureRecoversProtectedCore(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.dns.record = true
	env.dns.restoreErr = errors.New("dns restore failed")
	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("Down succeeded despite restoration failure")
	}
	want := []string{"barrier.install", "core.stop", "dns.restore", "core.start", "dns.ensure", "dns.inspect", "barrier.release"}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if got, _ := env.store.LoadDesired(); got != DesiredOn {
		t.Fatalf("desired = %q, want recovery to preserve on", got)
	}
	if got := env.manager.Status(); got.Protection != ProtectionProtected || got.Phase == PhaseNeedsAttention {
		t.Fatalf("status = %+v, want recovered protection", got)
	}
}

func TestManagerDownRestoreFailureReverifiesDNSBeforeRecoveredProtection(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.dns.record = true
	env.dns.restoreErr = errors.New("dns restore failed")
	env.events.reset()
	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("Down succeeded despite restoration failure")
	}
	want := []string{
		"barrier.install",
		"core.stop",
		"dns.restore",
		"core.start",
		"dns.ensure",
		"dns.inspect",
		"barrier.release",
	}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	status := env.manager.Status()
	if status.Protection != ProtectionProtected || status.DNSState != DNSManaged {
		t.Fatalf("status = %+v, want recovered protection with managed DNS", status)
	}
}

func TestManagerDownRecoveryPreservesDNSActivationError(t *testing.T) {
	for _, tt := range dnsActivationFailureCases() {
		t.Run(tt.name, func(t *testing.T) {
			env := newProtectedManagerTestEnv(t)
			env.dns.restoreErr = errors.New("dns restore failed")
			tt.configure(env.dns)

			if err := env.manager.Down(context.Background()); err == nil {
				t.Fatal("Down succeeded despite restore and DNS activation failures")
			}
			status := env.manager.Status()
			if status.LastError != tt.wantCode {
				t.Fatalf("LastError = %q, want %q; status=%+v", status.LastError, tt.wantCode, status)
			}
			if status.Protection != ProtectionBlocked || !env.manager.barrierProven() {
				t.Fatalf("status = %+v, want proven blocked protection", status)
			}
		})
	}
}

func TestManagerDownRestoreTimeoutUsesReservedRecoveryBudget(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.manager.restartTimeout = 40 * time.Millisecond
	env.dns.record = true
	env.dns.waitForContext = true
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if err := env.manager.Down(ctx); err == nil {
		t.Fatal("Down succeeded despite restore timeout")
	}
	if err := env.dns.lastContextError(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("restore context error = %v, want deadline exceeded", err)
	}
	if got := env.runner.startCount(); got != 2 {
		t.Fatalf("restore timeout did not attempt protected recovery: starts=%d", got)
	}
	startErrs := env.runner.startEntryContextErrors()
	if len(startErrs) != 2 || startErrs[1] != nil {
		t.Fatalf("recovery start context errors = %#v, want live second context", startErrs)
	}
	want := []string{"barrier.install", "core.stop", "dns.restore", "core.start", "dns.ensure", "dns.inspect", "barrier.release"}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if got := env.manager.Status(); got.Protection != ProtectionProtected {
		t.Fatalf("status = %+v, want recovered Protected Core", got)
	}
}

func TestManagerDownRestoreAndRecoveryStayWithinOverallDeadline(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.manager.restartTimeout = 30 * time.Millisecond
	env.dns.waitForContext = true
	env.runner.blockStartUntilContext = true
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	overallDeadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("overall mutation context has no deadline")
	}

	started := time.Now()
	if err := env.manager.Down(ctx); err == nil {
		t.Fatal("Down succeeded despite restore and recovery timeouts")
	}
	if elapsed := time.Since(started); elapsed >= 250*time.Millisecond {
		t.Fatalf("Down exceeded bounded restore/recovery phases: elapsed=%s", elapsed)
	}
	if got := env.runner.startCount(); got != 2 {
		t.Fatalf("recovery attempts = %d, want initial start plus one recovery", got)
	}
	startErrs := env.runner.startEntryContextErrors()
	if len(startErrs) != 2 || startErrs[1] != nil {
		t.Fatalf("recovery start context errors = %#v, want live bounded context", startErrs)
	}
	deadlines := env.runner.startDeadlinesSnapshot()
	if len(deadlines) != 2 || deadlines[1].IsZero() || deadlines[1].After(overallDeadline) {
		t.Fatalf("recovery deadlines = %#v, want child deadline no later than %s", deadlines, overallDeadline)
	}
	if !env.manager.barrierProven() {
		t.Fatal("barrier released after bounded recovery failure")
	}
	if got := env.manager.Status(); got.Protection != ProtectionBlocked || got.Phase != PhaseNeedsAttention {
		t.Fatalf("status = %+v, want blocked needs_attention", got)
	}
}

func TestManagerDownDoubleFailureKeepsBarrierAndNeedsAttention(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.dns.record = true
	env.dns.restoreErr = errors.New("dns restore failed")
	env.runner.startErr = errors.New("Core restart failed")
	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("Down succeeded despite restoration and recovery failures")
	}
	want := []string{"barrier.install", "core.stop", "dns.restore", "core.start"}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if got := env.manager.Status(); got.Phase != PhaseNeedsAttention || got.Protection != ProtectionBlocked {
		t.Fatalf("status = %+v, want blocked needs_attention", got)
	}
	if got, _ := env.store.LoadDesired(); got != DesiredOn {
		t.Fatalf("desired = %q, want on", got)
	}
}

func TestBarrierContextForRuntimeResolvesGatewayLazily(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.barrierContext = BarrierContext{BlockIPv6: true}
	gateways := &fakeGatewayProvider{gateway: "192.168.1.1"}
	env.manager.gatewayProvider = gateways

	barrierContext, err := env.manager.barrierContextForRuntime(context.Background(), supervisor.RuntimeState{ServerBypass: []string{"203.0.113.20/32"}})
	if err != nil {
		t.Fatal(err)
	}
	if barrierContext.Gateway != "192.168.1.1" {
		t.Fatalf("gateway = %q, want lazily resolved 192.168.1.1", barrierContext.Gateway)
	}
	if gateways.callCount() != 1 {
		t.Fatalf("gateway provider calls = %d, want exactly 1", gateways.callCount())
	}
}

func TestBarrierContextForRuntimeFailsWhenGatewayUnavailable(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.barrierContext = BarrierContext{BlockIPv6: true}
	env.manager.gatewayProvider = &fakeGatewayProvider{err: errors.New("no default gateway")}

	if _, err := env.manager.barrierContextForRuntime(context.Background(), supervisor.RuntimeState{ServerBypass: []string{"203.0.113.20/32"}}); err == nil {
		t.Fatal("want error (fail-closed) when gateway cannot be resolved for a bypass barrier")
	}
}

func TestManagerUnexpectedExitInstallsBarrierAndRestartsOnce(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	process := env.runner.currentProcess()
	env.events.reset()
	env.runner.exit(process.PID, errors.New("unexpected exit"))
	eventually(t, func() bool { return env.runner.startCount() == 2 })
	eventually(t, func() bool { return env.manager.Status().Protection == ProtectionProtected })
	want := []string{"barrier.install", "core.start", "barrier.release"}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	installed := env.barrier.lastInstallContext()
	if len(installed.ServerBypass) == 0 {
		t.Fatal("installed barrier ServerBypass is empty, want bypass-carrying barrier when gateway resolves")
	}
	if installed.blockOnly {
		t.Fatal("installed barrier blockOnly = true, want bypass-carrying (not block-only) when gateway resolves")
	}
}

func TestManagerUnexpectedExitPreservesDNSActivationError(t *testing.T) {
	for _, tt := range dnsActivationFailureCases() {
		t.Run(tt.name, func(t *testing.T) {
			env := newProtectedManagerTestEnv(t)
			tt.configure(env.dns)
			process := env.runner.currentProcess()

			env.runner.exit(process.PID, errors.New("unexpected exit"))
			eventually(t, func() bool {
				return env.runner.startCount() == 2 && env.manager.recoveryActiveCount() == 0
			})

			status := env.manager.Status()
			if status.LastError != tt.wantCode {
				t.Fatalf("LastError = %q, want %q; status=%+v", status.LastError, tt.wantCode, status)
			}
			if status.Protection != ProtectionBlocked || !env.manager.barrierProven() {
				t.Fatalf("status = %+v, want proven blocked protection", status)
			}
		})
	}
}

// TestManagerUnexpectedExitDegradesToBlockOnlyBarrierWhenGatewayUnavailable
// covers the fail-open window a reviewer found in lazy gateway discovery
// (Task 4): if the default gateway cannot be resolved during an
// unexpected-exit restart (e.g. another VPN owns the default route via a
// point-to-point utun), Guardian must still install a barrier — degraded to
// block-only (no bypasses) — rather than restarting Core with no barrier at
// all and leaking traffic direct.
func TestManagerUnexpectedExitDegradesToBlockOnlyBarrierWhenGatewayUnavailable(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.manager.barrierContext.Gateway = ""
	env.manager.gatewayProvider = &fakeGatewayProvider{err: errors.New("no default gateway")}
	process := env.runner.currentProcess()
	if len(env.manager.runtime.ServerBypass) == 0 {
		t.Fatal("test setup: runtime ServerBypass is empty, want non-empty to exercise gateway resolution")
	}
	env.events.reset()

	env.runner.exit(process.PID, errors.New("unexpected exit"))
	eventually(t, func() bool { return env.runner.startCount() == 2 })

	events := env.events.snapshot()
	if len(events) == 0 || events[0] != "barrier.install" {
		t.Fatalf("events = %#v, want barrier.install installed before Core restart", events)
	}

	installed := env.barrier.lastInstallContext()
	if len(installed.ServerBypass) != 0 {
		t.Fatalf("installed barrier ServerBypass = %v, want block-only (no bypasses) when gateway unresolvable", installed.ServerBypass)
	}
	if !installed.blockOnly {
		t.Fatal("installed barrier blockOnly = false, want true (public IPv4/IPv6 blackholed) when gateway unresolvable")
	}
}

func TestDaemonShutdownCancelsQueuedRecoveryBeforeStart(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	process := env.runner.currentProcess()
	daemon, err := StartDaemon(context.Background(), DaemonOptions{
		SocketPath: filepath.Join(shortSocketDir(t), "guard.sock"),
		Handler:    NewLocalAPI(env.manager),
		OwnerUID:   uint32(os.Geteuid()),
		PeerCredentials: func(net.Conn) (uint32, bool) {
			return 0, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.manager.acquireMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		env.manager.handleUnexpectedExit(process, errors.New("Core exited"))
		close(done)
	}()
	eventually(t, func() bool { return env.manager.recoveryActiveCount() == 1 })
	if err := daemon.Close(); err != nil {
		t.Fatal(err)
	}
	env.manager.releaseMutation()
	<-done
	if got := env.runner.startCount(); got != 1 {
		t.Fatalf("queued recovery starts = %d, want original Core only", got)
	}
}

func TestManagerUnexpectedExitWaitsForMutationWithoutLosingRecoveryBudget(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.manager.restartTimeout = 25 * time.Millisecond
	process := env.runner.currentProcess()
	if err := env.manager.acquireMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			env.manager.releaseMutation()
		}
	}()

	done := make(chan struct{})
	go func() {
		env.manager.handleUnexpectedExit(process, errors.New("Core exited"))
		close(done)
	}()
	time.Sleep(3 * env.manager.restartTimeout)
	select {
	case <-done:
		t.Fatal("unexpected exit was dropped while lifecycle mutation was busy")
	default:
	}

	env.manager.releaseMutation()
	released = true
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queued unexpected exit did not recover after mutation completed")
	}
	if got := env.runner.startCount(); got != 2 {
		t.Fatalf("Core starts = %d, want recovery restart", got)
	}
}

func TestDaemonShutdownDrainsInFlightRecoveryBeforeReturning(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.runner.blockStartUntilContext = true
	socketPath := filepath.Join(shortSocketDir(t), "guard.sock")
	daemon, err := StartDaemon(context.Background(), DaemonOptions{
		SocketPath:      socketPath,
		Handler:         NewLocalAPI(env.manager),
		OwnerUID:        uint32(os.Geteuid()),
		PeerCredentials: func(net.Conn) (uint32, bool) { return 0, true },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer daemon.Close()

	env.runner.exit(env.runner.currentProcess().PID, errors.New("Core exited"))
	eventually(t, func() bool { return env.runner.startCount() == 2 })
	closeDone := make(chan error, 1)
	go func() { closeDone <- daemon.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not cancel and drain in-flight recovery")
	}
	starts := env.runner.startCount()
	time.Sleep(30 * time.Millisecond)
	if got := env.runner.startCount(); got != starts {
		t.Fatalf("Core started after daemon returned: before=%d after=%d", starts, got)
	}
}

func TestManagerIgnoresStaleExitForReusedPIDWithDifferentGeneration(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	exited := env.runner.currentProcess()
	if exited.Generation == "" {
		t.Fatal("test Core generation is empty")
	}
	if err := env.manager.acquireMutation(context.Background()); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(entered)
		env.manager.handleUnexpectedExit(exited, errors.New("Core A exited"))
		close(done)
	}()
	<-entered

	replacement := exited
	replacement.Generation = exited.Generation + ":reused"
	replacement.Exit = nil
	env.manager.current = replacement
	env.manager.runtime = healthyRuntime(replacement.PID)
	env.manager.setStatus(Status{
		SchemaVersion: 1,
		Desired:       DesiredOn,
		Phase:         PhaseCommitted,
		CorePID:       replacement.PID,
		CoreVersion:   version.Version,
		Protection:    ProtectionProtected,
	})
	env.events.reset()
	env.manager.releaseMutation()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stale exit handler did not finish")
	}
	if got := env.manager.current; got.PID != replacement.PID || got.Generation != replacement.Generation {
		t.Fatalf("stale exit replaced current Core: got %+v, want %+v", got, replacement)
	}
	if got := env.runner.startCount(); got != 1 {
		t.Fatalf("stale exit started another Core: starts=%d", got)
	}
	if got := env.events.snapshot(); len(got) != 0 {
		t.Fatalf("stale exit mutated lifecycle state: events=%#v", got)
	}
	if got := env.manager.Status(); got.Protection != ProtectionProtected || got.CorePID != replacement.PID {
		t.Fatalf("status after stale exit = %+v, want replacement Protected", got)
	}
}

func TestManagerUnexpectedExitDesiredReadFailureFailsClosed(t *testing.T) {
	tests := []struct {
		name           string
		barrierErr     error
		wantProtection string
	}{
		{name: "barrier retained", wantProtection: ProtectionBlocked},
		{name: "barrier install fails", barrierErr: errors.New("barrier unavailable"), wantProtection: ProtectionNeedsAttention},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newProtectedManagerTestEnv(t)
			process := env.runner.currentProcess()
			env.events.reset()
			env.store.setLoadError(errors.New("desired state unreadable"))
			env.barrier.installErr = tt.barrierErr

			env.runner.exit(process.PID, errors.New("unexpected exit"))
			eventually(t, func() bool {
				return env.manager.Status().LastError == "desired_state_read_failed"
			})

			if got := env.events.snapshot(); !reflect.DeepEqual(got, []string{"barrier.install"}) {
				t.Fatalf("events = %#v, want fail-closed barrier attempt", got)
			}
			if got := env.runner.startCount(); got != 1 {
				t.Fatalf("Core restarted without readable desired state: starts=%d", got)
			}
			if got := env.manager.Status(); got.Desired != DesiredOn || got.Phase != PhaseNeedsAttention || got.Protection != tt.wantProtection {
				t.Fatalf("status = %+v, want desired on needs_attention protection %q", got, tt.wantProtection)
			}
			if got := env.manager.current.PID; got != process.PID {
				t.Fatalf("current PID cleared after ambiguous desired state: got %d, want %d", got, process.PID)
			}
		})
	}
}

func TestManagerHealthyRecoveryReleasesHeldBarrierBeforeProtected(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Manager, context.Context) error
	}{
		{name: "Up", run: (*Manager).Up},
		{name: "Recover", run: (*Manager).Recover},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newProtectedManagerTestEnv(t)
			retainBarrierAfterDesiredReadFailure(t, env)
			env.store.setLoadError(nil)
			env.events.reset()

			healthPassed := false
			env.health.onSuccess = func() { healthPassed = true }
			env.barrier.onRemove = func() {
				if !healthPassed {
					t.Error("held barrier removal started before health passed")
				}
				if status := env.manager.Status(); status.Protection == ProtectionProtected {
					t.Errorf("status was Protected before held barrier removal: %+v", status)
				}
			}

			if err := tt.run(env.manager, context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := env.events.snapshot(); !reflect.DeepEqual(got, []string{"core.start", "barrier.release"}) {
				t.Fatalf("events = %#v, want health-gated barrier release", got)
			}
			if env.manager.barrierProven() {
				t.Fatal("barrier remains held after healthy recovery")
			}
			if got := env.manager.Status(); got.Protection != ProtectionProtected || got.Phase != PhaseCommitted {
				t.Fatalf("status = %+v, want Protected only after barrier removal", got)
			}
		})
	}
}

// TestManagerHealthyRecoveryReleasesHeldBarrierWhenGatewayProviderErrors is
// the regression guard for the "gratuitous new failure mode" review finding:
// releaseBarrierToCore/removeBarrier only ever act on the BarrierContext
// recorded at install time (m.barrierOwnership.context) — they never needed
// a freshly resolved gateway. Yet the release call sites used to resolve one
// via barrierContextForRuntime first and abort on any resolution error (e.g.
// a transient `route -n get default` failure), discarding the result and
// leaving Core Blocked instead of Protected for no reason. This proves
// release still succeeds — and the machine still ends up Protected — even
// when the gateway provider is broken at release time.
func TestManagerHealthyRecoveryReleasesHeldBarrierWhenGatewayProviderErrors(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	retainBarrierAfterDesiredReadFailure(t, env)
	env.store.setLoadError(nil)
	env.events.reset()

	// Break gateway resolution only now, after the barrier is already held —
	// a bypass-carrying release would have needed a lazily-resolved gateway
	// under the old (buggy) call sites.
	env.manager.barrierContext.Gateway = ""
	env.manager.gatewayProvider = &fakeGatewayProvider{err: errors.New("no default gateway")}

	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatalf("Up failed despite the gateway provider erroring on a release-only path: %v", err)
	}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, []string{"core.start", "barrier.release"}) {
		t.Fatalf("events = %#v, want health-gated barrier release despite broken gateway provider", got)
	}
	if env.manager.barrierProven() {
		t.Fatal("barrier remains held after healthy recovery")
	}
	if got := env.manager.Status(); got.Protection != ProtectionProtected || got.Phase != PhaseCommitted {
		t.Fatalf("status = %+v, want Protected even though the gateway provider errors", got)
	}
}

func TestManagerHeldBarrierRemainsWhenRecoveryHealthFails(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	retainBarrierAfterDesiredReadFailure(t, env)
	env.store.setLoadError(nil)
	env.health.err = errors.New("Core unhealthy")
	env.events.reset()

	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("Up succeeded despite recovery health failure")
	}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, []string{"core.start", "core.stop"}) {
		t.Fatalf("events = %#v, want no barrier removal before health", got)
	}
	if !env.manager.barrierProven() {
		t.Fatal("barrier released after recovery health failure")
	}
	if got := env.manager.Status(); got.Protection != ProtectionBlocked || got.Phase != PhaseNeedsAttention || got.LastError != "core_health_failed" {
		t.Fatalf("status = %+v, want blocked core_health_failed", got)
	}
}

func TestManagerHeldBarrierRemovalFailureDoesNotClaimProtected(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	retainBarrierAfterDesiredReadFailure(t, env)
	env.store.setLoadError(nil)
	env.barrier.removeErr = errors.New("barrier remove failed")
	env.events.reset()

	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("Up succeeded despite held barrier removal failure")
	}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, []string{"core.start", "barrier.release"}) {
		t.Fatalf("events = %#v, want attempted post-health barrier removal", got)
	}
	if env.manager.barrierProven() || env.manager.barrierOwnership.proof != barrierReleaseAttempted {
		t.Fatal("partial barrier release was not retained as attempted and unproven")
	}
	if got := env.manager.Status(); got.Protection != ProtectionNeedsAttention || got.Phase != PhaseNeedsAttention || got.LastError != "barrier_remove_failed" {
		t.Fatalf("status = %+v, want unproven barrier_remove_failed", got)
	}
}

func retainBarrierAfterDesiredReadFailure(t *testing.T, env *managerTestEnv) {
	t.Helper()
	process := env.runner.currentProcess()
	env.store.setLoadError(errors.New("desired state unreadable"))
	env.runner.exit(process.PID, errors.New("unexpected exit"))
	eventually(t, func() bool {
		return env.manager.Status().LastError == "desired_state_read_failed"
	})
	if !env.manager.barrierProven() {
		t.Fatal("fail-closed setup did not retain barrier")
	}
}

func TestManagerPlannedDownDoesNotRestartExitedCore(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.runner.exitOnStop = true
	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if got := env.runner.startCount(); got != 1 {
		t.Fatalf("planned stop restarted Core: starts=%d", got)
	}
}

type dnsActivationFailureCase struct {
	name      string
	wantCode  string
	configure func(*fakeDNSManager)
}

func dnsActivationFailureCases() []dnsActivationFailureCase {
	return []dnsActivationFailureCase{
		{
			name:     "takeover",
			wantCode: "dns_takeover_failed",
			configure: func(dns *fakeDNSManager) {
				dns.ensureResults = []fakeDNSResult{{
					status: DNSStatus{State: DNSUnknown, Service: "Wi-Fi"},
					err:    errors.New("resolver change failed"),
				}}
			},
		},
		{
			name:     "verification",
			wantCode: "dns_verification_failed",
			configure: func(dns *fakeDNSManager) {
				dns.inspectResults = []fakeDNSResult{{
					status: DNSStatus{State: DNSUnknown, Service: "Wi-Fi"},
					err:    errors.New("resolver inspection failed"),
				}}
			},
		},
	}
}

type managerTestEnv struct {
	manager *Manager
	store   *recordingDesiredStore
	runner  *fakeCoreRunner
	health  *fakeHealthGate
	barrier *fakeBarrier
	dns     *fakeDNSManager
	legacy  *fakeLegacyCore
	events  *eventLog
}

func newManagerTestEnv(t *testing.T) *managerTestEnv {
	t.Helper()
	events := &eventLog{}
	store := &recordingDesiredStore{Store: OpenStore(Paths{
		Desired:     filepath.Join(t.TempDir(), "guardian-state.json"),
		Transaction: filepath.Join(t.TempDir(), "transaction.json"),
		Receipt:     filepath.Join(t.TempDir(), "receipt.json"),
		Staging:     filepath.Join(t.TempDir(), "staging"),
		Snapshots:   filepath.Join(t.TempDir(), "snapshots"),
	}), events: events}
	runner := newFakeCoreRunner(events)
	health := &fakeHealthGate{}
	barrier := &fakeBarrier{events: events}
	dns := newFakeDNSManager(events)
	legacy := &fakeLegacyCore{events: events}
	manager, err := NewManager(ManagerOptions{
		Store:          store,
		Runner:         runner,
		Health:         health,
		Barrier:        barrier,
		DNS:            dns,
		Legacy:         legacy,
		BarrierContext: BarrierContext{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}, BlockIPv6: true},
		CoreVersion:    version.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &managerTestEnv{manager: manager, store: store, runner: runner, health: health, barrier: barrier, dns: dns, legacy: legacy, events: events}
}

func newProtectedManagerTestEnv(t *testing.T) *managerTestEnv {
	t.Helper()
	env := newManagerTestEnv(t)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	env.events.reset()
	return env
}

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func (l *eventLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = nil
}

type recordingDesiredStore struct {
	*Store
	events  *eventLog
	mu      sync.Mutex
	loadErr error
}

func (s *recordingDesiredStore) LoadDesired() (DesiredState, error) {
	s.mu.Lock()
	err := s.loadErr
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return s.Store.LoadDesired()
}

func (s *recordingDesiredStore) setLoadError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadErr = err
}

func (s *recordingDesiredStore) SaveDesired(desired DesiredState) error {
	if err := s.Store.SaveDesired(desired); err != nil {
		return err
	}
	s.events.add("desired." + string(desired))
	return nil
}

type fakeCoreRunner struct {
	mu           sync.Mutex
	events       *eventLog
	existing     Process
	existingErr  error
	current      Process
	exits        map[int]chan error
	nextPID      int
	starts       int
	signals      int
	startErr     error
	stopErr      error
	verifyErr    error
	blockStart   chan struct{}
	startEntered chan struct{}
	exitOnStop   bool

	blockStartUntilContext bool
	startEntryErrors       []error
	startDeadlines         []time.Time
	watchExit              chan error
	watches                int
	stopContextErr         error
	stopDeadline           time.Time

	executable string
}

func newFakeCoreRunner(events *eventLog) *fakeCoreRunner {
	return &fakeCoreRunner{events: events, exits: make(map[int]chan error), nextPID: 100, startEntered: make(chan struct{}, 1)}
}

func (r *fakeCoreRunner) Existing(context.Context) (Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.existing, r.existingErr
}

func (r *fakeCoreRunner) Verify(process Process) error {
	if r.verifyErr != nil {
		return r.verifyErr
	}
	if process.UID != 0 || process.Executable != install.BinPath {
		return fmt.Errorf("unverifiable Core process")
	}
	return nil
}

func (r *fakeCoreRunner) Watch(process Process) Process {
	if process.Exit != nil {
		return process
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.watches++
	process.Exit = r.watchExit
	return process
}

func (r *fakeCoreRunner) Start(ctx context.Context, _ CoreStartOptions) (Process, error) {
	r.events.add("core.start")
	r.mu.Lock()
	r.starts++
	startErr := r.startErr
	block := r.blockStart
	blockUntilContext := r.blockStartUntilContext
	r.startEntryErrors = append(r.startEntryErrors, ctx.Err())
	deadline, _ := ctx.Deadline()
	r.startDeadlines = append(r.startDeadlines, deadline)
	select {
	case r.startEntered <- struct{}{}:
	default:
	}
	if startErr != nil {
		r.mu.Unlock()
		return Process{}, startErr
	}
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		return Process{}, err
	}
	if blockUntilContext {
		r.mu.Unlock()
		<-ctx.Done()
		return Process{}, ctx.Err()
	}
	r.nextPID++
	exit := make(chan error, 1)
	process := Process{
		PID:        r.nextPID,
		Executable: install.BinPath,
		UID:        0,
		Generation: fmt.Sprintf("fake:%d", r.starts),
		Exit:       exit,
	}
	r.current = process
	r.exits[process.PID] = exit
	r.mu.Unlock()
	if block != nil {
		<-block
	}
	return process, nil
}

func (r *fakeCoreRunner) Stop(ctx context.Context, process Process) error {
	r.events.add("core.stop")
	r.mu.Lock()
	r.signals++
	r.stopContextErr = ctx.Err()
	r.stopDeadline, _ = ctx.Deadline()
	err := r.stopErr
	exitOnStop := r.exitOnStop
	exit := r.exits[process.PID]
	r.mu.Unlock()
	if exitOnStop && exit != nil {
		select {
		case exit <- nil:
		default:
		}
	}
	return err
}

func (r *fakeCoreRunner) stopEntryContextError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopContextErr
}

func (r *fakeCoreRunner) stopEntryDeadline() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopDeadline
}

func (r *fakeCoreRunner) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

func (r *fakeCoreRunner) startEntryContextErrors() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.startEntryErrors...)
}

func (r *fakeCoreRunner) startDeadlinesSnapshot() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.startDeadlines...)
}

func (r *fakeCoreRunner) watchCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.watches
}

func (r *fakeCoreRunner) exitWatched(err error) {
	r.mu.Lock()
	exit := r.watchExit
	r.mu.Unlock()
	if exit == nil {
		return
	}
	select {
	case exit <- err:
	default:
	}
}

func (r *fakeCoreRunner) signalCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.signals
}

func (r *fakeCoreRunner) currentProcess() Process {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

func (r *fakeCoreRunner) exit(pid int, err error) {
	r.mu.Lock()
	exit := r.exits[pid]
	r.mu.Unlock()
	if exit != nil {
		exit <- err
	}
}

func (r *fakeCoreRunner) Executable() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.executable
}

func (r *fakeCoreRunner) SetExecutable(executable string) error {
	if !filepath.IsAbs(executable) {
		return fmt.Errorf("executable must be absolute")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executable = executable
	return nil
}

type fakeHealthGate struct {
	mu             sync.Mutex
	runtime        supervisor.RuntimeState
	err            error
	last           HealthTarget
	onSuccess      func()
	onWait         func()
	waitForContext bool
	deadline       time.Time
}

func (h *fakeHealthGate) Wait(ctx context.Context, target HealthTarget) (supervisor.RuntimeState, error) {
	h.mu.Lock()
	waitForContext := h.waitForContext
	defer h.mu.Unlock()
	h.last = target
	h.deadline, _ = ctx.Deadline()
	if h.onWait != nil {
		h.onWait()
	}
	if waitForContext {
		<-ctx.Done()
		return supervisor.RuntimeState{}, ctx.Err()
	}
	if h.err != nil {
		return supervisor.RuntimeState{}, h.err
	}
	state := h.runtime
	if state.PID == 0 {
		state = healthyRuntime(target.PID)
	}
	if h.onSuccess != nil {
		h.onSuccess()
	}
	return state, nil
}

func (h *fakeHealthGate) lastContextDeadline() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.deadline
}

func healthyRuntime(pid int) supervisor.RuntimeState {
	return supervisor.RuntimeState{
		Version: version.Version, PID: pid, TunName: "utun7", SocksAddr: "127.0.0.1:43210",
		ServerBypass: []string{"198.51.100.10/32"}, TunnelHealthy: true, DNSListening: true, RoutesInstalled: true,
	}
}

type fakeBarrier struct {
	events            *eventLog
	installErr        error
	reassertErr       error
	removeErr         error
	failIfContextDone bool
	onRemove          func()
	mu                sync.Mutex
	installContext    BarrierContext
	installContextErr error
	installDeadline   time.Time
	releaseContext    BarrierContext
	transferred       []string
}

func (b *fakeBarrier) Install(ctx context.Context, barrierContext BarrierContext) error {
	b.events.add("barrier.install")
	b.mu.Lock()
	b.installContext = cloneBarrierContext(barrierContext)
	b.installContextErr = ctx.Err()
	b.installDeadline, _ = ctx.Deadline()
	failIfContextDone := b.failIfContextDone
	b.mu.Unlock()
	if failIfContextDone && ctx.Err() != nil {
		return ctx.Err()
	}
	return b.installErr
}

func (b *fakeBarrier) ReassertBypass(context.Context, BarrierContext) error {
	b.events.add("barrier.reassert")
	return b.reassertErr
}

func (b *fakeBarrier) Release(_ context.Context, barrierContext BarrierContext, transferred []string) error {
	b.events.add("barrier.release")
	b.mu.Lock()
	b.releaseContext = cloneBarrierContext(barrierContext)
	b.transferred = append([]string(nil), transferred...)
	b.mu.Unlock()
	if b.onRemove != nil {
		b.onRemove()
	}
	return b.removeErr
}

func (b *fakeBarrier) lastRelease() (BarrierContext, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneBarrierContext(b.releaseContext), append([]string(nil), b.transferred...)
}

func (b *fakeBarrier) Remove(context.Context, BarrierContext) error {
	b.events.add("barrier.remove")
	if b.onRemove != nil {
		b.onRemove()
	}
	return b.removeErr
}

func (b *fakeBarrier) lastInstallContext() BarrierContext {
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneBarrierContext(b.installContext)
}

func (b *fakeBarrier) lastInstallCallContext() (error, time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.installContextErr, b.installDeadline
}

type fakeLegacyCore struct {
	events      *eventLog
	present     bool
	presentErr  error
	stopErr     error
	removeErr   error
	stopCount   int
	removeCount int
}

func (l *fakeLegacyCore) Present(context.Context) (bool, error) {
	return l.present, l.presentErr
}

func (l *fakeLegacyCore) Stop(context.Context) error {
	l.stopCount++
	l.events.add("legacy.stop")
	return l.stopErr
}

func (l *fakeLegacyCore) Remove() error {
	l.removeCount++
	l.events.add("legacy.remove")
	return l.removeErr
}

type fakeDNSResult struct {
	status DNSStatus
	err    error
}

type fakeDNSManager struct {
	events         *eventLog
	record         bool
	ensureErr      error
	inspectErr     error
	restoreErr     error
	ensureFunc     func(context.Context) (DNSStatus, error)
	inspectFunc    func(context.Context) (DNSStatus, error)
	ensureResults  []fakeDNSResult
	inspectResults []fakeDNSResult
	restoreResults []fakeDNSResult
	waitForContext bool
	mu             sync.Mutex
	contextErr     error
}

func newFakeDNSManager(events *eventLog) *fakeDNSManager {
	return &fakeDNSManager{events: events}
}

func (d *fakeDNSManager) EnsureManaged(ctx context.Context) (DNSStatus, error) {
	d.mu.Lock()
	d.recordEvent("dns.ensure")
	ensureFunc := d.ensureFunc
	if ensureFunc == nil {
		status, err := d.pop(&d.ensureResults, DNSStatus{State: DNSManaged, Service: "Wi-Fi"}, d.ensureErr)
		d.mu.Unlock()
		return status, err
	}
	d.mu.Unlock()
	return ensureFunc(ctx)
}

func (d *fakeDNSManager) Inspect(ctx context.Context) (DNSStatus, error) {
	d.mu.Lock()
	d.recordEvent("dns.inspect")
	inspectFunc := d.inspectFunc
	if inspectFunc == nil {
		status, err := d.pop(&d.inspectResults, DNSStatus{State: DNSManaged, Service: "Wi-Fi"}, d.inspectErr)
		d.mu.Unlock()
		return status, err
	}
	d.mu.Unlock()
	return inspectFunc(ctx)
}

func (d *fakeDNSManager) Restore(ctx context.Context) (DNSStatus, error) {
	d.mu.Lock()
	d.recordEvent("dns.restore")
	waitForContext := d.waitForContext
	d.mu.Unlock()
	if waitForContext {
		<-ctx.Done()
		d.mu.Lock()
		d.contextErr = ctx.Err()
		d.mu.Unlock()
		return DNSStatus{State: DNSUnknown, Service: "Wi-Fi"}, ctx.Err()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pop(&d.restoreResults, DNSStatus{State: DNSUnmanaged, Service: "Wi-Fi"}, d.restoreErr)
}

func (d *fakeDNSManager) recordEvent(event string) {
	if d.record {
		d.events.add(event)
	}
}

func (d *fakeDNSManager) pop(results *[]fakeDNSResult, fallback DNSStatus, fallbackErr error) (DNSStatus, error) {
	if len(*results) == 0 {
		return fallback, fallbackErr
	}
	result := (*results)[0]
	*results = (*results)[1:]
	return result.status, result.err
}

func (d *fakeDNSManager) lastContextError() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.contextErr
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}

// 事故主用例:另一条 mutation 占着锁时 bx up 排队超时。该路径从不走
// needsAttention,此前 500 响应因此不带任何码——「Guardian 正忙」正是用户
// 反复 sudo bx up 时最常撞上的形态,必须有一个属于本次失败的真实名字。
func TestManagerBusyMutationIsNamedGuardianBusy(t *testing.T) {
	env := newManagerTestEnv(t)
	env.runner.blockStart = make(chan struct{})
	firstDone := make(chan error, 1)
	go func() { firstDone <- env.manager.Up(context.Background()) }()
	select {
	case <-env.runner.startEntered:
	case <-time.After(time.Second):
		t.Fatal("Up did not enter Core start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() { secondDone <- env.manager.Down(ctx) }()
	<-ctx.Done()
	close(env.runner.blockStart)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	err := <-secondDone
	if !errors.Is(err, errMutationBusy) {
		t.Fatalf("排队超时的 Down err = %v, want errMutationBusy", err)
	}
	// 既有调用方按 ctx 错误分支处理,包装不得破坏它。
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("排队超时的 Down err = %v, 仍须可被 errors.Is(ctx err) 识别", err)
	}
	if got := env.manager.Status().LastError; got != "" {
		t.Errorf("busy 短路不应改写 LastError,实际 = %q", got)
	}
}

// 「启动恢复未完成」与「Guardian 正忙」是本次失败的真实描述,不是从别处
// 抄来的陈旧码;它们由错误本身识别,不经 needsAttention,故也不会污染
// status.LastError(那是长期状态,不该被一次排队超时改写)。
func TestFailureCodeForErrorNamesTheRealFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"recoveryBlocked 短路", errRecoveryIncomplete, "recovery_incomplete"},
		{"包装后的 recoveryBlocked", fmt.Errorf("up: %w", errRecoveryIncomplete), "recovery_incomplete"},
		{"acquireMutation 排队超时", fmt.Errorf("%w: %w", errMutationBusy, context.DeadlineExceeded), "guardian_busy"},
		{"其它错误不硬凑码", errors.New("start installed Core: boom"), ""},
		{"裸 ctx 错误不等于 busy", context.DeadlineExceeded, ""},
		{"nil", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := failureCodeForError(tt.err); got != tt.want {
				t.Fatalf("failureCodeForError(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// 事故的完整形态,走真实 ExecCoreRunner 的 Manager 级回归:陈旧 core-process.json
// 指向早已死亡的 PID 且清除失败时,sudo bx up 必须成功。此前 Existing() 一跳
// 被放行、Start() 立刻以「durable launch marker already exists」拒绝,
// 用户可见行为(core_ownership_uncertain)与修复前完全一致——只测 Existing()
// 一跳的单测是假绿。
func TestManagerUpStartsCoreDespiteUnremovableDeadCoreRecord(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "core-process.json")
	if err := saveProcessRecord(statePath, processRecord{PID: 5129, Executable: executable, Generation: "darwin:1785895536:393862", State: processRecordOwned}); err != nil {
		t.Fatal(err)
	}
	started := newStartTestProcess(6001)
	defer started.release()
	operations := &pidAwareProcessOperations{
		dead:    map[int]bool{5129: true},
		live:    map[int]Process{6001: {PID: 6001, Executable: executable, UID: 0, Generation: "darwin:1785999999:1"}},
		started: started,
	}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = statePath
	runner.ControlSocket = filepath.Join(dir, "bx.sock")
	runner.Operations = operations
	runner.RemoveProcessRecord = func(string) error { return errors.New("remove record: permission denied") }

	events := &eventLog{}
	manager, err := NewManager(ManagerOptions{
		Store: OpenStore(Paths{
			Desired:     filepath.Join(dir, "guardian-state.json"),
			Transaction: filepath.Join(dir, "transaction.json"),
			Receipt:     filepath.Join(dir, "receipt.json"),
			Staging:     filepath.Join(dir, "staging"),
			Snapshots:   filepath.Join(dir, "snapshots"),
		}),
		Runner:         runner,
		Health:         &fakeHealthGate{},
		Barrier:        &fakeBarrier{events: events},
		DNS:            newFakeDNSManager(events),
		BarrierContext: BarrierContext{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}, BlockIPv6: true},
		CoreVersion:    version.Version,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := manager.Up(context.Background()); err != nil {
		t.Fatalf("陈旧死记录仍卡死 bx up: %v", err)
	}
	if got := manager.Status().LastError; got != "" {
		t.Errorf("Up 成功时不应留失败码,实际 = %q", got)
	}
	if operations.startCount() != 1 {
		t.Errorf("Core 启动次数 = %d, want 1", operations.startCount())
	}
}

// 同一事故形态的另一个分支,Manager 级回归:孤儿 launching 标记(PID=0,Guardian
// 崩在"写完标记、还没保存 owned 记录"之间)也不该让 bx up 永久失败——这正是用
// 户 2026-08-05 实际踩到的形态。清不掉标记文件也一样不该卡死。只测 Existing()
// 一跳的单测是假绿(上一轮的教训),所以这里同样走真实 ExecCoreRunner 的 Manager
// 级 Up()。
func TestManagerUpStartsCoreDespiteOrphanLaunchMarker(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "core-process.json")
	if err := saveProcessRecord(statePath, processRecord{State: processRecordLaunching}); err != nil {
		t.Fatal(err)
	}
	started := newStartTestProcess(6001)
	defer started.release()
	operations := &pidAwareProcessOperations{
		live:    map[int]Process{6001: {PID: 6001, Executable: executable, UID: 0, Generation: "darwin:1785999999:1"}},
		started: started,
	}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = statePath
	runner.ControlSocket = filepath.Join(dir, "bx.sock")
	runner.Operations = operations
	runner.RemoveProcessRecord = func(string) error { return errors.New("remove record: permission denied") }

	events := &eventLog{}
	manager, err := NewManager(ManagerOptions{
		Store: OpenStore(Paths{
			Desired:     filepath.Join(dir, "guardian-state.json"),
			Transaction: filepath.Join(dir, "transaction.json"),
			Receipt:     filepath.Join(dir, "receipt.json"),
			Staging:     filepath.Join(dir, "staging"),
			Snapshots:   filepath.Join(dir, "snapshots"),
		}),
		Runner:         runner,
		Health:         &fakeHealthGate{},
		Barrier:        &fakeBarrier{events: events},
		DNS:            newFakeDNSManager(events),
		BarrierContext: BarrierContext{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}, BlockIPv6: true},
		CoreVersion:    version.Version,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := manager.Up(context.Background()); err != nil {
		t.Fatalf("孤儿 launching 标记仍卡死 bx up: %v", err)
	}
	if got := manager.Status().LastError; got != "" {
		t.Errorf("Up 成功时不应留失败码,实际 = %q", got)
	}
	if operations.startCount() != 1 {
		t.Errorf("Core 启动次数 = %d, want 1", operations.startCount())
	}
}
