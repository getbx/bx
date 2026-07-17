package guardian

import (
	"context"
	"encoding/json"
	"net/http"
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

func withPeerCredentials(ctx context.Context, uid uint32, got bool) context.Context {
	return context.WithValue(ctx, peerCredentialsKey{}, peerCredentials{uid: uid, got: got})
}

func NewLocalAPI(controller Controller) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeGuardianJSON(w, http.StatusOK, controller.Status())
	})
	mux.HandleFunc("/v1/up", mutationHandler(controller, controller.Up))
	mux.HandleFunc("/v1/down", mutationHandler(controller, controller.Down))
	return mux
}

func mutationHandler(controller Controller, mutate func(context.Context) error) http.HandlerFunc {
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
		mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), guardianMutationTimeout)
		defer cancel()
		if err := mutate(mutationCtx); err != nil {
			writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"error": "guardian operation failed"})
			return
		}
		writeGuardianJSON(w, http.StatusOK, controller.Status())
	}
}

func writeGuardianJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
