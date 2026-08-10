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

// assertAtomicReplace 钉住「整文件原子替换」这条 writeJSONAtomically 的核心契约:
// 临时文件 + rename,而不是就地截断。
//
// **就地截断时,改写一份已有状态中途崩溃会留下半截文件** —— 而这几个文件存在的
// 意义(记住用户要什么、记住此刻不该有保护)正好被反过来吃掉:一个读不懂的
// guardian-state.json 会让 LoadDesired 报错 → recoverLocked 置 recoveryBlocked
// → Manager.Down 永久返回 errRecoveryIncomplete(2026-08-04 那次 71 分钟事故的
// 机制);一个读不懂的 maintenance-hold.json 会让每一条 fail-closed 的栅栏永久
// 举着。rename 覆盖会换掉目录项指向的 inode,这里就用 inode 变化钉死「不是就地写」。
//
// **这个断言一度只挂在 SaveUpgradeIntent 上,随欠条一起被删掉了**,而
// writeJSONAtomically 底下还有六个生产调用点。实测:把它换成 os.WriteFile 的
// 就地截断,`go test ./... -count=1` 全部 32 个包照样绿。
func assertAtomicReplace(t *testing.T, path string, write func() error) {
	t.Helper()
	if err := write(); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := write(); err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(first, second) {
		t.Fatal("改写必须走临时文件 + rename(换 inode),就地截断会留下半截文件")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("不得留下临时文件,实际 %v", names)
	}
}

// desired 是这整期要保住的那句话,它的落盘必须是原子的。
func TestSaveDesiredReplacesFileAtomically(t *testing.T) {
	root := t.TempDir()
	// Desired 单独一个目录,好让「不得留下临时文件」这条断言精确到 1 —— 与
	// update/ 那几个路径混在一起时,数目里混着别人的东西就不是断言了。
	stateDir := filepath.Join(root, "state")
	paths := testPaths(root)
	paths.Desired = filepath.Join(stateDir, "guardian-state.json")
	store := OpenStore(paths)

	assertAtomicReplace(t, paths.Desired, func() error { return store.SaveDesired(DesiredOn) })

	if desired, err := store.LoadDesired(); err != nil || desired != DesiredOn {
		t.Fatalf("原子写之后内容仍须正确:%q err=%v", desired, err)
	}
}
