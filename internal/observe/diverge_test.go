package observe

import (
	"strings"
	"testing"
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
