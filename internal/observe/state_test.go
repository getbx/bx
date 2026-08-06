package observe

import "testing"

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
