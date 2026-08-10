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

// silentCore 造出**生产真的会产生**的那种「Core 不应答」观测。
//
// 这个 helper 存在的理由是一次真实的假绿:observeCore(observer.go)在
// FetchRuntime 失败时**同时**置 CoreSocket=False **并**记一条 core_socket 的
// ObserveError —— 二者永远成对。手写 `ObservedState{CoreSocket: False}` 造出的是
// 一个生产永远产生不了的状态(Errors 为 nil),于是「挂起期间保持安静」这条断言
// 在一份不可能出现的输入上成立,而在真机上根本不成立。
// Global Constraints 点名的「fixture 里字段全是零值」就是这一类。
func silentCore(now time.Time) ObservedState {
	return ObservedState{
		ObservedAt: now,
		CoreSocket: False,
		Errors: []ObserveError{{
			Item: "core_socket",
			Err:  "dial unix /var/run/bx/core.sock: connect: no such file or directory",
		}},
	}
}

// 维护挂起期间必须**完全**安静:升级正在进行,Core 被有意停掉了。
//
// 用的是升级真实的形状:desired 保持 on(这正是本期的全部意义),观测是
// silentCore 那份成对的事实,外加屏障与 DNS 仍在(Core 刚被停,残留还没清)。
func TestDivergeStaysQuietDuringArmedHold(t *testing.T) {
	now := time.Now()
	observed := silentCore(now)
	observed.BarrierPresent = True
	observed.DNSManaged = True
	got := Diverge(
		Intent{Desired: "on", Hold: &HoldIntent{Reason: "upgrade", ExpiresAt: now.Add(5 * time.Minute)}},
		observed,
		Believed{Protection: "off"},
	)
	if len(got) != 0 {
		t.Fatalf("挂起期间不该有分歧: %+v", got)
	}
}

// desired=off 那一支的 !held 门也要有覆盖。
//
// **可达性**(reviewer 认为这个组合不可达,实际可达,故留此说明):一台用户已经
// 关掉保护的机器上跑升级,recordStopIntent 照样武装挂起 —— 于是 desired=off 与
// 一张武装着的挂起并存。这不是退回路径(退回恰恰发生在武装失败时,那时没有挂起)。
func TestDivergeStaysQuietAboutResidueWhenDesiredOffUnderHold(t *testing.T) {
	now := time.Now()
	got := Diverge(
		Intent{Desired: "off", Hold: &HoldIntent{Reason: "upgrade", ExpiresAt: now.Add(5 * time.Minute)}},
		ObservedState{ObservedAt: now, BarrierPresent: True, DNSManaged: True},
		Believed{Protection: "off"},
	)
	if len(got) != 0 {
		t.Fatalf("挂起期间不该报残留: %+v", got)
	}
}

// **同一份输入,只把到期时刻挪到过去** —— 挂起失效之后必须重新说话。
// 只在一半输入空间上断言的不变量等于没断言。
func TestDivergeSpeaksUpOnceTheHoldExpires(t *testing.T) {
	now := time.Now()
	observed := silentCore(now)
	observed.BarrierPresent = True
	observed.DNSManaged = True
	got := Diverge(
		Intent{Desired: "on", Hold: &HoldIntent{Reason: "upgrade", ExpiresAt: now.Add(-time.Minute)}},
		observed,
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
	got := Diverge(Intent{Desired: "on"}, silentCore(now), Believed{Protection: "off"})
	if !hasField(got, "core_socket") {
		t.Fatalf("desired=on 而 Core 不应答,必须报一条分歧: %+v", got)
	}
}

// 而挂起武装着的时候,同一件事**不是**分歧。
func TestDivergeDoesNotReportMissingProtectionUnderHold(t *testing.T) {
	now := time.Now()
	got := Diverge(
		Intent{Desired: "on", Hold: &HoldIntent{Reason: "upgrade", ExpiresAt: now.Add(time.Minute)}},
		silentCore(now),
		Believed{Protection: "off"},
	)
	if hasField(got, "core_socket") {
		t.Fatalf("挂起期间报了假分歧: %+v", got)
	}
}

// **一个项最多一行,而且不许自相矛盾。**
//
// 生产里 CoreSocket=False 一定伴着一条 core_socket 的 ObserveError,于是过期挂起
// 这条路上曾同时产出 observed=false(「没有保护在跑」)与 observed=unknown
// (「该项无法观测」)—— 同一个 field、相反的 observed。下游按 field 计数
// (Swift 菜单的 anomalyCount)只能重复计数或在 UI 层自己发明去重规则。
func TestDivergeEmitsOneSelfConsistentRowPerField(t *testing.T) {
	now := time.Now()
	got := Diverge(Intent{Desired: "on"}, silentCore(now), Believed{Protection: "off"})
	seen := map[string]int{}
	for _, d := range got {
		seen[d.Field]++
	}
	for field, count := range seen {
		if count > 1 {
			t.Fatalf("field %q 出了 %d 行,下游按 field 计数会重复计:%+v", field, count, got)
		}
	}
	row := got[0]
	if row.Field != "core_socket" || row.Observed != "false" {
		t.Fatalf("赢的该是更具体的那一行(observed=false),实际 %+v", row)
	}
	// 「问不出来的原因」是排查的全部价值,合并之后一个字都不许丢。
	if !strings.Contains(row.Note, "core.sock") {
		t.Fatalf("合并把观测失败的原因丢了:%+v", row)
	}
}

// 同一条规则也修掉一处**既有**的重复:声称 protected 而观测失败时,
// capture_ok 曾同时出「未观测到劫持」与「该项无法观测」两行。
func TestDivergeMergesObservationFailureIntoTheProtectedRow(t *testing.T) {
	got := Diverge(
		Intent{Desired: "on"},
		ObservedState{
			CaptureOK:     Unknown,
			DNSManaged:    True,
			TunnelHealthy: True,
			Errors:        []ObserveError{{Item: "capture_ok", Err: "route: command timed out"}},
		},
		Believed{Protection: "protected"},
	)
	if len(got) != 1 {
		t.Fatalf("capture_ok 该只出一行,实际 %+v", got)
	}
	if !strings.Contains(got[0].Note, "route: command timed out") {
		t.Fatalf("合并后原因丢了:%+v", got[0])
	}
}

// **挂起只解释得了 core_socket,不解释别的观测失败。**
//
// capture_ok / dns_managed / barrier_present 走的是 route 与 networksetup,
// 与 Core 在不在跑无关。挂起期间把整张 Errors 一律压掉,会让一台 route 坏掉的
// 机器在最该出声的 15 分钟里完全沉默。
func TestDivergeStillReportsUnrelatedObservationFailuresUnderHold(t *testing.T) {
	now := time.Now()
	observed := silentCore(now)
	observed.Errors = append(observed.Errors, ObserveError{Item: "capture_ok", Err: "route: command timed out"})
	got := Diverge(
		Intent{Desired: "on", Hold: &HoldIntent{Reason: "upgrade", ExpiresAt: now.Add(time.Minute)}},
		observed,
		Believed{Protection: "off"},
	)
	if hasField(got, "core_socket") {
		t.Fatalf("挂起解释得了 core_socket,不该报:%+v", got)
	}
	if !hasField(got, "capture_ok") {
		t.Fatalf("挂起解释不了 route 坏掉,必须照报:%+v", got)
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
		silentCore(now),
		Believed{Protection: "off"},
	)
	if !hasField(got, "core_socket") {
		t.Fatalf("到期时刻与观测时刻相等的挂起必须已失效: %+v", got)
	}
}
