package guardian

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/supervisor"
)

func TestManagerUpdateTransactions(t *testing.T) {
	tests := []struct {
		name            string
		failAt          string
		wantPhase       Phase
		wantBarrier     bool
		wantVersion     string
		wantOldRunning  bool
		wantNoBarrier   bool
		wantErr         bool
		wantEventPrefix []string
	}{
		{
			name:        "success",
			wantPhase:   PhaseCommitted,
			wantVersion: "v2",
			wantEventPrefix: []string{
				"prepare", "health.v1", "install.bind_barrier", "journal.prepared",
				"journal.prepared.barrier_intent", "barrier.install", "journal.barrier_active",
				"core.stop.v1", "barrier.reassert", "journal.activating", "install.activate",
				"core.start.v2", "health.v2", "journal.committed", "barrier.release",
				"receipt.committed", "install.commit", "journal.clear",
			},
		},
		{
			name:        "new unhealthy old healthy",
			failAt:      "new-health",
			wantPhase:   PhaseRolledBack,
			wantVersion: "v1",
			wantEventPrefix: []string{
				"prepare", "health.v1", "install.bind_barrier", "journal.prepared",
				"journal.prepared.barrier_intent", "barrier.install", "journal.barrier_active",
				"core.stop.v1", "barrier.reassert", "journal.activating", "install.activate",
				"core.start.v2", "health.v2", "core.stop.v2", "journal.rolling_back",
				"install.restore", "barrier.reassert", "core.start.v1", "health.v1", "journal.rolled_back",
				"barrier.release", "receipt.rolled_back", "install.commit", "journal.clear",
			},
		},
		{
			name:        "new and old unhealthy",
			failAt:      "old-health",
			wantPhase:   PhaseNeedsAttention,
			wantBarrier: true,
			wantErr:     true,
			wantEventPrefix: []string{
				"prepare", "health.v1", "install.bind_barrier", "journal.prepared",
				"journal.prepared.barrier_intent", "barrier.install", "journal.barrier_active",
				"core.stop.v1", "barrier.reassert", "journal.activating", "install.activate",
				"core.start.v2", "health.v2", "core.stop.v2", "journal.rolling_back",
				"install.restore", "barrier.reassert", "core.start.v1", "health.v1", "core.stop.v1",
				"journal.needs_attention",
			},
		},
		{
			name:           "prepare failure",
			failAt:         "prepare",
			wantPhase:      PhaseCommitted,
			wantVersion:    "v1",
			wantOldRunning: true,
			wantNoBarrier:  true,
			wantErr:        true,
			wantEventPrefix: []string{
				"prepare",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newUpdateTestEnv(t)
			env.fail(tt.failAt)

			result, err := env.manager.Update(context.Background(), env.request)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Update error = %v, wantErr %v", err, tt.wantErr)
			}
			if got := env.events.snapshot(); !reflect.DeepEqual(got, tt.wantEventPrefix) {
				t.Fatalf("events = %#v, want %#v", got, tt.wantEventPrefix)
			}
			status := env.manager.Status()
			if status.Phase != tt.wantPhase {
				t.Fatalf("status phase = %q, want %q; result=%+v err=%v", status.Phase, tt.wantPhase, result, err)
			}
			if env.manager.barrierProven() != tt.wantBarrier {
				t.Fatalf("barrier proven = %v, want %v", env.manager.barrierProven(), tt.wantBarrier)
			}
			if tt.wantVersion != "" && status.CoreVersion != tt.wantVersion {
				t.Fatalf("CoreVersion = %q, want %q", status.CoreVersion, tt.wantVersion)
			}
			if tt.wantOldRunning && env.manager.current.PID != env.old.PID {
				t.Fatalf("old Core changed: current=%+v old=%+v", env.manager.current, env.old)
			}
			if tt.wantNoBarrier && containsEvent(env.events.snapshot(), "barrier.install") {
				t.Fatal("barrier was installed after prepare failure")
			}
		})
	}
}

func TestManagerUpdateFailureEventsRemainFailClosed(t *testing.T) {
	tests := []struct {
		failAt             string
		wantBarrier        bool
		wantPhase          Phase
		wantVersion        string
		wantBarrierRemoved bool
		wantErr            bool
	}{
		{failAt: "prepared", wantPhase: PhaseCommitted, wantVersion: "v1", wantErr: true},
		{failAt: "barrier-install", wantPhase: PhaseNeedsAttention, wantVersion: "v1", wantErr: true},
		{failAt: "old-stop", wantBarrier: true, wantPhase: PhaseNeedsAttention, wantVersion: "v1", wantErr: true},
		{failAt: "bypass-reassert", wantBarrier: true, wantPhase: PhaseNeedsAttention, wantVersion: "v1", wantErr: true},
		{failAt: "activate", wantPhase: PhaseRolledBack, wantVersion: "v1", wantBarrierRemoved: true},
		{failAt: "new-start", wantPhase: PhaseRolledBack, wantVersion: "v1", wantBarrierRemoved: true},
		{failAt: "new-health", wantPhase: PhaseRolledBack, wantVersion: "v1", wantBarrierRemoved: true},
		{failAt: "receipt", wantPhase: PhaseCommitted, wantVersion: "v2", wantBarrierRemoved: true, wantErr: true},
		{failAt: "barrier-cleanup", wantPhase: PhaseNeedsAttention, wantVersion: "v2", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.failAt, func(t *testing.T) {
			env := newUpdateTestEnv(t)
			env.fail(tt.failAt)

			result, err := env.manager.Update(context.Background(), env.request)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Update error = %v, wantErr %v", err, tt.wantErr)
			}
			status := env.manager.Status()
			if status.Phase != tt.wantPhase || status.CoreVersion != tt.wantVersion {
				t.Fatalf("status = %+v, want phase=%q version=%q; result=%+v err=%v", status, tt.wantPhase, tt.wantVersion, result, err)
			}
			if env.manager.barrierProven() != tt.wantBarrier {
				t.Fatalf("barrier proven = %v, want %v", env.manager.barrierProven(), tt.wantBarrier)
			}
			if tt.wantBarrierRemoved && !containsEvent(env.events.snapshot(), "barrier.release") {
				t.Fatalf("healthy Core did not reach barrier cleanup: %#v", env.events.snapshot())
			}
			assertSecretFreeUpdateValues(t, result, err, status)
		})
	}
}

func TestManagerUpdatePersistsBarrierIntentBeforeRouteMutation(t *testing.T) {
	env := newUpdateTestEnv(t)
	if _, err := env.manager.Update(context.Background(), env.request); err != nil {
		t.Fatal(err)
	}
	events := env.events.snapshot()
	assertEventBefore(t, events, "journal.prepared.barrier_intent", "barrier.install")
	assertEventBefore(t, events, "barrier.install", "journal.barrier_active")
}

