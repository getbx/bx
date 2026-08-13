package supervisor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// **一次失败的重探绝不许缩小旁路。**
//
// 这条纪律本来就写在 bypassrefresh.go 的注释里,但它此前是靠「压根不重探」实现的 ——
// 而不重探正是那个缺陷:bx 由开机自启拉起时网络往往还没好,抓 DERP map 失败就退回
// 内置兜底表,**此后永不重试**。现在会重试,于是这条纪律必须由代码而不是由「不做」
// 来保证。
//
// 成功时**整份替换**而不是合并:兜底表是十个写死的 IP,可能已经不再属于 Tailscale。
// 把它们永久留在旁路里等于给几个陌生 IP 常开一条绕过隧道的路 —— 那是泄漏面,
// 不是冗余。抓到的答案是权威的,哪怕更短。
func TestBypassUpdateNeverShrinksOnFailure(t *testing.T) {
	previous := []string{"203.0.113.10/32", "203.0.113.11/32"}

	if got := decideBypassUpdate(previous, nil, errors.New("network is unreachable")); !equalStringSets(got, previous) {
		t.Errorf("抓取失败时应保留上一份,得到 %v", got)
	}
	if got := decideBypassUpdate(previous, []string{}, nil); !equalStringSets(got, previous) {
		t.Errorf("抓到空答案应保留上一份(空不是权威答案),得到 %v", got)
	}
	fresh := []string{"198.51.100.7/32"}
	if got := decideBypassUpdate(previous, fresh, nil); !equalStringSets(got, fresh) {
		t.Errorf("抓取成功应整份替换(即使更短),得到 %v", got)
	}
}

// 拿到第一份权威答案**之前**要密集重试(开机那几十秒正是网络还没好的时候);
// 之后只需偶尔复查 —— DERP 节点变动很慢,而每一次请求都是发给 Tailscale 控制面的。
func TestBypassProbeDelayBacksOffThenSettles(t *testing.T) {
	first := nextBypassProbeDelay(0, false)
	if first > 1*time.Minute {
		t.Errorf("首次重试不该等太久(开机时网络马上就好),得到 %s", first)
	}
	if nextBypassProbeDelay(3, false) <= first {
		t.Error("失败重试必须退避")
	}
	// **上限必须真的封顶,而且不许溢出。** 这个仓库栽过:退避上限断言被 int64 溢出
	// 架空,round 54 回绕成 0s,而唯一那条探针恰好落在回绕之后,一直为错误的理由通过。
	for _, attempt := range []int{10, 20, 54, 100, 1000} {
		d := nextBypassProbeDelay(attempt, false)
		if d <= 0 {
			t.Fatalf("attempt=%d 退避成了 %s —— 溢出或回绕", attempt, d)
		}
		if d > 10*time.Minute {
			t.Errorf("attempt=%d 退避 %s 超过上限", attempt, d)
		}
	}
	// 成功之后是长周期复查,不再是重试。
	settled := nextBypassProbeDelay(0, true)
	if settled < 1*time.Hour {
		t.Errorf("拿到权威答案后应转为长周期复查,得到 %s", settled)
	}
}

// **开机那条路必须真的自愈。** 这是这次修复的全部理由:bx 由开机自启拉起,
// 那一刻网络往往还没好 → 抓 DERP map 失败 → 退回内置兜底表 → 此前**永不重试**,
// 兜底表一直用到下次重启。
func TestBypassSourceKeepsRetryingUntilItGetsTheRealAnswer(t *testing.T) {
	fallback := []string{"203.0.113.10/32"}
	src := newTailscaleBypassSource(fallback)

	// 前两次失败(网络还没好),第三次成功。
	calls := 0
	fetch := func(context.Context) ([]string, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("network is unreachable")
		}
		return []string{"198.51.100.7/32", "198.51.100.8/32"}, nil
	}

	for i := 0; i < 3; i++ {
		fetched, err := fetch(context.Background())
		src.apply(fetched, err)
		if i < 2 {
			// **失败期间旁路一个字都不能少** —— 少了就等于把 Tailscale 的中继
			// 关在隧道外面之后又把门也关了。
			if !equalStringSets(src.CIDRs(), fallback) {
				t.Fatalf("第 %d 次失败后旁路变成了 %v,应保持兜底表", i+1, src.CIDRs())
			}
			if src.succeeded() {
				t.Fatalf("第 %d 次还没成功,不该标记为已拿到权威答案", i+1)
			}
		}
	}
	if !src.succeeded() {
		t.Fatal("第三次成功后应记为已拿到权威答案(退避随之转成长周期)")
	}
	if len(src.CIDRs()) != 2 {
		t.Fatalf("成功后应替换成权威答案,得到 %v", src.CIDRs())
	}
}

// 循环必须能被 ctx 停下,而且**停下时不留 goroutine**。
func TestBypassSourceLoopStopsWithContext(t *testing.T) {
	src := newTailscaleBypassSource(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		src.Run(ctx, func(context.Context) ([]string, error) {
			return []string{"198.51.100.7/32"}, nil
		})
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后循环没退出 —— 一个停不下来的后台循环会拖住整个关闭路径")
	}
}

// **CIDRs 返回的必须是拷贝。** 调用方(bypass 刷新)拿到之后会 append/排序,
// 直接给出内部切片会让一次刷新悄悄改掉这份权威答案。
func TestBypassSourceHandsOutCopies(t *testing.T) {
	src := newTailscaleBypassSource([]string{"203.0.113.10/32"})
	got := src.CIDRs()
	got[0] = "0.0.0.0/0"
	if src.CIDRs()[0] != "203.0.113.10/32" {
		t.Fatal("调用方改了返回值,内部状态跟着变了 —— 必须返回拷贝")
	}
}
