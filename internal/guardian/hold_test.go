package guardian

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func holdPaths(root string) Paths {
	p := testPaths(root)
	p.MaintenanceHold = filepath.Join(root, "maintenance-hold.json")
	return p
}

// 挂起在**读取时**判过期,不靠任何定时器 —— 照 internal/toolkeys/store.go:154,182
// 那个唯一持久化过期的先例。
func TestMaintenanceHoldExpiresAtReadTime(t *testing.T) {
	s := OpenStore(holdPaths(t.TempDir()))
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	if err := s.ArmMaintenanceHold(HoldReasonUpgrade, base); err != nil {
		t.Fatal(err)
	}
	if _, armed, err := s.LoadMaintenanceHold(base.Add(MaintenanceHoldDuration - time.Second)); err != nil || !armed {
		t.Fatalf("过期前应仍武装:armed=%v err=%v", armed, err)
	}
	if _, armed, err := s.LoadMaintenanceHold(base.Add(MaintenanceHoldDuration)); err != nil || armed {
		t.Fatalf("到点即失效:armed=%v err=%v", armed, err)
	}
}

// 挂起必须活过进程重启(internal/confirm/deadman.go 那套纯内存的形状不适用)。
func TestMaintenanceHoldSurvivesProcessRestart(t *testing.T) {
	root := t.TempDir()
	base := time.Now()
	if err := OpenStore(holdPaths(root)).ArmMaintenanceHold(HoldReasonUpgrade, base); err != nil {
		t.Fatal(err)
	}
	hold, armed, err := OpenStore(holdPaths(root)).LoadMaintenanceHold(base.Add(time.Minute))
	if err != nil || !armed {
		t.Fatalf("另一个 Store 实例读不到挂起:armed=%v err=%v", armed, err)
	}
	if hold.Reason != HoldReasonUpgrade {
		t.Fatalf("reason = %q, want %q", hold.Reason, HoldReasonUpgrade)
	}
}

// **整个设计取舍一**:guardian-state.json 里是个裸 JSON 字符串,旧 Guardian 读不懂
// 任何新形状就会 recoveryBlocked=true 永久拒绝 Down(2026-08-04 那次 71 分钟事故的
// 机制)。挂起因此必须住在另一个文件里,而且武装它**一个字节都不能动** desired。
func TestArmingHoldLeavesDesiredFileByteIdentical(t *testing.T) {
	paths := holdPaths(t.TempDir())
	s := OpenStore(paths)
	if err := s.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.Desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(paths.Desired)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || string(after) != `"on"` {
		t.Fatalf("desired 文件被动过:before=%s after=%s", before, after)
	}
}

