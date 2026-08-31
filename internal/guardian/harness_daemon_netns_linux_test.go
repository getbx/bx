//go:build integration && linux

// 门开了才可能有的那条断言:**真 RunDaemon 在 netns 里跑起来**。
//
// 它与 Manager 级那几条不重复 —— 那些绕过了 RunDaemon 自己造 Manager,而
// 本仓库全部事故都在组装根:`RunDaemon` 的门、平台清单、Store、LocalAPI 的
// 接线,只有真的调它一次才会被执行到。
package guardian

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// 真 RunDaemon:门放行、平台清单接线可用、LocalAPI socket 起得来并答得出
// 状态。**它刻意不开保护** —— desired 缺省是 off,daemon 起来只是待命。
func TestHarnessRunDaemonServesStatusOnLinux(t *testing.T) {
	enterGuardianNetns(t)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	// owner_uid=0 让本地 API 退回 root-only —— 台子里就是 root,够用;
	// 而写一个假 uid 会让授权判据在一个真实用户不存在的环境里做判断。
	// server 是 config.Parse 的硬要求(server 或 transports 至少一个);
	// daemon 起来并不连它 —— 本测试不开保护。
	if err := os.WriteFile(configPath, []byte("owner_uid: 0\nserver: brook://203.0.113.9:9999/pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(dir, "guardian.sock")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// exited 记住「daemon 已经自己退了」。少了它,提前退出那一支把 done 读空,
	// 收尾再去 <-done 就空等到超时 —— 一次清晰的失败被一条 30 秒的假超时盖住。
	var exited atomic.Bool
	go func() {
		done <- RunDaemon(ctx, DaemonOptions{
			ConfigPath: configPath,
			DNSListen:  "127.0.0.1:53",
			SocketPath: socketPath,
		})
	}()
	t.Cleanup(func() {
		cancel()
		if exited.Load() {
			return
		}
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			// 关机挂住是这个项目栽过 71 分钟的那类事故,台子里也不许放过。
			t.Error("RunDaemon 没有随 ctx 结束 —— 停止路径挂住了")
		}
	})

	// socket 出现即证明门放行、清单接线、LocalAPI 都活了。
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		select {
		case err := <-done:
			exited.Store(true)
			t.Fatalf("RunDaemon 提前退出: %v", err)
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("Guardian socket 没出现: %v", err)
	}

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}}
	resp, err := client.Get("http://local/v1/status")
	if err != nil {
		t.Fatalf("读 /v1/status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/status = %d", resp.StatusCode)
	}
	var status Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("解 Status: %v", err)
	}
	// **保护必须是关着的**:起一个 daemon 不等于开保护,这条与全新安装的
	// 契约同源(CI 的 macos-fresh-install 断言的也是它)。
	if status.Protection == ProtectionProtected {
		t.Fatalf("RunDaemon 起来就把保护打开了: %+v", status)
	}
	// DNS 那一栏必须是 linux 的诚实答案,不是「没查」。
	if status.DNSState == DNSUnknown {
		t.Fatalf("DNS 报 unknown —— linux 上它该是 not_needed(数据面自己管): %+v", status)
	}
}
