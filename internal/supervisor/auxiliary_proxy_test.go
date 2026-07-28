package supervisor

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func TestAuxiliaryProxyKeepsFixedListenerAndSwitchesPrivateTarget(t *testing.T) {
	firstAddr, stopFirst := startTaggedTCPServer(t, "first")
	defer stopFirst()
	secondAddr, stopSecond := startTaggedTCPServer(t, "second")
	defer stopSecond()

	fixed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixedAddr := fixed.Addr().String()
	if err := fixed.Close(); err != nil {
		t.Fatal(err)
	}

	proxy, err := startAuxiliaryProxy(fixedAddr, firstAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	if collision, err := net.Listen("tcp", fixedAddr); err == nil {
		collision.Close()
		t.Fatal("configured auxiliary listener was not held by Core")
	}
	if got := readTaggedTCPServer(t, fixedAddr); got != "first" {
		t.Fatalf("initial auxiliary target = %q, want first", got)
	}

	proxy.SetTarget(secondAddr)
	if got := readTaggedTCPServer(t, fixedAddr); got != "second" {
		t.Fatalf("swapped auxiliary target = %q, want second", got)
	}
}

func TestPrivateAuxiliaryAddressNeverBindsConfiguredListener(t *testing.T) {
	fixed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer fixed.Close()

	privateAddr, err := privateAuxiliaryAddr(fixed.Addr().String(), true)
	if err != nil {
		t.Fatal(err)
	}
	if privateAddr == "" || privateAddr == fixed.Addr().String() {
		t.Fatalf("private auxiliary address = %q, configured = %q", privateAddr, fixed.Addr())
	}
	private, err := net.Listen("tcp", privateAddr)
	if err != nil {
		t.Fatalf("private candidate listener conflicts with active listener: %v", err)
	}
	private.Close()

	if got, err := privateAuxiliaryAddr(fixed.Addr().String(), false); err != nil || got != "" {
		t.Fatalf("UDP candidate auxiliary address = %q, %v; want empty", got, err)
	}
}

func TestPrivateAuxiliaryAddressExplicitlyRejectsUnboundConfiguredPort(t *testing.T) {
	if privateAuxiliaryAddrAllowed("127.0.0.1:17890", "127.0.0.1:17890") {
		t.Fatal("private endpoint accepted the custom fixed listener port before Core bound it")
	}
	if !privateAuxiliaryAddrAllowed("127.0.0.1:17890", "127.0.0.1:17891") {
		t.Fatal("private endpoint rejected a distinct loopback port")
	}
}

func startTaggedTCPServer(t *testing.T, tag string) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.WriteString(conn, tag)
			}()
		}
	}()
	return listener.Addr().String(), func() {
		_ = listener.Close()
		<-done
	}
}

func readTaggedTCPServer(t *testing.T, addr string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(fmt.Errorf("read auxiliary proxy: %w", err))
	}
	return string(buf[:n])
}
