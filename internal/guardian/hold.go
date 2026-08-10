package guardian

import (
	"encoding/json"
	"errors"
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

// maintenanceHoldClockSkewGrace 是判「过期时刻远得不合理」时给时钟留的余量:
// 武装与读取之间机器的钟可能被 NTP 步进一下(正常量级是毫秒到秒),不该因此
// 把一张好挂起判成坏的;而它拦的是小时/年那个量级的跳变。
const maintenanceHoldClockSkewGrace = time.Minute

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
// 已退休的 LoadUpgradeIntent 把它们压成两个 bool,于是 EACCES/EIO 与「文件不在」
// 返回同一个答案 —— 那与它自己的注释正好相反,也正是这份设计要消灭的读取偏置。
//
// **读取偏置在这里是刻意选的:只有 ENOENT 才是「确知没有挂起」,其余一切读失败
// 都报错。** 调用方对 err 一律 fail-closed(不起 Core、不收敛),因为挂起说的是
// 「此刻不该有人动手」,而「问不出来」时动手正是它要拦的那件事(设计缺陷⑤的
// intent_unreadable 栅栏)。这与本仓库「Unknown ≠ False」同一条纪律
// (internal/observe 的 Tristate)。
//
// 过期在**读取时**判,不设定时器:进程重启后照样成立。过期的文件刻意不在这里
// 删 —— 读路径不许因为一次 unlink 失败而失败;清理由 bx up/bx down/uninstall 做。
//
// **返回的 MaintenanceHold 在每种情形下装着什么**(调用方必须只看第二个返回值,
// 不许用 `hold.Reason != ""` 之类去替代它):
//   - 没有挂起(ENOENT):零值,armed=false,err=nil
//   - 任何错误:零值,armed=false,err!=nil
//   - **挂起过期了:内容原样返回**,armed=false,err=nil —— 好让 bx status 能说出
//     「一张 upgrade 挂起在 12:15 过期了」而不只是「没有挂起」。正因为它非零,
//     `if hold.Reason != ""` 会把过期挂起读成还武装着。
func (s *Store) LoadMaintenanceHold(now time.Time) (MaintenanceHold, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 路径没配 **不是**「没有挂起」,是这个 Store 答不了这个问题 —— 与 ENOENT
	// 完全不同,后者是问过盘、确知没有。返回一个自信的 false 正是本文件存在的
	// 理由要消灭的那种谎:本仓库到处都有只填一两个路径的 guardian.Paths{…} 替身,
	// 拿它们写「显式 down 清挂起」「有挂起就不起 Core」这类测试会**空洞地绿** ——
	// 没路径 ⇒ 从不武装 ⇒ 没得清 ⇒ 栅栏永不触发。故与 ArmMaintenanceHold 一致:报错。
	if s.paths.MaintenanceHold == "" {
		return MaintenanceHold{}, false, fmt.Errorf("guardian maintenance hold path required")
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
	// 「没有一张挂起可以无限期压制保护」这条不变量,零值只是它的一个角落:
	// 一个远在未来的 ExpiresAt 同样是无限期压制。现实里的触发者不是恶意而是
	// **时钟跳变** —— 武装那一刻机器的钟偶然快了几年,NTP 校回来之后这张挂起
	// 就实际上永不过期;在 Task 2 的 fail-closed 栅栏下,那台机器会一直
	// 「desired=on 却没有保护」,直到用户自己跑 bx up / bx down。
	//
	// 上限 = now + MaintenanceHoldDuration(+ 一分钟宽限,容下 NTP 的正常步进,
	// 而拦得住小时/年这个量级的偏差):任何一张诚实武装的挂起,在任何一次读取
	// 时都不可能比「此刻刚武装」更晚过期。
	//
	// **这里三条拒绝分支(零过期 / 未来过久 / 未知 schema / 坏 reason)在
	// fail-closed 的消费者眼里本身就是永久压制** —— 这是有意的取舍:它是**吵闹的**
	// (报错会进日志与 bx status,不像一张悄悄生效的坏挂起),而且**可恢复**
	// (ClearMaintenanceHold 不看内容,任何一条显式 up/down 都能把它删掉)。
	// Task 2 的栅栏设计依赖这一点:错误 ⇒ 停手 + 说清楚,而不是猜。
	if hold.ExpiresAt.IsZero() {
		return MaintenanceHold{}, false, fmt.Errorf("maintenance hold has no expiry")
	}
	if hold.ExpiresAt.After(now.Add(MaintenanceHoldDuration + maintenanceHoldClockSkewGrace)) {
		return MaintenanceHold{}, false, fmt.Errorf("maintenance hold expiry too far in the future")
	}
	return hold, hold.ExpiresAt.After(now), nil
}

// IntentSnapshot 是**一次**磁盘读拿到的完整意图:用户要什么,以及此刻允不允许动手。
//
// 为什么必须同源:调谐器今天读的是内存里的 m.Status().Desired,而内存**已经会
// 撒谎** —— needsAttention 会把调用方传进来的常量写进 status.Desired
// (manager.go:1396-1399),好几处传的是字面量 DesiredOn 而磁盘写着 off。挂起从
// 磁盘读、desired 从内存读,一轮之内就能出现两者互不相干的组合。
//
// **它是一个瞬间的快照,不是一段区间内的保证**:CLI 在另一个进程里随时可能改写
// 这两个文件,本期不引入锁(整文件原子替换 + 最后写者赢)。
type IntentSnapshot struct {
	Desired   DesiredState
	Hold      MaintenanceHold
	HoldArmed bool
}

// ErrMaintenanceHoldUnreadable 标记「读不出来的是**挂起**那一半」。
//
// 两半的失败后果不同,所以必须分得开:
//   - desired 读不出来 = 连用户要什么都不知道 ⇒ 启动恢复照旧置
//     recoveryBlocked=true(既有不变量,本期不放宽)。
//   - 挂起读不出来 ⇒ 一律 fail-closed(不起 Core),但**绝不置 recoveryBlocked**。
//     理由是一条闭环:LoadMaintenanceHold 对坏 schema / 坏 reason / 时钟跳变造出
//     的远期过期一律报错,而这份设计给出的唯一恢复路径是「任何一条显式 up/down
//     都能清掉它」(ClearMaintenanceHold 不看内容)。recoveryBlocked 为真时
//     Manager.Down 与 Up 的第一句就返回 errRecoveryIncomplete —— 那条恢复路径
//     会被自己掐断,等于用一个新文件把 2026-08-04 那次「用户 71 分钟关不掉保护」
//     原样复活。
var ErrMaintenanceHoldUnreadable = errors.New("maintenance hold unreadable")

// intentReadFailureCode 把「读不出意图」翻译成**对外发布**的失败码。
//
// 两半必须有各自的码,否则违反「失败必须留下可操作线索」:一张读不出来的挂起
// 若报成 desired_state_read_failed,发布出去的码点名的是 guardian-state.json,
// 而用户唯一的出路是处理 maintenance-hold.json —— 指引把人送去另一个文件,
// 比不给指引更糟。
//
// intent_unreadable 这个名字取自设计缺陷⑤给调谐器第五道栅栏预留的词,Task 3
// 接栅栏时应沿用同一个码。
func intentReadFailureCode(err error) string {
	if errors.Is(err, ErrMaintenanceHoldUnreadable) {
		return "intent_unreadable"
	}
	return "desired_state_read_failed"
}

// LoadIntentSnapshot 一次读出 desired 与挂起。
func (s *Store) LoadIntentSnapshot(now time.Time) (IntentSnapshot, error) {
	return loadIntentSnapshot(s.LoadDesired, s.LoadMaintenanceHold, now)
}

// loadIntentSnapshot 把组合逻辑(尤其是**哪一半失败了**这个分类)单点拥有。
//
// 测试替身要注入 LoadDesired 的失败,就必须覆盖 LoadIntentSnapshot(否则内嵌的
// *Store 会直接读盘、把注入绕过去);而一旦覆盖,它就会重抄一遍这段组合 ——
// 重抄的那份迟早把 ErrMaintenanceHoldUnreadable 的包装丢掉,于是替身与生产悄悄
// 分叉,而分叉的方向恰好是「测试照样绿」。故这段逻辑不许有第二份。
func loadIntentSnapshot(
	loadDesired func() (DesiredState, error),
	loadHold func(time.Time) (MaintenanceHold, bool, error),
	now time.Time,
) (IntentSnapshot, error) {
	desired, err := loadDesired()
	if err != nil {
		return IntentSnapshot{}, err
	}
	hold, armed, err := loadHold(now)
	if err != nil {
		return IntentSnapshot{}, fmt.Errorf("%w: %w", ErrMaintenanceHoldUnreadable, err)
	}
	return IntentSnapshot{Desired: desired, Hold: hold, HoldArmed: armed}, nil
}

// ClearMaintenanceHold 撤销挂起。文件本来就不在不算失败(幂等)——它跑在
// 「用户明确要关保护」的路径上,而停止不许依赖先成功做成别的事。
//
// 它**不看内容**:坏 schema、坏 reason、过期得离谱的挂起,一样能被一条显式的
// up/down 清掉。LoadMaintenanceHold 那几条拒绝分支之所以敢报错(在 fail-closed
// 的消费者眼里等同压制),依据就是这条恢复路径。
//
// 路径没配同样报错(与 Arm/Load 一致):一个答不了这个问题的 Store 说「清好了」
// 是谎。**调用方在拆除路径上必须把这个错误当警告继续做完拆除**,绝不许据此提前
// 返回 —— 「停止」永远不依赖先成功做成别的事(2026-08-04 那次 71 分钟事故)。
// **第一个返回值是「真的删掉了一张挂起吗」**,而不是「调用成功了吗」。
//
// 它不多花一次 syscall:os.Remove 已经把「删掉了」与「本来就没有」分开了
// (ENOENT 那一支),此前只是算出来又丢掉。有了它,日志才敢说
// guardian_maintenance_hold_cleared —— 否则那行字会印在每一次 bx up/down 上,
// 不管盘上有没有挂起,于是 grep 到它什么也证明不了,而这正是 _armed/_expired
// 两条线花了大力气去避免的噪声训练。
func (s *Store) ClearMaintenanceHold() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paths.MaintenanceHold == "" {
		return false, fmt.Errorf("guardian maintenance hold path required")
	}
	if err := os.Remove(s.paths.MaintenanceHold); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("remove maintenance hold: %w", err)
	}
	return true, nil
}
