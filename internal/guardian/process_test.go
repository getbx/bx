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
	want := processRecord{PID: 42, Executable: "/usr/local/bin/bx", Generation: "darwin:123:456"}
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

func TestVerifyInstalledProcessAllowsAtomicExecutableReplacement(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "bx")
	if err := os.WriteFile(installed, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	process := Process{PID: 42, Executable: installed, UID: 0, Generation: "darwin:123:456"}
	replaceExecutableAtomically(t, installed)
	if err := verifyInstalledProcess(process, installed); err != nil {
		t.Fatalf("atomic executable replacement rejected live generation: %v", err)
	}
}

func TestVerifyInstalledProcessRequiresRootPathAndGeneration(t *testing.T) {
	installed := filepath.Join(t.TempDir(), "bx")
	valid := Process{PID: 42, Executable: installed, UID: 0, Generation: "darwin:123:456"}
	if err := verifyInstalledProcess(valid, installed); err != nil {
		t.Fatalf("valid process rejected: %v", err)
	}
	if err := verifyInstalledProcess(Process{PID: 42, Executable: installed, UID: 501, Generation: valid.Generation}, installed); err == nil {
		t.Fatal("non-root process accepted")
	}
	if err := verifyInstalledProcess(Process{PID: 42, Executable: "/tmp/not-bx", UID: 0, Generation: valid.Generation}, installed); err == nil {
		t.Fatal("different executable path accepted")
	}
	if err := verifyInstalledProcess(Process{PID: 42, Executable: installed, UID: 0}, installed); err == nil {
		t.Fatal("missing process generation accepted")
	}
}

