package stats

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/getbx/bx/internal/dialfail"

	"github.com/getbx/bx/internal/udpsource"
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
		c.RuleFailure("user_direct", "*.steamstatic.com", dialfail.Unreachable)
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
			c.RuleFailure("user_direct", "*.steamstatic.com", dialfail.Unreachable)
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

// **TestUDPSourceNamesMatchTheDialer 2026-08-31 退场,记档在此。**
//
// 它读 dialer.go 的源码,检查那几个来源名字符串在不在。守的事情是对的 ——
// 这几个字符串是记账一侧(dialer)与汇总一侧(stats)之间唯一按字面对齐的
// 东西,漂开的后果彻底静默:计数照记、汇总一条都匹配不上,而输出与「UDP
// 完全正常」逐字节相同。
//
// 但它守的是「**两份拷贝还一样**」,而正确的做法是让它们**没法不一样**:
// 清单下沉到 internal/udpsource(不 import 本仓库任何东西的叶子包),两边
// 共读同一份 —— 与 internal/barriercidr 同一个先例、同一个理由。漂移在
// 构造上不可能之后,那条守卫没有东西可守了。
//
// 下面这条取代它:钉住两边**确实来自同一个来源**,而不是「两份字面量恰好
// 相等」。谁把任何一边改回自己的字面量,它当场转红。
func TestUDPSourceNamesComeFromTheSharedLeafPackage(t *testing.T) {
	for _, pair := range []struct {
		local  string
		shared string
	}{
		{udpSourceProxy, udpsource.Proxy},
		{udpSourceProxyFallback, udpsource.ProxyFallback},
		{udpSourceDirectRealtime, udpsource.DirectRealtime},
	} {
		if pair.local != pair.shared {
			t.Fatalf("%q 与叶子包里的 %q 不一致 —— 有人把它改回了自己的字面量,"+
				"而漂开的后果是汇总一条都匹配不上,输出与「一切正常」一模一样",
				pair.local, pair.shared)
		}
	}
}

// **回落必须被说出来。** 通路是好的、只是慢,用户完全感知不到;而它有一个
// 明确的动作:去看那台 UDP 服务器。
func TestUDPNoticeReportsSilentFallback(t *testing.T) {
	snap := Snapshot{Rules: []RuleOutcome{
		{Source: udpSourceProxy, Attempts: 10},
		{Source: udpSourceProxyFallback, Attempts: 90},
	}}
	notice := snap.UDPNotice()
	if !strings.Contains(notice, "90") || !strings.Contains(notice, "fast lane") {
		t.Fatalf("没说清回落:%q", notice)
	}
}