func TestManagerUpdateRejectsUnsafePackageBeforeBarrier(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*updateTestEnv)
	}{
		{name: "invalid transaction ID", mutate: func(env *updateTestEnv) { env.request.TransactionID = "../secret" }},
		{name: "missing source version", mutate: func(env *updateTestEnv) { env.request.FromVersion = "" }},
		{name: "missing target version", mutate: func(env *updateTestEnv) { env.request.ToVersion = "" }},
		{name: "invalid digest", mutate: func(env *updateTestEnv) { env.request.AssetSHA256 = "not-a-digest" }},
		{name: "digest mismatch", mutate: func(env *updateTestEnv) { env.request.AssetSHA256 = strings.Repeat("0", 64) }},
		{name: "outside staging", mutate: func(env *updateTestEnv) {
			path := filepath.Join(env.root, "outside", "secret-package")
			writeUpdatePackage(t, path, []byte("secret package"))
			env.request.PackagePath = path
		}},
		{name: "sibling prefix", mutate: func(env *updateTestEnv) {
			path := filepath.Join(env.paths.Staging, env.request.TransactionID+"-other", "package")
			writeUpdatePackage(t, path, []byte("secret package"))
			env.request.PackagePath = path
		}},
		{name: "wrong transaction", mutate: func(env *updateTestEnv) {
			path := filepath.Join(env.paths.Staging, "tx-other", "package")
			writeUpdatePackage(t, path, []byte("secret package"))
			env.request.PackagePath = path
		}},
		{name: "package symlink", mutate: func(env *updateTestEnv) {
			target := filepath.Join(env.root, "secret-target")
			writeUpdatePackage(t, target, []byte("secret package"))
			_ = os.Remove(env.request.PackagePath)
			if err := os.Symlink(target, env.request.PackagePath); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "staging symlink", mutate: func(env *updateTestEnv) {
			target := filepath.Join(env.root, "secret-dir")
			path := filepath.Join(target, "package")
			writeUpdatePackage(t, path, []byte("secret package"))
			if err := os.RemoveAll(filepath.Join(env.paths.Staging, env.request.TransactionID)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(env.paths.Staging, env.request.TransactionID)); err != nil {
				t.Fatal(err)
			}
			env.request.PackagePath = filepath.Join(env.paths.Staging, env.request.TransactionID, "package")
		}},
		{name: "package directory", mutate: func(env *updateTestEnv) {
			if err := os.Remove(env.request.PackagePath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(env.request.PackagePath, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "shared transaction directory", mutate: func(env *updateTestEnv) {
			if err := os.Chmod(filepath.Join(env.paths.Staging, env.request.TransactionID), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "shared package file", mutate: func(env *updateTestEnv) {
			if err := os.Chmod(env.request.PackagePath, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newUpdateTestEnv(t)
			tt.mutate(env)
			_, err := env.manager.Update(context.Background(), env.request)
			if err == nil {
				t.Fatal("unsafe update request accepted")
			}
			if got := env.events.snapshot(); len(got) != 0 {
				t.Fatalf("unsafe request caused mutation: %#v", got)
			}
			if env.manager.current.PID != env.old.PID || env.manager.barrierProven() {
				t.Fatalf("unsafe request changed protection: current=%+v barrier=%v", env.manager.current, env.manager.barrierProven())
			}
			assertSecretFreeUpdateValues(t, UpdateResult{}, err, env.manager.Status())
		})
	}
}

func TestManagerUpdateReservesDeadlineForTargetCleanup(t *testing.T) {
	env := newUpdateTestEnv(t)
	env.manager.cleanupTimeout = 100 * time.Millisecond
	env.health.blockVersions = map[string]bool{"v2": true}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	result, err := env.manager.Update(ctx, env.request)
	if err != nil {
		t.Fatalf("Update returned error after healthy rollback: %v; result=%+v", err, result)
	}
	if env.runner.stopSawCanceled["v2"] {
		t.Fatal("target cleanup inherited an expired health deadline")
	}
	if !result.RolledBack || result.Phase != PhaseRolledBack {
		t.Fatalf("result = %+v, want completed rollback", result)
	}
}

func TestManagerUpdateDoesNotRollbackAcrossUncertainTargetOwnership(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*updateTestEnv)
	}{
		{name: "target cleanup unproven", mutate: func(env *updateTestEnv) {
			env.health.failVersions = map[string]error{"v2": errors.New("target unhealthy")}
			env.runner.failStopVersion = "v2"
		}},
		{name: "target start ownership uncertain", mutate: func(env *updateTestEnv) {
			env.runner.uncertainStartVersion = "v2"
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newUpdateTestEnv(t)
			tt.mutate(env)

			result, err := env.manager.Update(context.Background(), env.request)
			if err == nil {
				t.Fatalf("uncertain target ownership returned success: %+v", result)
			}
			if containsEvent(env.events.snapshot(), "install.restore") || containsEvent(env.events.snapshot(), "core.start.v1") {
				t.Fatalf("uncertain target was crossed by rollback: %#v", env.events.snapshot())
			}
			if !env.manager.current.Uncertain || !env.manager.barrierProven() {
				t.Fatalf("uncertain target was not retained behind barrier: current=%+v barrier=%v", env.manager.current, env.manager.barrierProven())
			}
			if got := env.manager.Status(); got.Phase != PhaseNeedsAttention || got.Protection != ProtectionBlocked {
				t.Fatalf("status = %+v, want blocked needs_attention", got)
			}
		})
	}
}

func TestManagerUpdateRejectsNewerGuardianProtocolBeforeBarrier(t *testing.T) {
	env := newUpdateTestEnv(t)
	env.updater.requiredProtocol = currentGuardianProtocol + 1

	_, err := env.manager.Update(context.Background(), env.request)
	if err == nil {
		t.Fatal("newer Guardian protocol was accepted")
	}
	if got, want := env.events.snapshot(), []string{"prepare", "install.commit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want pre-barrier cleanup %#v", got, want)
	}
	if env.manager.barrierProven() || env.manager.current.PID != env.old.PID {
		t.Fatal("protocol rejection disturbed protected Core")
	}
}

func TestManagerUpdateDerivesBarrierFromLiveRuntime(t *testing.T) {
	env := newUpdateTestEnv(t)
	env.manager.barrierContext = BarrierContext{Gateway: "192.0.2.9", ServerBypass: []string{"203.0.113.99/32"}, BlockIPv6: true}
	env.manager.runtime.ServerBypass = []string{"198.51.100.44/32"}
	live := updateRuntime(env.old.PID, "v1")
	live.ServerBypass = []string{"198.51.100.77/32"}
	env.health.runtimeByVersion = map[string]supervisor.RuntimeState{"v1": live}
	gateways := &fakeGatewayProvider{gateway: "192.0.2.55"}
	env.manager.gatewayProvider = gateways

	if _, err := env.manager.Update(context.Background(), env.request); err != nil {
		t.Fatal(err)
	}
	got := env.barrier.lastInstallContext()
	want := BarrierContext{Gateway: "192.0.2.55", ServerBypass: []string{"198.51.100.77/32"}, BlockIPv6: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("barrier context = %+v, want live runtime %+v", got, want)
	}
	if !reflect.DeepEqual(env.prepared.boundBarrierContext, want) {
		t.Fatalf("recovery metadata context = %+v, want %+v", env.prepared.boundBarrierContext, want)
	}
	if gateways.callCount() != 1 {
		t.Fatalf("gateway discoveries = %d, want 1", gateways.callCount())
	}
	events := env.events.snapshot()
	assertEventBefore(t, events, "prepare", "health.v1")
	assertEventBefore(t, events, "health.v1", "install.bind_barrier")
	assertEventBefore(t, events, "install.bind_barrier", "journal.prepared")
}

func TestManagerReleasesOldBarrierAgainstTargetRuntimeBypassSet(t *testing.T) {
	env := newUpdateTestEnv(t)
	oldBypass := []string{"198.51.100.10/32"}
	targetBypass := []string{"203.0.113.20/32"}
	env.manager.barrierContext.ServerBypass = append([]string(nil), oldBypass...)
	env.manager.runtime.ServerBypass = append([]string(nil), oldBypass...)
	oldRuntime := updateRuntime(env.old.PID, "v1")
	oldRuntime.ServerBypass = append([]string(nil), oldBypass...)
	targetRuntime := updateRuntime(101, "v2")
	targetRuntime.ServerBypass = append([]string(nil), targetBypass...)
	env.health.runtimeByVersion = map[string]supervisor.RuntimeState{"v1": oldRuntime, "v2": targetRuntime}

	if _, err := env.manager.Update(context.Background(), env.request); err != nil {
		t.Fatal(err)
	}
	installed, transferred := env.barrier.lastRelease()
	if !reflect.DeepEqual(installed.ServerBypass, oldBypass) {
		t.Fatalf("released Guardian bypasses = %#v, want old set %#v", installed.ServerBypass, oldBypass)
	}
	if !reflect.DeepEqual(transferred, targetBypass) {
		t.Fatalf("transferred bypasses = %#v, want target runtime %#v", transferred, targetBypass)
	}
	startOptions := env.runner.startOptionsSnapshot()
	if len(startOptions) == 0 || !reflect.DeepEqual(startOptions[0].GuardianBypassHandoff, oldBypass) {
		t.Fatalf("target Core start handoff = %#v, want exact Guardian-owned old set %#v", startOptions, oldBypass)
	}
}

func TestManagerUpdateGatewayRefreshFailureCleansPreparationBeforeJournal(t *testing.T) {
	env := newUpdateTestEnv(t)
	env.manager.gatewayProvider = &fakeGatewayProvider{err: errors.New("DHCP gateway unavailable")}

	if _, err := env.manager.Update(context.Background(), env.request); err == nil {
		t.Fatal("update continued without a fresh gateway")
	}
	events := env.events.snapshot()
	if !containsEvent(events, "install.commit") {
		t.Fatalf("prepared update was not cleaned: %#v", events)
	}
	for _, forbidden := range []string{"journal.prepared", "barrier.install"} {
		if containsEvent(events, forbidden) {
			t.Fatalf("gateway failure reached %q: %#v", forbidden, events)
		}
	}
}

func TestManagerSerializesUpdatePreparationAgainstAnotherUpdate(t *testing.T) {
	env := newUpdateTestEnv(t)
	env.updater.block = make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := env.manager.Update(context.Background(), env.request)
		firstDone <- err
	}()
	select {
	case <-env.updater.entered:
	case <-time.After(time.Second):
		t.Fatal("first update did not enter preparation")
	}

	secondDone := make(chan error, 1)
	go func() {
		request := env.writeRequest(t, "tx-2", []byte("second verified package"))
		_, err := env.manager.Update(context.Background(), request)
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		close(env.updater.block)
		<-firstDone
		t.Fatalf("second update overlapped preparation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(env.updater.block)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Update: %v", err)
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second update did not continue after first Update")
	}
}

func TestManagerSlowUpdatePreparationDoesNotDropCoreExit(t *testing.T) {
	env := newUpdateTestEnv(t)
	env.manager.restartTimeout = 75 * time.Millisecond
	env.runner.startSequence = []string{"v1", "v2", "v1"}
	env.updater.block = make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(env.updater.block)
		}
	}()

	updateDone := make(chan error, 1)
	go func() {
		_, err := env.manager.Update(context.Background(), env.request)
		updateDone <- err
	}()
	select {
	case <-env.updater.entered:
	case <-time.After(time.Second):
		t.Fatal("update did not enter slow preparation")
	}

	env.runner.mu.Lock()
	env.runner.current = Process{}
	env.runner.mu.Unlock()
	recoveryDone := make(chan struct{})
	go func() {
		env.manager.handleUnexpectedExit(env.old, errors.New("Core exited during preparation"))
		close(recoveryDone)
	}()
	select {
	case <-recoveryDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Core exit recovery remained blocked by package preparation")
	}
	if env.manager.current.PID == 0 || env.manager.current.PID == env.old.PID {
		t.Fatalf("Core exit was dropped during preparation: current=%+v events=%#v", env.manager.current, env.events.snapshot())
	}
	if got := env.manager.Status(); got.Protection != ProtectionProtected || got.CorePID != env.manager.current.PID {
		t.Fatalf("status remained stale after concurrent Core exit: %+v", got)
	}

	close(env.updater.block)
	released = true
	if err := <-updateDone; err != nil {
		t.Fatalf("update did not revalidate after exit recovery: %v; events=%#v", err, env.events.snapshot())
	}
}

