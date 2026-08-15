package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/setup"
	"github.com/getbx/bx/internal/stats"
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
	fmt.Print(renderServerList(gatherServerList(c.String("config"), c.Bool("test"))))
	return nil
}

// serverListView 是渲染要的全部输入。
//
// Degraded 说明这一份是**退化的**(Guardian 不可达,只读到了配置)。它必须
// 出现在输出里:一个安静地少显示几列的界面,会让用户以为那几台真的没有数据,
// 而实际上是没问到 —— 与本仓库里 Tristate 同一条纪律。
type serverListView struct {
	Entries  []guardian.ServerEntry
	Current  string
	Degraded bool
	// Tested 表示这一次真的做过探测(--test)。没测过和测过都没通,是两回事。
	Tested bool
}

// gatherServerList 优先问 Guardian,问不到就退回只读配置。
//
// **优先问 Guardian 不是为了省事,是为了只有一份判据**:吞吐历史是 0600 root 的、
// 探测要 Core 的直连拨号器,这两样 CLI 自己都拿不到;而更要紧的是,两处各读一份
// 就会各渲染一份,菜单与 CLI 对同一台服务器说不同的话正是这个仓库反复栽的形状。
func gatherServerList(configPath string, test bool) serverListView {
	timeout := guardianServerListTimeout
	if test {
		// 服务端每台最多 8 秒且串行,客户端必须比那条链长 —— 否则拿到的永远是
		// 自己的超时,而服务端那边还在测。
		timeout = guardianServerProbeTimeout
	}
	client := guardian.NewClientWithTimeout(guardian.SocketPath, timeout)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var (
		resp guardian.ServerListResponse
		err  error
	)
	if test {
		resp, err = client.ProbeServers(ctx)
	} else {
		resp, err = client.ListServers(ctx)
	}
	if err == nil {
		return serverListView{Entries: resp.Servers, Current: resp.Current, Tested: test}
	}
	// 退回只读配置:名字与出口主机仍然答得出,延迟与吞吐答不出 —— 而输出会说明这一点。
	list, current, cfgErr := setup.ListServers(configPath)
	if cfgErr != nil {
		return serverListView{Degraded: true}
	}
	entries := make([]guardian.ServerEntry, 0, len(list))
	for _, srv := range list {
		host, _ := setup.LinkHost(srv.Link)
		udp := ""
		if strings.TrimSpace(srv.UDP) != "" {
			udp, _ = setup.LinkHost(srv.UDP)
		}
		entries = append(entries, guardian.ServerEntry{
			Name: srv.Name, Host: host, UDPHost: udp,
			Current: strings.EqualFold(strings.TrimSpace(srv.Name), strings.TrimSpace(current)),
		})
	}
	return serverListView{Entries: entries, Current: current, Degraded: true}
}

const (
	guardianServerListTimeout  = 5 * time.Second
	guardianServerProbeTimeout = 60 * time.Second
)

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
//
// **这一份与菜单显示的是同一批字段**(延迟 / 吞吐 / 年龄),数据也来自同一个
// 端点。两个界面对同一台服务器说不同的话,是这个仓库反复栽的形状。
func renderServerList(view serverListView) string {
	if len(view.Entries) == 0 {
		if view.Degraded {
			return "读不到服务器清单(Guardian 不可达,配置也读不出来)。\n"
		}
		return "配置里没有服务器清单(还是单服务器配置)。加第二台:bx setup --name <名字> '<链接>'\n"
	}
	var b strings.Builder
	b.WriteString("已配置的服务器(● = 当前在用):\n")
	for _, s := range view.Entries {
		mark := " "
		if s.Current {
			mark = "●"
		}
		host := s.Host
		if host == "" {
			host = "(链接解析不出主机)"
		}
		if s.UDPHost != "" && s.UDPHost != s.Host {
			host += "  UDP→" + s.UDPHost
		}
		fmt.Fprintf(&b, " %s %-20s %-28s%s\n", mark, s.Name, host, serverMetrics(s))
	}
	if view.Degraded {
		// **少显示了什么必须说出来。** 安静地少几列,用户会以为那几台真的
		// 没有数据,而实际上是没问到。
		b.WriteString("\n(Guardian 不可达,延迟与吞吐这次没问到 —— 只显示了配置里的内容)\n")
	} else if !view.Tested {
		b.WriteString("\n延迟没测(它要往隧道外面发包,只在你要的时候发):bx server list --test\n")
	}
	b.WriteString("\n切换:bx server use <name>\n")
	return b.String()
}

// serverMetrics 是延迟与吞吐那一段。**没有的东西一个字都不说** ——
// 每台后面挂一行「未测试 / 无数据」是墙纸,而墙纸会训练人忽略这一栏。
func serverMetrics(s guardian.ServerEntry) string {
	var parts []string
	if s.Probe != nil {
		if s.Probe.Reachable {
			parts = append(parts, fmt.Sprintf("%d ms", s.Probe.RTTMS))
		} else if s.Probe.Error != "" {
			// 失败必须说原因:一个光秃秃的叉让用户分不清是服务器关了还是
			// 自己这条网络的问题。
			parts = append(parts, s.Probe.Error)
		} else {
			parts = append(parts, "不可达")
		}
	}
	if s.PeakBPS > 0 {
		peak := "峰值 " + stats.HumanBPS(s.PeakBPS)
		if age := humanAge(time.Duration(s.PeakAgeSeconds) * time.Second); age != "" {
			// **历史数字必须带年龄。** 不带年龄的历史读起来像现状。
			peak += " · " + age
		}
		parts = append(parts, peak)
	}
	return strings.Join(parts, "   ")
}

// humanAge 把年龄写成人读的样子;**「就是现在」返回空串**,那时不该写
// 「0 分钟前」—— 它只会让人怀疑这个数字是不是坏的。门槛与菜单侧同为两分钟。
func humanAge(age time.Duration) string {
	switch {
	case age < 2*time.Minute:
		return ""
	case age < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(age/time.Minute))
	case age < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(age/time.Hour))
	default:
		return fmt.Sprintf("%d 天前", int(age/(24*time.Hour)))
	}
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