// **一切正常时一个字都不打** —— 这是它不被训练成噪声的前提。
func TestUDPNoticeIsSilentWhenHealthy(t *testing.T) {
	for _, tc := range []struct {
		name string
		snap Snapshot
	}{
		{"没有 UDP 流量", Snapshot{}},
		{"全走加速档", Snapshot{Rules: []RuleOutcome{{Source: udpSourceProxy, Attempts: 500}}}},
		{"启动那几次回落", Snapshot{Rules: []RuleOutcome{
			{Source: udpSourceProxy, Attempts: 500}, {Source: udpSourceProxyFallback, Attempts: 3},
		}}},
		{"零星失败", Snapshot{Rules: []RuleOutcome{
			{Source: udpSourceProxy, Attempts: 500, Failures: 4},
		}}},
		{"1/1 失败(100% 但说明不了任何事)", Snapshot{Rules: []RuleOutcome{
			{Source: udpSourceProxy, Attempts: 1, Failures: 1},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if notice := tc.snap.UDPNotice(); notice != "" {
				t.Errorf("正常却说了话:%q", notice)
			}
		})
	}
}

// 大量失败要报,而且要给出分母 —— 光一个「失败很多」用户无从判断严重程度。
func TestUDPNoticeReportsWidespreadFailure(t *testing.T) {
	snap := Snapshot{Rules: []RuleOutcome{
		{Source: udpSourceProxy, Attempts: 100, Failures: 80},
	}}
	notice := snap.UDPNotice()
	if !strings.Contains(notice, "80") || !strings.Contains(notice, "100") {
		t.Fatalf("没给出分子分母:%q", notice)
	}
}

// UDP 的来源**不许被当成用户规则点名**:用户在 config 里搜不到 `udp_proxy`,
// 那条「改哪一行」的指引会把他送去一个不存在的地方。
func TestUDPSourcesAreNeverNamedAsUserRules(t *testing.T) {
	snap := Snapshot{Rules: []RuleOutcome{
		{Source: udpSourceProxy, Attempts: 100, Failures: 100},
		{Source: udpSourceProxyFallback, Attempts: 100, Failures: 100},
		{Source: udpSourceDirectRealtime, Attempts: 100, Failures: 100},
	}}
	if failing := snap.FailingRules(); len(failing) != 0 {
		t.Fatalf("UDP 来源被当成用户规则点名了:%+v", failing)
	}
}

// —— 死规则判据的两个门槛输入(2026-08-24)——

// 全局判定数衡量「这台机器有没有真的被用过」,所以**内建列表命中也要数**。
// 只数用户规则会让流量几乎全走内建列表的机器永远达不到门槛,于是死规则这一类
// 静默地从不生效 —— 那是这个仓库反复出现的失效形状。
func TestDecisionsCountsBuiltinHitsToo(t *testing.T) {
	var c Counters
	c.RuleAttempt("user_direct", "*.qq.com")
	c.RuleAttempt("china_domain", "")
	c.RuleAttempt("china_domain", "")
	if got := c.Decisions(); got != 3 {
		t.Fatalf("Decisions() = %d, want 3(含两次内建命中)", got)
	}
}

// 失败不是一次新的判定 —— RuleFailure 跟在 RuleAttempt 之后,数两次会让门槛虚高。
func TestFailuresDoNotDoubleCountDecisions(t *testing.T) {
	var c Counters
	c.RuleAttempt("user_direct", "a.com")
	c.RuleFailure("user_direct", "a.com", dialfail.Timeout)
	if got := c.Decisions(); got != 1 {
		t.Fatalf("Decisions() = %d, want 1", got)
	}
}

// 跟踪表满过要留下痕迹 —— 之后「没有条目」不许被读成「没命中」。
func TestRuleTrackingOverflowIsObservable(t *testing.T) {
	var c Counters
	if c.RuleTrackingOverflowed() {
		t.Fatal("空计数器不该报溢出")
	}
	for i := 0; i < maxTrackedRules+5; i++ {
		c.RuleAttempt("user_direct", fmt.Sprintf("r%d.com", i))
	}
	if !c.RuleTrackingOverflowed() {
		t.Fatalf("插了 %d 条(上限 %d)却没报溢出", maxTrackedRules+5, maxTrackedRules)
	}
}

// **表满之后那些判定仍然要计入 Decisions。**
//
// 计划没写这一条,实现时定的:Decisions 衡量「这台机器有没有被用过」,而按规则
// 跟踪的 256 上限是个**实现细节**。表满恰恰是机器最忙的时候,那时停止计数会让
// 门槛在最该达到的时候反而更难达到 —— 一台忙到撑爆跟踪表的机器,永远等不到
// 死规则判据生效。
func TestDecisionsKeepCountingAfterTheRuleTableIsFull(t *testing.T) {
	var c Counters
	const extra = 5
	for i := 0; i < maxTrackedRules+extra; i++ {
		c.RuleAttempt("user_direct", fmt.Sprintf("r%d.com", i))
	}
	if got, want := c.Decisions(), int64(maxTrackedRules+extra); got != want {
		t.Fatalf("Decisions() = %d, want %d —— 表满之后的判定被丢掉了,"+
			"而跟踪上限是实现细节,不该影响「这台机器被用过多少」", got, want)
	}
}

// 失败分类必须真的被记下来,并且**快照要深拷贝那张 map**。
//
// `ruleSnapshot` 里是 `out = append(out, *entry)` —— 浅拷贝只复制 map 头,
// 快照与活计数器会共享同一张表,于是「快照」会跟着后续失败继续变,而调用方
// 以为它凝固了。与 statusdigest 那次 FailingRules 切片同一个根因,
// 而那一次的后果是 digest 污染了它所要度量的东西。
func TestRuleFailureKindsAreRecordedAndSnapshotIsADeepCopy(t *testing.T) {
	c := &Counters{}
	c.RuleAttempt("user_direct", "*.qq.com")
	c.RuleFailure("user_direct", "*.qq.com", dialfail.Unreachable)

	before := c.Snapshot().Rules
	if len(before) != 1 || before[0].FailureKinds[dialfail.Unreachable] != 1 {
		t.Fatalf("分类没被记下来:%+v", before)
	}

	// 快照之后继续记账 —— 已经拿走的那份不许跟着变。
	c.RuleAttempt("user_direct", "*.qq.com")
	c.RuleFailure("user_direct", "*.qq.com", dialfail.Unreachable)
	if got := before[0].FailureKinds[dialfail.Unreachable]; got != 1 {
		t.Errorf("快照跟着活计数器变了(%d)—— 那张 map 是共享的", got)
	}
}

// 归不了类的失败**仍然计入总数**,只是不进分类表。
// 少数一条也不许因为归不了类就从 Failures 里消失。
func TestUnclassifiedFailureStillCountsTowardTheTotal(t *testing.T) {
	c := &Counters{}
	c.RuleAttempt("user_direct", "a.com")
	c.RuleFailure("user_direct", "a.com", "")
	rules := c.Snapshot().Rules
	if len(rules) != 1 || rules[0].Failures != 1 {
		t.Fatalf("归不了类的失败从总数里消失了:%+v", rules)
	}
	if len(rules[0].FailureKinds) != 0 {
		t.Errorf("空分类被记成了一个类别:%+v", rules[0].FailureKinds)
	}
}
