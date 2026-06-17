package dialer

import (
	"crypto/tls"
	"net"
	"testing"
	"time"
)

func TestSniffHTTPHost(t *testing.T) {
	got := sniffDomain([]byte("GET / HTTP/1.1\r\nHost: Example.COM:443\r\n\r\n"))
	if got != "example.com" {
		t.Fatalf("host = %q", got)
	}
}

func TestSniffHTTPHostIgnoresMalformedRequestLine(t *testing.T) {
	if got := sniffDomain([]byte("   \r\nHost: example.com\r\n\r\n")); got != "" {
		t.Fatalf("malformed host = %q", got)
	}
}

func TestSniffTLSClientHelloSNI(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	errc := make(chan error, 1)
	go func() {
		c := tls.Client(client, &tls.Config{ServerName: "GitHub.com", InsecureSkipVerify: true})
		errc <- c.Handshake()
	}()

	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 4096)
	n, err := server.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := sniffDomain(buf[:n]); got != "github.com" {
		t.Fatalf("sni = %q", got)
	}
	_ = server.Close()
	<-errc
}
