//go:build darwin

package supervisor

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
)

// ServerBypassRoutesIntact 回答「发往每一台服务器的包,此刻走的是不是我们的 TUN」。
//
// **只读**:一台服务器一条 `route -n get <ip>`(不带 -ifscope:问的是普通 socket
// 会走哪条路,隧道子进程正是普通 socket),不发包不拨号。
func ServerBypassRoutesIntact(ctx context.Context, tunName string, servers func() []netip.Addr) (intact bool, known bool, err error) {
	var lookups []routeLookup
	for _, addr := range servers() {
		if !addr.Unmap().Is4() {
			continue
		}
		ip := addr.Unmap().String()
		out, err := exec.CommandContext(ctx, "route", "-n", "get", ip).CombinedOutput()
		if err != nil {
			lookups = append(lookups, routeLookup{Addr: ip, Err: fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))})
			continue
		}
		sel, perr := parseDarwinRouteSelection(out)
		if perr != nil {
			lookups = append(lookups, routeLookup{Addr: ip, Err: perr})
			continue
		}
		lookups = append(lookups, routeLookup{Addr: ip, Interface: sel.Interface})
	}
	return decideServerBypassIntact(tunName, lookups)
}
