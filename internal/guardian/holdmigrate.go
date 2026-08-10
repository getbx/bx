package guardian

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

// legacyUpgradeIntentMaxAge 是一张 legacy 欠条还算数的最长年龄。
//
// 迁移做的事是**把 desired 从 off 翻回 on**,也就是覆盖盘上此刻写着的意图。
// 那在「机器正处在升级中途」时是对的(那个 off 是失败的升级自己写的),在
// 「用户前天自己关掉了保护」时是错的 —— 而这两种情形在盘上长得一模一样,
// 唯一能分开它们的信号是**这个文件有多新**。
//
// 陈旧欠条真的会出现:一次报错的 bx down、或一次失败的菜单 Turn Off(后者根本
// 不经过 CLI)都会把它留在盘上。以前它只在下一次 app-install 时才咬人;迁移把它
// 提前到**下一次 Guardian 启动**,而升级本身就要重启 Guardian。
//
// 24 小时是刻意宽绰的:一台真的正在升级的机器,这个文件是几分钟前写的。反方向的
// 代价是安全的 —— 关机超过一天再开机的那台机器不自动恢复保护,用户自己 bx up。
const legacyUpgradeIntentMaxAge = 24 * time.Hour

// LegacyMigration 描述一次 legacy 欠条迁移**实际做了什么**。
//
// 不用一个 bool:日志是这件事在真机上留下的唯一痕迹,而「找到了但已作废」
// 「恢复了 desired 但没武装挂起」「武装过又撤了」这几种结果,bool 一个都说不出来。
type LegacyMigration struct {
	// Found 表示盘上确实有一张 legacy 欠条。
	Found bool
	// Stale 表示它太老、按用户的当前意图处理(什么都不恢复)。
	Stale bool
	// RestoredDesiredOn 表示 desired 被复位成了 on。
	RestoredDesiredOn bool
	// HoldArmed 表示**留在盘上**的挂起是这次武装的。删不掉欠条时它会被撤回,
	// 那种情况下这里是 false —— 它描述的是终态,不是中间做过什么。
	HoldArmed bool
}

// MigrateLegacyUpgradeIntent 把盘上一张旧的升级欠条(upgrade-intent.json)
// 翻译成本期的表示法,然后删掉它。**只保留一个版本的兼容**。
//
// 为什么不能只当作挂起:欠条携带的是「用户本来要保护」,而此刻盘上的 desired
// 正是那次失败自己写下的 off。只武装挂起 = 压制 15 分钟然后什么都没恢复,
// 恰好丢掉这个文件存在的全部理由。所以两半都要:挂起武装 + desired 复位 on。
//
// **两半的先后是有讲究的,与计划里给的顺序相反,理由如下**:
// 中途崩掉的那一刻,盘上必须是一个不比现状更危险的状态。
//   - 先武装再复位:崩在中间 ⇒ desired 仍是 off、挂起已武装、欠条还在盘上。
//     没有任何东西会起 Core,下一次启动恢复重跑一遍就补齐了(幂等)。
//   - 先复位再武装:崩在中间 ⇒ desired=on 而没有挂起,而紧接着的 recoverLocked
//     会忠实地在一台二进制刚换到一半的机器上把 Core 起起来 —— 那正是维护挂起
//     整个存在的理由。
//
// **删除永远排在最后**:反过来先删则一次崩溃就永久丢失那张欠条。
func (s *Store) MigrateLegacyUpgradeIntent(now time.Time) (LegacyMigration, error) {
	return s.migrateLegacyUpgradeIntent(now, func() error { return os.Remove(s.paths.UpgradeIntent) })
}

