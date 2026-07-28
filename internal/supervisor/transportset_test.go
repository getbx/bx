package supervisor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getbx/bx/internal/dialer"
	"github.com/getbx/bx/internal/tunnel"
)

type fakeTransportCandidate struct {
	healthy atomic.Bool
	stops   atomic.Int32
	onStop  func()
}

func newFakeTransportCandidate(healthy bool) *fakeTransportCandidate {
	candidate := &fakeTransportCandidate{}
	candidate.healthy.Store(healthy)
	return candidate
}

func (c *fakeTransportCandidate) Healthy() bool { return c.healthy.Load() }
func (c *fakeTransportCandidate) Stop() {
	c.stops.Add(1)
	if c.onStop != nil {
		c.onStop()
	}
}

type fakeTransportSlot struct {
	link      string
	candidate transportCandidate
	retired   transportCandidate
	transport *dialer.Transport
	err       error
	prepare   func(context.Context, string, string) (transportCandidate, error)

	mu          sync.Mutex
	recoveryIDs []string
	commits     []transportCandidate
	aborts      []transportCandidate
}

func (s *fakeTransportSlot) descriptor(name transportSlotName) transportSetSlot {
	return transportSetSlot{
		name: name,
		link: func() string { return s.link },
		prepare: func(ctx context.Context, link, recoveryID string) (transportCandidate, error) {
			s.mu.Lock()
			s.recoveryIDs = append(s.recoveryIDs, recoveryID)
			s.mu.Unlock()
			if s.prepare != nil {
				return s.prepare(ctx, link, recoveryID)
			}
			return s.candidate, s.err
		},
		commit: func(candidate transportCandidate, _ string) transportSlotCommit {
			s.mu.Lock()
			s.commits = append(s.commits, candidate)
			s.mu.Unlock()
			return transportSlotCommit{retired: s.retired, transport: s.transport}
		},
		abort: func(candidate transportCandidate) {
			s.mu.Lock()
			s.aborts = append(s.aborts, candidate)
			s.mu.Unlock()
			candidate.Stop()
		},
	}
}

func (s *fakeTransportSlot) counts() (commits, aborts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.commits), len(s.aborts)
}

func TestTransportSetMainOnlyCommitsHealthyCandidate(t *testing.T) {
	mainCandidate := newFakeTransportCandidate(true)
	main := &fakeTransportSlot{link: "vless://main", candidate: mainCandidate}
	set := newLiveTransportSet(newTransportGeneration(), main.descriptor(mainTransportSlot), nil, nil)

	if err := set.Recover(context.Background()); err != nil {
		t.Fatalf("Recover = %v", err)
	}
	commits, aborts := main.counts()
	if commits != 1 || aborts != 0 || mainCandidate.stops.Load() != 0 {
		t.Fatalf("main commits=%d aborts=%d stops=%d, want 1/0/0", commits, aborts, mainCandidate.stops.Load())
	}
}

func TestTransportSetMainAndUDPCommitOnlyAfterBothPrepare(t *testing.T) {
	mainCandidate := newFakeTransportCandidate(true)
	udpCandidate := newFakeTransportCandidate(true)
	mainPrepared := make(chan struct{})
	releaseMain := make(chan struct{})
	main := &fakeTransportSlot{link: "vless://main"}
	main.prepare = func(context.Context, string, string) (transportCandidate, error) {
		close(mainPrepared)
		<-releaseMain
		return mainCandidate, nil
	}
	udp := &fakeTransportSlot{link: "hysteria2://udp", candidate: udpCandidate}
	set := newLiveTransportSet(newTransportGeneration(), main.descriptor(mainTransportSlot), slotPointer(udp.descriptor(udpTransportSlot)), nil)

	done := make(chan error, 1)
	go func() { done <- set.Recover(context.Background()) }()
	<-mainPrepared
	waitForTransportTest(t, time.Second, func() bool {
		udp.mu.Lock()
		defer udp.mu.Unlock()
		return len(udp.recoveryIDs) == 1
	})
	if commits, _ := udp.counts(); commits != 0 {
		t.Fatalf("UDP committed before main prepared: commits=%d", commits)
	}
	close(releaseMain)
	if err := <-done; err != nil {
		t.Fatalf("Recover = %v", err)
	}
	mainCommits, mainAborts := main.counts()
	udpCommits, udpAborts := udp.counts()
	if mainCommits != 1 || udpCommits != 1 || mainAborts != 0 || udpAborts != 0 {
		t.Fatalf("main commits/aborts=%d/%d UDP=%d/%d, want 1/0 and 1/0", mainCommits, mainAborts, udpCommits, udpAborts)
	}
	main.mu.Lock()
	mainID := main.recoveryIDs[0]
	main.mu.Unlock()
	udp.mu.Lock()
	udpID := udp.recoveryIDs[0]
	udp.mu.Unlock()
	if mainID == udpID {
		t.Fatalf("main and UDP candidate recovery IDs collide: %q", mainID)
	}
}

