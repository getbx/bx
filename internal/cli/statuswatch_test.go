package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getbx/bx/internal/guardian"
)

// 退避:连续失败要越等越久,但有上限 —— 无上限的指数退避在 int64 上会溢出回绕
// (阶段③a 那条退避上限断言就是被溢出架空的),而回绕成 0 意味着满速重连。
func TestWatchBackoffGrowsThenCaps(t *testing.T) {
	if got := watchBackoff(0); got != 0 {
		t.Errorf("第一次不该等,got %v", got)
	}
	first := watchBackoff(1)
	second := watchBackoff(2)
	if !(first > 0 && second > first) {
		t.Errorf("退避没有增长:%v → %v", first, second)
	}
	// **上限必须在极大轮次上也成立。** 用一个大到会让未加保护的实现溢出的值。
	if got := watchBackoff(1000); got > watchBackoffMax {
		t.Errorf("第 1000 次退避是 %v,超过上限 %v —— 无上限的指数退避会溢出回绕成 0,"+
			"而那意味着满速重连", got, watchBackoffMax)
	}
	if got := watchBackoff(1000); got <= 0 {
		t.Errorf("第 1000 次退避是 %v —— 已经回绕了", got)
	}
}

// 客户端超时必须比服务端挂住上限(guardian.WatchMaxHold=25s)长,否则拿到的
// 永远是自己的超时,而服务端那个上限一次都不会生效(switchServer/probeServers
// 的注释里已经踩过并写下过同一个坑)。guardian.watchMaxHold 本身未导出,
// client.go 里补了一个导出别名 guardian.WatchMaxHold 专供这条不等式使用。
func TestWatchClientTimeoutExceedsServerHold(t *testing.T) {
	if watchClientTimeout <= guardian.WatchMaxHold {
		t.Fatalf("watchClientTimeout=%v 必须大于 guardian.WatchMaxHold=%v,"+
			"否则服务端的挂住上限永远不会被触发", watchClientTimeout, guardian.WatchMaxHold)
	}
}

// JSON 模式打整份 Status,且必须是单行(NDJSON,便于 jq 逐行处理)——
// 打印多行会让"数它吐了几次"这件事没法做。
func TestPrintWatchedStatusJSONIsSingleLine(t *testing.T) {
	status := guardian.Status{
		StatusGeneration: 7,
		Protection:       "protected",
		Desired:          guardian.DesiredOn,
	}
	var buf bytes.Buffer
	if err := printWatchedStatus(&buf, status, true); err != nil {
		t.Fatalf("printWatchedStatus 出错:%v", err)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("JSON 模式必须一行一份(NDJSON),实际:%q", out)
	}
	var decoded guardian.Status
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &decoded); err != nil {
		t.Fatalf("输出不是合法 JSON:%v,原文:%q", err, out)
	}
	if decoded.StatusGeneration != 7 {
		t.Errorf("StatusGeneration = %d, want 7", decoded.StatusGeneration)
	}
}

// 人面模式只打一行摘要:代际号 + protection_state + desired + 时刻。
// 不许把整个 Status 人眼输出打出来——这个工具的用途是数它吐了几次,
// 每次吐一屏会让那件事没法做。
func TestPrintWatchedStatusHumanModeIsOneLineSummary(t *testing.T) {
	status := guardian.Status{
		StatusGeneration: 3,
		Protection:       "needs_attention",
		Desired:          guardian.DesiredOn,
	}
	var buf bytes.Buffer
	if err := printWatchedStatus(&buf, status, false); err != nil {
		t.Fatalf("printWatchedStatus 出错:%v", err)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("人面模式必须只打一行,实际:%q", out)
	}
	for _, want := range []string{"3", "needs_attention", "on"} {
		if !strings.Contains(out, want) {
			t.Errorf("人面摘要缺少 %q,实际:%q", want, out)
		}
	}
}

// 代际号变化时打印;代际号未变(服务端超时心跳)时**不打印**——它是心跳,
// 打出来会把真正的变化淹掉,而这正是这个工具存在的全部理由。
func TestStatusWatchLoopPrintsOnChangeSkipsUnchanged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int
	var seenGenAtCall2 uint64
	watch := func(_ context.Context, generation uint64) (guardian.Status, error) {
		calls++
		switch calls {
		case 1:
			return guardian.Status{StatusGeneration: 1, Protection: "protected", Desired: guardian.DesiredOn}, nil
		case 2:
			seenGenAtCall2 = generation
			// 服务端超时,状态未变:同一个代际号原样返回。
			return guardian.Status{StatusGeneration: 1, Protection: "protected", Desired: guardian.DesiredOn}, nil
		case 3:
			return guardian.Status{StatusGeneration: 2, Protection: "needs_attention", Desired: guardian.DesiredOn}, nil
		default:
			cancel()
			return guardian.Status{}, ctx.Err()
		}
	}

	var buf bytes.Buffer
	err := statusWatchLoopWith(ctx, &buf, false, watch, func(int) time.Duration { return 0 }, 0)
	if err != nil {
		t.Fatalf("statusWatchLoopWith 返回 %v,want nil(context 取消应静默退出)", err)
	}
	if seenGenAtCall2 != 1 {
		t.Errorf("第 2 次调用时应把上次拿到的代际号 1 传回去,实际 %d", seenGenAtCall2)
	}
	trimmed := strings.TrimRight(buf.String(), "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) != 2 {
		t.Fatalf("打印了 %d 行,want 2(第 2 次心跳不该打印):\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "1") {
		t.Errorf("第一行没有代际号 1:%q", lines[0])
	}
	if !strings.Contains(lines[1], "2") {
		t.Errorf("第二行没有代际号 2:%q", lines[1])
	}
}

