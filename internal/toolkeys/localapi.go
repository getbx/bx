package toolkeys

import (
	"encoding/json"
	"net/http"
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
	return mux
}

func writeLocalJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
