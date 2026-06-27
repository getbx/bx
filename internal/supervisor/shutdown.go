package supervisor

import (
	"log"
	"os"
	"runtime"
	"time"
)

// shutdownGrace:还原(信号/死手/ctx)触发后,Run 的 cleanup(defer 链)允许的最长耗时。
// 超过则判定某个 defer 卡住(已知罕见 timing 竞态:疑 eng.Close/gVisor stack.Close 或 tun0.Stop),
// dump 全部 goroutine 栈 + 强制退出。死手契约是「到点必终止进程」,cleanup 卡住不该让它落空。
// 正常关机远快于此(实测 <1s),watchdog 不误触。
const shutdownGrace = 15 * time.Second

// armShutdownWatchdog 在 grace 后调用 onTimeout,返回 timer(Stop 可取消)。
// time.AfterFunc fire-and-forget:正常关机时 Run 返回→进程退出→timer 随进程作废(永不触发);
// cleanup 卡住则 grace 后触发 onTimeout。onTimeout 注入便于测试;生产传 dumpAndExit。
func armShutdownWatchdog(grace time.Duration, onTimeout func()) *time.Timer {
	return time.AfterFunc(grace, onTimeout)
}

// dumpAndExit 打印全部 goroutine 栈(定位卡住的 defer)后强制退出(码 1 标记异常关机)。
func dumpAndExit() {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	log.Printf("⚠ 关机超时 %s:cleanup 卡住,强制退出。goroutine 转储:\n%s", shutdownGrace, buf[:n])
	os.Exit(1)
}
