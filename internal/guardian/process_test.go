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

type watchTestProcessOperations struct {
	mu      sync.Mutex
	process Process
	alive   bool
}

func (*watchTestProcessOperations) Start(string, []string) (StartedProcess, error) {
	return nil, errors.New("unexpected start")
}

func (o *watchTestProcessOperations) Inspect(int) (Process, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.alive {
		return Process{}, errors.New("process exited")
	}
	return o.process, nil
}

func (*watchTestProcessOperations) Signal(int, os.Signal) error {
	return errors.New("unexpected signal")
}

func (o *watchTestProcessOperations) setAlive(alive bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.alive = alive
}
