package stats

import (
	"sync"
	"testing"
)

// **决策不等于结果。**
//
// bx 一直只数「我把这条连接判给了谁」(proxy 46447 / direct 26186),从不数
// 「拨通了没有」。一次真实排查里,那 26186 条直连中有大量是被用户自己的
// `'*.steamstatic.com'` 规则逼出去的、而且**每一条都在 4 毫秒内失败** ——
// bx 全程一个字都答不上来,用户以为是 CDN 或 bx 的问题。
//
// 失败计数是零成本的:bx 就在数据面上,它每一次都看见了,此前只是扔掉。
func TestCountersRecordFailuresSeparatelyFromDecisions(t *testing.T) {
	var c Counters
	c.Direct()
	c.Direct()
	c.DirectFailed()
	c.Proxy()
	c.ProxyFailed()

	s := c.Snapshot()
	if s.Direct != 2 || s.DirectFailed != 1 {
		t.Errorf("direct=%d failed=%d, want 2/1", s.Direct, s.DirectFailed)
	}
	if s.Proxy != 1 || s.ProxyFailed != 1 {
		t.Errorf("proxy=%d failed=%d, want 1/1", s.Proxy, s.ProxyFailed)
	}
}

// **归因要点名到 config 里那一行。** 列出规则没什么用(用户自己写的),
// 有用的是「这条规则逼出去 8113 条,8113 条全失败了」—— 它点名了该删哪一行。
func TestRuleOutcomesAttributeFailuresToTheRuleThatForcedThem(t *testing.T) {
	var c Counters
	for i := 0; i < 3; i++ {
		c.RuleAttempt("user_direct", "*.steamstatic.com")
		c.RuleFailure("user_direct", "*.steamstatic.com")
	}
	c.RuleAttempt("user_direct", "gsa.apple.com")
	c.RuleAttempt("china_domain", "")

	rules := c.Snapshot().Rules
	byRule := map[string]RuleOutcome{}
	for _, r := range rules {
		byRule[r.Rule] = r
	}
	if got := byRule["*.steamstatic.com"]; got.Attempts != 3 || got.Failures != 3 {
		t.Errorf("*.steamstatic.com = %+v, want 3 attempts / 3 failures", got)
	}
	if got := byRule["gsa.apple.com"]; got.Attempts != 1 || got.Failures != 0 {
		t.Errorf("gsa.apple.com = %+v, want 1 attempt / 0 failures", got)
	}
	// **内建列表也要计数,只是没有「哪一行」可点名。** 少了它,用户无从判断
	// 「全网都在失败」与「只有我这条规则在失败」—— 而两者的处置完全不同。
	found := false
	for _, r := range rules {
		if r.Source == "china_domain" {
			found = true
		}
	}
	if !found {
		t.Error("内建列表的结果没有被计数")
	}
}

// 快照必须是稳定排序的,否则 bx status 每次输出顺序都不同,diff 不了。
func TestRuleOutcomesSnapshotIsStablySorted(t *testing.T) {
	var c Counters
	for _, r := range []string{"z.com", "a.com", "m.com"} {
		c.RuleAttempt("user_direct", r)
	}
	first := c.Snapshot().Rules
	for i := 0; i < 5; i++ {
		if got := c.Snapshot().Rules; len(got) != len(first) {
			t.Fatalf("长度不稳定")
		} else {
			for j := range got {
				if got[j].Rule != first[j].Rule {
					t.Fatalf("顺序不稳定:%v vs %v", got, first)
				}
			}
		}
	}
}

// **规则数由 config 决定,是有界的;但计数器不许因为一个 bug 就无界增长。**
// 上限是纯防御:真实配置最多几十条,撞上上限本身就说明有人在按域名计数。
func TestRuleOutcomesAreBounded(t *testing.T) {
	var c Counters
	for i := 0; i < maxTrackedRules*3; i++ {
		c.RuleAttempt("user_direct", string(rune('a'+i%26))+string(rune('a'+i/26))+".com")
	}
	if n := len(c.Snapshot().Rules); n > maxTrackedRules {
		t.Fatalf("跟踪了 %d 条规则,超过上限 %d", n, maxTrackedRules)
	}
}

// 并发安全 —— 它被数据面每条连接写。
func TestRuleOutcomesAreConcurrencySafe(t *testing.T) {
	var c Counters
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.RuleAttempt("user_direct", "*.steamstatic.com")
			c.RuleFailure("user_direct", "*.steamstatic.com")
			_ = c.Snapshot()
		}()
	}
	wg.Wait()
	for _, r := range c.Snapshot().Rules {
		if r.Rule == "*.steamstatic.com" && (r.Attempts != 50 || r.Failures != 50) {
			t.Fatalf("并发下计数丢失:%+v", r)
		}
	}
}
