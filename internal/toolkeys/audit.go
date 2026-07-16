package toolkeys

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type AuditEntry struct {
	Time         time.Time `json:"time"`
	CredentialID string    `json:"credential_id"`
	Label        string    `json:"label"`
	Origin       string    `json:"origin"`
	Method       string    `json:"method"`
	Path         string    `json:"path"`
	Status       int       `json:"status"`
	DurationMS   int64     `json:"duration_ms"`
	Surface      string    `json:"surface"`
}

type Audit struct {
	mu        sync.Mutex
	path      string
	retention time.Duration
}

func OpenAudit(path string, retention time.Duration) (*Audit, error) {
	if retention <= 0 {
		return nil, fmt.Errorf("audit retention required")
	}
	return &Audit{path: path, retention: retention}, nil
}
func (a *Audit) Record(entry AuditEntry) error {
	if strings.Contains(entry.Path, "?") {
		return fmt.Errorf("audit path cannot contain query")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	var kept [][]byte
	if f, err := os.Open(a.path); err == nil {
		s := bufio.NewScanner(f)
		for s.Scan() {
			var old AuditEntry
			if json.Unmarshal(s.Bytes(), &old) == nil && old.Time.After(time.Now().Add(-a.retention)) {
				kept = append(kept, append([]byte(nil), s.Bytes()...))
			}
		}
		_ = f.Close()
	} else if !os.IsNotExist(err) {
		return err
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	kept = append(kept, b)
	tmp, err := os.CreateTemp(filepath.Dir(a.path), ".audit-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o600); err == nil {
		for _, line := range kept {
			if _, err = tmp.Write(append(line, '\n')); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, a.path)
}