func TestExecCoreRunnerStartPersistsInspectedGeneration(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := newStartTestProcess(52)
	t.Cleanup(started.release)
	operations := &startTestProcessOperations{
		started: started,
		process: Process{PID: 52, Executable: executable, UID: 0, Generation: "darwin:123:456"},
	}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.Operations = operations

	process, err := runner.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if process.Generation != operations.process.Generation {
		t.Fatalf("started generation = %q, want %q", process.Generation, operations.process.Generation)
	}
	record, err := loadProcessRecord(runner.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if record.Generation != operations.process.Generation {
		t.Fatalf("recorded generation = %q, want %q", record.Generation, operations.process.Generation)
	}
	if started.terminationCount() != 0 {
		t.Fatal("healthy started child was terminated")
	}
}

func TestExecCoreRunnerStartAmbiguousGenerationTerminatesDirectChild(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := newStartTestProcess(52)
	t.Cleanup(started.release)
	operations := &startTestProcessOperations{
		started: started,
		process: Process{PID: 52, Executable: executable, UID: 0},
	}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.Operations = operations

	if _, err := runner.Start(context.Background()); err == nil {
		t.Fatal("Start accepted a child without immutable generation")
	}
	if got := started.terminationCount(); got != 1 {
		t.Fatalf("direct child termination calls = %d, want 1", got)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("ambiguous child invoked bare PID signal seam %d times", got)
	}
}

func TestExecCoreRunnerAdoptedWatcherOutlivesInspectionContext(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bx")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	generation := "darwin:123:456"
	statePath := filepath.Join(dir, "core-process.json")
	if err := saveProcessRecord(statePath, processRecord{PID: 42, Executable: executable, Generation: generation}); err != nil {
		t.Fatal(err)
	}
	operations := &watchTestProcessOperations{process: Process{PID: 42, Executable: executable, UID: 0, Generation: generation}, alive: true}
	runner := NewExecCoreRunner(executable, filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = statePath
	runner.Operations = operations
	runner.InspectInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	process, err := runner.Existing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	process = runner.Watch(process)
	cancel()
	time.Sleep(20 * time.Millisecond)
	operations.setAlive(false)
	select {
	case <-process.Exit:
	case <-time.After(600 * time.Millisecond):
		t.Fatal("adopted Core exit was not observed after inspection context ended")
	}
}

func TestExecCoreRunnerExistingDoesNotStartWatcherBeforeAcceptance(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	t.Cleanup(func() {
		operations.setAlive(false)
		time.Sleep(3 * runner.InspectInterval)
	})

	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if process.Exit != nil {
		t.Fatal("Existing started an exit watcher before manager acceptance")
	}
	time.Sleep(4 * runner.InspectInterval)
	if got := operations.inspectCount(); got != 1 {
		t.Fatalf("pre-acceptance inspections = %d, want only the Existing inspection", got)
	}
}

func TestExecCoreRunnerAdoptedWatcherSurvivesAtomicExecutableReplacement(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	process = runner.Watch(process)
	replaceExecutableAtomically(t, runner.Executable)
	time.Sleep(4 * runner.InspectInterval)
	select {
	case err := <-process.Exit:
		t.Fatalf("watcher reported false exit after atomic replacement: %v", err)
	default:
	}
	operations.setAlive(false)
	select {
	case <-process.Exit:
	case <-time.After(time.Second):
		t.Fatal("watcher did not report definitive Core exit")
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
	process = runner.Watch(process)
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
	operations.setProcess(Process{PID: process.PID, Executable: process.Executable, UID: 0})
	shutdownCalls := 0
	runner.ShutdownCore = func(context.Context, string, int) error {
		shutdownCalls++
		return nil
	}

	if err := runner.Stop(context.Background(), process); err == nil {
		t.Fatal("Stop succeeded on ambiguous pre-shutdown identity")
	}
	if shutdownCalls != 0 {
		t.Fatalf("ambiguous identity received %d cooperative shutdown requests", shutdownCalls)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("ambiguous identity invoked signal seam %d times", got)
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("ambiguous process record removed: %v", err)
	}
}

func TestExecCoreRunnerStopDoesNotTreatAtomicReplacementAsExit(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	replaceExecutableAtomically(t, runner.Executable)
	shutdownCalls := 0
	runner.ShutdownCore = func(context.Context, string, int) error {
		shutdownCalls++
		return nil
	}

	err := runner.Stop(context.Background(), process)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop error = %v, want timeout while same generation remains alive", err)
	}
	if shutdownCalls != 1 {
		t.Fatalf("cooperative shutdown calls = %d, want 1", shutdownCalls)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("atomic replacement invoked signal seam %d times", got)
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("live process record removed after atomic replacement: %v", err)
	}
}

func TestExecCoreRunnerGenerationMismatchIsNotStopped(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	operations.setProcess(Process{
		PID:        process.PID,
		Executable: process.Executable,
		UID:        process.UID,
		Generation: "darwin:999:1",
	})
	shutdownCalls := 0
	runner.ShutdownCore = func(context.Context, string, int) error {
		shutdownCalls++
		return nil
	}

	if err := runner.Stop(context.Background(), process); err != nil {
		t.Fatalf("Stop returned error after recorded generation disappeared: %v", err)
	}
	if shutdownCalls != 0 {
		t.Fatalf("reused PID received %d cooperative shutdown requests", shutdownCalls)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("reused PID invoked signal seam %d times", got)
	}
}

func TestExecCoreRunnerExistingGenerationMismatchIsNotAdopted(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	operations.setProcess(Process{
		PID:        process.PID,
		Executable: process.Executable,
		UID:        process.UID,
		Generation: "darwin:999:1",
	})

	existing, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if existing.PID != 0 {
		t.Fatalf("reused PID was adopted: %+v", existing)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("reused PID invoked signal seam %d times", got)
	}
}

func TestExecCoreRunnerExistingAmbiguousGenerationRetainsRecord(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	operations.setProcess(Process{PID: process.PID, Executable: process.Executable, UID: process.UID})

	if _, err := runner.Existing(context.Background()); err == nil {
		t.Fatal("process without generation was adopted")
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("ambiguous process record removed: %v", err)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("ambiguous process invoked signal seam %d times", got)
	}
}

func TestExecCoreRunnerLegacyRecordFailsClosed(t *testing.T) {
	runner, _, operations := newRecordedProcessRunner(t)
	if err := writeJSONAtomically(runner.StatePath, processRecord{PID: 42, Executable: runner.Executable}); err != nil {
		t.Fatal(err)
	}

	if _, err := runner.Existing(context.Background()); err == nil {
		t.Fatal("legacy record without generation was adopted")
	}
	if _, err := os.Stat(runner.StatePath); err != nil {
		t.Fatalf("legacy process record removed: %v", err)
	}
	if got := operations.signalCount(); got != 0 {
		t.Fatalf("legacy record invoked signal seam %d times", got)
	}
}

func TestExecCoreRunnerStopWaitsForRecordedIdentityToDisappear(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	otherExecutable := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(otherExecutable, []byte("other"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner.ShutdownCore = func(context.Context, string, int) error {
		operations.setProcess(Process{PID: process.PID, Executable: otherExecutable, UID: 501, Generation: "darwin:999:1"})
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
	process := Process{PID: 42, Executable: executable, UID: 0, Generation: "darwin:123:456"}
	statePath := filepath.Join(dir, "core-process.json")
	if err := saveProcessRecord(statePath, processRecord{PID: process.PID, Executable: executable, Generation: process.Generation}); err != nil {
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

func replaceExecutableAtomically(t *testing.T, path string) {
	t.Helper()
	replacement := path + ".replacement"
	if err := os.WriteFile(replacement, []byte("replacement binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
}

type watchTestProcessOperations struct {
	mu          sync.Mutex
	process     Process
	alive       bool
	inspectErr  error
	signals     int
	inspections int
}

type startTestProcess struct {
	pid          int
	wait         chan struct{}
	releaseOnce  sync.Once
	mu           sync.Mutex
	terminations int
}

func newStartTestProcess(pid int) *startTestProcess {
	return &startTestProcess{pid: pid, wait: make(chan struct{})}
}

func (p *startTestProcess) PID() int { return p.pid }

func (p *startTestProcess) Wait() error {
	<-p.wait
	return nil
}

func (p *startTestProcess) Terminate() error {
	p.mu.Lock()
	p.terminations++
	p.mu.Unlock()
	p.release()
	return nil
}

func (p *startTestProcess) release() {
	p.releaseOnce.Do(func() { close(p.wait) })
}

func (p *startTestProcess) terminationCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminations
}

type startTestProcessOperations struct {
	started *startTestProcess
	process Process
	mu      sync.Mutex
	signals int
}

func (o *startTestProcessOperations) Start(string, []string) (StartedProcess, error) {
	return o.started, nil
}

func (o *startTestProcessOperations) Inspect(int) (Process, error) {
	return o.process, nil
}

func (o *startTestProcessOperations) Signal(int, os.Signal) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.signals++
	return errors.New("unexpected signal")
}

func (o *startTestProcessOperations) signalCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.signals
}

func (*watchTestProcessOperations) Start(string, []string) (StartedProcess, error) {
	return nil, errors.New("unexpected start")
}

func (o *watchTestProcessOperations) Inspect(int) (Process, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.inspections++
	if o.inspectErr != nil {
		return Process{}, o.inspectErr
	}
	if !o.alive {
		return Process{}, ErrProcessNotRunning
	}
	return o.process, nil
}

func (o *watchTestProcessOperations) inspectCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.inspections
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
