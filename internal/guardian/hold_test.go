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
	expired, armed, err := s.LoadMaintenanceHold(base.Add(MaintenanceHoldDuration))
	if err != nil || armed {
		t.Fatalf("到点即失效:armed=%v err=%v", armed, err)
	}
	// 过期的挂起**内容照样返回**(好让 bx status 说得出「一张 upgrade 挂起已在
	// 12:15 过期」),所以调用方只能看 armed —— `hold.Reason != ""` 会把它读成
	// 还武装着。这条断言就是把那个契约钉住的地方。
	if expired.Reason != HoldReasonUpgrade || !expired.ExpiresAt.Equal(base.Add(MaintenanceHoldDuration)) {
		t.Fatalf("过期挂起必须原样返回内容:%+v", expired)
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

// 一个没配挂起路径的 Store **答不了**「有没有挂起」,不许答「没有」。
//
// 这不是理论问题:internal/cli/upgraderun_test.go:257,331 就用
// guardian.OpenStore(guardian.Paths{UpgradeIntent: …}) 造替身,其余路径全空。
// 拿这种替身写「显式 down 清掉挂起」「有挂起就不起 Core」,在返回 false 的语义下
// 会**空洞地绿**:没路径 ⇒ 从不武装 ⇒ 没得清 ⇒ 栅栏永不触发。三个方法必须一致
// 报错,让造错的替身当场炸掉而不是给出一个自信的谎。
func TestStoreWithoutHoldPathRefusesToAnswer(t *testing.T) {
	s := OpenStore(testPaths(t.TempDir())) // testPaths 刻意不含 MaintenanceHold

	if err := s.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err == nil {
		t.Fatal("没有路径时武装必须报错")
	}
	hold, armed, err := s.LoadMaintenanceHold(time.Now())
	if err == nil || armed || hold != (MaintenanceHold{}) {
		t.Fatalf("没有路径时读取必须报错而不是答「没有挂起」:hold=%+v armed=%v err=%v", hold, armed, err)
	}
	if err := s.ClearMaintenanceHold(); err == nil {
		t.Fatal("没有路径时清挂起必须报错,不许假装清好了")
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

func writeHoldFile(t *testing.T, path string, hold MaintenanceHold) {
	t.Helper()
	body, err := json.Marshal(hold)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// 读取侧也必须校 reason,不能只靠写入侧:盘上的文件不一定是本进程写的(另一个
// 版本的 bx、手工编辑、部分写坏的文件),而 Task 7 会把 reason 原样发进
// bx status --json 与 Swift 菜单。本仓库的先例正是两侧都校 ——
// safeLastErrorPattern 在 LoadTransaction 与 SaveTransaction 里各查一次。
func TestHostileReasonOnDiskIsRejectedOnRead(t *testing.T) {
	for _, reason := range []string{
		"",
		"Upgrade",
		`up"grade`,
		"upgrade\nreason=other",
		"upgrade; rm -rf /",
		"../../etc/passwd",
		"升级",
	} {
		paths := holdPaths(t.TempDir())
		writeHoldFile(t, paths.MaintenanceHold, MaintenanceHold{
			SchemaVersion: 1,
			Reason:        reason,
			ExpiresAt:     time.Now().Add(time.Minute),
		})
		hold, armed, err := OpenStore(paths).LoadMaintenanceHold(time.Now())
		if err == nil || armed {
			t.Fatalf("盘上的 reason %q 必须被拒:hold=%+v armed=%v err=%v", reason, hold, armed, err)
		}
	}
}

// 「没有一张挂起可以无限期压制保护」必须在**整个输入空间**上成立,不能只堵零值:
// 一个远在未来的 ExpiresAt 同样是永久压制。现实触发者是时钟跳变(武装那一刻钟
// 快了几年,NTP 校回来之后这张挂起永不过期)。两个方向都要钉:上限之内照常武装,
// 上限之外报错。
func TestHoldExpiryFarInTheFutureIsRejected(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	ceiling := now.Add(MaintenanceHoldDuration + maintenanceHoldClockSkewGrace)

	for name, tc := range map[string]struct {
		expiresAt time.Time
		wantArmed bool
	}{
		"刚刚武装(now+15m)": {now.Add(MaintenanceHoldDuration), true},
		"时钟步进了一下,仍在宽限内": {ceiling, true},
		"刚过上限一秒":        {ceiling.Add(time.Second), false},
		"钟快了一年":         {now.AddDate(1, 0, 0), false},
		"钟快了十年":         {now.AddDate(10, 0, 0), false},
	} {
		paths := holdPaths(t.TempDir())
		writeHoldFile(t, paths.MaintenanceHold, MaintenanceHold{
			SchemaVersion: 1,
			Reason:        HoldReasonUpgrade,
			ExpiresAt:     tc.expiresAt,
		})
		_, armed, err := OpenStore(paths).LoadMaintenanceHold(now)
		if tc.wantArmed && (!armed || err != nil) {
			t.Fatalf("%s:应照常武装,armed=%v err=%v", name, armed, err)
		}
		if !tc.wantArmed && (armed || err == nil) {
			t.Fatalf("%s:必须被拒,armed=%v err=%v", name, armed, err)
		}
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

// 写不进去必须**报错**,不许悄悄成功 —— Task 4 的退回规则(挂起写失败就退回写
// desired=off、拆除照常做完)整条挂在这个返回值上:Arm 谎报成功,升级就会在
// 「既没挂起也没 desired=off」的状态下换二进制,而那正是设计取舍三点名要避免的
// 新失效模式。同理 Clear 删不掉也要说出来(调用方在拆除路径上当警告处理)。
func TestHoldIOFailuresAreReported(t *testing.T) {
	root := t.TempDir()

	// 拿一个普通文件冒充状态目录:MkdirAll 必然 ENOTDIR。
	notADir := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := testPaths(root)
	paths.MaintenanceHold = filepath.Join(notADir, "maintenance-hold.json")
	if err := OpenStore(paths).ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err == nil {
		t.Fatal("状态目录建不出来时武装必须报错")
	}

	// 目录建得出来、但**写**失败(挂起路径本身是个非空目录 ⇒ 原子替换的 rename
	// 必然失败)。必须单列这一种:上面那种在 MkdirAll 就返回了,根本走不到写,
	// 于是「Arm 吞掉写错误」这个改动在只有上一种用例时**照样绿**(实测)。
	blocked := holdPaths(t.TempDir())
	if err := os.MkdirAll(filepath.Join(blocked.MaintenanceHold, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := OpenStore(blocked).ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err == nil {
		t.Fatal("挂起写不进去时必须报错(Task 4 的退回规则靠这个返回值)")
	}

	// 拿一个非空目录冒充挂起文件:os.Remove 必然 ENOTEMPTY(既不是成功也不是
	// ENOENT,故不该被幂等豁免吃掉)。
	stuck := holdPaths(t.TempDir())
	if err := os.MkdirAll(filepath.Join(stuck.MaintenanceHold, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := OpenStore(stuck).ClearMaintenanceHold(); err == nil {
		t.Fatal("删不掉挂起必须报错,不许假装清好了")
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
