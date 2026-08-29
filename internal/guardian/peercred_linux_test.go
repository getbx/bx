//go:build linux

package guardian

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// SO_PEERCRED 在真实 unix socket 上取得到自己的 uid——这是 Linux Guardian
// 授权门(authorizeOwnerPeer)能工作的前提,CI 的 ubuntu 腿每次都验。
func TestLocalPeerCredentialsReportsOwnUIDOnLinux(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	go func() {
		conn, err := net.Dial("unix", path)
		if err != nil {
			return
		}
		defer conn.Close()
		<-hold // 挂住到测试结束,别让对端过早关闭
	}()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	uid, got := localPeerCredentials(server)
	if !got {
		t.Fatal("SO_PEERCRED 没取到对端凭据")
	}
	if uid != uint32(os.Getuid()) {
		t.Fatalf("uid=%d want %d", uid, os.Getuid())
	}
}

// 非 unix socket 一律 (0, false)——authorizeOwnerPeer 对 got=false 是拒绝,
// 这半边不许因为换了连接类型就悄悄放行 uid=0。
func TestLocalPeerCredentialsRejectsNonUnixConnOnLinux(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	if _, got := localPeerCredentials(server); got {
		t.Fatal("net.Pipe 不是 unix socket,不许报告取到了凭据")
	}
}
