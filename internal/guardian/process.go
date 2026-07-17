package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/getbx/bx/internal/supervisor"
)

const defaultProcessStatePath = "/var/lib/bx/core-process.json"

var ErrProcessNotRunning = errors.New("process is not running")
var ErrProcessOwnershipUncertain = errors.New("Core process ownership is uncertain")

const (
	processRecordLaunching = "launching"
	processRecordOwned     = "owned"
)

type Process struct {
	PID        int
	Executable string
	UID        int
	Generation string
	Exit       <-chan error
	Uncertain  bool
}

type processRecord struct {
	PID        int    `json:"pid"`
	Executable string `json:"executable"`
	Generation string `json:"generation"`
	State      string `json:"state,omitempty"`
}

type ownershipUncertainError struct {
	process Process
	cause   error
}

func (e *ownershipUncertainError) Error() string {
	if e.cause == nil {
		return ErrProcessOwnershipUncertain.Error()
	}
	return fmt.Sprintf("%s: %v", ErrProcessOwnershipUncertain, e.cause)
}

func (e *ownershipUncertainError) Unwrap() []error {
	if e.cause == nil {
		return []error{ErrProcessOwnershipUncertain}
	}
	return []error{ErrProcessOwnershipUncertain, e.cause}
}

type StartedProcess interface {
	PID() int
	Wait() error
	Terminate() error
}

type ProcessOperations interface {
	Start(executable string, args []string) (StartedProcess, error)
	Inspect(pid int) (Process, error)
}

type ExecCoreRunner struct {
	Executable           string
	ConfigPath           string
	DNSListen            string
	StatePath            string
	ControlSocket        string
	StopTimeout          time.Duration
	InspectInterval      time.Duration
	Operations           ProcessOperations
	ShutdownCore         func(context.Context, string, int) error
	LaunchCleanupTimeout time.Duration
	SaveProcessRecord    func(string, processRecord) error

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
	if record, err := loadProcessRecord(r.statePath()); err == nil {
		return Process{}, uncertainOwnership(Process{PID: record.PID, Executable: record.Executable, Generation: record.Generation, Uncertain: true}, errors.New("durable launch marker already exists"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return Process{}, uncertainOwnership(Process{Uncertain: true}, fmt.Errorf("read durable launch marker: %w", err))
	}
	if err := r.saveRecord(r.statePath(), processRecord{State: processRecordLaunching}); err != nil {
		return Process{}, fmt.Errorf("persist launch marker before starting Core: %w", err)
	}
	operations := r.operations()
	started, err := operations.Start(r.Executable, coreArgs(r.ConfigPath, r.DNSListen))
	if err != nil {
		return Process{}, fmt.Errorf("start installed Core: %w", err)
	}
	process, err := operations.Inspect(started.PID())
	if err != nil {
		return Process{}, r.cleanupFailedStart(started, Process{}, fmt.Errorf("inspect started Core PID %d: %w", started.PID(), err))
	}
	if process.PID != started.PID() {
		return Process{}, r.cleanupFailedStart(started, process, fmt.Errorf("started Core PID %d inspected as PID %d", started.PID(), process.PID))
	}
	if err := verifyInstalledProcess(process, r.Executable); err != nil {
		return Process{}, r.cleanupFailedStart(started, process, fmt.Errorf("verify started Core PID %d: %w", started.PID(), err))
	}
	if err := ctx.Err(); err != nil {
		return Process{}, r.cleanupFailedStart(started, process, err)
	}
	record := processRecord{PID: process.PID, Executable: process.Executable, Generation: process.Generation, State: processRecordOwned}
	if err := r.saveRecord(r.statePath(), record); err != nil {
		return Process{}, r.cleanupFailedStart(started, process, fmt.Errorf("persist Core process: %w", err))
	}
	exit := make(chan error, 1)
	go func() {
		exit <- started.Wait()
		close(exit)
		r.removeRecordIfGeneration(process.PID, process.Generation)
	}()
	process.Exit = exit
	return process, nil
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
	if record.state() != processRecordOwned {
		return Process{}, uncertainOwnership(Process{PID: record.PID, Executable: record.Executable, Generation: record.Generation, Uncertain: true}, errors.New("durable launch marker has no accepted process record"))
	}
	process, err := r.operations().Inspect(record.PID)
	if err != nil {
		if errors.Is(err, ErrProcessNotRunning) {
			r.removeRecordIfGeneration(record.PID, record.Generation)
			return Process{}, nil
		}
		return Process{}, fmt.Errorf("inspect recorded Core PID %d: %w", record.PID, err)
	}
	expected := Process{PID: record.PID, Executable: record.Executable, UID: 0, Generation: record.Generation}
	same, err := sameProcessIdentity(expected, process)
	if err != nil {
		return Process{}, fmt.Errorf("compare recorded Core PID %d identity: %w", record.PID, err)
	}
	if !same {
		r.removeRecordIfGeneration(record.PID, record.Generation)
		return Process{}, nil
	}
	return process, nil
}

func (r *ExecCoreRunner) Watch(process Process) Process {
	if process.PID <= 0 || process.Exit != nil {
		return process
	}
	exit := make(chan error, 1)
	process.Exit = exit
	go r.watchExisting(process, exit)
	return process
}

func (r *ExecCoreRunner) Verify(process Process) error {
	return verifyInstalledProcess(process, r.Executable)
}

func (r *ExecCoreRunner) Stop(ctx context.Context, process Process) error {
	if process.PID <= 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyInstalledProcess(process, r.Executable); err != nil {
		return fmt.Errorf("verify recorded Core PID %d before shutdown: %w", process.PID, err)
	}
	current, err := r.operations().Inspect(process.PID)
	if errors.Is(err, ErrProcessNotRunning) {
		r.removeRecordIfGeneration(process.PID, process.Generation)
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Core PID %d before shutdown request: %w", process.PID, err)
	}
	same, err := sameProcessIdentity(process, current)
	if err != nil {
		return fmt.Errorf("compare Core PID %d identity before shutdown request: %w", process.PID, err)
	}
	if !same {
		r.removeRecordIfGeneration(process.PID, process.Generation)
		return nil
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
			r.removeRecordIfGeneration(process.PID, process.Generation)
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
			r.removeRecordIfGeneration(process.PID, process.Generation)
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
				r.finishExistingWatch(process, exit, err)
				return
			}
			continue
		}
		same, err := sameProcessIdentity(process, current)
		if err != nil {
			continue
		}
		if !same {
			r.finishExistingWatch(process, exit, fmt.Errorf("%w: recorded Core identity disappeared", ErrProcessNotRunning))
			return
		}
	}
}

