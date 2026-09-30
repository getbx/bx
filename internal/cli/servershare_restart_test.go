package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/srvgen"
)

// share / revoke 改的是同一份服务端配置、重启的是同一个 bx-server,所以要同样的保护
// (known-gaps A14):重启前查、重启后确认、没回来就放回原配置。**记录要跟着配置走**:
// 服务器没回来时,share 不许留下一条用不了的用户记录,revoke 不许删掉一条其实还有效的。

func shareFixture(t *testing.T, host *fakeHost) (dir string, cfg serverConfig, before []byte) {
	t.Helper()
	fresh, err := srvgen.RealityParams{
		UUID: "11111111-2222-3333-4444-555555555555", Port: 8444, SNI: "www.cloudflare.com",
		PrivateKey: strings.Repeat("A", 43), ShortID: "abcd",
	}.ServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sbserver.json")
	if err := os.WriteFile(path, fresh, 0o600); err != nil {
		t.Fatal(err)
	}
	oldPath, oldHost := serverSingboxPath, serverRestartHost
	serverSingboxPath, serverRestartHost = path, host
	t.Cleanup(func() { serverSingboxPath, serverRestartHost = oldPath, oldHost })
	return t.TempDir(), serverConfig{Type: "reality", SNI: "www.cloudflare.com", Port: 8444, Link: deployTestLink}, fresh
}

func TestShareAddsTheUserOnlyOnceTheServerIsBack(t *testing.T) {
	host := &fakeHost{holders: map[string][]portHolder{"tcp/8444": bxOwn}}
	dir, cfg, before := shareFixture(t, host)
	rec, err := realityShare("friend", dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(serverSingboxPath)
	if string(after) == string(before) || host.restarts != 1 {
		t.Fatalf("user not added or not restarted (restarts=%d)", host.restarts)
	}
	if _, err := os.Stat(shareConfigPath(dir, "friend")); err != nil || rec.Link == "" {
		t.Fatalf("share record missing: %v", err)
	}
}

func TestShareLeavesNoRecordWhenTheServerDidNotComeBack(t *testing.T) {
	stolen := []portHolder{{PID: 7, Process: "sing-box", Unit: "sing-box.service"}}
	host := &fakeHost{holders: map[string][]portHolder{"tcp/8444": bxOwn}, afterRestart: map[string][]portHolder{"tcp/8444": stolen}}
	dir, cfg, before := shareFixture(t, host)
	if _, err := realityShare("friend", dir, cfg); err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("err = %v, want the previous config restored", err)
	}
	if after, _ := os.ReadFile(serverSingboxPath); string(after) != string(before) {
		t.Fatal("the config was not restored")
	}
	if _, err := os.Stat(shareConfigPath(dir, "friend")); !os.IsNotExist(err) {
		t.Fatalf("a share record for a user the server never got: %v", err)
	}
}

func TestShareRefusesWhileAnotherSingboxIsRetrying(t *testing.T) {
	host := &fakeHost{holders: map[string][]portHolder{"tcp/8444": bxOwn}, others: []string{"sing-box.service"}}
	dir, cfg, before := shareFixture(t, host)
	if _, err := realityShare("friend", dir, cfg); err == nil || !strings.Contains(err.Error(), "sing-box.service") {
		t.Fatalf("err = %v", err)
	}
	if after, _ := os.ReadFile(serverSingboxPath); string(after) != string(before) || host.restarts != 0 {
		t.Fatal("wrote or restarted although it refused")
	}
}

func TestRevokeKeepsTheRecordWhenTheServerDidNotComeBack(t *testing.T) {
	host := &fakeHost{holders: map[string][]portHolder{"tcp/8444": bxOwn}}
	dir, cfg, _ := shareFixture(t, host)
	if _, err := realityShare("friend", dir, cfg); err != nil {
		t.Fatal(err)
	}
	withFriend, _ := os.ReadFile(serverSingboxPath)
	stolen := []portHolder{{PID: 7, Process: "sing-box", Unit: "sing-box.service"}}
	*host = fakeHost{holders: map[string][]portHolder{"tcp/8444": bxOwn}, afterRestart: map[string][]portHolder{"tcp/8444": stolen}}
	if err := revokeShare("friend", dir); err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("err = %v", err)
	}
	if after, _ := os.ReadFile(serverSingboxPath); string(after) != string(withFriend) {
		t.Fatal("the config was not restored")
	}
	if _, err := os.Stat(shareConfigPath(dir, "friend")); err != nil {
		t.Fatalf("the record of a user that still works was deleted: %v", err)
	}
}

func TestRevokeRemovesTheUserAndTheRecord(t *testing.T) {
	host := &fakeHost{holders: map[string][]portHolder{"tcp/8444": bxOwn}}
	dir, cfg, before := shareFixture(t, host)
	if _, err := realityShare("friend", dir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := revokeShare("friend", dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(serverSingboxPath)
	if !strings.Contains(string(after), "11111111-2222-3333-4444-555555555555") || strings.Count(string(after), `"uuid"`) != strings.Count(string(before), `"uuid"`) {
		t.Fatal("revoke did not remove exactly the shared user")
	}
	if _, err := os.Stat(shareConfigPath(dir, "friend")); !os.IsNotExist(err) {
		t.Fatalf("record left behind: %v", err)
	}
}
