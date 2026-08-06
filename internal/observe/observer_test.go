package observe

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fixedDeps() Deps {
	return Deps{
		TunName:      func() (string, error) { return "utun4", nil },
		BarrierCIDRs: func() ([]string, []string) { return []string{"0.0.0.0/2"}, nil },
		LookupRoute: func(context.Context, string, bool) (RouteResult, error) {
			return RouteResult{Interface: "utun4"}, nil
		},
		InspectDNS: func(context.Context) (DNSResult, error) {
			return DNSResult{Servers: []string{"127.0.0.1"}, Enabled: true}, nil
		},
		FetchRuntime: func() (RuntimeResult, error) { return RuntimeResult{TunnelHealthy: true}, nil },
		Now:          func() time.Time { return time.Unix(1700000000, 0) },
	}
}

func TestObserveReportsHealthySystem(t *testing.T) {
	got := Observe(context.Background(), fixedDeps())
	if got.CaptureOK != True {
		t.Errorf("CaptureOK = %v, want True", got.CaptureOK)
	}
	if got.DNSManaged != True {
		t.Errorf("DNSManaged = %v, want True", got.DNSManaged)
	}
	if got.TunnelHealthy != True {
		t.Errorf("TunnelHealthy = %v, want True", got.TunnelHealthy)
	}
	if got.CoreSocket != True {
		t.Errorf("CoreSocket = %v, want True", got.CoreSocket)
	}
	if got.ObservedAt.IsZero() {
		t.Error("必须带观测时刻,消费者要据此判断新鲜度")
	}
}

// 劫持没生效时必须观测到 False,而不是沿用上次的记忆。
func TestObserveDetectsCaptureNotEffective(t *testing.T) {
	deps := fixedDeps()
	deps.LookupRoute = func(context.Context, string, bool) (RouteResult, error) {
		return RouteResult{Interface: "en0"}, nil
	}
	got := Observe(context.Background(), deps)
	if got.CaptureOK != False {
		t.Errorf("包走 en0 而非 TUN 时 CaptureOK 必须为 False,实际 = %v", got.CaptureOK)
	}
	if got.CaptureInterface != "en0" {
		t.Errorf("必须如实报告实际接口,实际 = %q", got.CaptureInterface)
	}
}

// 单项观测失败必须记为 Unknown 并附错误,且不得影响其余项。
func TestObserveIsolatesFailurePerItem(t *testing.T) {
	deps := fixedDeps()
	deps.InspectDNS = func(context.Context) (DNSResult, error) {
		return DNSResult{}, errors.New("networksetup timed out")
	}
	got := Observe(context.Background(), deps)
	if got.DNSManaged != Unknown {
		t.Errorf("DNS 观测失败时必须为 Unknown,实际 = %v", got.DNSManaged)
	}
	if got.CaptureOK != True {
		t.Errorf("一项失败不得影响其余项,CaptureOK = %v, want True", got.CaptureOK)
	}
	if len(got.Errors) == 0 {
		t.Error("必须记录失败原因,否则 Unknown 无从解释")
	}
}

// Core socket 不应答时,隧道健康必须是 Unknown 而不是 False——
// 问不出来不等于不健康。
func TestObserveUnknownTunnelWhenSocketSilent(t *testing.T) {
	deps := fixedDeps()
	deps.FetchRuntime = func() (RuntimeResult, error) { return RuntimeResult{}, errors.New("no such file") }
	got := Observe(context.Background(), deps)
	if got.CoreSocket != False {
		t.Errorf("socket 不应答时 CoreSocket = %v, want False", got.CoreSocket)
	}
	if got.TunnelHealthy != Unknown {
		t.Errorf("socket 不应答时隧道健康应为 Unknown(问不出来),实际 = %v", got.TunnelHealthy)
	}
}

// TUN 不存在(bx 未运行)时,劫持观测为 False 而非 Unknown:
// 没有 TUN 就是确定没劫持。
func TestObserveCaptureFalseWhenNoTun(t *testing.T) {
	deps := fixedDeps()
	deps.TunName = func() (string, error) { return "", nil }
	got := Observe(context.Background(), deps)
	if got.CaptureOK != False {
		t.Errorf("无 TUN 时 CaptureOK = %v, want False", got.CaptureOK)
	}
}

// 但「问不出 TUN 叫什么」与「确定没有 TUN」是两回事。生产接线里 TUN 名字取自
// Core 的控制 socket——socket 不应答时若把它当成"没有 TUN",就会报出一个自信的
// CaptureOK=False,而真相可能是 Core 活着、路由也在。这正是本包要消灭的谎言,
// 所以 TunName 必须能表达失败。
func TestObserveUnknownCaptureWhenTunNameUnavailable(t *testing.T) {
	deps := fixedDeps()
	deps.TunName = func() (string, error) { return "", errors.New("control socket silent") }
	got := Observe(context.Background(), deps)
	if got.CaptureOK != Unknown {
		t.Errorf("问不出 TUN 名字时 CaptureOK = %v, want Unknown", got.CaptureOK)
	}
	if len(got.Errors) == 0 {
		t.Error("必须记录失败原因,否则 Unknown 无从解释")
	}
}

// 屏障网段被内核以 reject 应答 = 屏障在位。desired=off 时这条观测是唯一能
// 发现"孤儿阻断路由"的手段。
func TestObserveDetectsBarrierPresent(t *testing.T) {
	deps := fixedDeps()
	deps.LookupRoute = func(_ context.Context, destination string, _ bool) (RouteResult, error) {
		if destination == "0.0.0.0" {
			return RouteResult{Interface: "lo0", Reject: true}, nil
		}
		return RouteResult{Interface: "utun4"}, nil
	}
	got := Observe(context.Background(), deps)
	if got.BarrierPresent != True {
		t.Errorf("内核对屏障网段应答 reject 时 BarrierPresent = %v, want True", got.BarrierPresent)
	}
}
