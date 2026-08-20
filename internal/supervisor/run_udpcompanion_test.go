package supervisor

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/dialer"
	"github.com/getbx/bx/internal/tunnel"
)

// blockRunner 是永不自行退出的假子进程(Kill 前 Wait 一直阻塞)。
//
// **Kill 必须是幂等的,而且不能靠「先查再关」实现。** 真的会有两个 goroutine
// 同时调它:`Tunnel.runOnce` 的 defer 与测试自己 `defer udpTun.Stop()`,而
// `select { case <-done: default: close(done) }` 是一次经典的 check-then-act ——
// 两边都能通过 default 分支、都去 close,于是 `panic: close of closed channel`
// 打死整个测试二进制。**实测 200 轮里复现一次**(`go test -race -count=200
// -run TestAttachUDPCompanion`),而它在全量 `verify.sh` 里表现为**随机一次红**。
//
// 这条修的是**机制**不是那次偶发:`sync.Once` 让「只关一次」成为确定的,
// 与本仓库那条纪律一致 —— 一个会偶发红的闸门比没有闸门更糟,因为它训练人去重跑,
// 而重跑正是「判据是退出码」这条纪律唯一的解毒方式。
type blockRunner struct {
	done chan struct{}
	once sync.Once
}

func (b *blockRunner) Wait() error { <-b.done; return nil }
func (b *blockRunner) Kill() error {
	b.once.Do(func() { close(b.done) })
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
