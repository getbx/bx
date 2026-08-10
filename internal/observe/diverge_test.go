package observe

import (
	"strings"
	"testing"
	"time"
)

// 不变量 3:声称 protected 就必须三项观测皆为 True。Unknown 不满足——
// 不确定不等于正常。真实事故里"status 显绿而流量明文直连"正是这么产生的。
func TestDivergeFlagsProtectedWithoutCapture(t *testing.T) {
	got := Diverge(
		Intent{Desired: "on"},
		ObservedState{CaptureOK: False, DNSManaged: True, TunnelHealthy: True},
		Believed{Protection: "protected"},
	)
	if !hasField(got, "capture_ok") {
		t.Errorf("声称 protected 但劫持未生效必须产出 divergence,实际 = %+v", got)
	}
}

func TestDivergeTreatsUnknownAsNotSatisfyingProtected(t *testing.T) {
	got := Diverge(
		Intent{Desired: "on"},
		ObservedState{CaptureOK: Unknown, DNSManaged: True, TunnelHealthy: True},
		Believed{Protection: "protected"},
	)
	if !hasField(got, "capture_ok") {
		t.Errorf("观测不到时不得当作满足 protected,实际 = %+v", got)
	}
}

// 不变量 1:desired=off 时不得残留屏障路由。真实事故:强制拆除后 8 条 /2
// reject 路由成为无主孤儿,整机断网且 bx 报绿。
func TestDivergeFlagsBarrierResidueWhenDesiredOff(t *testing.T) {
	got := Diverge(
		Intent{Desired: "off"},
		ObservedState{BarrierPresent: True},
		Believed{Protection: "off"},
	)
	if !hasField(got, "barrier_present") {
		t.Errorf("desired=off 却观测到屏障残留必须产出 divergence,实际 = %+v", got)
	}
}

// 不变量 2:desired=off 时系统 DNS 不得仍是 bx 的。真实事故:强制拆除后
// DNS 仍指 127.0.0.1 而监听者已退出,用户"网页打不开"。
func TestDivergeFlagsDNSResidueWhenDesiredOff(t *testing.T) {
	got := Diverge(
		Intent{Desired: "off"},
		ObservedState{DNSManaged: True},
		Believed{Protection: "off"},
	)
	if !hasField(got, "dns_managed") {
		t.Errorf("desired=off 却观测到 DNS 仍被接管必须产出 divergence,实际 = %+v", got)
	}
}

// 一致时必须安静:否则 divergence 会被噪声淹没,失去诊断价值。
func TestDivergeSilentWhenConsistent(t *testing.T) {
	got := Diverge(
		Intent{Desired: "on"},
		ObservedState{CaptureOK: True, DNSManaged: True, TunnelHealthy: True, BarrierPresent: False},
		Believed{Protection: "protected"},
	)
	if len(got) != 0 {
		t.Errorf("观测与信念一致时不应产出 divergence,实际 = %+v", got)
	}
}

// 不变量 4:每条 divergence 必须自解释——agent 没有直觉,note 是它唯一的上下文。
func TestDivergenceCarriesSelfExplainingNote(t *testing.T) {
	got := Diverge(
		Intent{Desired: "on"},
		ObservedState{CaptureOK: False, DNSManaged: True, TunnelHealthy: True},
		Believed{Protection: "protected"},
	)
	for _, d := range got {
		if strings.TrimSpace(d.Note) == "" {
			t.Errorf("divergence 必须带自解释的 note,实际 = %+v", d)
		}
		if d.Observed == "" || d.Believed == "" {
			t.Errorf("divergence 必须同时给出观测值与信念值,实际 = %+v", d)
		}
	}
}

func hasField(list []Divergence, field string) bool {
	for _, d := range list {
		if d.Field == field {
			return true
		}
	}
	return false
}

