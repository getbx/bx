package observe

import "fmt"

// Divergence 是一条「信念与事实不符」的记录。
//
// 它是本包给 agent(含用户自己的 AI)的核心产物:自解释、可操作。今天这个信号
// 存在于内核里但无人读取。
type Divergence struct {
	Field    string `json:"field"`
	Believed string `json:"believed"`
	Observed string `json:"observed"`
	Note     string `json:"note"`
}

// Diverge 比对意图、观测与信念,产出所有不一致。一致时返回空切片。
//
// 刻意不做的事:不修正信念、不改动系统、不排序优先级。它只陈述差异。
func Diverge(intent Intent, observed ObservedState, believed Believed) []Divergence {
	var out []Divergence

	// 挂起用**观测那一刻**的时间判过期,不用 time.Now():这份判断要与它旁边
	// 那些事实说的是同一个瞬间。
	//
	// `After` 而不是 `!Before`:到期时刻恰好等于观测时刻的那张挂起已经不算数
	// (与 guardian.LoadMaintenanceHold 的 `hold.ExpiresAt.After(now)` 同一条边界,
	// 两边分叉会让 Guardian 说「还武装着」而 bx status 说「过期了」)。
	held := intent.Hold != nil && intent.Hold.ExpiresAt.After(observed.ObservedAt)

	if believed.Protection == "protected" {
		// 声称已保护时,三项观测必须皆为 True。Unknown 不满足:不确定不等于正常。
		for _, check := range []struct {
			field string
			value Tristate
			note  string
		}{
			{"capture_ok", observed.CaptureOK, "保护状态声称已保护,但未观测到流量被劫持进 bx 的 TUN"},
			{"dns_managed", observed.DNSManaged, "保护状态声称已保护,但未观测到系统 DNS 由 bx 接管"},
			{"tunnel_healthy", observed.TunnelHealthy, "保护状态声称已保护,但未观测到隧道健康"},
		} {
			if check.value != True {
				out = append(out, Divergence{
					Field:    check.field,
					Believed: "protected",
					Observed: check.value.String(),
					Note:     check.note,
				})
			}
		}
	}

	// 挂起武装期间不谈残留:升级正在进行,屏障与 DNS 接管此刻本来就还在。
	// 这一支真正会在维护窗口里触发的是**退回路径**(挂起写不成时改写 desired=off)。
	if intent.Desired == "off" && !held {
		if observed.BarrierPresent == True {
			out = append(out, Divergence{
				Field:    "barrier_present",
				Believed: "off",
				Observed: "true",
				Note:     "意图是关闭,但内核里仍有 bx 装的阻断路由;它们会让整机断网且不随 bx 退出而消失",
			})
		}
		if observed.DNSManaged == True {
			out = append(out, Divergence{
				Field:    "dns_managed",
				Believed: "off",
				Observed: "true",
				Note:     "意图是关闭,但系统 DNS 仍指向 bx;若 bx 已退出,域名解析会全部失败",
			})
		}
	}

	// 用户要保护,而 Core 的控制 socket 不应答 —— **挂起期间这是预期,过期之后
	// 这就是故障**。设计取舍五:过期不会恢复保护,它买到的是「不再压制」,于是
	// 机器不再「看起来正确地关着」,而是如实地看起来坏了。
	//
	// **判据是 `== False` 而不是 `!= True`**,与上面 protected 那三项刻意相反,
	// 而两边都对:那三项是「声称已经做到了」,未经证实就不许算数;这一条是
	// 「指控系统坏了」,没问出来就不许指控。写成 `!= True` 会让每一台观测原语
	// 缺席的机器恒吐一条「没有保护在跑」—— 正是 observerForPlatform 那道门在
	// 防的噪声。
	if intent.Desired == "on" && !held && observed.CoreSocket == False {
		out = append(out, Divergence{
			Field:    "core_socket",
			Believed: "on",
			Observed: "false",
			Note:     "意图是保护,但 Core 的控制 socket 不应答:此刻没有保护在跑",
		})
	}

	for _, e := range observed.Errors {
		out = append(out, Divergence{
			Field:    e.Item,
			Believed: believed.Protection,
			Observed: "unknown",
			Note:     fmt.Sprintf("该项无法观测:%s", e.Err),
		})
	}

	return out
}