// 失败要打印"watch 断开(第 N 次)"并退避重连;成功一次之后失败计数清零,
// 下一轮失败重新从第 1 次算起。
func TestStatusWatchLoopRetriesOnErrorThenRecovers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int
	watch := func(_ context.Context, _ uint64) (guardian.Status, error) {
		calls++
		switch calls {
		case 1, 2:
			return guardian.Status{}, errors.New("dial unix: connection refused")
		case 3:
			return guardian.Status{StatusGeneration: 1, Protection: "protected", Desired: guardian.DesiredOn}, nil
		default:
			cancel()
			return guardian.Status{}, ctx.Err()
		}
	}

	var backoffCalls []int
	backoff := func(n int) time.Duration {
		backoffCalls = append(backoffCalls, n)
		return 0
	}

	var buf bytes.Buffer
	if err := statusWatchLoopWith(ctx, &buf, false, watch, backoff, 0); err != nil {
		t.Fatalf("statusWatchLoopWith 返回 %v,want nil", err)
	}

	out := buf.String()
	if !strings.Contains(out, "第 1 次") {
		t.Errorf("没有打印第 1 次失败提示:%q", out)
	}
	if !strings.Contains(out, "第 2 次") {
		t.Errorf("没有打印第 2 次失败提示:%q", out)
	}
	if strings.Count(out, "\n") != 3 { // 2 条失败提示 + 1 条状态行
		t.Fatalf("打印了 %d 行,want 3:%q", strings.Count(out, "\n"), out)
	}

	// backoff 依次拿到:第一次调用前 0(刚开始);第一次失败后以 failures=1 请求下一轮延迟;
	// 第二次失败后以 failures=2;成功后清零,若还有下一轮失败会重新从 0 起。
	want := []int{0, 1, 2, 0}
	if !reflect.DeepEqual(backoffCalls, want) {
		t.Errorf("backoff 调用序列 = %v, want %v", backoffCalls, want)
	}
}

// 在 backoff 等待期间(delay > 0)ctx 被取消时,循环必须立刻返回,不许等满
// 那段延迟——这正是死手/Ctrl-C 场景要保住的响应性。
func TestStatusWatchLoopRespondsToContextCancelDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	watch := func(_ context.Context, _ uint64) (guardian.Status, error) {
		return guardian.Status{}, errors.New("boom")
	}
	backoff := func(n int) time.Duration {
		if n >= 1 {
			cancel()
			// 若循环没有响应 ctx.Done(),它会真的等一小时——测试靠外层超时兜底。
			return time.Hour
		}
		return 0
	}
	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- statusWatchLoopWith(ctx, &buf, false, watch, backoff, 0) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("statusWatchLoopWith 返回 %v,want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("statusWatchLoopWith 在 backoff 等待期间没有响应 ctx 取消,可能挂住")
	}
}

// **真机验证撞上的真 bug**:本机正在跑的 Guardian 是 Tasks 1-3 之前的旧二进制,
// 不认 `wait=` 参数——GET /v1/status 对任何请求都秒回,而响应里根本没有
// status_generation 字段(旧 Status 结构没有这个字段),JSON 解出来的零值与
// 客户端起始的 generation=0 恰好相等。旧实现里"未变化"分支是裸 `continue`,
// 于是对着这样的服务端会以满速一直打 unix socket:真机实测 payload 是 CPU
// 常驻 26%~46%、每次调用 <1ms、吞吐上千次/秒。这不是"投影漏了一个易变字段"
// 那种噪声(brief 预想的失败模式),而是完全不同的另一种失败:响应快到
// 不像是长轮询在生效,同时又没有任何错误可供 backoff 介入。
//
// 修法是给"未变化、无错误"这条分支也加一个 floor 延迟(watchIdleDelay,
// 生产用 1s)——对一台真正遵守协议的服务端(挂住最多 25s 才回)这个 floor
// 永远不会被真正等到;它是防一台不遵守协议的服务端(旧版本、未来的 bug、
// 中间代理剥离了 query string……)的纵深防御。
func TestStatusWatchLoopThrottlesInstantUnchangedResponses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	watch := func(_ context.Context, generation uint64) (guardian.Status, error) {
		n := atomic.AddInt32(&calls, 1)
		if n > 5 {
			cancel()
		}
		// 模拟不认 wait= 的旧服务端:秒回、代际号原样弹回(恒为调用方传入的值,
		// 对旧 Guardian 而言实际上恒为 0,因为响应里根本没有这个字段)。
		return guardian.Status{StatusGeneration: generation}, nil
	}

	const floor = 50 * time.Millisecond
	var buf bytes.Buffer
	start := time.Now()
	err := statusWatchLoopWith(ctx, &buf, false, watch, func(int) time.Duration { return 0 }, floor)
	if err != nil {
		t.Fatalf("statusWatchLoopWith 返回 %v,want nil(context 取消应静默退出)", err)
	}
	elapsed := time.Since(start)

	// 5 次调用之间应各自等过一次 floor;若没有节流,这 5 次会在微秒内打完,
	// elapsed 会远小于 floor 的整数倍。
	if elapsed < 4*floor {
		t.Fatalf("elapsed=%v,过快 —— 服务端秒回且代际号不变时,客户端必须有 floor "+
			"节流,否则会像真机撞上的那样满速打爆本机 socket(want >= %v)", elapsed, 4*floor)
	}
	if buf.Len() != 0 {
		t.Errorf("unchanged 分支不该打印任何东西,实际:%q", buf.String())
	}
}
