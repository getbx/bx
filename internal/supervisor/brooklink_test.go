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
