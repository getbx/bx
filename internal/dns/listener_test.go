package dns

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/getbx/bx/internal/fakeip"
	"golang.org/x/net/dns/dnsmessage"
)

func TestListenUDPServesFakeDNS(t *testing.T) {
	pool, err := fakeip.New("198.18.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	pc, err := ListenUDP("127.0.0.1:0", NewServer(pool, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	conn, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))

	if _, err := conn.Write(buildListenerQuery(t, "example.com.")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	got := firstListenerA(t, buf[:n])
	if !netip.PrefixFrom(netip.MustParseAddr("198.18.0.0"), 15).Contains(got) {
		t.Fatalf("got A=%v, want fake 198.18/16", got)
	}
}

func buildListenerQuery(t *testing.T, domain string) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	name, err := dnsmessage.NewName(domain)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Question(dnsmessage.Question{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}); err != nil {
		t.Fatal(err)
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func firstListenerA(t *testing.T, msg []byte) netip.Addr {
	t.Helper()
	var p dnsmessage.Parser
	if _, err := p.Start(msg); err != nil {
		t.Fatal(err)
	}
	if err := p.SkipAllQuestions(); err != nil {
		t.Fatal(err)
	}
	for {
		h, err := p.AnswerHeader()
		if err != nil {
			t.Fatal(err)
		}
		if h.Type == dnsmessage.TypeA {
			a, err := p.AResource()
			if err != nil {
				t.Fatal(err)
			}
			return netip.AddrFrom4(a.A)
		}
		if err := p.SkipAnswer(); err != nil {
			t.Fatal(err)
		}
	}
}