func TestTransportSetActivatesBothSlotsBeforeRetiringOldCandidates(t *testing.T) {
	mainCandidate := newFakeTransportCandidate(true)
	udpCandidate := newFakeTransportCandidate(true)
	main := &fakeTransportSlot{link: "vless://main", candidate: mainCandidate}
	udp := &fakeTransportSlot{link: "hysteria2://udp", candidate: udpCandidate}
	main.retired = newFakeTransportCandidate(true)
	udp.retired = newFakeTransportCandidate(true)
	assertBothCommitted := func() {
		mainCommits, _ := main.counts()
		udpCommits, _ := udp.counts()
		if mainCommits != 1 || udpCommits != 1 {
			t.Errorf("retired old candidate before both slots activated: main=%d UDP=%d", mainCommits, udpCommits)
		}
	}
	main.retired.(*fakeTransportCandidate).onStop = assertBothCommitted
	udp.retired.(*fakeTransportCandidate).onStop = assertBothCommitted
	set := newLiveTransportSet(newTransportGeneration(), main.descriptor(mainTransportSlot), slotPointer(udp.descriptor(udpTransportSlot)), nil)

	if err := set.Recover(context.Background()); err != nil {
		t.Fatalf("Recover = %v", err)
	}
	if main.retired.(*fakeTransportCandidate).stops.Load() != 1 || udp.retired.(*fakeTransportCandidate).stops.Load() != 1 {
		t.Fatal("old candidates were not retired exactly once")
	}
}

func TestTransportSetPublishesMainAndUDPAsOneGeneration(t *testing.T) {
	mainTransport := &dialer.Transport{}
	udpTransport := &dialer.Transport{}
	main := &fakeTransportSlot{
		link:      "vless://main",
		candidate: newFakeTransportCandidate(true),
		transport: mainTransport,
	}
	udp := &fakeTransportSlot{
		link:      "hysteria2://udp",
		candidate: newFakeTransportCandidate(true),
		transport: udpTransport,
	}
	var publications atomic.Int32
	publish := func(gotMain, gotUDP *dialer.Transport) {
		if gotMain != mainTransport || gotUDP != udpTransport {
			t.Fatalf("published generation = (%p, %p), want (%p, %p)", gotMain, gotUDP, mainTransport, udpTransport)
		}
		mainCommits, _ := main.counts()
		udpCommits, _ := udp.counts()
		if mainCommits != 1 || udpCommits != 1 {
			t.Fatalf("published before both slots activated: main=%d UDP=%d", mainCommits, udpCommits)
		}
		publications.Add(1)
	}
	set := newLiveTransportSet(
		newTransportGeneration(),
		main.descriptor(mainTransportSlot),
		slotPointer(udp.descriptor(udpTransportSlot)),
		publish,
	)

	if err := set.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := publications.Load(); got != 1 {
		t.Fatalf("generation publications = %d, want 1", got)
	}
}

func TestTransportSetMainFailureAbortsEveryCandidateAndPreservesSlots(t *testing.T) {
	mainCandidate := newFakeTransportCandidate(false)
	udpCandidate := newFakeTransportCandidate(true)
	mainErr := errors.New("main unavailable")
	main := &fakeTransportSlot{link: "vless://main", candidate: mainCandidate, err: mainErr}
	udp := &fakeTransportSlot{link: "hysteria2://udp", candidate: udpCandidate}
	set := newLiveTransportSet(newTransportGeneration(), main.descriptor(mainTransportSlot), slotPointer(udp.descriptor(udpTransportSlot)), nil)

	err := set.Recover(context.Background())
	if !errors.Is(err, mainErr) || transportErrorSlot(err) != mainTransportSlot {
		t.Fatalf("Recover error = %v, want wrapped main failure", err)
	}
	mainCommits, mainAborts := main.counts()
	udpCommits, udpAborts := udp.counts()
	if mainCommits != 0 || udpCommits != 0 {
		t.Fatalf("failed generation committed slots: main=%d UDP=%d", mainCommits, udpCommits)
	}
	if mainAborts != 1 || udpAborts != 1 || mainCandidate.stops.Load() != 1 || udpCandidate.stops.Load() != 1 {
		t.Fatalf("cleanup main abort/stop=%d/%d UDP=%d/%d, want all 1", mainAborts, mainCandidate.stops.Load(), udpAborts, udpCandidate.stops.Load())
	}
}

