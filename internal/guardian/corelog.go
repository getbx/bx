package guardian

import (
	"errors"
	"os"
)

const (
	// coreLogMaxBytes 是 Core 日志轮转的门槛。
	//
	// 真机 2026-09-01 的教训是**无轮转**,不是这个数取多少:那台机器上的
	// bx-guard.err.log 长到 100MB。8MB 足够装下一次故障的上下文,又不至于
	// 让日志变成磁盘上第二大的东西。
	coreLogMaxBytes = 8 << 20
)

// openCoreLog 打开 Core 自己的日志,必要时先轮转。
//
// **轮转的判据放在每次 spawn,是刻意的**:Core 只在两次 spawn 之间写这个文件,
// 所以这里永远不需要给一个**正在被写**的 fd 做轮转。那件事在 macOS 上做不干净
// —— launchd 持有 Guardian 的 stderr fd,newsyslog 把文件 rename 之后写入会
// 跟着进归档文件,于是审计线索被悄悄写进一个即将被删掉的文件里。**那比一个
// 大文件糟得多**:大文件至少还看得见。
//
// 归档只留一代:上限存在的理由是磁盘,多留几代就是拿磁盘换一段没人会读的历史。
func openCoreLog(path string, maxBytes int64) (*os.File, error) {
	if path == "" {
		// 本平台没有「给 Core 单开日志」这回事。它与「该开却开不出来」是两件
		// 事,调用方对两者的处置相同(退回继承),但只有后者值得记一行日志。
		return nil, errors.New("core log path not configured on this platform")
	}
	if info, err := os.Stat(path); err == nil && info.Size() >= maxBytes {
		// 轮转失败不算致命:接着往原文件写,总好过不写。
		_ = os.Rename(path, path+".1")
	}
	// O_APPEND 而非 O_TRUNC:没超上限时旧内容是排查一次故障要看的上文,
	// 而 Core 每次崩溃重启都会走这条路。0600 —— 日志里有服务器 IP 与 116 条
	// bypass 网段,这正是 2026-08-05 把 guard 日志从 0644 收紧的理由。
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}
