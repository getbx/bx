package supervisor

import (
	"context"
	"log"
	"time"
)

// failoverPolicy 是多传输自动容灾的「何时切」决策核心,纯逻辑、可单测。
// 设计宪法:不泄漏为本质——切换由上层经 transportSwapper 执行(建新→等健康→
// SetTransport→停旧),全程 fail-closed;policy 只决定「切不切、切到谁」。
// 防抖三件套(吃过 defaultRoute metric 假抖动的亏):滞回 + 冷静期 + 全挂不切。
type failoverPolicy struct {
	failoverAfter time.Duration // 当前传输需连续不健康满此窗口才允许切(滞回,防瞬时抖动)
	cooldown      time.Duration // 刚切过此时长内不再切(防横跳)
}

// decide 返回应切到的候选下标,或 -1 表示保持当前(不切)。
//   - cur:当前活跃传输下标
//   - healthy:各候选(按优先级排序)的健康态
//   - unhealthySince:当前传输已连续不健康多久(当前健康则为 0)
//   - sinceLastSwitch:距上次切换多久
//
// 不切的几种情形:当前健康 / 未达滞回窗口 / 冷静期内 / 无健康备选(全挂=网络问题,
// 保持当前并由 kill-switch 继续 Block,绝不横跳、绝不回落直连)。
func (p failoverPolicy) decide(cur int, healthy []bool, unhealthySince, sinceLastSwitch time.Duration) int {
	if cur < 0 || cur >= len(healthy) || healthy[cur] {
		return -1 // 当前健康(或越界保护)→ 不切
	}
	if unhealthySince < p.failoverAfter {
		return -1 // 滞回:不健康还不够久,可能只是瞬时抖动
	}
	if sinceLastSwitch < p.cooldown {
		return -1 // 冷静期:刚切过,防横跳
	}
	// 按优先级找下一个健康候选(从 cur 之后绕一圈)。
	for off := 1; off < len(healthy); off++ {
		i := (cur + off) % len(healthy)
		if healthy[i] {
			return i
		}
	}
	return -1 // 无健康备选:网络问题,保持当前 + 继续 Block,不横跳
}

// transportLabel 给传输链接打个免凭据标签(scheme@host),供日志用——绝不打 link 全文(含 uuid/密码)。
func transportLabel(link string) string {
	host, _ := serverHostFromLink(link)
	return transportKind(link) + "@" + host
}

// switchAge 返回距上次切换的时长;从未切过(零值)返回很大值,使首次容灾不被冷静期挡。
func switchAge(last, now time.Time) time.Duration {
	if last.IsZero() {
		return 1 << 62
	}
	return now.Sub(last)
}

// runFailover 后台监当前传输健康:持续不健康(过滞回 + 冷静期)时,按优先级 swapTo 备选,
// 直到某个建得起来且健康;全部起不来则保持当前 + 继续 Block(kill-switch),不横跳(网络问题
// 不该触发 failover 风暴)。切换由 swapTo 经 fail-closed 路径执行(建新→等健康→SetTransport→停旧)。
// decide() 当大脑:备选乐观假设健康,swapTo 失败即标 false 重判,自然实现「全挂不切」。
func (s *transportSwapper) runFailover(ctx context.Context, transports []string, policy failoverPolicy, interval time.Duration) {
	if len(transports) < 2 {
		return // 单传输无可切;挂了由 kill-switch 接管
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	curIdx := 0 // cfg.Server = transports[0]
	var unhealthySince, lastSwitch time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if s.lt.get().Healthy() {
			unhealthySince = time.Time{}
			continue
		}
		now := time.Now()
		if unhealthySince.IsZero() {
			unhealthySince = now
		}
		// 备选乐观假设健康(只有当前已知不健康);先过一次门控,未过(滞回/冷静期)就别动 lastSwitch。
		healthy := make([]bool, len(transports))
		for i := range healthy {
			healthy[i] = true
		}
		healthy[curIdx] = false
		target := policy.decide(curIdx, healthy, now.Sub(unhealthySince), switchAge(lastSwitch, now))
		if target < 0 {
			continue // 门控未过(滞回未满 / 冷静期内),不尝试、不节流
		}
		// 门控已过:按优先级逐个 swapTo,失败标 false 重判下一个;全失败 → decide 返 -1 退出。
		for target >= 0 {
			if err := s.swapTo(transports[target]); err == nil {
				log.Printf("failover: 当前传输持续不健康,已切到 %s", transportLabel(transports[target]))
				curIdx = target
				unhealthySince = time.Time{}
				break
			}
			healthy[target] = false
			target = policy.decide(curIdx, healthy, now.Sub(unhealthySince), switchAge(lastSwitch, now))
		}
		if target < 0 {
			log.Printf("failover: 所有备选传输均不可用,保持当前并 Block(疑网络问题,不横跳)")
		}
		lastSwitch = now // 切成功→冷静期;全挂→节流重试,都避免每 tick 风暴
	}
}
