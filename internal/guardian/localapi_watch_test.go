package guardian

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestLocalAPI 用本包既有的 fakeController 替身构造一个 localAPI,OwnerUID=0
// (root-only,与既有 mutation 测试的默认写法一致)。status 起手是 Protected,
// 让 TestDownPokesTheStatusGeneration 那次 Down 有真实状态可翻转。
func newTestLocalAPI(t *testing.T) http.Handler {
	t.Helper()
	controller := &fakeController{
		status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected},
	}
	return NewLocalAPI(controller)
}

// withTestOwnerPeer 给请求装上 root peer 凭据 —— 与本包既有 mutation 测试同一
// 手法(`withPeerCredentials(ctx, 0, true)`),对应 newTestLocalAPI 的 OwnerUID=0。
func withTestOwnerPeer(r *http.Request) *http.Request {
	return r.WithContext(withPeerCredentials(r.Context(), 0, true))
}

// 不带 wait 的 GET 与今天行为相同,**但多一个 status_generation 键**。
// 「加个字段而已」正是会顺手把旧客户端弄坏的那类改动,所以两头都钉:
// 键必须在(客户端得先有代际号才能发回来),而且旧解码器不该因此失败。
func TestStatusWithoutWaitStillAnswersImmediatelyAndCarriesTheGeneration(t *testing.T) {
	api := newTestLocalAPI(t)

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d", rec.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解码: %v", err)
	}
	if _, ok := raw["status_generation"]; !ok {
		t.Fatal("应答里没有 status_generation —— 客户端拿不到代际号就没法发起 watch")
	}
	// 旧客户端(不认识这个键的解码器)仍应成功:Go 的 json.Decode 默认忽略未知键。
	var status Status
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("既有解码路径读不动新应答了: %v", err)
	}
	if status.StatusGeneration == 0 {
		t.Error("代际号是 0 —— 发布过至少一次之后它应当 >= 1")
	}
}

// 能力声明:菜单靠它决定走 watch 还是降级轮询。**绝不「试着拨一下看看」。**
func TestStatusWatchIsDeclaredAsACapability(t *testing.T) {
	var found bool
	for _, c := range GuardianCapabilities() {
		if c == CapabilityStatusWatch {
			found = true
		}
	}
	if !found {
		t.Fatalf("GuardianCapabilities() 里没有 %q —— 菜单无从知道这一版支持 watch,"+
			"只能去试拨,而试拨拿到的普通应答与「立刻返回因为变了」无法区分,"+
			"于是会退化成一个满速轮询", CapabilityStatusWatch)
	}
}

// wait 与当前不同 ⇒ 立刻返回。
func TestWaitWithStaleGenerationReturnsImmediately(t *testing.T) {
	api := newTestLocalAPI(t)
	start := time.Now()
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/status?wait=999999", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d", rec.Code)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("代际号不同却挂了 %v —— 比较可能用了 `>` 而不是 `!=`", elapsed)
	}
}

// timeout 参数要被钳住:客户端不许要求比服务端上限更长的挂住 ——
// 那会把关机延迟的上限交给客户端决定。
func TestWaitTimeoutIsClampedToTheServerCeiling(t *testing.T) {
	if got := clampWatchTimeout(9999 * time.Second); got != watchMaxHold {
		t.Errorf("超长 timeout 被钳到 %v,want %v", got, watchMaxHold)
	}
	if got := clampWatchTimeout(0); got != watchMinHold {
		t.Errorf("0 被钳到 %v,want %v(0 会让 watch 变成满速轮询)", got, watchMinHold)
	}
	if got := clampWatchTimeout(-5 * time.Second); got != watchMinHold {
		t.Errorf("负数被钳到 %v,want %v", got, watchMinHold)
	}
	if got := clampWatchTimeout(5 * time.Second); got != 5*time.Second {
		t.Errorf("合法值被改成了 %v", got)
	}
}

