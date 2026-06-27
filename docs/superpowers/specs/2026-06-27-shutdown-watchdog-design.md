# 关机 watchdog(死手/信号还原后保证进程终止)设计

Status: APPROVED(2026-06-27,用户认可推荐方案)。

## 背景

Rehijack 路由-only 真机回归收尾发现:死手到点后 bx **路由正确还原**(安全 OK),但 bx **进程**偶发卡住不退出(需手 kill)。systematic-debugging 结论:linger = `Run()` 未返回 = 某个 shutdown **defer 阻塞**(收敛到 `eng.Close()`/gVisor `stack.Close` 或 `tun0.Stop()`/`<-t.done`,疑被环回测试拓扑的连接 churn 触发)。11 次复现未抓到 goroutine 转储——**罕见 timing 竞态,根因未确证**。非安全问题(路由总还原)、生产 systemd `TimeoutStopSec` 会 SIGKILL 兜底,只咬 `--test-timeout` 调试路径。

## 目标 / 非目标

**目标**:给 `Run` 的关机加 watchdog——还原(信号/死手/ctx)触发后,若 cleanup(defer 链)超过 grace 仍未完成,**dump 全部 goroutine 栈(定位卡住的 defer)+ `os.Exit`**。这既是 timing 竞态的「appropriate handling」(死手契约 = 到点必终止进程),又是「monitoring」(下次现场自动抓到根因,定位 `eng.Close` vs `tun0.Stop`)。

**非目标**:确证/修复那个具体 defer(等 watchdog 在野抓到转储后再精修);改 systemd 行为。

## 设计

`internal/supervisor/shutdown.go`:
- `const shutdownGrace = 15 * time.Second`——正常关机远快于此(实测清理 <1s),watchdog 不误触;卡住才触发。
- `func armShutdownWatchdog(grace time.Duration, onTimeout func()) *time.Timer { return time.AfterFunc(grace, onTimeout) }`——可注入 onTimeout 便于测试;返回 timer 便于测试取消。
- `func dumpAndExit()`——`runtime.Stack(buf, true)` 全 goroutine 转储 → `log.Printf` → `os.Exit(1)`。

`run.go`:阻塞 select(信号/死手/ctx)之后、`return nil` 之前,`armShutdownWatchdog(shutdownGrace, dumpAndExit)`。**机制**:`time.AfterFunc` 起独立 goroutine;正常关机 Run 返回→main 退出→进程消失→timer 随进程作废(永不触发);卡住则 grace 后触发 dumpAndExit。**无需** close(done) 信号——进程正常退出即让 watchdog 自然作废。

## 测试

Mac 原生免 root(`shutdown_test.go`,无 build tag):
- `armShutdownWatchdog(5ms, close(fired))` → fired 在 2s 内收到(触发)。
- `tm := armShutdownWatchdog(50ms, ...); tm.Stop()` 返回 true 且之后不触发(可取消)。
- `dumpAndExit` 含 `os.Exit` 不单测(无逻辑;seam 让生产注入它、测试用 fake)。

真机验证(B 阶段):部署带 watchdog 的二进制 → 反复跑死手关机路径。正常关机:进程秒退、watchdog 不触发。若 linger 复发:watchdog 15s 后 dump goroutine 到 run.log + 强制退出(无需手 kill)→ **捕获根因**(看哪个 goroutine 卡在 eng.Close/tun0.Stop)→ 下一刀精修。

## 决策记录

- watchdog = timing 竞态的 appropriate-handling + monitoring(systematic-debugging「no reliably-reproducible root cause」分支认可)。
- `time.AfterFunc` fire-and-forget,正常退出自然作废,无需 cleanup-done 信号。
- grace 15s:远大于正常清理(<1s)、远小于人会等的时间;dump 后 `os.Exit(1)`(非 0,标记异常退出)。
- 不在本刀确证 eng.Close vs tun0.Stop——交给 watchdog 在野抓转储。
