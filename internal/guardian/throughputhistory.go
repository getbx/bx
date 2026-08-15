package guardian

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"
)

// 按服务器名记住「这台实际跑到过多快」。
//
// ## 为什么要存
//
// 吞吐是**被动观测**:没在用的服务器没有产生过流量,所以任何一个瞬间只有当前
// 那台有数。而用户真正的问题是「三台里哪台快」—— 那必然要跨时间。项目所有者
// 手工做的那次 A/B 正是这样:换过去、用一会儿、再换。存下来就是让 bx 自己记住
// 那件事,而不是每次都要人重做一遍。
//
// ## 陈旧不是问题,**假装不陈旧**才是
//
// 所有者明说「以前的值没事」。所以这里不设过期、不丢数据 —— 但每条都带观测时刻,
// 界面必须把年龄一起显示出来(「3.1 MB/s · 2 小时前」)。一个不带年龄的历史数字
// 读起来像现状,那才是谎。

// throughputStateSchema 是盘上格式的版本。
//
// **带信封是刻意的。** guardian-state.json 是个裸 JSON 字符串,没有版本没有信封,
// 于是新旧 Guardian 共存的那个瞬间(升级)差点变成一次永久失联(2026-08-10)。
// 新文件一律带版本。
const throughputStateSchema = 1

// maxThroughputEntries 是记录条数上限。
//
// 有上限是因为服务器名来自配置,而配置是用户改的:反复改名会让这个文件无限长。
// 淘汰**最旧的**那条 —— 最久没用过的服务器,也是用户最不关心的那台。
const maxThroughputEntries = 32

// throughputEntry 是一台的一条记录。
type throughputEntry struct {
	PeakBPS    int64     `json:"peak_bps"`
	ObservedAt time.Time `json:"observed_at"`
}

type throughputState struct {
	SchemaVersion int                        `json:"schema_version"`
	Servers       map[string]throughputEntry `json:"servers"`
}

// mergeThroughput 把一次观测并进历史。**纯函数**,不碰盘也不碰时钟。
//
// 规则只有一条:**记最新的那次观测,不记历史最大值**。
//
// 记最大值听起来更「有用」,而它会让一台今天已经不行的服务器永远顶着它一月份
// 那次的成绩 —— 那正是「陈旧数字冒充现状」。Core 那一侧的 RateMeter 已经把
// 「最近 30 分钟内的峰值」算好了,这里存的就是那个,带上它是什么时候看到的。
//
// 返回 changed=false 表示没有值得写盘的变化(观测无效,或与已有的完全一样)。
func mergeThroughput(state throughputState, name string, bps int64, at time.Time) (throughputState, bool) {
	name = strings.TrimSpace(name)
	// **无效观测一律不写。** 0 不是「跑不动」而是「这段时间没人用它传东西」,
	// 把它写进历史会把一条真实的记录覆盖成一个假的坏消息。
	if name == "" || bps <= 0 || at.IsZero() {
		return state, false
	}
	if state.Servers == nil {
		state.Servers = map[string]throughputEntry{}
	}
	if existing, ok := state.Servers[name]; ok {
		if existing.PeakBPS == bps && existing.ObservedAt.Equal(at) {
			return state, false
		}
		// **时光倒流的观测丢掉。** 系统时钟被改过时,一次「更早」的观测会把
		// 真正最新的那条盖掉,而界面会据此说它更新鲜。
		if at.Before(existing.ObservedAt) {
			return state, false
		}
	}
	state.SchemaVersion = throughputStateSchema
	state.Servers[name] = throughputEntry{PeakBPS: bps, ObservedAt: at}
	evictOldestThroughput(state.Servers)
	return state, true
}

// evictOldestThroughput 把记录砍回上限,淘汰最久没观测过的那些。
func evictOldestThroughput(servers map[string]throughputEntry) {
	if len(servers) <= maxThroughputEntries {
		return
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := servers[names[i]].ObservedAt, servers[names[j]].ObservedAt
		if a.Equal(b) {
			// 时刻相同时按名字定序:没有稳定顺序的话,同一份输入会淘汰掉
			// 不同的记录,而那种测试只会偶尔红。
			return names[i] < names[j]
		}
		return a.Before(b)
	})
	for _, name := range names[:len(servers)-maxThroughputEntries] {
		delete(servers, name)
	}
}

// loadThroughputState 读盘。**文件不存在是正常的**(还没观测过),返回空状态。
//
// 坏文件同样返回空状态并报错:调用方据此决定是记日志还是忽略 —— 一份诊断用的
// 历史绝不该让任何东西失败。
func loadThroughputState(path string) (throughputState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return throughputState{SchemaVersion: throughputStateSchema}, nil
		}
		return throughputState{SchemaVersion: throughputStateSchema}, err
	}
	var state throughputState
	if err := json.Unmarshal(data, &state); err != nil {
		return throughputState{SchemaVersion: throughputStateSchema}, err
	}
	if state.SchemaVersion != throughputStateSchema {
		// 版本对不上就当没有:一份诊断数据不值得为了兼容一个旧格式冒任何险。
		return throughputState{SchemaVersion: throughputStateSchema},
			fmt.Errorf("throughput history schema %d (want %d)", state.SchemaVersion, throughputStateSchema)
	}
	if state.Servers == nil {
		state.Servers = map[string]throughputEntry{}
	}
	return state, nil
}

// recordThroughput 把一次观测并进盘上的历史。
//
// **原子替换写盘,绝不 read-modify-write 之外的花样** —— 与 maintenance-hold
// 同一条纪律:多个进程可能同时写它,而安全**只**来自原子替换。
//
// 没有变化就不写盘:这个函数每 30 秒到 10 分钟被调一次,而绝大多数时候观测是
// 一样的;每次都写等于给一块 SSD 找事做。
func recordThroughput(path, name string, bps int64, at time.Time) error {
	state, err := loadThroughputState(path)
	if err != nil {
		// 读不出来就从空的开始重建:历史是诊断数据,丢了不影响任何保护。
		// 但错误要往上报,好让调用方打一行日志。
		state = throughputState{SchemaVersion: throughputStateSchema, Servers: map[string]throughputEntry{}}
	}
	merged, changed := mergeThroughput(state, name, bps, at)
	if !changed {
		return err
	}
	if writeErr := writeJSONAtomically(path, merged); writeErr != nil {
		return writeErr
	}
	return err
}
