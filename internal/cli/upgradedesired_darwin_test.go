//go:build darwin

// darwin-only:被测的 upgradeDesiredOn / desiredOnFrom 住在 appinstall_darwin.go
// (app-install 只在 macOS 上存在),与 upgrade_guardian_e2e_test.go 同一个约束。

package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/getbx/bx/internal/guardian"
)

// legacyIntentStore 造一台盘上**同时**有 desired 与一张陈旧 legacy 欠条的机器。
func legacyIntentStore(t *testing.T, desired guardian.DesiredState) *guardian.Store {
	t.Helper()
	root := t.TempDir()
	store := guardian.OpenStore(guardian.Paths{
		Desired: filepath.Join(root, "guardian-state.json"),
		// SaveDesired 会 MkdirAll 这四个,少填一个就是「guardian store path
		// required」而不是被测的那件事。
		Transaction:   filepath.Join(root, "update", "transaction.json"),
		Receipt:       filepath.Join(root, "update", "receipt.json"),
		Staging:       filepath.Join(root, "update", "staging"),
		Snapshots:     filepath.Join(root, "update", "snapshots"),
		UpgradeIntent: filepath.Join(root, "upgrade-intent.json"),
	})
	if err := store.SaveDesired(desired); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "upgrade-intent.json"),
		[]byte(`{"schema_version":1,"desired_on":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return store
}

// CLI 再也不看 upgrade-intent.json。
//
// 它今天会让 resolveUpgradeDesiredOn 违背用户明确的关闭请求把保护打开:一次装文件
// 失败留下欠条 → 用户说「就这么关着」(desired=off)→ 菜单的 Repair 带 --yes 连问
// 都不问 → 欠条胜出,保护被打开,还配一句「已按升级前的状态恢复」的假话。
//
// 断言打在 desiredOnFrom 上而不是 upgradeDesiredOn 上,只因为后者写死了
// /var/lib/bx —— 那是 root 的目录,测不了。两者之间只隔一次 OpenDefaultStore()。
//
// **另一个方向在别处**:一次夭折的**过渡**升级(新 CLI × 旧 Guardian)之后,
// 把用户的意图找回来的不再是这张欠条,而是停机之后那次 desired=on 的写回 ——
// 见 TestTransitionUpgradeKeepsTheIntentAgainstAHoldUnawareGuardian(它一路跑到
// 重跑并断言保护真的被起回来)。Guardian 侧那半(盘上真有欠条时的迁移)由
// guardian 包的 TestLegacyUpgradeIntentRestoresDesiredOn 及其顺序用例守着。
// 两个方向都要有人盯:只钉「陈旧欠条不许翻盘」,一个恒 false 的实现照样绿。
func TestStaleLegacyIntentFileDoesNotTurnProtectionBackOn(t *testing.T) {
	store := legacyIntentStore(t, guardian.DesiredOff)
	if desiredOnFrom(context.Background(), store, "/nonexistent/guardian.sock") {
		t.Fatal("陈旧欠条把保护打开了 —— 用户明确说过 off")
	}
	steps := upgradeSteps(true, false, true)
	if stepsContain(steps, UpgradeStartProtection) {
		t.Fatalf("升级计划里不该有「恢复保护」:%v", steps)
	}
}

// 另一半输入空间:盘上写着 on 就必须是 on(否则上面那条测试用一个恒 false 的
// 实现也能绿)。
func TestUpgradeDesiredOnFollowsTheDesiredFileWhenItSaysOn(t *testing.T) {
	store := legacyIntentStore(t, guardian.DesiredOn)
	if !desiredOnFrom(context.Background(), store, "/nonexistent/guardian.sock") {
		t.Fatal("盘上写着 on,升级末尾就必须把保护起回来")
	}
}
