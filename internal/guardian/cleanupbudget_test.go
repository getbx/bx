package guardian

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/supervisor"
)

// Minor 5:等别人的那个预算必须给它留余量。
//
// 三层此前是三个各写各的 15s(Manager.cleanupTimeout / ExecCoreRunner.StopTimeout /
// supervisor.ShutdownGrace),零余量。Core 的关机 watchdog 是**跑满 grace 才**
// 强制退出,所以一个把 defer 还原跑到最后一刻的健康 Core —— 还原默认路由、关 TUN、
// 把 DNS 交回系统,那正是我们让它协作关闭而不是 SIGKILL 的全部理由 —— 恰好踩在
// 这两个预算上:Stop 返回 deadline exceeded ⇒ retainUncertain ⇒
// core_ownership_uncertain,一台关得干干净净的机器被报成「可能有第二个 Core」。
//
// **钉的是顺序,不是任何一个具体数字**(先例:supervisor 的
// TestTeardownStepBudgetLeavesRoomBeforeTheShutdownWatchdog)—— 三个数将来都可能
// 因为别的理由被调,而要守住的是它们之间的关系。
func TestCoreCleanupBudgetLeavesRoomAboveWhatItWaitsOn(t *testing.T) {
	if defaultCoreStopWait <= supervisor.ShutdownGrace {
		t.Fatalf("runner.Stop 的等待 %v 没有排在 Core 自己的关机 grace %v 之后 ——\n"+
			"一个把 defer 还原跑满的健康 Core 会在这里表现为超时,而它关得干干净净;\n"+
			"那次超时会被翻成 core_ownership_uncertain",
			defaultCoreStopWait, supervisor.ShutdownGrace)
	}
	if defaultCoreCleanupTimeout <= defaultCoreStopWait {
		t.Fatalf("Manager 的清理预算 %v 没有包住 runner.Stop 的等待 %v ——\n"+
			"外层 ctx 先到期的话,内层那个等待根本走不完,超时的理由与 Core 无关",
			defaultCoreCleanupTimeout, defaultCoreStopWait)
	}
}

// 上面那条只在**默认值**这条路上成立;真正在跑的是 manager.go 与 process.go 里
// 那两处兜底赋值。判据打在源码上,因为「构造一个跑满 15 秒关机 grace 的假 Core」
// 在单测里要真等 15 秒 —— 而一个要跑十几秒的闸门等于没有闸门。
//
// 少了这一条,把 `cleanupTimeout = defaultCoreCleanupTimeout` 改回
// `15 * time.Second` 之后上面那条**照样全绿**:常量之间的关系还在,只是没人用它。
// 这正是本仓库反复罚过的那个形状 —— 守卫钉住的是缺陷旁边的东西。
func TestTheDefaultBudgetsActuallyComeFromThoseConstants(t *testing.T) {
	for _, tc := range []struct {
		file  string
		fn    string
		field string
		want  string
	}{
		{"manager.go", "NewManager", "cleanupTimeout", "defaultCoreCleanupTimeout"},
		{"process.go", "Stop", "timeout", "defaultCoreStopWait"},
	} {
		body := guardianFuncBody(t, tc.file, tc.fn)
		assign := tc.field + " = " + tc.want
		if !strings.Contains(body, assign) {
			t.Fatalf("%s 的 %s 里没有 %q —— 那个兜底值又变回了一个手写的秒数,\n"+
				"于是常量之间的关系还在、却没有人用它(而关系那条测试照样绿)",
				tc.file, tc.fn, assign)
		}
	}
}

// guardianFuncBody 交出某个函数体的源码文本。读不出来时**响亮失败** ——
// 一条认不出现在的代码却安静通过的守卫,与没有这条守卫一样。
func guardianFuncBody(t *testing.T, file, name string) string {
	t.Helper()
	source, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s:%v", file, err)
	}
	rawBytes, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读 %s:%v", file, err)
	}
	raw := string(rawBytes)
	for _, decl := range source.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name || fn.Body == nil {
			continue
		}
		return raw[fn.Body.Pos()-1 : fn.Body.End()-1]
	}
	t.Fatalf("%s 里找不到函数 %s —— 守卫的锚点漂了", file, name)
	return ""
}
