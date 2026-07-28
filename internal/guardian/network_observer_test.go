package guardian

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestNetworkObserverDebouncesBurstsAndChecksOpaqueGeneration(t *testing.T) {
	clock := newFakeNetworkObserverClock()
	events := make(chan struct{})
	source := &fakeNetworkEventSource{events: events}
	generations := &fakeUnderlayGenerationSource{generation: "wifi-a"}
	recoveries := &recordingNetworkRecoveries{}
	observer := newNetworkObserver(source, generations, recoveries, networkObserverConfig{
		clock:              clock,
		quietWindow:        time.Second,
		generationInterval: time.Minute,
		retryInitial:       100 * time.Millisecond,
		retryMax:           5 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		observer.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		waitForNetworkObserverDone(t, done)
	})

	generations.waitForCalls(t, 1)
	source.waitForCalls(t, 1)

	events <- struct{}{}
	clock.waitForReset(t, time.Second, 1)
	clock.Advance(750 * time.Millisecond)
	events <- struct{}{}
	clock.waitForReset(t, time.Second, 2)
	generations.set("wifi-b")
	clock.Advance(999 * time.Millisecond)
	recoveries.assertCount(t, 0)
	clock.Advance(time.Millisecond)

	first := recoveries.waitForCount(t, 1)[0]
	if first != (RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-b"}) {
		t.Fatalf("debounced recovery = %+v", first)
	}

	events <- struct{}{}
	clock.waitForReset(t, time.Second, 3)
	clock.Advance(time.Second)
	generations.waitForCalls(t, 3)
	recoveries.assertCount(t, 1)

	generations.set("wifi-c")
	clock.Advance(time.Minute)
	periodic := recoveries.waitForCount(t, 2)[1]
	if periodic.Generation != "wifi-c" || periodic.Reason != "underlay_changed" {
		t.Fatalf("periodic recovery = %+v", periodic)
	}
}

func TestNetworkObserverSubmitsNewGenerationWhileRecoveryIsActive(t *testing.T) {
	clock := newFakeNetworkObserverClock()
	events := make(chan struct{})
	generations := &fakeUnderlayGenerationSource{generation: "wifi-a"}
	recoveries := &coalescingNetworkRecoveries{}
	observer := newNetworkObserver(&fakeNetworkEventSource{events: events}, generations, recoveries, networkObserverConfig{
		clock:              clock,
		quietWindow:        time.Second,
		generationInterval: time.Minute,
		retryInitial:       100 * time.Millisecond,
		retryMax:           5 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		observer.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		waitForNetworkObserverDone(t, done)
	})
	generations.waitForCalls(t, 1)

	generations.set("wifi-b")
	events <- struct{}{}
	clock.waitForReset(t, time.Second, 1)
	clock.Advance(time.Second)
	recoveries.waitForActive(t, "wifi-b")

	generations.set("wifi-c")
	events <- struct{}{}
	clock.waitForReset(t, time.Second, 2)
	clock.Advance(time.Second)
	recoveries.waitForPending(t, "wifi-c")
	if got := recoveries.pendingCount(); got != 1 {
		t.Fatalf("pending recoveries = %d, want 1 newest generation", got)
	}
}

func TestNetworkObserverCancellationDrainsEventSourcePromptly(t *testing.T) {
	clock := newFakeNetworkObserverClock()
	source := &cancelClosingNetworkEventSource{started: make(chan struct{})}
	observer := newNetworkObserver(source, &fakeUnderlayGenerationSource{generation: "wifi-a"}, &recordingNetworkRecoveries{}, networkObserverConfig{
		clock:              clock,
		quietWindow:        time.Second,
		generationInterval: time.Minute,
		retryInitial:       100 * time.Millisecond,
		retryMax:           5 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		observer.Run(ctx)
		close(done)
	}()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("event source did not start")
	}

	cancel()
	waitForNetworkObserverDone(t, done)
}

func TestNetworkObserverRetriesEventSourceWithBoundedBackoff(t *testing.T) {
	clock := newFakeNetworkObserverClock()
	source := &fakeNetworkEventSource{
		events:       make(chan struct{}),
		failuresLeft: 9,
		err:          errors.New("route socket unavailable"),
	}
	observer := newNetworkObserver(source, &fakeUnderlayGenerationSource{generation: "wifi-a"}, &recordingNetworkRecoveries{}, networkObserverConfig{
		clock:              clock,
		quietWindow:        time.Second,
		generationInterval: time.Minute,
		retryInitial:       100 * time.Millisecond,
		retryMax:           5 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		observer.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		waitForNetworkObserverDone(t, done)
	})

	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1600 * time.Millisecond,
		3200 * time.Millisecond,
		5 * time.Second,
		5 * time.Second,
		5 * time.Second,
	}
	seen := make(map[time.Duration]int)
	for i, delay := range want {
		source.waitForCalls(t, i+1)
		seen[delay]++
		clock.waitForReset(t, delay, seen[delay])
		clock.Advance(delay)
	}
	source.waitForCalls(t, len(want)+1)
	clock.assertTimerCount(t, 2)
}

func waitForNetworkObserverDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("network observer did not stop")
	}
}

type fakeNetworkEventSource struct {
	mu           sync.Mutex
	events       <-chan struct{}
	err          error
	failuresLeft int
	calls        int
}

