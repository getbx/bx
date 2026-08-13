//go:build darwin

package cli

import (
	"context"
	"strings"

	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/supervisor"
)

// darwinTunnelClaimChecks 按**行为**回答「有没有别的隧道在抢公网流量」。
//
// 判据与 `bx leakcheck` 那条共用同一个纯函数(leakcheck.ClassifyTunnels)——
// 两处各写一份的话,`bx leak-check` 与 `bx leakcheck` 会对同一台机器给出不同答案,
// 两个都自认正确,而用户无从分辨谁对。
//
// **它取代的是一堆猜测。** 此前这里按产品名列清单,措辞是「may create another
// tunnel when connected」「verify its routes do not bypass bx」—— 让用户去查一件
// bx 自己查得到的事。而产品名本来也分不出类:同一个产品会随配置在叠加与竞争之间
// 翻转(Tailscale 的 exit node、WireGuard 的 AllowedIPs)。
func darwinTunnelClaimChecks(netstatOut, bxTun string) []checkReport {
	if strings.TrimSpace(netstatOut) == "" {
		// **「没问出来」不是「没有人在抢」。** 压成沉默正是这个仓库反复消灭的那种谎。
		return []checkReport{{
			Name:   "tunnel_claims",
			Status: "info",
			Detail: "the routing table could not be read, so it is not known whether another tunnel is taking public traffic",
		}}
	}

	local := leakcheck.LocalFacts{BXTunInterface: bxTun}
	for _, line := range strings.Split(netstatOut, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] == "Destination" || fields[0] == "Internet:" {
			continue
		}
		if strings.Contains(fields[2], "W") {
			continue // ARP 克隆条目,不是转发路由
		}
		local.Routes = append(local.Routes, leakcheck.RouteEntry{
			Destination: fields[0], Interface: fields[3], Flags: fields[2],
			// **平台标志由知道那个平台的人翻译。** 判据层不解析 flag 字母 ——
			// Linux 根本没有它们,在那边合成一串假 flags 才是撒谎。
			Blocking: strings.ContainsAny(fields[2], "RB"),
			Scoped:   strings.Contains(fields[2], "I"),
		})
	}

	var competing []string
	for _, claim := range leakcheck.ClassifyTunnels(local) {
		if claim.ClaimsPublicSpace {
			competing = append(competing, claim.Interface+" ("+strings.Join(claim.Prefixes, ", ")+")")
		}
	}
	if len(competing) == 0 {
		// 共存正常是用户的默认预期 —— 正常时说话只会让他以为出了事。
		return nil
	}
	return []checkReport{{
		Name:   "tunnel_claims",
		Status: "warn",
		Detail: "another tunnel has claimed public address space: " + strings.Join(competing, "; "),
		Hint:   "that is a full-tunnel VPN's behaviour, whatever product it belongs to — turn it off, or expect it and not bx to carry this traffic",
	}}
}

// darwinBXTunName 问 Core「你的 TUN 叫什么」。
//
// **必须问 Core,不能猜。** bx 与 Tailscale 在 macOS 上都叫 utunN,靠名字分不开 ——
// 猜错的后果是把 bx 自己的 split-default 报成「有人在抢公网流量」,即最蠢的那种误报。
//
// 问不出来返回空串是安全的,而理由要说准:**控制 socket 拨不通基本等于 Core 没在跑**,
// 那时根本不存在「bx 自己的隧道」需要排除,ClassifyTunnels 报出来的每一条都确实是
// 别人的。(而 `bx leak-check` 在 bx 关着时运行正是常态 —— 这个功能的主场景之一。)
func darwinBXTunName(_ context.Context) string {
	state, err := supervisor.FetchRuntimeState(supervisor.SockPath)
	if err != nil {
		return ""
	}
	return state.TunName
}
