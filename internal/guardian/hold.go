package guardian

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// MaintenanceHoldDuration 是一次维护挂起的有效期。
//
// 15 分钟:要盖住最长的一次可信升级(替换文件 + 重启 Guardian + 起保护;下载发生
// 在此之前),又要短到崩溃之后当天就能看出不对。
//
// **常量单点拥有,消费方不得各自重导**——照 ReconcileStaleAfter(reconcile_loop.go)
// 的先例:另一个进程里抄一个 15 分钟出来,两个数字迟早分叉。
const MaintenanceHoldDuration = 15 * time.Minute

// 挂起的来由。原样进 JSON 与日志,所以是稳定标识符,不是给人看的句子。
const (
	HoldReasonUpgrade       = "upgrade"
	HoldReasonLegacyUpgrade = "legacy_upgrade"
)

var maintenanceHoldReasonPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// MaintenanceHold 是「此刻不该有保护」这件事本身,与 desired 正交:它带来由与
// 过期时刻,**从不改写 desired**。
type MaintenanceHold struct {
	SchemaVersion int       `json:"schema_version"`
	Reason        string    `json:"reason"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// ArmMaintenanceHold 武装一次挂起。整文件原子替换,**绝不 read-modify-write**:
// 写这份状态的有两个进程(CLI 与 Guardian)且没有任何锁,Store 里那把 mutex
// 连进程内都保护不了什么(internal/cli 每次调用都新建一个 Store)。
func (s *Store) ArmMaintenanceHold(reason string, now time.Time) error {
	if !maintenanceHoldReasonPattern.MatchString(reason) {
		return fmt.Errorf("invalid maintenance hold reason %q", reason)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paths.MaintenanceHold == "" {
		return fmt.Errorf("guardian maintenance hold path required")
	}
	if err := os.MkdirAll(filepath.Dir(s.paths.MaintenanceHold), 0o700); err != nil {
		return fmt.Errorf("create guardian state directory: %w", err)
	}
	return writeJSONAtomically(s.paths.MaintenanceHold, MaintenanceHold{
		SchemaVersion: 1,
		Reason:        reason,
		ExpiresAt:     now.Add(MaintenanceHoldDuration),
	})
}

// LoadMaintenanceHold 返回 (挂起, 是否武装, 错误)。
//
// **三个返回值不是啰嗦:「没有挂起」「挂起过期了」「问不出来」是三件事。**
// LoadUpgradeIntent(store.go:92-107)把它们压成两个 bool,于是 EACCES/EIO 与
// 「文件不在」返回同一个答案 —— 那与它自己 :86-91 的注释正好相反,也正是这份
// 设计要消灭的读取偏置。
//
// **读取偏置在这里是刻意选的:只有 ENOENT 才是「确知没有挂起」,其余一切读失败
// 都报错。** 调用方对 err 一律 fail-closed(不起 Core、不收敛),因为挂起说的是
// 「此刻不该有人动手」,而「问不出来」时动手正是它要拦的那件事(设计缺陷⑤的
// intent_unreadable 栅栏)。这与本仓库「Unknown ≠ False」同一条纪律
// (internal/observe 的 Tristate)。
//
// 过期在**读取时**判,不设定时器:进程重启后照样成立。过期的文件刻意不在这里
// 删 —— 读路径不许因为一次 unlink 失败而失败;清理由 bx up/bx down/uninstall 做。
func (s *Store) LoadMaintenanceHold(now time.Time) (MaintenanceHold, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 路径没配 = 这个 Store 压根没有挂起这个概念(只有测试造得出来;
	// OpenDefaultStore 永远填它),与「盘上没有挂起」同义。
	if s.paths.MaintenanceHold == "" {
		return MaintenanceHold{}, false, nil
	}
	b, err := os.ReadFile(s.paths.MaintenanceHold)
	if os.IsNotExist(err) {
		return MaintenanceHold{}, false, nil
	}
	if err != nil {
		return MaintenanceHold{}, false, fmt.Errorf("read maintenance hold: %w", err)
	}
	var hold MaintenanceHold
	if err := json.Unmarshal(b, &hold); err != nil {
		return MaintenanceHold{}, false, fmt.Errorf("decode maintenance hold: %w", err)
	}
	if hold.SchemaVersion != 1 {
		return MaintenanceHold{}, false, fmt.Errorf("unsupported maintenance hold schema %d", hold.SchemaVersion)
	}
	if !maintenanceHoldReasonPattern.MatchString(hold.Reason) {
		return MaintenanceHold{}, false, fmt.Errorf("invalid maintenance hold reason")
	}
	// 没有过期时刻的挂起会永久压制保护。宁可报错(fail-closed 且会在
	// bx status 上现形),也不接受一个没有尽头的压制。
	if hold.ExpiresAt.IsZero() {
		return MaintenanceHold{}, false, fmt.Errorf("maintenance hold has no expiry")
	}
	return hold, hold.ExpiresAt.After(now), nil
}

// ClearMaintenanceHold 撤销挂起。文件本来就不在不算失败(幂等)——它跑在
// 「用户明确要关保护」的路径上,而停止不许依赖先成功做成别的事。
func (s *Store) ClearMaintenanceHold() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paths.MaintenanceHold == "" {
		return nil
	}
	if err := os.Remove(s.paths.MaintenanceHold); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove maintenance hold: %w", err)
	}
	return nil
}
