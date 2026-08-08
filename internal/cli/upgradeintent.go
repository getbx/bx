package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// upgradeIntentPath 记录「这次升级开始前,用户本来要不要保护开着」。
//
// 为什么必须落盘:第一步(停保护)会把 Guardian 的 desired 写成 off
// (`Manager.Down` 与 forcedMacOSTeardown 都会),于是**一次中途失败之后**再跑
// 一遍 app-install,读 desired 只会读到 off,`upgradeSteps` 就不再包含
// UpgradeStartProtection —— 重试「成功」了,而机器从此再也不会被保护。
// 把意图先写在自己的文件里,重试才知道自己还欠用户一次「把保护起回来」。
//
// 它与 /var/lib/bx 下的用户数据不同命运(同 core-process.json /
// guardian-state.json 的处置):卸载时逐个删掉,不整目录删。
const upgradeIntentPath = darwinDataDirPath + "/upgrade-intent.json"

type upgradeIntent struct {
	SchemaVersion int  `json:"schema_version"`
	DesiredOn     bool `json:"desired_on"`
}

// loadUpgradeIntent 读上一次未完成升级留下的意图。
//
// 返回 (desiredOn, present)。文件不存在、读不动、内容坏掉都返回 present=false ——
// 「没有可信的记录」和「记录说 off」在这里是同一件事,调用方会退回去问 Guardian
// 自己的 desired。
func loadUpgradeIntent(path string) (bool, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	var intent upgradeIntent
	if err := json.Unmarshal(b, &intent); err != nil || intent.SchemaVersion != 1 {
		return false, false
	}
	return intent.DesiredOn, true
}

func saveUpgradeIntent(path string, desiredOn bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	b, err := json.Marshal(upgradeIntent{SchemaVersion: 1, DesiredOn: desiredOn})
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// clearUpgradeIntent 抹掉欠条。文件本来就不在不算失败。
func clearUpgradeIntent(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// resolveUpgradeDesiredOn 把「上一次未完成升级留下的意图」与「Guardian 此刻的
// desired」合成一个答案。
//
// 只要任一方说 on 就是 on:欠条存在说明上一次升级停过保护而没能起回来,这时
// Guardian 的 desired 恰恰是**那次失败自己写下的 off**,不能拿它当用户的意思。
// 反过来,用户在两次尝试之间自己 bx up 了(欠条是 off 而 store 是 on)也一样要
// 起回来。
func resolveUpgradeDesiredOn(storeDesiredOn, pendingDesiredOn, pendingPresent bool) bool {
	if pendingPresent {
		return pendingDesiredOn || storeDesiredOn
	}
	return storeDesiredOn
}
