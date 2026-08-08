package guardian

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/getbx/bx/internal/supervisor"
)

// TestManagerDownFallsBackToBlockOnlyBarrierWhenGatewayUnavailable covers the
// real-world failure this bug fix exists for: network trouble means the
// default gateway can't be resolved, which is exactly when the user most
// needs `bx down` to work. Down must degrade to a block-only barrier (no
// ServerBypass, IPv6 forced blocked) and keep tearing down, mirroring what
// installBarrierForRecovery already does elsewhere in this package — not
// fail the whole operation and strand the user with no way to turn Guardian
// off.
func TestManagerDownFallsBackToBlockOnlyBarrierWhenGatewayUnavailable(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.manager.barrierContext.Gateway = ""
	env.manager.gatewayProvider = &fakeGatewayProvider{err: errors.New("default gateway not found")}
	if len(env.manager.runtime.ServerBypass) == 0 {
		t.Fatal("test setup: runtime ServerBypass is empty, want non-empty to exercise gateway resolution")
	}

	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("无网关时 Down 必须成功降级,实际返回错误: %v", err)
	}

	installed := env.barrier.lastInstallContext()
	if len(installed.ServerBypass) != 0 {
		t.Errorf("installed barrier ServerBypass = %v, want empty (block-only) when gateway unresolvable", installed.ServerBypass)
	}
	if !installed.blockOnly {
		t.Error("installed barrier blockOnly = false, want true when gateway unresolvable")
	}
	if !installed.BlockIPv6 {
		t.Error("installed barrier BlockIPv6 = false, want true (block-only forces IPv6 block)")
	}

	status := env.manager.Status()
	if status.Desired != DesiredOff || status.Protection != ProtectionOff {
		t.Fatalf("status = %+v, want fully torn down to off", status)
	}
}

// Core 从没成功起来过时,runtime 里没有 server bypass —— 而这恰恰是用户最需要
// 关掉保护的时刻。
//
// 真机 2026-08-06:VPS 不通 → 隧道连不上 → "core health check timed out after 20s"
// → Core 的控制 socket 从未出现 → runtime 为空 → bx down 报
// 500 barrier_install_failed("install down barrier: server bypass required")
// → 用户看到一屏红字,最后只能 uninstall。
//
// 网关那条路径早就降级了(见上一个用例),但降级挂在了错误的失败点上:
// barrierContextForRuntime 这时是**成功**返回的,只是产出的 context 里
// ServerBypass 为空,要到 installBarrier 里才被 validateBarrierContext 拒掉。
// 关闭路径不得依赖「Core 必须先成功起来过」这个前置条件。
func TestManagerDownFallsBackToBlockOnlyBarrierWhenServerBypassMissing(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	// Core 从没把 runtime 交回来过,且健康检查一直超时 —— 与真机一致。
	env.manager.runtime = supervisor.RuntimeState{}
	env.manager.barrierContext.ServerBypass = nil
	env.health.err = errors.New("core health check timed out after 20s")

	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("拿不到 server bypass 时 Down 必须降级为 block-only 继续拆除,实际返回错误: %v", err)
	}

	installed := env.barrier.lastInstallContext()
	if !installed.blockOnly {
		t.Error("installed barrier blockOnly = false, want true —— 没有 bypass 就只能装 block-only")
	}
	if len(installed.ServerBypass) != 0 {
		t.Errorf("installed barrier ServerBypass = %v, want empty", installed.ServerBypass)
	}
	if !installed.BlockIPv6 {
		t.Error("block-only 屏障必须强制阻断 IPv6")
	}

	status := env.manager.Status()
	if status.Desired != DesiredOff || status.Protection != ProtectionOff {
		t.Fatalf("status = %+v, want fully torn down to off", status)
	}
	if status.LastError == "barrier_install_failed" {
		t.Error("降级成功后不得留下 barrier_install_failed")
	}
}

