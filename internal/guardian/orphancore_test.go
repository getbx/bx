package guardian

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
)

// Guardian 的 plist 加上 AbandonProcessGroup 之后,bootout 不再顺带杀 Core。卸载与
// 强制拆除靠 StopOrphanedCore 显式收尾:协作关闭 → SIGTERM → SIGKILL,每一级
// 进程走了就停。
func TestStopOrphanedCoreEscalatesOnlyAsFarAsItHasTo(t *testing.T) {
	for _, tc := range []struct {
		name      string
		diesAfter string // "shutdown" | "SIGTERM" | "SIGKILL"
		want      []os.Signal
	}{
		{"cooperative shutdown is enough", "shutdown", nil},
		{"hung socket falls back to SIGTERM", "SIGTERM", []os.Signal{syscall.SIGTERM}},
		{"ignores SIGTERM, gets SIGKILL", "SIGKILL", []os.Signal{syscall.SIGTERM, syscall.SIGKILL}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, process, operations := newRecordedProcessRunner(t)
			runner.ShutdownCore = func(_ context.Context, _ string, pid int) error {
				if pid != process.PID {
					t.Fatalf("shutdown asked PID %d, want %d", pid, process.PID)
				}
				if tc.diesAfter == "shutdown" {
					operations.setAlive(false)
					return nil
				}
				return errors.New("control socket gone")
			}
			var sent []os.Signal
			runner.SignalProcess = func(pid int, sig os.Signal) error {
				if pid != process.PID {
					t.Fatalf("signalled PID %d, want %d", pid, process.PID)
				}
				sent = append(sent, sig)
				if (tc.diesAfter == "SIGTERM" && sig == syscall.SIGTERM) || (tc.diesAfter == "SIGKILL" && sig == syscall.SIGKILL) {
					operations.setAlive(false)
				}
				return nil
			}
			stopped, err := runner.StopOrphanedCore(context.Background())
			if err != nil || !stopped {
				t.Fatalf("StopOrphanedCore = (%v, %v), want (true, nil)", stopped, err)
			}
			if len(sent) != len(tc.want) {
				t.Fatalf("signals sent = %v, want %v", sent, tc.want)
			}
			for i := range sent {
				if sent[i] != tc.want[i] {
					t.Fatalf("signals sent = %v, want %v", sent, tc.want)
				}
			}
			if _, err := os.Stat(runner.StatePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("process record error = %v, want removed", err)
			}
		})
	}
}

// 发信号之前每一次都重新核对身份:协作关闭之后那个 PID 已经换了主人(我们的 Core
// 退了、PID 被复用),就绝不能再对它发 SIGTERM。没有记录 ⇒ 什么都不做。
func TestStopOrphanedCoreNeverSignalsAProcessItDoesNotOwn(t *testing.T) {
	runner, process, operations := newRecordedProcessRunner(t)
	runner.ShutdownCore = func(context.Context, string, int) error {
		// 同一个 PID,换了一代:那是别人的进程。
		operations.setProcess(Process{PID: process.PID, Executable: process.Executable, UID: 0, Generation: "darwin:999:999"})
		return errors.New("control socket gone")
	}
	runner.SignalProcess = func(int, os.Signal) error {
		t.Fatal("signalled a process whose identity no longer matches the record")
		return nil
	}
	stopped, err := runner.StopOrphanedCore(context.Background())
	if err != nil || !stopped {
		t.Fatalf("StopOrphanedCore = (%v, %v), want (true, nil): our Core is gone", stopped, err)
	}

	stopped, err = runner.StopOrphanedCore(context.Background())
	if err != nil || stopped {
		t.Fatalf("StopOrphanedCore with no record = (%v, %v), want (false, nil)", stopped, err)
	}
}
