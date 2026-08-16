package cli

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/getbx/bx/internal/setup"
	"github.com/urfave/cli/v2"
)

// `bx egress` —— 把点名的网段交给一条**已经存在的**隧道。
//
// **先说什么时候不该用它**:能用 Tailscale 就用 Tailscale(在内网那台或 VPS 上
// 开 subnet router),而 bx 不挡它 —— 私网地址不绑物理网卡、走系统路由表,
// subnet router 通告的 /16 也比 bx 那条 /8 更具体。那条路一次配置、所有设备
// 受益,bx 一行都不用改。这些命令是 mesh 用不了时的退路。
//
// **它们只改配置,不重启任何东西** —— 生效要 `bx down && bx up`,而那是一次
// 断网,必须是用户单独的、显式的一下(与 `bx direct add` 同一条纪律)。

func egressCommands() []*cli.Command {
	return []*cli.Command{
		{
			Name: "ls", Usage: "列出具名出口与交给它们的网段",
			Flags: ruleBaseFlags(), Action: egressListAction,
		},
		{
			// **地址是位置参数,不是 flag。**
			//
			// urfave/cli 遇到第一个位置参数就停止解析 flag,于是
			// `bx egress add office --socks5 …` 里那个 flag 根本不会被看见 ——
			// 而它是 Required,命令直接失败,报的还是「没给 --socks5」。
			// 这个坑本仓库 2026-08-14 在 `bx setup … --udp` 上栽过一次,
			// 实跑这条命令时又栽了一次。全用位置参数就没有这个问题。
			Name: "add", Usage: "加一个具名出口(同名则更新地址)", ArgsUsage: "<name> <socks5>",
			Flags: ruleBaseFlags(), Action: egressAddAction,
		},
		{
			Name: "route", Usage: "把一个网段交给某个出口(白名单)", ArgsUsage: "<name> <cidr>",
			Flags: ruleBaseFlags(), Action: egressRouteAction,
		},
		{
			Name: "rm", Usage: "删掉一个出口,连同交给它的所有网段", ArgsUsage: "<name>",
			Flags: ruleBaseFlags(), Action: egressRemoveAction,
		},
	}
}

func egressListAction(c *cli.Context) error {
	list, err := setup.ListEgresses(c.String("config"))
	if err != nil {
		return err
	}
	fmt.Print(renderEgressList(list, probeEgresses(list, dialLoopback)))
	return nil
}

// probeEgresses 逐个问「这个出口现在在听吗」。
//
// **这里主动探测是合理的,而别处不是。** 项目的规矩是「被动观测优于主动探测」,
// 那条规矩针对的是**定时**、**对外**的探测(泄漏面 + 假阳性)。这里是:用户
// 敲了 `bx egress ls` 明确要一个「现在」的答案,而且目标是 **loopback** ——
// 零泄漏、零成本、微秒级。
//
// 而它回答的正是最常见的那个故障:那条 `ssh -D` 断了。
func probeEgresses(list []setup.EgressEntry, dial func(string) error) map[string]bool {
	alive := make(map[string]bool, len(list))
	for _, e := range list {
		alive[e.Name] = dial(e.Socks5) == nil
	}
	return alive
}

func dialLoopback(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, egressProbeTimeout)
	if err != nil {
		return err
	}
	return conn.Close()
}

// egressProbeTimeout —— loopback 上连不上是立刻返回的(RST),给 300ms 只是
// 防一个卡住的监听器把命令挂住。
const egressProbeTimeout = 300 * time.Millisecond

func egressAddAction(c *cli.Context) error {
	name := strings.TrimSpace(c.Args().Get(0))
	addr := strings.TrimSpace(c.Args().Get(1))
	if name == "" || addr == "" {
		return fmt.Errorf("用法:bx egress add <name> <socks5>(如 bx egress add office 127.0.0.1:1080)")
	}
	if err := setup.AddEgress(c.String("config"), name, addr); err != nil {
		return err
	}
	fmt.Printf("✓ 出口 %s → %s\n", name, addr)
	fmt.Printf("  下一步:bx egress route %s <网段>\n", name)
	return nil
}

func egressRouteAction(c *cli.Context) error {
	name := strings.TrimSpace(c.Args().Get(0))
	cidr := strings.TrimSpace(c.Args().Get(1))
	if name == "" || cidr == "" {
		return fmt.Errorf("用法:bx egress route <name> <cidr>(如 bx egress route office 10.84.0.0/16)")
	}
	if err := setup.RouteViaEgress(c.String("config"), name, cidr); err != nil {
		return err
	}
	fmt.Printf("✓ %s 现在交给出口 %s\n", cidr, name)
	fmt.Print(egressRestartHint)
	return nil
}

func egressRemoveAction(c *cli.Context) error {
	name := strings.TrimSpace(c.Args().First())
	if name == "" {
		return fmt.Errorf("用法:bx egress rm <name>")
	}
	if err := setup.RemoveEgress(c.String("config"), name); err != nil {
		return err
	}
	fmt.Printf("✓ 已删掉出口 %s(连同交给它的网段)\n", name)
	fmt.Print(egressRestartHint)
	return nil
}

// egressRestartHint —— **改完必须说要重连。** bx 不热重载配置,而一条「已保存」
// 之后什么都没变的体验,会让用户以为功能坏了(`bx direct add` 同款)。
const egressRestartHint = "  改动在重连后生效:sudo bx down && sudo bx up\n"

// renderEgressList 渲染清单。
//
// **没有出口时说人话,并且说清它不是首选** —— 一个空列表加一句用法,会让人
// 以为这就是访问内网的正路;而绝大多数情况下 Tailscale 才是。
func renderEgressList(list []setup.EgressEntry, alive map[string]bool) string {
	if len(list) == 0 {
		return "没有配置具名出口。\n\n" +
			"它是「Tailscale 之类的 mesh 用不了」时的退路 —— 能用 mesh 就先用 mesh\n" +
			"(在内网那台或 VPS 上开 subnet router,bx 不挡它)。\n\n" +
			"确实要用:\n" +
			"  bx egress add office --socks5 127.0.0.1:1080\n" +
			"  bx egress route office 10.84.0.0/16\n"
	}
	var b strings.Builder
	b.WriteString("具名出口:\n")
	for _, e := range list {
		// **状态只有两态,而且「不在听」要说人话。**
		// 这是最常见的故障(那条 ssh -D 断了),而它对所有别的信号都是隐形的:
		// 隧道健康、status 显示 Protected,只有走那几个网段的连接在失败。
		state := "在听"
		if !alive[e.Name] {
			state = "连不上 —— 开着的 SOCKS5 断了?"
		}
		fmt.Fprintf(&b, "  %-16s %-22s %s\n", e.Name, e.Socks5, state)
		if len(e.CIDR) == 0 {
			// **没有网段 = 这个出口什么都不做。** 说出来,否则用户会以为配好了。
			b.WriteString("    (还没有交给它任何网段 —— 它现在什么都不做)\n")
			continue
		}
		for _, c := range e.CIDR {
			fmt.Fprintf(&b, "    %s\n", c)
		}
	}
	b.WriteString("\n白名单:只有上面列出的网段走出口,其余私网照旧直连。\n")
	return b.String()
}
