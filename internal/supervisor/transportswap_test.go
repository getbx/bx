package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/dialer"
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

func TestTransportSwapperSuccessfulSwapActivatesCandidateAndRetiresOld(t *testing.T) {
	old, oldRunner := newHealthySwapTunnel("127.0.0.1:10001")
	old.Start()
	if err := waitTunnelHealthy(context.Background(), old, 2*time.Second); err != nil {
		t.Fatalf("old tunnel health = %v", err)
	}
	candidate, _ := newHealthySwapTunnel("127.0.0.1:10002")
	live := &liveTunnel{}
	live.set(old)
	generation := newTransportGeneration()
	swapper := &transportSwapper{
		generation:    generation,
		lt:            live,
		d:             &dialer.Dialer{},
		ctx:           context.Background(),
		healthTimeout: 2 * time.Second,
		build: func(link, recoveryID string) (*tunnel.Tunnel, error) {
			if link != "vless://new" || recoveryID == "" {
				t.Fatalf("build link=%q recoveryID=%q", link, recoveryID)
			}
			return candidate, nil
		},
	}
	swapper.setLink("vless://old")

	if err := swapper.swapTo("vless://new"); err != nil {
		t.Fatalf("swapTo = %v", err)
	}
	if live.get() != candidate || swapper.currentLink() != "vless://new" {
		t.Fatalf("successful swap live=%p link=%q", live.get(), swapper.currentLink())
	}
	select {
	case <-oldRunner.done:
	default:
		t.Fatal("old transport was not retired after successful activation")
	}
	if !candidate.Healthy() {
		t.Fatal("committed candidate is not healthy")
	}
	candidate.Stop()
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

type healthySwapRunner struct {
	done chan struct{}
	once sync.Once
}

func newHealthySwapTunnel(addr string) (*tunnel.Tunnel, *healthySwapRunner) {
	runner := &healthySwapRunner{done: make(chan struct{})}
	return tunnel.New(
		addr,
		func(string) (tunnel.Runner, error) { return runner, nil },
		func(string) (int64, error) { return 1, nil },
	), runner
}

func (r *healthySwapRunner) Wait() error {
	<-r.done
	return nil
}

func (r *healthySwapRunner) Kill() error {
	r.once.Do(func() { close(r.done) })
	return nil
}
