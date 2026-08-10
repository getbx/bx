package guardian

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer 是 swapGuardianLogOutput 的接收端。加锁不是洁癖:Manager 会从
// goroutine 里打日志,而 bytes.Buffer 不是并发安全的 —— 不加锁的话本文件在
// -race 下会随机炸,而那种红是假的。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func countLines(logged, needle string) int {
	return strings.Count(logged, needle)
}

// 武装必须留下一行:**它是「什么时候、为什么、什么时候失效」唯一的记录**。
// 武装挂起的是 CLI 进程,而 CLI 的日志写在终端 stderr 上,一次升级结束就没了。
func TestArmedHoldIsLoggedOnceNotOnEveryRead(t *testing.T) {
	env := newManagerTestEnv(t)
	now := time.Now()
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, now); err != nil {
		t.Fatal(err)
	}
	var logged syncBuffer
	restore := swapGuardianLogOutput(&logged)
	// 二十次读 = 调谐环十分钟,或者菜单开着的四十秒。真机上一天几万次。
	for i := 0; i < 20; i++ {
		if _, err := env.manager.loadIntentSnapshot(now.Add(time.Duration(i) * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	restore()
	out := logged.String()
	if got := countLines(out, "guardian_maintenance_hold_armed"); got != 1 {
		t.Fatalf("武装该记且只记一行,实际 %d 行:%q", got, out)
	}
	if !strings.Contains(out, "reason="+HoldReasonUpgrade) {
		t.Fatalf("那一行没说为什么挂起:%q", out)
	}
	if !strings.Contains(out, "expires_at=") {
		t.Fatalf("那一行没说什么时候失效 —— 排查时最要紧的一半:%q", out)
	}
}

// **本次改动最重要的一条。** 过期在读取时判,没有定时器、没有事件,在这行字
// 之前它完全无声;而「升级崩在半路、挂起自己过期了」正是人最需要看到的结局。
func TestExpiredHoldIsLoggedOnceWhenObserved(t *testing.T) {
	env := newManagerTestEnv(t)
	armedAt := time.Now()
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, armedAt); err != nil {
		t.Fatal(err)
	}
	// 绝对时刻,不是「比 MaintenanceHoldDuration 晚一点」:后者会跟着常量一起
	// 挪动,把常量改小照样绿(本计划点名的那类 fixture)。
	afterExpiry := armedAt.Add(MaintenanceHoldDuration).Add(time.Second)
	var logged syncBuffer
	restore := swapGuardianLogOutput(&logged)
	for i := 0; i < 20; i++ {
		if _, err := env.manager.loadIntentSnapshot(afterExpiry.Add(time.Duration(i) * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	restore()
	out := logged.String()
	if got := countLines(out, "guardian_maintenance_hold_expired"); got != 1 {
		t.Fatalf("过期该记且只记一行,实际 %d 行:%q", got, out)
	}
	if !strings.Contains(out, "reason="+HoldReasonUpgrade) {
		t.Fatalf("过期那一行没说是哪张挂起:%q", out)
	}
}

// 同一张挂起先武装、后过期 = 两条各记一次的事。
//
// 只测其中一半的话,一个「记过一行就再也不记」的实现照样全绿 —— 而它恰好会
// 吃掉过期那一行,也就是本次改动唯一非有不可的那一行。
func TestHoldLogsArmingAndThenExpiryForTheSameHold(t *testing.T) {
	env := newManagerTestEnv(t)
	armedAt := time.Now()
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, armedAt); err != nil {
		t.Fatal(err)
	}
	var logged syncBuffer
	restore := swapGuardianLogOutput(&logged)
	for i := 0; i < 5; i++ {
		if _, err := env.manager.loadIntentSnapshot(armedAt.Add(time.Duration(i) * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	afterExpiry := armedAt.Add(MaintenanceHoldDuration).Add(time.Second)
	for i := 0; i < 5; i++ {
		if _, err := env.manager.loadIntentSnapshot(afterExpiry.Add(time.Duration(i) * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	restore()
	out := logged.String()
	armed := strings.Index(out, "guardian_maintenance_hold_armed")
	expired := strings.Index(out, "guardian_maintenance_hold_expired")
	if armed < 0 || expired < 0 {
		t.Fatalf("武装与过期各该有一行,实际:%q", out)
	}
	if armed > expired {
		t.Fatalf("顺序反了 —— 先武装后过期:%q", out)
	}
	if countLines(out, "guardian_maintenance_hold_armed") != 1 ||
		countLines(out, "guardian_maintenance_hold_expired") != 1 {
		t.Fatalf("各只该一行:%q", out)
	}
}

// 换一张新挂起(升级重试)⇒ 一切从头,再记一行。去重的键是挂起的身份,
// 不是「这个进程记过没有」。
func TestANewHoldGetsItsOwnArmedLine(t *testing.T) {
	env := newManagerTestEnv(t)
	first := time.Now()
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, first); err != nil {
		t.Fatal(err)
	}
	var logged syncBuffer
	restore := swapGuardianLogOutput(&logged)
	if _, err := env.manager.loadIntentSnapshot(first); err != nil {
		t.Fatal(err)
	}
	second := first.Add(time.Minute)
	if err := env.store.ArmMaintenanceHold(HoldReasonLegacyUpgrade, second); err != nil {
		t.Fatal(err)
	}
	if _, err := env.manager.loadIntentSnapshot(second); err != nil {
		t.Fatal(err)
	}
	restore()
	out := logged.String()
	if got := countLines(out, "guardian_maintenance_hold_armed"); got != 2 {
		t.Fatalf("两张不同的挂起该各记一行,实际 %d 行:%q", got, out)
	}
	if !strings.Contains(out, "reason="+HoldReasonLegacyUpgrade) {
		t.Fatalf("第二张挂起的来由没记下:%q", out)
	}
}

// 一台**没有挂起**的健康机器上,这条线一个字都不写。
//
// 这不是省字节:一行每轮都出现的日志会把这个词训练成噪声,而噪声会训练人忽略
// 它 —— 与 cli.go 的 observerForPlatform、reconcile 循环「只在判断变化时打」
// 同一条纪律。这一期加的每一行都必须过这一关。
func TestHealthyMachineWritesNoHoldLifecycleLines(t *testing.T) {
	env := newManagerTestEnv(t)
	now := time.Now()
	var logged syncBuffer
	restore := swapGuardianLogOutput(&logged)
	for i := 0; i < 50; i++ {
		if _, err := env.manager.loadIntentSnapshot(now.Add(time.Duration(i) * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	restore()
	if out := logged.String(); strings.Contains(out, "guardian_maintenance_hold") {
		t.Fatalf("没有挂起的机器上不许出现挂起的生命周期行:%q", out)
	}
}

// 「读不出来」不许被记成「过期了」或「没有挂起」。
//
// 这是 hold.go 三返回值设计的同一条纪律:一张坏挂起在 fail-closed 的消费者
// 眼里等同永久压制,把它记成「已过期」会把排查的人送去完全相反的方向。
//
// **诚实标注:这一条今天是双重保险下的绿,变异验证过。** 把 observe 挪到
// loadIntentSnapshot 的 err 检查之前,它照样 PASS —— 因为 LoadIntentSnapshot
// 出错时返回的是零值 IntentSnapshot,而零到期时刻在 holdObserver 里本就走
// 「没有挂起」那一支。所以它钉住的是**行为**(读不出来 ⇒ 不写生命周期行),
// 不是「err 那道检查还在」。别把它当成后者的守卫。
func TestUnreadableHoldWritesNoLifecycleLine(t *testing.T) {
	env := newManagerTestEnv(t)
	now := time.Now()
	breakHoldRead(t, env)
	var logged syncBuffer
	restore := swapGuardianLogOutput(&logged)
	if _, err := env.manager.loadIntentSnapshot(now); err == nil {
		t.Fatal("坏挂起必须报错,否则这条测试测的是别的东西")
	}
	restore()
	if out := logged.String(); strings.Contains(out, "guardian_maintenance_hold_armed") ||
		strings.Contains(out, "guardian_maintenance_hold_expired") {
		t.Fatalf("读不出来不是「武装着」也不是「过期了」:%q", out)
	}
}

// 销挂起要说清是**哪条用户动作**要求的:盘上看不出区别,而「用户点了 Turn On」
// 与「升级自己那次停机」在排查时是完全不同的两个故事。
func TestExplicitUpLogsWhoClearedTheHold(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	var logged syncBuffer
	restore := swapGuardianLogOutput(&logged)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	restore()
	out := logged.String()
	// **断言字面量,不是 "by="+holdClearedByUp。** 后者拿常量去证常量:把
	// holdClearedByUp 改成 "down" 整套测试照样绿,而一个对着 bx up 记 by=down 的
	// Guardian 会直接发出去。这些词进的是用户手工 grep 的日志,字面量就是契约。
	if !strings.Contains(out, "guardian_maintenance_hold_cleared by=up") {
		t.Fatalf("显式 up 销挂起没留痕:%q", out)
	}
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("挂起必须真的被清掉:armed=%v err=%v", armed, err)
	}
}

func TestExplicitDownLogsWhoClearedTheHold(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	var logged syncBuffer
	restore := swapGuardianLogOutput(&logged)
	if err := env.manager.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	restore()
	if out := logged.String(); !strings.Contains(out, "guardian_maintenance_hold_cleared by=down") {
		t.Fatalf("显式 down 销挂起没留痕:%q", out)
	}
}

// **升级自己那次 Down 不许被记成用户清的** —— 它根本没清(markMaintenanceStop
// 把这次 Down 标成维护),记一行「已清除」会让排查的人以为挂起没了,而它还在。
func TestMaintenanceDownDoesNotLogAClear(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	var logged syncBuffer
	restore := swapGuardianLogOutput(&logged)
	if err := env.manager.Down(withMaintenanceStop(context.Background())); err != nil {
		t.Fatal(err)
	}
	restore()
	if out := logged.String(); strings.Contains(out, "guardian_maintenance_hold_cleared") {
		t.Fatalf("维护自己那次 Down 没清挂起,不许记「已清除」:%q", out)
	}
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || !armed {
		t.Fatalf("维护那次 Down 之后挂起必须还在:armed=%v err=%v", armed, err)
	}
}

// 三个 actor 词汇逐字进日志,而用户是手工 grep 它们的。
//
// 上面几条断言的是字面量,所以拧错常量会红;这一条补的是另一半 —— 万一将来
// 有人「统一」成别的拼法(连字符、大写),这里会立刻指出契约变了。
func TestHoldClearActorVocabularyIsStable(t *testing.T) {
	for name, got := range map[string]string{
		"up": holdClearedByUp, "down": holdClearedByDown, "migrate": holdClearedByMigrate,
	} {
		if got != name {
			t.Errorf("actor 词汇变了:想要 %q,实际 %q", name, got)
		}
	}
}

// **盘上本来就没有挂起时,不许说「已清除」。**
//
// 销挂起跑在每一次 bx up/down/migrate 上,不管有没有挂起。无条件记一行会让
// grep 这个词在健康机器上恒有命中,读的人很快学会它什么也不代表 —— 与
// _armed/_expired 刻意避开的是同一种噪声训练。
func TestUpWithoutAHoldWritesNoClearedLine(t *testing.T) {
	env := newManagerTestEnv(t)
	if _, armed, err := env.store.LoadMaintenanceHold(time.Now()); err != nil || armed {
		t.Fatalf("测试前提不成立:这台机器本该没有挂起,armed=%v err=%v", armed, err)
	}
	var logged syncBuffer
	restore := swapGuardianLogOutput(&logged)
	if err := env.manager.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	restore()
	if out := logged.String(); strings.Contains(out, "guardian_maintenance_hold_cleared") {
		t.Fatalf("本来就没有挂起,不许声称清掉了一张:%q", out)
	}
}

// poisonedIntentStore 在**返回错误的同时**交出一份看起来武装着的快照。
//
// 这正是一次半解析成功或读到一半被改写的读取的样子。它存在的理由是把
// TestUnreadableHoldWritesNoLifecycleLine 从「双重保险下的绿」变成真守卫:
// 真实的 *Store 出错时返回的是零值快照,于是那条断言即使去掉 err 检查也照样绿。
type poisonedIntentStore struct {
	DesiredStore
}

func (s *poisonedIntentStore) LoadIntentSnapshot(now time.Time) (IntentSnapshot, error) {
	return IntentSnapshot{
			Desired:   DesiredOn,
			Hold:      MaintenanceHold{SchemaVersion: 1, Reason: HoldReasonUpgrade, ExpiresAt: now.Add(time.Minute)},
			HoldArmed: true,
		},
		fmt.Errorf("%w: %w", ErrMaintenanceHoldUnreadable, errors.New("unexpected EOF"))
}

// 读失败时,哪怕快照里装着一张像模像样的挂起,也一个字都不许记。
//
// 「读不出来」不是「武装着」,更不是「过期了」——把它记成后者会把排查的人送去
// 完全相反的方向。这一条钉的是 loadIntentSnapshot 里那道 err 检查本身。
func TestUnreadableHoldWritesNoLineEvenWhenTheSnapshotLooksArmed(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.store = &poisonedIntentStore{DesiredStore: env.store}
	var logged syncBuffer
	restore := swapGuardianLogOutput(&logged)
	if _, err := env.manager.loadIntentSnapshot(time.Now()); err == nil {
		t.Fatal("这个替身必须报错,否则测的是别的东西")
	}
	restore()
	if out := logged.String(); strings.Contains(out, "guardian_maintenance_hold") {
		t.Fatalf("读不出来时不许记任何生命周期行:%q", out)
	}
}