func TestManagerFailedPreparationCleanupDoesNotHoldLifecycleMutation(t *testing.T) {
	env := newUpdateTestEnv(t)
	env.updater.block = make(chan struct{})
	env.prepared.commitEntered = make(chan struct{}, 1)
	env.prepared.commitBlock = make(chan struct{})
	commitReleased := false
	defer func() {
		if !commitReleased {
			close(env.prepared.commitBlock)
		}
	}()

	updateDone := make(chan error, 1)
	go func() {
		_, err := env.manager.Update(context.Background(), env.request)
		updateDone <- err
	}()
	select {
	case <-env.updater.entered:
	case <-time.After(time.Second):
		t.Fatal("update did not enter preparation")
	}

	if err := env.manager.acquireMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	env.manager.setStatus(Status{SchemaVersion: 1, Desired: DesiredOff, Phase: PhaseIdle, Protection: ProtectionOff})
	env.manager.releaseMutation()
	close(env.updater.block)

	select {
	case <-env.prepared.commitEntered:
	case <-time.After(time.Second):
		t.Fatal("invalidated preparation did not begin cleanup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := env.manager.acquireMutation(ctx); err != nil {
		t.Fatalf("preparation cleanup retained lifecycle mutation: %v", err)
	}
	env.manager.releaseMutation()
	close(env.prepared.commitBlock)
	commitReleased = true
	if err := <-updateDone; err == nil {
		t.Fatal("update succeeded after lifecycle state changed during preparation")
	}
}

func TestUpdateErrorsResultsAndJournalContainNoSecrets(t *testing.T) {
	env := newUpdateTestEnv(t)
	env.prepared.activateErr = errors.New("vless://user:password@example.test?token=super-secret")
	env.prepared.restoreErr = errors.New("/Users/secret/Applications/Bx.app")

	result, err := env.manager.Update(context.Background(), env.request)
	if err == nil {
		t.Fatal("secret-bearing fake failure unexpectedly succeeded")
	}
	assertSecretFreeUpdateValues(t, result, err, env.manager.Status())
	transaction, loadErr := env.store.LoadTransaction()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	b, marshalErr := json.Marshal(transaction)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	assertNoUpdateSecrets(t, string(b))
}

func TestSaveUpdatePhasePreservesEmptyLastError(t *testing.T) {
	env := newUpdateTestEnv(t)
	now := time.Now().UTC()
	transaction := Transaction{
		ID: env.request.TransactionID, FromVersion: env.request.FromVersion, ToVersion: env.request.ToVersion,
		Phase: PhasePrepared, AssetDigest: env.request.AssetSHA256,
		SnapshotPath: filepath.Join(env.paths.Snapshots, env.request.TransactionID), StartedAt: now, UpdatedAt: now,
	}
	if err := env.manager.saveUpdatePhase(&transaction, PhaseBarrierActive, ""); err != nil {
		t.Fatal(err)
	}
	stored, err := env.store.LoadTransaction()
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastError != "" {
		t.Fatalf("successful phase last_error = %q, want empty", stored.LastError)
	}
}

func TestManagerRecoversEveryPersistedUpdatePhase(t *testing.T) {
	tests := []struct {
		phase           Phase
		existingVersion string
		wantPhase       Phase
		wantVersion     string
		wantBarrier     bool
		wantActivate    bool
		wantRestore     bool
		wantCommit      bool
	}{
		{
			phase: PhasePrepared, existingVersion: "v1", wantPhase: PhaseCommitted,
			wantVersion: "v1", wantCommit: true,
		},
		{
			phase: PhaseBarrierActive, wantPhase: PhaseRolledBack,
			wantVersion: "v1", wantRestore: true, wantCommit: true,
		},
		{
			phase: PhaseActivating, existingVersion: "v2", wantPhase: PhaseRolledBack,
			wantVersion: "v1", wantRestore: true, wantCommit: true,
		},
		{
			phase: PhaseRollingBack, wantPhase: PhaseRolledBack,
			wantVersion: "v1", wantRestore: true, wantCommit: true,
		},
		{
			phase: PhaseCommitted, existingVersion: "v2", wantPhase: PhaseCommitted,
			wantVersion: "v2", wantCommit: true,
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.phase), func(t *testing.T) {
			env := newUpdateTestEnv(t)
			manager := env.restartedManager(t, tt.phase, tt.existingVersion)

			if err := manager.Recover(context.Background()); err != nil {
				t.Fatalf("Recover(%s): %v; events=%#v", tt.phase, err, env.events.snapshot())
			}
			status := manager.Status()
			if status.Phase != tt.wantPhase || status.CoreVersion != tt.wantVersion {
				t.Fatalf("status = %+v, want phase=%q version=%q", status, tt.wantPhase, tt.wantVersion)
			}
			if manager.barrierProven() != tt.wantBarrier {
				t.Fatalf("barrier proven = %v, want %v", manager.barrierProven(), tt.wantBarrier)
			}
			events := env.events.snapshot()
			if containsEvent(events, "install.activate") != tt.wantActivate {
				t.Fatalf("activate presence = %v, want %v; events=%#v", containsEvent(events, "install.activate"), tt.wantActivate, events)
			}
			if containsEvent(events, "install.restore") != tt.wantRestore {
				t.Fatalf("restore presence = %v, want %v; events=%#v", containsEvent(events, "install.restore"), tt.wantRestore, events)
			}
			if containsEvent(events, "install.commit") != tt.wantCommit {
				t.Fatalf("commit presence = %v, want %v; events=%#v", containsEvent(events, "install.commit"), tt.wantCommit, events)
			}
			if tt.phase != PhasePrepared {
				assertEventBefore(t, events, "barrier.install", firstCoreStartOrHealth(events))
				if !containsEvent(events, "barrier.reassert") {
					t.Fatalf("recovery did not reassert bypass: %#v", events)
				}
			}
		})
	}
}

func TestManagerRecoversCrashAfterBarrierInstallBeforeBarrierActiveJournal(t *testing.T) {
	env := newUpdateTestEnv(t)
	now := time.Now().UTC()
	transaction := Transaction{
		ID: env.request.TransactionID, FromVersion: env.request.FromVersion, ToVersion: env.request.ToVersion,
		Phase: PhasePrepared, BarrierInstallIntent: true, AssetDigest: env.request.AssetSHA256,
		SnapshotPath: filepath.Join(env.paths.Snapshots, env.request.TransactionID),
		StartedAt:    now.Add(-time.Minute), UpdatedAt: now,
	}
	if err := env.store.Store.SaveTransaction(transaction); err != nil {
		t.Fatal(err)
	}
	env.events.reset()
	manager := env.restartedManagerWithoutJournal(t, "v1")

	if err := manager.Recover(context.Background()); err != nil {
		t.Fatalf("intent recovery failed: %v; events=%#v", err, env.events.snapshot())
	}
	events := env.events.snapshot()
	assertEventBefore(t, events, "barrier.install", "health.v1")
	for _, forbidden := range []string{"install.activate", "install.restore", "core.start.v2"} {
		if containsEvent(events, forbidden) {
			t.Fatalf("prepared barrier intent repeated %q: %#v", forbidden, events)
		}
	}
	for _, required := range []string{"barrier.reassert", "journal.rolled_back", "barrier.release", "receipt.rolled_back", "install.commit", "journal.clear"} {
		if !containsEvent(events, required) {
			t.Fatalf("intent recovery omitted %q: %#v", required, events)
		}
	}
	if manager.barrierProven() || manager.Status().Protection != ProtectionProtected || manager.Status().CoreVersion != "v1" {
		t.Fatalf("intent recovery did not return previous protected Core: status=%+v barrier=%v", manager.Status(), manager.barrierProven())
	}
}

func TestManagerCommittedRecoveryCleansUpWithoutSecondActivation(t *testing.T) {
	env := newUpdateTestEnv(t)
	manager := env.restartedManager(t, PhaseCommitted, "v2")

	if err := manager.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := env.events.snapshot()
	for _, forbidden := range []string{"install.activate", "install.restore", "core.start.v2"} {
		if containsEvent(events, forbidden) {
			t.Fatalf("committed recovery repeated activation step %q: %#v", forbidden, events)
		}
	}
	if !containsEvent(events, "receipt.committed") || !containsEvent(events, "install.commit") || !containsEvent(events, "journal.clear") {
		t.Fatalf("committed recovery did not finish receipt cleanup: %#v", events)
	}
}

