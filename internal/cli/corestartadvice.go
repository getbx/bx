package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/setup"
	"github.com/getbx/bx/internal/supervisor"
)

// 「Core 起不来时说出为什么」的**客户端那一半**。
//
// Guardian 的应答体只带一个码,这是刻意的:「那台服务器是谁」与「你还有哪几台」
// 两个客户端本来就合法持有 —— `bx up` 以 root 跑,读得到 /etc/bx/config.yaml;
// 菜单经 /v1/servers 拿到的条目本来就带 host/port。于是这次改动**不新增任何一个
// 字节的发布面**(spec §5),而「发布面扩大靠 review」在本仓库是已知的弱环。
//
// **措辞的每一条规矩都来自一次真实事故**,改之前先读:
//
//   - 只说 bx 观测到什么,**绝不断言那台服务器的状态**。本机自己没网时同样
//     拨不通,而一句「那台服务器没有应答」会让用户去重启一台好好的 VPS。
//   - 「连不上」与「连得上但没握上」的措辞必须**相反**。说反了就是把人派去修
//     一台好机器:reality 一度全挂,真因是默认 SNI www.microsoft.com 的证书
//     过大,而当时先误归因成 sing-box 同机问题、又误归因成网络 MITM。
//   - 「没判出来」有三个码,处置各不相同,但**没有一个可以被读成「服务器没事」**。
//   - 只在真有另一台时才说「你还配了另一台」,而且**绝不打印链接**(链接是凭据,
//     与 TestServerListNeverShipsTheLinkItself 同一条)。

// startFailureServers 是拼那句话要用的两样事实。
//
// **只有名字与 host:port,没有链接** —— 它会被拼进给用户看的文本里。
type startFailureServers struct {
	// CurrentName / CurrentHostPort 空 = **没问出来**(配置读不到、链接解不开)。
	// 那时那句话照说,只是不点名 —— 绝不编一个占位地址,一句指着 `<unknown>:0`
	// 的排查命令比不给更糟。
	CurrentName     string
	CurrentHostPort string
	// Others 是别的服务器,形如 `tokyo(166.1.190.123)`。空 = 真的只有一台。
	Others []string
}

// coreStartFailureCodePrefix 是 Guardian 给这一族码加的前缀(见 guardian 的
// coreStartFailureLastError):Status.LastError 那个命名空间里的其余码说的都是
// 「Guardian 这一侧发生了什么」,不带前缀会让 tunnel_unreachable 看起来像是
// Guardian 自己拨不通。
const coreStartFailureCodePrefix = "core_"

// coreStartFailureCodes 是这一族在 Guardian 应答里出现的全部码。
// **从 supervisor 那张表派生**,不是这里抄一份 —— 加一个码而忘了给它一句话,
// TestEveryStartFailureOutcomeReadsDifferently 当场转红。
func coreStartFailureCodes() []string {
	codes := supervisor.StartFailureCodes()
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		out = append(out, coreStartFailureCodePrefix+code)
	}
	return out
}

