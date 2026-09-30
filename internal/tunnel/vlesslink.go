package tunnel

import (
	"encoding/json"

	"github.com/getbx/bx/internal/singboxout"
)

// vlessLink 是 tunnel 这一侧的 REALITY 参数:内嵌 singboxout.Vless(解析与出站都在那边,
// 判据只有一份),这里只多挂一个起子进程时要的 singboxConfig。
type vlessLink struct {
	singboxout.Vless
}

// parseVlessLink 是 singboxout.ParseVless 的薄壳。
func parseVlessLink(s string) (vlessLink, error) {
	v, err := singboxout.ParseVless(s)
	return vlessLink{Vless: v}, err
}

// singboxConfig 生成最小 sing-box 客户端配置:本地 socks 入站 + vless-reality 出站。
// socksAddr 形如 "127.0.0.1:10800"。bx 数据面只连这个 socks,不关心引擎内部。
// httpAddr 非空时额外开一个 HTTP 代理入站(给只认 HTTP_PROXY 的应用,如 tailscaled
// 控制面;等价于 brook 的 --http,保证切到 reality 后 tailscale 控制面仍走代理)。
func (v vlessLink) singboxConfig(socksAddr, httpAddr string) ([]byte, error) {
	inbounds, err := socksInbounds(socksAddr, httpAddr)
	if err != nil {
		return nil, err
	}
	cfg := map[string]any{
		"log":       map[string]any{"level": "warn", "timestamp": false},
		"inbounds":  inbounds,
		"outbounds": []any{v.Outbound("reality-out")},
	}
	return json.MarshalIndent(cfg, "", "  ")
}
