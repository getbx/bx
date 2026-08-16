package supervisor

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// 「bx 自己的直连出不去」这件事的自愈。
//
// ## 为什么现有的机制不够
//
// 重装那条 scoped 默认路由的逻辑早就有(darwinUnderlayScopedDefault),但它挂在
// **换网** 那条路上:`darwinUnderlayPlan` 头一句就是
// `if underlayGeneration(old) == underlayGeneration(next) { return nil, nil }`。
// 也就是说,触发条件是「**我记账里的** underlay 变了」,而不是「那条路由真的
// 还在吗」。路由因别的原因消失(睡眠唤醒、别的程序动过路由表、系统裁剪),
// 它是个空操作。
//
// 这正是本仓库反复写过的那条:**不信自己的记账,去问内核**。
//
// ## 真机现场(2026-08-15,项目所有者的 Mac)
//
// 早上 `直连 446(失败 0)`,傍晚 `*.qq.com 55/68 失败`、`*.push.apple.com
// 175/333`,而 `route -n get -ifscope en0 8.8.8.8` 是 `not in table` ——
// 路由是**启动之后丢的**。全程 `bx status` 显示 Protected、隧道健康:
// 这个故障对所有既有信号都是隐形的,只有直接问内核才看得见。
//
// ## 为什么这样修是安全的
//
// 它只往 **scoped 表**加一条,不碰全局表;只有显式用了 `IP_BOUND_IF` 的 socket
// (恰好就是 bx 自己的直连/解析/socks)会用到它。**不削弱 kill-switch** ——
// 那是 Dialer 里的判定,不是路由属性。而且它**只加不删**:多服务的 Mac 上系统
// 自带一条同款,删它会打断用户的网络而 bx 无从恢复(2026-08-13 的教训)。

const (
	// egressCheckInterval 是两次探测之间的间隔。
	//
	// 一次探测是一条 `route -n get`(只读、不发包)。30 秒与调谐环的基础周期
	// 同量级:这个故障一旦发生就一直在,不需要秒级发现;而更密只是白烧 fork。
	egressCheckInterval = 30 * time.Second
	// egressRepairCooldown 是两次**修复**之间的最短间隔。
	//
	// 修不好的时候不该每 30 秒重试一次 —— 那多半意味着别的东西坏了(网卡没了、
	// 网关探不到),而重试改变不了它。冷静期让日志与 fork 都保持稀疏,同时
	// **永不放弃**:网络可能自己回来。
	egressRepairCooldown = 5 * time.Minute
)

// egressProbe 问一次「直连出得去吗」。三态:出得去 / 出不去 / 问不出来。
type egressProbe func(context.Context) (reachable bool, known bool, err error)

// egressRepair 重装那条 scoped 默认路由。
type egressRepair func(context.Context) error

// shouldRepairEgress 是这件事的**全部判定**。
//
// **只在明确观测到「出不去」时动手。** 问不出来(探不到物理网卡名、命令失败)
// 时一律不动:那时我们对这件事一无所知,而基于「不知道」去改路由,是拿一个
// 诊断功能去动真实网络 —— 与 Tristate 那条纪律同源。
//
// sinceLastRepair 为负表示还没修过。
func shouldRepairEgress(reachable, known bool, sinceLastRepair time.Duration) bool {
	if !known || reachable {
		return false
	}
	if sinceLastRepair < 0 {
		return true
	}
	return sinceLastRepair >= egressRepairCooldown
}

// watchDirectEgress 是那条自愈循环。**注入探测与修复**,好让整段逻辑可测 ——
// 真实实现要 fork `route`,测试进不去。
//
// probe / repair 任一为 nil 就不跑(非 darwin 平台没有这两样原语)。
func watchDirectEgress(ctx context.Context, probe egressProbe, repair egressRepair, tick <-chan time.Time) {
	if probe == nil || repair == nil {
		return
	}
	var lastRepair time.Time
	// **change-only 日志。** 这个循环在健康机器上一天跑 2880 次,每次打一行会把
	// 它训练成噪声,而噪声会训练人忽略它。
	lastKnownBad := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		}
		reachable, known, err := probe(ctx)
		if err != nil && known {
			// 探测报了错却仍声称知道答案 —— 不该发生;当作不知道,别据此改路由。
			known = false
		}
		if !known || reachable {
			if lastKnownBad {
				log.Printf("direct_egress 恢复:bx 自己的直连又出得去了")
				lastKnownBad = false
			}
			continue
		}
		if !lastKnownBad {
			// 第一次看见就说一句:它对用户的可见后果是**每一条 direct 规则都失败**,
			// 而隧道看起来一切正常。
			log.Printf("direct_egress 断了:bx 自己的直连出不去(scoped 默认路由不见了)—— 每一条 direct 规则都会失败")
			lastKnownBad = true
		}
		since := time.Duration(-1)
		if !lastRepair.IsZero() {
			since = time.Since(lastRepair)
		}
		if !shouldRepairEgress(reachable, known, since) {
			continue
		}
		lastRepair = time.Now()
		if err := repair(ctx); err != nil {
			log.Printf("direct_egress 重装 scoped 默认路由失败: %v", err)
			continue
		}
		log.Printf("direct_egress 已重装 scoped 默认路由")
	}
}

// egressRepairOutcome 判定一次 `route add` 算不算成功。
//
// **抽出来是因为上面那层直接 exec,测试进不去** —— 而这个仓库全部的事故都在
// 那种进不去的地方(变异验证当场证实:把「已存在」当成失败,没有任何测试会红)。
//
// 「已经存在」是**好消息**:多服务的 Mac 上系统自带一条同款,而探测与修复之间
// 路由也可能自己回来了。把它当成失败会让日志每 5 分钟报一次假警报,而路由是好的。
func egressRepairOutcome(err error, output, dev, gw string) error {
	if err == nil {
		return nil
	}
	if strings.Contains(output, "File exists") {
		return nil
	}
	return fmt.Errorf("route add -ifscope %s default %s: %w (%s)", dev, gw, err, strings.TrimSpace(output))
}
