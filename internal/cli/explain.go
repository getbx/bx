package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/getbx/bx/internal/dialfail"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/pathview"
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

	if note := explainHistoryNote(rep); note != "" {
		b.WriteString("\n" + note)
	}
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
	run := explainCountLine("  本次    ", p.Run)
	history := explainCountLine("  累计    ", p.History)
	// **具名规则本次 0 次记录,必须明说,不许留白。**
	//
	// 具名规则的每一次判定都按 (source, rule) 计数,表里没有条目就是 0 次 ——
	// 对这一档 nil 与 0 是同一件事(与下面 Rule=="" 那一档相反,那里不记名)。
	// 真机 2026-09-03:Tailscale 拨某 direct 域名的 fake-IP 超时,explain 判
	// DIRECT 而「本次」整行缺席;缺席正是答案(bx 从没见过那条连接,tailscaled
	// 的 socket 绑在 en0 没进 TUN),而缺席最容易被看漏,于是被记成「explain 骗人」。
	if p.Rule != "" && run == "" {
		run = "  本次    0 次判定 —— bx 这次运行没见过走这条规则的连接;要是有程序明明在连它,那些连接多半没进 bx(绑了物理网卡的 socket、或目的地在旁路路由里)\n"
	}
	b.WriteString(run)
	b.WriteString(history)
	// **没有规则可点名时,这两个数是一个桶的合计,不是这个目标的。**
	//
	// 计数按 (source, rule) 记。命中具体规则时它确实就是那条规则的成败(这正是
	// `*.qq.com 2658 次 / 410 次失败` 有用的原因);而 Rule=="" 那一档
	// (默认 / 内建列表)收的是**走这条路的全部流量**,把它印在一个具体目标下面,
	// 读起来就是那个目标的 —— 真机实测:steamstatic.com 与 1.1.1.1 拿到逐字相同
	// 的 49/15228/73,而它们毫无关系。
	//
	// 不删掉这两行是因为那个信息本身有用(「默认这条路整体 0.5% 失败」是背景),
	// 删了就换成另一种失真。加一句话把它归位。
	if p.Rule == "" && (run != "" || history != "") {
		fmt.Fprintf(b, "  注      这两个数是走「%s」这条路的全部流量的合计,不是这个目标的(这一层没有具体规则可点名)\n",
			explainSourceLabel(p.Source))
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
	line := fmt.Sprintf("%s%d 次判定 / %d 次失败 (%.1f%%)", prefix, o.Attempts, o.Failures, pct)
	if kinds := explainFailureKinds(o.FailureKinds); kinds != "" {
		line += "  " + kinds
	}
	return line + "\n"
}

