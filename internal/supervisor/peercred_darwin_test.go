//go:build darwin

package supervisor

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerCredUIDDarwin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan *net.UnixConn, 1)
	errs := make(chan error, 1)
	go func() {
		conn, err := ln.AcceptUnix()
		if err != nil {
			errs <- err
			return
		}
		accepted <- conn
	}()
	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var server *net.UnixConn
	select {
	case server = <-accepted:
	case err := <-errs:
		t.Fatal(err)
	}
	defer server.Close()

	uid, ok := peerCredUID(server)
	if !ok {
		t.Fatal("peer credentials unavailable")
	}
	if want := uint32(os.Geteuid()); uid != want {
		t.Fatalf("uid=%d want %d", uid, want)
	}
}
