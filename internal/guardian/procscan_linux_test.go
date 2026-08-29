//go:build linux

package guardian

import (
	"os"
	"testing"
)

// 与 darwin 的 TestScanRunningCoresFailsWithoutRootPrivileges 同一条:非 root
// 没有资格得出「没有 Core」的结论(hidepid 下它是瞎的,而瞎读出来是「干净」)。
func TestScanRunningCoresRequiresRootOnLinux(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("以 root 跑时这条门槛测不到")
	}
	if _, err := scanRunningCores(coreScanLifecycle); err == nil {
		t.Fatal("非 root 的扫描必须报错,不许返回「看起来查过」的结果")
	}
}