func TestManagerRecoveryUsesMatchingTerminalReceiptAfterDescriptorCleanup(t *testing.T) {
	env := newUpdateTestEnv(t)
	now := time.Now().UTC()
	transaction := Transaction{
		ID: env.request.TransactionID, FromVersion: env.request.FromVersion, ToVersion: env.request.ToVersion,
		Phase: PhaseCommitted, AssetDigest: env.request.AssetSHA256,
		SnapshotPath: filepath.Join(env.paths.Snapshots, env.request.TransactionID),
		StartedAt:    now.Add(-time.Minute), UpdatedAt: now,
	}
	if err := env.store.Store.SaveTransaction(transaction); err != nil {
		t.Fatal(err)
	}
	if err := env.store.Store.SaveReceipt(Receipt{
		TransactionID: transaction.ID, FromVersion: transaction.FromVersion, ToVersion: transaction.ToVersion,
		AssetDigest: transaction.AssetDigest, Outcome: transaction.Phase, CompletedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(env.paths.Staging, transaction.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(env.paths.Staging, transaction.ID, updateRecoveryDescriptorName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery descriptor still exists: %v", err)
	}

	env.events.reset()
	manager := env.restartedManagerWithoutJournal(t, "v2")
	manager.updatePreparer = macOSUpdatePreparer{}
	if err := manager.Recover(context.Background()); err != nil {
		t.Fatalf("receipt-backed recovery failed: %v; events=%#v", err, env.events.snapshot())
	}
	for _, forbidden := range []string{"barrier.install", "install.activate", "install.restore", "core.start.v2"} {
		if containsEvent(env.events.snapshot(), forbidden) {
			t.Fatalf("receipt-backed cleanup repeated %q: %#v", forbidden, env.events.snapshot())
		}
	}
	if transaction, err := env.store.LoadTransaction(); err != nil || transaction != nil {
		t.Fatalf("terminal journal remains after receipt recovery: %#v, %v", transaction, err)
	}
	if got := manager.Status(); got.Protection != ProtectionProtected || got.CoreVersion != "v2" {
		t.Fatalf("status = %+v, want adopted protected target", got)
	}
}

func TestManagerRecoveryUsesMatchingTerminalReceiptToFinishPendingCleanup(t *testing.T) {
	env := newUpdateTestEnv(t)
	now := time.Now().UTC()
	transaction := Transaction{
		ID: env.request.TransactionID, FromVersion: env.request.FromVersion, ToVersion: env.request.ToVersion,
		Phase: PhaseCommitted, AssetDigest: env.request.AssetSHA256,
		SnapshotPath: filepath.Join(env.paths.Snapshots, env.request.TransactionID),
		StartedAt:    now.Add(-time.Minute), UpdatedAt: now,
	}
	if err := env.store.Store.SaveTransaction(transaction); err != nil {
		t.Fatal(err)
	}
	if err := env.store.Store.SaveReceipt(Receipt{
		TransactionID: transaction.ID, FromVersion: transaction.FromVersion, ToVersion: transaction.ToVersion,
		AssetDigest: transaction.AssetDigest, Outcome: transaction.Phase, CompletedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	env.events.reset()
	manager := env.restartedManagerWithoutJournal(t, "v2")

	if err := manager.Recover(context.Background()); err != nil {
		t.Fatalf("pending terminal cleanup failed: %v; events=%#v", err, env.events.snapshot())
	}
	if !containsEvent(env.events.snapshot(), "install.commit") || !containsEvent(env.events.snapshot(), "journal.clear") {
		t.Fatalf("receipt recovery skipped pending cleanup: %#v", env.events.snapshot())
	}
	for _, forbidden := range []string{"barrier.install", "install.activate", "install.restore", "core.start.v2"} {
		if containsEvent(env.events.snapshot(), forbidden) {
			t.Fatalf("pending cleanup repeated %q: %#v", forbidden, env.events.snapshot())
		}
	}
}

func TestManagerCleanupOnlyRecoveryFailureProvesBarrierWithoutCore(t *testing.T) {
	tests := []struct {
		name       string
		phase      Phase
		receipt    bool
		commitFail bool
		clearFail  bool
	}{
		{name: "prepared commit", phase: PhasePrepared, commitFail: true},
		{name: "prepared journal clear", phase: PhasePrepared, clearFail: true},
		{name: "receipt commit", phase: PhaseCommitted, receipt: true, commitFail: true},
		{name: "receipt journal clear", phase: PhaseCommitted, receipt: true, clearFail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newUpdateTestEnv(t)
			manager := env.restartedManager(t, tt.phase, "")
			if tt.receipt {
				transaction, err := env.store.LoadTransaction()
				if err != nil || transaction == nil {
					t.Fatalf("load transaction: %#v, %v", transaction, err)
				}
				if err := env.store.Store.SaveReceipt(Receipt{
					TransactionID: transaction.ID, FromVersion: transaction.FromVersion, ToVersion: transaction.ToVersion,
					AssetDigest: transaction.AssetDigest, Outcome: transaction.Phase,
					CompletedAt: transaction.UpdatedAt.Add(time.Second),
				}); err != nil {
					t.Fatal(err)
				}
			}
			env.prepared.commitErr = nil
			if tt.commitFail {
				env.prepared.commitErr = errors.New("injected cleanup failure")
			}
			env.store.failClear = tt.clearFail
			env.events.reset()
			planningBarrier := &planValidatingBarrier{delegate: env.barrier}
			manager.barrier = planningBarrier

			if err := manager.Recover(context.Background()); err == nil {
				t.Fatal("cleanup-only recovery failure returned success")
			}
			if manager.current.PID != 0 {
				t.Fatalf("cleanup failure unexpectedly accepted Core: %+v", manager.current)
			}
			if got := manager.Status(); got.Protection != ProtectionBlocked || got.Protection == ProtectionProtected {
				t.Fatalf("cleanup failure status = %+v, want proven blocked and never protected", got)
			}
			if !containsEvent(env.events.snapshot(), "barrier.install") {
				t.Fatalf("cleanup failure returned without barrier install: %#v", env.events.snapshot())
			}
			var rejects int
			for _, command := range planningBarrier.installedCommands {
				if strings.Contains(command.String(), "-reject") {
					rejects++
				}
			}
			if want := len(publicIPv4Blocks) + len(publicIPv6Blocks); rejects != want {
				t.Fatalf("cleanup failure installed %d reject routes, want exact dual-stack %d: %#v", rejects, want, planningBarrier.installedCommands)
			}

			env.prepared.commitErr = nil
			env.store.failClear = false
			if err := manager.Recover(context.Background()); err != nil {
				t.Fatalf("receipt cleanup retry failed: %v; events=%#v", err, env.events.snapshot())
			}
			if manager.current.PID == 0 || manager.Status().Protection != ProtectionProtected || manager.barrierProven() {
				t.Fatalf("cleanup retry did not return healthy protected Core: status=%+v current=%+v barrier=%v", manager.Status(), manager.current, manager.barrierProven())
			}
		})
	}
}

func TestManagerReceiptJournalClearFailureAfterDurableStagingRemovalUsesBlockOnlyBarrier(t *testing.T) {
	env := newUpdateTestEnv(t)
	manager := env.restartedManager(t, PhaseCommitted, "")
	transaction, err := env.store.LoadTransaction()
	if err != nil || transaction == nil {
		t.Fatalf("load transaction: %#v, %v", transaction, err)
	}
	if err := env.store.Store.SaveReceipt(Receipt{
		TransactionID: transaction.ID, FromVersion: transaction.FromVersion, ToVersion: transaction.ToVersion,
		AssetDigest: transaction.AssetDigest, Outcome: transaction.Phase,
		CompletedAt: transaction.UpdatedAt.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(env.paths.Staging, transaction.ID)); err != nil {
		t.Fatal(err)
	}
	env.updater.recoverErr = errors.New("descriptor durably removed")
	env.updater.recoveryContext = BarrierContext{}
	env.store.failClear = true
	env.events.reset()
	planningBarrier := &planValidatingBarrier{delegate: env.barrier}
	manager.barrier = planningBarrier

	if err := manager.Recover(context.Background()); err == nil {
		t.Fatal("journal clear failure after staging removal returned success")
	}
	if manager.current.PID != 0 || !manager.barrierProven() || manager.Status().Protection != ProtectionBlocked {
		t.Fatalf("descriptor-gone clear failure was not blocked: status=%+v current=%+v barrier=%v", manager.Status(), manager.current, manager.barrierProven())
	}
	if len(planningBarrier.installedCommands) != len(publicIPv4Blocks)+len(publicIPv6Blocks) {
		t.Fatalf("block-only commands = %d, want exact dual-stack %d: %#v", len(planningBarrier.installedCommands), len(publicIPv4Blocks)+len(publicIPv6Blocks), planningBarrier.installedCommands)
	}

	env.store.failClear = false
	if err := manager.Recover(context.Background()); err != nil {
		t.Fatalf("descriptor-gone journal clear retry failed: %v", err)
	}
	if manager.current.PID == 0 || manager.Status().Protection != ProtectionProtected || manager.barrierProven() {
		t.Fatalf("descriptor-gone retry did not restore protected Core: status=%+v current=%+v barrier=%v", manager.Status(), manager.current, manager.barrierProven())
	}
}

func TestManagerFailedBarrierAttemptIsRetriedBeforeOwnershipUncertainBlockClaim(t *testing.T) {
	env := newUpdateTestEnv(t)
	env.barrier.installErr = errors.New("first route command failed")

	if _, err := env.manager.Update(context.Background(), env.request); err == nil {
		t.Fatal("update accepted failed barrier attempt")
	}
	if got := env.manager.Status(); got.Protection == ProtectionBlocked {
		t.Fatalf("failed barrier attempt claimed blocked protection: %+v", got)
	}

	env.barrier.installErr = nil
	env.events.reset()
	env.manager.handleUnexpectedExit(env.old, ErrProcessOwnershipUncertain)
	if got := env.events.snapshot(); !containsEvent(got, "barrier.install") {
		t.Fatalf("ownership-uncertain exit did not retry attempted barrier: %#v", got)
	}
	if got := env.manager.Status(); got.Protection != ProtectionBlocked || got.LastError != "core_ownership_uncertain" {
		t.Fatalf("successful barrier retry status = %+v, want proven blocked ownership uncertainty", got)
	}
}

func TestManagerMalformedUpdateJournalKeepsBarrierAndStartsNoCore(t *testing.T) {
	env := newUpdateTestEnv(t)
	if err := os.MkdirAll(filepath.Dir(env.paths.Transaction), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.paths.Transaction, []byte(`{"transaction_id":"tx-1","phase":`), 0o600); err != nil {
		t.Fatal(err)
	}
	env.events.reset()
	manager := env.restartedManagerWithoutJournal(t, "v1")
	env.updater.recoverErr = errors.New("missing recovery descriptor")
	planningBarrier := &planValidatingBarrier{delegate: env.barrier}
	manager.barrier = planningBarrier

	err := manager.Recover(context.Background())
	if err == nil {
		t.Fatal("malformed journal recovery succeeded")
	}
	if got, want := env.events.snapshot(), []string{"recovery.context", "barrier.install"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if !manager.barrierProven() {
		t.Fatal("malformed journal did not retain fail-closed barrier")
	}
	if len(planningBarrier.installedCommands) != len(publicIPv4Blocks)+len(publicIPv6Blocks) {
		t.Fatalf("malformed journal installed %d blocking routes, want %d", len(planningBarrier.installedCommands), len(publicIPv4Blocks)+len(publicIPv6Blocks))
	}
	status := manager.Status()
	if status.Phase != PhaseNeedsAttention || status.Protection != ProtectionBlocked {
		t.Fatalf("status = %+v, want blocked needs_attention", status)
	}
	assertNoUpdateSecrets(t, err.Error())
}

func TestBlockOnlyRecoveryAlwaysPlansExactIPv4AndIPv6Routes(t *testing.T) {
	apply, reassert, cleanup, err := PlanBarrier(blockOnlyRecoveryContext(BarrierContext{}))
	if err != nil {
		t.Fatal(err)
	}
	requireCommands(t, apply,
		"route -n add -net 0.0.0.0/2 127.0.0.1 -reject",
		"route -n add -net 64.0.0.0/2 127.0.0.1 -reject",
		"route -n add -net 128.0.0.0/2 127.0.0.1 -reject",
		"route -n add -net 192.0.0.0/2 127.0.0.1 -reject",
		"route -n add -inet6 -net ::/2 ::1 -reject",
		"route -n add -inet6 -net 4000::/2 ::1 -reject",
		"route -n add -inet6 -net 8000::/2 ::1 -reject",
		"route -n add -inet6 -net c000::/2 ::1 -reject",
	)
	if len(reassert) != 0 {
		t.Fatalf("block-only recovery planned bypass commands: %v", reassert)
	}
	requireCommands(t, cleanup,
		"route -n delete -inet6 -net c000::/2",
		"route -n delete -inet6 -net 8000::/2",
		"route -n delete -inet6 -net 4000::/2",
		"route -n delete -inet6 -net ::/2",
		"route -n delete -net 192.0.0.0/2",
		"route -n delete -net 128.0.0.0/2",
		"route -n delete -net 64.0.0.0/2",
		"route -n delete -net 0.0.0.0/2",
	)
}

func TestManagerRecoveryRejectsSecretBearingJournal(t *testing.T) {
	env := newUpdateTestEnv(t)
	transaction := Transaction{
		ID: env.request.TransactionID, FromVersion: env.request.FromVersion, ToVersion: env.request.ToVersion,
		Phase: PhaseCommitted, AssetDigest: env.request.AssetSHA256,
		SnapshotPath: filepath.Join(env.paths.Snapshots, env.request.TransactionID),
		StartedAt:    time.Now().Add(-time.Minute).UTC(), UpdatedAt: time.Now().UTC(),
		LastError: "token=super-secret",
	}
	b, err := json.Marshal(transaction)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.paths.Transaction, b, 0o600); err != nil {
		t.Fatal(err)
	}
	env.events.reset()
	manager := env.restartedManagerWithoutJournal(t, "v2")

	err = manager.Recover(context.Background())
	if err == nil {
		t.Fatal("secret-bearing journal was accepted")
	}
	if got := manager.Status(); got.Phase != PhaseNeedsAttention || got.LastError != "update_journal_malformed" {
		t.Fatalf("status = %+v, want safe malformed-journal state", got)
	}
	assertSecretFreeUpdateValues(t, UpdateResult{}, err, manager.Status())
}

func TestManagerRecoveryFailuresStayBehindBarrier(t *testing.T) {
	tests := []struct {
		name           string
		phase          Phase
		mutate         func(*updateTestEnv)
		wantBarrier    bool
		wantProtection string
	}{
		{name: "descriptor", phase: PhaseActivating, mutate: func(env *updateTestEnv) { env.updater.recoverErr = errors.New("secret descriptor path") }, wantBarrier: true, wantProtection: ProtectionBlocked},
		{name: "barrier install", phase: PhaseActivating, mutate: func(env *updateTestEnv) { env.barrier.installErr = errors.New("secret barrier failure") }, wantProtection: ProtectionNeedsAttention},
		{name: "bypass reassert", phase: PhaseActivating, mutate: func(env *updateTestEnv) { env.barrier.reassertErr = errors.New("secret bypass failure") }, wantBarrier: true, wantProtection: ProtectionBlocked},
		{name: "restore", phase: PhaseActivating, mutate: func(env *updateTestEnv) { env.prepared.restoreErr = errors.New("vless://secret") }, wantBarrier: true, wantProtection: ProtectionBlocked},
		{name: "previous start", phase: PhaseActivating, mutate: func(env *updateTestEnv) { env.runner.failStartVersion = "v1" }, wantBarrier: true, wantProtection: ProtectionBlocked},
		{name: "previous health", phase: PhaseRollingBack, mutate: func(env *updateTestEnv) {
			env.health.failVersions = map[string]error{"v1": errors.New("token=secret")}
		}, wantBarrier: true, wantProtection: ProtectionBlocked},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newUpdateTestEnv(t)
			manager := env.restartedManager(t, tt.phase, "")
			tt.mutate(env)

			err := manager.Recover(context.Background())
			if err == nil {
				t.Fatal("injected recovery failure succeeded")
			}
			if manager.barrierProven() != tt.wantBarrier {
				t.Fatalf("barrier proven = %v, want %v; events=%#v", manager.barrierProven(), tt.wantBarrier, env.events.snapshot())
			}
			if got := manager.Status(); got.Phase != PhaseNeedsAttention || got.Protection != tt.wantProtection {
				t.Fatalf("status = %+v, want needs_attention protection %q", got, tt.wantProtection)
			}
			assertNoUpdateSecrets(t, err.Error())
		})
	}
}

func TestManagerUnprovenRecoveryBarrierGatesLifecycleUntilRetry(t *testing.T) {
	env := newUpdateTestEnv(t)
	manager := env.restartedManager(t, PhaseActivating, "")
	env.barrier.installErr = errors.New("barrier unavailable")

	err := manager.Recover(context.Background())
	if err == nil {
		t.Fatal("recovery unexpectedly succeeded without a proven barrier")
	}
	if manager.barrierProven() {
		t.Fatal("failed barrier install was recorded as held")
	}
	if got := manager.Status(); got.Protection != ProtectionNeedsAttention || got.Phase != PhaseNeedsAttention {
		t.Fatalf("status = %+v, want unproven needs_attention", got)
	}
	if containsEvent(env.events.snapshot(), "core.start.v1") {
		t.Fatalf("Core started without a proven recovery barrier: %#v", env.events.snapshot())
	}

	eventsBeforeUp := env.events.snapshot()
	if err := manager.Up(context.Background()); err == nil {
		t.Fatal("Up bypassed failed startup recovery")
	}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, eventsBeforeUp) {
		t.Fatalf("gated Up mutated lifecycle: before=%#v after=%#v", eventsBeforeUp, got)
	}

	env.barrier.installErr = nil
	if err := manager.Recover(context.Background()); err != nil {
		t.Fatalf("recovery retry failed: %v; events=%#v", err, env.events.snapshot())
	}
	if manager.barrierProven() || manager.Status().Protection != ProtectionProtected {
		t.Fatalf("successful retry did not restore protection: status=%+v barrier=%v", manager.Status(), manager.barrierProven())
	}
}

func TestManagerRecoveryRejectsNewerPersistedGuardianProtocol(t *testing.T) {
	env := newUpdateTestEnv(t)
	manager := env.restartedManager(t, PhaseActivating, "")
	env.prepared.requiredProtocol = currentGuardianProtocol + 1

	err := manager.Recover(context.Background())
	if err == nil {
		t.Fatal("newer persisted Guardian protocol was accepted")
	}
	if !manager.barrierProven() || containsEvent(env.events.snapshot(), "install.restore") || containsEvent(env.events.snapshot(), "core.start.v1") {
		t.Fatalf("incompatible recovery was not stopped behind barrier: %#v", env.events.snapshot())
	}
}

func TestManagerPreparedRecoveryDiscardsNewerGuardianProtocolPackage(t *testing.T) {
	env := newUpdateTestEnv(t)
	manager := env.restartedManager(t, PhasePrepared, "v1")
	env.prepared.requiredProtocol = currentGuardianProtocol + 1

	if err := manager.Recover(context.Background()); err != nil {
		t.Fatalf("prepared recovery did not continue previous Core: %v", err)
	}
	if !containsEvent(env.events.snapshot(), "install.commit") {
		t.Fatalf("incompatible pre-barrier staging was not discarded: %#v", env.events.snapshot())
	}
	if containsEvent(env.events.snapshot(), "barrier.install") {
		t.Fatalf("incompatible pre-barrier package installed barrier: %#v", env.events.snapshot())
	}
	if got := manager.Status(); got.CoreVersion != "v1" || got.Protection != ProtectionProtected {
		t.Fatalf("status = %+v, want protected previous Core", got)
	}
}

func assertEventBefore(t *testing.T, events []string, first, second string) {
	t.Helper()
	firstIndex, secondIndex := -1, -1
	for i, event := range events {
		if firstIndex < 0 && event == first {
			firstIndex = i
		}
		if secondIndex < 0 && event == second {
			secondIndex = i
		}
	}
	if firstIndex < 0 || secondIndex < 0 || firstIndex >= secondIndex {
		t.Fatalf("event %q did not precede %q: %#v", first, second, events)
	}
}

func firstCoreStartOrHealth(events []string) string {
	for _, event := range events {
		if strings.HasPrefix(event, "core.start.") || strings.HasPrefix(event, "health.") {
			return event
		}
	}
	return ""
}

func assertSecretFreeUpdateValues(t *testing.T, result UpdateResult, err error, status Status) {
	t.Helper()
	b, marshalErr := json.Marshal(struct {
		Result UpdateResult `json:"result"`
		Status Status       `json:"status"`
	}{result, status})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	value := string(b)
	if err != nil {
		value += " " + err.Error()
	}
	assertNoUpdateSecrets(t, value)
}

func assertNoUpdateSecrets(t *testing.T, value string) {
	t.Helper()
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"vless://", "password", "super-secret", "token=", "/users/secret", "secret-package", "secret-target"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("update value contains %q: %s", forbidden, value)
		}
	}
}

func containsEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

type updateTestEnv struct {
	root     string
	paths    Paths
	events   *eventLog
	store    *updateTestStore
	runner   *updateCoreRunner
	health   *updateHealthGate
	barrier  *fakeBarrier
	updater  *fakeUpdatePreparer
	prepared *fakePreparedUpdate
	manager  *Manager
	old      Process
	request  UpdateRequest
}

func (e *updateTestEnv) restartedManager(t *testing.T, phase Phase, existingVersion string) *Manager {
	t.Helper()
	transaction := Transaction{
		ID: e.request.TransactionID, FromVersion: e.request.FromVersion, ToVersion: e.request.ToVersion,
		Phase: phase, AssetDigest: e.request.AssetSHA256, SnapshotPath: filepath.Join(e.paths.Snapshots, e.request.TransactionID),
		StartedAt: time.Now().Add(-time.Minute).UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := e.store.Store.SaveTransaction(transaction); err != nil {
		t.Fatal(err)
	}
	e.events.reset()
	return e.restartedManagerWithoutJournal(t, existingVersion)
}

func (e *updateTestEnv) restartedManagerWithoutJournal(t *testing.T, existingVersion string) *Manager {
	t.Helper()
	e.runner = newUpdateCoreRunner(e.events)
	e.runner.startSequence = []string{"v1", "v1"}
	if existingVersion != "" {
		process := Process{PID: 200, Executable: install.BinPath, UID: 0, Generation: "existing-" + existingVersion}
		e.runner.seed(process, existingVersion)
	}
	e.health = &updateHealthGate{events: e.events}
	e.barrier = &fakeBarrier{events: e.events}
	e.prepared = &fakePreparedUpdate{
		events: e.events, snapshotPath: filepath.Join(e.paths.Snapshots, e.request.TransactionID),
		requiredProtocol: currentGuardianProtocol,
	}
	e.updater = &fakeUpdatePreparer{
		events: e.events, prepared: e.prepared, entered: make(chan struct{}, 1),
		requiredProtocol: currentGuardianProtocol,
		recoveryContext: BarrierContext{
			Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}, BlockIPv6: true,
		},
	}
	manager, err := NewManager(ManagerOptions{
		Store: e.store, Runner: e.runner, Health: e.health, Barrier: e.barrier,
		Restorer: &fakeNetworkRestorer{events: e.events}, Legacy: &fakeLegacyCore{events: e.events},
		BarrierContext: BarrierContext{Gateway: "192.0.2.1", BlockIPv6: true}, CoreVersion: "v2",
		UpdatePreparer: e.updater, GuardianProtocol: currentGuardianProtocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.manager = manager
	return manager
}

func newUpdateTestEnv(t *testing.T) *updateTestEnv {
	t.Helper()
	root := t.TempDir()
	paths := Paths{
		Desired:     filepath.Join(root, "guardian-state.json"),
		Transaction: filepath.Join(root, "update", "transaction.json"),
		Receipt:     filepath.Join(root, "update", "receipt.json"),
		Staging:     filepath.Join(root, "update", "staging"),
		Snapshots:   filepath.Join(root, "update", "snapshots"),
	}
	events := &eventLog{}
	store := &updateTestStore{Store: OpenStore(paths), events: events}
	if err := store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	events.reset()
	runner := newUpdateCoreRunner(events)
	health := &updateHealthGate{events: events}
	barrier := &fakeBarrier{events: events}
	prepared := &fakePreparedUpdate{events: events, snapshotPath: filepath.Join(paths.Snapshots, "tx-1"), requiredProtocol: currentGuardianProtocol}
	updater := &fakeUpdatePreparer{events: events, prepared: prepared, entered: make(chan struct{}, 1), requiredProtocol: currentGuardianProtocol}
	manager, err := NewManager(ManagerOptions{
		Store:            store,
		Runner:           runner,
		Health:           health,
		Barrier:          barrier,
		Restorer:         &fakeNetworkRestorer{events: events},
		Legacy:           &fakeLegacyCore{events: events},
		BarrierContext:   BarrierContext{Gateway: "192.0.2.1", ServerBypass: []string{"203.0.113.9/32"}, BlockIPv6: true},
		CoreVersion:      "v1",
		UpdatePreparer:   updater,
		GuardianProtocol: currentGuardianProtocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	old := Process{PID: 100, Executable: install.BinPath, UID: 0, Generation: "old"}
	runner.seed(old, "v1")
	manager.current = old
	manager.runtime = updateRuntime(old.PID, "v1")
	manager.setStatus(Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, CorePID: old.PID, CoreVersion: "v1", Protection: ProtectionProtected})
	env := &updateTestEnv{
		root: root, paths: paths, events: events, store: store, runner: runner, health: health,
		barrier: barrier, updater: updater, prepared: prepared, manager: manager, old: old,
	}
	env.request = env.writeRequest(t, "tx-1", []byte("verified macOS package"))
	return env
}

func (e *updateTestEnv) writeRequest(t *testing.T, transactionID string, data []byte) UpdateRequest {
	t.Helper()
	path := filepath.Join(e.paths.Staging, transactionID, "package.tar.gz")
	writeUpdatePackage(t, path, data)
	sum := sha256.Sum256(data)
	return UpdateRequest{
		TransactionID: transactionID,
		FromVersion:   "v1",
		ToVersion:     "v2",
		AssetSHA256:   hex.EncodeToString(sum[:]),
		PackagePath:   path,
		AppPath:       "/Applications/Bx.app",
	}
}

func writeUpdatePackage(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *updateTestEnv) fail(event string) {
	switch event {
	case "":
	case "prepare":
		e.updater.prepareErr = errors.New("prepare secret failure")
	case "prepared":
		e.store.failPhase = PhasePrepared
	case "barrier-install":
		e.barrier.installErr = errors.New("barrier secret failure")
	case "old-stop":
		e.runner.failStopVersion = "v1"
	case "bypass-reassert":
		e.barrier.reassertErr = errors.New("bypass secret failure")
	case "activate":
		e.prepared.activateErr = errors.New("activate secret failure")
	case "new-start":
		e.runner.failStartVersion = "v2"
	case "new-health":
		e.health.failVersions = map[string]error{"v2": errors.New("new health secret failure")}
	case "old-health":
		e.health.failVersions = map[string]error{
			"v2": errors.New("new health secret failure"),
			"v1": errors.New("old health secret failure"),
		}
		e.health.failAfter = map[string]int{"v1": 2}
	case "receipt":
		e.store.failReceipt = true
	case "barrier-cleanup":
		e.barrier.removeErr = errors.New("barrier cleanup secret failure")
	default:
		panic("unknown failure event " + event)
	}
}

type updateTestStore struct {
	*Store
	events      *eventLog
	failPhase   Phase
	failReceipt bool
	failClear   bool
}

type planValidatingBarrier struct {
	delegate          Barrier
	installedCommands []Command
}

func (b *planValidatingBarrier) Install(ctx context.Context, barrierContext BarrierContext) error {
	commands, _, _, err := PlanBarrier(barrierContext)
	if err != nil {
		return err
	}
	b.installedCommands = append([]Command(nil), commands...)
	return b.delegate.Install(ctx, barrierContext)
}

func (b *planValidatingBarrier) ReassertBypass(ctx context.Context, barrierContext BarrierContext) error {
	return b.delegate.ReassertBypass(ctx, barrierContext)
}

func (b *planValidatingBarrier) Release(ctx context.Context, barrierContext BarrierContext, transferredBypasses []string) error {
	return b.delegate.Release(ctx, barrierContext, transferredBypasses)
}

func (b *planValidatingBarrier) Remove(ctx context.Context, barrierContext BarrierContext) error {
	return b.delegate.Remove(ctx, barrierContext)
}

func (s *updateTestStore) SaveDesired(desired DesiredState) error {
	return s.Store.SaveDesired(desired)
}

func (s *updateTestStore) SaveTransaction(transaction Transaction) error {
	event := "journal." + string(transaction.Phase)
	if transaction.BarrierInstallIntent {
		event += ".barrier_intent"
	}
	s.events.add(event)
	if s.failPhase == transaction.Phase {
		return errors.New("journal secret failure")
	}
	return s.Store.SaveTransaction(transaction)
}

func (s *updateTestStore) SaveReceipt(receipt Receipt) error {
	s.events.add("receipt." + string(receipt.Outcome))
	if s.failReceipt {
		return errors.New("receipt secret failure")
	}
	return s.Store.SaveReceipt(receipt)
}

func (s *updateTestStore) ClearTransaction() error {
	s.events.add("journal.clear")
	if s.failClear {
		return errors.New("journal clear failure")
	}
	return s.Store.ClearTransaction()
}

type fakePreparedUpdate struct {
	events              *eventLog
	snapshotPath        string
	requiredProtocol    int
	activateErr         error
	restoreErr          error
	commitErr           error
	boundBarrierContext BarrierContext
	bindErr             error
	commitEntered       chan struct{}
	commitBlock         chan struct{}
}

func (p *fakePreparedUpdate) SnapshotPath() string { return p.snapshotPath }
func (p *fakePreparedUpdate) RequiredGuardianProtocol() int {
	return p.requiredProtocol
}
func (p *fakePreparedUpdate) BindBarrierContext(barrierContext BarrierContext) error {
	p.events.add("install.bind_barrier")
	p.boundBarrierContext = cloneBarrierContext(barrierContext)
	return p.bindErr
}
func (p *fakePreparedUpdate) Activate() error {
	p.events.add("install.activate")
	return p.activateErr
}
func (p *fakePreparedUpdate) Restore() error {
	p.events.add("install.restore")
	return p.restoreErr
}
func (p *fakePreparedUpdate) Commit() error {
	p.events.add("install.commit")
	select {
	case p.commitEntered <- struct{}{}:
	default:
	}
	if p.commitBlock != nil {
		<-p.commitBlock
	}
	return p.commitErr
}

type fakeUpdatePreparer struct {
	events           *eventLog
	prepared         *fakePreparedUpdate
	prepareErr       error
	requiredProtocol int
	block            chan struct{}
	entered          chan struct{}
	recoveryContext  BarrierContext
	recoverErr       error
}

func (p *fakeUpdatePreparer) Prepare(_ context.Context, _ UpdateRequest, _ []byte, _ Paths) (PreparedUpdate, error) {
	p.events.add("prepare")
	select {
	case p.entered <- struct{}{}:
	default:
	}
	if p.block != nil {
		<-p.block
	}
	if p.prepareErr != nil {
		return nil, p.prepareErr
	}
	p.prepared.requiredProtocol = p.requiredProtocol
	return p.prepared, nil
}

func (p *fakeUpdatePreparer) Recover(context.Context, Transaction, Paths) (PreparedUpdate, BarrierContext, error) {
	p.events.add("recover")
	if p.recoverErr != nil {
		return nil, p.recoveryContext, p.recoverErr
	}
	return p.prepared, p.recoveryContext, nil
}

func (p *fakeUpdatePreparer) RecoveryBarrierContext(context.Context, Paths) (BarrierContext, error) {
	p.events.add("recovery.context")
	if p.recoverErr != nil {
		return BarrierContext{}, p.recoverErr
	}
	return p.recoveryContext, nil
}

type updateCoreRunner struct {
	mu                    sync.Mutex
	events                *eventLog
	current               Process
	versions              map[int]string
	nextPID               int
	startSequence         []string
	failStartVersion      string
	uncertainStartVersion string
	failStopVersion       string
	stopSawCanceled       map[string]bool
	startOptions          []CoreStartOptions
}

func newUpdateCoreRunner(events *eventLog) *updateCoreRunner {
	return &updateCoreRunner{
		events: events, versions: make(map[int]string), nextPID: 100,
		startSequence: []string{"v2", "v1", "v2", "v1"}, stopSawCanceled: make(map[string]bool),
	}
}

func (r *updateCoreRunner) seed(process Process, version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current = process
	r.versions[process.PID] = version
}

func (r *updateCoreRunner) Existing(context.Context) (Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current, nil
}

func (r *updateCoreRunner) Watch(process Process) Process { return process }

func (r *updateCoreRunner) Verify(process Process) error {
	if process.PID <= 0 || process.UID != 0 || process.Executable != install.BinPath {
		return errors.New("unverifiable process")
	}
	return nil
}

func (r *updateCoreRunner) Start(_ context.Context, options CoreStartOptions) (Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startOptions = append(r.startOptions, CoreStartOptions{GuardianBypassHandoff: append([]string(nil), options.GuardianBypassHandoff...)})
	version := "v2"
	if len(r.startSequence) != 0 {
		version = r.startSequence[0]
		r.startSequence = r.startSequence[1:]
	}
	r.events.add("core.start." + version)
	if version == r.uncertainStartVersion {
		r.nextPID++
		process := Process{PID: r.nextPID, Executable: install.BinPath, UID: 0, Generation: fmt.Sprintf("%s:%d", version, r.nextPID)}
		r.current = process
		r.versions[process.PID] = version
		return Process{}, uncertainOwnership(process, errors.New("launch ownership uncertain"))
	}
	if version == r.failStartVersion {
		return Process{}, errors.New("start secret failure")
	}
	r.nextPID++
	process := Process{PID: r.nextPID, Executable: install.BinPath, UID: 0, Generation: fmt.Sprintf("%s:%d", version, r.nextPID)}
	r.current = process
	r.versions[process.PID] = version
	return process, nil
}

func (r *updateCoreRunner) startOptionsSnapshot() []CoreStartOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]CoreStartOptions, len(r.startOptions))
	for index, options := range r.startOptions {
		result[index].GuardianBypassHandoff = append([]string(nil), options.GuardianBypassHandoff...)
	}
	return result
}

