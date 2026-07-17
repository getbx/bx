package guardian

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestCoreArgsUsesArgumentVector(t *testing.T) {
	got := coreArgs("/etc/bx/config.yaml", "127.0.0.1:53")
	want := []string{"run", "-c", "/etc/bx/config.yaml", "--listen-dns", "127.0.0.1:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("coreArgs() = %#v, want %#v", got, want)
	}
	got[0] = "changed"
	if next := coreArgs("/etc/bx/config.yaml", "127.0.0.1:53"); next[0] != "run" {
		t.Fatalf("coreArgs returned shared mutable storage: %#v", next)
	}
}

func TestProcessRecordRoundTripUsesRootOnlyMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core-process.json")
	want := processRecord{PID: 42, Executable: "/usr/local/bin/bx"}
	if err := saveProcessRecord(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadProcessRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("record = %+v, want %+v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %#o, want 0600", got)
	}
}

func TestVerifyInstalledProcessRequiresRootAndInstalledInode(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "bx")
	alias := filepath.Join(dir, "running-bx")
	if err := os.WriteFile(installed, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(installed, alias); err != nil {
		t.Fatal(err)
	}
	if err := verifyInstalledProcess(Process{PID: 42, Executable: alias, UID: 0}, installed); err != nil {
		t.Fatalf("same installed inode rejected: %v", err)
	}
	if err := verifyInstalledProcess(Process{PID: 42, Executable: alias, UID: 501}, installed); err == nil {
		t.Fatal("non-root process accepted")
	}
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyInstalledProcess(Process{PID: 42, Executable: other, UID: 0}, installed); err == nil {
		t.Fatal("different executable inode accepted")
	}
}

func TestExecCoreRunnerAdoptedWatcherOutlivesInspectionContext(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	identity, err := statExecutableIdentity(executable)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "core-process.json")
	if err := saveProcessRecord(statePath, processRecord{PID: 42, Executable: executable, Device: identity.device, Inode: identity.inode}); err != nil {
		t.Fatal(err)
	}
	operations := &watchTestProcessOperations{process: Process{PID: 42, Executable: executable, UID: 0}, alive: true}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = statePath
	runner.Operations = operations
	runner.InspectInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	process, err := runner.Existing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(20 * time.Millisecond)
	operations.setAlive(false)
	select {
	case <-process.Exit:
	case <-time.After(600 * time.Millisecond):
		t.Fatal("adopted Core exit was not observed after inspection context ended")
	}
}

func TestExecCoreRunnerExistingInspectionFailureRetainsRecord(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	operations.setInspectError(errors.New("inspect permission denied"))

	if _, err := runner.Existing(context.Background()); err == nil {
		t.Fatal("Existing succeeded despite ambiguous inspection")
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("process record removed after inspection failure: %v", err)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("inspection failure signalled PID %d times", got)
	}
}

func TestExecCoreRunnerExistingDefinitiveExitRemovesRecord(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	operations.setAlive(false)

	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if process.PID != 0 {
		t.Fatalf("Existing returned process %+v after definitive exit", process)
	}
	if _, err := os.Stat(runner.StatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("process record error = %v, want removed", err)
	}
}

func TestExecCoreRunnerAdoptedWatcherIgnoresInspectionFailure(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	operations.setInspectError(errors.New("transient sysctl failure"))
	time.Sleep(4 * runner.InspectInterval)
	select {
	case err := <-process.Exit:
		t.Fatalf("watcher ended on ambiguous inspection: %v", err)
	default:
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("watcher removed process record after transient error: %v", err)
	}

	operations.setInspectError(nil)
	operations.setAlive(false)
	select {
	case <-process.Exit:
	case <-time.After(time.Second):
		t.Fatal("watcher did not report definitive Core exit")
	}
}

