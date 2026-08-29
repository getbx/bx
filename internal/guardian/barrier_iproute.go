package guardian

import (
	"strings"

	"github.com/getbx/bx/internal/route"
)

// linux 执行器的容错判据(与 darwin 的 isRouteAlreadyExists/isRouteNotInTable
// 同构):装到已存在的是幂等,删到不存在的是幂等,**别的一律如实上报** ——
// 「Operation not permitted」这类真实失败被容错吞掉,屏障就是装了个寂寞而
// 调用方以为 fail-closed 已就位。busybox 与 iproute2 的「不存在」措辞不同
// (No such process / No such file or directory),两个都认,harness 在 busybox 里跑。
func isIPRouteExists(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "file exists")
}

func isIPRouteMissing(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such process") ||
		strings.Contains(msg, "no such file or directory")
}

// isIPv6FamilyUnsupported:内核整族没有 v6(ipv6.disable=1,VPS 与 netns 环境
// 常见)。**只许对 -6 命令容错**:没有 v6 的内核上,v6 阻断要挡的东西按构造
// 不存在,跳过不是 fail-open;而放到 v4 命令上它就会吞掉真实失败。少了这条
// 容错,teardown 里排在前面的 -6 命令会让 v4 的 pref-120 rule 永远清不掉 ——
// 逃生口对着一台黑洞机器恒失败(2026-08-29 code review 抓到;supervisor 的
// linux Hijack 用 /proc/net/if_inet6 探测干的是同一件事)。
func isIPv6FamilyUnsupported(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "address family not supported")
}

func commandIsIPv6(c Command) bool {
	return len(c.Args) > 0 && c.Args[0] == "-6"
}

// linux 屏障计划器(纯函数,无 build tag:计划在哪个 OS 上都该可测)。
// 设计与逐条语义对齐清单见 docs/superpowers/specs/2026-08-29-guardian-linux-adapter-design.md。
//
// 与 darwin 的 PlanBarrier 共读同一份语义源(validateBarrierContext +
// barriercidr;私网 carve 另加 route.DefaultPrivateCIDRs)——「哪些网段阻断、
// 哪些旁路」只有一份答案,防漂移由 TestPlanBarrierLinuxMatchesDarwinSemantics
// 钉住,darwin 计划器因此一行不用动。
//
// 机制:专用 table 90 + pref 120 的 ip rule。三个数字都承重:
//   - pref 120 < 150/200:屏障压过 supervisor 的劫持规则,「屏障压过一切」
//     这条 darwin 语义(那边靠 /2 比 /1 长)在 linux 靠 rule 优先级给出;
//   - pref 120 > 100:bx 自身打标(SO_MARK,fwmark pref 100)的出站逃过屏障,
//     忠实移植 darwin 上 IP_BOUND_IF 走 scoped 表的结构性逃逸 —— 压到 100
//     之前,过渡窗口里 Core 解析 server 域名会死锁在自家屏障上;
//   - throw 私网段:/2 覆盖整个地址空间,darwin 靠主表连接路由按最长前缀
//     救私网,linux 的 rule 命中即终止查找,必须用 throw(停查本表、落回
//     后续 rule)亲手移植,否则屏障一装私网全断。
const (
	linuxBarrierTable    = "90"
	linuxBarrierRulePref = "120"
)

func linuxRuleCommand(verb string, ipv6 bool) Command {
	args := []string{"rule", verb, "pref", linuxBarrierRulePref, "table", linuxBarrierTable}
	if ipv6 {
		args = append([]string{"-6"}, args...)
	}
	return Command{Name: "ip", Args: args}
}

func linuxRouteAdd(spec []string, ipv6 bool) Command {
	args := append(append([]string{"route", "add"}, spec...), "table", linuxBarrierTable)
	if ipv6 {
		args = append([]string{"-6"}, args...)
	}
	return Command{Name: "ip", Args: args}
}

func linuxRouteDel(spec []string, ipv6 bool) Command {
	args := append(append([]string{"route", "del"}, spec...), "table", linuxBarrierTable)
	if ipv6 {
		args = append([]string{"-6"}, args...)
	}
	return Command{Name: "ip", Args: args}
}

