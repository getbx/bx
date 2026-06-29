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

func TestIndexOfLink(t *testing.T) {
	ts := []string{"vless://a", "brook://b", "hysteria2://c"}
	if indexOfLink(ts, "brook://b") != 1 {
		t.Error("应找到下标 1")
	}
	if indexOfLink(ts, "不在列表") != -1 {
		t.Error("不存在应返回 -1")
	}
}

// status 呈现「scheme@host」(无凭据)。锁定全部六种传输——尤其 ss/vmess 的
// base64 authority 必须解出真实 host(否则 status「传输」行会显示乱码 base64)。
func TestTransportLabelAllSchemes(t *testing.T) {
	cases := map[string]string{
		"vless://uid@1.2.3.4:443?security=reality":                                     "reality@1.2.3.4",
		"hysteria2://pw@5.6.7.8:8443?sni=x":                                            "hysteria2@5.6.7.8",
		"trojan://pw@9.9.9.9:443":                                                      "trojan@9.9.9.9",
		"ss://YWVzLTI1Ni1nY206cHc@1.2.3.4:8388#hk":                                     "shadowsocks@1.2.3.4",
		"vmess://eyJhZGQiOiIyLjIuMi4yIiwicG9ydCI6IjQ0MyIsImlkIjoieCIsIm5ldCI6InRjcCJ9": "vmess@2.2.2.2",
		"brook://server?server=3.3.3.3%3A9999&password=pw":                             "brook@3.3.3.3",
	}
	for link, want := range cases {
		if got := transportLabel(link); got != want {
			t.Errorf("transportLabel(%q)=%q want %q", link, got, want)
		}
	}
}
