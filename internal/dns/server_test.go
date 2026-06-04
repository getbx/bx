package dns

import (
	"net/netip"
	"testing"

	"github.com/getbx/bx/internal/fakeip"
	"golang.org/x/net/dns/dnsmessage"
)

func buildQuery(t *testing.T, id uint16, name string, typ dnsmessage.Type) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(name),
		Type:  typ,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatal(err)
	}
	buf, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return buf
}

func TestServer_AQuery_ReturnsFakeIP(t *testing.T) {
	pool, _ := fakeip.New("198.18.0.0/15")
	s := NewServer(pool, 1)

	resp, err := s.Respond(buildQuery(t, 0x1234, "google.com.", dnsmessage.TypeA))
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}

	var p dnsmessage.Parser
	h, err := p.Start(resp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if h.ID != 0x1234 {
		t.Errorf("ID = %#x, want 0x1234", h.ID)
	}
	if !h.Response {
		t.Error("应是响应(QR=1)")
	}
	if err := p.SkipAllQuestions(); err != nil {
		t.Fatal(err)
	}
	ah, err := p.AnswerHeader()
	if err != nil {
		t.Fatalf("无 answer: %v", err)
	}
	if ah.Type != dnsmessage.TypeA {
		t.Fatalf("answer type = %v, want A", ah.Type)
	}
	ar, err := p.AResource()
	if err != nil {
		t.Fatal(err)
	}
	ip := netip.AddrFrom4(ar.A)
	dom, ok := pool.Domain(ip)
	if !ok {
		t.Fatalf("fake IP %v 未在池中登记", ip)
	}
	if dom != "google.com" {
		t.Errorf("反查域名 = %q, want google.com(应去掉尾点)", dom)
	}
}

func TestServer_AAAAQuery_ReturnsNoData(t *testing.T) {
	pool, _ := fakeip.New("198.18.0.0/15")
	s := NewServer(pool, 1)

	resp, err := s.Respond(buildQuery(t, 7, "google.com.", dnsmessage.TypeAAAA))
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}

	var p dnsmessage.Parser
	h, err := p.Start(resp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !h.Response {
		t.Error("应是响应")
	}
	if h.RCode != dnsmessage.RCodeSuccess {
		t.Errorf("RCode = %v, want Success(NODATA)", h.RCode)
	}
	if err := p.SkipAllQuestions(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AnswerHeader(); err != dnsmessage.ErrSectionDone {
		t.Errorf("AAAA 应无 answer(NODATA),却有 answer 或错误: %v", err)
	}
}
