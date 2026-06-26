// mutationengine.go 是常驻守护进程的 commit-confirmed 引擎:confirm.Guard(死手)+
// confirm.Snapshotter(9a 真快照器)的薄编排。改动类操作 Arm 后须在窗口内 Commit,
// 否则 tickLoop 到点自动 revert(undo + 路由快照网)。把 MCP 短命进程里的 armThen
// 搬进守护进程,故 agent 断开后死手仍在。
//
// 9b-1 只交付引擎单元;挂进 Run() 守护循环 / 接控制 socket / 接真实隧道 mutation 是 9b-2/9b-3。
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/getbx/bx/internal/confirm"
)

type mutationEngine struct {
	guard   *confirm.Guard
	snapper confirm.Snapshotter
}

// newMutationEngine 构造引擎。生产用 NewSystemSnapshotter()+240s+time.Now;测试注入 fake。
func newMutationEngine(snapper confirm.Snapshotter, window time.Duration, now func() time.Time) *mutationEngine {
	return &mutationEngine{guard: confirm.New(window, now), snapper: snapper}
}

// Arm:抓快照 → 武装死手(restore = undo + 快照网)→ apply。
// capture 失败 → 不武装、不 apply;apply 失败 → 立即 Rollback、返回错误(不留半截)。
func (e *mutationEngine) Arm(apply, undo func() error) error {
	if apply == nil {
		return errors.New("apply 不能为空")
	}
	if e.guard.State() == confirm.StateArmed {
		return confirm.ErrAlreadyArmed
	}
	snap, err := e.snapper.Capture()
	if err != nil {
		return fmt.Errorf("抓 last-known-good 快照失败,已中止改动: %w", err)
	}
	restore := func() error {
		var errs []error
		if undo != nil {
			if uerr := undo(); uerr != nil {
				errs = append(errs, fmt.Errorf("undo: %w", uerr))
			}
		}
		if rerr := e.snapper.Restore(snap); rerr != nil {
			errs = append(errs, fmt.Errorf("快照还原: %w", rerr))
		}
		return errors.Join(errs...)
	}
	if err := e.guard.Arm(restore); err != nil {
		return err // ErrAlreadyArmed (authoritative check)
	}
	if err := apply(); err != nil {
		if rerr := e.guard.Rollback(); rerr != nil {
			return fmt.Errorf("apply 失败,回滚也失败(系统可能半改动): %w", errors.Join(err, rerr))
		}
		return fmt.Errorf("apply 失败已回滚: %w", err)
	}
	return nil
}

func (e *mutationEngine) Commit() error       { return e.guard.Commit() }
func (e *mutationEngine) Rollback() error      { return e.guard.Rollback() }
func (e *mutationEngine) State() confirm.State { return e.guard.State() }
func (e *mutationEngine) tick() (bool, error)  { return e.guard.Tick() }

// Run 跑 tickLoop:每 2s Tick,未 commit 到点自动 revert,直到 ctx 取消。
func (e *mutationEngine) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = e.tick()
		}
	}
}
