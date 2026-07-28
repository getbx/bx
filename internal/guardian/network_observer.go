package guardian

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	networkObserverQuietWindow        = time.Second
	networkObserverGenerationInterval = time.Minute
	networkObserverRetryInitial       = 100 * time.Millisecond
	networkObserverRetryMax           = 5 * time.Second
)

type NetworkEventSource interface {
	Events(context.Context) (<-chan struct{}, error)
}

type UnderlayGenerationSource interface {
	Current(context.Context) (string, error)
}

type networkRecoveryRequester interface {
	RequestPathRecovery(RecoveryRequest) (RecoverySnapshot, error)
}

type NetworkObserver struct {
	events      NetworkEventSource
	generations UnderlayGenerationSource
	recoveries  networkRecoveryRequester
	config      networkObserverConfig
}

type networkObserverConfig struct {
	clock              networkObserverClock
	quietWindow        time.Duration
	generationInterval time.Duration
	retryInitial       time.Duration
	retryMax           time.Duration
}

type networkObserverClock interface {
	NewTimer(time.Duration) networkObserverTimer
}

type networkObserverTimer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(time.Duration) bool
}

func newNetworkObserver(events NetworkEventSource, generations UnderlayGenerationSource, recoveries networkRecoveryRequester, config networkObserverConfig) *NetworkObserver {
	if config.clock == nil {
		config.clock = realNetworkObserverClock{}
	}
	if config.quietWindow <= 0 {
		config.quietWindow = networkObserverQuietWindow
	}
	if config.generationInterval <= 0 {
		config.generationInterval = networkObserverGenerationInterval
	}
	if config.retryInitial <= 0 {
		config.retryInitial = networkObserverRetryInitial
	}
	if config.retryMax < config.retryInitial {
		config.retryMax = networkObserverRetryMax
	}
	return &NetworkObserver{
		events:      events,
		generations: generations,
		recoveries:  recoveries,
		config:      config,
	}
}

func (o *NetworkObserver) Run(ctx context.Context) {
	if o == nil || o.events == nil || o.generations == nil || o.recoveries == nil {
		return
	}

	generation, ok := o.initialGeneration(ctx)
	if !ok {
		return
	}

	checkTimer := o.config.clock.NewTimer(o.config.generationInterval)
	defer stopNetworkObserverTimer(checkTimer)

	var (
		events        <-chan struct{}
		debounceTimer networkObserverTimer
		debounceC     <-chan time.Time
		retryTimer    networkObserverTimer
		retryC        <-chan time.Time
		retryDelay    = o.config.retryInitial
	)
	defer func() {
		stopNetworkObserverTimer(debounceTimer)
		stopNetworkObserverTimer(retryTimer)
	}()

	subscribe := func() {
		next, err := o.events.Events(ctx)
		if err == nil && next != nil {
			events = next
			retryC = nil
			return
		}
		events = nil
		retryTimer = resetOrCreateNetworkObserverTimer(o.config.clock, retryTimer, retryDelay)
		retryC = retryTimer.C()
		retryDelay = nextNetworkObserverBackoff(retryDelay, o.config.retryMax)
	}
	subscribe()

	for {
		select {
		case <-ctx.Done():
			drainNetworkEventSource(events)
			return
		case _, open := <-events:
			if !open {
				events = nil
				if ctx.Err() != nil {
					return
				}
				retryTimer = resetOrCreateNetworkObserverTimer(o.config.clock, retryTimer, retryDelay)
				retryC = retryTimer.C()
				retryDelay = nextNetworkObserverBackoff(retryDelay, o.config.retryMax)
				continue
			}
			retryDelay = o.config.retryInitial
			debounceTimer = resetOrCreateNetworkObserverTimer(o.config.clock, debounceTimer, o.config.quietWindow)
			debounceC = debounceTimer.C()
		case <-debounceC:
			debounceC = nil
			o.checkGeneration(ctx, &generation)
		case <-checkTimer.C():
			if debounceC == nil {
				o.checkGeneration(ctx, &generation)
			}
			resetNetworkObserverTimer(checkTimer, o.config.generationInterval)
		case <-retryC:
			retryC = nil
			subscribe()
		}
	}
}

func (o *NetworkObserver) initialGeneration(ctx context.Context) (string, bool) {
	delay := o.config.retryInitial
	var retryTimer networkObserverTimer
	defer stopNetworkObserverTimer(retryTimer)
	for {
		generation, err := o.generations.Current(ctx)
		generation = strings.TrimSpace(generation)
		if err == nil && generation != "" {
			return generation, true
		}
		retryTimer = resetOrCreateNetworkObserverTimer(o.config.clock, retryTimer, delay)
		select {
		case <-ctx.Done():
			return "", false
		case <-retryTimer.C():
		}
		delay = nextNetworkObserverBackoff(delay, o.config.retryMax)
	}
}

func (o *NetworkObserver) checkGeneration(ctx context.Context, previous *string) {
	current, err := o.generations.Current(ctx)
	current = strings.TrimSpace(current)
	if err != nil || current == "" || current == *previous {
		return
	}
	_, err = o.recoveries.RequestPathRecovery(RecoveryRequest{
		Reason:     "underlay_changed",
		Generation: current,
	})
	if err == nil {
		*previous = current
	}
}

func nextNetworkObserverBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum {
		return maximum
	}
	next := current * 2
	if next < current || next > maximum {
		return maximum
	}
	return next
}

func resetOrCreateNetworkObserverTimer(clock networkObserverClock, timer networkObserverTimer, delay time.Duration) networkObserverTimer {
	if timer == nil {
		return clock.NewTimer(delay)
	}
	resetNetworkObserverTimer(timer, delay)
	return timer
}

func resetNetworkObserverTimer(timer networkObserverTimer, delay time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C():
		default:
		}
	}
	timer.Reset(delay)
}

func stopNetworkObserverTimer(timer networkObserverTimer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C():
	default:
	}
}

func drainNetworkEventSource(events <-chan struct{}) {
	if events == nil {
		return
	}
	for range events {
	}
}

type realNetworkObserverClock struct{}

func (realNetworkObserverClock) NewTimer(delay time.Duration) networkObserverTimer {
	return &realNetworkObserverTimer{timer: time.NewTimer(delay)}
}

type realNetworkObserverTimer struct {
	timer *time.Timer
}

func (t *realNetworkObserverTimer) C() <-chan time.Time {
	return t.timer.C
}

func (t *realNetworkObserverTimer) Stop() bool {
	return t.timer.Stop()
}

func (t *realNetworkObserverTimer) Reset(delay time.Duration) bool {
	return t.timer.Reset(delay)
}

var errNetworkObserverUnsupported = errors.New("network observation unsupported")