// coreStartFailureAdvice 把一个码拼成用户能照做的那几行。
//
// **认不出的码返回空串** —— 宁可不给,也不编一句错的(与菜单
// toggleFailureHint 同一条)。
func coreStartFailureAdvice(code string, facts startFailureServers) string {
	bare, ok := strings.CutPrefix(code, coreStartFailureCodePrefix)
	if !ok || !supervisor.IsStartFailureCode(bare) {
		return ""
	}
	where := facts.CurrentHostPort
	named := where != ""

	var headline string
	var steps []string
	tunnelOutcome := true
	switch {
	case bare == supervisor.StartFailureTunnelUnreachable:
		// 「bx 连不上 X」是关于**这次尝试**的事实;「X 没有应答」是关于那台
		// 服务器的断言,而本机自己没网时同样连不上。
		headline = "bx 起不来:" + phrase(named, "bx 连不上服务器 "+where, "bx 连不上你的服务器") +
			" —— 那个地址上没有建立起 TCP 连接。"
		steps = append(steps,
			phrase(named,
				"可能是那台机器停了/换了 IP,也可能是这台机器自己的网络不通。自己确认一下:nc -z "+hostOf(where)+" "+portOf(where),
				"可能是那台机器停了/换了 IP,也可能是这台机器自己的网络不通"))
	case bare == supervisor.StartFailureTunnelHandshakeFailed:
		// 措辞与上面**相反**:那台机器活着,去修它是白费力气。
		headline = "bx 起不来:" + phrase(named, "服务器 "+where+" 的 TCP 端口在应答", "你的服务器在应答") +
			",而隧道没能在启动窗口内建起来。"
		steps = append(steps,
			"那台机器活着 —— 要查的是这条链接、凭据、SNI、或者路上的干扰,不是那台机器停没停",
			"完整原因:sudo tail -50 "+coreLogPathForAdvice())
	case bare == supervisor.StartFailureTunnelUndeterminedUDPTransport:
		headline = "bx 起不来:隧道没建起来,而 bx 没能判断那台服务器还在不在 —— " +
			"它跑的是 UDP 传输(hysteria2/QUIC),一次 TCP 拨号观测不到它。"
		steps = append(steps,
			phrase(named, "想确认那台机器活着,用 ping / ssh 试它:"+hostOf(where), "想确认那台机器活着,用 ping / ssh 试它"),
			"完整原因:sudo tail -50 "+coreLogPathForAdvice())
	case bare == supervisor.StartFailureTunnelUndeterminedLocalDial:
		// **它指着 bx 自己的直连器,不指着 VPS。** 2026-08-13 那次事故的签名:
		// DirectDialer 用 IP_BOUND_IF 绑物理网卡,而 IP_BOUND_IF 只查 scoped
		// 路由表 —— 那条 scoped 默认路由由 Hijack 装,比这次判别拨号晚 572 行。
		// 这时去探 VPS 什么也说明不了:SYN 根本没离开这台机器。
		headline = "bx 起不来:隧道没建起来,而 bx 没能判断那台服务器还在不在 —— " +
			"那次判别拨号在本机就失败了,SYN 一个都没发出去。"
		steps = append(steps,
			"先查 bx 自己的直连出口(2026-08-13 那次故障的签名):route -n get -ifscope <你的网卡> 1.1.1.1;"+
				"答 `not in table` 就是它,与那台服务器无关",
			"完整原因:sudo tail -50 "+coreLogPathForAdvice())
	case supervisor.IsTunnelUndeterminedCode(bare):
		headline = "bx 起不来:隧道没建起来,而 bx 没能判断那台服务器还在不在(那次判别本身没做成)。"
		steps = append(steps,
			phrase(named, "想自己确认那台服务器:nc -z "+hostOf(where)+" "+portOf(where), "先看看配置里那条服务器链接对不对"),
			"完整原因:sudo tail -50 "+coreLogPathForAdvice())
	default:
		// 隧道之外的启动失败(配置 / 释放二进制 / 开 TUN / 劫持路由 / 认不出)。
		// 换一台服务器对它们一点用都没有,所以**不给那句话**。
		tunnelOutcome = false
		headline = "bx 起不来:" + nonTunnelStartFailureHeadline(bare)
		steps = append(steps, "完整原因:sudo tail -50 "+coreLogPathForAdvice())
	}

	if tunnelOutcome && len(facts.Others) > 0 {
		steps = append(steps, fmt.Sprintf("你还配了另一台:%s —— sudo bx server use <名字>",
			strings.Join(facts.Others, "、")))
	}
	lines := []string{headline}
	for _, step := range steps {
		lines = append(lines, "  · "+step)
	}
	return strings.Join(lines, "\n")
}

