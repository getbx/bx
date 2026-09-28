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
