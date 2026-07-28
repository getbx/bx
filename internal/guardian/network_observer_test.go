package guardian

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/supervisor"
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

func TestNetworkObserverAndManagerRetryAcceptedGenerationAfterAsyncFailure(t *testing.T) {
	clock := newFakeNetworkObserverClock()
	events := make(chan struct{})
	generations := &fakeUnderlayGenerationSource{generation: "wifi-a"}
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	retries := newControlledPathRecoveryRetries()
	env.manager.pathRecoveryRetryWait = retries.Wait
	observer := newNetworkObserver(&fakeNetworkEventSource{events: events}, generations, env.manager, networkObserverConfig{
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
		env.manager.beginPathRecoveryShutdown()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
		defer waitCancel()
		if err := env.manager.waitForPathRecoveries(waitCtx); err != nil {
			t.Errorf("drain path recovery: %v", err)
		}
	})
	generations.waitForCalls(t, 1)

	generations.set("wifi-b")
	events <- struct{}{}
	clock.waitForReset(t, time.Second, 1)
	clock.Advance(time.Second)
	if got := core.waitForRequest(t); got.Generation != "wifi-b" {
		t.Fatalf("first Core request = %+v, want wifi-b", got)
	}
	first := env.manager.CurrentPathRecovery()
	core.release(corePathResult{
		snapshot: supervisor.PathRecoverySnapshot{State: "blocked", Stage: "observe", ErrorCode: "network_unavailable"},
		err:      &supervisor.PathRecoveryError{Code: "network_unavailable", Detail: "default route missing: secret interface detail"},
	})

	retry := retries.Next(t)
	if retry.delay != 100*time.Millisecond {
		t.Fatalf("first retry delay = %s, want 100ms", retry.delay)
	}
	queued := env.manager.CurrentPathRecovery()
	if queued.ID != first.ID || queued.Generation != "wifi-b" || queued.State != "accepted" || queued.Stage != "queued" ||
		queued.Attempt != 2 || queued.ErrorCode != "network_unavailable" || queued.Detail != "" {
		t.Fatalf("queued retry = %+v, want same redacted transaction at attempt 2", queued)
	}

	retry.Release()
	if got := core.waitForRequest(t); got.Generation != "wifi-b" {
		t.Fatalf("retried Core request = %+v, want wifi-b", got)
	}
	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{
		State: "succeeded", Stage: "succeeded", Attempt: 1,
	}})
	eventually(t, func() bool {
		current := env.manager.CurrentPathRecovery()
		return current.ID == first.ID && current.State == "succeeded"
	})
	if got := env.manager.CurrentPathRecovery(); got.Attempt != 2 || got.ErrorCode != "" || got.Detail != "" {
		t.Fatalf("completed retry = %+v, want redacted attempt 2 success", got)
	}
	if got := core.callCount(); got != 2 {
		t.Fatalf("Core calls = %d, want failed attempt plus retry", got)
	}
}

func TestNetworkObserverAndManagerDoesNotRetryPermanentUnderlayUnavailable(t *testing.T) {
	clock := newFakeNetworkObserverClock()
	events := make(chan struct{})
	generations := &fakeUnderlayGenerationSource{generation: "wifi-a"}
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	retries := newControlledPathRecoveryRetries()
	env.manager.pathRecoveryRetryWait = retries.Wait
	observer := newNetworkObserver(&fakeNetworkEventSource{events: events}, generations, env.manager, networkObserverConfig{
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
		env.manager.beginPathRecoveryShutdown()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
		defer waitCancel()
		if err := env.manager.waitForPathRecoveries(waitCtx); err != nil {
			t.Errorf("drain path recovery: %v", err)
		}
	})
	generations.waitForCalls(t, 1)

	generations.set("wifi-b")
	events <- struct{}{}
	clock.waitForReset(t, time.Second, 1)
	clock.Advance(time.Second)
	if got := core.waitForRequest(t); got.Generation != "wifi-b" {
		t.Fatalf("Core request = %+v, want wifi-b", got)
	}
	first := env.manager.CurrentPathRecovery()
	core.release(corePathResult{
		snapshot: supervisor.PathRecoverySnapshot{State: "blocked", Stage: "observe", ErrorCode: "underlay_unavailable"},
		err:      &supervisor.PathRecoveryError{Code: "underlay_unavailable", Detail: "unsupported platform wiring: secret"},
	})

	eventually(t, func() bool {
		current := env.manager.CurrentPathRecovery()
		return current.ID == first.ID && current.State == "failed" && env.manager.pathRecoveryActiveCount() == 0
	})
	current := env.manager.CurrentPathRecovery()
	if current.Generation != "wifi-b" || current.Stage != "observe" ||
		current.ErrorCode != "underlay_unavailable" || current.Attempt != 1 || current.Detail != "" {
		t.Fatalf("terminal recovery = %+v, want redacted permanent failure at attempt 1", current)
	}
	if got := core.callCount(); got != 1 {
		t.Fatalf("Core calls = %d, want one terminal attempt", got)
	}
	if got := retries.Count(); got != 0 {
		t.Fatalf("scheduled retries = %d, want none for permanent underlay failure", got)
	}
}

