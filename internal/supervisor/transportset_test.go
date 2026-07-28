package supervisor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
		commit: func(candidate transportCandidate, _ string) transportCandidate {
			s.mu.Lock()
			s.commits = append(s.commits, candidate)
			s.mu.Unlock()
			return s.retired
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
	set := newLiveTransportSet(&sync.Mutex{}, main.descriptor(mainTransportSlot), nil)

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
	set := newLiveTransportSet(&sync.Mutex{}, main.descriptor(mainTransportSlot), slotPointer(udp.descriptor(udpTransportSlot)))

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
	set := newLiveTransportSet(&sync.Mutex{}, main.descriptor(mainTransportSlot), slotPointer(udp.descriptor(udpTransportSlot)))

	if err := set.Recover(context.Background()); err != nil {
		t.Fatalf("Recover = %v", err)
	}
	if main.retired.(*fakeTransportCandidate).stops.Load() != 1 || udp.retired.(*fakeTransportCandidate).stops.Load() != 1 {
		t.Fatal("old candidates were not retired exactly once")
	}
}

func TestTransportSetMainFailureAbortsEveryCandidateAndPreservesSlots(t *testing.T) {
	mainCandidate := newFakeTransportCandidate(false)
	udpCandidate := newFakeTransportCandidate(true)
	mainErr := errors.New("main unavailable")
	main := &fakeTransportSlot{link: "vless://main", candidate: mainCandidate, err: mainErr}
	udp := &fakeTransportSlot{link: "hysteria2://udp", candidate: udpCandidate}
	set := newLiveTransportSet(&sync.Mutex{}, main.descriptor(mainTransportSlot), slotPointer(udp.descriptor(udpTransportSlot)))

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
	set := newLiveTransportSet(&sync.Mutex{}, main.descriptor(mainTransportSlot), slotPointer(udp.descriptor(udpTransportSlot)))

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
	set := newLiveTransportSet(&sync.Mutex{}, main.descriptor(mainTransportSlot), slotPointer(udp.descriptor(udpTransportSlot)))
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
	set := newLiveTransportSet(&sync.Mutex{}, main.descriptor(mainTransportSlot), nil)

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { firstDone <- set.Recover(context.Background()) }()
	<-firstEntered
	go func() { secondDone <- set.Recover(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
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
