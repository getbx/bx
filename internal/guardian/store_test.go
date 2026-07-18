package guardian

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestOpenDefaultStoreUsesProductionStatePaths(t *testing.T) {
	s := OpenDefaultStore()
	if s.paths.Desired != "/var/lib/bx/guardian-state.json" {
		t.Errorf("desired path = %q", s.paths.Desired)
	}
	if s.paths.Transaction != "/var/lib/bx/update/transaction.json" {
		t.Errorf("transaction path = %q", s.paths.Transaction)
	}
	if s.paths.Receipt != "/var/lib/bx/update/receipt.json" {
		t.Errorf("receipt path = %q", s.paths.Receipt)
	}
	if s.paths.Staging != "/var/lib/bx/update/staging" {
		t.Errorf("staging path = %q", s.paths.Staging)
	}
	if s.paths.Snapshots != "/var/lib/bx/update/snapshots" {
		t.Errorf("snapshots path = %q", s.paths.Snapshots)
	}
}

func TestStoreRejectsInvalidAndNonTerminalReceiptOutcomes(t *testing.T) {
	s := OpenStore(testPaths(t.TempDir()))
	for _, outcome := range []Phase{Phase("unknown"), PhaseIdle, PhasePrepared, PhaseBarrierActive, PhaseActivating, PhaseRollingBack} {
		t.Run(string(outcome), func(t *testing.T) {
			receipt := validStoreTestReceipt(PhaseCommitted)
			receipt.Outcome = outcome
			err := s.SaveReceipt(receipt)
			if err == nil {
				t.Fatalf("receipt outcome %q accepted", outcome)
			}
		})
	}
}

func TestStoreAcceptsTerminalReceiptOutcomes(t *testing.T) {
	s := OpenStore(testPaths(t.TempDir()))
	for _, outcome := range []Phase{PhaseCommitted, PhaseRolledBack, PhaseNeedsAttention} {
		t.Run(string(outcome), func(t *testing.T) {
			if err := s.SaveReceipt(validStoreTestReceipt(outcome)); err != nil {
				t.Fatalf("save receipt: %v", err)
			}
		})
	}
}

func TestStoreLoadsOnlyValidatedReceipts(t *testing.T) {
	paths := testPaths(t.TempDir())
	store := OpenStore(paths)
	want := validStoreTestReceipt(PhaseCommitted)
	if err := store.SaveReceipt(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadReceipt()
	if err != nil || got == nil || *got != want {
		t.Fatalf("LoadReceipt = %#v, %v; want %#v", got, err, want)
	}

	invalid := want
	invalid.AssetDigest = "not-a-digest"
	b, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Receipt, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := store.LoadReceipt(); err == nil || got != nil {
		t.Fatalf("invalid receipt loaded as %#v, %v", got, err)
	}
}

func TestStoreClearTransactionSyncsParentDirectory(t *testing.T) {
	paths := testPaths(t.TempDir())
	store := OpenStore(paths)
	transaction := Transaction{ID: "tx-1", Phase: PhasePrepared}
	if err := store.SaveTransaction(transaction); err != nil {
		t.Fatal(err)
	}
	var synced string
	store.syncDirectory = func(path string) error {
		synced = path
		return nil
	}
	if err := store.ClearTransaction(); err != nil {
		t.Fatal(err)
	}
	if synced != filepath.Dir(paths.Transaction) {
		t.Fatalf("synced directory = %q, want %q", synced, filepath.Dir(paths.Transaction))
	}
}

func validStoreTestReceipt(outcome Phase) Receipt {
	return Receipt{
		TransactionID: "tx-1",
		FromVersion:   "v1",
		ToVersion:     "v2",
		AssetDigest:   strings.Repeat("a", 64),
		Outcome:       outcome,
		CompletedAt:   time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC),
	}
}

func TestStoreAcceptsBoundedSafeTransactionLastErrorCodes(t *testing.T) {
	for _, lastError := range []string{"", "new_core_unhealthy", "update.rollback_failed"} {
		t.Run(lastError, func(t *testing.T) {
			paths := testPaths(t.TempDir())
			if err := OpenStore(paths).SaveTransaction(Transaction{ID: "tx-1", Phase: PhasePrepared, LastError: lastError}); err != nil {
				t.Fatal(err)
			}
			transaction, err := OpenStore(paths).LoadTransaction()
			if err != nil {
				t.Fatal(err)
			}
			if transaction.LastError != lastError {
				t.Errorf("last error = %q, want %q", transaction.LastError, lastError)
			}
		})
	}
}

func TestStoreRejectsUnsafeTransactionLastErrorsWithoutRewriting(t *testing.T) {
	for _, lastError := range []string{
		"bx://client.example/config?token=secret",
		"vless://uuid@client.example:443",
		"hysteria2://user:password@client.example:443",
		"trojan://password@client.example:443",
		"vmess://encoded-client-link",
		"password=hunter2",
		"token: abc123",
		"has spaces",
		"New_Core_Unhealthy",
		"_invalid",
		".invalid",
		"-invalid",
		strings.Repeat("a", 129),
	} {
		t.Run(lastError, func(t *testing.T) {
			paths := testPaths(t.TempDir())
			store := OpenStore(paths)
			if err := store.SaveTransaction(Transaction{ID: "tx-1", Phase: PhasePrepared, LastError: "existing_safe_error"}); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveTransaction(Transaction{ID: "tx-2", Phase: PhasePrepared, LastError: lastError}); err == nil {
				t.Fatalf("unsafe last error %q accepted", lastError)
			}
			transaction, err := store.LoadTransaction()
			if err != nil {
				t.Fatal(err)
			}
			if transaction.ID != "tx-1" || transaction.LastError != "existing_safe_error" {
				t.Fatalf("transaction rewritten: %#v", transaction)
			}
		})
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
