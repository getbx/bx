// Package confirm 实现 commit-confirmed 死手:改动类操作 Arm 后须在窗口内
// Commit,否则 Tick 到期自动 restore 到 last-known-good。纯逻辑,免 root,
// 时钟可注入。
package confirm

import (
	"errors"
	"sync"
	"time"
)

type State int

const (
	StateIdle State = iota
	StateArmed
	StateCommitted
	StateReverted
)

var (
	ErrNotArmed     = errors.New("guard 未处于 armed 状态")
	ErrAlreadyArmed = errors.New("guard 已 armed,先 Commit 或 Rollback")
)

type Guard struct {
	mu       sync.Mutex
	window   time.Duration
	now      func() time.Time
	state    State
	deadline time.Time
	restore  func() error
}

func New(window time.Duration, now func() time.Time) *Guard {
	return &Guard{window: window, now: now, state: StateIdle}
}

func (g *Guard) Arm(restore func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state == StateArmed {
		return ErrAlreadyArmed
	}
	g.state = StateArmed
	g.restore = restore
	g.deadline = g.now().Add(g.window)
	return nil
}

func (g *Guard) Commit() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != StateArmed {
		return ErrNotArmed
	}
	g.state = StateCommitted
	g.restore = nil
	return nil
}

func (g *Guard) Rollback() error {
	g.mu.Lock()
	if g.state != StateArmed {
		g.mu.Unlock()
		return ErrNotArmed
	}
	fn := g.restore
	g.state = StateReverted
	g.restore = nil
	g.mu.Unlock()
	return fn()
}

// Tick 由后台循环周期调用;到期且仍 Armed 时自动 restore。
func (g *Guard) Tick() (bool, error) {
	g.mu.Lock()
	if g.state != StateArmed || g.now().Before(g.deadline) {
		g.mu.Unlock()
		return false, nil
	}
	fn := g.restore
	g.state = StateReverted
	g.restore = nil
	g.mu.Unlock()
	return true, fn()
}

func (g *Guard) State() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}