// 维护挂起期间,残留是**预期之内**的:升级正在进行。不告诉 Diverge 就会冒出
// 一条假分歧,而 divergence 一旦被训练成噪声,它唯一的价值就没了。
func TestDivergeStaysQuietDuringArmedHold(t *testing.T) {
	now := time.Now()
	got := Diverge(
		Intent{Desired: "off", Hold: &HoldIntent{Reason: "upgrade", ExpiresAt: now.Add(5 * time.Minute)}},
		ObservedState{ObservedAt: now, BarrierPresent: True, DNSManaged: True, CoreSocket: False},
		Believed{Protection: "off"},
	)
	if len(got) != 0 {
		t.Fatalf("挂起期间不该有分歧: %+v", got)
	}
}

// **同一份输入,只把到期时刻挪到过去** —— 挂起失效之后必须重新说话。
// 只在一半输入空间上断言的不变量等于没断言。
func TestDivergeSpeaksUpOnceTheHoldExpires(t *testing.T) {
	now := time.Now()
	got := Diverge(
		Intent{Desired: "off", Hold: &HoldIntent{Reason: "upgrade", ExpiresAt: now.Add(-time.Minute)}},
		ObservedState{ObservedAt: now, BarrierPresent: True, DNSManaged: True, CoreSocket: False},
		Believed{Protection: "off"},
	)
	if len(got) == 0 {
		t.Fatal("过期的挂起还在压制分歧")
	}
}

// 挂起过期之后,「用户要保护而 Core 不在」必须现形 —— 这正是挂起强于欠条的地方:
// 欠条让机器看起来是关的,挂起让它看起来是坏的,而它确实是坏的。
func TestDivergeReportsMissingProtectionWhenDesiredOnAndNoHold(t *testing.T) {
	now := time.Now()
	got := Diverge(
		Intent{Desired: "on"},
		ObservedState{ObservedAt: now, CoreSocket: False},
		Believed{Protection: "off"},
	)
	if !hasField(got, "core_socket") {
		t.Fatalf("desired=on 而 Core 不应答,必须报一条分歧: %+v", got)
	}
}

// 而挂起武装着的时候,同一件事**不是**分歧。
func TestDivergeDoesNotReportMissingProtectionUnderHold(t *testing.T) {
	now := time.Now()
	got := Diverge(
		Intent{Desired: "on", Hold: &HoldIntent{Reason: "upgrade", ExpiresAt: now.Add(time.Minute)}},
		ObservedState{ObservedAt: now, CoreSocket: False},
		Believed{Protection: "off"},
	)
	if hasField(got, "core_socket") {
		t.Fatalf("挂起期间报了假分歧: %+v", got)
	}
}

// 「问不出来」不是「不在」:CoreSocket 为 Unknown 时不得报这条分歧。
//
// 与 protected 那三项刻意相反,而两边都对:那三项是**声称已经做到了**,
// 未经证实就不许算数;这一条是**指控系统坏了**,没问出来就不许指控。
// 判据写成 `!= True` 会让每一台观测原语缺席的机器恒吐一条「没有保护在跑」,
// 正是 observerForPlatform 那道门在防的噪声。
func TestDivergeStaysQuietWhenCoreSocketIsUnknown(t *testing.T) {
	now := time.Now()
	got := Diverge(
		Intent{Desired: "on"},
		ObservedState{ObservedAt: now, CoreSocket: Unknown},
		Believed{Protection: "off"},
	)
	if hasField(got, "core_socket") {
		t.Fatalf("观测不到不等于观测到不在: %+v", got)
	}
}

// 到期时刻**恰好等于**观测时刻的那张挂起已经不算数了。
//
// 判据写在绝对值上而不是「比 ExpiresAt 早一点」:MaintenanceHoldDuration 缩短
// 时这条 fixture 不会跟着挪,而那正是本计划点名的「fixture 写成相对于被钉住的
// 那个值,于是把它改小照样绿」。
func TestDivergeTreatsHoldExpiringExactlyNowAsExpired(t *testing.T) {
	now := time.Now()
	got := Diverge(
		Intent{Desired: "on", Hold: &HoldIntent{Reason: "upgrade", ExpiresAt: now}},
		ObservedState{ObservedAt: now, CoreSocket: False},
		Believed{Protection: "off"},
	)
	if !hasField(got, "core_socket") {
		t.Fatalf("到期时刻与观测时刻相等的挂起必须已失效: %+v", got)
	}
}
