// Package syncstore is the VPS side of rule sync: a loopback-only HTTP store that keeps one
// opaque blob per id. It cannot read what it stores (policysync seals it with a key derived
// from the server link, which the VPS does not use), and it listens on 127.0.0.1 only —
// clients reach it through their own tunnel; nothing new is opened to the Internet.
package syncstore

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MaxBlob caps one blob. A policy of a few hundred rules seals to a few KB.
const MaxBlob = 256 << 10

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Handler serves GET and PUT on /v1/blob/<id>, storing files named <id> in dir (0600, atomic).
func Handler(dir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/blob/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v1/blob/")
		if !idPattern.MatchString(id) {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		path := filepath.Join(dir, id)
		switch r.Method {
		case http.MethodGet:
			b, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			if err != nil {
				http.Error(w, "read failed", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(b)
		case http.MethodPut:
			body, err := io.ReadAll(io.LimitReader(r.Body, MaxBlob+1))
			if err != nil {
				http.Error(w, "read failed", http.StatusBadRequest)
				return
			}
			if len(body) > MaxBlob {
				http.Error(w, "too large", http.StatusRequestEntityTooLarge)
				return
			}
			if len(body) == 0 {
				http.Error(w, "empty", http.StatusBadRequest)
				return
			}
			if err := writeAtomic(dir, id, body); err != nil {
				http.Error(w, "write failed", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	return mux
}

func writeAtomic(dir, id string, body []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+id+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, id))
}
