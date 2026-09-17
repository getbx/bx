package cli

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/getbx/bx/internal/elevate"

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
			Name: "ls", Usage: "list the named egresses and the prefixes handed to them",
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
			Name: "add", Usage: "add a named egress (an existing name has its address updated)", ArgsUsage: "<name> <socks5>",
			Flags: ruleBaseFlags(), Action: egressAddAction,
		},
		{
			Name: "route", Usage: "hand a prefix to an egress (allowlist)", ArgsUsage: "<name> <cidr>",
			Flags: ruleBaseFlags(), Action: egressRouteAction,
		},
		{
			Name: "rm", Usage: "delete an egress along with every prefix handed to it", ArgsUsage: "<name>",
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
		return fmt.Errorf("usage: bx egress add <name> <socks5>   (for example: bx egress add office 127.0.0.1:1080)")
	}
	if err := setup.AddEgress(c.String("config"), name, addr); err != nil {
		return err
	}
	fmt.Printf("✓ egress %s → %s\n", name, addr)
	fmt.Printf("  Next: bx egress route %s <prefix>\n", name)
	return nil
}

func egressRouteAction(c *cli.Context) error {
	name := strings.TrimSpace(c.Args().Get(0))
	cidr := strings.TrimSpace(c.Args().Get(1))
	if name == "" || cidr == "" {
		return fmt.Errorf("usage: bx egress route <name> <cidr>   (for example: bx egress route office 10.84.0.0/16)")
	}
	if err := setup.RouteViaEgress(c.String("config"), name, cidr); err != nil {
		return err
	}
	fmt.Printf("✓ %s is now handed to the egress %s\n", cidr, name)
	fmt.Print(egressRestartHint)
	return nil
}

func egressRemoveAction(c *cli.Context) error {
	name := strings.TrimSpace(c.Args().First())
	if name == "" {
		return fmt.Errorf("usage: bx egress rm <name>")
	}
	if err := setup.RemoveEgress(c.String("config"), name); err != nil {
		return err
	}
	fmt.Printf("✓ deleted the egress %s, along with the prefixes handed to it\n", name)
	fmt.Print(egressRestartHint)
	return nil
}

// egressRestartHint —— **改完必须说要重连。** bx 不热重载配置,而一条「已保存」
// 之后什么都没变的体验,会让用户以为功能坏了(`bx direct add` 同款)。
const egressRestartHint = "  The change takes effect after a reconnect: " + elevate.Prefix + "bx down && " + elevate.Prefix + "bx up\n"

// renderEgressList 渲染清单。
//
// **没有出口时说人话,并且说清它不是首选** —— 一个空列表加一句用法,会让人
// 以为这就是访问内网的正路;而绝大多数情况下 Tailscale 才是。
func renderEgressList(list []setup.EgressEntry, alive map[string]bool) string {
	if len(list) == 0 {
		return "No named egresses are configured.\n\n" +
			"They are the fallback for when a mesh like Tailscale will not work — if a mesh works, use the mesh\n" +
			"(turn on a subnet router on that internal machine or on the VPS — bx does not get in its way).\n\n" +
			"If you really need one:\n" +
			"  bx egress add office 127.0.0.1:1080\n" +
			"  bx egress route office 10.84.0.0/16\n"
	}
	var b strings.Builder
	b.WriteString("Named egresses:\n")
	for _, e := range list {
		// **状态只有两态,而且「不在听」要说人话。**
		// 这是最常见的故障(那条 ssh -D 断了),而它对所有别的信号都是隐形的:
		// 隧道健康、status 显示 Protected,只有走那几个网段的连接在失败。
		state := "listening"
		if !alive[e.Name] {
			state = "unreachable — did the SOCKS5 you had open go away?"
		}
		fmt.Fprintf(&b, "  %-16s %-22s %s\n", e.Name, e.Socks5, state)
		if len(e.CIDR) == 0 {
			// **没有网段 = 这个出口什么都不做。** 说出来,否则用户会以为配好了。
			b.WriteString("    (nothing has been handed to it yet — it does nothing right now)\n")
			continue
		}
		for _, c := range e.CIDR {
			fmt.Fprintf(&b, "    %s\n", c)
		}
	}
	b.WriteString("\nThis is an allowlist: only the prefixes listed above go through an egress; every other private address goes direct as before.\n")
	return b.String()
}
