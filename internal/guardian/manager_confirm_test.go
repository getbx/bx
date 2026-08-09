package guardian

import (
	"context"
	"errors"
	"testing"
	"time"
)

// scannerOnlyRunner / nonScanningRunner 是两个最小的 CoreRunner 实现:除
// ScanRunning 外全部返回零值。confirmCoreStopped 只经过 ScanRunning 这一跳,
// 其余方法在本文件里从不被调用,写出来只为满足接口。
type scannerOnlyRunner struct {
	scan *scriptedScanner
}

func (r *scannerOnlyRunner) Existing(context.Context) (Process, error) { return Process{}, nil }
func (r *scannerOnlyRunner) Watch(process Process) Process             { return process }
func (r *scannerOnlyRunner) Verify(Process) error                      { return nil }
func (r *scannerOnlyRunner) Start(context.Context, CoreStartOptions) (Process, error) {
	return Process{}, nil
}
func (r *scannerOnlyRunner) Stop(context.Context, Process) error { return nil }
func (r *scannerOnlyRunner) Executable() string                  { return "" }
func (r *scannerOnlyRunner) SetExecutable(string) error          { return nil }
func (r *scannerOnlyRunner) ScanRunning() ([]Process, error)     { return r.scan.ScanRunning() }

// nonScanningRunner 刻意不实现 ScanRunning:它代表「求证不了」的 runner。
type nonScanningRunner struct{}

func (r *nonScanningRunner) Existing(context.Context) (Process, error) { return Process{}, nil }
func (r *nonScanningRunner) Watch(process Process) Process             { return process }
func (r *nonScanningRunner) Verify(Process) error                      { return nil }
func (r *nonScanningRunner) Start(context.Context, CoreStartOptions) (Process, error) {
	return Process{}, nil
}
func (r *nonScanningRunner) Stop(context.Context, Process) error { return nil }
func (r *nonScanningRunner) Executable() string                  { return "" }
func (r *nonScanningRunner) SetExecutable(string) error          { return nil }

type scriptedScanner struct {
	results [][]Process
	errs    []error
	calls   int
}

func (s *scriptedScanner) ScanRunning() ([]Process, error) {
	i := s.calls
	s.calls++
	if i < len(s.errs) && s.errs[i] != nil {
		return nil, s.errs[i]
	}
	if i < len(s.results) {
		return s.results[i], nil
	}
	return nil, nil
}

// 扫不到 Core = 求证过了,可以说 off。
func TestConfirmCoreStoppedAcceptsAnEmptyScan(t *testing.T) {
	m := &Manager{runner: &scannerOnlyRunner{scan: &scriptedScanner{}}}
	stopped, reason := m.confirmCoreStopped()
	if !stopped {
		t.Fatalf("扫不到 Core 时必须允许报 off, reason=%s", reason)
	}
}

// 扫到 Core 还在 = 不许说 off。**这条是整个改动的理由。**
func TestConfirmCoreStoppedRefusesWhenACoreIsStillRunning(t *testing.T) {
	scan := &scriptedScanner{results: [][]Process{{{PID: 4242}}, {{PID: 4242}}}}
	m := &Manager{runner: &scannerOnlyRunner{scan: scan}}
	stopped, reason := m.confirmCoreStopped()
	if stopped {
		t.Fatal("系统里还有 Core 在跑时绝不能报 off —— 那是对用户撒谎,而他此刻以为保护关了")
	}
	if reason != "core_still_running" {
		t.Errorf("理由要能区分「确知还在」与「问不出来」, got %q", reason)
	}
}

// 刚被 SIGTERM、还在跑 defer 的进程既不是僵尸也还没消失。
// 扫到就断言「还在」会把正常的关闭误报成故障,所以要等一小会儿重扫一次。
func TestConfirmCoreStoppedReScansAfterASettleWindow(t *testing.T) {
	restore := coreScanSettle
	coreScanSettle = time.Millisecond
	defer func() { coreScanSettle = restore }()

	scan := &scriptedScanner{results: [][]Process{{{PID: 4242}}, nil}}
	m := &Manager{runner: &scannerOnlyRunner{scan: scan}}
	stopped, _ := m.confirmCoreStopped()
	if !stopped {
		t.Fatal("第二次扫描已经干净了,不该报「还在跑」—— 那会把每次正常关闭都变成告警")
	}
	if scan.calls != 2 {
		t.Errorf("应当重扫一次, calls=%d", scan.calls)
	}
}

// **扫描失败不许让停止失败,也不许被读成「没有」。**
func TestConfirmCoreStoppedTreatsAFailedScanAsUnconfirmed(t *testing.T) {
	scan := &scriptedScanner{errs: []error{errors.New("sysctl 挂了"), errors.New("还是挂")}}
	m := &Manager{runner: &scannerOnlyRunner{scan: scan}}
	stopped, reason := m.confirmCoreStopped()
	if stopped {
		t.Fatal("问不出来不等于没有 —— 不许塌缩成「已关闭」")
	}
	if reason != "core_scan_failed" {
		t.Errorf("理由必须与「确知还在」区分开, got %q", reason)
	}
}

// runner 压根不会扫(测试替身、将来别的平台):同样是「没能确认」,而不是崩溃或失败。
func TestConfirmCoreStoppedHandlesARunnerThatCannotScan(t *testing.T) {
	m := &Manager{runner: &nonScanningRunner{}}
	stopped, reason := m.confirmCoreStopped()
	if stopped {
		t.Fatal("不能求证时不许声称已关闭")
	}
	if reason != "core_scan_unsupported" {
		t.Errorf("got %q", reason)
	}
}
