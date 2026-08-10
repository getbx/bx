package guardian

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

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
func (s *Store) MigrateLegacyUpgradeIntent(now time.Time) (bool, error) {
	// 路径没配 **不是**「没有欠条」,是这个 Store 答不了这个问题(与 hold.go 的
	// Arm/Load/ClearMaintenanceHold 同一条取舍)。一个自信的 false 会把「这台机器
	// 的保护该不该恢复」静悄悄地答成「不该」—— 正是欠条存在的理由要消灭的那种谎。
	if s.paths.UpgradeIntent == "" {
		return false, fmt.Errorf("guardian upgrade intent path required")
	}
	b, err := os.ReadFile(s.paths.UpgradeIntent)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read legacy upgrade intent: %w", err)
	}
	// 「存在但坏了 ⇒ 仍算欠条」:这个文件只在 desired_on=true 时被写出来,所以
	// 「在、却读不懂」几乎必然是一张真欠条(store.go 的 LoadUpgradeIntent 同判)。
	// 往「多恢复一次保护」偏,不往「永远不再保护」偏。
	desiredOn := true
	var intent UpgradeIntent
	if err := json.Unmarshal(b, &intent); err == nil && intent.SchemaVersion == 1 {
		desiredOn = intent.DesiredOn
	}
	if desiredOn {
		if err := s.ArmMaintenanceHold(HoldReasonLegacyUpgrade, now); err != nil {
			return false, err
		}
		if err := s.SaveDesired(DesiredOn); err != nil {
			return false, err
		}
	}
	if err := os.Remove(s.paths.UpgradeIntent); err != nil && !os.IsNotExist(err) {
		// 删不掉不是失败:上面两件事已经做成了,而本次进程只跑这一遍
		// (见 migrateLegacyIntentOnce),不会反复刷新过期时间。
		log.Printf("guardian_legacy_upgrade_intent_remove_failed err=%v", err)
	}
	return true, nil
}
