package guardian

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/getbx/bx/internal/supervisor"
)

// StopOrphanedCore 收掉一个 Guardian 已经不在、而它记下的 Core 还活着的进程。
//
// **为什么需要它(2026-09-25)**:此前 Guardian 被 bootout 时 launchd 收掉整个进程组,
// Core 作为子进程顺带死掉 —— 卸载与强制拆除在 Core 不应答时就靠这个副作用收尾。
// Guardian 的 plist 加上 AbandonProcessGroup 之后(Core 的寿命不再绑在 Guardian 进程上,
// 否则 Guardian 一退出 Core 就还原路由、流量直连),那个副作用没了;凡是「用户明确要
// 停掉 bx」的路径都必须**显式**停 Core,否则留下一个没人管的孤儿。
//
// 只认 core-process.json 记下、且身份核对得上的那一个(Existing 的判据):一个手敲的
// `sudo bx run` 调试进程**不在这份记录里**,不会被它杀掉 —— 以前 launchd 收进程组时
// 也碰不到它。
//
// 顺序:先经控制 socket 请它自己退出(它会跑 defer 还原路由),等;不走再 SIGTERM
// (同样走还原),等;还不走才 SIGKILL。每一次发信号之前都重新核对身份,PID 被复用
// 时绝不误杀。返回 true 表示确实停掉了一个。
func (r *ExecCoreRunner) StopOrphanedCore(ctx context.Context) (bool, error) {
	process, err := r.Existing(ctx)
	if err != nil {
		return false, err
	}
	if process.PID <= 0 {
		return false, nil
	}
	shutdown := r.ShutdownCore
	if shutdown == nil {
		shutdown = supervisor.ShutdownControl
	}
	controlSocket := r.ControlSocket
	if controlSocket == "" {
		controlSocket = supervisor.SockPath
	}
	// 协作关闭失败(socket 没了、卡住)不是错误:下面还有信号那两级。
	_ = shutdown(ctx, controlSocket, process.PID)

	wait := r.StopTimeout
	if wait <= 0 {
		wait = defaultCoreStopWait
	}
	for _, step := range []struct {
		signal os.Signal
		wait   time.Duration
	}{
		{nil, wait},
		{syscall.SIGTERM, wait},
		{syscall.SIGKILL, 5 * time.Second},
	} {
		if step.signal != nil {
			gone, err := r.orphanGone(process)
			if err != nil {
				return false, err
			}
			if gone {
				return true, r.clearStoppedOrphanRecord(process)
			}
			if err := r.signalProcess(process.PID, step.signal); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return false, fmt.Errorf("signal orphaned Core PID %d with %v: %w", process.PID, step.signal, err)
			}
		}
		gone, err := r.waitOrphanGone(ctx, process, step.wait)
		if err != nil {
			return false, err
		}
		if gone {
			return true, r.clearStoppedOrphanRecord(process)
		}
	}
	return false, fmt.Errorf("orphaned Core PID %d is still running after SIGKILL", process.PID)
}

func (r *ExecCoreRunner) clearStoppedOrphanRecord(process Process) error {
	if err := r.removeRecordIfGeneration(process.PID, process.Generation); err != nil {
		return fmt.Errorf("clear stopped Core record: %w", err)
	}
	return nil
}

func (r *ExecCoreRunner) signalProcess(pid int, sig os.Signal) error {
	if r.SignalProcess != nil {
		return r.SignalProcess(pid, sig)
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(sig)
}

// orphanGone:进程不在了,或者那个 PID 已经换了主人。问不出来如实报错。
func (r *ExecCoreRunner) orphanGone(process Process) (bool, error) {
	current, err := r.operations().Inspect(process.PID)
	if errors.Is(err, ErrProcessNotRunning) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect orphaned Core PID %d: %w", process.PID, err)
	}
	same, err := sameProcessIdentity(process, current)
	if err != nil {
		return false, fmt.Errorf("compare orphaned Core PID %d identity: %w", process.PID, err)
	}
	return !same, nil
}

func (r *ExecCoreRunner) waitOrphanGone(ctx context.Context, process Process, d time.Duration) (bool, error) {
	waitCtx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	ticker := time.NewTicker(r.inspectInterval(50 * time.Millisecond))
	defer ticker.Stop()
	for {
		gone, err := r.orphanGone(process)
		if err != nil || gone {
			return gone, err
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, nil
		case <-ticker.C:
		}
	}
}