// migrateLegacyUpgradeIntent 把删除动作做成参数,只为一件事:「删不掉」这条分支
// 必须能被测到,而用 chmod 造它在 root 身份下拦不住 unlink —— 那种造法会在 CI 的
// root 容器里静静变绿(hold_test.go 里同款教训)。生产入口就在上面一行。
func (s *Store) migrateLegacyUpgradeIntent(now time.Time, remove func() error) (LegacyMigration, error) {
	// 路径没配 **不是**「没有欠条」,是这个 Store 答不了这个问题(与 hold.go 的
	// Arm/Load/ClearMaintenanceHold 同一条取舍)。一个自信的 false 会把「这台机器
	// 的保护该不该恢复」静悄悄地答成「不该」—— 正是欠条存在的理由要消灭的那种谎。
	if s.paths.UpgradeIntent == "" {
		return LegacyMigration{}, fmt.Errorf("guardian upgrade intent path required")
	}
	info, err := os.Stat(s.paths.UpgradeIntent)
	if os.IsNotExist(err) {
		return LegacyMigration{}, nil
	}
	if err != nil {
		return LegacyMigration{}, fmt.Errorf("stat legacy upgrade intent: %w", err)
	}
	result := LegacyMigration{Found: true}
	// 年龄闸门排在**任何写入之前**:作废的欠条一个字节都不许改盘。
	// 认定作废之后照样删掉它 —— 留着只会让每一次启动重判一遍同一件事,
	// 而这个仓库只承诺一个版本的兼容。
	if now.Sub(info.ModTime()) > legacyUpgradeIntentMaxAge {
		result.Stale = true
		if err := remove(); err != nil && !os.IsNotExist(err) {
			return result, fmt.Errorf("remove stale legacy upgrade intent: %w", err)
		}
		return result, nil
	}
	b, err := os.ReadFile(s.paths.UpgradeIntent)
	if os.IsNotExist(err) {
		return LegacyMigration{}, nil
	}
	if err != nil {
		return result, fmt.Errorf("read legacy upgrade intent: %w", err)
	}
	// 「存在但坏了 ⇒ 仍算欠条」:这个文件只在 desired_on=true 时被写出来,所以
	// 「在、却读不懂」几乎必然是一张真欠条(已退休的 LoadUpgradeIntent 同判)。
	// 往「多恢复一次保护」偏,不往「永远不再保护」偏。
	desiredOn := true
	var intent UpgradeIntent
	if err := json.Unmarshal(b, &intent); err == nil && intent.SchemaVersion == 1 {
		desiredOn = intent.DesiredOn
	}
	if desiredOn {
		if err := s.ArmMaintenanceHold(HoldReasonLegacyUpgrade, now); err != nil {
			return result, err
		}
		result.HoldArmed = true
		if err := s.SaveDesired(DesiredOn); err != nil {
			return result, err
		}
		result.RestoredDesiredOn = true
	}
	if err := remove(); err != nil && !os.IsNotExist(err) {
		// **删不掉 ⇒ 把挂起撤回。**
		//
		// 挂起的职责是「拦住这一次启动,15 分钟内把它交还」,而它的尽头靠的是
		// 欠条被删掉:删不掉的话每一次 Guardian 启动都会重新武装一次,过期时刻
		// 跟着往后推。叠加两条既有事实就致命 —— recoverLocked 在
		// intent.HoldArmed 那一支直接 return,调谐器又只观察没有执行权:一张
		// 武装着的挂起压制的是**整个进程生命周期**。于是「删不掉」就等于
		// 「保护再也不回来」,而以前这里还报成功。
		//
		// desired=on 那一半保留(它是欠条的全部意义,且不依赖删得掉与否);
		// 拦不住有尽头的东西就不拦。撤回本身失败只能记日志 —— 没有别的手段了。
		if result.HoldArmed {
			if _, clearErr := s.ClearMaintenanceHold(); clearErr != nil {
				log.Printf("guardian_legacy_upgrade_intent_hold_rollback_failed err=%v", clearErr)
			} else {
				result.HoldArmed = false
			}
		}
		return result, fmt.Errorf("remove legacy upgrade intent: %w", err)
	}
	return result, nil
}
