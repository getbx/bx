// Package acceptance 回答一个问题:**这台机器上,这一版真的在工作吗?**
//
// 它不是 doctor(那问「装没装好」),也不是 status(那问「此刻怎么样」)。
// 它问的是「刚发布的这批东西,有没有真的到达这台机器并生效」—— 而这个仓库里
// 「真机未验」的清单已经长到没人愿意手工走一遍,于是它永远不被走。
//
// # 三条纪律
//
//  1. **「没问出来」是一等结果,而且是零值。** 一份把「查不到」显示成「通过」的
//     验收报告,比没有验收更糟:它会让人相信一件没被验证的事。
//  2. **只读。** 它一个字节都不改。要改动的步骤由它**打印出来给人自己敲** ——
//     项目所有者的机器是生产环境,这条是硬纪律。
//  3. **验不了的要明说验不了。** 有些事按构造在只读、单次运行下不可验(比如
//     「UDP 现在遵守 direct 规则」要一份升级前的基线)。列出来,而不是省略 ——
//     省略会让人以为清单是完整的。
package acceptance

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/stats"
)

// Verdict 是一条检查的结论。**零值是 Unknown**,与本仓库 Tristate 同一条纪律:
// 漏填的代价必须是「多报一个不确定」,而不是「多报一个通过」。
type Verdict int

const (
	Unknown Verdict = iota
	Pass
	Fail
)

func (v Verdict) String() string {
	switch v {
	case Pass:
		return "PASS"
	case Fail:
		return "FAIL"
	default:
		return "UNKNOWN"
	}
}

// Check 是一条检查的结果。Why 只在 Unknown 时有意义:**为什么没问出来**。
type Check struct {
	Name    string
	Verdict Verdict
	Detail  string
	Why     string
}

// Facts 是一次采集到的全部事实。每一项都带自己的错误 —— 一项问不出来绝不能
// 影响其余(与 internal/observe 同源)。
type Facts struct {
	Status     guardian.Status
	StatusErr  error
	Report     stats.Report
	ReportErr  error
	Servers    guardian.ServerListResponse
	ServersErr error
	// ThroughputHistory 是盘上那份历史里的记录条数;ThroughputErr 非空表示读不到。
	ThroughputEntries int
	ThroughputErr     error
	// CoreUptime 是 Core 已经跑了多久。有些检查在刚启动时**按构造**没有答案,
	// 那时必须报 Unknown 而不是 Fail(报 Fail 会训练人忽略这份报告)。
	CoreUptime time.Duration
}

// RequiredCapabilities 是这一版 Guardian 应当声明的能力。
//
// 少一个的后果不是崩溃,而是**界面安静地少一块**:菜单按能力键决定画不画服务器
// 与规则入口,键缺席时它什么都不画,而用户以为这一版就没有这个功能。
var RequiredCapabilities = []string{
	guardian.CapabilityRules,
	guardian.CapabilityServers,
	guardian.CapabilityMaintenanceHold,
	guardian.CapabilityReconcileReport,
}

// Run 跑完全部检查。**顺序是给人读的**:先「通不通」,再「在不在」,最后「对不对」。
func Run(f Facts) []Check {
	checks := []Check{guardianCheck(f), capabilityCheck(f), serverListCheck(f)}
	checks = append(checks, throughputCheck(f), trafficCheck(f), udpCountingCheck(f))
	return checks
}

func guardianCheck(f Facts) Check {
	if f.StatusErr != nil {
		return Check{
			Name: "Guardian 可达", Verdict: Fail,
			Detail: f.StatusErr.Error(),
			Why:    "没有它,下面每一条都问不出来",
		}
	}
	return Check{Name: "Guardian 可达", Verdict: Pass, Detail: string(f.Status.Protection)}
}

