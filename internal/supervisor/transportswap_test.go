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
	var gotAuxiliaryHTTP bool
	swapper := &transportSwapper{
		build: func(link, recoveryID string, auxiliaryHTTP bool) (*tunnel.Tunnel, error) {
			gotLink, gotRecoveryID, gotAuxiliaryHTTP = link, recoveryID, auxiliaryHTTP
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
	if !gotAuxiliaryHTTP {
		t.Fatal("main candidate did not request a private auxiliary endpoint")
	}

	swapper.udp = true
	_, _ = swapper.prepare(context.Background(), "hysteria2://candidate", "recovery-7-udp")
	if gotAuxiliaryHTTP {
		t.Fatal("UDP candidate requested an auxiliary HTTP endpoint")
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
		build: func(string, string, bool) (*tunnel.Tunnel, error) {
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

func TestAuxiliaryPortHandoffCollisionFailsHealthGateWithoutCommit(t *testing.T) {
	old, _ := newHealthySwapTunnel("127.0.0.1:10001")
	old.Start()
	defer old.Stop()
	if err := waitTunnelHealthy(context.Background(), old, 2*time.Second); err != nil {
		t.Fatalf("active protected transport health = %v", err)
	}
	candidate := tunnel.New(
		"127.0.0.1:10002",
		func(string) (tunnel.Runner, error) {
			return nil, errors.New("bind private HTTP endpoint: address in use")
		},
		func(string) (int64, error) { return 0, errors.New("candidate unavailable") },
	)
	live := &liveTunnel{}
	live.set(old)
	swapper := &transportSwapper{
		lt:            live,
		ctx:           context.Background(),
		healthTimeout: 5 * time.Millisecond,
		build: func(string, string, bool) (*tunnel.Tunnel, error) {
			return candidate, nil
		},
	}
	swapper.setLink("vless://active")

	if err := swapper.swapTo("vless://candidate"); err == nil {
		t.Fatal("candidate with a handed-off port collision passed the health gate")
	}
	if live.get() != old || swapper.currentLink() != "vless://active" {
		t.Fatalf("failed candidate committed: tunnel=%p link=%q", live.get(), swapper.currentLink())
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
		build: func(link, recoveryID string, auxiliaryHTTP bool) (*tunnel.Tunnel, error) {
			if link != "vless://new" || recoveryID == "" {
				t.Fatalf("build link=%q recoveryID=%q", link, recoveryID)
			}
			if !auxiliaryHTTP {
				t.Fatal("main failover candidate did not request auxiliary HTTP")
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

func TestTransportSwapperAbortRemovesCandidateConfigAfterProcessStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sing-box.recovery-1-main.json")
	if err := os.WriteFile(path, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &healthySwapRunner{done: make(chan struct{})}
	candidate := tunnel.New(
		"127.0.0.1:10003",
		func(string) (tunnel.Runner, error) { return runner, nil },
		func(string) (int64, error) { return 0, errors.New("candidate unhealthy") },
	)
	candidate.CleanupFileOnStop(path)
	swapper := &transportSwapper{
		ctx:           context.Background(),
		healthTimeout: 5 * time.Millisecond,
		build: func(string, string, bool) (*tunnel.Tunnel, error) {
			return candidate, nil
		},
	}

	_, err := swapper.prepare(context.Background(), "vless://candidate", "recovery-1-main")
	if err == nil {
		t.Fatal("prepare unexpectedly succeeded before its first health poll")
	}
	select {
	case <-runner.done:
	default:
		t.Fatal("candidate config cleanup ran before process stop")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("aborted candidate config still exists: %v", err)
	}
}

func TestTransportSwapperFailoverKeepsActiveConfigUntilTeardown(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "sing-box.active-main.json")
	newPath := filepath.Join(dir, "sing-box.swap-1.json")
	for path, contents := range map[string]string{oldPath: "old", newPath: "new"} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	old, _ := newHealthySwapTunnel("127.0.0.1:10004")
	old.CleanupFileOnStop(oldPath)
	old.Start()
	if err := waitTunnelHealthy(context.Background(), old, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	candidate, _ := newHealthySwapTunnel("127.0.0.1:10005")
	candidate.CleanupFileOnStop(newPath)
	live := &liveTunnel{}
	live.set(old)
	swapper := &transportSwapper{
		lt:            live,
		d:             &dialer.Dialer{},
		ctx:           context.Background(),
		healthTimeout: 2 * time.Second,
		build: func(string, string, bool) (*tunnel.Tunnel, error) {
			return candidate, nil
		},
	}

	if err := swapper.swapTo("vless://new"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("retired config still exists: %v", err)
	}
	if got, err := os.ReadFile(newPath); err != nil || string(got) != "new" {
		t.Fatalf("active config = %q, %v; want retained", got, err)
	}

	candidate.Stop()
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatalf("teardown config still exists: %v", err)
	}
}

func TestTransportSetRecoveryCleansRetiredConfigsAndTeardownCleansActive(t *testing.T) {
	dir := t.TempDir()
	paths := map[string]string{
		"old-main": filepath.Join(dir, "sing-box.active-main.json"),
		"old-udp":  filepath.Join(dir, "sing-box.active-udp.json"),
		"new-main": filepath.Join(dir, "sing-box.recovery-1-main.json"),
		"new-udp":  filepath.Join(dir, "sing-box.recovery-1-udp.json"),
	}
	for name, path := range paths {
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	oldMain, _ := newHealthySwapTunnel("127.0.0.1:10006")
	oldUDP, _ := newHealthySwapTunnel("127.0.0.1:10007")
	newMain, _ := newHealthySwapTunnel("127.0.0.1:10008")
	newUDP, _ := newHealthySwapTunnel("127.0.0.1:10009")
	for tun, path := range map[*tunnel.Tunnel]string{
		oldMain: paths["old-main"],
		oldUDP:  paths["old-udp"],
		newMain: paths["new-main"],
		newUDP:  paths["new-udp"],
	} {
		tun.CleanupFileOnStop(path)
	}
	oldMain.Start()
	oldUDP.Start()
	for _, tun := range []*tunnel.Tunnel{oldMain, oldUDP} {
		if err := waitTunnelHealthy(context.Background(), tun, 2*time.Second); err != nil {
			t.Fatal(err)
		}
	}

	generation := newTransportGeneration()
	d := &dialer.Dialer{}
	mainLive := &liveTunnel{}
	mainLive.set(oldMain)
	udpLive := &liveTunnel{}
	udpLive.set(oldUDP)
	main := &transportSwapper{
		generation:    generation,
		lt:            mainLive,
		d:             d,
		ctx:           context.Background(),
		healthTimeout: 2 * time.Second,
		build: func(string, string, bool) (*tunnel.Tunnel, error) {
			return newMain, nil
		},
	}
	main.setLink("vless://main")
	udp := &transportSwapper{
		generation:    generation,
		lt:            udpLive,
		d:             d,
		ctx:           context.Background(),
		healthTimeout: 2 * time.Second,
		udp:           true,
		build: func(string, string, bool) (*tunnel.Tunnel, error) {
			return newUDP, nil
		},
	}
	udp.setLink("hysteria2://udp")
	udpSlot := udp.transportSetSlot(udpTransportSlot)
	set := newLiveTransportSet(
		generation,
		main.transportSetSlot(mainTransportSlot),
		&udpSlot,
		d.SetTransportGeneration,
	)
	t.Cleanup(set.Stop)

	if err := set.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"old-main", "old-udp"} {
		if _, err := os.Stat(paths[name]); !os.IsNotExist(err) {
			t.Errorf("%s config still exists: %v", name, err)
		}
	}
	for _, name := range []string{"new-main", "new-udp"} {
		if got, err := os.ReadFile(paths[name]); err != nil || string(got) != name {
			t.Errorf("%s active config = %q, %v", name, got, err)
		}
	}

	set.Stop()
	for _, name := range []string{"new-main", "new-udp"} {
		if _, err := os.Stat(paths[name]); !os.IsNotExist(err) {
			t.Errorf("%s teardown config still exists: %v", name, err)
		}
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
