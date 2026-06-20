package dns

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/splitdns"
	"golang.org/x/net/dns/dnsmessage"
)

func TestUDPForwarderRoundTrip(t *testing.T) {
	// 起一个本地 UDP 服务器,把收到的查询字节原样加个标记回送。
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 512)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		resp := append([]byte{0xAB}, buf[:n]...) // 标记 + 回显
		_, _ = pc.WriteTo(resp, addr)
	}()

	fwd := NewUDPForwarder(&net.Dialer{Timeout: 2 * time.Second})
	resp, err := fwd.Forward(context.Background(), pc.LocalAddr().String(), []byte("query!"))
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if len(resp) != len("query!")+1 || resp[0] != 0xAB {
		t.Fatalf("bad resp: %v", resp)
	}
}

// fakeForwarder 返回固定的 A 应答(10.0.13.45),并记录是否被调用。
type fakeForwarder struct {
	called bool
	answer netip.Addr
	fail   bool
}

func (f *fakeForwarder) Forward(_ context.Context, _ string, query []byte) ([]byte, error) {
	f.called = true
	if f.fail {
		return nil, fmt.Errorf("内网 DNS 不可达")
	}
	var p dnsmessage.Parser
	h, _ := p.Start(query)
	q, _ := p.Question()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RCode: dnsmessage.RCodeSuccess})
	_ = b.StartQuestions()
	_ = b.Question(q)
	_ = b.StartAnswers()
	_ = b.AResource(
		dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
		dnsmessage.AResource{A: f.answer.As4()},
	)
	out, _ := b.Finish()
	return out, nil
}

func newSplitServer(fwd Forwarder, set *splitdns.Set) *Server {
	pool, _ := fakeip.New("198.18.0.0/15")
	s := NewServer(pool, 1)
	s.SetSplit([]SplitRoute{{
		Match:  route.NewDomainSet([]string{"*.shanghai-electric.com"}),
		Server: "10.0.13.23:53",
	}}, fwd, set)
	return s
}

