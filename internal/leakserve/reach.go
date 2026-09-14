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
