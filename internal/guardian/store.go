package guardian

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Store struct {
	mu    sync.Mutex
	paths Paths
}

func OpenStore(paths Paths) *Store {
	return &Store{paths: paths}
}

func (s *Store) LoadDesired() (DesiredState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := os.ReadFile(s.paths.Desired)
	if os.IsNotExist(err) {
		return DesiredOff, nil
	}
	if err != nil {
		return "", fmt.Errorf("read desired state: %w", err)
	}
	var desired DesiredState
	if err := json.Unmarshal(b, &desired); err != nil {
		return "", fmt.Errorf("decode desired state: %w", err)
	}
	if !desired.valid() {
		return "", fmt.Errorf("invalid desired state %q", desired)
	}
	return desired, nil
}

func (s *Store) SaveDesired(desired DesiredState) error {
	if !desired.valid() {
		return fmt.Errorf("invalid desired state %q", desired)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDirectories(); err != nil {
		return err
	}
	return writeJSONAtomically(s.paths.Desired, desired)
}

func (s *Store) LoadTransaction() (*Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := os.ReadFile(s.paths.Transaction)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read transaction: %w", err)
	}
	var transaction Transaction
	if err := json.Unmarshal(b, &transaction); err != nil {
		return nil, fmt.Errorf("decode transaction: %w", err)
	}
	if !transaction.Phase.valid() {
		return nil, fmt.Errorf("invalid transaction phase %q", transaction.Phase)
	}
	return &transaction, nil
}

func (s *Store) SaveTransaction(transaction Transaction) error {
	if !transaction.Phase.valid() {
		return fmt.Errorf("invalid transaction phase %q", transaction.Phase)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDirectories(); err != nil {
		return err
	}
	return writeJSONAtomically(s.paths.Transaction, transaction)
}

func (s *Store) ClearTransaction() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDirectories(); err != nil {
		return err
	}
	if err := os.Remove(s.paths.Transaction); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear transaction: %w", err)
	}
	return nil
}

func (s *Store) SaveReceipt(receipt Receipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDirectories(); err != nil {
		return err
	}
	return writeJSONAtomically(s.paths.Receipt, receipt)
}

func (s *Store) ensureDirectories() error {
	for _, path := range []string{
		filepath.Dir(s.paths.Desired),
		filepath.Dir(s.paths.Transaction),
		filepath.Dir(s.paths.Receipt),
		s.paths.Staging,
		s.paths.Snapshots,
	} {
		if path == "" || path == "." {
			return fmt.Errorf("guardian store path required")
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create guardian state directory: %w", err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("restrict guardian state directory: %w", err)
		}
	}
	return nil
}

func writeJSONAtomically(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".guardian-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return nil
}
