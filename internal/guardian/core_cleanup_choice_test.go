package guardian

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/supervisor"
)

// I4:强杀还是协作关闭,判据是**这个 Core 有没有服务过**,不是「我是哪个调用点」。
//
// 「健康检查没过」并不蕴含「从没服务过」:控制 socket 在 supervisor.Run:792 打开,
// 而 OpenTUN 在 473 —— UDP 档没就绪、socks 探测整个窗口失败、隧道恰好抖了、升级时
// 版本对不上,都会让一个**已经开了 TUN、装了路由、接管了 DNS**的 Core 没过健康门。
// 把那样的 Core SIGKILL 掉会跳过它自己的 defer 还原:路由不还原、TUN 不关,而 linux
// 上 pref 150/200 那两条 ip rule 比设备活得还久。
//
// 这里造的正是那个状态:Core 的控制 socket **亲口报出了自己的 PID**(它服务过),
// 只是版本对不上,于是健康门失败。
func TestACoreThatAnsweredTheControlSocketIsNeverForceKilled(t *testing.T) {
	env := newManagerTestEnv(t)
	env.health.onWait = func() {
		// onWait 在 h.mu 里被调,直接改字段(读它的那一句排在后面)。
		env.health.runtime = supervisor.RuntimeState{
			Version: "别的版本", PID: env.health.last.PID, TunName: "utun9",
			SocksAddr: "127.0.0.1:43210", TunnelHealthy: true,
			DNSListening: true, RoutesInstalled: true,
		}
	}

	if err := env.manager.Up(context.Background()); err == nil {
		t.Fatal("版本对不上而 Up 报成功了")
	}
	events := env.events.snapshot()
	if slices.Contains(events, "core.force_stop") {
		t.Fatalf("一个已经服务过的 Core 被 SIGKILL 了(events=%v)——\n"+
			"它开着 TUN、装着路由,强杀跳过它自己的 defer 还原,机器会指向一个不存在的 TUN", events)
	}
	if !slices.Contains(events, "core.stop") {
		t.Fatalf("已经服务过的 Core 没有走协作关闭(events=%v)", events)
	}
}

// M3:上面那条判据必须是**规则**,不是某一处写对了。
//
// 判据结构化:forceCleanupStartedCore 与 cleanupStartedCore 都只许有一个调用点,
// 而那个调用点是 cleanupCoreAfterFailedStart —— 那里是唯一问「服务过没有」的地方。
// 少了这一条,下一个人在第五个失败分支上直接写 forceCleanupStartedCore,行为测试
// 一条都不会红(它守的是它自己造的那个状态,不是那条新加的路)。
func TestForceKillHasExactlyOneCallSiteAndItIsTheOneThatAsksWhetherTheCoreServed(t *testing.T) {
	const decider = "cleanupCoreAfterFailedStart"
	for _, name := range []string{"forceCleanupStartedCore", "cleanupStartedCore"} {
		callers := guardianCallersOf(t, name)
		if len(callers) != 1 || callers[0] != decider {
			t.Fatalf("%s 的调用点 = %v,want 只有 %s ——\n"+
				"强杀/协作的分界一旦按调用点各写各的,「这个 Core 有没有服务过」就有了第二份答案,\n"+
				"而错的那一份会把一个开着 TUN 的 Core 杀掉、跳过它的还原", name, callers, decider)
		}
	}
}

