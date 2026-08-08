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
		// 文件不存在(常态)或读不动:没有欠条。
		return false, false
	}
	var intent upgradeIntent
	if err := json.Unmarshal(b, &intent); err != nil || intent.SchemaVersion != 1 {
		// 文件在、内容却读不懂:仍然算欠条,且按 desired_on=true 处理。
		// 这个方向是有依据的——这个文件**只在** desiredOn=true 时被写出来
		// (runUpgrade 里唯一的 saveIntent 调用),所以「存在但坏了」几乎必然
		// 是一次 desired_on=true 的欠条。往「多恢复一次保护」偏,而不是往
		// 「永远不再保护」偏:后者正是这个文件存在的原因。
		return true, true
	}
	return intent.DesiredOn, true
}

// saveUpgradeIntent 原子写:先写同目录临时文件、fsync、再 rename 覆盖。
//
// 直接 os.WriteFile 是**就地截断**:改写一份已有欠条时崩溃/断电会留下一个空
// 或半截的文件。虽然 loadUpgradeIntent 现在把「坏文件」也算作欠条,但原子写
// 才是根治——半截文件根本不会出现。
func saveUpgradeIntent(path string, desiredOn bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	b, err := json.Marshal(upgradeIntent{SchemaVersion: 1, DesiredOn: desiredOn})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".upgrade-intent-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后这行是 no-op(路径已不存在)
	if err := writeAndSync(tmp, b); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("restrict %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}
	// 目录项本身也要落盘,否则崩溃后 rename 可能不可见。best-effort:
	// 内容已经在盘上,目录没同步不至于把欠条读成半截。
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

func writeAndSync(f *os.File, b []byte) error {
	if _, err := f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}

// clearUpgradeIntent 抹掉欠条。文件本来就不在不算失败。
func clearUpgradeIntent(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// forgetUpgradeDebtOnExplicitOff 在用户**明确要求关闭保护**之后抹掉欠条。
//
// 欠条的含义是「上一次升级欠你一次『把保护起回来』」。`bx down` 是用户毫不含糊
// 地说「我现在不要保护」的唯一地方 —— 此后再拿一张旧欠条把保护打开(而菜单的
// Repair 带 --yes,连问都不会问),就是一次没有同意的网络接管,还会配上一句
// 「保护已按升级前的状态恢复」的假话。所以关掉即销账。
//
// 只在 down 真的成功之后调用:失败时保护可能还开着,欠条还有意义。
// 抹不掉只警告,绝不让「关闭」这条路因为一个记账文件而失败 —— 停止不得依赖
// 先成功做成别的事。
func forgetUpgradeDebtOnExplicitOff(clear func() error, warn func(string)) {
	if clear == nil {
		return
	}
	if err := clear(); err != nil && warn != nil {
		warn(fmt.Sprintf("! 未能清除升级标记(%v):下次 app-install 可能会多恢复一次保护", err))
	}
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
