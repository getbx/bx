package supervisor

import (
	"sort"
	"strings"

	"github.com/getbx/bx/internal/overlay"
	"github.com/getbx/bx/internal/stats"
)

// tenantsAppearedSince 返回启动之后才出现的租户名。
//
// **只报新增,不报消失。** 用户关掉 Tailscale 不是异常,而一条「它不见了」的告警会在
// 每次关掉之后常驻 —— 把这一行训练成噪声,而它的全部价值在于平时不出现。
func tenantsAppearedSince(startup, now []overlay.Tenant) []string {
	had := map[string]bool{}
	for _, t := range startup {
		had[t.Name] = true
	}
	var out []string
	for _, t := range now {
		if !had[t.Name] {
			out = append(out, t.Name)
		}
	}
	sort.Strings(out)
	return out
}

// lateTenantWarning 把「有个 overlay 是 bx 起来之后才跑的」翻成一条能照着做的告警。
//
// **它必须说清楚还差什么。** 两半的处境不一样:
//   - 中继旁路会自己跟上 —— 重试循环每轮重新检测,新值在下一次路由重装时生效;
//   - DNS split 不会 —— dns.Server 的 SetSplit 是无锁赋值,今天安全只因为它只在启动时
//     调一次;运行期再调是 data race。要热更新得先给 dns.Server 加同步,那是数据面
//     改动,不该为一个共存便利顺手做。
//
// 所以出路是「重启 bx」,而这一行必须把它说出来 —— 一条不给出路的告警只是噪声。
func lateTenantWarning(names []string) stats.Warning {
	if len(names) == 0 {
		return stats.Warning{}
	}
	joined := strings.Join(names, ", ")
	return stats.Warning{
		Name:     "overlay_started_late",
		Severity: "warn",
		Detail: joined + " started after bx did; its relay bypass will catch up on the next " +
			"route reinstall, but its DNS namespace is not being resolved",
		Hint: "restart bx (bx down && bx up) to give " + joined + " its DNS split",
	}
}
