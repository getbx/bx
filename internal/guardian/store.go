package guardian

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type Store struct {
	mu            sync.Mutex
	paths         Paths
	syncDirectory func(string) error
}

const (
	guardianStateDirectory  = "/var/lib/bx"
	guardianUpdateDirectory = guardianStateDirectory + "/update"

	// defaultMaintenanceHoldPath 同时被 OpenDefaultStore 与 intent_unreadable 的
	// 排查指引引用 —— 指引若把用户送去一个不存在的路径,等于没有指引。
	defaultMaintenanceHoldPath = guardianStateDirectory + "/maintenance-hold.json"
)

var safeLastErrorPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

func OpenDefaultStore() *Store {
	return OpenStore(Paths{
		Desired:         guardianStateDirectory + "/guardian-state.json",
		Transaction:     guardianUpdateDirectory + "/transaction.json",
		Receipt:         guardianUpdateDirectory + "/receipt.json",
		Staging:         guardianUpdateDirectory + "/staging",
		Snapshots:       guardianUpdateDirectory + "/snapshots",
		UpgradeIntent:   guardianStateDirectory + "/upgrade-intent.json",
		MaintenanceHold: defaultMaintenanceHoldPath,
	})
}

func OpenStore(paths Paths) *Store {
	return &Store{paths: paths, syncDirectory: syncGuardianDirectory}
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

// UpgradeIntent 是**已退休**的升级欠条的盘上格式:一次升级开始前记下的
// 「用户本来要不要保护开着」。
//
// 它当初存在的理由是升级第一步(停保护)会把 desired 写成 off,于是一次中途失败
// 之后重跑 app-install,读 desired 只会读到 off,升级计划就不再包含「恢复保护」。
// **今天升级停机改为武装维护挂起、一个字节都不动 desired**(见 hold.go),
// 所以重跑读到的就是用户本来的意图 —— 这正是欠条能被删掉的全部依据。
//
// 结构体本身**只为 MigrateLegacyUpgradeIntent 保留**:一台正处在升级中途、跨过
// 这次切换的机器,盘上还有一张这种文件要被翻译成挂起。写入的那一侧
// (SaveUpgradeIntent)与合成语义(resolveUpgradeDesiredOn)都已删除,新代码
// 不许再产生这个文件。
type UpgradeIntent struct {
	SchemaVersion int  `json:"schema_version"`
	DesiredOn     bool `json:"desired_on"`
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
	if transaction.LastError != "" && !safeLastErrorPattern.MatchString(transaction.LastError) {
		return nil, fmt.Errorf("invalid transaction last error")
	}
	return &transaction, nil
}

func (s *Store) SaveTransaction(transaction Transaction) error {
	if !transaction.Phase.valid() {
		return fmt.Errorf("invalid transaction phase %q", transaction.Phase)
	}
	if transaction.LastError != "" && !safeLastErrorPattern.MatchString(transaction.LastError) {
		return fmt.Errorf("invalid transaction last error")
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
	if err := os.Remove(s.paths.Transaction); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("clear transaction: %w", err)
	}
	if err := s.syncDirectory(filepath.Dir(s.paths.Transaction)); err != nil {
		return fmt.Errorf("sync cleared transaction: %w", err)
	}
	return nil
}

func (s *Store) LoadReceipt() (*Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := os.ReadFile(s.paths.Receipt)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read receipt: %w", err)
	}
	var receipt Receipt
	if err := json.Unmarshal(b, &receipt); err != nil {
		return nil, fmt.Errorf("decode receipt: %w", err)
	}
	if !validReceipt(receipt) {
		return nil, fmt.Errorf("invalid receipt")
	}
	return &receipt, nil
}

func (s *Store) SaveReceipt(receipt Receipt) error {
	if !validReceipt(receipt) {
		return fmt.Errorf("invalid receipt")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDirectories(); err != nil {
		return err
	}
	return writeJSONAtomically(s.paths.Receipt, receipt)
}

func validReceipt(receipt Receipt) bool {
	if !receipt.Outcome.terminal() || !updateTransactionIDPattern.MatchString(receipt.TransactionID) ||
		!updateVersionPattern.MatchString(receipt.FromVersion) || !updateVersionPattern.MatchString(receipt.ToVersion) ||
		len(receipt.AssetDigest) != 2*sha256.Size || receipt.AssetDigest != strings.ToLower(receipt.AssetDigest) || receipt.CompletedAt.IsZero() {
		return false
	}
	_, err := hex.DecodeString(receipt.AssetDigest)
	return err == nil
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
	return syncGuardianDirectory(filepath.Dir(path))
}

func syncGuardianDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
