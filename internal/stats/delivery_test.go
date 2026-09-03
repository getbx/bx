package stats

import (
	"strings"
	"testing"
)

// 这条判据存在的全部理由:真机上 `Status Protected` / `Tunnel 2073ms`,而同一次
// 会话里纯 IP、域名、出口 IP 全军覆没。**一个不该有的绿灯**,而 bx 在数据面上
// 每一次失败都看见了。

// 样本不够时**说不知道,不说一切正常**。
// 这是整个功能最容易搞反的一处:它存在就是为了消灭不该有的绿灯,
// 而「样本不够 ⇒ 报 OK」正好又造一个。
func TestDeliveryStaysUnknownUntilThereIsEnoughEvidence(t *testing.T) {
	for _, n := range []int64{0, 1, 5, deliveryMinSamples - 1} {
		if got := JudgeDelivery(n, n); got != DeliveryUnknown {
			t.Errorf("%d 次全失败就下判断了(得到 %v)—— 样本不够时必须说不知道", n, got)
		}
	}
}

// 零值必须是 Unknown。漏填是「说不知道」,不是「说一切正常」。
func TestDeliveryZeroValueIsUnknown(t *testing.T) {
	var v DeliveryVerdict
	if v != DeliveryUnknown {
		t.Fatalf("零值是 %v,必须是 Unknown", v)
	}
	if DeliveryWarning(v, 0, 0) != nil {
		t.Error("零值产出了告警")
	}
}

// 够了样本、近乎全灭 ⇒ 判 Stalled。
func TestDeliveryFlagsANearTotalFailure(t *testing.T) {
	if got := JudgeDelivery(24, 24); got != DeliveryStalled {
		t.Errorf("24/24 全失败判成了 %v", got)
	}
	if got := JudgeDelivery(100, 96); got != DeliveryStalled {
		t.Errorf("96%% 失败判成了 %v", got)
	}
}

// **半数失败不算。**
// 那可以是一个挂掉的目的地、一次抖动;而这条判据要说的是「这条隧道整个不通」。
// 门槛松掉的代价是让一台工作正常的机器显得坏掉,而那正是另一种误导。
func TestDeliveryDoesNotCryWolfOnPartialFailures(t *testing.T) {
	for _, tc := range []struct{ attempts, failures int64 }{
		{100, 50}, {100, 80}, {100, 94}, {40, 20},
	} {
		if got := JudgeDelivery(tc.attempts, tc.failures); got == DeliveryStalled {
			t.Errorf("%d/%d 被判成隧道整个不通 —— 门槛太松", tc.failures, tc.attempts)
		}
	}
}

// 计数器越界/回退时**说不知道,不猜**。
// 一个负的或超过尝试数的失败计数是记账出了问题,据它下结论是拿一个坏值
// 去驱动用户看到的第一屏。
func TestDeliverySaysNothingOnImpossibleCounts(t *testing.T) {
	for _, tc := range []struct{ attempts, failures int64 }{{50, -1}, {50, 51}} {
		if got := JudgeDelivery(tc.attempts, tc.failures); got != DeliveryUnknown {
			t.Errorf("attempts=%d failures=%d 判成了 %v", tc.attempts, tc.failures, got)
		}
	}
}

// 只有 Stalled 才出声。**一切正常时一个字都不打**,这是它不被训练成噪声的前提。
func TestDeliveryWarningOnlyFiresWhenStalled(t *testing.T) {
	if DeliveryWarning(DeliveryFlowing, 100, 1) != nil {
		t.Error("流量正常却报了告警")
	}
	if DeliveryWarning(DeliveryUnknown, 3, 3) != nil {
		t.Error("样本不够却报了告警")
	}
	w := DeliveryWarning(DeliveryStalled, 24, 24)
	if w == nil {
		t.Fatal("隧道载不动数据却一个字没说")
	}
	if w.Hint == "" {
		t.Error("说了坏消息却没给下一步 —— 这条会出现在用户看到的第一屏上")
	}
}

// **措辞只陈述观测到的事实,原因只给可能性。**
// bx 分不清「服务器挂了」「这条线路到不了它」「被墙了」,断言其中一个就是编答案。
func TestDeliveryWarningDoesNotInventACause(t *testing.T) {
	w := DeliveryWarning(DeliveryStalled, 24, 24)
	for _, forbidden := range []string{"服务器已挂", "被墙", "被封", "服务器故障"} {
		if strings.Contains(w.Detail, forbidden) || strings.Contains(w.Hint, forbidden) {
			t.Errorf("断言了一个 bx 分辨不出来的原因 %q:%s / %s", forbidden, w.Detail, w.Hint)
		}
	}
	if !strings.Contains(w.Detail, "24") {
		t.Errorf("没有把观测到的数字说出来:%s", w.Detail)
	}
}

// **severity 是 warn 不是 error,而且这是刻意的。**
// error 会把总状态降级成 Needs Attention,而这条判据真机上一次都没跑过 ——
// 阈值取自推理不是数据。先让它把话说出来、跑一段,拿到证据再决定升级。
// 这条守卫钉住的是那个「刻意的中间态」,免得有人顺手改掉而没有新证据。
func TestDeliveryWarningStaysAdvisoryUntilThereIsRealMachineEvidence(t *testing.T) {
	if w := DeliveryWarning(DeliveryStalled, 24, 24); w.Severity != "warn" {
		t.Errorf("severity=%q —— 升级成 error 会让总状态降级,而这条判据还没有真机证据支撑它的阈值", w.Severity)
	}
}