func TestManagerPathRecoveryRetryBackoffCapsAndNewGenerationSupersedesWait(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	retries := newControlledPathRecoveryRetries()
	env.manager.pathRecoveryRetryWait = retries.Wait

	first, err := env.manager.RequestPathRecovery(RecoveryRequest{
		Reason:     "underlay_changed",
		Generation: "wifi-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantDelays := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1600 * time.Millisecond,
		3200 * time.Millisecond,
		5 * time.Second,
		5 * time.Second,
	}
	for attempt, wantDelay := range wantDelays {
		if got := core.waitForRequest(t); got.Generation != "wifi-a" {
			t.Fatalf("Core request %d = %+v, want wifi-a", attempt+1, got)
		}
		core.release(corePathResult{
			snapshot: supervisor.PathRecoverySnapshot{State: "blocked", Stage: "transport_health", ErrorCode: "transport_unavailable"},
			err:      &supervisor.PathRecoveryError{Code: "transport_unavailable", Detail: "secret transport failure"},
		})
		retry := retries.Next(t)
		if retry.delay != wantDelay {
			t.Fatalf("retry %d delay = %s, want %s", attempt+1, retry.delay, wantDelay)
		}
		if attempt != len(wantDelays)-1 {
			retry.Release()
		}
	}

	waiting := retries.Last()
	newer, err := env.manager.RequestPathRecovery(RecoveryRequest{
		Reason:     "underlay_changed",
		Generation: "wifi-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting.WaitCanceled(t)
	if newer.ID == first.ID {
		t.Fatalf("newer generation reused retrying transaction ID %q", first.ID)
	}
	if got := core.waitForRequest(t); got.Generation != "wifi-b" {
		t.Fatalf("superseding Core request = %+v, want wifi-b", got)
	}
	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
	eventually(t, func() bool {
		current := env.manager.CurrentPathRecovery()
		return current.ID == newer.ID && current.State == "succeeded"
	})
	if got := core.callCount(); got != len(wantDelays)+1 {
		t.Fatalf("Core calls = %d, want %d capped retries plus superseding generation", got, len(wantDelays)+1)
	}
}

func TestManagerPathRecoveryRetryWaitRespectsOffShutdownAndLifecycleFences(t *testing.T) {
	newRetryingRecovery := func(t *testing.T) (*managerTestEnv, *fakeCorePathClient, *controlledPathRecoveryRetries, *controlledPathRecoveryRetry, RecoverySnapshot) {
		t.Helper()
		env := newProtectedManagerTestEnv(t)
		core := newFakeCorePathClient(false)
		env.manager.corePath = core
		retries := newControlledPathRecoveryRetries()
		env.manager.pathRecoveryRetryWait = retries.Wait
		accepted, err := env.manager.RequestPathRecovery(RecoveryRequest{
			Reason:     "underlay_changed",
			Generation: "wifi-b",
		})
		if err != nil {
			t.Fatal(err)
		}
		core.waitForRequest(t)
		core.release(corePathResult{
			snapshot: supervisor.PathRecoverySnapshot{State: "blocked", Stage: "observe", ErrorCode: "network_unavailable"},
			err:      &supervisor.PathRecoveryError{Code: "network_unavailable"},
		})
		return env, core, retries, retries.Next(t), accepted
	}

	t.Run("successful Down resolves retry as Off", func(t *testing.T) {
		env, core, _, retry, accepted := newRetryingRecovery(t)
		if err := env.manager.Down(context.Background()); err != nil {
			t.Fatal(err)
		}
		retry.WaitCanceled(t)
		eventually(t, func() bool { return env.manager.pathRecoveryActiveCount() == 0 })
		current := env.manager.CurrentPathRecovery()
		if current.ID != accepted.ID || current.State != "ignored" || current.Stage != "off" {
			t.Fatalf("recovery after successful Down = %+v, want %q ignored/off", current, accepted.ID)
		}
		if got := core.callCount(); got != 1 {
			t.Fatalf("Core calls after successful Down = %d, want failed attempt only", got)
		}
	})

	t.Run("shutdown cancels retry and drains", func(t *testing.T) {
		env, core, _, retry, _ := newRetryingRecovery(t)
		env.manager.beginPathRecoveryShutdown()
		retry.WaitCanceled(t)
		waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := env.manager.waitForPathRecoveries(waitCtx); err != nil {
			t.Fatal(err)
		}
		if got := core.callCount(); got != 1 {
			t.Fatalf("Core calls after shutdown = %d, want failed attempt only", got)
		}
	})

	t.Run("On-preserving update preserves backoff and replays after fence", func(t *testing.T) {
		env, core, retries, retry, accepted := newRetryingRecovery(t)
		env.manager.updates = nil
		_, err := env.manager.Update(context.Background(), unavailablePathRecoveryUpdateRequest("tx-retry-fence"))
		if err == nil || err.Error() != "update_unavailable" {
			t.Fatalf("Update error = %v, want update_unavailable", err)
		}
		retry.WaitCanceled(t)
		if got := core.waitForRequest(t); got.Generation != "wifi-b" {
			t.Fatalf("replayed Core request = %+v, want wifi-b", got)
		}
		core.release(corePathResult{
			snapshot: supervisor.PathRecoverySnapshot{State: "blocked", Stage: "observe", ErrorCode: "network_unavailable"},
			err:      &supervisor.PathRecoveryError{Code: "network_unavailable"},
		})
		nextRetry := retries.Next(t)
		if nextRetry.delay != 200*time.Millisecond {
			t.Fatalf("retry delay after lifecycle fence = %s, want preserved 200ms backoff", nextRetry.delay)
		}
		nextRetry.Release()
		if got := core.waitForRequest(t); got.Generation != "wifi-b" {
			t.Fatalf("post-fence retry Core request = %+v, want wifi-b", got)
		}
		core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
		eventually(t, func() bool {
			current := env.manager.CurrentPathRecovery()
			return current.ID == accepted.ID && current.State == "succeeded"
		})
		if got := core.callCount(); got != 3 {
			t.Fatalf("Core calls after update fence = %d, want failed attempt, replay, and retry", got)
		}
	})
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

func TestNetworkObserverClosedChannelsBackOffAndStableEventResetsDelay(t *testing.T) {
	clock := newFakeNetworkObserverClock()
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1600 * time.Millisecond,
		3200 * time.Millisecond,
		5 * time.Second,
		5 * time.Second,
	}
	sources := make([]<-chan struct{}, 0, len(want)+1)
	for range want {
		closed := make(chan struct{})
		close(closed)
		sources = append(sources, closed)
	}
	stable := make(chan struct{})
	sources = append(sources, stable)
	source := &sequenceNetworkEventSource{
		sources: sources,
		calls:   make(chan int, len(sources)),
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

	if got := <-source.calls; got != 1 {
		t.Fatalf("first subscription call = %d", got)
	}
	seen := make(map[time.Duration]int)
	for i, delay := range want {
		seen[delay]++
		clock.waitForReset(t, delay, seen[delay])
		clock.Advance(delay)
		if got := <-source.calls; got != i+2 {
			t.Fatalf("subscription call after %s = %d, want %d", delay, got, i+2)
		}
	}

	select {
	case stable <- struct{}{}:
	case <-time.After(time.Second):
		t.Fatal("stable source did not accept an event")
	}
	clock.waitForReset(t, time.Second, 1)
	close(stable)
	clock.waitForReset(t, 100*time.Millisecond, seen[100*time.Millisecond]+1)
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

type sequenceNetworkEventSource struct {
	mu      sync.Mutex
	sources []<-chan struct{}
	calls   chan int
	count   int
}

func (s *sequenceNetworkEventSource) Events(ctx context.Context) (<-chan struct{}, error) {
	s.mu.Lock()
	if len(s.sources) == 0 {
		s.mu.Unlock()
		return nil, errors.New("unexpected subscription")
	}
	s.count++
	s.calls <- s.count
	source := s.sources[0]
	s.sources = s.sources[1:]
	s.mu.Unlock()

	events := make(chan struct{})
	go func() {
		defer close(events)
		for {
			select {
			case <-ctx.Done():
				return
			case event, open := <-source:
				if !open {
					return
				}
				select {
				case events <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return events, nil
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

type controlledPathRecoveryRetries struct {
	mu      sync.Mutex
	started chan *controlledPathRecoveryRetry
	waits   []*controlledPathRecoveryRetry
}

type controlledPathRecoveryRetry struct {
	delay      time.Duration
	release    chan struct{}
	canceled   chan struct{}
	releaseOne sync.Once
	cancelOne  sync.Once
}

func newControlledPathRecoveryRetries() *controlledPathRecoveryRetries {
	return &controlledPathRecoveryRetries{
		started: make(chan *controlledPathRecoveryRetry, 16),
	}
}

func (r *controlledPathRecoveryRetries) Wait(ctx context.Context, delay time.Duration) error {
	wait := &controlledPathRecoveryRetry{
		delay:    delay,
		release:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	r.mu.Lock()
	r.waits = append(r.waits, wait)
	r.mu.Unlock()
	r.started <- wait
	select {
	case <-wait.release:
		return nil
	case <-ctx.Done():
		wait.cancelOne.Do(func() { close(wait.canceled) })
		return ctx.Err()
	}
}

func (r *controlledPathRecoveryRetries) Next(t *testing.T) *controlledPathRecoveryRetry {
	t.Helper()
	select {
	case wait := <-r.started:
		return wait
	case <-time.After(time.Second):
		t.Fatal("path recovery retry was not scheduled")
		return nil
	}
}

func (r *controlledPathRecoveryRetries) Last() *controlledPathRecoveryRetry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.waits[len(r.waits)-1]
}

func (r *controlledPathRecoveryRetries) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.waits)
}

func (r *controlledPathRecoveryRetry) Release() {
	r.releaseOne.Do(func() { close(r.release) })
}

func (r *controlledPathRecoveryRetry) WaitCanceled(t *testing.T) {
	t.Helper()
	select {
	case <-r.canceled:
	case <-time.After(time.Second):
		t.Fatal("path recovery retry wait was not canceled")
	}
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