func (r *updateCoreRunner) Stop(ctx context.Context, process Process) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	version := r.versions[process.PID]
	r.events.add("core.stop." + version)
	r.stopSawCanceled[version] = ctx.Err() != nil
	if version == r.failStopVersion {
		return errors.New("stop secret failure")
	}
	if r.current.PID == process.PID {
		r.current = Process{}
	}
	return nil
}

type updateHealthGate struct {
	events           *eventLog
	failVersions     map[string]error
	failAfter        map[string]int
	blockVersions    map[string]bool
	runtimeByVersion map[string]supervisor.RuntimeState
	calls            map[string]int
}

func (h *updateHealthGate) Wait(ctx context.Context, target HealthTarget) (supervisor.RuntimeState, error) {
	h.events.add("health." + target.Version)
	if h.calls == nil {
		h.calls = make(map[string]int)
	}
	h.calls[target.Version]++
	if h.blockVersions[target.Version] {
		<-ctx.Done()
		return supervisor.RuntimeState{}, ctx.Err()
	}
	if err := h.failVersions[target.Version]; err != nil &&
		(h.failAfter[target.Version] == 0 || h.calls[target.Version] >= h.failAfter[target.Version]) {
		return supervisor.RuntimeState{}, err
	}
	if state, ok := h.runtimeByVersion[target.Version]; ok {
		return state, nil
	}
	return updateRuntime(target.PID, target.Version), nil
}

