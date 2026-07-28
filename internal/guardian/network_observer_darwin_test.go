//go:build darwin

package guardian

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

func TestNetworkObserverDarwinRouteSocketOpenSetsCloseOnExecUnderForkLock(t *testing.T) {
	var order []string
	lock := &recordingDarwinRouteSocketLock{order: &order}
	var domain, socketType, protocol int
	ops := darwinRouteSocketOps{
		forkLock: lock,
		socket: func(gotDomain, gotType, gotProtocol int) (int, error) {
			order = append(order, "socket")
			domain, socketType, protocol = gotDomain, gotType, gotProtocol
			return 42, nil
		},
		closeOnExec: func(fd int) error {
			order = append(order, "close_on_exec")
			if fd != 42 {
				t.Fatalf("close-on-exec fd = %d, want 42", fd)
			}
			return nil
		},
		close: func(int) error { return nil },
	}

	fd, err := openDarwinRouteSocket(ops)
	if err != nil {
		t.Fatal(err)
	}
	if fd != 42 {
		t.Fatalf("route socket fd = %d, want 42", fd)
	}
	if domain != unix.AF_ROUTE || socketType != unix.SOCK_RAW || protocol != unix.AF_UNSPEC {
		t.Fatalf("socket args = (%d, %d, %d), want AF_ROUTE/SOCK_RAW/AF_UNSPEC", domain, socketType, protocol)
	}
	if want := []string{"lock", "socket", "close_on_exec", "unlock"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("route socket open order = %v, want %v", order, want)
	}
}

func TestNetworkObserverDarwinRouteSocketCloseOnExecFailureClosesDescriptor(t *testing.T) {
	sentinel := errors.New("fcntl failed")
	var closed []int
	ops := darwinRouteSocketOps{
		forkLock: &sync.Mutex{},
		socket: func(int, int, int) (int, error) {
			return 42, nil
		},
		closeOnExec: func(int) error {
			return sentinel
		},
		close: func(fd int) error {
			closed = append(closed, fd)
			return nil
		},
	}

	fd, err := openDarwinRouteSocket(ops)
	if fd != -1 || !errors.Is(err, sentinel) {
		t.Fatalf("open = fd %d, error %v; want -1 wrapping CLOEXEC failure", fd, err)
	}
	if !reflect.DeepEqual(closed, []int{42}) {
		t.Fatalf("closed descriptors = %v, want [42]", closed)
	}
}

func TestRelevantDarwinRouteEventParsesValidTruncatedAndIgnoredFrames(t *testing.T) {
	valid, err := (&route.RouteMessage{Type: unix.RTM_ADD}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	ignored, err := (&route.RouteMessage{Type: unix.RTM_GET}).Marshal()
	if err != nil {
		t.Fatal(err)
	}

	if !relevantDarwinRouteEvent(valid) {
		t.Fatal("RTM_ADD frame was not treated as relevant")
	}
	if relevantDarwinRouteEvent(valid[:len(valid)/2]) {
		t.Fatal("truncated route frame was treated as relevant")
	}
	if relevantDarwinRouteEvent(ignored) {
		t.Fatal("RTM_GET request class was treated as a network-change event")
	}
}

func TestReadDarwinRouteEventsTerminatesOnZeroLengthRead(t *testing.T) {
	var reads, closes int
	events := make(chan struct{}, 1)
	readDarwinRouteEventsWithIO(context.Background(), 42, events, darwinRouteEventIO{
		read: func(int, []byte) (int, error) {
			reads++
			return 0, nil
		},
		close: func(int) error {
			closes++
			return nil
		},
	})

	if reads != 1 || closes != 1 {
		t.Fatalf("zero-read lifecycle = reads %d closes %d, want 1/1", reads, closes)
	}
	if _, open := <-events; open {
		t.Fatal("event channel remained open after zero-length read")
	}
}

func TestReadDarwinRouteEventsTerminatesOnReadError(t *testing.T) {
	sentinel := errors.New("read failed")
	var reads, closes int
	events := make(chan struct{}, 1)
	readDarwinRouteEventsWithIO(context.Background(), 42, events, darwinRouteEventIO{
		read: func(int, []byte) (int, error) {
			reads++
			return 0, sentinel
		},
		close: func(int) error {
			closes++
			return nil
		},
	})

	if reads != 1 || closes != 1 {
		t.Fatalf("read-error lifecycle = reads %d closes %d, want 1/1", reads, closes)
	}
	if _, open := <-events; open {
		t.Fatal("event channel remained open after read error")
	}
}

func TestReadDarwinRouteEventsEmitsValidFrameThenTerminates(t *testing.T) {
	frame, err := (&route.RouteMessage{Type: unix.RTM_CHANGE}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	read := 0
	events := make(chan struct{}, 1)
	readDarwinRouteEventsWithIO(context.Background(), 42, events, darwinRouteEventIO{
		read: func(_ int, buffer []byte) (int, error) {
			read++
			if read == 1 {
				return copy(buffer, frame), nil
			}
			return 0, nil
		},
		close: func(int) error { return nil },
	})

	count := 0
	for range events {
		count++
	}
	if count != 1 {
		t.Fatalf("route events = %d, want one coalesced signal", count)
	}
}

func TestReadDarwinRouteEventsCancellationClosesDescriptorAndUnblocksRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	readStarted := make(chan struct{})
	descriptorClosed := make(chan struct{})
	var readOnce, closeOnce sync.Once
	events := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		readDarwinRouteEventsWithIO(ctx, 42, events, darwinRouteEventIO{
			read: func(int, []byte) (int, error) {
				readOnce.Do(func() { close(readStarted) })
				<-descriptorClosed
				return 0, unix.EBADF
			},
			close: func(int) error {
				closeOnce.Do(func() { close(descriptorClosed) })
				return nil
			},
		})
		close(done)
	}()

	<-readStarted
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not unblock the route socket read")
	}
	if _, open := <-events; open {
		t.Fatal("event channel remained open after cancellation")
	}
}

type recordingDarwinRouteSocketLock struct {
	mu    sync.Mutex
	order *[]string
}

func (l *recordingDarwinRouteSocketLock) Lock() {
	l.mu.Lock()
	*l.order = append(*l.order, "lock")
}

func (l *recordingDarwinRouteSocketLock) Unlock() {
	*l.order = append(*l.order, "unlock")
	l.mu.Unlock()
}
