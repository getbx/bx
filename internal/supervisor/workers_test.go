package supervisor

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"sync"
	"testing"
	"time"
)

// 后台工人登记册:Run 里那六个长命 goroutine。
//
// **动它的理由**:裸 `go f(ctx)` 里的 panic 不会被 Run 的 defer 接住 —— 它
// 当场打死整个进程,而**别的 goroutine 的 defer 一个都不会跑**。对 bx 这意味着
// 进程没了而内核里的 ip rule / 策略路由**还在**:整机流量指向一个已经不存在的
// TUN,也就是断网。六个工人没有一个值得用「一台受保护的机器断网」来换。

func TestWorkerRegistryRunsTheWorkerWithContext(t *testing.T) {
	workers := &workerRegistry{}
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	workers.start(ctx, "probe", func(c context.Context) {
		if c.Err() != nil {
			t.Error("工人拿到的是一个已经取消的 ctx")
		}
		close(ran)
		<-c.Done()
	})
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("工人没被启动")
	}
	cancel()
	if names := workers.names(); len(names) != 1 || names[0] != "probe" {
		t.Fatalf("登记册没记下工人: %v", names)
	}
}

// **一个工人 panic 不许打死进程,也不许连累别的工人。**
//
// 这条是整件事的存在理由:今天六个裸 go 里任何一个 panic 都是整机断网
// (进程没了而 ip rule 还在),而这六件事没有一件值得那个代价。
func TestWorkerRegistryContainsAPanicAndKeepsOtherWorkersAlive(t *testing.T) {
	workers := &workerRegistry{}
	var mu sync.Mutex
	var seen []string
	workers.onPanic = func(name string, _ any) {
		mu.Lock()
		seen = append(seen, name)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	survivor := make(chan struct{})
	workers.start(ctx, "boom", func(context.Context) { panic("工人炸了") })
	workers.start(ctx, "survivor", func(c context.Context) {
		close(survivor)
		<-c.Done()
	})

	select {
	case <-survivor:
	case <-time.After(2 * time.Second):
		t.Fatal("另一个工人没能活下来 —— panic 连累了它")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := strings.Join(seen, ",")
		mu.Unlock()
		if got == "boom" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	got := strings.Join(seen, ",")
	mu.Unlock()
	if got != "boom" {
		t.Fatalf("panic 没有被按名字记下: %q", got)
	}
	if panicked := workers.panickedNames(); len(panicked) != 1 || panicked[0] != "boom" {
		t.Fatalf("登记册没记下炸掉的工人: %v", panicked)
	}
}

// 炸掉的工人**就此不再跑**,而且这件事必须是可见的 —— 悄悄少一个后台循环
// (比如直连出口自愈)正是这个仓库反复栽的「静默降级」。登记册把它记成数据,
// 而不是只留一行日志。
func TestWorkerRegistryRemembersWhichWorkerDied(t *testing.T) {
	workers := &workerRegistry{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	workers.onPanic = func(string, any) { close(done) }
	workers.start(ctx, "egress-repair", func(context.Context) { panic("boom") })

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("panic 没被接住")
	}
	if names := workers.names(); len(names) != 1 || names[0] != "egress-repair" {
		t.Fatalf("启动记录 = %v", names)
	}
	if panicked := workers.panickedNames(); len(panicked) != 1 {
		t.Fatalf("死亡记录 = %v", panicked)
	}
}

// 正常结束的工人不算「炸掉」——ctx 取消是它退出的正常方式,把它记成故障会
// 让每一次关机都留下一份假的死亡名单。
func TestWorkerRegistryDoesNotMarkACleanExitAsPanicked(t *testing.T) {
	workers := &workerRegistry{}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	workers.start(ctx, "clean", func(c context.Context) {
		<-c.Done()
		close(stopped)
	})
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("工人没有随 ctx 退出")
	}
	// 给 recover 那一层一点时间跑完收尾记账。
	time.Sleep(50 * time.Millisecond)
	if panicked := workers.panickedNames(); len(panicked) != 0 {
		t.Fatalf("干净退出被记成了故障: %v", panicked)
	}
}

// Run 里不许再有裸 `go` —— 新加一个后台循环时,它是最容易被顺手写出来的形状,
// 而后果是「一个 panic 打死进程,ip rule 留在内核里」。
//
// **判据取 AST 而不是文本**:注释里、字符串里、别的函数里的 `go ` 都不算,
// 只认 Run 这一个函数体内真实的 GoStmt。本仓库的文本守卫被绕过二十余次,
// 而这条性质恰好是 AST 能精确表达的。
func TestRunLaunchesNoBareGoroutines(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "run.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 run.go: %v", err)
	}
	var run *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "Run" {
			run = fn
		}
	}
	if run == nil {
		// 读不懂现在的代码时必须响亮失败,而不是静默放行 —— 一条认不出目标的
		// 守卫与没有守卫完全一样,但它看起来更让人放心。
		t.Fatal("run.go 里找不到 func Run(本守卫失效,请更新它)")
	}
	var bare []string
	ast.Inspect(run, func(n ast.Node) bool {
		if stmt, ok := n.(*ast.GoStmt); ok {
			bare = append(bare, fset.Position(stmt.Pos()).String())
		}
		return true
	})
	if len(bare) > 0 {
		t.Fatalf("Run 里有 %d 处裸 goroutine —— 请改走 workers.start(具名 + panic 收在自己那一层):\n%s",
			len(bare), strings.Join(bare, "\n"))
	}
}
