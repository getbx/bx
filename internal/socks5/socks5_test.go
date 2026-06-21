package socks5

import (
	"bytes"
	"context"
	"io"
	"net"
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

type fakeServer struct {
	addr      string
	tcp       net.Listener
	udp       net.PacketConn
	datagrams chan fakeDatagram
}

type fakeDatagram struct {
	target  string
	payload []byte
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{addr: tcp.Addr().String(), tcp: tcp, udp: udp, datagrams: make(chan fakeDatagram, 4)}
	t.Cleanup(func() {
		tcp.Close()
		udp.Close()
	})
	go s.serveTCP(t)
	go s.serveUDP(t)
	return s
}

func (s *fakeServer) serveTCP(t *testing.T) {
	t.Helper()
	c, err := s.tcp.Accept()
	if err != nil {
		return
	}
	defer c.Close()
	buf := make([]byte, 512)
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		t.Errorf("read greeting: %v", err)
		return
	}
	if buf[0] != 5 {
		t.Errorf("bad version %d", buf[0])
		return
	}
	if _, err := io.ReadFull(c, buf[:int(buf[1])]); err != nil {
		t.Errorf("read methods: %v", err)
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		t.Errorf("write method: %v", err)
		return
	}

	if _, err := io.ReadFull(c, buf[:4]); err != nil {
		t.Errorf("read request: %v", err)
		return
	}
	if !bytes.Equal(buf[:4], []byte{5, 3, 0, 1}) {
		t.Errorf("request prefix = %v, want UDP associate IPv4", buf[:4])
		return
	}
	if _, err := io.ReadFull(c, buf[:6]); err != nil {
		t.Errorf("read request addr: %v", err)
		return
	}
	_, port, _ := net.SplitHostPort(s.udp.LocalAddr().String())
	p, _ := net.LookupPort("udp", port)
	reply := []byte{5, 0, 0, 1, 127, 0, 0, 1, byte(p >> 8), byte(p)}
	if _, err := c.Write(reply); err != nil {
		t.Errorf("write reply: %v", err)
		return
	}
	_, _ = c.Read(buf[:1])
}

func (s *fakeServer) serveUDP(t *testing.T) {
	t.Helper()
	buf := make([]byte, 2048)
	for {
		n, from, err := s.udp.ReadFrom(buf)
		if err != nil {
			return
		}
		target, payload, err := parseUDPDatagram(buf[:n])
		if err != nil {
			t.Errorf("parse udp datagram: %v", err)
			return
		}
		s.datagrams <- fakeDatagram{target: target, payload: append([]byte(nil), payload...)}
		resp, err := buildUDPDatagram(target, []byte("pong"))
		if err != nil {
			t.Errorf("build udp datagram: %v", err)
			return
		}
		_, _ = s.udp.WriteTo(resp, from)
	}
}