func TestRespondSplitMatchForwardsAndRegisters(t *testing.T) {
	set := splitdns.NewSet()
	fwd := &fakeForwarder{answer: netip.MustParseAddr("10.0.13.45")}
	s := newSplitServer(fwd, set)

	resp, err := s.Respond(buildQuery(t, 1, "app.shanghai-electric.com.", dnsmessage.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if !fwd.called {
		t.Fatal("匹配域名应调用 forwarder")
	}
	if !set.Contains(netip.MustParseAddr("10.0.13.45")) {
		t.Fatal("解析出的真实 IP 应注册进 splitDirect 集")
	}
	if firstA(t, resp) != netip.MustParseAddr("10.0.13.45") {
		t.Fatal("应原样返回内网 DNS 的 A 记录")
	}
}

func TestRespondStaticAMatchReturnsPinnedAddress(t *testing.T) {
	set := splitdns.NewSet()
	fwd := &fakeForwarder{answer: netip.MustParseAddr("10.0.13.45")}
	pool, _ := fakeip.New("198.18.0.0/15")
	s := NewServer(pool, 1)
	pinned := netip.MustParseAddr("203.0.113.10")
	s.SetStaticA(map[string][]netip.Addr{
		"vps.example.com": {pinned},
	}, set)
	s.SetSplit([]SplitRoute{{
		Match:  route.NewDomainSet([]string{"*.example.com"}),
		Server: "10.0.13.23:53",
	}}, fwd, set)

	resp, err := s.Respond(buildQuery(t, 1, "vps.example.com.", dnsmessage.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if fwd.called {
		t.Fatal("静态 A 命中不应再转发 DNS")
	}
	if firstA(t, resp) != pinned {
		t.Fatalf("静态 A 应返回启动时旁路 IP, got %v", firstA(t, resp))
	}
	if !set.Contains(pinned) {
		t.Fatal("静态 A 应注册进 splitDirect 集")
	}
}

func TestRespondSplitMissDoesNotForward(t *testing.T) {
	set := splitdns.NewSet()
	fwd := &fakeForwarder{answer: netip.MustParseAddr("10.0.13.45")}
	s := newSplitServer(fwd, set)

	resp, err := s.Respond(buildQuery(t, 1, "www.google.com.", dnsmessage.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if fwd.called {
		t.Fatal("非匹配域名不应调用 forwarder")
	}
	a := firstA(t, resp)
	if !netip.PrefixFrom(netip.MustParseAddr("198.18.0.0"), 15).Contains(a) {
		t.Fatalf("非匹配应得 fake-IP,实得 %v", a)
	}
}

func TestRespondSplitAAAAIsNoData(t *testing.T) {
	set := splitdns.NewSet()
	fwd := &fakeForwarder{answer: netip.MustParseAddr("10.0.13.45")}
	s := newSplitServer(fwd, set)

	resp, err := s.Respond(buildQuery(t, 1, "app.shanghai-electric.com.", dnsmessage.TypeAAAA))
	if err != nil {
		t.Fatal(err)
	}
	if fwd.called {
		t.Fatal("split AAAA 不应转发(逼 v4)")
	}
	if answerCount(t, resp) != 0 {
		t.Fatal("AAAA 应为 NODATA(无答案)")
	}
}

func TestRespondSplitForwardFailIsServFail(t *testing.T) {
	set := splitdns.NewSet()
	fwd := &fakeForwarder{fail: true}
	s := newSplitServer(fwd, set)

	resp, err := s.Respond(buildQuery(t, 1, "app.shanghai-electric.com.", dnsmessage.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if rcode(t, resp) != dnsmessage.RCodeServerFailure {
		t.Fatal("转发失败应返回 SERVFAIL")
	}
}

// --- 解析辅助 ---

func firstA(t *testing.T, msg []byte) netip.Addr {
	t.Helper()
	var p dnsmessage.Parser
	if _, err := p.Start(msg); err != nil {
		t.Fatal(err)
	}
	_ = p.SkipAllQuestions()
	for {
		h, err := p.AnswerHeader()
		if err != nil {
			t.Fatal("无 A 记录")
		}
		if h.Type == dnsmessage.TypeA {
			a, _ := p.AResource()
			return netip.AddrFrom4(a.A)
		}
		_ = p.SkipAnswer()
	}
}

func answerCount(t *testing.T, msg []byte) int {
	t.Helper()
	var p dnsmessage.Parser
	if _, err := p.Start(msg); err != nil {
		t.Fatal(err)
	}
	_ = p.SkipAllQuestions()
	n := 0
	for {
		if _, err := p.AnswerHeader(); err != nil {
			return n
		}
		_ = p.SkipAnswer()
		n++
	}
}

func rcode(t *testing.T, msg []byte) dnsmessage.RCode {
	t.Helper()
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil {
		t.Fatal(err)
	}
	return h.RCode
}

// multiAForwarder 返回两条 A 记录(10.0.13.45 与 10.0.13.46)。
type multiAForwarder struct{}

func (multiAForwarder) Forward(_ context.Context, _ string, query []byte) ([]byte, error) {
	var p dnsmessage.Parser
	h, _ := p.Start(query)
	q, _ := p.Question()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RCode: dnsmessage.RCodeSuccess})
	_ = b.StartQuestions()
	_ = b.Question(q)
	_ = b.StartAnswers()
	for _, ip := range []string{"10.0.13.45", "10.0.13.46"} {
		_ = b.AResource(
			dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
			dnsmessage.AResource{A: netip.MustParseAddr(ip).As4()},
		)
	}
	out, _ := b.Finish()
	return out, nil
}

func TestRespondSplitRegistersAllARecords(t *testing.T) {
	set := splitdns.NewSet()
	s := newSplitServer(multiAForwarder{}, set)

	if _, err := s.Respond(buildQuery(t, 1, "app.shanghai-electric.com.", dnsmessage.TypeA)); err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"10.0.13.45", "10.0.13.46"} {
		if !set.Contains(netip.MustParseAddr(ip)) {
			t.Fatalf("应注册全部 A 记录,缺 %s", ip)
		}
	}
}
