package guardian

import (
	"errors"
	"testing"
)

// 同时实现两条入口,记下被调的是哪一条。
type labelRecordingScanner struct {
	CoreRunner
	observed  int
	lifecycle int
	err       error
}

func (s *labelRecordingScanner) ScanRunning() ([]Process, error) {
	s.lifecycle++
	return nil, s.err
}

func (s *labelRecordingScanner) ScanRunningObserved() ([]Process, error) {
	s.observed++
	return nil, s.err
}

// 只实现通用入口的旧式 runner。
type lifecycleOnlyScanner struct {
	CoreRunner
	calls int
}

func (s *lifecycleOnlyScanner) ScanRunning() ([]Process, error) {
	s.calls++
	return []Process{{PID: 4242}}, nil
}

// 循环的每轮测量必须走「只观察」那条入口。
//
// 两条入口问的是同一个问题、用的是同一个 looksLikeCore,差别只在那行普查日志的
// reason=。而这个差别不是装饰:准入路径一天几次、由用户动作触发,紧邻它的
// guardian_orphan_launch_marker / guardian_no_core_record 是「我放行了一个 Core,
// 因为我认为没有别的 Core 在跑」**唯一**的记录;循环每轮都扫一次,稳态约 144 次/天
// 且永久。不分标签,那条审计线索就被淹在噪声里。
//
// 这里钉的是**路由**而不是日志文本:日志只有真实 darwin 扫描(需要 root)才打得出来,
// 单测里造不出;而会悄悄回退的恰恰是路由这一跳。
func TestReconcileMeasurementUsesTheObservingScanEntry(t *testing.T) {
	env := newManagerTestEnv(t)
	scanner := &labelRecordingScanner{CoreRunner: env.manager.runner}
	env.manager.runner = scanner

	env.manager.measureRunningCores()

	if scanner.observed != 1 {
		t.Errorf("只观察入口被调用 %d 次, want 1", scanner.observed)
	}
	if scanner.lifecycle != 0 {
		t.Errorf("循环的测量不该走准入那条入口(会污染放行审计的 grep), 被调用 %d 次", scanner.lifecycle)
	}
}

// 没实现「只观察」入口的 runner 必须**照常被测量**,退回通用入口 ——
// 绝不能因为少一个可选接口就把「没测」当成「测到 0 个」:那正是这份测量的反面,
// 会让误报率算出来偏低。
func TestReconcileMeasurementFallsBackInsteadOfReportingNotMeasured(t *testing.T) {
	env := newManagerTestEnv(t)
	scanner := &lifecycleOnlyScanner{CoreRunner: env.manager.runner}
	env.manager.runner = scanner

	got := env.manager.measureRunningCores()

	if scanner.calls != 1 {
		t.Errorf("通用入口被调用 %d 次, want 1", scanner.calls)
	}
	if !got.Measured || got.Cores != 1 {
		t.Errorf("退回通用入口时答案必须一模一样, got %+v", got)
	}
}

// 退回那条路上的失败仍然是「没测成」,不是「测到 0 个」。
func TestReconcileMeasurementKeepsFailureDistinctOnTheFallbackPath(t *testing.T) {
	env := newManagerTestEnv(t)
	env.manager.runner = &labelRecordingScanner{CoreRunner: env.manager.runner, err: errors.New("boom")}

	got := env.manager.measureRunningCores()

	if got.Measured || got.Cores != 0 || got.Reason != coreScanFailed {
		t.Errorf("扫描失败必须记成没测成, got %+v", got)
	}
}
