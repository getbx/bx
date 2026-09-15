package install

import (
	"fmt"
	"os"
	"time"

	"github.com/getbx/bx/internal/elevate"
)

// logSource 是一份候选日志文件。
type logSource struct {
	Path    string
	ModTime time.Time
	// Stale 表示这个文件已经很久没被写过 —— 它多半属于一个早就不在跑的架构。
	Stale bool
}

// logStaleAfter 是「多久没写过就算陈旧」。
//
// 门槛不必精确:它只是为了让「上个月的文件」看起来不像「刚才的文件」。
const logStaleAfter = 24 * time.Hour

func logSourceIsStale(mod, now time.Time) bool {
	return now.Sub(mod) > logStaleAfter
}

// clientLogCandidates 是 macOS 上可能存在的 Core 日志,**活着的排在前面**。
//
// **2026-09-01 起 Core 又写回 bx.log 了**:此前它继承 Guardian 的 stdout/stderr,
// 于是两个进程挤进 bx-guard.*,而 Core 转发的传输子进程输出占了那个文件的 99%,
// 把 Guardian 自己的审计线索埋掉(见 guardian.openCoreLog)。bx-guard.* 现在
// 只剩 Guardian 自己的话。
//
// 三个路径全部保留为候选,并且**哪个是活着的那一个由修改时间决定、不由这份
// 顺序决定** —— 跨版本升级时机器上什么组合都可能有,写死优先级就是在猜。
func clientLogCandidates() []string {
	return []string{
		GuardianStdoutLogPath,
		GuardianStderrLogPath,
		launchdStdoutPath,
		launchdStderrPath,
	}
}

// clientLogSources 挑出实际存在的日志文件,按「最近写过的在前」排序。
//
// **陈旧的仍然列出来**(它可能还有历史线索),但会被标出来 —— 一份看起来正常、
// 其实是上个月的日志比没有日志更糟,而那正是 2026-08-14 把排查带进沟里的东西。
func clientLogSources(stat func(string) (time.Time, bool), now time.Time) []logSource {
	var sources []logSource
	for _, path := range clientLogCandidates() {
		mod, ok := stat(path)
		if !ok {
			continue
		}
		sources = append(sources, logSource{Path: path, ModTime: mod, Stale: logSourceIsStale(mod, now)})
	}
	// 最近写过的排前面。**不按候选顺序**:哪个是「活着的那个」是可观测的事实,
	// 不该靠一份写死的优先级去猜。
	for i := 1; i < len(sources); i++ {
		for j := i; j > 0 && sources[j].ModTime.After(sources[j-1].ModTime); j-- {
			sources[j], sources[j-1] = sources[j-1], sources[j]
		}
	}
	return sources
}

// logSourceHeader 是每份日志前面那一行:**哪个文件、多久之前写的**。
//
// 上一版只有一个文件时 `tail` 连文件名都不打,用户完全不知道自己在看什么。
func logSourceHeader(source logSource, now time.Time) string {
	age := now.Sub(source.ModTime).Round(time.Second)
	if source.Stale {
		return fmt.Sprintf("==> %s(最后写入 %s 前 —— **陈旧**,多半不是当前实例的日志)<==",
			source.Path, humanAge(age))
	}
	return fmt.Sprintf("==> %s(最后写入 %s 前)<==", source.Path, humanAge(age))
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%d天", int(d.Hours()/24))
	}
}

func statLogFile(path string) (time.Time, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// staleOnlyNotice 在**一份当前日志都没读到**时给一句收尾话。
//
// 起因是 2026-09-02 的真机排障:非 root 跑 `bx logs`,四段里前三段是
// `Permission denied`,最后一段是一个月前的旧实例内容 —— 而**最后那段正是眼睛
// 落下的地方**。逐段的提示都在(读失败会打、陈旧会标),但读的人拿走的是最后
// 那屏文字,于是把旧实例的状态当成了现在的状态,并据此差点写出一条不存在的 bug。
//
// **这不是「没提示」,是提示被排在了它要否定的那段内容前面。** 修法因此是收尾
// 补一句,而不是重做输出:前面每一段的事实都是对的,缺的是那个总结。
//
// 只在「没有任何**当前**来源被读到」时出声 —— 读到了当前日志就什么都不说,
// 一句恒真的提示会被训练成噪声。
func staleOnlyNotice(sources []logSource, readOK map[string]bool) string {
	var freshSeen, freshRead, staleRead bool
	for _, s := range sources {
		if s.Stale {
			if readOK[s.Path] {
				staleRead = true
			}
			continue
		}
		freshSeen = true
		if readOK[s.Path] {
			freshRead = true
		}
	}
	if freshRead || !freshSeen {
		// 读到了当前日志,或者压根没有当前日志(那是另一回事,别在这里下结论)。
		return ""
	}
	notice := "⚠ 一份**当前**日志都没读到 —— 上面能读到的内容来自已经停写的旧实例,**不代表现在的状态**。"
	if staleRead {
		notice += "\n  当前日志是 root-only(里面有服务器 IP 与旁路网段),用 `" + elevate.Prefix + "bx logs` 看。"
	}
	return notice + "\n"
}