func TestTransportSetUDPFailureAbortsMainAndPreservesBothSlots(t *testing.T) {
	mainCandidate := newFakeTransportCandidate(true)
	udpCandidate := newFakeTransportCandidate(false)
	udpErr := errors.New("UDP unavailable")
	main := &fakeTransportSlot{link: "vless://main", candidate: mainCandidate}
	udp := &fakeTransportSlot{link: "hysteria2://udp", candidate: udpCandidate, err: udpErr}
	set := newLiveTransportSet(newTransportGeneration(), main.descriptor(mainTransportSlot), slotPointer(udp.descriptor(udpTransportSlot)), nil)

	err := set.Recover(context.Background())
	if !errors.Is(err, udpErr) || transportErrorSlot(err) != udpTransportSlot {
		t.Fatalf("Recover error = %v, want wrapped UDP failure", err)
	}
	mainCommits, mainAborts := main.counts()
	udpCommits, udpAborts := udp.counts()
	if mainCommits != 0 || udpCommits != 0 {
		t.Fatalf("UDP failure partially committed generation: main=%d UDP=%d", mainCommits, udpCommits)
	}
	if mainAborts != 1 || udpAborts != 1 {
		t.Fatalf("UDP failure cleanup main=%d UDP=%d, want both aborted", mainAborts, udpAborts)
	}
}

func TestTransportSetCancellationCleansAllCandidates(t *testing.T) {
	mainCandidate := newFakeTransportCandidate(true)
	udpCandidate := newFakeTransportCandidate(true)
	started := make(chan struct{}, 2)
	blockedPrepare := func(candidate transportCandidate) func(context.Context, string, string) (transportCandidate, error) {
		return func(ctx context.Context, _, _ string) (transportCandidate, error) {
			started <- struct{}{}
			<-ctx.Done()
			return candidate, ctx.Err()
		}
	}
	main := &fakeTransportSlot{link: "vless://main", prepare: blockedPrepare(mainCandidate)}
	udp := &fakeTransportSlot{link: "hysteria2://udp", prepare: blockedPrepare(udpCandidate)}
	set := newLiveTransportSet(newTransportGeneration(), main.descriptor(mainTransportSlot), slotPointer(udp.descriptor(udpTransportSlot)), nil)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- set.Recover(ctx) }()
	<-started
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Recover error = %v, want context canceled", err)
	}
	mainCommits, mainAborts := main.counts()
	udpCommits, udpAborts := udp.counts()
	if mainCommits != 0 || udpCommits != 0 || mainAborts != 1 || udpAborts != 1 {
		t.Fatalf("canceled generation main commit/abort=%d/%d UDP=%d/%d", mainCommits, mainAborts, udpCommits, udpAborts)
	}
}

func TestTransportSetSerializesSecondRecovery(t *testing.T) {
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	main := &fakeTransportSlot{link: "vless://main"}
	main.prepare = func(context.Context, string, string) (transportCandidate, error) {
		if calls.Add(1) == 1 {
			close(firstEntered)
			<-releaseFirst
		}
		return newFakeTransportCandidate(true), nil
	}
	generation := newTransportGeneration()
	set := newLiveTransportSet(generation, main.descriptor(mainTransportSlot), nil, nil)

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { firstDone <- set.Recover(context.Background()) }()
	<-firstEntered
	secondAtLockBoundary := make(chan struct{})
	var secondBoundaryOnce sync.Once
	generation.beforeLockTest = func() {
		secondBoundaryOnce.Do(func() { close(secondAtLockBoundary) })
	}
	go func() { secondDone <- set.Recover(context.Background()) }()
	<-secondAtLockBoundary
	if got := calls.Load(); got != 1 {
		t.Fatalf("prepare calls while first recovery holds lock = %d, want 1", got)
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Recover = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second Recover = %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("serialized prepare calls = %d, want 2", got)
	}
}

