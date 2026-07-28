package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/getbx/bx/internal/tunnel"
)

func TestTransportSwapperPreparePassesTransactionIDToBuilder(t *testing.T) {
	buildErr := errors.New("stop before process start")
	var gotLink, gotRecoveryID string
	swapper := &transportSwapper{
		build: func(link, recoveryID string) (*tunnel.Tunnel, error) {
			gotLink, gotRecoveryID = link, recoveryID
			return nil, buildErr
		},
	}

	_, err := swapper.prepare(context.Background(), "vless://candidate", "recovery-7-main")
	if !errors.Is(err, buildErr) {
		t.Fatalf("prepare error = %v, want %v", err, buildErr)
	}
	if gotLink != "vless://candidate" || gotRecoveryID != "recovery-7-main" {
		t.Fatalf("builder got link=%q recoveryID=%q", gotLink, gotRecoveryID)
	}
}

func TestTransportSwapperFailedSwapPreservesOldSlot(t *testing.T) {
	old := tunnel.New("127.0.0.1:10001", nil, nil)
	live := &liveTunnel{}
	live.set(old)
	buildErr := errors.New("candidate failed")
	swapper := &transportSwapper{
		lt:  live,
		ctx: context.Background(),
		build: func(string, string) (*tunnel.Tunnel, error) {
			return nil, buildErr
		},
	}
	swapper.setLink("vless://old")

	if err := swapper.swapTo("vless://new"); !errors.Is(err, buildErr) {
		t.Fatalf("swapTo error = %v, want %v", err, buildErr)
	}
	if live.get() != old || swapper.currentLink() != "vless://old" {
		t.Fatalf("failed swap changed old slot: tunnel=%p link=%q", live.get(), swapper.currentLink())
	}
}

func TestTransportConfigPathSeparatesActiveAndRecoveryCandidates(t *testing.T) {
	dir := t.TempDir()
	active := transportConfigPath(dir, "reality", "")
	mainCandidate := transportConfigPath(dir, "reality", "recovery-7-main")
	udpCandidate := transportConfigPath(dir, "reality", "recovery-7-udp")
	if active == mainCandidate || active == udpCandidate || mainCandidate == udpCandidate {
		t.Fatalf("config paths collide: active=%q main=%q UDP=%q", active, mainCandidate, udpCandidate)
	}
	if filepath.Base(mainCandidate) != "sing-box.recovery-7-main.json" {
		t.Fatalf("main candidate path = %q", mainCandidate)
	}
	if err := os.WriteFile(active, []byte("active-generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainCandidate, []byte("candidate-generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "active-generation" {
		t.Fatalf("candidate preparation overwrote active config: %q", got)
	}
}
