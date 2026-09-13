package guardian

import (
	"path/filepath"
	"testing"
)

// newTestCoreRunner 是本包测试里构造 ExecCoreRunner 的**唯一**入口。
//
// NewExecCoreRunner 交出来的两个路径字段都指着**生产位置**:
// StatePath = /var/lib/bx/core-process.json、
// StartFailurePath = corestartfailure.DefaultPath(同一个目录)。
// 全仓 34 个构造点里此前只有 6 个把后者指走 —— 其余那些在普通 `go test` 下
// 靠 EACCES 侥幸(discardStaleStartFailureRecord 删不掉只打一行日志),而
// **`sudo go test ./internal/guardian/` 会让 Start 之前那一刀真的落在项目
// 所有者机器上那个目录**,连同 `.core-start-failure-*` 那些原子写碎片一起扫掉。
// 「纯逻辑测试免 root(用 t.TempDir(),不碰真实路由/设备)」是本仓库写在约定里
// 的一条,而这个暴露面是 7228b20 这一支自己引入的。
//
// **两个字段一起指走,不是只指出事的那一个**:它们由同一个构造器给出、
// 落在同一个目录,只修一半就是把同一件事修一半 —— 下一个人看到 StatePath
// 仍指着生产位置,会合理地以为这里本来就允许那么做。
//
// helper 只保证「绝不落在真实的 /var/lib/bx」,不规定落在哪儿:需要一个**确定**
// 路径的测试(要自己往那个文件里写、或要断言它被删掉)照旧在这之后覆盖它。
func newTestCoreRunner(t *testing.T, executable, configPath, dnsListen string) *ExecCoreRunner {
	t.Helper()
	runner := NewExecCoreRunner(executable, configPath, dnsListen)
	dir := t.TempDir()
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.StartFailurePath = filepath.Join(dir, "core-start-failure.json")
	return runner
}
