package guardian

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var thBase = time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)

// **记最新的那次观测,不记历史最大值。**
//
// 记最大值听起来更有用,而它会让一台今天已经不行的服务器永远顶着它一月份那次的
// 成绩 —— 那正是「陈旧数字冒充现状」。Core 那边的 RateMeter 已经算好了「最近
// 30 分钟内的峰值」,这里存的就是那个。
func TestThroughputHistoryKeepsTheLatestNotTheBest(t *testing.T) {
	state := throughputState{SchemaVersion: throughputStateSchema}
	state, _ = mergeThroughput(state, "tokyo", 9_000_000, thBase)
	state, changed := mergeThroughput(state, "tokyo", 500_000, thBase.Add(time.Hour))

	if !changed {
		t.Fatal("更新没有被记下来")
	}
	if got := state.Servers["tokyo"].PeakBPS; got != 500_000 {
		t.Errorf("PeakBPS = %d, want 500000 —— 历史最好的那次盖住了现在", got)
	}
	if !state.Servers["tokyo"].ObservedAt.Equal(thBase.Add(time.Hour)) {
		t.Errorf("观测时刻没跟着更新:%v", state.Servers["tokyo"].ObservedAt)
	}
}

// **无效观测一律不写。** 0 不是「跑不动」而是「这段时间没人用它传东西」,
// 把它写进历史会把一条真实的记录覆盖成一个假的坏消息。
func TestThroughputHistoryIgnoresNonObservations(t *testing.T) {
	state := throughputState{SchemaVersion: throughputStateSchema}
	state, _ = mergeThroughput(state, "tokyo", 3_000_000, thBase)

	for _, tc := range []struct {
		name string
		srv  string
		bps  int64
		at   time.Time
	}{
		{"0 字节每秒", "tokyo", 0, thBase.Add(time.Hour)},
		{"负数", "tokyo", -1, thBase.Add(time.Hour)},
		{"没有名字", "", 1_000_000, thBase.Add(time.Hour)},
		{"没有时刻", "tokyo", 1_000_000, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after, changed := mergeThroughput(state, tc.srv, tc.bps, tc.at)
			if changed {
				t.Fatal("无效观测被记下来了")
			}
			if after.Servers["tokyo"].PeakBPS != 3_000_000 {
				t.Fatalf("原有的记录被覆盖了:%d", after.Servers["tokyo"].PeakBPS)
			}
		})
	}
}

// **时光倒流的观测丢掉。** 系统时钟被改过时,一次「更早」的观测会把真正最新的
// 那条盖掉,而界面会据此说它更新鲜。
func TestThroughputHistoryRejectsObservationsFromThePast(t *testing.T) {
	state := throughputState{SchemaVersion: throughputStateSchema}
	state, _ = mergeThroughput(state, "tokyo", 3_000_000, thBase)
	after, changed := mergeThroughput(state, "tokyo", 9_000_000, thBase.Add(-time.Hour))
	if changed {
		t.Fatal("一次更早的观测被接受了")
	}
	if after.Servers["tokyo"].PeakBPS != 3_000_000 {
		t.Fatalf("被更早的观测盖掉了:%d", after.Servers["tokyo"].PeakBPS)
	}
}

// 完全相同的观测不算变化 —— 这个函数每 30 秒到 10 分钟被调一次,而绝大多数
// 时候观测是一样的;每次都写等于给一块 SSD 找事做。
func TestIdenticalObservationIsNotAChange(t *testing.T) {
	state := throughputState{SchemaVersion: throughputStateSchema}
	state, _ = mergeThroughput(state, "tokyo", 3_000_000, thBase)
	if _, changed := mergeThroughput(state, "tokyo", 3_000_000, thBase); changed {
		t.Fatal("同一次观测被当成了变化")
	}
}

// 条数有上限(名字来自配置,而配置是用户改的),淘汰最久没观测过的那些。
func TestThroughputHistoryEvictsTheOldest(t *testing.T) {
	state := throughputState{SchemaVersion: throughputStateSchema}
	for i := 0; i < maxThroughputEntries+5; i++ {
		state, _ = mergeThroughput(state, fmt.Sprintf("srv%02d", i), 1_000_000,
			thBase.Add(time.Duration(i)*time.Minute))
	}
	if len(state.Servers) != maxThroughputEntries {
		t.Fatalf("条数 = %d, want %d", len(state.Servers), maxThroughputEntries)
	}
	if _, ok := state.Servers["srv00"]; ok {
		t.Error("最旧的那条没有被淘汰")
	}
	if _, ok := state.Servers[fmt.Sprintf("srv%02d", maxThroughputEntries+4)]; !ok {
		t.Error("最新的那条被淘汰了")
	}
}

// 盘上往返:文件不存在是正常的(还没观测过),不是错误。
func TestThroughputHistoryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "throughput.json")

	state, err := loadThroughputState(path)
	if err != nil {
		t.Fatalf("文件不存在被当成了错误:%v", err)
	}
	if len(state.Servers) != 0 {
		t.Fatal("空文件却读出了记录")
	}
	if err := recordThroughput(path, "tokyo", 3_100_000, thBase); err != nil {
		t.Fatal(err)
	}
	back, err := loadThroughputState(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Servers["tokyo"].PeakBPS != 3_100_000 {
		t.Fatalf("读回来不对:%+v", back.Servers["tokyo"])
	}
	// **信封必须在。** guardian-state.json 是个裸 JSON 字符串,没有版本 ——
	// 新旧 Guardian 共存的那个瞬间(升级)差点变成一次永久失联。
	raw, _ := os.ReadFile(path)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if _, ok := envelope["schema_version"]; !ok {
		t.Fatalf("盘上没有 schema_version:%s", raw)
	}
}

// 坏文件不许让任何东西失败:历史是诊断数据,丢了不影响任何保护。
// 但它要**如实报错**,好让调用方打一行日志 —— 静默吞掉的话,一个一直写不进去的
// 文件会表现成「这台服务器从来没被观测过」。
func TestCorruptThroughputHistoryIsRebuiltButReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "throughput.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadThroughputState(path); err == nil {
		t.Error("坏文件没有报错 —— 调用方无从知道历史丢了")
	}
	// 但记录仍然写得进去(从空的重建)。
	if err := recordThroughput(path, "tokyo", 1_000_000, thBase); err != nil {
		t.Logf("recordThroughput 报告了读阶段的错误(预期):%v", err)
	}
	back, err := loadThroughputState(path)
	if err != nil {
		t.Fatalf("重建之后仍然读不出来:%v", err)
	}
	if back.Servers["tokyo"].PeakBPS != 1_000_000 {
		t.Fatalf("重建之后没记上:%+v", back.Servers)
	}
}

// 版本对不上就当没有:一份诊断数据不值得为了兼容一个旧格式冒任何险。
func TestUnknownSchemaIsTreatedAsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "throughput.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":99,"servers":{"a":{"peak_bps":1}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := loadThroughputState(path)
	if err == nil {
		t.Error("未知版本没有报错")
	}
	if len(state.Servers) != 0 {
		t.Fatalf("未知版本的内容被读了进来:%+v", state.Servers)
	}
}
