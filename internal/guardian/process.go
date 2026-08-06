package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/getbx/bx/internal/supervisor"
)

const (
	defaultProcessStatePath  = "/var/lib/bx/core-process.json"
	guardianBypassHandoffEnv = "BX_GUARDIAN_BYPASS_HANDOFF"
)

var (
	ErrProcessNotRunning         = errors.New("process is not running")
	ErrProcessOwnershipUncertain = errors.New("Core process ownership is uncertain")
)

const (
	processRecordLaunching = "launching"
	// processRecordSpawned 记录「已经 fork 出子进程,但还没验明它的身份」。
	//
	// 它把残留窗口从「fork → Inspect → verify → 写 owned」缩短到「fork → 一次写盘」:
	// 窗口内崩溃留下的是带真实 PID、可向 OS 求证的 spawned 记录(死了自愈,活着
	// fail-closed),而不是一个 PID==0、无从判断的 launching 标记。
	//
	// **注意它没有让 launching 变安全**:spawned 那次写盘本身失败时(磁盘错误),
	// fork 已发生而盘上仍只有 launching。故 launching 依旧一律判所有权不确定。
	// 要彻底解开,需要绕过自身簿记去问系统「有没有进程在跑我们的 Core 可执行文件」。
	processRecordSpawned = "spawned"
	processRecordOwned   = "owned"
)