func capabilityCheck(f Facts) Check {
	if f.StatusErr != nil {
		return unknown("能力声明", "Guardian 不可达")
	}
	// **能力键整个缺席 = 旧版 Guardian**,与「声明了但为空」是两回事:
	// 前者说明装上去的还是老二进制,那正是升级最常见的失败方式。
	if f.Status.Capabilities == nil {
		return Check{
			Name: "能力声明", Verdict: Fail,
			Detail: "一个都没声明 —— 跑着的多半还是升级前那个 Guardian",
		}
	}
	have := map[string]bool{}
	for _, c := range f.Status.Capabilities {
		have[c] = true
	}
	var missing []string
	for _, want := range RequiredCapabilities {
		if !have[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return Check{
			Name: "能力声明", Verdict: Fail,
			Detail: "缺:" + strings.Join(missing, "、") + " —— 菜单会安静地少掉对应入口",
		}
	}
	return Check{Name: "能力声明", Verdict: Pass, Detail: fmt.Sprintf("%d 项齐全", len(RequiredCapabilities))}
}

func serverListCheck(f Facts) Check {
	if f.ServersErr != nil {
		return unknown("服务器清单", f.ServersErr.Error())
	}
	if len(f.Servers.Servers) == 0 {
		// 单服务器配置是合法状态,不是失败 —— 但也不能报 Pass,因为这一条
		// 什么都没验证到。
		return unknown("服务器清单", "配置里没有 servers 清单(单服务器配置,合法)")
	}
	var current string
	for _, s := range f.Servers.Servers {
		if s.Current {
			current = s.Name
		}
	}
	if current == "" {
		return Check{
			Name: "服务器清单", Verdict: Fail,
			Detail: fmt.Sprintf("%d 台,但没有一台被标为当前在用", len(f.Servers.Servers)),
		}
	}
	return Check{
		Name: "服务器清单", Verdict: Pass,
		Detail: fmt.Sprintf("%d 台,当前 %s", len(f.Servers.Servers), current),
	}
}

// throughputMinUptime 是吞吐历史开始有内容所需的最短运行时间。
//
// 调谐环基础周期 30 秒、退避到 10 分钟;而 Core 那边的峰值要先有人产生流量。
// 取 15 分钟:比它短就**按构造**答不出来,那时报 Fail 是在骂一台正常的机器。
const throughputMinUptime = 15 * time.Minute

func throughputCheck(f Facts) Check {
	if f.ThroughputErr != nil {
		if f.CoreUptime > 0 && f.CoreUptime < throughputMinUptime {
			return unknown("吞吐历史", fmt.Sprintf("才跑了 %s,还没到攒得出数据的时候", f.CoreUptime.Round(time.Minute)))
		}
		// **「没权限读」要给出下一步。** 那个文件是 0600 root(里面是服务器名),
		// 而这个工具刻意以普通用户跑 —— 一句 permission denied 加一句 sudo,
		// 比让人自己去猜要跑什么强。
		if errors.Is(f.ThroughputErr, fs.ErrPermission) {
			return unknown("吞吐历史", "没权限读(它是 0600 root);想验这一条:sudo go run ./cmd/bx-acceptance")
		}
		return unknown("吞吐历史", f.ThroughputErr.Error())
	}
	if f.ThroughputEntries == 0 {
		if f.CoreUptime > 0 && f.CoreUptime < throughputMinUptime {
			return unknown("吞吐历史", fmt.Sprintf("才跑了 %s,还没到攒得出数据的时候", f.CoreUptime.Round(time.Minute)))
		}
		return Check{
			Name: "吞吐历史", Verdict: Fail,
			Detail: "跑了这么久还是空的 —— 调谐环那一步可能没接上",
		}
	}
	return Check{Name: "吞吐历史", Verdict: Pass, Detail: fmt.Sprintf("%d 台有记录", f.ThroughputEntries)}
}

func trafficCheck(f Facts) Check {
	if f.ReportErr != nil {
		return unknown("流量成败", f.ReportErr.Error())
	}
	// **判据与 status / doctor 同源。** 这里再写一份阈值,就会出现三处各说各话。
	if failing := f.Report.FailingRules(); len(failing) > 0 {
		names := make([]string, 0, len(failing))
		for _, r := range failing {
			names = append(names, fmt.Sprintf("%s(%d/%d 失败)", r.Rule, r.Failures, r.Attempts))
		}
		return Check{Name: "流量成败", Verdict: Fail, Detail: strings.Join(names, "、")}
	}
	total := f.Report.Direct + f.Report.Proxy
	if total == 0 {
		return unknown("流量成败", "还没有任何连接经过 —— 用一会儿再看")
	}
	return Check{
		Name: "流量成败", Verdict: Pass,
		Detail: fmt.Sprintf("直连 %d(失败 %d)· 代理 %d(失败 %d)",
			f.Report.Direct, f.Report.DirectFailed, f.Report.Proxy, f.Report.ProxyFailed),
	}
}

// udpCountingCheck 证明**新的 UDP 计数真的到了这台机器上**。
//
// 它是这份验收里唯一一条能证明「新二进制在跑」的行为证据:反事实那几个桶是
// 这一版才有的,旧版一条都不会产出。版本号只证明文件换了,这个证明进程换了。
func udpCountingCheck(f Facts) Check {
	if f.ReportErr != nil {
		return unknown("UDP 计数已生效", f.ReportErr.Error())
	}
	var buckets int64
	for _, r := range f.Report.Rules {
		if strings.HasPrefix(r.Source, "udp_would_flip") ||
			r.Source == "udp_undecidable" || r.Source == "udp_no_change" {
			buckets += r.Attempts
		}
	}
	if buckets == 0 {
		// 一台完全没有 UDP 流量的机器是可能的(极少见),所以这不是 Fail。
		return unknown("UDP 计数已生效", "还没有 UDP 连接经过 —— 打开一个网页再看")
	}
	return Check{Name: "UDP 计数已生效", Verdict: Pass, Detail: fmt.Sprintf("%d 条已归桶", buckets)}
}

func unknown(name, why string) Check {
	return Check{Name: name, Verdict: Unknown, Why: why}
}

// NotCheckableReadOnly 列出**按构造在只读、单次运行下验不了**的事。
//
// 列出来而不是省略:省略会让人以为这份清单是完整的,而那正是一份验收报告最坏的
// 失败方式 —— 它让人相信一件没被验证的事。
func NotCheckableReadOnly() []string {
	return []string{
		"UDP 现在遵守 direct 规则:要一份升级前的基线才比得出来。" +
			"手工验:开一个只走 QUIC 的站,看 `bx status` 里那条规则的 attempts 有没有涨。",
		"换服务器不再撒谎(武装→验证→确认):要真切一次,而切换会改出口 IP。",
		"菜单栏那几个窗口:要人在屏幕前点一下。",
		"装新 VPS 的表单:要一台空 VPS。",
	}
}

// Render 把结果写成人读的样子。**失败排最前** —— 人打开这份报告十有八九是因为
// 有东西不对,而不对的那条不该排在第七行。
func Render(checks []Check, notCheckable []string) string {
	var b strings.Builder
	b.WriteString("bx 验收(只读,不改动任何东西)\n\n")
	for _, want := range []Verdict{Fail, Unknown, Pass} {
		for _, c := range checks {
			if c.Verdict != want {
				continue
			}
			fmt.Fprintf(&b, "  %-8s%-16s%s\n", c.Verdict, c.Name, detailOf(c))
		}
	}
	if len(notCheckable) > 0 {
		b.WriteString("\n只读验不了的(**不是通过**,是没验):\n")
		for _, item := range notCheckable {
			fmt.Fprintf(&b, "  · %s\n", item)
		}
	}
	return b.String()
}

func detailOf(c Check) string {
	if c.Verdict == Unknown {
		return c.Why
	}
	return c.Detail
}

// ExitCode:**只有 Fail 才非零。** Unknown 不是失败 —— 把「没问出来」变成非零
// 退出码,会让人在一台正常但刚启动的机器上以为出了事,而后就再也不跑这个工具了。
func ExitCode(checks []Check) int {
	for _, c := range checks {
		if c.Verdict == Fail {
			return 1
		}
	}
	return 0
}
