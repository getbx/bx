package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/getbx/bx/internal/guardian"
)

// watchBackoffMax 是重连退避的上限。
//
// **上限不是礼貌,是正确性。** 无上限的指数退避在 int64 上会溢出回绕
// (阶段③a 那条退避断言就是被溢出架空的:round 54 回绕成 0),而回绕成 0
// 意味着满速重连。
const watchBackoffMax = 30 * time.Second

// watchClientTimeout 是每次长轮询调用的客户端超时。
//
// **它必须大于服务端的挂住上限 guardian.WatchMaxHold(25s)**——否则拿到的
// 永远是自己的超时,而服务端那个上限一次都不会生效(switchServer/probeServers
// 的注释里已经踩过并写下过同一个坑)。这个不等式由
// TestWatchClientTimeoutExceedsServerHold 钉住。
const watchClientTimeout = 40 * time.Second

// watchIdleDelay 是「服务端秒回、代际号没变」这个分支的 floor 延迟。
//
// 一台遵守协议的服务端应该已经挂住到最多 guardian.WatchMaxHold(25s)才回,
// 所以这个 floor 正常情况下不会真的被等到。它是纵深防御,防的是**真机验证
// 撞上过的真实场景**:本机正在跑的 Guardian 是这个功能上线之前的旧二进制,
// 不认 `wait=` 参数——GET /v1/status 对任何请求都秒回,响应里连
// status_generation 字段都没有(旧 Status 结构没这个字段),解出来是零值,
// 与客户端起始的 generation=0 恰好相等,于是"未变化"分支被命中,而且没有
// 任何错误可供 watchBackoff 介入。没有这个 floor,客户端会以满速一直打本机
// unix socket——真机实测 CPU 常驻 26%~46%、吞吐上千次/秒。
const watchIdleDelay = time.Second

// watchBackoff 是连续第 n 次失败之后该等多久。n==0(刚成功/刚开始)不等。
//
// 先乘后钳会溢出,所以**先判轮次**:超过阈值直接返回上限,不去算那个会回绕的
// 乘法。
func watchBackoff(consecutiveFailures int) time.Duration {
	if consecutiveFailures <= 0 {
		return 0
	}
	if consecutiveFailures > 10 {
		return watchBackoffMax
	}
	delay := time.Second << (consecutiveFailures - 1)
	if delay > watchBackoffMax {
		return watchBackoffMax
	}
	return delay
}

// statusWatchLoop 挂在 Guardian 的 /v1/status?wait= 上,每次状态变化打印一次。
//
// **它是这个功能唯一的只读真机验证手段**:菜单那一半要重装 App 才能验,
// 而这一条只要一个终端。它也是唯一能长时间观察「投影够不够安静」的工具 ——
// 稳态下它应当几乎不吐。
//
// 只读、不改任何东西:不联网(只连本机 unix socket)、不写配置、不做任何
// mutation 调用。
func statusWatchLoop(ctx context.Context, out io.Writer, asJSON bool) error {
	client := guardian.NewClient(guardian.SocketPath)
	return statusWatchLoopWith(ctx, out, asJSON, client.StatusWatch, watchBackoff, watchIdleDelay)
}

// statusWatchLoopWith 是 statusWatchLoop 的可注入核心:watch、backoff、
// idleDelay 都是参数,好让循环本身(打印时机、心跳静默、失败重试、代际号
// 推进、不变化响应的节流)免 root、免真实 socket、免真实等待地单测——与
// 本包 readClientStatusReportWithObserver 同一个套路(公开入口固定依赖,
// 内部函数注入依赖以便测试)。
func statusWatchLoopWith(
	ctx context.Context,
	out io.Writer,
	asJSON bool,
	watch func(context.Context, uint64) (guardian.Status, error),
	backoff func(int) time.Duration,
	idleDelay time.Duration,
) error {
	var generation uint64
	failures := 0
	for {
		if delay := backoff(failures); delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil
			}
		}
		// 客户端超时必须比服务端挂住上限长,否则拿到的永远是自己的超时
		// (watchClientTimeout 的注释里已经写明)。
		callCtx, cancel := context.WithTimeout(ctx, watchClientTimeout)
		status, err := watch(callCtx, generation)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			failures++
			fmt.Fprintf(out, "watch 断开(第 %d 次):%v\n", failures, err)
			continue
		}
		failures = 0
		if status.StatusGeneration == generation {
			// 服务端超时,状态没变。**这一条不打印** —— 它是心跳,
			// 打出来会把真正的变化淹掉,而那正是这个工具存在的全部理由。
			//
			// 但也不能裸 continue:一台不遵守协议、秒回的服务端(见
			// watchIdleDelay 的注释)会让这条分支被命中到满速,把本机
			// unix socket 打爆。idleDelay 是 floor,对遵守协议的服务端
			// 而言不会真的被等到。
			if idleDelay > 0 {
				select {
				case <-time.After(idleDelay):
				case <-ctx.Done():
					return nil
				}
			}
			continue
		}
		generation = status.StatusGeneration
		if err := printWatchedStatus(out, status, asJSON); err != nil {
			return err
		}
	}
}

// printWatchedStatus 打印一次状态变化。
//
// JSON 模式整份 Status 一行(NDJSON,便于 jq 逐行处理);人面模式只打一行
// 摘要:代际号 + protection_state + desired + 时刻。**不把整个 Status 人眼
// 输出打出来**——这个工具的用途是数它吐了几次,每次吐一屏字段会让那件事
// 没法做。
func printWatchedStatus(out io.Writer, status guardian.Status, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetEscapeHTML(false)
		return enc.Encode(status)
	}
	_, err := fmt.Fprintf(out, "[%s] generation=%d protection=%s desired=%s\n",
		time.Now().Format(time.RFC3339), status.StatusGeneration, status.Protection, status.Desired)
	return err
}
