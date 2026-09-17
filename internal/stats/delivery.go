package stats

import "fmt"

// —— 「探针说健康,而真流量一条都过不去」——
//
// 2026-09-02 真机:`sudo bx up` 返回 `Status Protected` / `Tunnel 2073ms`,
// 而同一次会话里纯 IP、域名、出口 IP **全军覆没**;事后独立确证那台服务器从那条
// 线路根本不可达。也就是说 bx 对一条**载不动任何数据**的路径测出了 2073ms 并
// 判定为受保护 —— 对一个「死了要让用户立刻知道」的产品,这个绿灯是有害的。
//
// **这不是第一次,是第三次。** CLAUDE.md 里已经记过两回,只是从没被联系起来:
//   - 2026-06-30「TCP CONNECT 成功但批量数据被双跳 MTU 黑洞(**health 绿、
//     curl exit 28**)」
//   - 2026-08-14「经 SOCKS5 时本地握手就算成功,**必定关闭的 12345/54321 一样
//     「通」**」
// 两次都只当成排查技巧记下来,没有一次变成对健康判据的修改。
//
// **根因是判据本身弱**:`socks5Health` 做的是经 SOCKS 到 1.1.1.1:443 的一次
// TCP CONNECT。「能建立一条连接」严格弱于「数据流得动」,而两者之间那道缝正是
// MTU 黑洞、半开链路、以及 SOCKS 服务端提前应答会落进去的地方。
//
// **修法刻意不是加主动探测。** bx 已经有**被动证据**:每一次经隧道的拨号成败
// 都在 stats 里记着。真流量在失败时隧道就是不健康的,不管探针说什么 —— 零新增
// 出站、数据已经在记,而且符合本仓库那条「被动观测优于主动探测」。

const (
	// deliveryMinSamples 是下判断所需的最小样本。
	//
	// **取 20 而不是 5**:这条结论会出现在用户看到的第一屏上,而误报的代价是
	// 让一台工作正常的机器显得坏掉。20 次连续失败没有良性解释。
	deliveryMinSamples = 20
	// deliveryFailureFloor 是判定「载不动」所需的失败比例。
	//
	// **取 0.95 而不是 0.5**:半数失败可以是一个挂掉的目的地、一次网络抖动;
	// 而这条判据要说的是「这条隧道整个不通」,那对应的是近乎全灭。
	deliveryFailureFloor = 0.95
)

// DeliveryVerdict 是「隧道到底载不载得动数据」的三态。
//
// **零值是 DeliveryUnknown,不是 DeliveryOK** —— 与本仓库其它判据同一条纪律:
// 漏填是「说不知道」,而不是「说一切正常」。这条尤其要紧:这个功能存在的全部
// 理由就是「不该有的绿灯」。
type DeliveryVerdict int

const (
	// DeliveryUnknown:样本不够,说不出来。**它不是好消息也不是坏消息。**
	DeliveryUnknown DeliveryVerdict = iota
	// DeliveryFlowing:真流量在通。
	DeliveryFlowing
	// DeliveryStalled:探针说健康,而真流量近乎全灭。
	DeliveryStalled
)

// JudgeDelivery 按一个采样窗口内的经隧道拨号成败下判断。
//
// attempts/failures 是**这个窗口内的增量**,不是累计值 —— 用累计值会让一台
// 昨天出过问题的机器永远显示不健康,而那正好是另一种「不该有的颜色」。
func JudgeDelivery(attempts, failures int64) DeliveryVerdict {
	if attempts < deliveryMinSamples {
		return DeliveryUnknown
	}
	if failures < 0 || failures > attempts {
		// 计数器回退/越界:说不知道,不猜。
		return DeliveryUnknown
	}
	if float64(failures)/float64(attempts) >= deliveryFailureFloor {
		return DeliveryStalled
	}
	return DeliveryFlowing
}

// DeliveryWarning 把 Stalled 翻成用户看得懂的一句话;其余两态返回 nil。
//
// **措辞只陈述观测到的事实,原因只给可能性。** bx 分不清「服务器挂了」「这条
// 线路到不了它」「被墙了」—— 断言其中一个就是编答案(与 leakcheck 那条
// 「Protection may be off.」同一条纪律)。
//
// **severity 取 warn 不是 error,是刻意的中间态。** error 会把总状态降级成
// Needs Attention,而这条判据**真机上一次都没跑过** —— 阈值(20 次 / 95%)取自
// 推理不是数据。先让它把话说出来、跑一段,拿到真机证据再决定要不要升级成
// error、乃至接进 kill-switch(那会让 fail-closed 由它触发,爆炸半径大得多)。
// 这与调谐环当初「先只观察」是同一条纪律。
func DeliveryWarning(v DeliveryVerdict, attempts, failures int64) *Warning {
	if v != DeliveryStalled {
		return nil
	}
	return &Warning{
		Name:     "tunnel_not_delivering",
		Severity: "warn",
		Detail: fmt.Sprintf("the tunnel's own health check passes, but %d of the last %d connections through it failed — the tunnel most likely cannot carry data",
			failures, attempts),
		Hint: "the health check only proves a connection can be opened, not that data flows; start with bx explain <a domain you cannot open> to see the verdict, then look at the server or switch to another line",
	}
}
