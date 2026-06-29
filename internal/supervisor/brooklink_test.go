package supervisor

import "testing"

func TestServerHostFromLink(t *testing.T) {
	link := "brook://server?server=203.0.113.10%3A9999&username&password=pw"
	host, err := serverHostFromLink(link)
	if err != nil {
		t.Fatalf("serverHostFromLink: %v", err)
	}
	if host != "203.0.113.10" {
		t.Fatalf("host = %q, want 203.0.113.10", host)
	}
}

func TestServerHostFromLink_WSSServer(t *testing.T) {
	link := "brook://wssserver?wssserver=wss%3A%2F%2Fvps.example.com%3A443&username&password=pw"
	host, err := serverHostFromLink(link)
	if err != nil {
		t.Fatalf("serverHostFromLink: %v", err)
	}
	if host != "vps.example.com" {
		t.Fatalf("host = %q, want vps.example.com", host)
	}
}

func TestServerHostFromLink_BareHost(t *testing.T) {
	host, err := serverHostFromLink("example.com")
	if err != nil {
		t.Fatalf("serverHostFromLink: %v", err)
	}
	if host != "example.com" {
		t.Fatalf("host = %q, want example.com", host)
	}
}

func TestHostToCIDRs_IP(t *testing.T) {
	got := hostToCIDRs("203.0.113.10")
	if len(got) != 1 || got[0] != "203.0.113.10/32" {
		t.Fatalf("hostToCIDRs(IP) = %v, want [203.0.113.10/32]", got)
	}
}

func TestServerHostFromLink_PlainHostPort(t *testing.T) {
	// 也支持直接 host:port(配置可不用 brook:// link)
	host, err := serverHostFromLink("1.2.3.4:8080")
	if err != nil {
		t.Fatalf("serverHostFromLink: %v", err)
	}
	if host != "1.2.3.4" {
		t.Fatalf("host = %q, want 1.2.3.4", host)
	}
}

func TestServerHostFromLinkVless(t *testing.T) {
	h, err := serverHostFromLink("vless://uid@203.0.113.10:443?security=reality&pbk=p&sid=s&sni=www.microsoft.com")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if h != "203.0.113.10" {
		t.Fatalf("host=%q want 203.0.113.10", h)
	}
}

func TestServerHostFromLinkSS(t *testing.T) {
	// ss:// authority 是 base64,须经 tunnel.SSHost 解出 host(SIP002:base64@host:port)。
	h, err := serverHostFromLink("ss://YWVzLTI1Ni1nY206cHcxMjM@203.0.113.10:8388#hk")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if h != "203.0.113.10" {
		t.Fatalf("ss host=%q want 203.0.113.10", h)
	}
}

func TestServerHostFromLinkVmess(t *testing.T) {
	// vmess:// authority 是 base64-JSON,须经 tunnel.VmessHost 解出 add。
	link := "vmess://eyJhZGQiOiIyMy4yNy4xMzQuNzciLCJwb3J0IjoiNDQzIiwiaWQiOiJ1dWlkLXgiLCJuZXQiOiJ0Y3AifQ"
	h, err := serverHostFromLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if h != "203.0.113.10" {
		t.Fatalf("vmess host=%q want 203.0.113.10", h)
	}
}

func TestServerHostFromLinkBrookStillWorks(t *testing.T) {
	h, err := serverHostFromLink("brook://server?server=203.0.113.10%3A9999&password=x")
	if err != nil || h != "203.0.113.10" {
		t.Fatalf("brook host=%q err=%v", h, err)
	}
}