func (s *fakeNetworkEventSource) Events(ctx context.Context) (<-chan struct{}, error) {
	s.mu.Lock()
	s.calls++
	if s.failuresLeft > 0 {
		s.failuresLeft--
		s.mu.Unlock()
		return nil, s.err
	}
	source := s.events
	s.mu.Unlock()

	events := make(chan struct{})
	go func() {
		defer close(events)
		for {
			select {
			case <-ctx.Done():
				return
			case signal, open := <-source:
				if !open {
					return
				}
				select {
				case events <- signal:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return events, nil
}

func (s *fakeNetworkEventSource) waitForCalls(t *testing.T, want int) {
	t.Helper()
	waitForNetworkObserverCondition(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.calls >= want
	}, "event source calls")
}

type cancelClosingNetworkEventSource struct {
	started chan struct{}
	once    sync.Once
}

func (s *cancelClosingNetworkEventSource) Events(ctx context.Context) (<-chan struct{}, error) {
	events := make(chan struct{})
	s.once.Do(func() { close(s.started) })
	go func() {
		<-ctx.Done()
		close(events)
	}()
	return events, nil
}

type fakeUnderlayGenerationSource struct {
	mu         sync.Mutex
	generation string
	calls      int
}

func (s *fakeUnderlayGenerationSource) Current(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.generation, nil
}

func (s *fakeUnderlayGenerationSource) set(generation string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation = generation
}

func (s *fakeUnderlayGenerationSource) waitForCalls(t *testing.T, want int) {
	t.Helper()
	waitForNetworkObserverCondition(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.calls >= want
	}, "generation source calls")
}

type recordingNetworkRecoveries struct {
	mu       sync.Mutex
	requests []RecoveryRequest
}

func (r *recordingNetworkRecoveries) RequestPathRecovery(request RecoveryRequest) (RecoverySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
	return RecoverySnapshot{State: "accepted", Generation: request.Generation}, nil
}

func (r *recordingNetworkRecoveries) waitForCount(t *testing.T, want int) []RecoveryRequest {
	t.Helper()
	waitForNetworkObserverCondition(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.requests) >= want
	}, "recovery request count")
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RecoveryRequest(nil), r.requests...)
}

func (r *recordingNetworkRecoveries) assertCount(t *testing.T, want int) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) != want {
		t.Fatalf("recovery requests = %d, want %d: %+v", len(r.requests), want, r.requests)
	}
}

type coalescingNetworkRecoveries struct {
	mu      sync.Mutex
	active  *RecoveryRequest
	pending *RecoveryRequest
}

func (r *coalescingNetworkRecoveries) RequestPathRecovery(request RecoveryRequest) (RecoverySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		copy := request
		r.active = &copy
	} else {
		copy := request
		r.pending = &copy
	}
	return RecoverySnapshot{State: "accepted", Generation: request.Generation}, nil
}

func (r *coalescingNetworkRecoveries) waitForActive(t *testing.T, generation string) {
	t.Helper()
	waitForNetworkObserverCondition(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.active != nil && r.active.Generation == generation
	}, "active recovery")
}

func (r *coalescingNetworkRecoveries) waitForPending(t *testing.T, generation string) {
	t.Helper()
	waitForNetworkObserverCondition(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.pending != nil && r.pending.Generation == generation
	}, "pending recovery")
}

func (r *coalescingNetworkRecoveries) pendingCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil {
		return 0
	}
	return 1
}

type fakeNetworkObserverClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*fakeNetworkObserverTimer]struct{}
	resets map[time.Duration]int
}

func newFakeNetworkObserverClock() *fakeNetworkObserverClock {
	return &fakeNetworkObserverClock{
		now:    time.Unix(0, 0),
		timers: make(map[*fakeNetworkObserverTimer]struct{}),
		resets: make(map[time.Duration]int),
	}
}

func (c *fakeNetworkObserverClock) NewTimer(delay time.Duration) networkObserverTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeNetworkObserverTimer{
		clock:    c,
		channel:  make(chan time.Time, 1),
		deadline: c.now.Add(delay),
		active:   true,
	}
	c.timers[timer] = struct{}{}
	c.resets[delay]++
	return timer
}

func (c *fakeNetworkObserverClock) Advance(delay time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	now := c.now
	var due []*fakeNetworkObserverTimer
	for timer := range c.timers {
		if timer.active && !timer.deadline.After(now) {
			timer.active = false
			due = append(due, timer)
		}
	}
	c.mu.Unlock()
	for _, timer := range due {
		timer.channel <- now
	}
}

func (c *fakeNetworkObserverClock) waitForReset(t *testing.T, delay time.Duration, want int) {
	t.Helper()
	waitForNetworkObserverCondition(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.resets[delay] >= want
	}, "timer reset")
}

func (c *fakeNetworkObserverClock) assertTimerCount(t *testing.T, want int) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.timers) != want {
		t.Fatalf("observer timers = %d, want %d reusable timers", len(c.timers), want)
	}
}

type fakeNetworkObserverTimer struct {
	clock    *fakeNetworkObserverClock
	channel  chan time.Time
	deadline time.Time
	active   bool
}

func (t *fakeNetworkObserverTimer) C() <-chan time.Time {
	return t.channel
}

func (t *fakeNetworkObserverTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.active = false
	return wasActive
}

func (t *fakeNetworkObserverTimer) Reset(delay time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.deadline = t.clock.now.Add(delay)
	t.active = true
	t.clock.resets[delay]++
	return wasActive
}

func waitForNetworkObserverCondition(t *testing.T, condition func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}
