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

	if intent.Desired == "off" {
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
