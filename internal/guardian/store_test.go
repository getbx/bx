package guardian

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func testPaths(root string) Paths {
	update := filepath.Join(root, "update")
	return Paths{
		Desired:     filepath.Join(root, "guardian-state.json"),
		Transaction: filepath.Join(update, "transaction.json"),
		Receipt:     filepath.Join(update, "receipt.json"),
		Staging:     filepath.Join(update, "staging"),
		Snapshots:   filepath.Join(update, "snapshots"),
	}
}

func TestStorePersistsDesiredStateAtomically(t *testing.T) {
	paths := testPaths(t.TempDir())
	s := OpenStore(paths)
	if err := s.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadDesired()
	if err != nil || got != DesiredOn {
		t.Fatalf("desired = %q, %v", got, err)
	}
	info, err := os.Stat(paths.Desired)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
}

func TestStoreRejectsInvalidTransactionPhase(t *testing.T) {
	s := OpenStore(testPaths(t.TempDir()))
	err := s.SaveTransaction(Transaction{ID: "tx-1", Phase: Phase("unknown")})
	if err == nil {
		t.Fatal("invalid phase accepted")
	}
}

func TestStoreRejectsMalformedDesiredState(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := os.WriteFile(paths.Desired, []byte(`"unknown"`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := OpenStore(paths).LoadDesired()
	if err == nil || got != "" {
		t.Fatalf("desired = %q, %v", got, err)
	}
}

func TestStoreRestrictsStateDirectories(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := os.Mkdir(filepath.Dir(paths.Transaction), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(paths.Transaction), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := OpenStore(paths).SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Dir(paths.Transaction))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
}

func TestTransactionJSONContainsNoClientSecrets(t *testing.T) {
	tx := Transaction{ID: "tx-1", FromVersion: "v1", ToVersion: "v2", Phase: PhasePrepared}
	b, err := json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"server_link", "client_link", "token", "password"} {
		if bytes.Contains(bytes.ToLower(b), []byte(forbidden)) {
			t.Fatalf("journal contains %q: %s", forbidden, b)
		}
	}
}
