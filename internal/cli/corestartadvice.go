package cli

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/getbx/bx/internal/elevate"

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
	// CurrentHostPort 空 = **没问出来**(配置读不到、链接解不开)。那时那句话
	// 照说,只是不点名 —— 绝不编一个占位地址,一句指着 `<unknown>:0` 的排查
	// 命令比不给更糟。
	//
	// **这里刻意没有服务器的名字。** 它曾经在,而且被采集、被断言,却没有任何
	// 一句渲染读它 —— 一个有测试盖着、没人读的字段与没有这个字段在输出上完全
	// 一样,只是看起来还活着。要点名就得先有一句话真的说出它。
	CurrentHostPort string
	// Others 是别的服务器。空 = 真的只有一台。**存名字与主机两样,不存拼好的串**:
	// 那句「你还配了另一台」要写出它的名字(命令里要用)与地址(人要认),而名字常常
	// 就是地址 —— 拼好的串无从知道该不该省掉括号里那一半。
	Others []otherServerForAdvice
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
		headline = "bx could not start: " + phrase(named, "bx cannot reach server "+where, "bx cannot reach your server") +
			" — no TCP connection came up to that address. "
		steps = append(steps,
			"That machine may be down or may have changed IP, or this machine's own network may be broken"+selfCheckSuffix(where),
			// **这一族里最要紧的一条偏偏此前没给这句话** —— 事故那一次就是它,
			// 而 `dial tcp <server>:443: i/o timeout` 那句原文只在 Core 日志里。
			"Full reason: "+coreLogCommandForAdvice())
	case bare == supervisor.StartFailureTunnelHandshakeFailed:
		// 措辞与上面**相反**:那台机器活着,去修它是白费力气。
		headline = "bx could not start: " + phrase(named, "server "+where+" answers on its TCP port", "your server answers") +
			", but the tunnel did not come up inside the start window. "
		steps = append(steps,
			"That machine is alive — what to check is this link, the credentials, the SNI, or interference on the path, not whether that machine has stopped",
			"Full reason: "+coreLogCommandForAdvice())
	case bare == supervisor.StartFailureTunnelUndeterminedUDPTransport:
		headline = "bx could not start: the tunnel did not come up, and bx could not tell whether that server is still there — " +
			"it runs a UDP transport (hysteria2/QUIC), which a single TCP dial cannot observe. "
		steps = append(steps,
			phrase(named, "To confirm that machine is alive, try ping or ssh: "+hostOf(where), "To confirm that machine is alive, try ping or ssh"),
			"Full reason: "+coreLogCommandForAdvice())
	case bare == supervisor.StartFailureTunnelUndeterminedLocalDial:
		// **它指着 bx 自己的直连器,不指着 VPS。** 2026-08-13 那次事故的签名:
		// DirectDialer 用 IP_BOUND_IF 绑物理网卡,而 IP_BOUND_IF 只查 scoped
		// 路由表 —— 那条 scoped 默认路由由 Hijack 装,比这次判别拨号晚 572 行。
		// 这时去探 VPS 什么也说明不了:SYN 根本没离开这台机器。
		// **这一档也要点名 host:port。** 从前它是这一族里唯一一句连地址都没有
		// 的话,而 2026-08-13 那种机器上(scoped 表里没有默认路由)最容易落进
		// 来的恰恰是「VPS 真的挂了」那一次 —— 用户拿到一句既不说哪台机器、
		// 又先派他去查 bx 自己路由的话。bx 明明知道那个地址。
		headline = "bx could not start: the tunnel did not come up, and bx " +
			phrase(named, "could not tell whether server "+where+" is still there", "could not tell whether that server is still there") +
			" — that diagnostic dial failed on this machine; not one SYN left it. "
		steps = append(steps,
			"First check bx's own direct egress (the signature of the 2026-08-13 failure): route -n get -ifscope <your NIC> 1.1.1.1; "+
				"if the reply is not in table, what is broken is bx's direct dialer on this machine, and switching servers will not help",
			// **这条不许省。** 同一个码还盖着「解析不出那台服务器的主机名」——
			// 那一种是服务器特有的,换一台确实有用。少了它,下面那句「你还配了
			// 另一台」就与上面那句读起来自相矛盾,而两句各自都只对一半情形成立。
			"if it does answer with a route, this machine most likely cannot resolve that server's hostname — and for that one, switching servers really does help",
			"Full reason: "+coreLogCommandForAdvice())
	case supervisor.IsTunnelUndeterminedCode(bare):
		headline = "bx could not start: the tunnel did not come up, and bx could not tell whether that server is still there (the diagnostic itself did not complete). "
		steps = append(steps,
			phrase(ncCheckable(where), "To check that server yourself: "+ncCommand(where), "start by checking the server link in your config"),
			"Full reason: "+coreLogCommandForAdvice())
	default:
		// 隧道之外的启动失败(配置 / 释放二进制 / 开 TUN / 劫持路由 / 认不出)。
		// 换一台服务器对它们一点用都没有,所以**不给那句话**。
		tunnelOutcome = false
		headline = "bx could not start: " + nonTunnelStartFailureHeadline(bare)
		steps = append(steps, "Full reason: "+coreLogCommandForAdvice())
	}

	if tunnelOutcome && len(facts.Others) > 0 {
		// 只有一台候选时命令里直接写出它的名字:读到这句话的人正处在「bx 起不来」
		// 的时刻,让他自己把名字抄进一个占位符是白白多一步。
		use := "sudo bx server use <name>"
		if len(facts.Others) == 1 {
			use = "sudo bx server use " + shellQuoteForAdvice(facts.Others[0].Name)
		}
		steps = append(steps, fmt.Sprintf("You have another server configured: %s — %s",
			otherServersLabel(facts.Others), use))
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
		return "bx cannot use what is in the config (one of the rules, CIDRs, hosts entries, or the server link is bad). After fixing it, " + elevate.Prefix + "bx down && " + elevate.Prefix + "bx up. "
	case supervisor.StartFailureConfigUnreadable:
		return "bx could not read its config file — most likely bx has not been set up on this machine yet, or the file is not readable. To set it up: " + elevate.Prefix + "bx setup <your link>. "
	case supervisor.StartFailureProvision:
		return "could not unpack the embedded transport binary into data_dir (most likely the disk is full or that directory is not writable). "
	case supervisor.StartFailureTUNOpen:
		return "could not open the TUN device (permissions, or the device is in use). "
	case supervisor.StartFailureHijack:
		return "the TUN came up, but hijacking the default route failed. "
	}
	return "Core reported a start failure this version of bx has no specific wording for. "
}

