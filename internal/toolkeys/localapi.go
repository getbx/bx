package toolkeys

import (
	"encoding/json"
	"net/http"
	"strings"
)

func NewLocalAPI(store *Store, broker *Broker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeLocalJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeLocalJSON(w, http.StatusOK, store.List())
	})
	mux.HandleFunc("/v1/pending", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeLocalJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var input PendingRequestInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.Reason) == "" {
			writeLocalJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid pending request"})
			return
		}
		origin, err := CanonicalOrigin(input.Origin)
		if err != nil {
			writeLocalJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid origin"})
			return
		}
		pending, err := store.CreatePending(origin, input.AuthHint, input.Reason, input.DocsURL)
		if err != nil {
			writeLocalJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid auth hint"})
			return
		}
		writeLocalJSON(w, http.StatusCreated, pending)
	})
	return mux
}

func writeLocalJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