// nonTunnelStartFailureHeadline:隧道之外那几种的一句话。
//
// 它们各自只有一句是刻意的:换服务器帮不上忙,而真正的细节在 Core 日志里。
// **认不出的那一支说「认不出」,不编一个病因**。
func nonTunnelStartFailureHeadline(bare string) string {
	switch bare {
	case supervisor.StartFailureConfig:
		return "配置里的内容 bx 用不了(规则 / 网段 / hosts / 服务器链接里有一条是坏的)。改完要 sudo bx down && sudo bx up。"
	case supervisor.StartFailureProvision:
		return "没能把内嵌的传输二进制释放到 data_dir(多半是磁盘满了或那个目录不可写)。"
	case supervisor.StartFailureTUNOpen:
		return "打不开 TUN 设备(权限、设备被占用)。"
	case supervisor.StartFailureHijack:
		return "TUN 起来了,而劫持默认路由失败了。"
	}
	return "Core 报了一个 bx 这一版还没有专门说法的启动失败。"
}

// phrase:问得出地址就点名,问不出就说一句仍然成立的话。
// **绝不编一个占位地址** —— 一句指着 `<unknown>:0` 的排查命令比不给更糟。
func phrase(named bool, withName, without string) string {
	if named {
		return withName
	}
	return without
}

func hostOf(hostPort string) string {
	if i := strings.LastIndex(hostPort, ":"); i >= 0 {
		return strings.Trim(hostPort[:i], "[]")
	}
	return hostPort
}

func portOf(hostPort string) string {
	if i := strings.LastIndex(hostPort, ":"); i >= 0 {
		return hostPort[i+1:]
	}
	return ""
}

// coreLogPathForAdvice:细节照旧只进 Core 日志(它是给人读的、root-only),
// 而应答体只带码 —— 这条指引是两者之间唯一的桥。
func coreLogPathForAdvice() string { return "/var/log/bx.log" }

// readStartFailureServers 从配置里取那两样事实。
//
// **读不到就交空事实,不报错、不猜**:这是一条诊断路径,它不许把一次故障
// 换成另一次故障。非 root、配置不存在、配置坏了,全落这里。
func readStartFailureServers(configPath string) startFailureServers {
	var facts startFailureServers
	// 当前那台走 config.Parse:它已经把 servers/current 与 server/transports
	// 三种写法归一成 cfg.Server,**判据只有一份**(自己再推一遍 current 该指谁,
	// 就是第二份判据,而它会与真正在用的那条漂开)。
	if raw, err := os.ReadFile(configPath); err == nil {
		if cfg, err := config.Parse(raw); err == nil {
			if host, ok := setup.LinkHost(cfg.Server); ok {
				facts.CurrentHostPort = joinHostPortForAdvice(host, setup.LinkPort(cfg.Server))
			}
		}
	}
	list, current, err := setup.ListServers(configPath)
	if err != nil {
		return facts
	}
	for _, server := range list {
		if setup.SameServerName(server.Name, current) {
			facts.CurrentName = server.Name
			continue
		}
		// **只发名字与主机,链接一个字节都不出门。**
		if host, ok := setup.LinkHost(server.Link); ok {
			facts.Others = append(facts.Others, fmt.Sprintf("%s(%s)", server.Name, host))
			continue
		}
		facts.Others = append(facts.Others, server.Name)
	}
	return facts
}

func joinHostPortForAdvice(host string, port int) string {
	if host == "" {
		return ""
	}
	if port <= 0 {
		return host
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// annotateCoreStartFailure 把那段可行动的话拼到 Guardian 的失败前面。
//
// **码取自应答体那个字段,不是从消息文本里抠出来的** —— 按文本认码是本仓库
// 反复禁止的形状:措辞一改,消费方悄悄退回一句通用的废话而两边都不报错。
//
// 原始错误**留在链上**(%w):码、Guardian 日志那条指引、以及别的按
// *guardian.HTTPError 做的判定都还挂在它上面。那段话排在前面是因为用户
// 要看的就是它。
func annotateCoreStartFailure(err error, configPath string) error {
	if err == nil {
		return nil
	}
	advice := coreStartFailureAdvice(guardian.FailureCode(err), readStartFailureServers(configPath))
	if advice == "" {
		return err
	}
	return fmt.Errorf("%s\n%w", advice, err)
}