// 读不懂的 wait 值不许当成 0(那会让每次请求都立刻返回 = 满速轮询),
// 也不许 500(菜单会以为 Guardian 坏了)。**当成「没带 wait」** —— 立刻返回一次
// 当前状态,客户端拿到真代际号后自然会用对。
func TestUnparsableWaitIsTreatedAsNoWait(t *testing.T) {
	api := newTestLocalAPI(t)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/status?wait=abc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d,want 200 —— 读不懂的参数不该让菜单以为 Guardian 坏了", rec.Code)
	}
}

// **接线守卫。** publisher 全对而没人在 up/down 之后 poke,与没有广播在输出上
// 完全一样(只是慢 3 秒),而这个功能的原始现场正是那两处。
//
// 这里不查源码文本 —— 那类守卫在本仓库被绕过过八次 —— 而是真的打一次
// POST /v1/down,断言代际号动了。
func TestDownPokesTheStatusGeneration(t *testing.T) {
	api := newTestLocalAPI(t)

	before := statusGenerationVia(t, api)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/down", strings.NewReader("{}"))
	api.ServeHTTP(rec, withTestOwnerPeer(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/down 回了 %d: %s", rec.Code, rec.Body.String())
	}
	after := statusGenerationVia(t, api)
	if after == before {
		t.Fatalf("/v1/down 之后代际号仍是 %d —— poke 没接上,"+
			"于是用户敲完 bx down 要等一个兵底间隔(3 秒)才看到图标变", after)
	}
}

func statusGenerationVia(t *testing.T, api http.Handler) uint64 {
	t.Helper()
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	var status Status
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("解码 status: %v", err)
	}
	return status.StatusGeneration
}

