package stats

import (
	"strings"
	"testing"
)

func TestCounters_ActiveAndDecisions(t *testing.T) {
	var c Counters
	c.ConnOpen()
	c.ConnOpen()
	c.ConnClose() // 活跃 = 1
	c.Proxy()
	c.Proxy()
	c.Proxy()
	c.Direct() // 代理 3 直连 1
	c.Blocked()
	c.UDPBlocked()
	c.AddUp(1000)
	c.AddDown(4000)

	s := c.Snapshot()
	if s.Active != 1 {
		t.Errorf("Active = %d, want 1", s.Active)
	}
	if s.Proxy != 3 || s.Direct != 1 || s.Blocked != 1 {
		t.Errorf("Proxy/Direct/Blocked = %d/%d/%d, want 3/1/1", s.Proxy, s.Direct, s.Blocked)
	}
	if s.UDPBlocked != 1 {
		t.Errorf("UDPBlocked = %d, want 1", s.UDPBlocked)
	}
	if s.BytesUp != 1000 || s.BytesDown != 4000 {
		t.Errorf("BytesUp/Down = %d/%d, want 1000/4000", s.BytesUp, s.BytesDown)
	}
	if r := s.ProxyRatio(); r < 0.74 || r > 0.76 {
		t.Errorf("ProxyRatio = %.3f, want ~0.75", r)
	}
}

func TestSnapshot_ProxyRatio_NoConns(t *testing.T) {
	var c Counters
	if r := c.Snapshot().ProxyRatio(); r != 0 {
		t.Errorf("无连接时 ProxyRatio = %.3f, want 0", r)
	}
}

func TestRenderShowsUDPPolicy(t *testing.T) {
	rep := Report{
		Snapshot: Snapshot{
			Active:     2,
			Proxy:      3,
			Direct:     1,
			Blocked:    9,
			UDPBlocked: 4,
		},
		Server:        "example.com",
		SocksAddr:     "127.0.0.1:60000",
		TunnelHealthy: true,
		LatencyMS:     42,
		UDPMode:       "block",
		UDPNote:       "non-DNS UDP blocked; WebRTC/Google Meet may stutter",
	}
	out := Render(rep)
	for _, want := range []string{
		"UDP",
		"mode block",
		"阻断 4",
		"Google Meet",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderDefaultsUDPPolicyToProxy(t *testing.T) {
	out := Render(Report{TunnelHealthy: true})
	if !strings.Contains(out, "mode proxy") {
		t.Fatalf("status output should default UDP mode to proxy:\n%s", out)
	}
}
