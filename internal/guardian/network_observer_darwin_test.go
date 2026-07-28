//go:build darwin

package guardian

import (
	"reflect"
	"sync"
	"testing"

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
