package supervisor

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// 拆除台账:把 Run 的 defer 链从**隐式的语言机制**变成**有序数据**。
//
// 动它的理由不是好看:今天一步拆除挂住,它后面的每一步都不会跑,只有关机
// watchdog 强制退出整个进程兜底 —— 而那条兜底会把**剩下的还原全部跳过**。
// 这条路径的谱系是 2026-08-04 那次「用户 71 分钟关不掉保护」,不变量原话是
// 「停止路径不许因为别的事没做完而变慢或失败」。

func TestTeardownLedgerUnwindsInReverseOrder(t *testing.T) {
	var order []string
	ledger := &teardownLedger{}
	ledger.push("first", func() { order = append(order, "first") })
	ledger.push("second", func() { order = append(order, "second") })
	ledger.push("third", func() { order = append(order, "third") })

	outcomes := ledger.unwind()

	// LIFO —— 与 defer 完全一致。**这是这次重构的全部安全性前提**:后获取的
	// 资源先释放(路由还原必须排在关 TUN 之前,否则 TUN 一没系统就没了回去的路)。
	if want := []string{"third", "second", "first"}; strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("拆除顺序 = %v, want %v", order, want)
	}
	if len(outcomes) != 3 || outcomes[0].Name != "third" || outcomes[2].Name != "first" {
		t.Fatalf("台账没有如实报出顺序: %+v", outcomes)
	}
	for _, o := range outcomes {
		if o.TimedOut {
			t.Fatalf("%s 被误判为超时: %+v", o.Name, o)
		}
	}
}

// **一步挂住不许连累其余** —— 这是整个改动的存在理由。
func TestTeardownLedgerBoundsEachStepAndKeepsGoing(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // 让挂住的那步在测试结束后退出,不泄漏到别的测试

	var mu sync.Mutex
	var ran []string
	ledger := &teardownLedger{}
	ledger.push("earliest", func() { mu.Lock(); ran = append(ran, "earliest"); mu.Unlock() })
	ledger.pushBounded("stuck", 50*time.Millisecond, func() { <-release })
	ledger.push("latest", func() { mu.Lock(); ran = append(ran, "latest"); mu.Unlock() })

	start := time.Now()
	outcomes := ledger.unwind()
	elapsed := time.Since(start)

	mu.Lock()
	got := strings.Join(ran, ",")
	mu.Unlock()
	if got != "latest,earliest" {
		t.Fatalf("挂住的一步连累了别人:跑过的是 %q", got)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("整条拆除被那一步拖了 %v —— 停止路径不许因为别的事没做完而变慢", elapsed)
	}
	var stuck *teardownOutcome
	for i := range outcomes {
		if outcomes[i].Name == "stuck" {
			stuck = &outcomes[i]
		}
	}
	if stuck == nil || !stuck.TimedOut {
		t.Fatalf("超时那一步没有如实上报: %+v", outcomes)
	}
}

// panic 不许吃掉剩下的拆除。一步 panic 与一步挂住是同一类事:它是**那一步**
// 的失败,不是整条路径的终点。
func TestTeardownLedgerSurvivesAPanickingStep(t *testing.T) {
	var ran []string
	ledger := &teardownLedger{}
	ledger.push("earliest", func() { ran = append(ran, "earliest") })
	ledger.push("boom", func() { panic("拆除里炸了") })
	ledger.push("latest", func() { ran = append(ran, "latest") })

	outcomes := ledger.unwind()

	if strings.Join(ran, ",") != "latest,earliest" {
		t.Fatalf("panic 吃掉了后续拆除:跑过的是 %v", ran)
	}
	var boom *teardownOutcome
	for i := range outcomes {
		if outcomes[i].Name == "boom" {
			boom = &outcomes[i]
		}
	}
	if boom == nil || boom.Panic == "" {
		t.Fatalf("panic 没有如实上报: %+v", outcomes)
	}
}

// unwind 只跑一次:Run 里它挂在 defer 上,而重复跑会把已经关掉的东西再关一遍
// (双重 Close、删掉不属于自己的 socket)。
func TestTeardownLedgerUnwindsOnlyOnce(t *testing.T) {
	count := 0
	ledger := &teardownLedger{}
	ledger.push("once", func() { count++ })

	ledger.unwind()
	second := ledger.unwind()

	if count != 1 {
		t.Fatalf("拆除跑了 %d 次", count)
	}
	if len(second) != 0 {
		t.Fatalf("第二次 unwind 报出了步骤: %+v", second)
	}
}

// 空台账拆除是平凡成功 —— Run 在很早的地方就可能返回(配置错、隧道起不来),
// 那时一个资源都没拿到。
func TestTeardownLedgerHandlesNothingToDo(t *testing.T) {
	ledger := &teardownLedger{}
	if outcomes := ledger.unwind(); len(outcomes) != 0 {
		t.Fatalf("空台账报出了步骤: %+v", outcomes)
	}
}
