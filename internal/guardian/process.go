package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const defaultProcessStatePath = "/var/lib/bx/core-process.json"

type Process struct {
	PID        int
	Executable string
	UID        int
	Exit       <-chan error
	identity   executableIdentity
}

type processRecord struct {
	PID        int    `json:"pid"`
	Executable string `json:"executable"`
	Device     uint64 `json:"device"`
	Inode      uint64 `json:"inode"`
}

type executableIdentity struct {
	device uint64
	inode  uint64
}

type StartedProcess interface {
	PID() int
	Wait() error
}

type ProcessOperations interface {
	Start(executable string, args []string) (StartedProcess, error)
	Inspect(pid int) (Process, error)
	Signal(pid int, signal os.Signal) error
}

type ExecCoreRunner struct {
	Executable  string
	ConfigPath  string
	DNSListen   string
	StatePath   string
	StopTimeout time.Duration
	Operations  ProcessOperations

	mu sync.Mutex
}

func NewExecCoreRunner(executable, configPath, dnsListen string) *ExecCoreRunner {
	return &ExecCoreRunner{
		Executable: executable,
		ConfigPath: configPath,
		DNSListen:  dnsListen,
		StatePath:  defaultProcessStatePath,
	}
}

func coreArgs(configPath, dnsListen string) []string {
	return []string{"run", "-c", configPath, "--listen-dns", dnsListen}
}

func (r *ExecCoreRunner) Start(ctx context.Context) (Process, error) {
	if err := ctx.Err(); err != nil {
		return Process{}, err
	}
	if err := r.validate(); err != nil {
		return Process{}, err
	}
	operations := r.operations()
	started, err := operations.Start(r.Executable, coreArgs(r.ConfigPath, r.DNSListen))
	if err != nil {
		return Process{}, fmt.Errorf("start installed Core: %w", err)
	}
	identity, err := statExecutableIdentity(r.Executable)
	if err != nil {
		_ = operations.Signal(started.PID(), os.Kill)
		return Process{}, fmt.Errorf("identify installed Core: %w", err)
	}
	record := processRecord{PID: started.PID(), Executable: r.Executable, Device: identity.device, Inode: identity.inode}
	if err := saveProcessRecord(r.statePath(), record); err != nil {
		_ = operations.Signal(started.PID(), os.Kill)
		return Process{}, fmt.Errorf("persist Core process: %w", err)
	}
	exit := make(chan error, 1)
	go func() {
		exit <- started.Wait()
		close(exit)
		r.removeRecordIfPID(started.PID())
	}()
	return Process{PID: started.PID(), Executable: r.Executable, UID: os.Geteuid(), Exit: exit, identity: identity}, nil
}

func (r *ExecCoreRunner) Existing(ctx context.Context) (Process, error) {
	if err := ctx.Err(); err != nil {
		return Process{}, err
	}
	if err := r.validate(); err != nil {
		return Process{}, err
	}
	record, err := loadProcessRecord(r.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return Process{}, nil
	}
	if err != nil {
		return Process{}, err
	}
	process, err := r.operations().Inspect(record.PID)
	if err != nil {
		r.removeRecordIfPID(record.PID)
		return Process{}, nil
	}
	process.identity = executableIdentity{device: record.Device, inode: record.Inode}
	exit := make(chan error, 1)
	process.Exit = exit
	go r.watchExisting(process.PID, exit)
	return process, nil
}

func (r *ExecCoreRunner) Verify(process Process) error {
	if err := verifyInstalledProcess(process, r.Executable); err != nil {
		return err
	}
	if process.identity != (executableIdentity{}) {
		installed, err := statExecutableIdentity(r.Executable)
		if err != nil {
			return err
		}
		if process.identity != installed {
			return errors.New("recorded Core executable no longer matches installed inode")
		}
	}
	return nil
}

func (r *ExecCoreRunner) Stop(ctx context.Context, process Process) error {
	if process.PID <= 0 {
		return nil
	}
	current, err := r.operations().Inspect(process.PID)
	if err != nil {
		r.removeRecordIfPID(process.PID)
		return nil
	}
	current.identity = process.identity
	if err := r.Verify(current); err != nil {
		return fmt.Errorf("refuse to signal unverified PID %d: %w", process.PID, err)
	}
	if err := r.operations().Signal(process.PID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal Core PID %d: %w", process.PID, err)
	}
	timeout := r.StopTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := r.operations().Inspect(process.PID); err != nil {
			r.removeRecordIfPID(process.PID)
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("wait for Core PID %d: %w", process.PID, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (r *ExecCoreRunner) watchExisting(pid int, exit chan<- error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		if _, err := r.operations().Inspect(pid); err != nil {
			exit <- err
			close(exit)
			r.removeRecordIfPID(pid)
			return
		}
	}
}

func (r *ExecCoreRunner) validate() error {
	if !filepath.IsAbs(r.Executable) {
		return errors.New("installed Core executable must be absolute")
	}
	if !filepath.IsAbs(r.ConfigPath) {
		return errors.New("Core config path must be absolute")
	}
	if r.DNSListen == "" {
		return errors.New("Core DNS listen address required")
	}
	return nil
}

func (r *ExecCoreRunner) operations() ProcessOperations {
	if r.Operations != nil {
		return r.Operations
	}
	return osProcessOperations{}
}

func (r *ExecCoreRunner) statePath() string {
	if r.StatePath != "" {
		return r.StatePath
	}
	return defaultProcessStatePath
}

func (r *ExecCoreRunner) removeRecordIfPID(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := loadProcessRecord(r.statePath())
	if err != nil || record.PID != pid {
		return
	}
	_ = os.Remove(r.statePath())
}

func verifyInstalledProcess(process Process, installedPath string) error {
	if process.PID <= 0 {
		return errors.New("Core PID must be positive")
	}
	if process.UID != 0 {
		return fmt.Errorf("Core effective UID %d is not root", process.UID)
	}
	actual, err := os.Stat(process.Executable)
	if err != nil {
		return fmt.Errorf("stat running Core executable: %w", err)
	}
	installed, err := os.Stat(installedPath)
	if err != nil {
		return fmt.Errorf("stat installed Core executable: %w", err)
	}
	if !os.SameFile(actual, installed) {
		return errors.New("running Core executable does not match installed inode")
	}
	return nil
}

func statExecutableIdentity(path string) (executableIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return executableIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return executableIdentity{}, errors.New("executable inode identity unavailable")
	}
	return executableIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func saveProcessRecord(path string, record processRecord) error {
	if record.PID <= 0 || !filepath.IsAbs(record.Executable) {
		return errors.New("invalid Core process record")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	return writeJSONAtomically(path, record)
}

func loadProcessRecord(path string) (processRecord, error) {
	var record processRecord
	b, err := os.ReadFile(path)
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(b, &record); err != nil {
		return record, fmt.Errorf("decode Core process record: %w", err)
	}
	if record.PID <= 0 || !filepath.IsAbs(record.Executable) {
		return processRecord{}, errors.New("invalid Core process record")
	}
	return record, nil
}

type osProcessOperations struct{}

func (osProcessOperations) Start(executable string, args []string) (StartedProcess, error) {
	cmd := exec.Command(executable, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return execStartedProcess{cmd: cmd}, nil
}

func (osProcessOperations) Inspect(pid int) (Process, error) {
	return inspectProcess(pid)
}

func (osProcessOperations) Signal(pid int, signal os.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(signal)
}

type execStartedProcess struct{ cmd *exec.Cmd }

func (p execStartedProcess) PID() int    { return p.cmd.Process.Pid }
func (p execStartedProcess) Wait() error { return p.cmd.Wait() }