// 读不动 / 读不懂**不许**塌缩成「没有挂起」—— 那正是 LoadUpgradeIntent
// (store.go:98-101)与它自己注释相反的那个 bug。
func TestUnreadableHoldIsAnErrorNotSilentlyUnarmed(t *testing.T) {
	paths := holdPaths(t.TempDir())
	if err := os.WriteFile(paths.MaintenanceHold, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, armed, err := OpenStore(paths).LoadMaintenanceHold(time.Now()); err == nil || armed {
		t.Fatalf("坏文件必须报错:armed=%v err=%v", armed, err)
	}
}

// 「没有挂起」与「问不出来」必须在返回值上可分辨 —— 这正是 LoadUpgradeIntent
// 塌缩掉的那一维(任何 os.ReadFile 错误都被它答成「没有欠条」,包括 EACCES/EIO)。
// 拿目录冒充文件造出一个与 uid 无关的 I/O 错误(root 跑测试时 chmod 000 仍可读,
// 那种造法会在 CI 的 root 容器里变成假绿)。
//
// 两半必须写在同一条测试里:分开写就只剩两条各自成立的断言,而这条测试存在的
// 全部理由是**两者答案不同**。
func TestMaintenanceHoldAbsenceIsDistinguishableFromReadFailure(t *testing.T) {
	absent := OpenStore(holdPaths(t.TempDir()))
	hold, armed, err := absent.LoadMaintenanceHold(time.Now())
	if err != nil || armed || hold != (MaintenanceHold{}) {
		t.Fatalf("文件不存在 = 确知没有挂起,不是错误:hold=%+v armed=%v err=%v", hold, armed, err)
	}

	broken := holdPaths(t.TempDir())
	if err := os.MkdirAll(broken.MaintenanceHold, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, armed, err := OpenStore(broken).LoadMaintenanceHold(time.Now()); err == nil || armed {
		t.Fatalf("读不动必须报错(调用方据此 fail-closed):armed=%v err=%v", armed, err)
	}
}

// 没有过期时刻的挂起会永久压制保护,不许存在。
func TestHoldWithoutExpiryIsRejectedOnRead(t *testing.T) {
	paths := holdPaths(t.TempDir())
	body, _ := json.Marshal(MaintenanceHold{SchemaVersion: 1, Reason: HoldReasonUpgrade})
	if err := os.WriteFile(paths.MaintenanceHold, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, armed, err := OpenStore(paths).LoadMaintenanceHold(time.Now()); err == nil || armed {
		t.Fatalf("无过期时刻的挂起必须报错:armed=%v err=%v", armed, err)
	}
}

// 未来某版若换了 schema,今天这一版必须拒绝而不是按 1 硬解 —— 字段含义变了以后
// 「按老规矩读到的挂起」比读不到更危险。
func TestHoldWithUnknownSchemaIsRejectedOnRead(t *testing.T) {
	paths := holdPaths(t.TempDir())
	body, err := json.Marshal(MaintenanceHold{SchemaVersion: 2, Reason: HoldReasonUpgrade, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.MaintenanceHold, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, armed, err := OpenStore(paths).LoadMaintenanceHold(time.Now()); err == nil || armed {
		t.Fatalf("未知 schema 必须报错:armed=%v err=%v", armed, err)
	}
}

// reason 原样进 JSON 与日志,故只收稳定标识符。写入侧先挡一道:一条带引号/换行的
// 「原因」进了盘,读它的日志与 status 就成了注入面。
func TestArmMaintenanceHoldRejectsUnstableReason(t *testing.T) {
	for _, reason := range []string{"", "Upgrade", "upgrade x", "upgrade\n", `up"grade`, "升级"} {
		paths := holdPaths(t.TempDir())
		if err := OpenStore(paths).ArmMaintenanceHold(reason, time.Now()); err == nil {
			t.Fatalf("reason %q 必须被拒", reason)
		}
		if _, err := os.Stat(paths.MaintenanceHold); !os.IsNotExist(err) {
			t.Fatalf("被拒的 reason 不许落盘:%v", err)
		}
	}
}

// 两个导出的 reason 常量都必须真的能武装 —— 校验用的正则收得比常量还紧的话,
// 迁移垫片(legacy_upgrade)会在真机上失败而单测毫无察觉。
func TestExportedHoldReasonsAreArmable(t *testing.T) {
	for _, reason := range []string{HoldReasonUpgrade, HoldReasonLegacyUpgrade} {
		s := OpenStore(holdPaths(t.TempDir()))
		if err := s.ArmMaintenanceHold(reason, time.Now()); err != nil {
			t.Fatalf("常量 %q 必须可武装: %v", reason, err)
		}
		hold, armed, err := s.LoadMaintenanceHold(time.Now())
		if err != nil || !armed || hold.Reason != reason {
			t.Fatalf("常量 %q 往返失败:hold=%+v armed=%v err=%v", reason, hold, armed, err)
		}
	}
}

// 盘上的形状与时长都要钉住:15 分钟这个数字是设计里定的(盖住最长一次可信升级,
// 又短到崩溃当天就能看出不对),不能靠 MaintenanceHoldDuration 自证 —— 用常量
// 两边一起算的断言,把常量改成 5 分钟照样绿。
func TestArmedHoldOnDiskCarriesFifteenMinuteExpiry(t *testing.T) {
	paths := holdPaths(t.TempDir())
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	if err := OpenStore(paths).ArmMaintenanceHold(HoldReasonUpgrade, base); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(paths.MaintenanceHold)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}

	b, err := os.ReadFile(paths.MaintenanceHold)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("盘上必须是一个自带信封的 JSON 对象(不是裸字符串): %v; body=%s", err, b)
	}
	for _, key := range []string{"schema_version", "reason", "expires_at"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("盘上缺字段 %q: %s", key, b)
		}
	}

	var hold MaintenanceHold
	if err := json.Unmarshal(b, &hold); err != nil {
		t.Fatal(err)
	}
	if want := base.Add(15 * time.Minute); !hold.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %s, want %s", hold.ExpiresAt, want)
	}
	if hold.SchemaVersion != 1 || hold.Reason != HoldReasonUpgrade {
		t.Fatalf("hold = %+v", hold)
	}
}

// 默认路径写死在这里,好让 internal/cli 的卸载计划有个可对齐的字面量:两边各写
// 一个常量、谁都不知道对方是什么时,卸载清的是哪个文件就没人守得住了(卸载测试
// 若只用 cli 自己的常量做 slices.Contains,常量改错它照样绿)。
func TestDefaultStoreMaintenanceHoldPath(t *testing.T) {
	if got := OpenDefaultStore().paths.MaintenanceHold; got != "/var/lib/bx/maintenance-hold.json" {
		t.Fatalf("默认挂起路径 = %q", got)
	}
}

// 清挂起是幂等的:文件本来就不在不算失败。它是逃生路径上的一步,不许挑剔。
func TestClearMaintenanceHoldIsIdempotent(t *testing.T) {
	s := OpenStore(holdPaths(t.TempDir()))
	if err := s.ClearMaintenanceHold(); err != nil {
		t.Fatalf("文件不存在时清挂起不该失败: %v", err)
	}
	if err := s.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearMaintenanceHold(); err != nil {
		t.Fatal(err)
	}
	if _, armed, err := s.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("清完还武装着:armed=%v err=%v", armed, err)
	}
}
