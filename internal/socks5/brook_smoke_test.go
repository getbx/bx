package socks5

import (
	"context"
	"net"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestBrookUDPAssociateSmoke(t *testing.T) {
	brook := os.Getenv("BX_BROOK_BIN")
	if brook == "" {
		t.Skip("set BX_BROOK_BIN to run brook UDP smoke test")
	}
	serverAddr := freeTCPAddr(t)
	socksAddr := freeTCPAddr(t)
	password := "bx-smoke"

	server := exec.Command(brook, "server", "-l", serverAddr, "-p", password)
	if err := server.Start(); err != nil {
		t.Fatalf("start brook server: %v", err)
	}
	t.Cleanup(func() { _ = server.Process.Kill(); _ = server.Wait() })

	link := "brook://server?server=" + url.QueryEscape(serverAddr) + "&password=" + url.QueryEscape(password)
	client := exec.Command(brook, "connect", "-l", link, "--socks5", socksAddr)
	if err := client.Start(); err != nil {
		t.Fatalf("start brook connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Process.Kill(); _ = client.Wait() })
	waitTCP(t, socksAddr)

	echo := newUDPEcho(t)
	d, err := NewDialer(socksAddr, &net.Dialer{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := d.DialContext(context.Background(), "udp", echo.LocalAddr().String())
	if err != nil {
		t.Fatalf("udp associate through brook: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("udp write through brook: %v", err)
	}
	buf := make([]byte, 16)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("udp read through brook: %v", err)
	}
	if string(buf[:n]) != "pong" {
		t.Fatalf("udp read %q, want pong", buf[:n])
	}
}

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func waitTCP(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for tcp %s", addr)
}

func newUDPEcho(t *testing.T) net.PacketConn {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			_, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo([]byte("pong"), addr)
		}
	}()
	return pc
}
