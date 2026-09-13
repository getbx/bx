package supervisor

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/tunnel"
)

// 每个哨兵都必须有产地。
//
// 一个没有产地的哨兵是一个**永远不会出现的码** —— 消费方为它写的那一支分支
// 与不写在输出上完全一样,而它看起来是被覆盖着的(本仓库的「死代码被绿色测试
// 守着」那个形状)。这条守卫穷举 startFailureSentinels:加一个哨兵却不接产地,
// 它当场转红。
//
// 「产地」的证明分两种,取决于那一处够不够得着:
//   - trigger 非 nil:**真的把生产代码跑出那个错误**(强证据);
//   - platformCall 非空:产地是 Run() 里那一行,而 Run() 要真 TUN、真路由,
//     开发机上跑不起来(集成台只有 linux netns 一条腿,也造不出「OpenTUN 失败」)。
//     退而用 AST,判据打在「那次调用之后紧跟着的失败分支里出现了这个哨兵」上,
//     不是「文件里出现过这个词」—— 后者在把哨兵挂到隔壁那一跳上时照样绿。
func TestEveryStartFailureSentinelHasAProductionSite(t *testing.T) {
	type site struct {
		trigger      func(*testing.T) error
		platformCall string
	}
	sites := map[error]site{
		ErrTunnelUnhealthy: {trigger: func(t *testing.T) error {
			return waitTunnelHealthy(context.Background(), newNeverHealthyTunnel(), 10*time.Millisecond)
		}},
		ErrProvision: {trigger: func(t *testing.T) error {
			_, err := ensureSingboxBinary(&config.Config{
				DataDir:    t.TempDir(),
				SingboxBin: filepath.Join(t.TempDir(), "没有这个文件"),
			})
			return err
		}},
		ErrConfig: {trigger: func(t *testing.T) error {
			// via + 一条不是网段的 cidr:配置里那几行本身就是坏的。
			_, err := buildSplitBrain(&config.Config{
				Global: true,
				Rules:  []config.Rule{{Via: "wan2", CIDR: []string{"这不是网段"}}},
			}, Options{})
			return err
		}},
		ErrTunnelUnreachable: {trigger: func(t *testing.T) error {
			return diagnoseUnhealthyTunnel(context.Background(), closedLocalAddress(t),
				func() tunnelDialFunc { return (&net.Dialer{}).DialContext },
				tagStartFailure(ErrTunnelUnhealthy, errors.New("健康检查超时")))
		}},
		ErrTunnelHandshakeFailed: {trigger: func(t *testing.T) error {
			return diagnoseUnhealthyTunnel(context.Background(), listeningLocalAddress(t),
				func() tunnelDialFunc { return (&net.Dialer{}).DialContext },
				tagStartFailure(ErrTunnelUnhealthy, errors.New("健康检查超时")))
		}},
		ErrTUNOpen: {platformCall: "OpenTUN"},
		ErrHijack:  {platformCall: "Hijack"},
	}

	fn := runFuncDecl(t)
	for _, entry := range startFailureSentinels {
		known, ok := sites[entry.Err]
		if !ok {
			t.Fatalf("哨兵 %v(码 %s)没有登记产地 —— 一个没有产地的哨兵是一个永远不会\n"+
				"出现的码。要么在这张表里给它一个真能跑出它的 trigger,要么(产地在 Run() 里、\n"+
				"够不着时)登记 platformCall。", entry.Err, entry.Code)
		}
		switch {
		case known.trigger != nil:
			err := known.trigger(t)
			if err == nil {
				t.Fatalf("%s 的产地没有报错 —— 这条 trigger 已经不成立了,它证明不了任何事", entry.Code)
			}
			if !errors.Is(err, entry.Err) {
				t.Fatalf("%s 的产地产出 %v,errors.Is 认不出哨兵 %v", entry.Code, err, entry.Err)
			}
			if got := StartFailureCode(err); got != entry.Code {
				t.Fatalf("产地产出的错误分类成 %q,want %q", got, entry.Code)
			}
		case known.platformCall != "":
			requireSentinelTagsPlatformCall(t, fn, known.platformCall, entry.Err)
		default:
			t.Fatalf("哨兵 %v 的产地登记是空的", entry.Err)
		}
	}
}

