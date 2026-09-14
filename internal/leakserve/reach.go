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

	client := &http.Client{Transport: &http.Transport{DialContext: dial}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tgt.URL, nil)
	if err != nil {
		return leakcheck.ReachProbe{
			TargetID: tgt.ID, Path: path,
			State: leakcheck.ReachUnreachable, Detail: "这个地址解析不了",
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return leakcheck.ReachProbe{
			TargetID: tgt.ID, Path: path,
			State:  leakcheck.JudgeReach(0, nil, err),
			Detail: "这条路到不了",
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