// 用户明确关掉保护之后,升级欠条必须销账 —— 而且必须在**菜单走的那条路**上。
//
// 上一轮把销账挂在 internal/cli 的 macOSDownAction 上,以为菜单的 Turn Off 会
// 经过它。它不会:菜单直接连 Guardian socket,POST /v1/down → controller.Down,
// 全程不碰 CLI(ToggleController 只有在 socket **失败**时才回落到
// privilegedCLIDown)。于是钩子恰好只在罕见的逃生路径上生效。
//
// 这个用例走真实的 LocalAPI handler + 真实的 *Store,证明的是那条路本身:
// 若销账不在 Manager.Down 里,请求成功而欠条仍在。
func TestDownOverTheLocalAPISocketClearsTheUpgradeIntent(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	if err := env.store.SaveUpgradeIntent(true); err != nil {
		t.Fatal(err)
	}
	if _, present := env.store.LoadUpgradeIntent(); !present {
		t.Fatal("test setup: 欠条应当已存在")
	}

	// 菜单栏的 Turn Off 就是这一跳(GuardianClient.turnOff → POST /v1/down)。
	request := httptest.NewRequest(http.MethodPost, "/v1/down", nil)
	request = request.WithContext(withPeerCredentials(request.Context(), 0, true))
	recorder := httptest.NewRecorder()
	NewLocalAPI(env.manager).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /v1/down = %d, body=%s", recorder.Code, recorder.Body.String())
	}

	if pending, present := env.store.LoadUpgradeIntent(); present {
		t.Fatalf("经 socket 关闭保护之后,欠条必须销账(否则下次 Repair 会违背用户意愿开回保护),实际 pending=%v", pending)
	}
}

// 升级自己的那次停保护不得销掉它前一秒才写下的欠条。
//
// app-install 的顺序是「写欠条 → 停保护 → 换文件 → 重启 Guardian → 起保护」。
// 上一轮里第二步走的是普通 Down,而 Manager.Down 把每一次成功的关闭都当作
// 「用户不要保护了」销账 —— 欠条在写下的**下一步**就被删掉,于是换文件一失败,
// 重试既读到 desired=off 又找不到欠条,「成功」地把机器永久留在无保护状态。
//
// 两条路都跑真实的 Client → 真实 unix socket → 真实 LocalAPI → 真实
// Manager.Down → 真实 *Store:标记是否被客户端带上、handler 是否认得、
// Manager 是否照办,任何一环断了这个用例都会红。
func TestDownForUpgradeKeepsTheUpgradeIntentThatUserDownClears(t *testing.T) {
	for _, tc := range []struct {
		name        string
		down        func(context.Context, *Client) (Status, error)
		wantPresent bool
		why         string
	}{
		{
			name:        "用户明确关闭",
			down:        func(ctx context.Context, c *Client) (Status, error) { return c.Down(ctx) },
			wantPresent: false,
			why:         "用户明确说 off 之后,旧欠条必须销账,否则下次 Repair 会违背用户意愿开回保护",
		},
		{
			name:        "升级停保护",
			down:        func(ctx context.Context, c *Client) (Status, error) { return c.DownForUpgrade(ctx) },
			wantPresent: true,
			why:         "升级把停保护当作自己的一步,欠条是中途失败后唯一知道「还欠一次恢复保护」的东西",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newProtectedManagerTestEnv(t)
			if err := env.store.SaveUpgradeIntent(true); err != nil {
				t.Fatal(err)
			}
			socketPath := filepath.Join(shortSocketDir(t), "guard.sock")
			daemon, err := StartDaemon(context.Background(), DaemonOptions{
				SocketPath:      socketPath,
				Handler:         NewLocalAPI(env.manager),
				OwnerUID:        uint32(os.Geteuid()),
				PeerCredentials: func(net.Conn) (uint32, bool) { return 0, true },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer daemon.Close()

			if _, err := tc.down(context.Background(), NewClient(socketPath)); err != nil {
				t.Fatalf("停保护失败: %v", err)
			}
			if _, present := env.store.LoadUpgradeIntent(); present != tc.wantPresent {
				t.Fatalf("停保护之后欠条 present=%v,want %v —— %s", present, tc.wantPresent, tc.why)
			}
		})
	}
}

// Down 失败时不得销账:保护可能还开着,欠条仍然有意义。
func TestFailedDownKeepsTheUpgradeIntent(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	if err := env.store.SaveUpgradeIntent(true); err != nil {
		t.Fatal(err)
	}
	env.store.setLoadError(errors.New("desired state unreadable"))

	if err := env.manager.Down(context.Background()); err == nil {
		t.Fatal("test setup: 这一步应当失败")
	}
	if _, present := env.store.LoadUpgradeIntent(); !present {
		t.Fatal("Down 失败时欠条必须留着 —— 保护可能还开着")
	}
}
