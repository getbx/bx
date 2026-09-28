package guardian

import (
	"context"
	"testing"
)

// 上一个 Core 崩溃留下的 bx pf anchor 会把物理网卡上非 root 的公网 TCP 全拒掉,而新
// Core 起来之后一切看起来正常。起 Core 之前先冲。做成 Manager 的一个可替换字段,让
// 「真的在 fork 之前叫了」这一跳可测。
func TestManagerFlushesAStalePFAnchorBeforeStartingCore(t *testing.T) {
	env := newManagerTestEnv(t)
	var flushedBefore []string
	env.manager.flushStalePF = func(context.Context) (bool, error) {
		flushedBefore = append([]string(nil), env.events.snapshot()...)
		return true, nil
	}
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if flushedBefore == nil {
		t.Fatal("startCore must flush a stale bx pf anchor first")
	}
	for _, e := range flushedBefore {
		if e == "core.start" {
			t.Fatalf("the flush must happen before Core is forked, events at flush time: %v", flushedBefore)
		}
	}
}

// 冲不掉不许挡住起 Core:残留的 anchor 是「网坏了」,而不起 Core 是「没保护」,后者更糟;
// 记日志即可。
func TestAFailedStalePFFlushDoesNotBlockCoreStart(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.flushStalePF = func(context.Context) (bool, error) { return false, context.DeadlineExceeded }
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatalf("Up must still start Core, got %v", err)
	}
	started := false
	for _, e := range env.events.snapshot() {
		if e == "core.start" {
			started = true
		}
	}
	if !started {
		t.Fatal("Core was not started")
	}
}

// 组装根:生产的 NewManager 必须接上真的 flushStalePF(复审变异实测:删掉默认值,上面两条
// 照样绿 —— 它们自己注入了字段)。这里只钉「非 nil」,exec pfctl 那一半只有真机能验。
func TestNewManagerWiresTheStalePFFlush(t *testing.T) {
	env := newManagerTestEnv(t)
	m, err := NewManager(env.options())
	if err != nil {
		t.Fatal(err)
	}
	if m.flushStalePF == nil {
		t.Fatal("NewManager must wire flushStalePF; a nil field silently skips the flush before every Core start")
	}
}

// 测试环境必须把它换成空操作:否则每条 Manager 测试都在 exec /sbin/pfctl 并读真机的
// /var/run/bx/pf.token —— 一个真的 token 文件会被测试 `-X` 掉。
func TestManagerTestEnvNeutralizesTheStalePFFlush(t *testing.T) {
	env := newManagerTestEnv(t)
	if env.manager.flushStalePF == nil {
		t.Fatal("env must set a no-op flushStalePF, not nil (nil would also skip, but hides that the env forgot)")
	}
	if flushed, err := env.manager.flushStalePF(context.Background()); flushed || err != nil {
		t.Fatalf("env's flushStalePF must be a no-op, got %v %v", flushed, err)
	}
}