type fakeGatewayProvider struct {
	mu      sync.Mutex
	gateway string
	err     error
	calls   int
}

func (p *fakeGatewayProvider) DefaultGateway(context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.gateway, p.err
}

func (p *fakeGatewayProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func updateRuntime(pid int, version string) supervisor.RuntimeState {
	return supervisor.RuntimeState{
		Version: version, PID: pid, TunName: "utun7", SocksAddr: "127.0.0.1:1080",
		ServerBypass: []string{"198.51.100.10/32"}, TunnelHealthy: true,
		DNSListening: true, RoutesInstalled: true,
	}
}

func TestUpdateRequestJSONContract(t *testing.T) {
	request := UpdateRequest{
		TransactionID: "tx-1", FromVersion: "v1", ToVersion: "v2",
		AssetSHA256: strings.Repeat("a", 64), PackagePath: "/var/lib/bx/update/staging/tx-1/package.tar.gz",
		AppPath: "/Users/test/Applications/Bx.app", AppUID: 501, AppGID: 20,
	}
	b, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	wantFields := []string{"transaction_id", "from_version", "to_version", "asset_sha256", "package_path", "app_path", "app_uid", "app_gid"}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(wantFields) {
		t.Fatalf("fields = %#v, want exactly %#v", got, wantFields)
	}
	for _, field := range wantFields {
		if _, ok := got[field]; !ok {
			t.Fatalf("missing field %q in %s", field, b)
		}
	}
	for _, forbidden := range []string{"gateway", "server_bypass", "client_link", "token", "password"} {
		if bytes.Contains(bytes.ToLower(b), []byte(forbidden)) {
			t.Fatalf("request contains forbidden field %q: %s", forbidden, b)
		}
	}
}

func TestRequiredGuardianProtocolReadsPackageMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata []byte
		want     int
		wantErr  bool
	}{
		{name: "legacy package", want: currentGuardianProtocol},
		{name: "current", metadata: []byte(`{"guardian_protocol":1}`), want: 1},
		{name: "newer", metadata: []byte(`{"guardian_protocol":2}`), want: 2},
		{name: "unknown field", metadata: []byte(`{"guardian_protocol":1,"token":"secret"}`), wantErr: true},
		{name: "invalid", metadata: []byte(`{"guardian_protocol":0}`), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := guardianProtocolTestPackage(t, "arm64", tt.metadata)
			got, err := requiredGuardianProtocol(data, "arm64")
			if (err != nil) != tt.wantErr {
				t.Fatalf("requiredGuardianProtocol error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("protocol = %d, want %d", got, tt.want)
			}
			if err != nil {
				assertNoUpdateSecrets(t, err.Error())
			}
		})
	}
}

func TestRecoveredMacOSUpdateRestoresPreviousArtifactsAndCleans(t *testing.T) {
	env := newDiskRecoveryTestEnv(t)
	transaction := env.writeDescriptor(t, updateRecoveryDescriptorVersion)

	prepared, barrierContext, err := readRecoveredMacOSUpdate(transaction, env.paths, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(barrierContext, env.barrierContext) {
		t.Fatalf("barrier context = %+v, want %+v", barrierContext, env.barrierContext)
	}
	if prepared.RequiredGuardianProtocol() != currentGuardianProtocol {
		t.Fatalf("required protocol = %d", prepared.RequiredGuardianProtocol())
	}
	if err := prepared.Restore(); err != nil {
		t.Fatal(err)
	}
	requireDiskFileContents(t, env.cliPath, "old-cli")
	requireDiskFileContents(t, filepath.Join(env.appPath, "Contents/MacOS/BxMenu"), "old-menu")
	if err := prepared.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		env.snapshotPath,
		env.stagingPath,
		filepath.Join(filepath.Dir(env.cliPath), ".bx-update-tx-1"),
		filepath.Join(filepath.Dir(env.appPath), ".Bx.app.previous-tx-1"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recovery path still exists %q: %v", path, err)
		}
	}
}

func TestRecoveredMacOSUpdateRefusesSubstitutedDestination(t *testing.T) {
	env := newDiskRecoveryTestEnv(t)
	transaction := env.writeDescriptor(t, updateRecoveryDescriptorVersion)
	writeUpdatePackage(t, env.cliPath, []byte("substituted-secret-cli"))

	prepared, _, err := readRecoveredMacOSUpdate(transaction, env.paths, false)
	if err != nil {
		t.Fatal(err)
	}
	err = prepared.Restore()
	if err == nil {
		t.Fatal("substituted destination was overwritten")
	}
	requireDiskFileContents(t, env.cliPath, "substituted-secret-cli")
	assertNoUpdateSecrets(t, err.Error())
}

func TestRecoveredMacOSUpdateResumesAtomicRenameStates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*diskRecoveryTestEnv) error
	}{
		{name: "activation app old moved aside", mutate: func(env *diskRecoveryTestEnv) error {
			return os.Rename(env.appPath, filepath.Join(filepath.Dir(env.appPath), ".Bx.app.update-tx-1"))
		}},
		{name: "rollback app new moved to discard", mutate: func(env *diskRecoveryTestEnv) error {
			parent := filepath.Dir(env.appPath)
			if err := os.Rename(filepath.Join(parent, ".Bx.app.previous-tx-1"), filepath.Join(parent, ".Bx.app.restore-tx-1")); err != nil {
				return err
			}
			return os.Rename(env.appPath, filepath.Join(parent, ".Bx.app.discard-tx-1"))
		}},
		{name: "rollback app restore promoted", mutate: func(env *diskRecoveryTestEnv) error {
			parent := filepath.Dir(env.appPath)
			if err := os.Rename(env.appPath, filepath.Join(parent, ".Bx.app.discard-tx-1")); err != nil {
				return err
			}
			return os.Rename(filepath.Join(parent, ".Bx.app.previous-tx-1"), env.appPath)
		}},
		{name: "rollback CLI new moved to discard", mutate: func(env *diskRecoveryTestEnv) error {
			parent := filepath.Dir(env.cliPath)
			if err := os.Rename(env.cliPath, filepath.Join(parent, ".bx-update-tx-1.discard-recovery")); err != nil {
				return err
			}
			data, err := os.ReadFile(filepath.Join(env.snapshotPath, "bx"))
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(parent, ".bx-update-tx-1.restore-recovery"), data, 0o600)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newDiskRecoveryTestEnv(t)
			transaction := env.writeDescriptor(t, updateRecoveryDescriptorVersion)
			if err := tt.mutate(env); err != nil {
				t.Fatal(err)
			}
			prepared, _, err := readRecoveredMacOSUpdate(transaction, env.paths, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := prepared.Restore(); err != nil {
				t.Fatalf("resume restore: %v", err)
			}
			if err := prepared.Restore(); err != nil {
				t.Fatalf("idempotent restore: %v", err)
			}
			requireDiskFileContents(t, env.cliPath, "old-cli")
			requireDiskFileContents(t, filepath.Join(env.appPath, "Contents/MacOS/BxMenu"), "old-menu")
			if err := prepared.Commit(); err != nil {
				t.Fatalf("cleanup resumed state: %v", err)
			}
		})
	}
}

func TestRecoveredMacOSUpdateResumesCleanupRenameResidue(t *testing.T) {
	tests := []struct {
		name string
		path func(*diskRecoveryTestEnv) string
	}{
		{name: "CLI discard", path: func(env *diskRecoveryTestEnv) string {
			return filepath.Join(filepath.Dir(env.cliPath), ".bx-update-tx-1.discard-recovery")
		}},
		{name: "app discard", path: func(env *diskRecoveryTestEnv) string {
			return filepath.Join(filepath.Dir(env.appPath), ".Bx.app.discard-tx-1")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newDiskRecoveryTestEnv(t)
			transaction := env.writeDescriptor(t, updateRecoveryDescriptorVersion)
			prepared, _, err := readRecoveredMacOSUpdate(transaction, env.paths, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := prepared.Restore(); err != nil {
				t.Fatal(err)
			}
			path := tt.path(env)
			if err := os.Rename(path, path+".guardian-cleanup"); err != nil {
				t.Fatal(err)
			}
			if err := prepared.Commit(); err != nil {
				t.Fatalf("cleanup residue was not resumed: %v", err)
			}
			if _, err := os.Lstat(path + ".guardian-cleanup"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cleanup residue remains: %v", err)
			}
		})
	}
}

func TestRecoveredMacOSUpdateSyncFailurePreservesStagingForRetry(t *testing.T) {
	env := newDiskRecoveryTestEnv(t)
	transaction := env.writeDescriptor(t, updateRecoveryDescriptorVersion)
	prepared, _, err := readRecoveredMacOSUpdate(transaction, env.paths, false)
	if err != nil {
		t.Fatal(err)
	}
	originalSync := syncRecoveryRoot
	t.Cleanup(func() { syncRecoveryRoot = originalSync })
	failed := false
	syncRecoveryRoot = func(*os.Root) error {
		if !failed {
			failed = true
			return errors.New("injected directory sync failure")
		}
		return nil
	}

	if err := prepared.Commit(); err == nil {
		t.Fatal("directory sync failure returned successful cleanup")
	}
	if _, err := os.Stat(env.stagingPath); err != nil {
		t.Fatalf("sync failure removed staging proof: %v", err)
	}
	syncRecoveryRoot = originalSync
	if err := prepared.Commit(); err != nil {
		t.Fatalf("cleanup retry after sync failure: %v", err)
	}
	if _, err := os.Stat(env.stagingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful cleanup retry retained staging: %v", err)
	}
}

func TestRecoveredMacOSUpdateVerifiesEverySnapshotBeforeMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*diskRecoveryTestEnv)
	}{
		{name: "CLI snapshot", mutate: func(env *diskRecoveryTestEnv) {
			writeUpdatePackage(t, filepath.Join(env.snapshotPath, "bx"), []byte("substituted-secret-cli"))
		}},
		{name: "app snapshot", mutate: func(env *diskRecoveryTestEnv) {
			writeUpdatePackage(t, filepath.Join(env.snapshotPath, "Bx.app/Contents/MacOS/BxMenu"), []byte("substituted-secret-menu"))
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newDiskRecoveryTestEnv(t)
			transaction := env.writeDescriptor(t, updateRecoveryDescriptorVersion)
			tt.mutate(env)

			prepared, _, err := readRecoveredMacOSUpdate(transaction, env.paths, false)
			if err != nil {
				t.Fatal(err)
			}
			err = prepared.Restore()
			if err == nil {
				t.Fatal("substituted snapshot was restored")
			}
			requireDiskFileContents(t, env.cliPath, "new-cli")
			requireDiskFileContents(t, filepath.Join(env.appPath, "Contents/MacOS/BxMenu"), "new-menu")
			assertNoUpdateSecrets(t, err.Error())
		})
	}
}