// explainFailureKinds 把失败拆成可行动的几类。
//
// **一个百分比答不出该不该管。** 同样是 15%,全是 unreachable 就要立刻去查
// 路由(2026-08-13 那个 DirectDialer 故障的签名),全是 timeout 就一个字都
// 不用改。按次数倒序,最大的那一类排最前 —— 读的人只需要看第一个。
//
// 空表返回空串:这一版没有分类、或者归不了类,都**不许显示成「各类都是 0」**。
func explainFailureKinds(kinds map[string]int64) string {
	if len(kinds) == 0 {
		return ""
	}
	type kv struct {
		kind string
		n    int64
	}
	list := make([]kv, 0, len(kinds))
	for k, n := range kinds {
		list = append(list, kv{k, n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].kind < list[j].kind // 并列时定序,否则 map 迭代序让输出 diff 不了
	})
	parts := make([]string, 0, len(list))
	for _, e := range list {
		parts = append(parts, fmt.Sprintf("%s×%d", explainFailureKindLabel(e.kind), e.n))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func explainFailureKindLabel(kind string) string {
	switch kind {
	case dialfail.Unreachable:
		return "路由不可达"
	case dialfail.Timeout:
		return "对端不应答"
	case dialfail.Refused:
		return "对端拒绝"
	case dialfail.Reset:
		return "被重置"
	case dialfail.DNS:
		return "够不着解析器"
	case dialfail.DNSNotFound:
		return "域名不存在"
	case dialfail.Canceled:
		return "调用方取消"
	case dialfail.EgressUnwired:
		return "出口未接线"
	case dialfail.Other:
		return "其它"
	}
	return kind
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
		return "split-DNS 或 fakeip_filter 解析出的真实 IP(强制直连)"
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
	// 本机视角先采(它不依赖 Core):这个目标在这台机器上会怎么走。
	ctx, cancel := context.WithTimeout(context.Background(), explainMachineTimeout)
	defer cancel()
	view := pathview.Judge(collectPathFacts(ctx, target))

	rep, err := supervisor.FetchExplain(supervisor.SockPath, target)
	if err != nil {
		if errors.Is(err, supervisor.ErrExplainUnsupported) {
			return errors.New("跑着的这一版 Core 没有发布判定查询 —— 升级后重试(bx explain 需要 Core 侧的 /v0/explain)")
		}
		// **「连不上」与「连上了但答不了」措辞必须不同。** 后者 bx 明明应答了,
		// 再叫人去 `bx up` 就是把他送去做一件确定无用的事。前者不再是错误:
		// bx 没在跑时本机视角照样有用,那正是它存在的理由。
		if !isControlSocketUnreachable(err) {
			return fmt.Errorf("Core 答不了这个问题:%w", err)
		}
	}
	out, oerr := explainOutput(view, rep, err, c.Bool("json"))
	if oerr != nil {
		return oerr
	}
	fmt.Print(out)
	return nil
}

// explainMachineTimeout 封顶本机视角的采集(几条 route get + 一次系统解析)。
const explainMachineTimeout = 6 * time.Second

// explainOutput 把本机视角与 Core 判定拼成最终输出。**本机视角在前**;coreErr
// 非空(Core 连不上)时不渲染零值判定,只留一句。JSON 在 Core 应答上**追加**
// machine 键,顶层字段一个不动 —— MCP 的 bx_explain 直接转发这份 JSON。
func explainOutput(view pathview.View, rep supervisor.ExplainResponse, coreErr error, asJSON bool) (string, error) {
	if asJSON {
		var top map[string]any
		if coreErr == nil {
			raw, err := json.Marshal(rep)
			if err != nil {
				return "", err
			}
			if err := json.Unmarshal(raw, &top); err != nil {
				return "", err
			}
		} else {
			top = map[string]any{"core_unavailable": coreErr.Error()}
		}
		top["machine"] = view
		out, err := json.MarshalIndent(top, "", "  ")
		if err != nil {
			return "", err
		}
		return string(out) + "\n", nil
	}
	var b strings.Builder
	b.WriteString(renderMachineView(view))
	if coreErr != nil {
		b.WriteString("\nbx 没在跑(连不上 Core 的控制 socket),以上是没有 bx 时的样子;要看 bx 的判定先 sudo bx up。\n")
		return b.String(), nil
	}
	b.WriteString("\n")
	b.WriteString(renderExplain(rep))
	return b.String(), nil
}

// renderMachineView:先一句结论,再几行证据,标签对齐到与 Core 那半同宽。
func renderMachineView(v pathview.View) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s\n", padLabel("结论"), v.Conclusion)
	for _, l := range v.Lines {
		fmt.Fprintf(&b, "%s%s\n", padLabel(l.Label), l.Text)
	}
	return b.String()
}

// padLabel 把标签补到与 Core 那半同宽(10 列)。%-8s 按字符数补,CJK 每字占两列,
// 会让「目标类型」比「解析」多凸出去四列。
func padLabel(label string) string {
	width := 0
	for _, r := range label {
		if r > 0x7f {
			width += 2
		} else {
			width++
		}
	}
	if pad := 10 - width; pad > 0 {
		return label + strings.Repeat(" ", pad)
	}
	return label + " "
}

// collectPathFacts 是本机视角的**事实采集**(全部只读:几条 route get、一次系统
// 解析、一次 Core 运行时读取)。每一项失败都如实进 Facts,判据在 pathview 里
// 按「问不出来」处置,绝不让 explain 整个失败。
func collectPathFacts(ctx context.Context, target string) pathview.Facts {
	f := pathview.Facts{Target: target, FakeIP: netip.MustParsePrefix("198.18.0.0/15")}
	if addr, err := netip.ParseAddr(target); err == nil {
		f.LiteralIP = true
		f.Addrs = []netip.Addr{addr.Unmap()}
	} else if addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", target); err != nil {
		f.ResolveErr = err.Error()
	} else {
		for _, a := range addrs {
			f.Addrs = append(f.Addrs, a.Unmap())
		}
	}
	// Core 在跑时知道自己的 TUN 与服务器旁路;不在跑时这两样「问不出来」。
	if state, err := supervisor.FetchRuntimeState(supervisor.SockPath); err == nil {
		f.CoreRunning = true
		f.BxTun, f.BxTunKnown = state.TunName, state.TunName != ""
		for _, c := range state.ServerBypass {
			if p, err := netip.ParsePrefix(c); err == nil {
				f.ServerBypass = append(f.ServerBypass, p)
			}
		}
	}
	f.China = pathview.ChinaSetFromList(strings.Split(strings.TrimSpace(string(embedded.ChinaCIDR())), "\n"))
	if gw, dev, err := supervisor.PhysicalDefaultRoute(ctx); err == nil {
		_ = gw
		f.PhysicalDev = dev
	}
	addr, ok := firstUsableAddr(f.Addrs)
	if !ok {
		return f
	}
	f.Route = routeFact(func() (supervisor.RouteSelection, error) {
		return supervisor.LookupRoute(ctx, addr.String(), addr.Is6())
	})
	if f.PhysicalDev != "" && addr.Is4() {
		f.Bound = routeFact(func() (supervisor.RouteSelection, error) {
			return supervisor.LookupBoundRoute(ctx, f.PhysicalDev, addr.String())
		})
	}
	return f
}

