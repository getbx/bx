package cli

import (
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/setup"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/urfave/cli/v2"
)

// 本文件是**客户端**那一半:用户自己在已配好的几台之间选。
//
// **它刻意不是自动容灾。** `transports:` 那张优先级表会在健康检查失败时自己切,
// 而自动切会**悄悄改变你的出口 IP** —— 项目所有者明确要求它退场。这里是
// `servers:` + `current:`,谁在用由人决定。
//
// (计划 2026-08-09-multi-server 的 Task 9 还要把管理员那半改名成 `bx vps`,
// 好让 `bx server` 整个归客户端。改名会动 README 与既有习惯,**本次不做**;
// 在那之前 `bx server` 下混着两类命令,这是已知的取舍。)

func serverListAction(c *cli.Context) error {
	list, current, err := setup.ListServers(c.String("config"))
	if err != nil {
		return err
	}
	fmt.Print(renderServerList(list, current))
	return nil
}

func serverUseAction(c *cli.Context) error {
	name := strings.TrimSpace(c.Args().First())
	if name == "" {
		return fmt.Errorf("用法:bx server use <name>(先 `bx server list` 看有哪些)")
	}
	path := c.String("config")
	// **先写配置,再热切。** 顺序反过来的话,热切成功而写盘失败就会留下
	// 「现在在 B、下次启动回 A」——一个没人看得出来的不一致。
	if err := setup.SetCurrentServer(path, name); err != nil {
		return err
	}
	list, current, err := setup.ListServers(path)
	if err != nil {
		return err
	}
	var target config.Server
	for _, s := range list {
		if strings.EqualFold(s.Name, current) {
			target = s
		}
	}
	host, _ := setup.LinkHost(target.Link)
	// **解壳再交给 Core。** 它只认内层链接;喂换壳的进去它解析不出主机、
	// 装不了 bypass,于是正确地拒绝切换(以免成环),而用户看到的是一句
	// 关于 base64 的错误。
	hotErr := supervisor.SwitchServer(supervisor.LiveSwitchDeps(), target.Name,
		setup.DecodeLink(target.Link), setup.DecodeLink(target.UDP))
	fmt.Print(switchOutcomeMessage(target.Name, host, hotErr))
	return nil
}

func serverRemoveAction(c *cli.Context) error {
	name := strings.TrimSpace(c.Args().First())
	if name == "" {
		return fmt.Errorf("用法:bx server rm <name>")
	}
	if err := setup.RemoveServer(c.String("config"), name); err != nil {
		return err
	}
	fmt.Printf("✓ 已从清单里删掉 %s\n", name)
	return nil
}

// renderServerList 渲染清单。
//
// **出口主机比名字重要**:名字是用户随便起的,而他真正关心的是流量从哪出去。
// UDP 单独标出来 —— 少了那一条,UDP 会静默走主传输,而两者出口可能不同。
func renderServerList(list []config.Server, current string) string {
	if len(list) == 0 {
		return "配置里没有服务器清单(还是单服务器配置)。加第二台:bx setup --name <名字> '<链接>'\n"
	}
	var b strings.Builder
	b.WriteString("已配置的服务器(● = 当前在用):\n")
	for _, s := range list {
		mark := " "
		if strings.EqualFold(strings.TrimSpace(s.Name), strings.TrimSpace(current)) {
			mark = "●"
		}
		// **必须用会解 bx:// 壳的那份判据。** 配置里存的是换壳链接,
		// 直接喂 tunnel.ServerHost 会得到整串 base64(真机实测)。
		host, ok := setup.LinkHost(s.Link)
		if !ok {
			host = "(链接解析不出主机)"
		}
		udp := ""
		if strings.TrimSpace(s.UDP) != "" {
			if uh, ok := setup.LinkHost(s.UDP); ok {
				udp = "  UDP→" + uh
			} else {
				udp = "  UDP→(解析不出)"
			}
		}
		fmt.Fprintf(&b, " %s %-20s %s%s\n", mark, s.Name, host, udp)
	}
	b.WriteString("\n切换:bx server use <name>\n")
	return b.String()
}

// switchOutcomeMessage 说清楚这次切换到底生效了没有。
//
// **不许把失败说成成功。** 热切失败时配置已经写好了,但正在跑的那个实例还在
// 旧服务器上 —— 不明说要重启,用户就会以为已经切过去了,而那正是 2026-08-06
// 「以为换了服务器其实没换」那次事故的形状。
func switchOutcomeMessage(name, host string, hotErr error) string {
	if hotErr == nil {
		// 走到这里说明**已经确认过**(commit),死手不会再把它还原 ——
		// 第一版只武装就说「立即生效」,而 Core 随后自动回滚了。
		return fmt.Sprintf("✓ 已切到 %s(%s),已确认生效 —— 你的流量现在从这里出去。\n", name, host)
	}
	return fmt.Sprintf(""+
		"✓ 已写入配置:current = %s(%s)\n"+
		"⚠ 但热切没有成功:%v\n"+
		"  配置里的选择已经落定,执行下面两条即可用上:\n"+
		"    sudo bx down\n"+
		"    sudo bx up\n", name, host, hotErr)
}