// mutation 应答**自己**带的 status_generation 不该是 0。
//
// **与 TestDownPokesTheStatusGeneration 不同的地方**:那条测的是「poke 之后紧跟
// 一次独立的 GET /v1/status 能看到新代际号」——证明的是 poke 接上了 publisher。
// 这条测的是 POST /v1/down **这次响应自己的 body**——`statusWithVersions` today
// 走的是 `statusOf(controller)` 这条与 publisher 完全不相干的路径,从不给
// `StatusGeneration` 赋值,于是它停在 Go 零值 0。字段没有 `omitempty`,「存在
// 但是 0」按字段自己的文档注释意味着「这一版有 watch 这个概念、只是还没发布
// 过」——而这次响应组装之前,同一个 handler 刚刚调用过 watch.poke()、确确实实
// 发布过一次。今天没有消费方读这个字段,但从一次开关响应里播种一次 watch 正是
// 显然的下一步,不该让它从一个假的"还没发布过"起跑。
func TestDownResponseItselfCarriesTheStatusGeneration(t *testing.T) {
	api := newTestLocalAPI(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/down", strings.NewReader("{}"))
	api.ServeHTTP(rec, withTestOwnerPeer(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/down 回了 %d: %s", rec.Code, rec.Body.String())
	}
	var status Status
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("解码: %v", err)
	}
	if status.StatusGeneration == 0 {
		t.Fatalf("/v1/down 应答自己的 status_generation 是 0 —— 这次响应组装之前 " +
			"watch.poke() 已经真的发布过一次,不该回一个「还没发布过」的假象")
	}
}

// /v1/migrate 与 /v1/up、/v1/down 共用同一条 statusWithVersions 出口,同一条
// 纪律。migrate 本身不是广播点(设计明确只有 up/down 是),但它的应答同样不该
// 撒谎说「从没发布过」——publisher 早在 Guardian 起跑、第一次任意一条路径碰到
// 它的时候就已经发布过。
func TestMigrateResponseItselfCarriesTheStatusGeneration(t *testing.T) {
	controller := &fakeController{
		status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected},
	}
	api := NewLocalAPI(controller)
	// 先拨一次 /v1/status,确保 publisher 在这台假 Guardian 上至少发布过一次
	// (与真实 Guardian 的自然状态一致:generation 从 1 开始,不会恒为 0)。
	statusGenerationVia(t, api)

	body := `{"gateway":"192.168.1.1","server_bypass":["10.0.0.1/32"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/migrate", strings.NewReader(body))
	api.ServeHTTP(rec, withTestOwnerPeer(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/migrate 回了 %d: %s", rec.Code, rec.Body.String())
	}
	var status Status
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("解码: %v", err)
	}
	if status.StatusGeneration == 0 {
		t.Fatalf("/v1/migrate 应答自己的 status_generation 是 0 —— 应当带上 publisher " +
			"此刻已经发布过的那个代际号,而不是 statusOf(controller) 这条与 publisher " +
			"不相干的路径留下的零值")
	}
}

// **关机接线守卫。** localAPI.beginShutdown 必须唤醒任何 parked 的长轮询请求 ——
// http.Server.Shutdown 会等在跑的 handler 返回,而 Daemon.Shutdown 正是在
// server.Shutdown **之前**调用 mutations.beginShutdown()(daemon.go:319,
// localAPI 经 mutationLifecycle 接口被塞进 d.mutations)。若 watch 那半没接上,
// 一个 parked 在 25 秒挂住上限里的 waiter 会让整台 Guardian 的关机被它拖住,
// 而这个项目在「关机慢」上栽过 71 分钟(2026-08-04)。
//
// 这里走真实 StartDaemon + 真实 unix socket,而不是直接调用 localAPI 的私有
// 方法:要证明的是 Daemon.Shutdown 那条真实调用链,不是 beginShutdown 自己
// 能不能唤醒 publisher(那条已由 statuswatch_test.go 钉住)。
func TestDaemonShutdownWakesParkedStatusWatch(t *testing.T) {
	controller := &fakeController{
		status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected},
	}
	socketPath := filepath.Join(shortSocketDir(t), "watch-shutdown.sock")
	daemon, err := StartDaemon(context.Background(), DaemonOptions{
		SocketPath: socketPath,
		Handler:    NewLocalAPI(controller),
		OwnerUID:   uint32(os.Geteuid()),
		PeerCredentials: func(net.Conn) (uint32, bool) {
			return 0, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer daemon.Close()

	client := guardianHTTPClient(socketPath)
	defer client.CloseIdleConnections()

	statusReq, err := http.NewRequest(http.MethodGet, "http://local/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	statusResp, err := client.Do(statusReq)
	if err != nil {
		t.Fatal(err)
	}
	var initial Status
	if err := json.NewDecoder(statusResp.Body).Decode(&initial); err != nil {
		t.Fatal(err)
	}
	statusResp.Body.Close()

	// 用一个远大于「beginShutdown 该救的时长」的 timeout(20s < watchMaxHold
	// 25s,合法值不会被钳)长轮询,好让「没接上」与「接上了」在关机耗时上
	// 有明确的区分——若没接上,server.Shutdown 只能等这条请求自然到期或等
	// 关机 ctx 的截止时间,不会是巧合般地碰上一次兵底重算。
	waitURL := fmt.Sprintf("http://local/v1/status?wait=%d&timeout=20", initial.StatusGeneration)
	waitDone := make(chan error, 1)
	go func() {
		waitReq, err := http.NewRequest(http.MethodGet, waitURL, nil)
		if err != nil {
			waitDone <- err
			return
		}
		resp, err := client.Do(waitReq)
		if err != nil {
			waitDone <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			waitDone <- fmt.Errorf("wait 状态码 %d", resp.StatusCode)
			return
		}
		waitDone <- nil
	}()

	// 给长轮询请求一点时间真正 park 到 select 里,再触发关机。
	time.Sleep(100 * time.Millisecond)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	shutdownStart := time.Now()
	if err := daemon.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("daemon shutdown 未在 3 秒内干净完成(parked watch 没被唤醒?): %v", err)
	}
	if elapsed := time.Since(shutdownStart); elapsed > 2*time.Second {
		t.Fatalf("daemon shutdown 耗时 %v —— parked watch 应当被立刻唤醒,不该挂到关机超时附近", elapsed)
	}

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("parked watch 请求失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon 关机已完成,但 parked watch 请求仍未返回")
	}
}
