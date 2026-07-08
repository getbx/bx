package supervisor

import (
	"errors"
	"testing"
	"time"

	"github.com/getbx/bx/internal/dialer"
	"github.com/getbx/bx/internal/tunnel"
)

// blockRunner 是永不自行退出的假子进程(Kill 前 Wait 一直阻塞)。
type blockRunner struct{ done chan struct{} }

func (b *blockRunner) Wait() error { <-b.done; return nil }
func (b *blockRunner) Kill() error {
	select {
	case <-b.done:
	default:
		close(b.done)
	}
	return nil
}

// UDP companion 是"锦上添花"的速度档:挂载它绝不能阻塞主隧道(reality)把 TUN 拉起。
// 旧代码在启动期 waitTunnelHealthy 硬等 UDP 健康(20s),flaky UDP 上行(运营商丢包)会
// 让秒健康的 reality 被反复重启拖死。attachUDPCompanion 必须 best-effort:立即挂载,
// 未健康时由 dialer 的 killswitch fail-closed 兜住 UDP(既有 dialer 测试保证不回落)。
func TestAttachUDPCompanionDoesNotBlockOnHealth(t *testing.T) {
	// 假 UDP 隧道:进程永不退出、健康检查永远失败 → Healthy() 恒 false。
	udpTun := tunnel.New(
		"127.0.0.1:1",
		func(string) (tunnel.Runner, error) { return &blockRunner{done: make(chan struct{})}, nil },
		func(string) (int64, error) { return 0, errors.New("unhealthy") },
	)
	defer udpTun.Stop()

	done := make(chan error, 1)
	go func() { done <- attachUDPCompanion(&dialer.Dialer{}, udpTun, "hysteria2") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("attachUDPCompanion 应成功挂载, got err=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("attachUDPCompanion 阻塞了 >3s——疑似仍在硬等 UDP 健康(回归:UDP 会拖垮主隧道启动)")
	}

	if udpTun.Healthy() {
		t.Fatal("前提失效:假 UDP 隧道不该健康(测试想验证的正是'未健康也立即返回')")
	}
}
