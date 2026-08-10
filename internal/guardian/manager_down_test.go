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

// 系统里还有 Core 在跑时,Down 必须**如实说没能关掉**,而不是报 off。
//
// 这是本改动的全部理由:Existing() 读的是 Guardian 自己的记账,而 legacy Core、
// sudo bx run 起的 Core、以及手删过 core-process.json 的机器上,记账里都没有它。
func TestDownDoesNotClaimOffWhileACoreIsStillRunning(t *testing.T) {
	// 走完整拆除那条路(desired=on + 记账里有 Core),而不是提前返回那条
	// —— 提前返回由 TestDownEarlyExitAlsoAsksTheSystem 单独覆盖。
	env := newProtectedManagerTestEnv(t)
	env.dns.record = true
	env.runner.scanResult = []Process{{PID: 4242}}

	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("扫到 Core 不该让停止失败: %v", err)
	}
	status := env.manager.Status()
	if status.Protection == ProtectionOff {
		t.Fatal("保护还开着时不许报 off")
	}
	if status.LastError != "core_still_running" {
		t.Errorf("理由要说清, got %q", status.LastError)
	}
	// **拆除必须照常做完** —— 「没能确认」不是「什么都没做」。
	env.assertBarrierRemoved(t)
	env.assertDNSRestored(t)
	env.assertDesiredOffPersisted(t)
}

// 扫描失败不许让停止失败(2026-08-04)。
func TestDownStillSucceedsWhenTheScanFails(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	env.dns.record = true
	env.runner.scanErr = errors.New("sysctl 挂了")

	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("扫描失败绝不能让 Down 失败 —— 那正是 71 分钟事故那条不变量: %v", err)
	}
	status := env.manager.Status()
	if status.Protection == ProtectionOff {
		t.Error("问不出来时不许声称已关闭")
	}
	if status.LastError != "core_scan_failed" {
		t.Errorf("「问不出来」与「确知还在」是两回事,理由不许塌缩, got %q", status.LastError)
	}
	env.assertBarrierRemoved(t)
	env.assertDNSRestored(t)
	env.assertDesiredOffPersisted(t)
}

// 提前返回那条路(desired 已 off 且记账里没 Core)同样是记账,同样要求证。
func TestDownEarlyExitAlsoAsksTheSystem(t *testing.T) {
	env := newManagerTestEnv(t)
	// 提前返回的两个前提:desired 已经是 off、记账里没有 Core。后者是
	// newManagerTestEnv 的初始状态(从没 Up 过)。
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	env.events.reset()
	env.runner.scanResult = []Process{{PID: 4242}}

	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 证明这一条走的确实是提前返回:那条路不装屏障,也不停任何 Core。
	if events := env.events.snapshot(); containsEvent(events, "barrier.install") {
		t.Fatalf("test setup: 这一条要覆盖提前返回那条路,不该走到装屏障, events=%v", events)
	}
	if env.manager.Status().Protection == ProtectionOff {
		t.Fatal("这条路也不许只凭记账就说 off")
	}
	if got := env.manager.Status().LastError; got != "core_still_running" {
		t.Errorf("理由要说清, got %q", got)
	}
}

// **回归**:干净机器仍然报 off。否则这条修复自己成了噪声源,
// 而被训练成忽略 needs_attention 的用户,下次真出事也会忽略。
//
// 两条出口都要覆盖 —— 只守住一条的话,另一条把每一次正常关闭都变成告警,
// 而这个用例照样绿。
func TestDownStillReportsOffOnAHealthyMachine(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  func(*testing.T) *managerTestEnv
	}{
		{name: "完整拆除", env: newProtectedManagerTestEnv},
		{name: "提前返回", env: newManagerTestEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := tc.env(t)
			if err := env.manager.Down(context.Background()); err != nil {
				t.Fatal(err)
			}
			status := env.manager.Status()
			if status.Protection != ProtectionOff {
				t.Fatalf("干净机器必须仍然报 off, got %q (last_error=%q)", status.Protection, status.LastError)
			}
			if status.LastError != "" {
				t.Errorf("干净机器不该留下失败码, got %q", status.LastError)
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

// Recover 在 Guardian **每次重启**时都会跑,而 desired==off 那一支此前只凭账本
// 就发 ProtectionOff。后果比 Down 那一跳更严重:它会把上一次 Down 好不容易向系统
// 求证出来的判断**抹掉**,换成一句没求证过的 off —— 诚实的答案不耐重启。
func TestRecoverDoesNotClaimOffWhileACoreIsStillRunning(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	env.runner.scanResult = []Process{{PID: 4242}}

	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatalf("扫到 Core 不该让启动恢复失败: %v", err)
	}
	if got := env.manager.Status().Protection; got == ProtectionOff {
		t.Fatal("重启后不许只凭账本就说 off —— 那会抹掉上一次求证过的判断")
	}
}

// **最要紧的一条**:没能确认时,关闭的路绝不能被堵死。
//
// recoveryBlocked 为真时 Manager.Down 的第一句就返回 errRecoveryIncomplete,
// 而那正是 2026-08-04「用户 71 分钟关不掉保护」的机制。这一期新增的「没能确认」
// 分支若顺手把它留成 true,就等于用一条新不变量把老不变量撞碎。
func TestRecoverKeepsShutdownReachableWhenItCannotConfirm(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	env.runner.scanErr = errors.New("sysctl 挂了")

	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatalf("扫描失败不该让启动恢复失败: %v", err)
	}
	if env.manager.recoveryBlocked {
		t.Fatal("「没能确认关掉了」绝不能顺带把关闭的路堵死 —— 那是 71 分钟事故的机制")
	}
	// 求证过一次:Down 现在应当仍然可达。
	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatalf("恢复之后 Down 必须仍然可用: %v", err)
	}
}

// 回归:干净机器上重启后仍然报 off,否则每次开机都是一条告警。
func TestRecoverStillReportsOffOnAHealthyMachine(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.manager.Status().Protection; got != ProtectionOff {
		t.Fatalf("干净机器重启后必须仍报 off, got %q", got)
	}
}

type panickingScanner struct{ CoreRunner }

func (panickingScanner) ScanRunning() ([]Process, error) { panic("sysctl 返回了畸形数据") }

// 扫描 panic 不许打死 Guardian。
//
// Down 经 HTTP handler 进来,net/http 会兜住;但 Recover 跑在
// trackStartupRecovery 的裸 goroutine 里 —— 一次 panic 打死整个进程,
// 而 launchd 的 KeepAlive 会把它拉起来再跑 Recover 再 panic:崩溃循环。
// 把扫描接进 Recover 的同时,必须把这个失败模式一起收掉。
func TestConfirmCoreStoppedSurvivesAPanickingScanner(t *testing.T) {
	m := &Manager{runner: panickingScanner{}}
	stopped, reason := m.confirmCoreStopped()
	if stopped {
		t.Fatal("panic 之后不许声称已关闭")
	}
	if reason != "core_scan_failed" {
		t.Errorf("panic 应收成「没能确认」, got %q", reason)
	}
}

// 端到端:Recover 遇上 panic 的扫描器仍然正常返回,且不堵死关闭的路。
func TestRecoverSurvivesAPanickingScanner(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	env.manager.runner = panickingScanner{CoreRunner: env.runner}

	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatalf("扫描 panic 不该让启动恢复失败: %v", err)
	}
	if env.manager.recoveryBlocked {
		t.Fatal("也不该堵死关闭的路")
	}
}
