package observe

import (
	"context"
	"testing"
	"time"
)

// 「观测不到」与「观测到否」必须可区分——用 bool 会把「问不出来」压成 false,
// 而这正是当前架构骗人的方式之一(RoutesInstalled 是个只置位不复查的 atomic.Bool)。
func TestTristateDistinguishesUnknownFromFalse(t *testing.T) {
	if Unknown == False {
		t.Fatal("Unknown 必须与 False 不同")
	}
	if Unknown == True {
		t.Fatal("Unknown 必须与 True 不同")
	}
	cases := map[Tristate]string{Unknown: "unknown", True: "true", False: "false"}
	for value, want := range cases {
		if got := value.String(); got != want {
			t.Errorf("Tristate(%d).String() = %q, want %q", value, got, want)
		}
	}
}

// 零值必须是 Unknown:未填写的观测项不得冒充"正常"。
func TestTristateZeroValueIsUnknown(t *testing.T) {
	var zero Tristate
	if zero != Unknown {
		t.Errorf("零值 = %v, want Unknown——未观测的项绝不能默认为 True/False", zero)
	}
	var state ObservedState
	if state.CaptureOK != Unknown || state.BarrierPresent != Unknown ||
		state.DNSManaged != Unknown || state.CoreSocket != Unknown || state.TunnelHealthy != Unknown {
		t.Errorf("ObservedState 零值的所有三态字段必须是 Unknown,实际 = %+v", state)
	}
}

// UnobservableItems 是「这一轮没能问出来什么」的**唯一**说法。
//
// 存在的理由:下游(Guardian 的调谐环)对每个 Unknown 都「什么都不做」,于是
// 一台三项探测全失败的机器与一台完全健康的机器产出同一个判断。没有这份清单,
// 「我们从没问过」与「问了、一切正常」在下游读起来一模一样。
func TestUnobservableItemsNamesEveryProbeThatCouldNotBeAnswered(t *testing.T) {
	var blind ObservedState
	want := []string{"capture_ok", "barrier_present", "dns_managed", "core_socket", "tunnel_healthy"}
	got := blind.UnobservableItems()
	if len(got) != len(want) {
		t.Fatalf("五项全 Unknown 时必须五项全报, got %v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("次序必须稳定(它会进日志与状态行), got %v want %v", got, want)
		}
	}

	full := ObservedState{
		CaptureOK: True, BarrierPresent: False, DNSManaged: True,
		CoreSocket: True, TunnelHealthy: False,
	}
	if items := full.UnobservableItems(); len(items) != 0 {
		t.Errorf("每一项都问出来了,不该报「未观测」, got %v", items)
	}

	// **False 绝不能被算成「没问出来」**:那是观测到的结论,不是缺席。
	if items := (ObservedState{CaptureOK: False}).UnobservableItems(); contains(items, "capture_ok") {
		t.Errorf("capture_ok=False 是一条确定的观测, got %v", items)
	}

	// 依赖缺席(Deps 某个函数为 nil)一条 Errors 都不会留,却同样问不出来 ——
	// 按 Errors 推的实现会把这一轮印成「全都问到了」。
	partial := Observe(context.Background(), Deps{Now: func() time.Time { return time.Unix(0, 0) }})
	if items := partial.UnobservableItems(); len(items) != 5 {
		t.Errorf("一个依赖都没接时五项全都问不出来, got %v (errors=%v)", items, partial.Errors)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