// linuxBarrierRoutes 产出表 90 的全部路由(add/del 成对),顺序:
// bypass → v4 throw → v4 unreachable → v6 throw → v6 unreachable。
// 表内查找靠最长前缀,顺序本身不承重;承重的是 rule 排在全部路由之后武装
// (PlanBarrierLinux 保证)。
func linuxBarrierRoutes(gateway string, bypasses []string, blockIPv6 bool) []barrierRoute {
	var routes []barrierRoute
	for _, bypass := range bypasses {
		routes = append(routes, barrierRoute{
			add: linuxRouteAdd([]string{bypass, "via", gateway}, false),
			del: linuxRouteDel([]string{bypass}, false),
		})
	}
	for _, cidr := range route.DefaultPrivateCIDRs {
		routes = append(routes, barrierRoute{
			add: linuxRouteAdd([]string{"throw", cidr}, false),
			del: linuxRouteDel([]string{"throw", cidr}, false),
		})
	}
	for _, block := range publicIPv4Blocks {
		routes = append(routes, barrierRoute{
			add: linuxRouteAdd([]string{"unreachable", block}, false),
			del: linuxRouteDel([]string{"unreachable", block}, false),
		})
	}
	if blockIPv6 {
		for _, cidr := range route.DefaultPrivateV6CIDRs {
			routes = append(routes, barrierRoute{
				add: linuxRouteAdd([]string{"throw", cidr}, true),
				del: linuxRouteDel([]string{"throw", cidr}, true),
			})
		}
		for _, block := range publicIPv6Blocks {
			routes = append(routes, barrierRoute{
				add: linuxRouteAdd([]string{"unreachable", block}, true),
				del: linuxRouteDel([]string{"unreachable", block}, true),
			})
		}
	}
	return routes
}

func linuxBarrierRules(blockIPv6 bool) (arm, disarm []Command) {
	arm = []Command{linuxRuleCommand("add", false)}
	disarm = []Command{linuxRuleCommand("del", false)}
	if blockIPv6 {
		arm = append(arm, linuxRuleCommand("add", true))
		disarm = append([]Command{linuxRuleCommand("del", true)}, disarm...)
	}
	return arm, disarm
}

// PlanBarrierLinux 是 PlanBarrier 的 linux 同位物。
// rule 最后武装、最先解除:表没填好就武装,私网 throw 缺席的那一瞬是瞬态
// 整机私网黑洞;拆除方向同理。
func PlanBarrierLinux(ctx BarrierContext) (apply, reassert, cleanup []Command, err error) {
	gateway, bypasses, err := validateBarrierContext(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	routes := linuxBarrierRoutes(gateway, bypasses, ctx.BlockIPv6)
	arm, disarm := linuxBarrierRules(ctx.BlockIPv6)

	apply = make([]Command, 0, len(routes)+len(arm))
	for _, r := range routes {
		apply = append(apply, r.add)
	}
	apply = append(apply, arm...)

	reassert = make([]Command, 0, len(bypasses))
	for _, bypass := range bypasses {
		reassert = append(reassert, linuxRouteAdd([]string{bypass, "via", gateway}, false))
	}

	cleanup = make([]Command, 0, len(routes)+len(disarm))
	cleanup = append(cleanup, disarm...)
	for i := len(routes) - 1; i >= 0; i-- {
		cleanup = append(cleanup, routes[i].del)
	}
	return apply, reassert, cleanup, nil
}

// PlanBarrierReleaseLinux 是 PlanBarrierRelease 的 linux 同位物:解除武装 +
// 清掉自己表里的全部内容。
//
// **linux 不需要 darwin 的「转让」语义**:那边 transferred bypass 之所以留下,
// 是因为主表里那一行与 Core 要装的是同一行,删了会拽掉 Core 的旁路;这里的
// 专用表没有与任何人共享的行,rule 一解除整张表就是惰性的,全清才是对称。
// transferred 的形状校验保留 —— 坏输入要响亮,不许静默吞(与 darwin 一致)。
func PlanBarrierReleaseLinux(ctx BarrierContext, transferredBypasses []string) ([]Command, error) {
	gateway, bypasses, err := validateBarrierContext(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := canonicalBypassSet(transferredBypasses); err != nil {
		return nil, err
	}
	routes := linuxBarrierRoutes(gateway, bypasses, ctx.BlockIPv6)
	_, disarm := linuxBarrierRules(ctx.BlockIPv6)

	release := make([]Command, 0, len(disarm)+len(routes))
	release = append(release, disarm...)
	for i := len(routes) - 1; i >= 0; i-- {
		release = append(release, routes[i].del)
	}
	return release, nil
}

// PlanLinuxBarrierCleanup 是 PlanBlockingBarrierCleanup 的 linux 同位物:
// 逃生口用,无 ctx、可无条件跑。孤儿 pref-120 rule 与 darwin 的孤儿 /2 一样
// 能打死连通(Guardian 被 bootout 后内存里的 barrierOwnership 没了,内核里
// 的 rule 还在),清理原语必须与安装原语同批存在。
// flush 的是**自己的**专用表 —— 表号是本文件的常量,不碰任何别人的表。
func PlanLinuxBarrierCleanup() []Command {
	return []Command{
		linuxRuleCommand("del", true),
		linuxRuleCommand("del", false),
		{Name: "ip", Args: []string{"route", "flush", "table", linuxBarrierTable}},
		{Name: "ip", Args: []string{"-6", "route", "flush", "table", linuxBarrierTable}},
	}
}
