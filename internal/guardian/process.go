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
	"time"

	"github.com/getbx/bx/internal/supervisor"
)

const defaultProcessStatePath = "/var/lib/bx/core-process.json"

var ErrProcessNotRunning = errors.New("process is not running")

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
	Executable      string
	ConfigPath      string
	DNSListen       string
	StatePath       string
	ControlSocket   string
	StopTimeout     time.Duration
	InspectInterval time.Duration
	Operations      ProcessOperations
	ShutdownCore    func(context.Context, string, int) error

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
		if errors.Is(err, ErrProcessNotRunning) {
			r.removeRecordIfPID(record.PID)
			return Process{}, nil
		}
		return Process{}, fmt.Errorf("inspect recorded Core PID %d: %w", record.PID, err)
	}
	process.identity = executableIdentity{device: record.Device, inode: record.Inode}
	exit := make(chan error, 1)
	process.Exit = exit
	go r.watchExisting(process, exit)
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
	if err := ctx.Err(); err != nil {
		return err
	}
	shutdown := r.ShutdownCore
	if shutdown == nil {
		shutdown = supervisor.ShutdownControl
	}
	controlSocket := r.ControlSocket
	if controlSocket == "" {
		controlSocket = supervisor.SockPath
	}
	if err := shutdown(ctx, controlSocket, process.PID); err != nil {
		return fmt.Errorf("request cooperative shutdown for Core PID %d: %w", process.PID, err)
	}
	timeout := r.StopTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(r.inspectInterval(50 * time.Millisecond))
	defer ticker.Stop()
	for {
		current, err := r.operations().Inspect(process.PID)
		if errors.Is(err, ErrProcessNotRunning) {
			r.removeRecordIfPID(process.PID)
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect Core PID %d after shutdown request: %w", process.PID, err)
		}
		same, err := sameProcessIdentity(process, current)
		if err != nil {
			return fmt.Errorf("compare Core PID %d identity after shutdown request: %w", process.PID, err)
		}
		if !same {
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

func (r *ExecCoreRunner) watchExisting(process Process, exit chan<- error) {
	ticker := time.NewTicker(r.inspectInterval(250 * time.Millisecond))
	defer ticker.Stop()
	for range ticker.C {
		current, err := r.operations().Inspect(process.PID)
		if err != nil {
			if errors.Is(err, ErrProcessNotRunning) {
				r.finishExistingWatch(process.PID, exit, err)
				return
			}
			continue
		}
		same, err := sameProcessIdentity(process, current)
		if err != nil {
			continue
		}
		if !same {
			r.finishExistingWatch(process.PID, exit, fmt.Errorf("%w: recorded Core identity disappeared", ErrProcessNotRunning))
			return
		}
	}
}

func (r *ExecCoreRunner) finishExistingWatch(pid int, exit chan<- error, err error) {
	r.removeRecordIfPID(pid)
	exit <- err
	close(exit)
}

func (r *ExecCoreRunner) inspectInterval(fallback time.Duration) time.Duration {
	if r.InspectInterval > 0 {
		return r.InspectInterval
	}
	return fallback
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

func sameProcessIdentity(expected, current Process) (bool, error) {
	if current.PID != expected.PID || current.UID != expected.UID {
		return false, nil
	}
	if expected.identity != (executableIdentity{}) {
		identity, err := statExecutableIdentity(current.Executable)
		if err != nil {
			return false, err
		}
		return identity == expected.identity, nil
	}
	expectedInfo, err := os.Stat(expected.Executable)
	if err != nil {
		return false, err
	}
	currentInfo, err := os.Stat(current.Executable)
	if err != nil {
		return false, err
	}
	return os.SameFile(expectedInfo, currentInfo), nil
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
