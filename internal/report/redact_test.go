package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// **脱敏是构造上的,不是事后 grep。** 夹具里放真形状的服务器 IP、v6 地址、bx:// 与 vless://
// 链接、带 token 的查询串、域名;序列化之后一个都找不到,而私网、TUN、fake-IP 与 bx 自己的
// 端点保留(它们说明的是拓扑,不是身份)。**反向断言**:先证明夹具本身确实含这些,否则守卫
// 在空集合上恒真。
func TestRedactedBundleCarriesNoAddressOrCredential(t *testing.T) {
	b := Bundle{
		Schema: 1, InstallID: "0123456789abcdef0123456789abcdef", BXVersion: "v0.4.16", OS: "darwin",
		OccurredAt: "2026-09-29T01:02:03Z", Signature: "attention:core_health_failed",
		Failure: Failure{Code: "core_health_failed", Stage: "transport_health", ErrorCode: "transport_unavailable"},
		Doctor: []Check{
			{Name: "server_link", Status: "ok", Detail: "vless://11111111-2222-3333-4444-555555555555@203.0.113.92:443?sni=www.cloudflare.com"},
			{Name: "tunnel", Status: "fail", Detail: "dial 203.0.113.92:443: timeout; v6 2001:db8::1 too; bx://AAAA.BBBB link; https://example.com/x?token=abc"},
			{Name: "dns", Status: "ok", Detail: "resolver 192.168.50.2 via utun7 198.51.100.1, fake 198.18.0.16, probe https://ipv4.icanhazip.com and localhost"},
		},
		LogTail: []string{
			"2026/09/29 guardian_probe host=vps.example.net ip=203.0.113.92 ok=false",
			"2026/09/29 guardian_bypass cidr=10.0.0.0/8 cidr=203.0.113.0/24",
		},
	}
	raw := marshalPlain(t, b)
	for _, secret := range []string{"203.0.113.92", "2001:db8::1", "vless://", "bx://", "token=abc", "example.com", "vps.example.net", "203.0.113.0/24"} {
		if !strings.Contains(string(raw), secret) {
			t.Fatalf("fixture must contain %q before redaction, or this guard proves nothing", secret)
		}
	}
	out := Redact(b)
	s := string(marshalPlain(t, out))
	for _, secret := range []string{"203.0.113.92", "2001:db8::1", "vless://", "bx://", "token=abc", "example.com", "vps.example.net", "203.0.113.0/24"} {
		if strings.Contains(s, secret) {
			t.Fatalf("redacted bundle still carries %q:\n%s", secret, s)
		}
	}
	for _, keep := range []string{"192.168.50.2", "utun7", "198.51.100.1", "198.18.0.16", "ipv4.icanhazip.com", "localhost", "10.0.0.0/8", "<ip-1>", "<link>", "<host>"} {
		if !strings.Contains(s, keep) {
			t.Fatalf("redaction must keep %q (topology, not identity) or use the placeholder:\n%s", keep, s)
		}
	}
	// 同一个地址同一个占位号:tunnel 那条与日志那条里的 203.0.113.92 都是 <ip-1>,
	// 2001:db8::1 是 <ip-2>;读报告的人才对得上「这两处是同一台」。
	if strings.Count(s, "<ip-1>") < 2 || !strings.Contains(s, "<ip-2>") {
		t.Fatalf("same address must map to the same placeholder across fields:\n%s", s)
	}
	if b.Doctor[1].Detail == out.Doctor[1].Detail {
		t.Fatal("Redact must not mutate its input's meaning-bearing fields without returning a copy")
	}
}

// 一行里既有链接又有地址:链接整段先替换(里面的地址不再单独露出来)。
func TestRedactLineReplacesLinksBeforeAddresses(t *testing.T) {
	r := newRedactor()
	got := r.line("link bx://abc?x=1 then 203.0.113.5 then vless://u@203.0.113.6:443/?sni=a.b")
	if strings.Contains(got, "203.0.113.6") || strings.Contains(got, "vless://") || strings.Contains(got, "bx://abc") {
		t.Fatalf("link contents leaked: %q", got)
	}
	if !strings.Contains(got, "<link>") || !strings.Contains(got, "<ip-1>") {
		t.Fatalf("placeholders missing: %q", got)
	}
}

// json.Marshal 会把 < > 转成 \u003c \u003e,守卫要按字面找占位符,所以关掉 HTML 转义。
func marshalPlain(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
