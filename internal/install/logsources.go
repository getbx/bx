package install

import (
	"fmt"
	"os"
	"time"
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
// Guardian 架构下 Core 是 Guardian 的子进程,写的是 bx-guard.*;
// bx.log / bx.err.log 是 legacy launchd 时代直接起 Core 时的路径。
// 后者在今天的安装上通常是一份三周前的遗留 —— 而 `bx logs` 一直只读它。
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