func TestRecoveredMacOSUpdateReadsPriorDescriptorVersion(t *testing.T) {
	env := newDiskRecoveryTestEnv(t)
	transaction := env.writeDescriptor(t, 0)
	descriptorPath := filepath.Join(env.stagingPath, updateRecoveryDescriptorName)
	b, err := os.ReadFile(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "schema_version")
	delete(raw, "guardian_protocol")
	b, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(descriptorPath, b, 0o600); err != nil {
		t.Fatal(err)
	}

	prepared, _, err := readRecoveredMacOSUpdate(transaction, env.paths, false)
	if err != nil {
		t.Fatalf("prior descriptor rejected: %v", err)
	}
	if got := prepared.RequiredGuardianProtocol(); got != currentGuardianProtocol {
		t.Fatalf("prior descriptor protocol = %d, want %d", got, currentGuardianProtocol)
	}
}

func TestRecoveredMacOSUpdateRejectsMalformedDescriptorWithoutMutation(t *testing.T) {
	env := newDiskRecoveryTestEnv(t)
	transaction := env.writeDescriptor(t, updateRecoveryDescriptorVersion)
	descriptorPath := filepath.Join(env.stagingPath, updateRecoveryDescriptorName)
	if err := os.WriteFile(descriptorPath, []byte(`{"schema_version":99,"app_path":"/Users/secret/Applications/Bx.app"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := readRecoveredMacOSUpdate(transaction, env.paths, false)
	if err == nil {
		t.Fatal("malformed recovery descriptor accepted")
	}
	requireDiskFileContents(t, env.cliPath, "new-cli")
	requireDiskFileContents(t, filepath.Join(env.appPath, "Contents/MacOS/BxMenu"), "new-menu")
	assertNoUpdateSecrets(t, err.Error())
}

func TestRecoveredMacOSUpdateRejectsOversizedDescriptor(t *testing.T) {
	env := newDiskRecoveryTestEnv(t)
	transaction := env.writeDescriptor(t, updateRecoveryDescriptorVersion)
	descriptorPath := filepath.Join(env.stagingPath, updateRecoveryDescriptorName)
	b, err := os.ReadFile(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, bytes.Repeat([]byte(" "), 64<<10)...)
	b = append(b, 'x')
	if err := os.WriteFile(descriptorPath, b, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := readRecoveredMacOSUpdate(transaction, env.paths, false); err == nil {
		t.Fatal("oversized recovery descriptor was accepted")
	}
}

func TestArtifactFingerprintSeparatesFileContentsFromFollowingEntries(t *testing.T) {
	root := t.TempDir()
	one := filepath.Join(root, "one")
	two := filepath.Join(root, "two")
	writeUpdatePackage(t, filepath.Join(one, "a"), []byte("/b\x00600\x00f\x00X"))
	writeUpdatePackage(t, filepath.Join(two, "a"), nil)
	writeUpdatePackage(t, filepath.Join(two, "b"), []byte("X"))

	oneFingerprint, err := fingerprintArtifact(one)
	if err != nil {
		t.Fatal(err)
	}
	twoFingerprint, err := fingerprintArtifact(two)
	if err != nil {
		t.Fatal(err)
	}
	if oneFingerprint == twoFingerprint {
		t.Fatalf("different artifact trees shared fingerprint %+v", oneFingerprint)
	}
}

func TestRecoveredMacOSUpdateRejectsMissingPreviousArtifactFingerprint(t *testing.T) {
	env := newDiskRecoveryTestEnv(t)
	transaction := env.writeDescriptor(t, updateRecoveryDescriptorVersion)
	descriptorPath := filepath.Join(env.stagingPath, updateRecoveryDescriptorName)
	descriptor, err := readUpdateRecoveryDescriptor(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.OldCLI = artifactFingerprint{}
	if err := writeUpdateRecoveryDescriptor(descriptor); err != nil {
		t.Fatal(err)
	}

	if _, _, err := readRecoveredMacOSUpdate(transaction, env.paths, false); err == nil {
		t.Fatal("descriptor without previous CLI fingerprint was accepted")
	}
}

func TestRecoveredMacOSUpdateRejectsSharedDescriptorDirectory(t *testing.T) {
	env := newDiskRecoveryTestEnv(t)
	transaction := env.writeDescriptor(t, updateRecoveryDescriptorVersion)
	if err := os.Chmod(env.stagingPath, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := readRecoveredMacOSUpdate(transaction, env.paths, false); err == nil {
		t.Fatal("descriptor in shared staging directory was accepted")
	}
}

func TestRecoveryBarrierContextScansSingleRootOwnedDescriptor(t *testing.T) {
	env := newDiskRecoveryTestEnv(t)
	env.writeDescriptor(t, updateRecoveryDescriptorVersion)

	got, err := (macOSUpdatePreparer{}).RecoveryBarrierContext(context.Background(), env.paths)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, env.barrierContext) {
		t.Fatalf("barrier context = %+v, want %+v", got, env.barrierContext)
	}

	other := filepath.Join(env.paths.Staging, "tx-2")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, updateRecoveryDescriptorName), []byte(`{"schema_version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (macOSUpdatePreparer{}).RecoveryBarrierContext(context.Background(), env.paths); err == nil {
		t.Fatal("valid descriptor alongside malformed candidate was accepted")
	}
	descriptor, err := os.ReadFile(filepath.Join(env.stagingPath, updateRecoveryDescriptorName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, updateRecoveryDescriptorName), descriptor, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (macOSUpdatePreparer{}).RecoveryBarrierContext(context.Background(), env.paths); err == nil {
		t.Fatal("ambiguous recovery descriptors were accepted")
	}
}

type diskRecoveryTestEnv struct {
	root           string
	paths          Paths
	cliPath        string
	appPath        string
	snapshotPath   string
	stagingPath    string
	barrierContext BarrierContext
	oldCLI         artifactFingerprint
	newCLI         artifactFingerprint
	oldApp         artifactFingerprint
	newApp         artifactFingerprint
}

func newDiskRecoveryTestEnv(t *testing.T) *diskRecoveryTestEnv {
	t.Helper()
	root := t.TempDir()
	env := &diskRecoveryTestEnv{
		root: root,
		paths: Paths{
			Desired: filepath.Join(root, "guardian-state.json"), Transaction: filepath.Join(root, "update/transaction.json"),
			Receipt: filepath.Join(root, "update/receipt.json"), Staging: filepath.Join(root, "update/staging"),
			Snapshots: filepath.Join(root, "update/snapshots"),
		},
		cliPath:        filepath.Join(root, "usr/local/bin/bx"),
		appPath:        filepath.Join(root, "Applications/Bx.app"),
		snapshotPath:   filepath.Join(root, "update/snapshots/tx-1"),
		stagingPath:    filepath.Join(root, "update/staging/tx-1"),
		barrierContext: BarrierContext{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}, BlockIPv6: true},
	}
	writeUpdatePackage(t, filepath.Join(env.snapshotPath, "bx"), []byte("old-cli"))
	writeUpdatePackage(t, filepath.Join(env.snapshotPath, "Bx.app/Contents/MacOS/BxMenu"), []byte("old-menu"))
	writeUpdatePackage(t, filepath.Join(env.snapshotPath, "Bx.app/Contents/Info.plist"), []byte("old-plist"))
	writeUpdatePackage(t, env.cliPath, []byte("new-cli"))
	writeUpdatePackage(t, filepath.Join(env.appPath, "Contents/MacOS/BxMenu"), []byte("new-menu"))
	writeUpdatePackage(t, filepath.Join(env.appPath, "Contents/Info.plist"), []byte("new-plist"))
	writeUpdatePackage(t, filepath.Join(filepath.Dir(env.appPath), ".Bx.app.previous-tx-1/Contents/MacOS/BxMenu"), []byte("old-menu"))
	writeUpdatePackage(t, filepath.Join(filepath.Dir(env.appPath), ".Bx.app.previous-tx-1/Contents/Info.plist"), []byte("old-plist"))
	if err := os.MkdirAll(env.stagingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	var err error
	if env.oldCLI, err = fingerprintArtifact(filepath.Join(env.snapshotPath, "bx")); err != nil {
		t.Fatal(err)
	}
	if env.newCLI, err = fingerprintArtifact(env.cliPath); err != nil {
		t.Fatal(err)
	}
	if env.oldApp, err = fingerprintArtifact(filepath.Join(env.snapshotPath, "Bx.app")); err != nil {
		t.Fatal(err)
	}
	if env.newApp, err = fingerprintArtifact(env.appPath); err != nil {
		t.Fatal(err)
	}
	return env
}

func (e *diskRecoveryTestEnv) writeDescriptor(t *testing.T, schemaVersion int) Transaction {
	t.Helper()
	descriptor := updateRecoveryDescriptor{
		SchemaVersion: schemaVersion, GuardianProtocol: currentGuardianProtocol,
		TransactionID: "tx-1", FromVersion: "v1", ToVersion: "v2", AssetDigest: strings.Repeat("a", 64),
		CLIPath: e.cliPath, AppPath: e.appPath, AppUID: os.Geteuid(), AppGID: os.Getegid(),
		SnapshotPath: e.snapshotPath, StagingPath: e.stagingPath, BarrierContext: e.barrierContext,
		HadCLI: true, HadApp: true, OldCLI: e.oldCLI, NewCLI: e.newCLI, OldApp: e.oldApp, NewApp: e.newApp,
	}
	if err := writeUpdateRecoveryDescriptor(descriptor); err != nil {
		t.Fatal(err)
	}
	return Transaction{
		ID: descriptor.TransactionID, FromVersion: descriptor.FromVersion, ToVersion: descriptor.ToVersion,
		Phase: PhaseActivating, AssetDigest: descriptor.AssetDigest, SnapshotPath: descriptor.SnapshotPath,
		StartedAt: time.Now().Add(-time.Minute).UTC(), UpdatedAt: time.Now().UTC(),
	}
}

func requireDiskFileContents(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatalf("%s = %q, want %q", path, b, want)
	}
}

func guardianProtocolTestPackage(t *testing.T, arch string, metadata []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	files := map[string][]byte{
		"bx-macos-" + arch + "/bx":                           []byte("cli"),
		"bx-macos-" + arch + "/Bx.app/Contents/Info.plist":   []byte("plist"),
		"bx-macos-" + arch + "/Bx.app/Contents/MacOS/BxMenu": []byte("menu"),
	}
	if metadata != nil {
		files["bx-macos-"+arch+"/guardian-update.json"] = metadata
	}
	for name, content := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}
