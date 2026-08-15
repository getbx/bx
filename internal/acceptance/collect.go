package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/supervisor"
)

// 采集这一半会碰外界。**它只读** —— 三个 socket 请求(全是 GET)与一次读文件,
// 一个字节都不写。判定住在 checks.go 的纯函数里,这里只负责把事实搬过来。

const collectTimeout = 5 * time.Second

// Collect 问一遍这台机器。**任何一项失败都只记进它自己那个 Err 字段**,
// 绝不中断其余项、也绝不让整次采集失败(与 internal/observe 同源)。
func Collect(ctx context.Context) Facts {
	var f Facts
	client := guardian.NewClientWithTimeout(guardian.SocketPath, collectTimeout)

	statusCtx, cancel := context.WithTimeout(ctx, collectTimeout)
	f.Status, f.StatusErr = client.Status(statusCtx)
	cancel()

	serversCtx, cancel := context.WithTimeout(ctx, collectTimeout)
	f.Servers, f.ServersErr = client.ListServers(serversCtx)
	cancel()

	f.Report, f.ReportErr = supervisor.FetchStatusReport(supervisor.SockPath)
	f.ThroughputEntries, f.ThroughputErr = readThroughputEntries(guardian.DefaultThroughputHistoryPath)
	f.CoreUptime = coreUptime()
	return f
}

// readThroughputEntries 只数条数,不解读内容 —— 这里要回答的是「有没有在攒」,
// 而不是「攒了什么」。文件不存在与解析失败**分开报**:前者可能只是还没到时候。
func readThroughputEntries(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var envelope struct {
		Servers map[string]json.RawMessage `json:"servers"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return 0, err
	}
	return len(envelope.Servers), nil
}

// coreUptime 用**控制 socket 文件的创建时间**当作 Core 的启动时刻。
//
// 它是个近似,而且必须当近似用:socket 在 Run() 里创建,早于 Hijack;
// 而它唯一的用途是「刚起来的机器别骂它」,近似完全够。**问不出来时返回 0**,
// 而调用方对 0 的处置是「不因为时间短而豁免」—— 宁可多报一个 Fail 让人去看,
// 也不要用一个编出来的运行时长把真失败豁免掉。
func coreUptime() time.Duration {
	info, err := os.Stat(supervisor.SockPath)
	if err != nil {
		return 0
	}
	elapsed := time.Since(info.ModTime())
	if elapsed < 0 {
		// 时钟被改过。返回 0(= 不知道),绝不返回一个负的或荒谬的时长。
		return 0
	}
	return elapsed
}

// ErrNotSupported 说明这个平台上没有可问的对象(Guardian 只在 darwin 跑)。
var ErrNotSupported = errors.New("acceptance: 这台机器上没有 Guardian 可问")
