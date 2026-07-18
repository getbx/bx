package guardian

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
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

func TestManagerDownTransitionsBehindBarrier(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"barrier.install", "core.stop", "network.restore", "desired.off", "barrier.remove"}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if got := env.manager.Status(); got.Desired != DesiredOff || got.Protection != ProtectionOff {
		t.Fatalf("status = %+v, want off", got)
	}
	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("repeated Down() error = %v", err)
	}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("repeated Down changed events = %#v", got)
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
	if !env.manager.barrierHeld {
		t.Fatal("ambiguous partial barrier was not retained fail closed")
	}
	if got := env.manager.Status(); got.Protection != ProtectionBlocked || got.LastError != "barrier_install_failed" {
		t.Fatalf("migration status = %+v, want blocked barrier_install_failed", got)
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
	if !env.manager.barrierHeld {
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
	env.manager.barrierHeld = true
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

	reconstructed, err := NewManager(ManagerOptions{
		Store: env.store, Runner: newRunner(), Health: env.health, Barrier: env.barrier, Restorer: env.restorer,
		BarrierContext: env.manager.barrierContext, CoreVersion: version.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconstructed.Up(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Fatalf("reconstructed Up error = %v, want uncertain ownership", err)
	}
	if got := operations.startCount(); got != 1 {
		t.Fatalf("duplicate starts = %d, want 1", got)
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
	env.restorer.err = errors.New("dns restore failed")
	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("Down succeeded despite restoration failure")
	}
	want := []string{"barrier.install", "core.stop", "network.restore", "core.start", "barrier.release"}
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

func TestManagerDownRestoreTimeoutUsesReservedRecoveryBudget(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.manager.restartTimeout = 40 * time.Millisecond
	env.restorer.waitForContext = true
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if err := env.manager.Down(ctx); err == nil {
		t.Fatal("Down succeeded despite restore timeout")
	}
	if err := env.restorer.lastContextError(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("restore context error = %v, want deadline exceeded", err)
	}
	if got := env.runner.startCount(); got != 2 {
		t.Fatalf("restore timeout did not attempt protected recovery: starts=%d", got)
	}
	startErrs := env.runner.startEntryContextErrors()
	if len(startErrs) != 2 || startErrs[1] != nil {
		t.Fatalf("recovery start context errors = %#v, want live second context", startErrs)
	}
	want := []string{"barrier.install", "core.stop", "network.restore", "core.start", "barrier.release"}
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
	env.restorer.waitForContext = true
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
	if !env.manager.barrierHeld {
		t.Fatal("barrier released after bounded recovery failure")
	}
	if got := env.manager.Status(); got.Protection != ProtectionBlocked || got.Phase != PhaseNeedsAttention {
		t.Fatalf("status = %+v, want blocked needs_attention", got)
	}
}

func TestManagerDownDoubleFailureKeepsBarrierAndNeedsAttention(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.restorer.err = errors.New("dns restore failed")
	env.runner.startErr = errors.New("Core restart failed")
	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("Down succeeded despite restoration and recovery failures")
	}
	want := []string{"barrier.install", "core.stop", "network.restore", "core.start"}
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
			if env.manager.barrierHeld {
				t.Fatal("barrier remains held after healthy recovery")
			}
			if got := env.manager.Status(); got.Protection != ProtectionProtected || got.Phase != PhaseCommitted {
				t.Fatalf("status = %+v, want Protected only after barrier removal", got)
			}
		})
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
	if !env.manager.barrierHeld {
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
	if !env.manager.barrierHeld {
		t.Fatal("barrier state cleared after removal failure")
	}
	if got := env.manager.Status(); got.Protection != ProtectionBlocked || got.Phase != PhaseNeedsAttention || got.LastError != "barrier_remove_failed" {
		t.Fatalf("status = %+v, want blocked barrier_remove_failed", got)
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
	if !env.manager.barrierHeld {
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

type managerTestEnv struct {
	manager  *Manager
	store    *recordingDesiredStore
	runner   *fakeCoreRunner
	health   *fakeHealthGate
	barrier  *fakeBarrier
	restorer *fakeNetworkRestorer
	legacy   *fakeLegacyCore
	events   *eventLog
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
	restorer := &fakeNetworkRestorer{events: events}
	legacy := &fakeLegacyCore{events: events}
	manager, err := NewManager(ManagerOptions{
		Store:          store,
		Runner:         runner,
		Health:         health,
		Barrier:        barrier,
		Restorer:       restorer,
		Legacy:         legacy,
		BarrierContext: BarrierContext{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}, BlockIPv6: true},
		CoreVersion:    version.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &managerTestEnv{manager: manager, store: store, runner: runner, health: health, barrier: barrier, restorer: restorer, legacy: legacy, events: events}
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

func (r *fakeCoreRunner) Start(ctx context.Context) (Process, error) {
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
	events         *eventLog
	installErr     error
	reassertErr    error
	removeErr      error
	onRemove       func()
	mu             sync.Mutex
	installContext BarrierContext
}

func (b *fakeBarrier) Install(_ context.Context, barrierContext BarrierContext) error {
	b.events.add("barrier.install")
	b.mu.Lock()
	b.installContext = cloneBarrierContext(barrierContext)
	b.mu.Unlock()
	return b.installErr
}

func (b *fakeBarrier) ReassertBypass(context.Context, BarrierContext) error {
	b.events.add("barrier.reassert")
	return b.reassertErr
}

func (b *fakeBarrier) Release(context.Context, BarrierContext) error {
	b.events.add("barrier.release")
	if b.onRemove != nil {
		b.onRemove()
	}
	return b.removeErr
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

type fakeNetworkRestorer struct {
	events         *eventLog
	err            error
	waitForContext bool
	mu             sync.Mutex
	contextErr     error
}

func (r *fakeNetworkRestorer) Restore(ctx context.Context) error {
	r.events.add("network.restore")
	if r.waitForContext {
		<-ctx.Done()
		r.mu.Lock()
		r.contextErr = ctx.Err()
		r.mu.Unlock()
		return ctx.Err()
	}
	return r.err
}

func (r *fakeNetworkRestorer) lastContextError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.contextErr
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
