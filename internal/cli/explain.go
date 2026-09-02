package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"

	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/urfave/cli/v2"
)

// renderExplain 是**纯函数**:给定一份 ExplainResponse,产出人读的那几行。
//
// 抽出来的理由与本仓库其它渲染层相同 —— 「说了什么」与「说得像句人话」是两件
// 事,而后者只有把它变成可断言的字符串才盯得住(summarizeFindings 那次无条件
// 拼 `← CoveredBy`、输出 `*.a ← 、*.b ← ` 一句没写完的话,既有断言全绿)。
func renderExplain(rep supervisor.ExplainResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "目标      %s\n", rep.Target)
	if rep.Domain != "" && rep.Domain != rep.Target {
		if rep.FakeIPResolved {
			fmt.Fprintf(&b, "          假 IP 反查到域名 %s\n", rep.Domain)
		} else {
			fmt.Fprintf(&b, "          域名 %s\n", rep.Domain)
		}
	}
	if rep.RouterMissing {
		// **说不知道,而不是让一份零值读起来像一个判定。**
		b.WriteString("\n还没有路由表可问 —— bx 此刻不在做任何分流判定。\n")
		return b.String()
	}

	writeExplainPath(&b, "TCP", rep.TCP)
	writeExplainPath(&b, "UDP", rep.UDP)

	fmt.Fprintf(&b, "\n隧道      %s", explainHealthLabel(rep.TunnelHealth))
	if rep.UDPTransportHealth != "unknown" {
		fmt.Fprintf(&b, "  ·  UDP 专用传输 %s", explainHealthLabel(rep.UDPTransportHealth))
	}
	b.WriteString("\n")
	return b.String()
}

func writeExplainPath(b *strings.Builder, label string, p supervisor.ExplainPath) {
	fmt.Fprintf(b, "\n%-9s %s", label, strings.ToUpper(p.Effective))
	// **路由判定与实际结局不同时,必须把那一跳说出来** —— 「判定走隧道,而隧道
	// 不健康所以被拦下」正是「我的请求为什么失败」的答案,少了它用户只看到
	// 一个 BLOCKED 而不知道该去修什么。
	if p.BlockedBy == "killswitch" {
		fmt.Fprintf(b, "(路由判定是 %s,但隧道不健康 → kill-switch 拦下)", p.Decision)
	} else if p.BlockedBy == "udp_mode_block" {
		b.WriteString("(udp.mode=block:UDP 整个被丢弃)")
	}
	b.WriteString("\n")

	if p.Rule != "" {
		fmt.Fprintf(b, "  依据    %s: %s\n", explainSourceLabel(p.Source), p.Rule)
	} else {
		fmt.Fprintf(b, "  依据    %s\n", explainSourceLabel(p.Source))
	}
	if line := explainCountLine("  本次    ", p.Run); line != "" {
		b.WriteString(line)
	}
	if line := explainCountLine("  累计    ", p.History); line != "" {
		b.WriteString(line)
	}
	if p.Egress != "" {
		fmt.Fprintf(b, "  出口    %s\n", p.Egress)
	}
}

// explainCountLine 只在**真有记录**时占地方。
//
// nil 与「记录为 0」是两件事:前者是这条规则从没被记过(可能是内建列表命中,
// 它本来就不记名),后者是记了、确实一次没命中。把 nil 渲染成「0 次」会把
// 「没这个记录」说成「一次都没走过」,而那正是死规则判据最忌讳的假阳性。
func explainCountLine(prefix string, o *stats.RuleOutcome) string {
	if o == nil || o.Attempts == 0 {
		return ""
	}
	if o.Failures == 0 {
		return fmt.Sprintf("%s%d 次判定,无失败\n", prefix, o.Attempts)
	}
	pct := float64(o.Failures) * 100 / float64(o.Attempts)
	return fmt.Sprintf("%s%d 次判定 / %d 次失败 (%.1f%%)\n", prefix, o.Attempts, o.Failures, pct)
}

func explainSourceLabel(source string) string {
	switch source {
	case "user_direct", "user_direct_ip":
		return "用户规则 direct"
	case "user_proxy", "user_proxy_ip":
		return "用户规则 proxy"
	case "user_egress":
		return "用户具名出口"
	case "china_domain":
		return "内建 china 域名列表"
	case "china_cidr":
		return "内建 china IP 段"
	case "private":
		return "私网恒直连(不受 global 影响)"
	case "split_dns":
		return "split-DNS 解析出的内网真实 IP"
	case "default":
		return "没有命中任何列表(默认)"
	case "udp_proxy":
		return "UDP 走专用传输"
	case "udp_proxy_fallback":
		return "UDP 专用传输不健康,回落主传输"
	case "udp_direct_realtime":
		return "udp.mode=direct-realtime(以真实 IP 直连)"
	case "udp_block":
		return "udp.mode=block"
	case "":
		return "(未记名)"
	}
	return source
}

func explainHealthLabel(h string) string {
	switch h {
	case "healthy":
		return "健康"
	case "unhealthy":
		return "不健康"
	}
	// **「没有健康探针」不是「不健康」。** 压成不健康会把「传输还没装好」
	// 说成「隧道断了」,而 kill-switch 对两者的处置相同不代表它们是同一件事。
	return "未知(没有健康探针;kill-switch 按不健康处理)"
}

func explainAction(c *cli.Context) error {
	target := strings.TrimSpace(c.Args().First())
	if target == "" {
		return errors.New("要问哪个目标?例如:bx explain steamstatic.com")
	}
	rep, err := supervisor.FetchExplain(supervisor.SockPath, target)
	if err != nil {
		if errors.Is(err, supervisor.ErrExplainUnsupported) {
			return errors.New("跑着的这一版 Core 没有发布判定查询 —— 升级后重试(bx explain 需要 Core 侧的 /v0/explain)")
		}
		// **「连不上」与「连上了但答不了」措辞必须不同。** 前者 bx 可能真没在跑;
		// 后者 bx 明明应答了,再叫人去 `bx up` 就是把他送去做一件确定无用的事。
		if isControlSocketUnreachable(err) {
			return fmt.Errorf("连不上跑着的 Core(%v)—— bx 没在跑时它没有判定可言,先 sudo bx up", err)
		}
		return fmt.Errorf("Core 答不了这个问题:%w", err)
	}
	if c.Bool("json") {
		out, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}
	fmt.Print(renderExplain(rep))
	return nil
}

// isControlSocketUnreachable 区分「拨不通控制 socket」与「拨通了、对方拒答」。
//
// 判据取 net.Error/syscall 那一类而不是字符串匹配:控制面的错误串是中文的、
// 会变,而「连接失败」这件事在类型上是确定的。
func isControlSocketUnreachable(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}
