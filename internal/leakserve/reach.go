package leakserve

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
)

// DefaultProbeBypass 决定「绕过隧道」那条路径默不默认跑。
//
// **今天是 false,这是 spec §5.1 的待定项。** 那条路径会从物理网卡直接发 4 个
// GET,把用户的**真实 IP** 暴露给 Anthropic / OpenAI / Google —— 而 leakcheck
// 今天的探测(icanhazip / cloudflare trace)走的都是当前路径,绑物理网卡发请求
// 是新增行为。所有者拍板之前保守。**翻转成本为零**——改这一个常量即可。
const DefaultProbeBypass = false

// probeTimeout 是单个目标的上限。四个目标串行,最坏 4×。
const probeTimeout = 8 * time.Second

// DialFunc 是探测用的拨号器。两条路径的区别**只在这里**:
// 当前路径给普通 Dialer,绕过隧道给绑了物理接口的那个。
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func probeOne(ctx context.Context, dial DialFunc, tgt leakcheck.ReachTarget, path string) leakcheck.ReachProbe {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	client := &http.Client{
		Transport: &http.Transport{DialContext: dial},
		// **不跟随重定向。** 跟过去之后 status 与 body 都是**终点**的,而这条记录
		// 仍然标着起点的 TargetID —— 那就是「记录 A 的观测、归因给 B」。
		// 3xx 因此落进 JudgeReach 的「认不出的状态码 ⇒ Undetermined」那一支,
		// 而那正是诚实的答案:目标把我们支到别处去了,我们没问出它自己的状态。
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	// **Detail 是给用户看的一句话,而这份报告通篇是英文**(CLAUDE.md「服务端写的是
	// 中文」那条先例:`rulereview`/`deadFindings` 的中文 summary 原样渲染进英文
	// 菜单,是本仓库罚过的形状)。`Detail` 此前零消费方,Task 5 是第一个把它送上
	// 用户可见面的 —— 中文字面量因此在这里成了缺陷,一并改成英文。
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tgt.URL, nil)
	if err != nil {
		return leakcheck.ReachProbe{
			TargetID: tgt.ID, Path: path,
			State: leakcheck.ReachUnreachable, Detail: "this address could not be resolved",
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return leakcheck.ReachProbe{
			TargetID: tgt.ID, Path: path,
			State:  leakcheck.JudgeReach(0, nil, err),
			Detail: "this path could not be reached",
		}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return leakcheck.ReachProbe{
		TargetID: tgt.ID, Path: path,
		State: leakcheck.JudgeReach(resp.StatusCode, body, nil),
	}
}

// ProbeReach 串行跑完一条路径上的全部目标。
// **串行是刻意的**:四个目标同时握手是一个很整齐的模式,而它们恰好都是
// AI 服务商(与 Servers 窗口「只在用户点时发、且串行」同一条)。
func ProbeReach(ctx context.Context, dial DialFunc, path string) []leakcheck.ReachProbe {
	targets := leakcheck.ReachTargets()
	out := make([]leakcheck.ReachProbe, 0, len(targets))
	for _, tgt := range targets {
		out = append(out, probeOne(ctx, dial, tgt, path))
	}
	return out
}

// reachBudgetSlack 是整轮探测在「每个探测各自的上限之和」之上留的余量
// (DNS 解析、TLS 握手之外的调度开销)。
const reachBudgetSlack = 5 * time.Second

// reachBudget 由**这一轮真的要发几个探测**派生,不写死一个秒数。
//
// 写死的后果是静默的:加第五个目标(或把 bypass 那条路打开,探测数翻倍)的那天,
// 排在后面的目标会被外层预算掐断,而被掐断的探测**长得和「这条路不通」一模一样**
// —— 这一段最不该产生的答案。派生出来的预算跟着目标数一起长,那一天什么都不用改。
func reachBudget(probes int) time.Duration {
	return time.Duration(probes)*probeTimeout + reachBudgetSlack
}

// ReachDeps 是跑一轮可达性探测所需的拨号器。
//
// **两条路径各一个拨号器,nil 表示这一轮不跑那条路** —— 而不是「跑,但用默认
// 拨号器」:两条路径共用一个不绑网卡的拨号器时,它们会走同一条路,而报告仍然
// 把它们当成两条独立观测并排出示,那比不跑更糟(一句凭空造出来的对照)。
type ReachDeps struct {
	// CurrentDial 走系统路由:bx / 别人的 VPN / 裸奔,它经的是什么就测什么。
	CurrentDial DialFunc
	// BypassDial 绑物理网卡,绕过隧道。**今天恒为 nil**,见 LiveReachDeps。
	BypassDial DialFunc
}

// LiveReachDeps 接上生产环境的拨号器。
//
// **绕过隧道那条路今天没有产地,这是刻意的,不是忘了接。** `DefaultProbeBypass`
// 是 false(spec §5.1 把「暴露真实 IP 给 Anthropic/OpenAI/Google」这个决定留给
// 所有者);它翻成 true 的那天还需要一个绑物理网卡(IP_BOUND_IF)的拨号器,而
// 那个拨号器今天不存在。翻转常量时**必须同时供货 BypassDial** —— 只翻常量而
// 让 BypassDial 留空,这一轮会安静地什么都不多跑,而守卫全绿。
//
// **spec §5 承诺的那三句比较结论**(「直连不行、走当前隧道行」/「两边都不行,
// 换台服务器」/「两边都行」)属于同一天:今天只有 current 一条路径,没有可比的
// 对照,所以 judgeReachTarget 与渲染层对「两条路一比」一个字都不说 —— 不是漏了,
// 是没有素材。bypass 那条路打开之后,那三句话是它的第一批消费方。
func LiveReachDeps() ReachDeps {
	return ReachDeps{
		CurrentDial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
		// BypassDial 故意留空 —— 见上面那段。
	}
}

// CollectReach 跑一轮可达性探测,**自带预算**。
//
// **它刻意不住在 CollectFacts 里。** 那一轮的预算是 DefaultFactsBudget = 5 秒,
// 量的是本机毫秒级的只读观测(route / scutil / 一次本机 socket 往返);而这里是
// 四次跨洋 TLS 握手,最坏 4×probeTimeout。塞进去的后果不是「慢一点」,是**探测
// 跑到一半被掐断,剩下的目标全部变成「没问出来」** —— 而那个答案与「这条路真的
// 不通」在屏幕上长得一模一样。两件事量级差两个数量级,就该是两跳两份预算。
func CollectReach(ctx context.Context, deps ReachDeps) []leakcheck.ReachProbe {
	type reachPath struct {
		dial DialFunc
		name string
	}
	paths := make([]reachPath, 0, 2)
	if deps.CurrentDial != nil {
		paths = append(paths, reachPath{deps.CurrentDial, leakcheck.ReachPathCurrent})
	}
	if deps.BypassDial != nil {
		paths = append(paths, reachPath{deps.BypassDial, leakcheck.ReachPathBypass})
	}
	if len(paths) == 0 {
		// **nil 而不是一组 undetermined 记录。** 「这一轮没跑探测」与「探过了、
		// 没问出来」是两句不同的话,judgeReachTarget 对前者说的是「这一轮没有
		// 检查」,对后者说的是「认不出」。造一组假记录会让它说错那一句。
		return nil
	}
	targets := leakcheck.ReachTargets()
	ctx, cancel := context.WithTimeout(ctx, reachBudget(len(targets)*len(paths)))
	defer cancel()

	out := make([]leakcheck.ReachProbe, 0, len(targets)*len(paths))
	for _, p := range paths {
		out = append(out, ProbeReach(ctx, p.dial, p.name)...)
	}
	return out
}
