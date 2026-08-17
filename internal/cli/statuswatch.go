package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
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
// **这是第二道防线,不是主要机制** —— 主要机制是 requireStatusWatchCapability
// 那道能力门控(见下)。一台真正认得 `wait=` 的服务端应该已经挂住到最多
// guardian.WatchMaxHold(25s)才回,所以这个 floor 正常情况下不会真的被等到。
// 它兜的是「第一道门被绕过,或者对面明明声明了 status_watch 能力却仍然回一份
// 没有代际号推进的应答」这种理论上不该发生、但不该让客户端付出满速空转代价的
// 情形。
//
// 它存在的直接理由是**真机验证撞上过的真实场景**:本机正在跑的 Guardian 是
// 这个功能上线之前的旧二进制,不认 `wait=` 参数——GET /v1/status 对任何请求
// 都秒回,响应里连 status_generation 字段都没有(旧 Status 结构没这个字段),
// 解出来是零值,与客户端起始的 generation=0 恰好相等,于是"未变化"分支被
// 命中,而且没有任何错误可供 watchBackoff 介入,没有这个 floor 就会以满速
// 一直打本机 unix socket(真机实测 CPU 常驻 26%~46%、吞吐上千次/秒)。
// **那次事故的真根因其实是缺了能力门控**(见 requireStatusWatchCapability
// 的注释)——这道 floor 补上之后,能力门控会先一步在同样的场景下直接拒绝
// 进入循环,所以理论上这道 floor 从此再也不会在生产里被触发。留着它是因为
// 「门被绕过时退化成满速轮询」比「门被绕过时退化成每秒一次的轮询」仍然是
// 更差的结果——防线不该只有一层。
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
	return statusWatchLoopWith(ctx, out, asJSON, client.StatusCapabilities, client.StatusWatch, watchBackoff, watchIdleDelay)
}

// requireStatusWatchCapability 是进入 watch 循环前的**第一道、也是主要的**
// 一道门:拨一次不带 `wait=` 的 /v1/status,确认对面这一版 Guardian 声明过
// CapabilityStatusWatch,再决定要不要进循环。
//
// **绝不「试着拨一下看看」**:旧 Guardian 会忽略它不认识的 `wait` 查询参数、
// 回一份普通应答,而客户端无从区分「立刻返回是因为状态真的变了」与「这版根本
// 不支持长轮询,每次都是这样立刻返回」——不做这道门,结果就是真机验证撞上的
// 那次事故:退化成一个不出错、不提示、看起来像挂住了、实际在满速空转的命令
// (watchIdleDelay 的注释里有完整事故经过;那道 floor 是这道门失手时的第二道
// 防线,不是替代品)。既有代码在 /v1/rules、/v1/servers 的能力门控上已经踩过
// 并写明了同样的理由,这里取一致。
//
// **两种「没有」都要拒绝,但要分开报**(哪怕两条分支的处置动作相同——拒绝、
// 非零退出):
//   - capabilities 键**整个缺席**:这一版 Guardian 从没声明过任何能力
//     (Status 结构体里压根没有这个字段),不是「声明了、没有 status_watch」。
//   - capabilities 键**出现但不含 status_watch**(空数组或有别的能力但没有
//     这一项):这一版声明过能力,只是这一项还没上线。
//
// 两者都必须拒绝进入循环,理由相同(客户端确认不了对面支不支持长轮询),但
// 报给用户的话要分开——键缺席意味着「这版本比能力声明这个概念本身还老」,
// 通常暗示离得更远;而键存在但缺一项通常意味着只差一次小版本升级。
func requireStatusWatchCapability(ctx context.Context, probe func(context.Context) (guardian.Status, bool, error)) error {
	status, declared, err := probe(ctx)
	if err != nil {
		return fmt.Errorf("watch 无法确认这一版 Guardian 是否支持长轮询(探测 /v1/status 失败):%w", err)
	}
	version := status.GuardianVersion
	if version == "" {
		version = "unknown"
	}
	if !declared {
		return fmt.Errorf(
			"这一版 Guardian(guardian_version=%s)从未声明过 capabilities 字段,"+
				"无法确认是否支持长轮询;为避免退化成满速空转轮询,拒绝进入 --watch。"+
				"升级 Guardian 后重试,或不带 --watch 直接跑 bx status", version,
		)
	}
	if !slices.Contains(status.Capabilities, guardian.CapabilityStatusWatch) {
		return fmt.Errorf(
			"这一版 Guardian(guardian_version=%s)声明的 capabilities 里没有 %s,"+
				"不支持 --watch;为避免退化成满速空转轮询,拒绝进入循环。"+
				"升级 Guardian 后重试,或不带 --watch 直接跑 bx status",
			version, guardian.CapabilityStatusWatch,
		)
	}
	return nil
}

// statusWatchLoopWith 是 statusWatchLoop 的可注入核心:probeCapabilities、
// watch、backoff、idleDelay 都是参数,好让循环本身(能力门控、打印时机、
// 心跳静默、失败重试、代际号推进、不变化响应的节流)免 root、免真实 socket、
// 免真实等待地单测——与本包 readClientStatusReportWithObserver 同一个套路
// (公开入口固定依赖,内部函数注入依赖以便测试)。
func statusWatchLoopWith(
	ctx context.Context,
	out io.Writer,
	asJSON bool,
	probeCapabilities func(context.Context) (guardian.Status, bool, error),
	watch func(context.Context, uint64) (guardian.Status, error),
	backoff func(int) time.Duration,
	idleDelay time.Duration,
) error {
	if err := requireStatusWatchCapability(ctx, probeCapabilities); err != nil {
		return err
	}
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
