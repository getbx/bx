package supervisor

import (
	"testing"
	"time"
)

func newPolicy() failoverPolicy {
	return failoverPolicy{failoverAfter: 20 * time.Second, cooldown: 60 * time.Second}
}

// 当前传输健康 → 不切。
func TestFailoverStayWhenCurrentHealthy(t *testing.T) {
	p := newPolicy()
	if got := p.decide(0, []bool{true, true}, 0, 5*time.Minute); got != -1 {
		t.Fatalf("当前健康应不切, got=%d", got)
	}
}

// 当前不健康但未达滞回窗口 → 不切(防瞬时抖动,吃过假抖动的亏)。
func TestFailoverHysteresisHoldsWithinWindow(t *testing.T) {
	p := newPolicy()
	if got := p.decide(0, []bool{false, true}, 10*time.Second, 5*time.Minute); got != -1 {
		t.Fatalf("未达 failoverAfter 不该切, got=%d", got)
	}
}

// 当前持续不健康超阈值 + 有健康候选 + 过冷静期 → 切到下一个健康的。
func TestFailoverSwitchesToHealthyCandidate(t *testing.T) {
	p := newPolicy()
	if got := p.decide(0, []bool{false, true}, 25*time.Second, 5*time.Minute); got != 1 {
		t.Fatalf("应切到健康候选 1, got=%d", got)
	}
}

// 刚切过(冷静期内)→ 不切(防横跳)。
func TestFailoverCooldownBlocksFlapping(t *testing.T) {
	p := newPolicy()
	if got := p.decide(0, []bool{false, true}, 25*time.Second, 30*time.Second); got != -1 {
		t.Fatalf("冷静期内不该切, got=%d", got)
	}
}

// 所有候选都不健康 → 不切(那是网络问题,非传输被封;保持当前 + 继续 Block,不横跳)。
func TestFailoverAllUnhealthyStays(t *testing.T) {
	p := newPolicy()
	if got := p.decide(0, []bool{false, false, false}, 30*time.Second, 5*time.Minute); got != -1 {
		t.Fatalf("全挂应不切(网络问题), got=%d", got)
	}
}

// 按优先级顺序选下一个健康的(跳过中间不健康的)。
func TestFailoverPicksNextHealthyInOrder(t *testing.T) {
	p := newPolicy()
	// 当前 0 挂,1 也挂,2 健康 → 选 2。
	if got := p.decide(0, []bool{false, false, true}, 30*time.Second, 5*time.Minute); got != 2 {
		t.Fatalf("应跳过不健康的 1 选 2, got=%d", got)
	}
}

// 单传输(无备选)→ 永不切(就一个,挂了只能 Block,由 kill-switch 接管)。
func TestFailoverSingleTransportNeverSwitches(t *testing.T) {
	p := newPolicy()
	if got := p.decide(0, []bool{false}, 5*time.Minute, 5*time.Minute); got != -1 {
		t.Fatalf("单传输不该切, got=%d", got)
	}
}