// 既有错误文案**一个字都不许改** —— 它们进 Core 日志,是给人读的;哨兵是给
// 机器读的。两者并存但互不污染,这是「不碰字符串」那条纪律的另一半:判据不许
// 长在文案上,文案也不许被判据改写。
func TestTaggingASentinelDoesNotChangeTheHumanReadableMessage(t *testing.T) {
	original := fmt.Errorf("建 TUN: %w", errors.New("operation not permitted"))
	tagged := tagStartFailure(ErrTUNOpen, original)
	if tagged.Error() != original.Error() {
		t.Fatalf("挂上哨兵之后文案变了:\n  前 %q\n  后 %q", original.Error(), tagged.Error())
	}
	if !errors.Is(tagged, ErrTUNOpen) {
		t.Fatal("哨兵没进错误链")
	}
	if !errors.Is(tagged, original) {
		t.Fatal("原错误被哨兵顶掉了 —— 链上还有别人在靠它做判定")
	}
}

// 分类只认哨兵,认不出的落 other,而 nil 既不是失败也不是 other。
func TestStartFailureCodeNeverGuessesFromText(t *testing.T) {
	if got := StartFailureCode(nil); got != "" {
		t.Fatalf("nil 分类成 %q,want 空串 ——「没有失败」不是一种失败", got)
	}
	// 文案里塞满每一个码的字面量,而链上一个哨兵都没有:按文本猜的实现会中招。
	textual := errors.New("tunnel_unreachable tun_open_failed hijack_failed provision_failed config_unusable 隧道没能建起来")
	if got := StartFailureCode(textual); got != StartFailureOther {
		t.Fatalf("按文本猜出了 %q —— 判据必须是 errors.Is", got)
	}
}

// requireSentinelTagsPlatformCall 断言 Run() 里 plat.<method>(…) 那一跳的失败
// 分支真的**把这个哨兵挂上去了**。
//
// 判据两层,缺一不可:
//   - **位置**:紧跟那次调用的那个 if 语句体内 —— 只查「run.go 里出现过这个词」
//     的话,把哨兵挂到隔壁任何一跳上都照样绿,而挂错跳产生的是一个自信的错误
//     答案(用户被派去查 TUN,真因在路由);
//   - **那次挂标的结果真的被返回了**:哨兵必须是这个 if 体里某条 return 的
//     结果表达式**里**的一次 tagStartFailure(<哨兵>, …)。
//
// 第二层为什么是「被返回」而不是「这次调用存在」:后者写过一版,而它挡不住
//
//	_ = tagStartFailure(ErrTUNOpen, fmt.Errorf("建 TUN: %w", err))
//	return fmt.Errorf("建 TUN: %w", err)
//
// —— 变异实测(LANDED,与副本 diff 确认):守卫与整个包全绿,而 tun_open_failed
// 又变回一个**永远不会被产生的码**,正是这条守卫存在要消灭的那件事。
// **作实参不等于被返回**;错误链上有没有那个哨兵,只由被 return 出去的那个值说了算。
//
// 允许包一层(`return fmt.Errorf("…: %w", tagStartFailure(…))`):那样的错误链上
// 哨兵仍在,errors.Is 照样认得出。
//
// 这一跳无法用行为测试覆盖:plat.OpenTUN / plat.Hijack 都要 root 才跑得动。
func requireSentinelTagsPlatformCall(t *testing.T, fn *ast.FuncDecl, method string, sentinel error) {
	t.Helper()
	name := sentinelIdentName(t, sentinel)
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, stmt := range block.List {
			if !callsPlatformMethod(stmt, method) || i+1 >= len(block.List) {
				continue
			}
			guard, ok := block.List[i+1].(*ast.IfStmt)
			if !ok {
				continue
			}
			if returnsSentinelTagged(guard, name) {
				found = true
			}
		}
		return true
	})
	if !found {
		t.Fatalf("Run() 里 plat.%s(…) 失败那一支 **return 出去的那个错误**没有挂上 %s\n"+
			"(要的是 return … tagStartFailure(%s, …) …,不是这次调用在附近发生过 ——\n"+
			"作实参不等于被返回)—— 少了它,那个哨兵没有产地,它对应的码永远不会出现,\n"+
			"而消费方为它写的分支看起来是被覆盖着的",
			method, name, name)
	}
}

