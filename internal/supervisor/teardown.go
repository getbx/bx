package supervisor

import (
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"
)

// teardownLedger 是 Run 的拆除台账:**把还原顺序从 defer 栈里搬出来变成数据**。
//
// 为什么值得动这条最危险的路径:今天一步拆除挂住,它后面的每一步都不会跑
// (defer 是同步的),只有关机 watchdog 强制退出整个进程兜底 —— 而强制退出会把
// **剩下的还原全部跳过**。run.go 那条 watchdog 的注释里写着已知的挂点嫌疑
// (eng.Close / tun0.Stop),也就是说这不是假想。
//
// 台账给出三件 defer 给不了的:
//   - **逐步限时**:一步挂住只损失那一步,后面的照常跑完(这条不变量的原话是
//     「停止路径不许因为别的事没做完而变慢或失败」,谱系是 2026-08-04 那次
//     用户 71 分钟关不掉保护);
//   - **命名与记录**:关机时每一步的成败与耗时进日志,事后查得出卡在哪 ——
//     此前只有一份 goroutine dump;
//   - **可断言**:顺序是一份数据,单测直接比对,不必读源码文本。
//
// **LIFO 一字不改**:后获取的先释放。路由还原必须排在关 TUN 之前,顺序错了
// 就是把机器留在一个没有 TUN 也没有原路由的状态里。
type teardownLedger struct {
	mu       sync.Mutex
	steps    []teardownStep
	unwound  bool
	budget   time.Duration // 0 = teardownDefaultBudget
	onRecord func(teardownOutcome)
}

type teardownStep struct {
	name   string
	budget time.Duration
	fn     func()
}

// teardownOutcome 是一步拆除的结果。**超时与 panic 分开报**:前者是「它还没
// 做完」,后者是「它做坏了」,压成一个 bool 会让关机日志答不出到底发生了什么。
type teardownOutcome struct {
	Name     string
	Elapsed  time.Duration
	TimedOut bool
	Panic    string
}

// teardownDefaultBudget 是单步默认预算。
//
// 取值比整条关机 grace(ShutdownGrace,15s)小一个量级:一步的预算不该接近
// 整条路径的预算 —— 那样第一步挂住就把额度用光了,后面的步骤等于没有预算。
const teardownDefaultBudget = 3 * time.Second

func (l *teardownLedger) push(name string, fn func()) {
	l.pushBounded(name, 0, fn)
}

func (l *teardownLedger) pushBounded(name string, budget time.Duration, fn func()) {
	if fn == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.steps = append(l.steps, teardownStep{name: name, budget: budget, fn: fn})
}

// unwind 逆序跑完全部步骤,返回每一步的结果。**只跑一次**:它挂在 Run 的
// defer 上,重复跑会把已经关掉的东西再关一遍(双重 Close、删掉不再属于自己的
// socket)。
func (l *teardownLedger) unwind() []teardownOutcome {
	l.mu.Lock()
	if l.unwound {
		l.mu.Unlock()
		return nil
	}
	l.unwound = true
	steps := l.steps
	budget := l.budget
	if budget <= 0 {
		budget = teardownDefaultBudget
	}
	onRecord := l.onRecord
	l.mu.Unlock()

	outcomes := make([]teardownOutcome, 0, len(steps))
	for i := len(steps) - 1; i >= 0; i-- {
		outcome := runTeardownStep(steps[i], budget)
		outcomes = append(outcomes, outcome)
		if onRecord != nil {
			onRecord(outcome)
		}
		logTeardownOutcome(outcome)
	}
	return outcomes
}

// runTeardownStep 跑一步并给它封顶。
//
// **超时之后那个 goroutine 是被放生的,不是被杀掉的** —— Go 杀不掉 goroutine。
// 这是刻意接受的代价:进程正在关闭,一个卡在 ioctl 上的清理协程留着,好过让它
// 把后面每一步都堵死。反过来说,这也意味着「超时」不等于「那件事没做成」,
// 只等于「它没在预算内做完」—— 日志措辞按这个来。
func runTeardownStep(step teardownStep, defaultBudget time.Duration) teardownOutcome {
	budget := step.budget
	if budget <= 0 {
		budget = defaultBudget
	}
	done := make(chan string, 1) // 容量 1:超时之后没人再读,goroutine 也不会卡在发送上
	start := time.Now()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// **panic 收在这里,不许穿出去**:一步炸了是那一步的失败,
				// 不是整条拆除的终点 —— 而它穿出去会打死正在关机的进程,
				// 把剩下的还原全部跳过。
				done <- fmt.Sprintf("%v\n%s", r, debug.Stack())
			}
		}()
		step.fn()
		done <- ""
	}()

	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case panicked := <-done:
		return teardownOutcome{Name: step.name, Elapsed: time.Since(start), Panic: panicked}
	case <-timer.C:
		return teardownOutcome{Name: step.name, Elapsed: time.Since(start), TimedOut: true}
	}
}

// logTeardownOutcome 只在**出事时**说话。
//
// 每一步都打一行会让关机刷屏,而关机日志的读者永远是在查「这次为什么没关干净」
// ——正常的那些行只会把要紧的那一行淹掉(与调谐环 change-only 日志同一条纪律)。
func logTeardownOutcome(outcome teardownOutcome) {
	switch {
	case outcome.TimedOut:
		log.Printf("拆除步骤 %q 超过预算(%s)未返回,跳过它继续还原 —— 该步可能没做完",
			outcome.Name, outcome.Elapsed.Round(time.Millisecond))
	case outcome.Panic != "":
		log.Printf("拆除步骤 %q panic,已收住并继续还原:%s", outcome.Name, outcome.Panic)
	}
}
