package leakserve

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/supervisor"
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
		Transport: &http.Transport{
			DialContext: dial,
			// **单次 GET,keep-alive 一点用都没有,而它的代价不为零**:这个进程
			// 在探测结束之后还要活最多 2 分钟等浏览器那半,期间四条到 Anthropic /
			// OpenAI / Google 的空闲 TLS 连接会一直挂到 90 秒的空闲超时 ——
			// 对一个隐私工具那是白送出去的持续连接。
			DisableKeepAlives: true,
		},
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
			// **不说「解析不了」** —— 这一支只在 URL **解析**失败时走到
			// (目标 URL 是本仓库钉死的字面量,它到不了网络那一层),说「这个地址
			// 解析不了」就是**断言了一次并没有发生的 DNS 观测**,与本段
			// 「只说 bx 观测到什么」那条纪律正好反着来。
			State: leakcheck.ReachUnreachable, Detail: "this target URL could not be parsed",
		}
	}
	// 连 DisableKeepAlives 一起是两道:前者不复用,这一句把已经建起来的也收掉,
	// 不等 Go 的空闲回收。
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		// **直连那条路上「我们自己没问出来」不是「不通」。** 本机没把包发出去(另一个
		// VPN 在跑时 scoped 表常常是空的,绑网卡的 socket 当场 ENETUNREACH)、或名字
		// 在物理网卡上解析不出来 —— 都是这一轮没测成。当前路径的判法一个字不改。
		if path == leakcheck.ReachPathBypass {
			if errors.Is(err, errBypassResolve) {
				return leakcheck.ReachProbe{
					TargetID: tgt.ID, Path: path, State: leakcheck.ReachUndetermined,
					Detail: "the name could not be resolved over the physical network interface",
				}
			}
			if supervisor.DialFailedBeforeLeavingThisMachine(err) {
				return leakcheck.ReachProbe{
					TargetID: tgt.ID, Path: path, State: leakcheck.ReachUndetermined,
					Detail: "bx could not send from the physical network interface",
				}
			}
		}
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
		// **状态码是这一段唯一的真观测,必须出示。** 在它进来之前,第四段的
		// Evidence 只是把结论换个词重说一遍(`current path: reachable`),而
		// leakcheck 的地基是「Evidence 是**必须**的那一半 —— bx 判错时用户看得出
		// 它是怎么错的」:同一个 403 可以是 API 在正常应答也可以是人机挑战,
		// 看不到那个数字的人无从复核判据挑了哪一支。
		// **只放这个数字,不放 body、不放原始错误**(ReachProbe.Detail 的注释)。
		Detail: "HTTP " + strconv.Itoa(resp.StatusCode),
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

// LiveReachDeps 接上生产环境的拨号器 —— **默认那一轮只有当前路径**。
//
// 「绕过隧道」那条路是 opt-in(`bx leakcheck --compare-direct`,所有者 2026-09-23 定):
// 它由 WithBypass(reach_bypass.go)按需接上,这里刻意不供货,`DefaultProbeBypass`
// 因此仍是 false —— 两者由 reach_test 里那条守卫绑在一起(只翻常量不供拨号器,
// 这一轮会安静地什么都不多跑)。
//
// 直连那条路的几件事住在 reach_bypass.go 与它的消费方里:名字解析也走物理网卡
// (系统 DNS 在 bx 开着时答的是假 IP);本机没把包发出去 / 解析不出来判「没测成」
// (probeOne);披露那句说出「会从物理网卡发、会暴露真实 IP」(announceReachTargets);
// 两条路一比的那句话由 leakcheck.reachComparison 追加在结论末尾。
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
	paths := reachPathsOf(deps)
	if len(paths) == 0 {
		// **nil 而不是一组 undetermined 记录。** 「这一轮没跑探测」与「探过了、
		// 没问出来」是两句不同的话,judgeReachTarget 对前者说的是「这一轮没有
		// 检查」,对后者说的是「认不出」。造一组假记录会让它说错那一句。
		return nil
	}
	targets := leakcheck.ReachTargets()
	ctx, cancel := context.WithTimeout(ctx, ReachBudgetFor(deps))
	defer cancel()

	out := make([]leakcheck.ReachProbe, 0, len(targets)*len(paths))
	for _, p := range paths {
		out = append(out, ProbeReach(ctx, p.dial, p.name)...)
	}
	return out
}

type reachPath struct {
	dial DialFunc
	name string
}

// reachPathsOf 说出这份 deps 这一轮真的会跑哪几条路。**预算与执行共用它** ——
// 两边各数一遍的话,加一条路径时可能只改了执行那半,而多出来的那些探测会被一份
// 没跟着长的预算静默掐掉。
func reachPathsOf(deps ReachDeps) []reachPath {
	paths := make([]reachPath, 0, 2)
	if deps.CurrentDial != nil {
		paths = append(paths, reachPath{deps.CurrentDial, leakcheck.ReachPathCurrent})
	}
	if deps.BypassDial != nil {
		paths = append(paths, reachPath{deps.BypassDial, leakcheck.ReachPathBypass})
	}
	return paths
}

// WillProbe 说出这份 deps 这一轮到底会不会发请求。
//
// 披露那一句(CLI 的 announceReachTargets)必须与探测**读同一个判据** ——
// 各判各的话,两个漂开的方向分别是「探了没说」与「说了没探」,都是假话。
func (d ReachDeps) WillProbe() bool { return len(reachPathsOf(d)) > 0 }

// ReachBudgetFor 是**这一份 deps** 这一轮探测的上限,给「这一步最多要等多久」
// 那句话用。它算的就是 CollectReach(ctx, deps) 会用的那份预算。
//
// **必须吃调用方手里那份 deps,不许自己去取 LiveReachDeps()。** 此前的无参版本
// 就是那么写的:披露那一句问的是「我这一轮会发几个探测」,而答案来自**另一个
// 对象**。今天两者恰好是同一份,所以数字对;`DefaultProbeBypass` 翻成 true 的
// 那天探测数翻倍、预算变成 69 秒,而一个拿着 `--no-reach` 之外的定制 deps 的
// 调用方会在屏幕上读到一个不属于自己那一轮的秒数 —— 而这个文件上面刚用三段
// 注释论证过「披露与探测必须读同一个值」(WillProbe),预算是同一件事的另一半。
//
// 无参那个版本**已删**,不是保留着不用:留着它,下一个人照着 `WillProbe()` 的
// 形状写一行 `leakserve.ReachBudget()` 是最自然的动作,而编译器不会拦他。
func ReachBudgetFor(deps ReachDeps) time.Duration {
	return reachBudget(len(leakcheck.ReachTargets()) * len(reachPathsOf(deps)))
}
