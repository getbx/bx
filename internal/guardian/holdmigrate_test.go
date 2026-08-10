package guardian

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// storeWritesWithRemove 是生产那一组写入动作,只把删除换掉 —— 其余三个仍打在
// 真 Store 上,好让「盘上留下了什么」这类断言仍然是对真文件的观察。
func storeWritesWithRemove(s *Store, remove func() error) legacyMigrationWrites {
	return legacyMigrationWrites{
		arm:    s.ArmMaintenanceHold,
		save:   s.SaveDesired,
		clear:  s.ClearMaintenanceHold,
		remove: remove,
	}
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
	migration, err := s.MigrateLegacyUpgradeIntent(now)
	if err != nil || !migration.Found {
		t.Fatalf("migration=%+v err=%v", migration, err)
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

// **武装挂起必须排在复位 desired 之前,而这个顺序在终态里看不见。**
//
// 两种顺序跑完之后盘上一模一样(desired=on + 一张挂起 + 欠条已删),所以上面那
// 两条各断言一半的用例在**对调之后照样全绿**(复审实测)。差别只存在于崩溃窗口:
//   - 先武装再复位:崩在中间 ⇒ desired 仍是 off、挂起已武装 ⇒ 没有任何东西会起
//     Core,下一次启动恢复重跑一遍就补齐。
//   - 先复位再武装:崩在中间 ⇒ desired=on 而没有挂起 ⇒ 紧接着的 recoverLocked
//     会忠实地在一台二进制刚换到一半的机器上把 Core 起起来。
//
// 观察点因此必须在窗口**内部**:这里记下调用次序,并让第二步失败、看盘上停在
// 哪一半。
func TestLegacyMigrationArmsTheHoldBeforeRestoringDesiredOn(t *testing.T) {
	paths := legacyIntentPaths(t.TempDir())
	s := OpenStore(paths)
	if err := s.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.UpgradeIntent, []byte(`{"schema_version":1,"desired_on":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var order []string
	writes := storeWritesWithRemove(s, func() error { order = append(order, "remove"); return nil })
	arm, save := writes.arm, writes.save
	writes.arm = func(reason string, now time.Time) error {
		order = append(order, "arm")
		return arm(reason, now)
	}
	// 第二步失败 = 崩在窗口里。安全的那一半必须已经落盘。
	saveFailure := errors.New("read-only file system")
	writes.save = func(DesiredState) error {
		order = append(order, "save")
		_ = save // 生产实现留着不调:这一格要的就是它没写成
		return saveFailure
	}

	if _, err := s.migrateLegacyUpgradeIntent(time.Now(), writes); !errors.Is(err, saveFailure) {
		t.Fatalf("desired 写不成必须报错:err=%v", err)
	}
	if len(order) < 2 || order[0] != "arm" || order[1] != "save" {
		t.Fatalf("顺序 = %v,必须先武装挂起再复位 desired", order)
	}
	if _, armed, err := s.LoadMaintenanceHold(time.Now()); err != nil || !armed {
		t.Fatalf("崩在窗口里时挂起必须已经武装(否则会有人在换到一半的二进制上起 Core):armed=%v err=%v", armed, err)
	}
	if slices.Contains(order, "remove") {
		t.Fatalf("写没成功就把欠条删了,那张欠条永久丢失:%v", order)
	}
	if _, err := os.Stat(paths.UpgradeIntent); err != nil {
		t.Fatalf("欠条必须留在盘上等下一次重试:%v", err)
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
	migration, err := s.MigrateLegacyUpgradeIntent(time.Now())
	if err != nil || migration.Found {
		t.Fatalf("migration=%+v err=%v", migration, err)
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
	migration, err := s.MigrateLegacyUpgradeIntent(time.Now())
	if err != nil || !migration.Found {
		t.Fatalf("migration=%+v err=%v", migration, err)
	}
	if migration.RestoredDesiredOn || migration.HoldArmed {
		t.Fatalf("desired_on=false 的欠条什么都不该恢复:%+v", migration)
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
	migration, err := OpenStore(paths).MigrateLegacyUpgradeIntent(time.Now())
	if err == nil || migration.Found {
		t.Fatalf("答不了的问题必须报错:migration=%+v err=%v", migration, err)
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

// 删不掉的欠条**不许**留下一张武装着的挂起。
//
// 复审抓到的:`os.Remove` 失败时原来只记一行日志就 return true —— 于是
// migrateLegacyIntentOnce 打印一句「已迁移」的成功日志,而文件还在盘上。
// 原注释「本次进程只跑这一遍,不会反复刷新过期时间」在**进程内**成立、跨重启
// 不成立,而跨重启正是「删不掉」这件事的定义。叠加两条既有事实就致命:
// recoverLocked 在 intent.HoldArmed 那一支直接 return,而调谐器只观察没有执行权
// —— 一张武装着的挂起压制的是**整个进程生命周期**,不是 15 分钟。净结果是
// 保护再也回不来,而日志说成功。
//
// 处置:删不掉 ⇒ 把挂起撤掉。desired=on 照样恢复(那一半是欠条的全部意义),
// 而挂起的职责只是「拦住这一次启动」—— 拦不住有尽头的东西就不该拦。
func TestUndeletableLegacyIntentDoesNotLeaveAHoldArmed(t *testing.T) {
	paths := legacyIntentPaths(t.TempDir())
	s := OpenStore(paths)
	if err := s.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.UpgradeIntent, []byte(`{"schema_version":1,"desired_on":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// 用注入的删除器造「删不掉」,而不是 chmod:root 身份下 chmod 拦不住 unlink,
	// 那种造法会在 CI 的 root 容器里变成假绿(hold_test.go 里同款教训)。
	removeFailure := errors.New("read-only file system")
	migration, err := s.migrateLegacyUpgradeIntent(time.Now(), storeWritesWithRemove(s, func() error { return removeFailure }))
	if !errors.Is(err, removeFailure) {
		t.Fatalf("删不掉必须报错(否则日志会说成功):err=%v", err)
	}
	if migration.HoldArmed {
		t.Fatal("删不掉的欠条会在每次重启时重新武装挂起 —— 那是永久压制,不是 15 分钟")
	}
	if _, armed, err := s.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("盘上不得留下武装着的挂起:armed=%v err=%v", armed, err)
	}
	// 而「用户本来要保护」这一半照样要恢复 —— 它不依赖删得掉删不掉。
	if desired, err := s.LoadDesired(); err != nil || desired != DesiredOn {
		t.Fatalf("desired 没恢复:%q err=%v", desired, err)
	}
}

// 太老的欠条不作数。
//
// 复审抓到的:一张陈旧欠条(上一次 bx down 报错留下的,或菜单 Turn Off 失败留下
// 的——后者根本不经过 CLI)本来只在下一次 app-install 时才咬人;迁移把它提前到
// **下一次 Guardian 启动**,而升级本身就会重启 Guardian。「把用户明确的 off 翻回
// on」正是这一整期要消灭的 bug 形状,不能靠「只兼容一个版本」的承诺发货。
//
// 24 小时是个宽绰的窗口:一台真的正处在升级中途的机器,那个文件是几分钟前写的。
// 反方向的代价是安全的 —— 保护不自动恢复,用户自己 bx up。
func TestStaleLegacyUpgradeIntentIsIgnoredNotObeyed(t *testing.T) {
	paths := legacyIntentPaths(t.TempDir())
	s := OpenStore(paths)
	if err := s.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.UpgradeIntent, []byte(`{"schema_version":1,"desired_on":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-legacyUpgradeIntentMaxAge - time.Hour)
	if err := os.Chtimes(paths.UpgradeIntent, stale, stale); err != nil {
		t.Fatal(err)
	}

	migration, err := s.MigrateLegacyUpgradeIntent(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !migration.Found || !migration.Stale {
		t.Fatalf("过期的欠条必须被认出来并如实报出:%+v", migration)
	}
	if desired, err := s.LoadDesired(); err != nil || desired != DesiredOff {
		t.Fatalf("陈旧欠条把用户明确的 off 翻回了 on:%q err=%v", desired, err)
	}
	if _, armed, err := s.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("陈旧欠条不该武装挂起:armed=%v err=%v", armed, err)
	}
	if _, err := os.Stat(paths.UpgradeIntent); !os.IsNotExist(err) {
		t.Fatalf("认定作废之后就该删掉,否则每次启动都重判一遍: %v", err)
	}
}

// 另一半输入空间:**窗口之内**的欠条照旧完整迁移。
//
// 没有这一条,把 legacyUpgradeIntentMaxAge 设成 0(或让判定恒真)会让上面那条
// 照样绿,而迁移垫片整个失效。
func TestLegacyUpgradeIntentInsideTheWindowIsStillMigrated(t *testing.T) {
	paths := legacyIntentPaths(t.TempDir())
	s := OpenStore(paths)
	if err := s.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.UpgradeIntent, []byte(`{"schema_version":1,"desired_on":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// 年龄写成**绝对值**(一小时前),不是 maxAge 的相对偏移:相对写法会跟着
	// 常量一起缩水 —— 把 legacyUpgradeIntentMaxAge 改成 0 时它照样绿(实测)。
	// 一小时也是真实需求的下限:正在升级的机器,那个文件是几分钟前写的。
	recent := time.Now().Add(-time.Hour)
	if err := os.Chtimes(paths.UpgradeIntent, recent, recent); err != nil {
		t.Fatal(err)
	}

	migration, err := s.MigrateLegacyUpgradeIntent(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if migration.Stale || !migration.RestoredDesiredOn || !migration.HoldArmed {
		t.Fatalf("窗口内的欠条必须两半都恢复:%+v", migration)
	}
	if desired, err := s.LoadDesired(); err != nil || desired != DesiredOn {
		t.Fatalf("desired 没恢复:%q err=%v", desired, err)
	}
}

// 迁移在真机上留下的**唯一**痕迹是那行日志,所以它必须说实话。
//
// 一张 desired_on=false 的欠条什么都不恢复、也不武装挂起,而原来的日志无条件打
// 「guardian_legacy_upgrade_intent_migrated hold_reason=legacy_upgrade」。
func TestMigrationLogDoesNotClaimAHoldItNeverArmed(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.legacyIntentPath, []byte(`{"schema_version":1,"desired_on":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	restore := swapGuardianLogOutput(&logged)
	if err := env.manager.Recover(context.Background()); err != nil {
		restore()
		t.Fatal(err)
	}
	restore()

	line := logged.String()
	if !strings.Contains(line, "guardian_legacy_upgrade_intent_migrated") {
		t.Fatalf("迁移过就得留痕:%s", line)
	}
	// 查 `hold_reason=` 而不是 HoldReasonLegacyUpgrade 本身:后者("legacy_upgrade")
	// 是日志键 guardian_legacy_upgrade_intent_migrated 的子串,那样断言恒红。
	if strings.Contains(line, "hold_armed=true") || strings.Contains(line, "hold_reason=") {
		t.Fatalf("没武装过挂起就不许在日志里说武装了:%s", line)
	}
	if !strings.Contains(line, "restored_desired_on=false") {
		t.Fatalf("也得说清什么都没恢复:%s", line)
	}
}