func (r *ExecCoreRunner) finishExistingWatch(process Process, exit chan<- error, err error) {
	r.removeRecordIfGeneration(process.PID, process.Generation)
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

func (r *ExecCoreRunner) saveRecord(path string, record processRecord) error {
	if r.SaveProcessRecord != nil {
		return r.SaveProcessRecord(path, record)
	}
	return saveProcessRecord(path, record)
}

func (r *ExecCoreRunner) cleanupFailedStart(started StartedProcess, process Process, cause error) error {
	if r.cleanupStartedProcess(started) {
		r.removeLaunchMarker()
		return cause
	}
	process.Uncertain = true
	return uncertainOwnership(process, cause)
}

func (r *ExecCoreRunner) cleanupStartedProcess(process StartedProcess) bool {
	_ = process.Terminate()
	done := make(chan struct{})
	go func() {
		_ = process.Wait()
		close(done)
	}()
	timer := time.NewTimer(r.launchCleanupTimeout())
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (r *ExecCoreRunner) launchCleanupTimeout() time.Duration {
	if r.LaunchCleanupTimeout > 0 {
		return r.LaunchCleanupTimeout
	}
	return 5 * time.Second
}

func (r *ExecCoreRunner) removeLaunchMarker() {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := loadProcessRecord(r.statePath())
	if err != nil || record.state() != processRecordLaunching {
		return
	}
	_ = os.Remove(r.statePath())
}

func (r *ExecCoreRunner) removeRecordIfGeneration(pid int, generation string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := loadProcessRecord(r.statePath())
	if err != nil || record.state() != processRecordOwned || record.PID != pid || record.Generation != generation {
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
	if strings.TrimSpace(process.Generation) == "" {
		return errors.New("Core process generation is unavailable")
	}
	if !filepath.IsAbs(process.Executable) || !filepath.IsAbs(installedPath) {
		return errors.New("Core executable paths must be absolute")
	}
	if filepath.Clean(process.Executable) != filepath.Clean(installedPath) {
		return errors.New("running Core executable path does not match installed path")
	}
	return nil
}

func sameProcessIdentity(expected, current Process) (bool, error) {
	if strings.TrimSpace(expected.Generation) == "" || strings.TrimSpace(current.Generation) == "" {
		return false, errors.New("process generation is unavailable")
	}
	if !filepath.IsAbs(expected.Executable) || !filepath.IsAbs(current.Executable) {
		return false, errors.New("process executable path is unavailable")
	}
	return current.PID == expected.PID &&
		current.UID == expected.UID &&
		current.Generation == expected.Generation &&
		filepath.Clean(current.Executable) == filepath.Clean(expected.Executable), nil
}

func saveProcessRecord(path string, record processRecord) error {
	if !record.valid() {
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
	if !record.valid() {
		return processRecord{}, errors.New("invalid Core process record")
	}
	return record, nil
}

func (r processRecord) state() string {
	if r.State == "" {
		return processRecordOwned
	}
	return r.State
}

func (r processRecord) valid() bool {
	switch r.state() {
	case processRecordLaunching:
		return r.PID == 0 && r.Executable == "" && r.Generation == ""
	case processRecordOwned:
		return r.PID > 0 && filepath.IsAbs(r.Executable) && strings.TrimSpace(r.Generation) != ""
	default:
		return false
	}
}

func uncertainOwnership(process Process, cause error) error {
	process.Uncertain = true
	return &ownershipUncertainError{process: process, cause: cause}
}

func uncertainProcess(err error) (Process, bool) {
	var ownershipErr *ownershipUncertainError
	if errors.As(err, &ownershipErr) {
		return ownershipErr.process, true
	}
	return Process{}, false
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

type execStartedProcess struct{ cmd *exec.Cmd }

func (p execStartedProcess) PID() int         { return p.cmd.Process.Pid }
func (p execStartedProcess) Wait() error      { return p.cmd.Wait() }
func (p execStartedProcess) Terminate() error { return p.cmd.Process.Kill() }