// phrase:问得出地址就点名,问不出就说一句仍然成立的话。
// **绝不编一个占位地址** —— 一句指着 `<unknown>:0` 的排查命令比不给更糟。
func phrase(named bool, withName, without string) string {
	if named {
		return withName
	}
	return without
}

// ncCheckable / ncCommand / selfCheckSuffix:**端口解不出来就不给那条 `nc -z`**。
//
// 链接里看不出端口时 joinHostPortForAdvice 只写主机,于是 portOf 返回空串,
// 那条指引渲染成 `nc -z 203.0.113.92 `(尾巴上一个空端口)—— 一条粘贴过去
// 就报错的命令,而它出现在一条唯一目的就是「照着做」的话里。
func ncCheckable(hostPort string) bool {
	return hostOf(hostPort) != "" && portOf(hostPort) != ""
}

func ncCommand(hostPort string) string {
	return "nc -z " + hostOf(hostPort) + " " + portOf(hostPort)
}

func selfCheckSuffix(hostPort string) string {
	if !ncCheckable(hostPort) {
		return ""
	}
	return ". Check for yourself: " + ncCommand(hostPort)
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

// coreLogCommandForAdvice:细节照旧只进 Core 日志(它是给人读的、root-only),
// 而应答体只带码 —— 这条指引是两者之间唯一的桥。
func coreLogCommandForAdvice() string { return coreLogCommandFor(runtime.GOOS) }

// coreLogCommandFor:**Core 日志在哪儿因平台而异。** darwin 上 Guardian 把 Core 的
// 输出写进 /var/log/bx.log;linux 上 Core 由 systemd 直管,输出进 journald ——
// 在 linux 上说「sudo tail /var/log/bx.log」是把人派去看一个不存在的文件,而读到
// 这句话的人正处在 bx 起不来的时刻。
func coreLogCommandFor(goos string) string {
	if goos == "linux" {
		return "sudo journalctl -u bx.service -n 50 --no-pager"
	}
	return "sudo tail -50 /var/log/bx.log"
}

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
			// 当前那台不进 Others(那句话说的是「你还配了**别的**」)。
			continue
		}
		// **只发名字与主机,链接一个字节都不出门。**
		host, _ := setup.LinkHost(server.Link)
		facts.Others = append(facts.Others, otherServerForAdvice{Name: server.Name, Host: host})
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

// otherServerForAdvice 是「你还配了另一台」那句话要的两样东西。链接一个字节都不在这里。
type otherServerForAdvice struct {
	Name string
	Host string
}

// otherServersLabel 把候选服务器写成给人读的一串。名字就是主机时只写一次
// (2026-09-24 真机上那句话写成了「<地址>(<同一个地址>)」);分隔用 ASCII 的
// 逗号 —— 这是一段英文。
func otherServersLabel(others []otherServerForAdvice) string {
	parts := make([]string, 0, len(others))
	for _, o := range others {
		name, host := strings.TrimSpace(o.Name), strings.TrimSpace(o.Host)
		if host == "" || strings.EqualFold(name, host) {
			parts = append(parts, name)
			continue
		}
		parts = append(parts, name+" ("+host+")")
	}
	return strings.Join(parts, ", ")
}

// shellQuoteForAdvice 让命令粘贴过去就能跑:名字只含安全字符时原样,否则单引号括起来。
func shellQuoteForAdvice(name string) string {
	safe := name != ""
	for _, r := range name {
		if !(r == '-' || r == '_' || r == '.' || r == ':' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			safe = false
			break
		}
	}
	if safe {
		return name
	}
	return "'" + strings.ReplaceAll(name, "'", `'\''`) + "'"
}
