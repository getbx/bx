package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
)

type transportCandidate interface {
	Healthy() bool
	Stop()
}

type transportSet interface {
	Recover(context.Context) error
}

var errTransportGenerationStopped = errors.New("transport generation stopped")

type transportGeneration struct {
	mu      sync.Mutex
	stopped bool
}

func newTransportGeneration() *transportGeneration {
	return &transportGeneration{}
}

func (g *transportGeneration) run(operation func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return errTransportGenerationStopped
	}
	return operation()
}

func (g *transportGeneration) stop(teardown func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return
	}
	g.stopped = true
	teardown()
}

type transportSlotName string

const (
	mainTransportSlot transportSlotName = "main"
	udpTransportSlot  transportSlotName = "udp"
)

type transportSetError struct {
	Slot transportSlotName
	Err  error
}

func (e *transportSetError) Error() string {
	if e == nil {
		return "transport recovery failed"
	}
	return fmt.Sprintf("%s transport recovery failed: %v", e.Slot, e.Err)
}

func (e *transportSetError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func transportErrorSlot(err error) transportSlotName {
	var setErr *transportSetError
	if errors.As(err, &setErr) && setErr != nil {
		return setErr.Slot
	}
	return ""
}

type transportSetSlot struct {
	name    transportSlotName
	link    func() string
	prepare func(context.Context, string, string) (transportCandidate, error)
	commit  func(transportCandidate, string) transportCandidate
	abort   func(transportCandidate)
	stop    func()
}

type liveTransportSet struct {
	generation *transportGeneration
	main       transportSetSlot
	udp        *transportSetSlot
	sequence   atomic.Uint64
}

func newLiveTransportSet(generation *transportGeneration, main transportSetSlot, udp *transportSetSlot) *liveTransportSet {
	if generation == nil {
		generation = newTransportGeneration()
	}
	return &liveTransportSet{generation: generation, main: main, udp: udp}
}

type preparedTransportResult struct {
	slot      transportSetSlot
	link      string
	candidate transportCandidate
	err       error
}

func (s *liveTransportSet) Recover(ctx context.Context) error {
	return s.generation.run(func() error {
		return s.recover(ctx)
	})
}

func (s *liveTransportSet) recover(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	recoveryID := "recovery-" + strconv.FormatUint(s.sequence.Add(1), 10)
	slots := []transportSetSlot{s.main}
	if s.udp != nil {
		slots = append(slots, *s.udp)
	}

	results := make(chan preparedTransportResult, len(slots))
	for _, slot := range slots {
		slot := slot
		go func() {
			link := slot.link()
			candidate, err := slot.prepare(ctx, link, recoveryID+"-"+string(slot.name))
			results <- preparedTransportResult{slot: slot, link: link, candidate: candidate, err: err}
		}()
	}

	bySlot := make(map[transportSlotName]preparedTransportResult, len(slots))
	for range slots {
		result := <-results
		bySlot[result.slot.name] = result
	}
	prepared := make([]preparedTransportResult, 0, len(slots))
	for _, slot := range slots {
		prepared = append(prepared, bySlot[slot.name])
	}

	var mainErr, udpErr error
	for i := range prepared {
		result := &prepared[i]
		if result.err == nil && result.candidate == nil {
			result.err = errors.New("candidate was not created")
		}
		if result.err == nil && !result.candidate.Healthy() {
			result.err = errors.New("candidate became unhealthy before commit")
		}
		switch result.slot.name {
		case mainTransportSlot:
			mainErr = result.err
		case udpTransportSlot:
			udpErr = result.err
		}
	}
	if ctx.Err() != nil || mainErr != nil || udpErr != nil {
		for _, result := range prepared {
			if result.candidate != nil {
				result.slot.abort(result.candidate)
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if mainErr != nil {
			return &transportSetError{Slot: mainTransportSlot, Err: mainErr}
		}
		return &transportSetError{Slot: udpTransportSlot, Err: udpErr}
	}

	retired := make([]transportCandidate, 0, len(prepared))
	for _, result := range prepared {
		if old := result.slot.commit(result.candidate, result.link); old != nil {
			retired = append(retired, old)
		}
	}
	for _, old := range retired {
		old.Stop()
	}
	return nil
}

func (s *liveTransportSet) Stop() {
	s.generation.stop(func() {
		if s.udp != nil && s.udp.stop != nil {
			s.udp.stop()
		}
		if s.main.stop != nil {
			s.main.stop()
		}
	})
}

var _ transportSet = (*liveTransportSet)(nil)
