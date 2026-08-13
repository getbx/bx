package observe

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// **「这个平台上没有这个问题」与「没问出来」是两回事,而此前只有后者可表达。**
//
// Linux 上 bx **不改系统 DNS**(它在 TUN 里拦 UDP:53),所以 `dns_managed` 那个问题
// 在那里根本不成立:报 False 像「明明受保护却说没接管」一样撒谎,报 Unknown 则会让
// 每一台健康的 Linux 机器每次观测都吐一条永久 divergence —— 而 CLAUDE.md 早就写过,
// 满屏「无法观测」会把 divergence 训练成用户和 agent 学会忽略的东西,
// 正好毁掉它唯一的价值。
func TestNotApplicableItemsAreNeitherUnobservableNorDivergent(t *testing.T) {
	state := ObservedState{
		CaptureOK:     True,
		TunnelHealthy: True,
		CoreSocket:    True,
		// 平台上不成立的那一项:值仍是零值 Unknown,但被显式标出。
		NotApplicable: []string{"dns_managed"},
	}

	// ① 不进「没问出来」清单 —— 那份清单是给「本该问、却没问到」用的。
	for _, item := range state.UnobservableItems() {
		if item == "dns_managed" {
			t.Errorf("不适用的项被算成了「没问出来」:%v", state.UnobservableItems())
		}
	}

	// ② 声称已保护时不产生 divergence —— 否则每台健康的 Linux 机器恒报一条。
	divs := Diverge(Intent{Desired: "on"}, state, Believed{Protection: "protected"})
	for _, d := range divs {
		if d.Field == "dns_managed" {
			t.Errorf("不适用的项产生了 divergence:%+v", d)
		}
	}

	// ③ 但**真正没问出来**的项照旧要报 —— 否则这个机制会变成藏问题的地方。
	blind := ObservedState{CaptureOK: Unknown, NotApplicable: []string{"dns_managed"}}
	items := strings.Join(blind.UnobservableItems(), ",")
	if !strings.Contains(items, "capture_ok") {
		t.Errorf("真正没问出来的项被漏掉了:%q", items)
	}
	divs2 := Diverge(Intent{Desired: "on"}, blind, Believed{Protection: "protected"})
	var sawCapture bool
	for _, d := range divs2 {
		if d.Field == "capture_ok" {
			sawCapture = true
		}
	}
	if !sawCapture {
		t.Error("声称已保护却观测不到劫持,必须照旧报 divergence")
	}
}

// **不适用的项不去问,连错误都不留。**
//
// 问了只会在每一轮的 Errors 里留下同一条永久记录(「本平台不支持 DNS 接管观测」),
// 而那与满屏「无法观测」是同一种噪声:把一个静态的平台事实伪装成每次调用都新发生
// 的失败。真 Linux 容器里第一次跑观测时看到的就是这条。
func TestNotApplicableProbesAreNotEvenAttempted(t *testing.T) {
	asked := false
	deps := Deps{
		NotApplicable: []string{"dns_managed"},
		InspectDNS: func(ctx context.Context) (DNSResult, error) {
			asked = true
			return DNSResult{}, errors.New("本平台不支持 DNS 接管观测")
		},
	}
	state := Observe(context.Background(), deps)
	if asked {
		t.Error("不适用的项仍然被问了 —— 每一轮都会留下同一条永久错误")
	}
	for _, e := range state.Errors {
		if e.Item == "dns_managed" {
			t.Errorf("不适用的项留下了错误记录:%+v", e)
		}
	}
}
