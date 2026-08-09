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
	body, err := r.query(ctx, domain)
	if err != nil {
		return netip.Addr{}, err
	}
	return parseDoHJSON(body)
}

// ResolveAll 给出全部 A/AAAA 应答,供 bypass 刷新用。
//
// 为什么不能只给首条:服务器域名有多条 A 记录时,bypass 只覆盖其中一条,
// 而隧道子进程可能连到没被覆盖的那条 —— 成环。UDP 解析器走 LookupNetIP
// 天然拿全部,DoH 这条路以前只取首条,故补上。
func (r *dohResolver) ResolveAll(ctx context.Context, domain string) ([]netip.Addr, error) {
	body, err := r.query(ctx, domain)
	if err != nil {
		return nil, err
	}
	return parseDoHJSONAll(body)
}

func (r *dohResolver) query(ctx context.Context, domain string) ([]byte, error) {
	q := r.url + "?name=" + url.QueryEscape(domain) + "&type=A"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DoH 请求: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DoH 返回 %d", resp.StatusCode)
	}
	return body, nil
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

// parseDoHJSONAll 与 parseDoHJSON 同源,但把**全部** A/AAAA 记录交出来。
func parseDoHJSONAll(b []byte) ([]netip.Addr, error) {
	var r struct {
		Status int `json:"Status"`
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("DoH 响应非法 JSON: %w", err)
	}
	if r.Status != 0 {
		return nil, fmt.Errorf("DoH 解析失败 Status=%d", r.Status)
	}
	var out []netip.Addr
	for _, a := range r.Answer {
		if a.Type != 1 && a.Type != 28 { // 仅 A / AAAA
			continue
		}
		if ip, err := netip.ParseAddr(a.Data); err == nil {
			out = append(out, ip.Unmap())
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("DoH 无 A/AAAA 记录")
	}
	return out, nil
}
