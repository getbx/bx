package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

// dohResolver 走 DNS-over-HTTPS(JSON API,RFC8484 的 application/dns-json 变体)解析域名。
// TLS 校验 DoH 服务器证书,堵住明文 UDP:53 被投毒 → 直连白名单/国内域名被引到冒牌 IP 的
// 去匿名化路径(见 SECURITY.md)。HTTP 传输走 DirectDialer(防环:DoH 请求直连出网,不进 TUN)。
type dohResolver struct {
	url    string
	client *http.Client
}

// newDoHResolver 用 base(DirectDialer)作 HTTP 传输,防环;TLS 默认校验证书。
func newDoHResolver(dohURL string, base *net.Dialer) *dohResolver {
	tr := &http.Transport{ForceAttemptHTTP2: true}
	if base != nil {
		tr.DialContext = base.DialContext
	}
	return &dohResolver{
		url:    dohURL,
		client: &http.Client{Timeout: 5 * time.Second, Transport: tr},
	}
}

func (r *dohResolver) Resolve(ctx context.Context, domain string) (netip.Addr, error) {
	q := r.url + "?name=" + url.QueryEscape(domain) + "&type=A"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := r.client.Do(req)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("DoH 请求: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return netip.Addr{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("DoH 返回 %d", resp.StatusCode)
	}
	return parseDoHJSON(body)
}

// parseDoHJSON 解析 dns-json 响应,取首条 A(type 1)或 AAAA(type 28)记录;
// Status!=0(如 NXDOMAIN=3)或无地址记录均报错。
func parseDoHJSON(b []byte) (netip.Addr, error) {
	var r struct {
		Status int `json:"Status"`
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return netip.Addr{}, fmt.Errorf("DoH 响应非法 JSON: %w", err)
	}
	if r.Status != 0 {
		return netip.Addr{}, fmt.Errorf("DoH 解析失败 Status=%d", r.Status)
	}
	for _, a := range r.Answer {
		if a.Type != 1 && a.Type != 28 { // 仅 A / AAAA
			continue
		}
		if ip, err := netip.ParseAddr(a.Data); err == nil {
			return ip.Unmap(), nil
		}
	}
	return netip.Addr{}, fmt.Errorf("DoH 无 A/AAAA 记录")
}