func firstUsableAddr(addrs []netip.Addr) (netip.Addr, bool) {
	for _, a := range addrs { // v4 优先:v6 在 bx 下是 fail-closed 阻断,先答 v4 那条
		if a.Is4() {
			return a, true
		}
	}
	for _, a := range addrs {
		if a.IsValid() {
			return a, true
		}
	}
	return netip.Addr{}, false
}

// routeFact 把一次路由查询翻成 pathview 的三态事实。「不支持」记为没问;
// ErrRouteMissing 记为「表里没有」(那是答案);其余错误记为问不出来。
func routeFact(lookup func() (supervisor.RouteSelection, error)) pathview.RouteFact {
	sel, err := lookup()
	switch {
	case err == nil:
		return pathview.RouteFact{Applicable: true, Interface: sel.Interface, Gateway: sel.Gateway, Reject: sel.Reject}
	case errors.Is(err, supervisor.ErrRouteMissing):
		return pathview.RouteFact{Applicable: true, Missing: true}
	case strings.Contains(err.Error(), "only implemented"):
		return pathview.RouteFact{Applicable: false}
	default:
		return pathview.RouteFact{Applicable: true, Err: err.Error()}
	}
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

// explainHistoryNote 说明「累计」那一行覆盖了多长时间、跨了几个版本。
//
// 少了它,「累计 2679 次 / 410 次失败」答不出**这是什么时候的事** —— 一个跨了
// 半年、几个版本的 15% 与一天之内的 15% 是完全不同的两件事,而读的人会默认
// 它是后者。两个数本来就在 RuleHistorySnapshot 里,此前被整个丢掉了。
//
// 没有历史可言时一个字都不说:一句「累计覆盖 0 天」比不说更容易被读错。
func explainHistoryNote(rep supervisor.ExplainResponse) string {
	if rep.HistoryWindowSeconds <= 0 {
		return ""
	}
	note := fmt.Sprintf("累计口径  覆盖 Core 累计在跑的 %.1f 天", float64(rep.HistoryWindowSeconds)/86400)
	if n := len(rep.HistoryVersions); n > 1 {
		// **跨版本要说**:中间几版的计数行为可能并不一致,那份累计要打折看。
		note += fmt.Sprintf(",跨 %d 个版本(%s)", n, strings.Join(rep.HistoryVersions, "、"))
	}
	if rep.HistoryOverflowed {
		// 表满过之后,「这条规则没有条目」不再等于「它没命中过」。
		note += ";跟踪表溢出过 —— 没有条目不等于没命中"
	}
	return note + "\n"
}
