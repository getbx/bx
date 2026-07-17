package guardian

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const guardianMutationTimeout = time.Minute

type Controller interface {
	Status() Status
	Up(context.Context) error
	Down(context.Context) error
}

type peerCredentialsKey struct{}

type peerCredentials struct {
	uid uint32
	got bool
}

type localAPI struct {
	handler    http.Handler
	mutations  *acceptedMutations
	recoveries recoveryLifecycle
}

type recoveryLifecycle interface {
	beginRecoveryShutdown()
	waitForRecoveries(context.Context) error
}

type acceptedMutations struct {
	mu        sync.Mutex
	accepting bool
	active    int
	drained   chan struct{}
	closed    bool
}

func withPeerCredentials(ctx context.Context, uid uint32, got bool) context.Context {
	return context.WithValue(ctx, peerCredentialsKey{}, peerCredentials{uid: uid, got: got})
}

func NewLocalAPI(controller Controller) http.Handler {
	mutations := &acceptedMutations{accepting: true, drained: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeGuardianJSON(w, http.StatusOK, controller.Status())
	})
	mux.HandleFunc("/v1/up", mutationHandler(controller, controller.Up, mutations))
	mux.HandleFunc("/v1/down", mutationHandler(controller, controller.Down, mutations))
	recoveries, _ := controller.(recoveryLifecycle)
	return &localAPI{handler: mux, mutations: mutations, recoveries: recoveries}
}

func (a *localAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.handler.ServeHTTP(w, r)
}

func (a *localAPI) beginShutdown() {
	a.mutations.stopAccepting()
}

func (a *localAPI) waitForMutations(ctx context.Context) error {
	return a.mutations.wait(ctx)
}

func (a *localAPI) beginRecoveryShutdown() {
	if a.recoveries != nil {
		a.recoveries.beginRecoveryShutdown()
	}
}

func (a *localAPI) waitForRecoveries(ctx context.Context) error {
	if a.recoveries == nil {
		return nil
	}
	return a.recoveries.waitForRecoveries(ctx)
}

func mutationHandler(controller Controller, mutate func(context.Context) error, mutations *acceptedMutations) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		credentials, _ := r.Context().Value(peerCredentialsKey{}).(peerCredentials)
		if !credentials.got || credentials.uid != 0 {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "mutation requires root peer"})
			return
		}
		if !mutations.accept() {
			writeGuardianJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "guardian is shutting down"})
			return
		}
		defer mutations.done()
		mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), guardianMutationTimeout)
		defer cancel()
		if err := mutate(mutationCtx); err != nil {
			writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"error": "guardian operation failed"})
			return
		}
		writeGuardianJSON(w, http.StatusOK, controller.Status())
	}
}

func (m *acceptedMutations) accept() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.accepting {
		return false
	}
	m.active++
	return true
}

func (m *acceptedMutations) done() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
	m.closeDrainedLocked()
}

func (m *acceptedMutations) stopAccepting() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accepting = false
	m.closeDrainedLocked()
}

func (m *acceptedMutations) closeDrainedLocked() {
	if !m.accepting && m.active == 0 && !m.closed {
		close(m.drained)
		m.closed = true
	}
}

func (m *acceptedMutations) wait(ctx context.Context) error {
	select {
	case <-m.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func writeGuardianJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
