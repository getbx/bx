package socks5

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestDialerUDPAssociateRelaysDatagrams(t *testing.T) {
	server := newFakeServer(t)
	d, err := NewDialer(server.addr, &net.Dialer{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := d.DialContext(context.Background(), "udp", "1.2.3.4:3478")
	if err != nil {
		t.Fatalf("udp dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("udp write: %v", err)
	}

	got := <-server.datagrams
	if got.target != "1.2.3.4:3478" || string(got.payload) != "ping" {
		t.Fatalf("server got target=%q payload=%q", got.target, got.payload)
	}

	buf := make([]byte, 16)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("udp read: %v", err)
	}
	if string(buf[:n]) != "pong" {
		t.Fatalf("read %q, want pong", buf[:n])
	}
}

func TestDialerUDPAssociateSupportsDomainTargets(t *testing.T) {
	server := newFakeServer(t)
	d, err := NewDialer(server.addr, &net.Dialer{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := d.DialContext(context.Background(), "udp", "stun.l.google.com:19302")
	if err != nil {
		t.Fatalf("udp dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("q")); err != nil {
		t.Fatalf("udp write: %v", err)
	}

	got := <-server.datagrams
	if got.target != "stun.l.google.com:19302" || string(got.payload) != "q" {
		t.Fatalf("server got target=%q payload=%q", got.target, got.payload)
	}
}

// TestDialerUDPAssociateLocalAddrMatchesIPv4Relay pins the fix's mechanism:
// against an IPv4 relay, the client must bind an IPv4 socket, not a
// dual-stack wildcard. A dual-stack "udp" wildcard socket also reports a
// non-IPv4 LocalAddr ("[::]:port"), so this test would fail the same way
// against that regression.
func TestDialerUDPAssociateLocalAddrMatchesIPv4Relay(t *testing.T) {
	server := newFakeServer(t)
	d, err := NewDialer(server.addr, &net.Dialer{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := d.DialContext(context.Background(), "udp", "1.2.3.4:3478")
	if err != nil {
		t.Fatalf("udp dial: %v", err)
	}
	defer conn.Close()

	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("LocalAddr type = %T, want *net.UDPAddr", conn.LocalAddr())
	}
	if local.IP.To4() == nil {
		t.Fatalf("client bound to an IPv4 relay but LocalAddr = %v (not IPv4) — "+
			"a dual-stack wildcard socket reintroduces a cross-family delivery "+
			"path that this machine demonstrably drops some datagrams on", local)
	}
}

// TestDialerUDPAssociateLocalAddrMatchesIPv6Relay is the counterweight to the
// IPv4 test above: it stops the fix from being "simplified" into a
// hardcoded udp4 bind, which would make an IPv6-only relay unreachable.
func TestDialerUDPAssociateLocalAddrMatchesIPv6Relay(t *testing.T) {
	server := newFakeServerV6(t)
	d, err := NewDialer(server.addr, &net.Dialer{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := d.DialContext(context.Background(), "udp", "1.2.3.4:3478")
	if err != nil {
		t.Fatalf("udp dial: %v", err)
	}
	defer conn.Close()

	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("LocalAddr type = %T, want *net.UDPAddr", conn.LocalAddr())
	}
	if local.IP.To4() != nil {
		t.Fatalf("client bound to an IPv6 relay but LocalAddr = %v (looks IPv4) — "+
			"a hardcoded udp4 socket cannot reach an IPv6-only relay, a cross-family "+
			"delivery path that drops every datagram rather than some of them", local)
	}
}

type fakeServer struct {
	addr      string
	tcp       net.Listener
	udp       net.PacketConn
	datagrams chan fakeDatagram
	errs      chan error
	wg        sync.WaitGroup
}

type fakeDatagram struct {
	target  string
	payload []byte
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	return newFakeServerOn(t, "127.0.0.1")
}

// newFakeServerV6 is the IPv6 counterpart of newFakeServer, used to exercise
// the relay-family-selection path against an IPv6-only relay. It skips the
// test if this machine has no working IPv6 loopback.
func newFakeServerV6(t *testing.T) *fakeServer {
	t.Helper()
	probe, err := net.ListenPacket("udp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback not available: %v", err)
	}
	probe.Close()
	return newFakeServerOn(t, "::1")
}

func newFakeServerOn(t *testing.T, host string) *fakeServer {
	t.Helper()
	tcp, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{
		addr:      tcp.Addr().String(),
		tcp:       tcp,
		udp:       udp,
		datagrams: make(chan fakeDatagram, 4),
		// Sized generously above the handful of diagnostics either
		// goroutine can possibly queue in one test run (see reportf):
		// large enough that the sends in reportf never block, so
		// s.wg.Wait() below can never deadlock on a full channel.
		errs: make(chan error, 16),
	}
	s.wg.Add(2)
	t.Cleanup(func() {
		tcp.Close()
		udp.Close()
		// serveTCP/serveUDP can still be mid-iteration here (e.g. a
		// test that never reads the relayed reply returns while
		// serveUDP is still building/sending it). Wait for both to
		// actually exit before touching s.errs, so every reportf call
		// they make is guaranteed to have already happened -- and only
		// then replay them through t.Errorf, on this goroutine, before
		// the test is marked complete. Calling t.Errorf directly from
		// those goroutines would panic ("Log in goroutine after
		// TestXxx has completed") once they outlive the test, which is
		// exactly what closing tcp/udp above makes them do.
		s.wg.Wait()
		close(s.errs)
		for err := range s.errs {
			t.Errorf("fake socks5 server: %v", err)
		}
	})
	go s.serveTCP()
	go s.serveUDP()
	return s
}

// reportf queues a diagnostic from serveTCP or serveUDP. It must never call
// any *testing.T method itself (including t.Helper(), which is exactly as
// unsafe here as t.Errorf: both are calls into a *testing.T that this
// goroutine can make after the test function has already returned). The
// t.Cleanup callback in newFakeServerOn is what turns these back into
// t.Errorf, on the test goroutine, after waiting for both server goroutines
// to finish.
func (s *fakeServer) reportf(format string, args ...any) {
	s.errs <- fmt.Errorf(format, args...)
}

func (s *fakeServer) serveTCP() {
	defer s.wg.Done()
	c, err := s.tcp.Accept()
	if err != nil {
		return
	}
	defer c.Close()
	buf := make([]byte, 512)
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		s.reportf("read greeting: %v", err)
		return
	}
	if buf[0] != 5 {
		s.reportf("bad version %d", buf[0])
		return
	}
	if _, err := io.ReadFull(c, buf[:int(buf[1])]); err != nil {
		s.reportf("read methods: %v", err)
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		s.reportf("write method: %v", err)
		return
	}

	if _, err := io.ReadFull(c, buf[:4]); err != nil {
		s.reportf("read request: %v", err)
		return
	}
	if !bytes.Equal(buf[:4], []byte{5, 3, 0, 1}) {
		s.reportf("request prefix = %v, want UDP associate IPv4", buf[:4])
		return
	}
	if _, err := io.ReadFull(c, buf[:6]); err != nil {
		s.reportf("read request addr: %v", err)
		return
	}
	reply, err := udpAssociateReply(s.udp.LocalAddr())
	if err != nil {
		s.reportf("build udp associate reply: %v", err)
		return
	}
	if _, err := c.Write(reply); err != nil {
		s.reportf("write reply: %v", err)
		return
	}
	_, _ = c.Read(buf[:1])
}

// udpAssociateReply builds a SOCKS5 UDP-ASSOCIATE reply carrying the relay's
// actual address family (IPv4 or IPv6 ATYP), so the same fake server code
// serves both newFakeServer (127.0.0.1) and newFakeServerV6 (::1).
func udpAssociateReply(addr net.Addr) ([]byte, error) {
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		return nil, fmt.Errorf("unexpected relay addr type %T", addr)
	}
	reply := []byte{5, 0, 0}
	if v4 := udpAddr.IP.To4(); v4 != nil {
		reply = append(reply, 1)
		reply = append(reply, v4...)
	} else {
		reply = append(reply, 4)
		reply = append(reply, udpAddr.IP.To16()...)
	}
	return append(reply, byte(udpAddr.Port>>8), byte(udpAddr.Port)), nil
}

func (s *fakeServer) serveUDP() {
	defer s.wg.Done()
	buf := make([]byte, 2048)
	for {
		n, from, err := s.udp.ReadFrom(buf)
		if err != nil {
			return
		}
		target, payload, err := parseUDPDatagram(buf[:n])
		if err != nil {
			s.reportf("parse udp datagram: %v", err)
			return
		}
		s.datagrams <- fakeDatagram{target: target, payload: append([]byte(nil), payload...)}
		resp, err := buildUDPDatagram(target, []byte("pong"))
		if err != nil {
			s.reportf("build udp datagram: %v", err)
			return
		}
		if _, err := s.udp.WriteTo(resp, from); err != nil {
			s.reportf("write udp reply: %v", err)
		}
	}
}
