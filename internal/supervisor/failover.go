package supervisor

import "time"

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