// returnsSentinelTagged:这个 if 体里有没有一条 return,它**返回的表达式**里
// 含一次 tagStartFailure(<name>, …)。
//
// 判据打在 ReturnStmt.Results 上而不是整个块上 —— 这正是「作实参」与「被返回」
// 的分界,也是这条守卫上一版被一句 `_ = tagStartFailure(…)` + 裸 return 绕过去
// 的地方。嵌套的函数字面量不算数:它里面那条 return 返回的是那个闭包的结果,
// 不是这一跳的错误。
func returnsSentinelTagged(guard *ast.IfStmt, name string) bool {
	tagged := false
	ast.Inspect(guard.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, result := range ret.Results {
			if tagsSentinel(result, name) {
				tagged = true
			}
		}
		return true
	})
	return tagged
}

// tagsSentinel:node 里有没有一次 tagStartFailure(<name>, …)。
func tagsSentinel(node ast.Node, name string) bool {
	tagged := false
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "tagStartFailure" {
			return true
		}
		if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == name {
			tagged = true
		}
		return true
	})
	return tagged
}

// sentinelIdentName 把哨兵的值映射回它在源码里的标识符名。**写死一张表**是刻意的:
// 反射拿不到变量名,而按错误文案去猜正是这份工作明令不许的东西。
func sentinelIdentName(t *testing.T, sentinel error) string {
	t.Helper()
	switch sentinel {
	case ErrTUNOpen:
		return "ErrTUNOpen"
	case ErrHijack:
		return "ErrHijack"
	}
	t.Fatalf("哨兵 %v 没有登记标识符名(AST 守卫认不出它)", sentinel)
	return ""
}

func callsPlatformMethod(stmt ast.Stmt, method string) bool {
	hit := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != method {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "plat" {
			hit = true
		}
		return true
	})
	return hit
}

// newNeverHealthyTunnel 是一条永远不健康的隧道:健康探测恒失败,子进程立刻结束。
func newNeverHealthyTunnel() *tunnel.Tunnel {
	return tunnel.New(
		"127.0.0.1:0",
		func(string) (tunnel.Runner, error) { return nil, errors.New("不起子进程") },
		func(string) (int64, error) { return 0, errors.New("探测失败") },
	)
}

// 码要跨进程走(Core 写记录 → Guardian 读回来 → 用户看见),所以「哪些字符串
// 算一个码」必须有一份**派生自哨兵表**的答案。
//
// 手抄第二张白名单正是这个仓库反复栽的形状:加一个码而忘了加进白名单,
// Guardian 就会把它当成陌生字符串丢掉,那一支静默地永不生效。
func TestStartFailureCodesAreDerivedFromTheSentinelTable(t *testing.T) {
	codes := StartFailureCodes()
	seen := map[string]bool{}
	for _, code := range codes {
		if code == "" {
			t.Fatal("码里有空串 —— 空串是「没有码」,不是一种码")
		}
		if seen[code] {
			t.Fatalf("码 %q 出现了两次", code)
		}
		seen[code] = true
		if !IsStartFailureCode(code) {
			t.Fatalf("IsStartFailureCode(%q) = false", code)
		}
	}
	for _, entry := range startFailureSentinels {
		if !seen[entry.Code] {
			t.Fatalf("哨兵表里的 %q 不在 StartFailureCodes() 里 —— 那个码跨进程走时会被丢掉", entry.Code)
		}
	}
	if !seen[StartFailureOther] {
		t.Fatal("other 不在清单里 —— 认不出的失败也要能跨进程说出来")
	}
	if IsStartFailureCode("") || IsStartFailureCode("我是盘上被人改出来的字符串") {
		t.Fatal("IsStartFailureCode 放过了不属于这一族的字符串")
	}
}