func TestTransportSetSerializesRecoveryAndShutdownWithoutPostTeardownPublication(t *testing.T) {
	generation := newTransportGeneration()
	oldMain := newFakeTransportCandidate(true)
	oldUDP := newFakeTransportCandidate(true)
	newMain := newFakeTransportCandidate(true)
	newUDP := newFakeTransportCandidate(true)
	mainEntered := make(chan struct{})
	releaseMain := make(chan struct{})
	var prepareCalls atomic.Int32
	var stateMu sync.Mutex
	currentMain := transportCandidate(oldMain)
	currentUDP := transportCandidate(oldUDP)

	main := transportSetSlot{
		name: mainTransportSlot,
		link: func() string { return "vless://main" },
		prepare: func(context.Context, string, string) (transportCandidate, error) {
			prepareCalls.Add(1)
			close(mainEntered)
			<-releaseMain
			return newMain, nil
		},
		commit: func(candidate transportCandidate, _ string) transportSlotCommit {
			stateMu.Lock()
			old := currentMain
			currentMain = candidate
			stateMu.Unlock()
			return transportSlotCommit{retired: old}
		},
		abort: func(candidate transportCandidate) { candidate.Stop() },
		stop: func() {
			stateMu.Lock()
			current := currentMain
			currentMain = nil
			stateMu.Unlock()
			if current != nil {
				current.Stop()
			}
		},
	}
	udp := transportSetSlot{
		name: udpTransportSlot,
		link: func() string { return "hysteria2://udp" },
		prepare: func(context.Context, string, string) (transportCandidate, error) {
			prepareCalls.Add(1)
			return newUDP, nil
		},
		commit: func(candidate transportCandidate, _ string) transportSlotCommit {
			stateMu.Lock()
			old := currentUDP
			currentUDP = candidate
			stateMu.Unlock()
			return transportSlotCommit{retired: old}
		},
		abort: func(candidate transportCandidate) { candidate.Stop() },
		stop: func() {
			stateMu.Lock()
			current := currentUDP
			currentUDP = nil
			stateMu.Unlock()
			if current != nil {
				current.Stop()
			}
		},
	}
	set := newLiveTransportSet(generation, main, slotPointer(udp), nil)

	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- set.Recover(context.Background()) }()
	<-mainEntered
	shutdownAtLockBoundary := make(chan struct{})
	var shutdownBoundaryOnce sync.Once
	generation.beforeLockTest = func() {
		shutdownBoundaryOnce.Do(func() { close(shutdownAtLockBoundary) })
	}
	shutdownDone := make(chan struct{})
	go func() {
		set.Stop()
		close(shutdownDone)
	}()
	<-shutdownAtLockBoundary
	select {
	case <-shutdownDone:
		t.Fatal("shutdown crossed the in-flight recovery generation")
	default:
	}

	close(releaseMain)
	if err := <-recoveryDone; err != nil {
		t.Fatalf("Recover = %v", err)
	}
	<-shutdownDone
	stateMu.Lock()
	gotMain, gotUDP := currentMain, currentUDP
	stateMu.Unlock()
	if gotMain != nil || gotUDP != nil {
		t.Fatalf("live slots after shutdown = main %v UDP %v, want nil", gotMain, gotUDP)
	}
	for name, candidate := range map[string]*fakeTransportCandidate{
		"old main": oldMain,
		"old UDP":  oldUDP,
		"new main": newMain,
		"new UDP":  newUDP,
	} {
		if got := candidate.stops.Load(); got != 1 {
			t.Errorf("%s stops = %d, want 1", name, got)
		}
	}

	err := set.Recover(context.Background())
	if !errors.Is(err, errTransportGenerationStopped) {
		t.Fatalf("post-shutdown Recover error = %v, want generation stopped", err)
	}
	if got := prepareCalls.Load(); got != 2 {
		t.Fatalf("post-shutdown recovery prepared candidates: calls=%d, want 2", got)
	}
}

func TestTransportSetSerializesRecoveryAgainstAutomaticFailoverSwap(t *testing.T) {
	generation := newTransportGeneration()
	recoveryEntered := make(chan struct{})
	releaseRecovery := make(chan struct{})
	main := &fakeTransportSlot{link: "vless://main"}
	main.prepare = func(context.Context, string, string) (transportCandidate, error) {
		close(recoveryEntered)
		<-releaseRecovery
		return newFakeTransportCandidate(true), nil
	}
	set := newLiveTransportSet(generation, main.descriptor(mainTransportSlot), nil, nil)
	buildErr := errors.New("failover candidate unavailable")
	failoverBuild := make(chan struct{})
	swapper := &transportSwapper{
		generation: generation,
		ctx:        context.Background(),
		build: func(string, string, bool) (*tunnel.Tunnel, error) {
			close(failoverBuild)
			return nil, buildErr
		},
	}

	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- set.Recover(context.Background()) }()
	<-recoveryEntered
	failoverAtLockBoundary := make(chan struct{})
	var failoverBoundaryOnce sync.Once
	generation.beforeLockTest = func() {
		failoverBoundaryOnce.Do(func() { close(failoverAtLockBoundary) })
	}
	failoverDone := make(chan error, 1)
	go func() {
		failoverDone <- swapper.swapTo("brook://backup")
	}()
	<-failoverAtLockBoundary
	select {
	case <-failoverBuild:
		t.Fatal("automatic failover prepared while recovery held the generation")
	default:
	}

	close(releaseRecovery)
	if err := <-recoveryDone; err != nil {
		t.Fatalf("Recover = %v", err)
	}
	<-failoverBuild
	if err := <-failoverDone; !errors.Is(err, buildErr) {
		t.Fatalf("failover swap error = %v, want %v", err, buildErr)
	}
}

func slotPointer(slot transportSetSlot) *transportSetSlot { return &slot }

func waitForTransportTest(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met before timeout")
		}
		time.Sleep(time.Millisecond)
	}
}