// M1(定向复审):上面那条钉的是**包装函数的名字**,不是**能力**。
//
// 变异实测(LANDED):把 waitHealthy 失败那一处换成内联的
// `m.runner.ForceStop(cleanupCtx, process)`,上面那条守卫全绿 —— 它数的是
// forceCleanupStartedCore 被谁调,而这次改动根本没调它。行为测试也接不住:
// 它守的是它自己造的那个状态,盖不到一条**新开的第六个**失败分支,而那正是
// 上面那条守卫的注释里自己写明的存在理由。
//
// 所以判据下沉一层,打在**那个能力**上:m.runner.ForceStop 全包只许有一个
// 调用点,而它在 forceCleanupStartedCore 里。
//
// **m.runner.Stop 不是「只许一个调用点」** —— 实测四处,其中三处与失败启动的
// 清理无关(Down 拆保护 / 更新回滚 / 换二进制时停旧 Core),它们收拾的都是
// 正在正常服务的 Core。定向复审那条建议在这一半上与事实不符,故改成一张**具名
// 白名单**:形状同 internal/guardian/reconcile_execute.go 那份可执行动作白名单
// —— 加一个调用点会红一次,而那正是回来回答「这个 Core 有没有服务过」的时刻。
func TestTheKillCapabilityItselfHasExactlyOneCallSite(t *testing.T) {
	if callers := guardianCallersOfRunnerMethod(t, "ForceStop"); len(callers) != 1 || callers[0] != "forceCleanupStartedCore" {
		t.Fatalf("m.runner.ForceStop 的调用点 = %v,want 只有 forceCleanupStartedCore ——\n"+
			"内联一次强杀就绕开了「这个 Core 有没有服务过」那道唯一的问句,\n"+
			"而一个开着 TUN、装着路由的 Core 被 SIGKILL 会跳过它自己的 defer 还原", callers)
	}

	// 协作关闭这一侧:每一个调用点都要说得出「它收拾的不是一个从没服务过的 Core」。
	//   - cleanupStartedCore ...... 失败启动的清理,判据经 cleanupCoreAfterFailedStart
	//   - Down .................... 用户关保护,那个 Core 一直在服务
	//   - recoverUpdateRollback ... 更新回滚,停的是验明过身份的在跑 Core
	//   - updatePreparedLocked .... 换二进制,停的是旧的、正在服务的 Core
	wantStopCallers := []string{"Down", "cleanupStartedCore", "recoverUpdateRollback", "updatePreparedLocked"}
	slices.Sort(wantStopCallers)
	if callers := guardianCallersOfRunnerMethod(t, "Stop"); !slices.Equal(callers, wantStopCallers) {
		t.Fatalf("m.runner.Stop 的调用点 = %v,want %v ——\n"+
			"新开一个调用点之前先回答:它收拾的那个 Core 服务过没有?没服务过的\n"+
			"协作关闭必定失败(它要的 socket 正是它没能建出来的那个),真话会被\n"+
			"core_ownership_uncertain 顶掉。答案只有一份,在 cleanupCoreAfterFailedStart。",
			callers, wantStopCallers)
	}
}

// guardianCallersOfRunnerMethod 返回调用 m.runner.<method>(…) 的那些函数名(去重)。
//
// 判据带上**接收者**:光看选择子叫不叫 Stop 的话,满仓的 timer.Stop() /
// ticker.Stop() / m.legacy.Stop() 会一起被数进来,这条守卫就永远红着 —— 而一条
// 恒红的守卫会被下一个人删掉。
func guardianCallersOfRunnerMethod(t *testing.T, method string) []string {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("扫不到 internal/guardian 的源码:%v", err)
	}
	var callers []string
	scanned := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s:%v", path, err)
		}
		scanned++
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != method {
					return true
				}
				recv, ok := sel.X.(*ast.SelectorExpr)
				if !ok || recv.Sel.Name != "runner" {
					return true
				}
				if id, ok := recv.X.(*ast.Ident); !ok || id.Name != "m" {
					return true
				}
				if !slices.Contains(callers, fn.Name.Name) {
					callers = append(callers, fn.Name.Name)
				}
				return true
			})
		}
	}
	if scanned == 0 {
		t.Fatal("一个 .go 文件都没扫到 —— 这条守卫什么都没守")
	}
	if len(callers) == 0 {
		t.Fatalf("全包一处 m.runner.%s(…) 都没扫到 —— 判据认不出现在的写法了,"+
			"而认不出的时候它恰好什么都不守", method)
	}
	slices.Sort(callers)
	return callers
}

// guardianCallersOf 返回 internal/guardian 里调用 m.<name>(…) 的那些函数名(去重)。
// 读不出源码时**响亮失败** —— 一条安静地扫了零个文件的守卫与没有这条守卫一样。
func guardianCallersOf(t *testing.T, name string) []string {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("扫不到 internal/guardian 的源码:%v", err)
	}
	var callers []string
	scanned := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s:%v", path, err)
		}
		scanned++
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name == name {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
					if !slices.Contains(callers, fn.Name.Name) {
						callers = append(callers, fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	if scanned == 0 {
		t.Fatal("一个 .go 文件都没扫到 —— 这条守卫什么都没守")
	}
	slices.Sort(callers)
	return callers
}