func TestExecCoreRunnerStopUsesCooperativeShutdownWithoutSignal(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	shutdownCalls := 0
	runner.ShutdownCore = func(_ context.Context, socketPath string, expectedPID int) error {
		shutdownCalls++
		if socketPath != runner.ControlSocket || expectedPID != process.PID {
			t.Fatalf("shutdown request = (%q, %d), want (%q, %d)", socketPath, expectedPID, runner.ControlSocket, process.PID)
		}
		operations.setAlive(false)
		return nil
	}

	if err := runner.Stop(context.Background(), process); err != nil {
		t.Fatal(err)
	}
	if shutdownCalls != 1 {
		t.Fatalf("cooperative shutdown calls = %d, want 1", shutdownCalls)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("cooperative stop invoked legacy signal seam %d times", got)
	}
	if _, err := os.Stat(runner.StatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("process record error = %v, want removed", err)
	}
}

func TestExecCoreRunnerStopLegacyCoreFailsClosedWithoutSignal(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	runner.ShutdownCore = func(context.Context, string, int) error {
		return errors.New("shutdown endpoint returned 404")
	}

	if err := runner.Stop(context.Background(), process); err == nil {
		t.Fatal("legacy Core without shutdown endpoint was treated as stopped")
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("legacy Core invoked signal seam %d times", got)
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("legacy Core process record removed: %v", err)
	}
}

func TestExecCoreRunnerStopAmbiguousInspectionFailsClosedWithoutSignal(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	runner.ShutdownCore = func(context.Context, string, int) error {
		operations.setInspectError(errors.New("inspect resource exhausted"))
		return nil
	}

	if err := runner.Stop(context.Background(), process); err == nil {
		t.Fatal("Stop succeeded on ambiguous post-shutdown inspection")
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("ambiguous identity invoked signal seam %d times", got)
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("ambiguous process record removed: %v", err)
	}
}

func TestExecCoreRunnerStopWaitsForRecordedIdentityToDisappear(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	otherExecutable := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(otherExecutable, []byte("other"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner.ShutdownCore = func(context.Context, string, int) error {
		operations.setProcess(Process{PID: process.PID, Executable: otherExecutable, UID: 501})
		return nil
	}

	if err := runner.Stop(context.Background(), process); err != nil {
		t.Fatal(err)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("reused PID invoked signal seam %d times", got)
	}
}

func newRecordedProcessRunner(t *testing.T) (*ExecCoreRunner, Process, *watchTestProcessOperations) {
	t.Helper()
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	identity, err := statExecutableIdentity(executable)
	if err != nil {
		t.Fatal(err)
	}
	process := Process{PID: 42, Executable: executable, UID: 0, identity: identity}
	statePath := filepath.Join(dir, "core-process.json")
	if err := saveProcessRecord(statePath, processRecord{PID: process.PID, Executable: executable, Device: identity.device, Inode: identity.inode}); err != nil {
		t.Fatal(err)
	}
	operations := &watchTestProcessOperations{process: process, alive: true}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = statePath
	runner.ControlSocket = filepath.Join(dir, "bx.sock")
	runner.InspectInterval = 5 * time.Millisecond
	runner.StopTimeout = 100 * time.Millisecond
	runner.Operations = operations
	return runner, process, operations
}

type watchTestProcessOperations struct {
	mu         sync.Mutex
	process    Process
	alive      bool
	inspectErr error
	signals    int
}

func (*watchTestProcessOperations) Start(string, []string) (StartedProcess, error) {
	return nil, errors.New("unexpected start")
}

func (o *watchTestProcessOperations) Inspect(int) (Process, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.inspectErr != nil {
		return Process{}, o.inspectErr
	}
	if !o.alive {
		return Process{}, ErrProcessNotRunning
	}
	return o.process, nil
}

func (o *watchTestProcessOperations) Signal(int, os.Signal) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.signals++
	return errors.New("unexpected signal")
}

func (o *watchTestProcessOperations) setAlive(alive bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.alive = alive
}

func (o *watchTestProcessOperations) setInspectError(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.inspectErr = err
}

func (o *watchTestProcessOperations) setProcess(process Process) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.process = process
}

func (o *watchTestProcessOperations) signalCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.signals
}
