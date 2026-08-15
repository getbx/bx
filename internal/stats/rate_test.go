package stats

import (
	"testing"
	"time"
)

var rateBase = time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)

// **第一次采样只记基线。** 从零开始的累计值除以「程序启动到现在」会得出一个
// 毫无意义的数,而它会立刻变成峰值、把之后所有真实观测都盖住。
func TestFirstSampleOnlyEstablishesTheBaseline(t *testing.T) {
	var m RateMeter
	m.Observe(rateBase, 0, 5_000_000)
	if _, _, ok := m.PeakBPS(rateBase); ok {
		t.Fatal("第一次采样就产出了峰值")
	}
}

func TestPeakIsTheHighestObservedRate(t *testing.T) {
	var m RateMeter
	m.Observe(rateBase, 0, 0)
	m.Observe(rateBase.Add(time.Second), 0, 1_000_000)   // 1 MB/s
	m.Observe(rateBase.Add(2*time.Second), 0, 4_000_000) // 3 MB/s
	m.Observe(rateBase.Add(3*time.Second), 0, 4_100_000) // 100 kB/s

	bps, at, ok := m.PeakBPS(rateBase.Add(3 * time.Second))
	if !ok {
		t.Fatal("没有峰值")
	}
	if bps != 3_000_000 {
		t.Errorf("峰值 = %d, want 3000000 —— 后来的慢速把峰值盖掉了", bps)
	}
	if !at.Equal(rateBase.Add(2 * time.Second)) {
		t.Errorf("峰值时间 = %v", at)
	}
}

// **采样太密不许更新基线。** 一个 64KB 的 TCP 窗口在 10ms 里到齐,折算成
// 6.4MB/s —— 那不是链路速度,是突发。更新基线还会让下一拍的窗口同样短,
// 于是永远在放大突发。
func TestBurstsWithinTheWindowDoNotBecomeAPeak(t *testing.T) {
	var m RateMeter
	m.Observe(rateBase, 0, 0)
	for i := 1; i <= 20; i++ {
		m.Observe(rateBase.Add(time.Duration(i)*10*time.Millisecond), 0, int64(i)*64_000)
	}
	// 200ms 里共 1.28MB;这一串全在窗口内,一个都不该产出峰值。
	if _, _, ok := m.PeakBPS(rateBase.Add(200 * time.Millisecond)); ok {
		t.Fatal("窗口内的突发被当成了峰值")
	}
	// 满一秒之后才结算,而且用的是整整一秒的量。
	m.Observe(rateBase.Add(time.Second), 0, 1_280_000)
	bps, _, ok := m.PeakBPS(rateBase.Add(time.Second))
	if !ok || bps != 1_280_000 {
		t.Fatalf("整秒结算 = %d(ok=%v), want 1280000", bps, ok)
	}
}

// **峰值必须会过期。** 三天前的数字挂在界面上读起来像「现在就这么快」,
// 而那台服务器可能早就不行了。
func TestPeakExpires(t *testing.T) {
	var m RateMeter
	m.Observe(rateBase, 0, 0)
	m.Observe(rateBase.Add(time.Second), 0, 2_000_000)

	if _, _, ok := m.PeakBPS(rateBase.Add(rateDecayAfter)); !ok {
		t.Fatal("还没到期就失效了")
	}
	if _, _, ok := m.PeakBPS(rateBase.Add(rateDecayAfter + time.Minute)); ok {
		t.Fatal("过期的峰值还在报 —— 陈旧数字冒充现状")
	}
}

// 过期之后新的观测要重新立峰,**哪怕它比那个陈旧的峰低** —— 否则一台变慢了的
// 服务器会永远显示它历史最好的那次。
func TestAStaleHighPeakIsReplacedByAFreshLowerOne(t *testing.T) {
	var m RateMeter
	m.Observe(rateBase, 0, 0)
	m.Observe(rateBase.Add(time.Second), 0, 9_000_000)

	late := rateBase.Add(rateDecayAfter + time.Hour)
	m.Observe(late, 0, 9_000_000)
	m.Observe(late.Add(time.Second), 0, 9_100_000) // 100 kB/s

	bps, _, ok := m.PeakBPS(late.Add(time.Second))
	if !ok {
		t.Fatal("新观测之后没有峰值")
	}
	if bps != 100_000 {
		t.Errorf("峰值 = %d, want 100000 —— 陈旧的高峰没有被换掉", bps)
	}
}

// 计数器下降(不该发生,但真发生了就是不可信)时**重置基线,绝不产出负速率
// 或天文数字**。
func TestCountersGoingBackwardsNeverProduceANumber(t *testing.T) {
	var m RateMeter
	m.Observe(rateBase, 0, 5_000_000)
	m.Observe(rateBase.Add(time.Second), 0, 1_000_000)
	if _, _, ok := m.PeakBPS(rateBase.Add(time.Second)); ok {
		t.Fatal("计数器倒退却产出了峰值")
	}
	// 基线已经重置,后续正常采样照常工作。
	m.Observe(rateBase.Add(2*time.Second), 0, 1_500_000)
	bps, _, ok := m.PeakBPS(rateBase.Add(2 * time.Second))
	if !ok || bps != 500_000 {
		t.Fatalf("重置之后 = %d(ok=%v), want 500000", bps, ok)
	}
}

// 上下行都算进去:一次大上传同样是「这条隧道跑得动」的证据。
func TestUploadCountsToo(t *testing.T) {
	var m RateMeter
	m.Observe(rateBase, 0, 0)
	m.Observe(rateBase.Add(time.Second), 2_000_000, 0)
	bps, _, ok := m.PeakBPS(rateBase.Add(time.Second))
	if !ok || bps != 2_000_000 {
		t.Fatalf("上行没算进去:%d(ok=%v)", bps, ok)
	}
}

// 十进制单位:用户拿这个数去跟宽带套餐和别的测速工具比,那些全是十进制。
func TestHumanBPS(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B/s"},
		{999, "999 B/s"},
		{1_000, "1 kB/s"},
		{125_000, "125 kB/s"},
		{3_100_000, "3.1 MB/s"},
	} {
		if got := HumanBPS(tc.in); got != tc.want {
			t.Errorf("HumanBPS(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
