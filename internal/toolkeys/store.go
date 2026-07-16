package toolkeys

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type storeState struct {
	Credentials map[string]diskCredential
	Pending     map[string]PendingRequest
}
type diskCredential struct {
	Credential
	Secret string `json:"secret"`
}
type Store struct {
	mu    sync.Mutex
	path  string
	state storeState
	now   func() time.Time
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, state: storeState{Credentials: map[string]diskCredential{}, Pending: map[string]PendingRequest{}}, now: time.Now}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &s.state); err != nil {
			return nil, fmt.Errorf("read credential store: %w", err)
		}
		if s.state.Credentials == nil {
			s.state.Credentials = map[string]diskCredential{}
		}
		if s.state.Pending == nil {
			s.state.Pending = map[string]PendingRequest{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".credentials-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(b)
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
	return os.Rename(name, s.path)
}

func (s *Store) Put(c Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == "" {
		return fmt.Errorf("credential id required")
	}
	if err := c.AuthHint.Validate(); err != nil {
		return err
	}
	s.state.Credentials[c.ID] = diskCredential{Credential: c, Secret: c.Secret}
	return s.save()
}
func (s *Store) Resolve(id string) (Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	disk, ok := s.state.Credentials[id]
	if !ok {
		return Credential{}, fmt.Errorf("credential not found")
	}
	c := disk.Credential
	c.Secret = disk.Secret
	return c, nil
}
func (s *Store) List() []CredentialMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CredentialMeta, 0, len(s.state.Credentials))
	for _, disk := range s.state.Credentials {
		c := disk.Credential
		out = append(out, CredentialMeta{ID: c.ID, Label: c.Label, Origin: c.Origin, AuthHint: c.AuthHint, Enabled: c.Enabled, CreatedAt: c.CreatedAt, RotatedAt: c.RotatedAt, LastUsedAt: c.LastUsedAt})
	}
	return out
}
func (s *Store) ReplaceSecret(id, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	disk, ok := s.state.Credentials[id]
	if !ok {
		return fmt.Errorf("credential not found")
	}
	disk.Secret = secret
	disk.RotatedAt = s.now()
	s.state.Credentials[id] = disk
	return s.save()
}

func randomID() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (s *Store) CreatePending(origin string, hint AuthHint, reason, docs string) (PendingRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := hint.Validate(); err != nil {
		return PendingRequest{}, err
	}
	id, err := randomID()
	if err != nil {
		return PendingRequest{}, err
	}
	now := s.now()
	p := PendingRequest{ID: id, Origin: origin, AuthHint: hint, Reason: reason, DocsURL: docs, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
	s.state.Pending[id] = p
	return p, s.save()
}
func (s *Store) CompletePending(id, secret string) (Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.state.Pending[id]
	if !ok || !p.ExpiresAt.After(s.now()) {
		return Credential{}, fmt.Errorf("pending request not found")
	}
	cid, err := randomID()
	if err != nil {
		return Credential{}, err
	}
	c := Credential{ID: cid, Label: p.Origin, Origin: p.Origin, Secret: secret, AuthHint: p.AuthHint, Enabled: true, CreatedAt: s.now()}
	s.state.Credentials[cid] = diskCredential{Credential: c, Secret: c.Secret}
	delete(s.state.Pending, id)
	if err := s.save(); err != nil {
		return Credential{}, err
	}
	return c, nil
}
