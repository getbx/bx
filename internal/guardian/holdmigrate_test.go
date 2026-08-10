package guardian

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// legacyIntentPaths 是 holdPaths 加上一条 legacy 欠条路径。
//
// **三条路径必须一起填**:Store 在 MaintenanceHold 或 UpgradeIntent 没配时
// 报错而不是答「没有」(hold.go 与 holdmigrate.go 的同一条取舍),否则本文件里
// 每一条断言都会平凡地绿 —— 没路径 ⇒ 读不到欠条 ⇒ 什么都不迁移。
func legacyIntentPaths(root string) Paths {
	p := holdPaths(root)
	p.UpgradeIntent = filepath.Join(root, "upgrade-intent.json")
	return p
}

// migrateInterruptedUpgrade 摆出一台**正处在升级中途**、跨过这次切换的机器:
// 盘上是那次升级自己写下的 desired=off,外加旧 CLI 留下的一张欠条。
func migrateInterruptedUpgrade(t *testing.T, now time.Time) (*Store, Paths) {
	t.Helper()
	paths := legacyIntentPaths(t.TempDir())
	s := OpenStore(paths)
	if err := s.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.UpgradeIntent, []byte(`{"schema_version":1,"desired_on":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	migrated, err := s.MigrateLegacyUpgradeIntent(now)
	if err != nil || !migrated {
		t.Fatalf("migrated=%v err=%v", migrated, err)
	}
	return s, paths
}

// 欠条携带的信息是「**用户本来要保护**」,而盘上的 desired 恰恰是那次失败自己
// 写下的 off。这一半单独立一条测试,因为它正是最容易被漏掉的那一半:只武装挂起
// 就是压制 15 分钟、然后什么都不恢复 —— 恰好丢掉这个文件存在的全部理由。
func TestLegacyUpgradeIntentRestoresDesiredOn(t *testing.T) {
	s, _ := migrateInterruptedUpgrade(t, time.Now())
	if desired, err := s.LoadDesired(); err != nil || desired != DesiredOn {
		t.Fatalf("欠条携带的意图没恢复:%q err=%v", desired, err)
	}
}

// 另一半:此刻**不该有人动手**(二进制可能才换到一半)。它与上面那半各自独立
// 转红,一次改坏两处只能证明「有东西坏了」,证明不了两半各自都有人盯着。
func TestLegacyUpgradeIntentArmsMaintenanceHold(t *testing.T) {
	now := time.Now()
	s, _ := migrateInterruptedUpgrade(t, now)
	hold, armed, err := s.LoadMaintenanceHold(now)
	if err != nil || !armed {
		t.Fatalf("没有武装挂起:armed=%v err=%v", armed, err)
	}
	if hold.Reason != HoldReasonLegacyUpgrade {
		t.Fatalf("reason = %q, want %q", hold.Reason, HoldReasonLegacyUpgrade)
	}
}

// 迁移完必须把 legacy 文件删掉:留着它等于每个进程都会再迁移一次
// (而每一次都把挂起的过期时刻往后推),而这个仓库只承诺**一个版本**的兼容。
func TestLegacyUpgradeIntentFileIsRemovedAfterMigration(t *testing.T) {
	_, paths := migrateInterruptedUpgrade(t, time.Now())
	if _, err := os.Stat(paths.UpgradeIntent); !os.IsNotExist(err) {
		t.Fatalf("legacy 欠条没删掉: %v", err)
	}
}

// 没有 legacy 文件时**一个字节都不许写** —— 否则每次启动恢复都会凭空武装一次
// 挂起,把每一台机器的保护压制 15 分钟。
func TestLegacyMigrationIsANoOpWithoutTheLegacyFile(t *testing.T) {
	paths := legacyIntentPaths(t.TempDir())
	s := OpenStore(paths)
	migrated, err := s.MigrateLegacyUpgradeIntent(time.Now())
	if err != nil || migrated {
		t.Fatalf("migrated=%v err=%v", migrated, err)
	}
	if _, armed, err := s.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("凭空武装了挂起:armed=%v err=%v", armed, err)
	}
}

// 「存在但坏了 ⇒ 仍算欠条」—— 这个文件只在 desired_on=true 时被写出来,
// 往「多恢复一次保护」偏,不往「永远不再保护」偏。
func TestCorruptLegacyUpgradeIntentStillCountsAsDebt(t *testing.T) {
	paths := legacyIntentPaths(t.TempDir())
	s := OpenStore(paths)
	if err := s.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.UpgradeIntent, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MigrateLegacyUpgradeIntent(time.Now()); err != nil {
		t.Fatal(err)
	}
	if desired, _ := s.LoadDesired(); desired != DesiredOn {
		t.Fatalf("坏欠条被当成没有欠条:%q", desired)
	}
}

// **另一半输入空间**:一张写着 desired_on=false 的欠条说的是「那次升级开始前
// 用户本来就没开保护」。它什么都不该恢复 —— 既不许把 desired 翻成 on(那是替
// 用户做主开保护),也不许武装挂起(没有保护需要被压制,凭空压 15 分钟只会让
// 一台本来就关着的机器多一段说不清的窗口)。文件照样删掉。
//
// 没有这一条,`if desiredOn` 那道判断整个可以删掉而全套测试照样绿。
func TestLegacyUpgradeIntentSayingProtectionWasOffRestoresNothing(t *testing.T) {
	paths := legacyIntentPaths(t.TempDir())
	s := OpenStore(paths)
	if err := s.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.UpgradeIntent, []byte(`{"schema_version":1,"desired_on":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	migrated, err := s.MigrateLegacyUpgradeIntent(time.Now())
	if err != nil || !migrated {
		t.Fatalf("migrated=%v err=%v", migrated, err)
	}
	if desired, err := s.LoadDesired(); err != nil || desired != DesiredOff {
		t.Fatalf("欠条说用户本来没开保护,不许替他开:%q err=%v", desired, err)
	}
	if _, armed, err := s.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("没有保护要压制,不该武装挂起:armed=%v err=%v", armed, err)
	}
	if _, err := os.Stat(paths.UpgradeIntent); !os.IsNotExist(err) {
		t.Fatalf("legacy 欠条没删掉: %v", err)
	}
}

// 路径没配 **不是**「没有欠条」,是这个 Store 答不了这个问题 —— 与 hold.go 里
// LoadMaintenanceHold / ClearMaintenanceHold 同一条取舍。一个自信的 false 会把
// 「一台机器的保护该不该恢复」这个问题静悄悄地答成「不该」,而这正是欠条存在的
// 理由要消灭的那种谎;它还会让上面几条断言在有人删掉一行路径赋值之后继续绿。
func TestLegacyMigrationWithoutConfiguredPathIsAnError(t *testing.T) {
	paths := holdPaths(t.TempDir()) // 刻意不填 UpgradeIntent
	migrated, err := OpenStore(paths).MigrateLegacyUpgradeIntent(time.Now())
	if err == nil || migrated {
		t.Fatalf("答不了的问题必须报错:migrated=%v err=%v", migrated, err)
	}
}

// 迁移必须跑在启动恢复**之前**:反过来的话 recoverLocked 会先按 desired=off
// 把状态定成 off,再由迁移改盘,机器与状态两张皮。
func TestStartupRecoveryRunsLegacyMigrationFirst(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.legacyIntentPath, []byte(`{"schema_version":1,"desired_on":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.manager.Status().Desired; got != DesiredOn {
		t.Fatalf("启动恢复发布的意图 = %q, want on(迁移排在它后面就会是 off)", got)
	}
	if got := env.runner.startCount(); got != 0 {
		t.Fatalf("迁移出来的挂起没拦住 Core:start = %d", got)
	}
}

// 每个进程只迁移一遍。启动恢复有重试循环(daemon.go 的 retryDaemonRecovery),
// 而一张删不掉的欠条(欠条所在目录只读、EIO……)会让每一次重试都重新武装一次
// 挂起 —— 过期时刻跟着刷新,15 分钟的上限就成了永久压制。
//
// 这里用「把欠条重新放回盘上」来代替那个删不掉的场景:两者对迁移入口是同一件事
// (盘上还有一张欠条),而它不依赖 chmod 在不同 CI 身份下的行为。
func TestLegacyMigrationRunsOnlyOncePerProcess(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.legacyIntentPath, []byte(`{"schema_version":1,"desired_on":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.legacyIntentPath, []byte(`{"schema_version":1,"desired_on":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.manager.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.store.migrateCount(); got != 1 {
		t.Fatalf("迁移跑了 %d 次;每多跑一次都会把挂起的过期时刻往后推一次", got)
	}
}
