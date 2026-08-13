//go:build linux

package supervisor

import "testing"

// **接口名判据故意窄。** 只认 `tailscale*`;泛化到 `tun*`/`utun*` 会把别人的
// OpenVPN/WireGuard 接口当成「Tailscale 没就绪」,产生一条永远消不掉的假告警 ——
// 而假告警比没有告警更糟:它把用户训练成忽略这一行,而这一行的全部价值就是平时不出现。
func TestLooksLikeTailscaleInterfaceIsNarrow(t *testing.T) {
	for _, name := range []string{"tailscale0", "tailscale1"} {
		if !looksLikeTailscaleInterface(name) {
			t.Errorf("%q 应认作 Tailscale 接口", name)
		}
	}
	for _, name := range []string{"tun0", "utun3", "wg0", "eth0", "tail", "", "ts0"} {
		if looksLikeTailscaleInterface(name) {
			t.Errorf("%q 不该被认作 Tailscale 接口 —— 把别人的隧道认成它会产生"+
				"一条永远消不掉的假告警", name)
		}
	}
}

// 一台**没有** Tailscale 的机器上必须一个字都不说。这是这条告警存在的前提:
// 它平时不出现,出现时才有人看。
func TestNoTailscaleMeansNoWarningOnThisMachine(t *testing.T) {
	// 本机(CI 容器 / 开发机)通常没有 tailscale 接口;有的话这条用例本身没有意义,
	// 但它仍然不该 panic 或报出别的东西。
	w := linuxTailscaleWarning()
	if w.Name != "" && w.Name != "tailscale" {
		t.Fatalf("只该产出 tailscale 告警或什么都不产出,得到 %+v", w)
	}
	if w.Name != "" && w.Severity != "warn" {
		t.Errorf("共存告警不该是 error 级 —— 它不阻断保护,只是提示:%+v", w)
	}
}
