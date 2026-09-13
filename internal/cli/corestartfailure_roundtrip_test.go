package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getbx/bx/internal/corestartfailure"
	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/supervisor"
)

// **生产的写方与生产的读方,在同一个测试里碰面。**
//
// 在这一条之前,这条跨进程的线两头各有各的测试而从不相遇:写那半只在
// internal/cli 里被测(对着一个临时目录),读那半只在 internal/guardian 里被测
// (对着手工拼出来的 Record)。于是**三条各一行的改动都能让这个功能整个退回
// 改动前,而三个包全绿**:
//
//   - 构造器里那句 StartFailurePath 被删掉(路径变空 ⇒ coreArgs 不带 flag ⇒
//     Core 一个字节都不写);
//   - coreArgs 收到的是 "" 而不是 r.startFailurePath();
//   - 写记录时 At 取零值(每一份记录都掉出新鲜度窗口)。
//
// 前两条另有各自的守卫(startFailurePath 的默认兜底 + spawn 那一刻的 argv 断言),
// 这一条钉的是**跨进程那两个值本身**:写下去的字节,读回来必须还是同一个码。
// 判据刻意不是「调用发生过」—— 这一支上「判据是对的,而把真实输入递给它的那根线
// 没人守」已经出现四次。
func TestTheCoreWritesExactlyWhatTheGuardianReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core-start-failure.json")

	// fork **之前**那一刻 —— Guardian 就是这么取 since 的(manager.go 的 spawnedAt)。
	since := time.Now()

	// 2026-09-12 那次事故的形状:VPS 的端口没有应答。
	cause := fmt.Errorf("到服务器 203.0.113.92:443 的 TCP 连接没有建立: %w", supervisor.ErrTunnelUnreachable)

	// —— 写:Core 那一侧真正在跑的那条路(bx run 的包装)。
	if back := runWithStartFailureRecord(path, func() error { return cause }); !errors.Is(back, supervisor.ErrTunnelUnreachable) {
		t.Fatalf("包装改写了 Run 返回的错误:%v —— 一个诊断不许把一次故障换成另一次", back)
	}

	// —— 读:Guardian 那一侧真正在跑的那条路(生产构造器造出来的 runner)。
	// **先钉住它开箱就指着那个共享的默认位置** —— 写的人与读的人只有一个
	// corestartfailure.DefaultPath,构造器不给这个值就等于 Core 收不到 flag。
	//
	// **这里的裸 NewExecCoreRunner 是刻意的例外**(其余全走 newRoundtripRunner):
	// 断言的对象就是生产默认值本身,换成一个指进 t.TempDir() 的 runner,
	// 这一句会恒真而什么也不守。它只读字段,不 spawn、不碰文件系统。
	if fresh := guardian.NewExecCoreRunner("/usr/local/bin/bx", "/etc/bx/config.yaml", "127.0.0.1:53"); fresh.StartFailurePath != corestartfailure.DefaultPath {
		t.Fatalf("生产构造器交出来的记录位置是 %q,want %q", fresh.StartFailurePath, corestartfailure.DefaultPath)
	}
	runner := newRoundtripRunner(t, dir, path)
	got := runner.StartFailureCode(context.Background(), guardian.Process{PID: os.Getpid()}, since)
	if got != supervisor.StartFailureTunnelUnreachable {
		t.Fatalf("Guardian 从 Core 写下的那份记录里读出 %q,want %q ——\n"+
			"跨进程那条线断了,而两头各自的测试都是绿的",
			got, supervisor.StartFailureTunnelUnreachable)
	}

	// 读完即删(诊断记录不许留到下一次 spawn 去冒充「这一次」)。
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("记录读完没被删掉(stat=%v)", err)
	}
}

// 同一条线的反面:**写的人一个字节都没写时,读的人绝不能编一个码出来。**
//
// 手敲的 `sudo bx run` 不带 --start-failure-file,那时这条路整个关掉;而
// Guardian 读到的必须是「这一次没说」(空串 ⇒ 回落 core_health_failed),
// 不是任何一个具体病因。
func TestNoRecordMeansTheGuardianSaysNothingRatherThanGuessing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core-start-failure.json")

	cause := fmt.Errorf("到服务器的 TCP 连接没有建立: %w", supervisor.ErrTunnelUnreachable)
	// 路径为空 = 手敲的 bx run:一个字节都不写。
	if back := runWithStartFailureRecord("", func() error { return cause }); !errors.Is(back, supervisor.ErrTunnelUnreachable) {
		t.Fatalf("包装改写了 Run 返回的错误:%v", back)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("没给路径却写出了记录(stat=%v)—— 陈旧记录的第一层防线没了", err)
	}

	runner := newRoundtripRunner(t, dir, path)
	if got := runner.StartFailureCode(context.Background(), guardian.Process{PID: os.Getpid()}, time.Now().Add(-time.Second)); got != "" {
		t.Fatalf("没有记录时读出了 %q —— 「问不出来」不是任何一个具体答案", got)
	}
}

// newRoundtripRunner 造读那一半用的 runner。
//
// **两个路径字段都要指走**:guardian.NewExecCoreRunner 交出来的 StatePath 与
// StartFailurePath 都落在 /var/lib/bx —— 项目所有者真机上正在用的那个目录。
// 这两个测试今天只调 StartFailureCode(碰不到 StatePath),而「今天碰不到」
// 不是一条能留给下一个人的保证;guardian 那半的同款 helper
// (newTestCoreRunner)也是两个一起指走的,理由写在那里。
//
// 由 guardian 侧的 TestTestsNeverPointACoreRunnerAtTheProductionPaths 全仓钉住:
// 任何一个 *_test.go 里新长出来的裸 NewExecCoreRunner( 都会让它转红。
func newRoundtripRunner(t *testing.T, dir, startFailurePath string) *guardian.ExecCoreRunner {
	t.Helper()
	runner := guardian.NewExecCoreRunner(filepath.Join(dir, "bx"), filepath.Join(dir, "config.yaml"), "127.0.0.1:53")
	runner.StatePath = filepath.Join(dir, "core-process.json")
	runner.StartFailurePath = startFailurePath
	return runner
}
