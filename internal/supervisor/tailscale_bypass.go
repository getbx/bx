package supervisor

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"time"
)

const tailscaleDERPMapURL = "https://controlplane.tailscale.com/derpmap/default"

var tailscaleBootstrapFallbackCIDRs = []string{
	"64.225.56.166/32",
	"68.183.90.120/32",
	"134.122.94.167/32",
	"144.202.67.195/32",
	"149.28.119.105/32",
	"165.22.33.71/32",
	"178.62.44.132/32",
	"192.73.252.134/32",
	"208.111.34.178/32",
	"216.128.144.130/32",
}

func tailscaleBootstrapBypassCIDRs(ctx context.Context, direct *net.Dialer) []string {
	if runtime.GOOS != "darwin" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tailscaleDERPMapURL, nil)
	if err != nil {
		return mergeBypassCIDRs(tailscaleBootstrapFallbackCIDRs, tailscaleControlplaneFallbackCIDRs())
	}
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			Proxy:       nil,
			DialContext: direct.DialContext,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("tailscale bootstrap bypass:DERP map 获取失败,使用内置 bootstrap 旁路:%v", err)
		return mergeBypassCIDRs(tailscaleBootstrapFallbackCIDRs, tailscaleControlplaneFallbackCIDRs())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		log.Printf("tailscale bootstrap bypass:DERP map 返回 %d,使用内置 bootstrap 旁路", resp.StatusCode)
		return mergeBypassCIDRs(tailscaleBootstrapFallbackCIDRs, tailscaleControlplaneFallbackCIDRs())
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		log.Printf("tailscale bootstrap bypass:读取 DERP map 失败,使用内置 bootstrap 旁路:%v", err)
		return mergeBypassCIDRs(tailscaleBootstrapFallbackCIDRs, tailscaleControlplaneFallbackCIDRs())
	}
	cidrs := tailscaleDERPMapBypassCIDRs(body)
	if len(cidrs) == 0 {
		log.Printf("tailscale bootstrap bypass:DERP map 无 IPv4 节点,使用内置 bootstrap 旁路")
		return mergeBypassCIDRs(tailscaleBootstrapFallbackCIDRs, tailscaleControlplaneFallbackCIDRs())
	}
	out := mergeBypassCIDRs(cidrs, tailscaleControlplaneFallbackCIDRs())
	log.Printf("tailscale bootstrap bypass:已准备 %d 条 DERP/control IPv4 旁路", len(out))
	return out
}

func tailscaleDERPMapBypassCIDRs(data []byte) []string {
	var m struct {
		Regions map[string]struct {
			Nodes []struct {
				IPv4 string `json:"IPv4"`
			} `json:"Nodes"`
		} `json:"Regions"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, region := range m.Regions {
		for _, node := range region.Nodes {
			addr, err := netip.ParseAddr(node.IPv4)
			if err != nil || !addr.Is4() {
				continue
			}
			cidr := netip.PrefixFrom(addr, 32).String()
			if seen[cidr] {
				continue
			}
			out = append(out, cidr)
			seen[cidr] = true
		}
	}
	return out
}

func tailscaleControlplaneFallbackCIDRs() []string {
	out := make([]string, 0, 16)
	for i := 101; i <= 116; i++ {
		out = append(out, netip.PrefixFrom(netip.AddrFrom4([4]byte{192, 200, 0, byte(i)}), 32).String())
	}
	return out
}

func mergeBypassCIDRs(groups ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, group := range groups {
		for _, cidr := range group {
			if cidr == "" || seen[cidr] {
				continue
			}
			out = append(out, cidr)
			seen[cidr] = true
		}
	}
	return out
}