type Process struct {
	PID        int
	Executable string
	UID        int
	Generation string
	Exit       <-chan error
	Uncertain  bool
	Resolution <-chan error
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

type postForkCleanupContextKey struct{}

func withPostForkCleanupContext(operationCtx, cleanupCtx context.Context) context.Context {
	return context.WithValue(operationCtx, postForkCleanupContextKey{}, cleanupCtx)
}

func postForkCleanupContext(ctx context.Context) context.Context {
	cleanupCtx, _ := ctx.Value(postForkCleanupContextKey{}).(context.Context)
	if cleanupCtx == nil {
		return ctx
	}
	return cleanupCtx
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
	Start(executable string, args, environment []string) (StartedProcess, error)
	Inspect(pid int) (Process, error)
}

type ExecCoreRunner struct {
	// ExecutablePath 是构造期设置的初始 Core 可执行路径;运行期读写请走
	// Executable()/SetExecutable()(锁保护,支持热切换),不要直接读写此字段。
	ExecutablePath       string
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
	RemoveProcessRecord  func(string) error

	mu sync.Mutex
}

func NewExecCoreRunner(executable, configPath, dnsListen string) *ExecCoreRunner {
	return &ExecCoreRunner{
		ExecutablePath: executable,
		ConfigPath:     configPath,
		DNSListen:      dnsListen,
		StatePath:      defaultProcessStatePath,
	}
}

func coreArgs(configPath, dnsListen string) []string {
	return []string{"run", "-c", configPath, "--listen-dns", dnsListen}
}

func (r *ExecCoreRunner) Start(ctx context.Context, options CoreStartOptions) (Process, error) {
	if err := ctx.Err(); err != nil {
		return Process{}, err
	}
	if err := r.validate(); err != nil {
		return Process{}, err
	}
	if record, err := loadProcessRecord(r.statePath()); err == nil {
		if err := r.refuseLiveLaunchMarker(record); err != nil {
			return Process{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Process{}, uncertainOwnership(Process{Uncertain: true}, fmt.Errorf("read durable launch marker: %w", err))
	}
	if err := r.saveRecord(r.statePath(), processRecord{State: processRecordLaunching}); err != nil {
		return Process{}, fmt.Errorf("persist launch marker before starting Core: %w", err)
	}
	environment, err := coreStartEnvironment(os.Environ(), options)
	if err != nil {
		if clearErr := r.clearLaunchMarker(); clearErr != nil {
			return Process{}, uncertainOwnership(Process{Uncertain: true}, errors.Join(err, clearErr))
		}
		return Process{}, err
	}
	operations := r.operations()
	executable := r.executablePath()
	started, err := operations.Start(executable, coreArgs(r.ConfigPath, r.DNSListen), environment)
	if err != nil {
		if clearErr := r.clearLaunchMarker(); clearErr != nil {
			return Process{}, uncertainOwnership(Process{Uncertain: true}, errors.Join(fmt.Errorf("start installed Core: %w", err), clearErr))
		}
		return Process{}, fmt.Errorf("start installed Core: %w", err)
	}
	// fork 已经发生:立刻把子进程 PID 落盘,再去验明身份。此后即便崩在
	// Inspect/verify/写 owned 之间,盘上留下的也是可向 OS 求证的 spawned 记录,
	// 而不是一个 PID==0、无从判断有没有孤儿 Core 的 launching 标记。
	if err := r.saveRecord(r.statePath(), processRecord{PID: started.PID(), State: processRecordSpawned}); err != nil {
		return Process{}, r.cleanupFailedStart(ctx, started, Process{},
			fmt.Errorf("persist spawned Core PID %d: %w", started.PID(), err))
	}
	process, err := operations.Inspect(started.PID())
	if err != nil {
		return Process{}, r.cleanupFailedStart(ctx, started, Process{}, fmt.Errorf("inspect started Core PID %d: %w", started.PID(), err))
	}
	if process.PID != started.PID() {
		return Process{}, r.cleanupFailedStart(ctx, started, process, fmt.Errorf("started Core PID %d inspected as PID %d", started.PID(), process.PID))
	}
	if err := verifyInstalledProcess(process, executable); err != nil {
		return Process{}, r.cleanupFailedStart(ctx, started, process, fmt.Errorf("verify started Core PID %d: %w", started.PID(), err))
	}
	if err := ctx.Err(); err != nil {
		return Process{}, r.cleanupFailedStart(ctx, started, process, err)
	}
	record := processRecord{PID: process.PID, Executable: process.Executable, Generation: process.Generation, State: processRecordOwned}
	if err := r.saveRecord(r.statePath(), record); err != nil {
		return Process{}, r.cleanupFailedStart(ctx, started, process, fmt.Errorf("persist Core process: %w", err))
	}
	exit := make(chan error, 1)
	go func() {
		waitErr := started.Wait()
		if err := r.removeRecordIfGeneration(process.PID, process.Generation); err != nil {
			waitErr = errors.Join(waitErr, uncertainOwnership(process, fmt.Errorf("clear owned Core record after exit: %w", err)))
		}
		exit <- waitErr
		close(exit)
	}()
	process.Exit = exit
	return process, nil
}

// refuseLiveLaunchMarker decides whether an already-present durable launch
// marker blocks a new Start. It returns nil only when the OS authoritatively
// confirms the recorded PID is dead — the same authority Existing() uses to
// treat a stale record as "no existing Core".
//
// Without this, Existing()'s tolerance of an unremovable stale record bought
// nothing end to end: Start immediately rejected the very same file with
// "durable launch marker already exists", producing the identical
// core_ownership_uncertain the user saw before (only one step later).
//
// fail-closed is unchanged in every uncertain case: a *live* recorded PID
// still refuses (another Core may genuinely be running under it, matching
// identity or not), and an inconclusive inspection still refuses.
//
// launching(PID==0)仍然拒绝:两段式标记缩小了它出现的窗口,但没有让它变得
// 安全——spawned 那次写盘失败时,fork 已发生而盘上仍只有 launching。
func (r *ExecCoreRunner) refuseLiveLaunchMarker(record processRecord) error {
	uncertain := func(cause error) error {
		return uncertainOwnership(Process{PID: record.PID, Executable: record.Executable, Generation: record.Generation, Uncertain: true}, cause)
	}
	if record.PID <= 0 {
		return uncertain(errors.New("durable launch marker already exists"))
	}
	if _, err := r.operations().Inspect(record.PID); err != nil {
		if !errors.Is(err, ErrProcessNotRunning) {
			return uncertain(fmt.Errorf("durable launch marker already exists: inspect recorded Core PID %d: %w", record.PID, err))
		}
		// 记录里的进程已被 OS 确认死亡:陈旧文件不是"所有权不确定"。
		// saveRecord 随后会覆盖它(remove 可能正因权限失败而清不掉,那也
		// 不该卡死 bx up)。
		log.Printf("guardian_stale_launch_marker pid=%d generation=%s", record.PID, record.Generation)
		return nil
	}
	return uncertain(errors.New("durable launch marker already exists"))
}

func coreStartEnvironment(base []string, options CoreStartOptions) ([]string, error) {
	environment := make([]string, 0, len(base)+1)
	for _, entry := range base {
		if strings.HasPrefix(entry, guardianBypassHandoffEnv+"=") {
			continue
		}
		environment = append(environment, entry)
	}
	if len(options.GuardianBypassHandoff) == 0 {
		return environment, nil
	}
	canonical, err := canonicalBypassSet(options.GuardianBypassHandoff)
	if err != nil {
		return nil, fmt.Errorf("invalid Guardian bypass handoff")
	}
	bypasses := make([]string, 0, len(canonical))
	for bypass := range canonical {
		bypasses = append(bypasses, bypass)
	}
	sort.Strings(bypasses)
	environment = append(environment, guardianBypassHandoffEnv+"="+strings.Join(bypasses, ","))
	return environment, nil
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
	switch record.state() {
	case processRecordLaunching:
		// 仍然 fail-closed。两段式标记把「已 fork 未记录」这一含义搬给了 spawned,
		// 但没有把 launching 变成安全的:spawned 那次**写盘本身失败**时(磁盘错误),
		// fork 已经发生而盘上仍只有 launching,孤儿 Core 可能真的活着。
		// 把它当作「确定没 fork」会起第二个 Core——正是 af81632 被回退的原因。
		// 要真正解开,需要绕过自身簿记去向系统求证「有没有跑着我们的 Core 可执行
		// 文件的进程」,见 docs/superpowers/plans/2026-08-05-guardian-launch-marker-deadlock.md。
		return Process{}, uncertainOwnership(Process{PID: record.PID, Executable: record.Executable, Generation: record.Generation, Uncertain: true}, errors.New("durable launch marker has no accepted process record"))
	case processRecordSpawned:
		// 已 fork、身份未验:交给下面的 Inspect 向 OS 求证——死了自愈,
		// 活着则 fail-closed(我们没验明它,绝不能当自己的 Core 接管)。
	case processRecordOwned:
	default:
		return Process{}, uncertainOwnership(Process{PID: record.PID, Executable: record.Executable, Generation: record.Generation, Uncertain: true}, errors.New("durable launch marker has no accepted process record"))
	}
	process, err := r.operations().Inspect(record.PID)
	if err != nil {
		if errors.Is(err, ErrProcessNotRunning) {
			// 进程已经死了。清不掉一个陈旧文件不等于"所有权不确定"——
			// 后者是给"进程还在但身份存疑"准备的语义。把它当成不确定会卡死
			// bx up(真机事故:core-process.json 指向早已死亡的 PID,手工删
			// 文件后才恢复)。记日志继续,当作无既有 Core。
			if clearErr := r.removeStaleRecord(record); clearErr != nil {
				log.Printf("guardian_stale_core_record pid=%d state=%s clear_failed=%v", record.PID, record.state(), clearErr)
			}
			return Process{}, nil
		}
		return Process{}, fmt.Errorf("inspect recorded Core PID %d: %w", record.PID, err)
	}
	if record.state() == processRecordSpawned {
		// 进程活着,但我们从没验明它的身份(spawned 记录里没有 executable /
		// generation)。既不能当自己的 Core 接管,更不能当它不存在——那会起
		// 第二个 Core:两个 Core 抢控制 socket 与 split-default 路由,先退出的
		// 那个用旧快照还原,掀掉另一个的劫持 → status 显绿而流量明文直连。
		return Process{}, uncertainOwnership(
			Process{PID: record.PID, Uncertain: true},
			errors.New("spawned Core record has a live process whose identity was never verified"))
	}
	expected := Process{PID: record.PID, Executable: record.Executable, UID: 0, Generation: record.Generation}
	same, err := sameProcessIdentity(expected, process)
	if err != nil {
		return Process{}, fmt.Errorf("compare recorded Core PID %d identity: %w", record.PID, err)
	}
	if !same {
		if clearErr := r.removeRecordIfGeneration(record.PID, record.Generation); clearErr != nil {
			return Process{}, uncertainOwnership(Process{PID: record.PID, Executable: record.Executable, Generation: record.Generation, Uncertain: true}, fmt.Errorf("clear replaced Core record: %w", clearErr))
		}
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
	return verifyInstalledProcess(process, r.executablePath())
}

func (r *ExecCoreRunner) Stop(ctx context.Context, process Process) error {
	if process.PID <= 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyInstalledProcess(process, r.executablePath()); err != nil {
		return fmt.Errorf("verify recorded Core PID %d before shutdown: %w", process.PID, err)
	}
	current, err := r.operations().Inspect(process.PID)
	if errors.Is(err, ErrProcessNotRunning) {
		if clearErr := r.removeRecordIfGeneration(process.PID, process.Generation); clearErr != nil {
			return uncertainOwnership(process, fmt.Errorf("clear exited Core record: %w", clearErr))
		}
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
		if clearErr := r.removeRecordIfGeneration(process.PID, process.Generation); clearErr != nil {
			return uncertainOwnership(process, fmt.Errorf("clear replaced Core record: %w", clearErr))
		}
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
			if clearErr := r.removeRecordIfGeneration(process.PID, process.Generation); clearErr != nil {
				return uncertainOwnership(process, fmt.Errorf("clear exited Core record: %w", clearErr))
			}
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
			if clearErr := r.removeRecordIfGeneration(process.PID, process.Generation); clearErr != nil {
				return uncertainOwnership(process, fmt.Errorf("clear replaced Core record: %w", clearErr))
			}
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
	if clearErr := r.removeRecordIfGeneration(process.PID, process.Generation); clearErr != nil {
		err = errors.Join(err, uncertainOwnership(process, fmt.Errorf("clear owned Core record after exit: %w", clearErr)))
	}
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
	if !filepath.IsAbs(r.executablePath()) {
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

// Executable 返回当前生效的 Core 可执行路径(并发安全)。
func (r *ExecCoreRunner) Executable() string {
	return r.executablePath()
}

// SetExecutable 热切换 Core 可执行路径,必须绝对路径,并发安全。
func (r *ExecCoreRunner) SetExecutable(executable string) error {
	if !filepath.IsAbs(executable) {
		return errors.New("installed Core executable must be absolute")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ExecutablePath = executable
	return nil
}

// executablePath 是内部读取入口,锁内读取导出字段 ExecutablePath,
// 供 Start/Verify/Stop/validate 使用,避免与 SetExecutable 并发写产生数据竞争。
func (r *ExecCoreRunner) executablePath() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ExecutablePath
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

func (r *ExecCoreRunner) cleanupFailedStart(ctx context.Context, started StartedProcess, process Process, cause error) error {
	proved, cleanupErr, late := r.cleanupStartedProcess(postForkCleanupContext(ctx), started)
	if proved && cleanupErr == nil {
		return cause
	}
	process.Uncertain = true
	if late != nil {
		process.Resolution = late
	}
	return uncertainOwnership(process, errors.Join(cause, cleanupErr))
}

func (r *ExecCoreRunner) cleanupStartedProcess(ctx context.Context, process StartedProcess) (bool, error, <-chan error) {
	_ = process.Terminate()
	done := make(chan error, 1)
	go func() {
		_ = process.Wait()
		done <- r.clearLaunchMarker()
		close(done)
	}()
	timer := time.NewTimer(r.launchCleanupTimeout())
	defer timer.Stop()
	select {
	case err := <-done:
		return true, err, nil
	case <-ctx.Done():
		return false, nil, done
	case <-timer.C:
		return false, nil, done
	}
}

func (r *ExecCoreRunner) launchCleanupTimeout() time.Duration {
	if r.LaunchCleanupTimeout > 0 {
		return r.LaunchCleanupTimeout
	}
	return 5 * time.Second
}

func (r *ExecCoreRunner) clearLaunchMarker() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := loadProcessRecord(r.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.state() != processRecordLaunching {
		return nil
	}
	return r.removeProcessRecord(r.statePath())
}

// removeStaleRecord 删除一条已判定为陈旧的非 owned 记录:launching(还没 fork),
// 或进程已被 OS 确认死亡的 spawned。按 state+PID 精确匹配后才删,避免误删并发
// 写入的新记录。owned 记录不走这里——它由 removeRecordIfGeneration 按
// pid+generation 匹配删除。
func (r *ExecCoreRunner) removeStaleRecord(expect processRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := loadProcessRecord(r.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.state() == processRecordOwned {
		return r.removeRecordIfGenerationLocked(record.PID, record.Generation)
	}
	if record.state() != expect.state() || record.PID != expect.PID {
		return nil
	}
	return r.removeProcessRecord(r.statePath())
}

func (r *ExecCoreRunner) removeRecordIfGeneration(pid int, generation string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.removeRecordIfGenerationLocked(pid, generation)
}

func (r *ExecCoreRunner) removeRecordIfGenerationLocked(pid int, generation string) error {
	record, err := loadProcessRecord(r.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.state() != processRecordOwned || record.PID != pid || record.Generation != generation {
		return nil
	}
	return r.removeProcessRecord(r.statePath())
}

func (r *ExecCoreRunner) removeProcessRecord(path string) error {
	if r.RemoveProcessRecord != nil {
		return r.RemoveProcessRecord(path)
	}
	return removeProcessRecordFile(path)
}

func removeProcessRecordFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
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
	case processRecordSpawned:
		// 只要求 PID:此刻还没 Inspect 过,可执行路径与 generation 都无从填写。
		return r.PID > 0
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

func (osProcessOperations) Start(executable string, args, environment []string) (StartedProcess, error) {
	cmd := exec.Command(executable, args...)
	cmd.Env = environment
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
