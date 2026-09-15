package supervisor

import (
	"errors"
	"fmt"
	"time"

	"github.com/getbx/bx/internal/elevate"
)

// 本文件是「换服务器」的编排,**cli 与 guardian 共用**。
//
// 放在这里是因为两边都要做同一件事,而它们不能互相 import(cli 引 guardian)。
// 写第二份的后果这个仓库已经反复见过:一份改了另一份没改,而两边测试都绿。

// SwitchDeps 把切换的四步注入进来,好让顺序可测。
type SwitchDeps struct {
	Arm      func(link, udp string) error
	Healthy  func() bool
	Commit   func() error
	Rollback func() error
}

// SwitchServer 执行一次 commit-confirmed 切换:**武装 → 验证 → 确认**。
//
// `/v0/server` 只负责武装;不确认的话死手到点会还原到 last-known-good。
// 第一版只调了武装就打印「立即生效」—— 真机上 Core 日志里
// `死手自动回滚` 出现了两次,而用户看到的「切过去了」只是回滚前的窗口。
//
// 新隧道不健康时**立刻回滚**而不是等死手:等的那段时间里流量走在一条不通的
// 隧道上,而且用户还以为切成功了。
func SwitchServer(deps SwitchDeps, name, link, udp string) error {
	if err := deps.Arm(link, udp); err != nil {
		return taggedSwitchError(ErrSwitchArmFailed,
			fmt.Errorf("切换到 %s 失败(未生效,仍在原来那台):%w", name, err))
	}
	if !deps.Healthy() {
		if rerr := deps.Rollback(); rerr != nil {
			return taggedSwitchError(ErrSwitchRollbackFailed,
				fmt.Errorf("切换到 %s 后隧道不健康,且回滚失败(%v)——"+
					"死手仍会在超时后还原,或直接 `"+elevate.Prefix+"bx down && "+elevate.Prefix+"bx up`", name, rerr))
		}
		return taggedSwitchError(ErrSwitchRolledBack,
			fmt.Errorf("切换到 %s 后隧道起不来,**已回滚**到原来那台", name))
	}
	if err := deps.Commit(); err != nil {
		return taggedSwitchError(ErrSwitchCommitFailed,
			fmt.Errorf("切换到 %s 已生效但确认失败(%v)——死手可能在超时后把它还原,"+
				"请立刻 `"+elevate.Prefix+"bx down && "+elevate.Prefix+"bx up` 让配置里的选择落定", name, err))
	}
	return nil
}

// 四种结局各有各的哨兵。
//
// **它们只是贴在上面那四句原话外面的标签,一个字都没改那四句。** `bx server use`
// 打的是 `%v` 的完整原话,而它今天比 GUI 诚实:Guardian 把四种压成一个码,于是
// 菜单对**已生效但确认失败**说「没切过去」—— 那是假的,它切过去了,而死手可能
// 在超时后把它还原,用户必须**立刻**动手;对**回滚也失败了**则轻描淡写了一次
// 正在发生的断网。哨兵存在的全部目的,就是让那两句话说得对。
var (
	// ErrSwitchArmFailed:根本没切过去,仍在原来那台。
	ErrSwitchArmFailed = errors.New("switch outcome: arm failed")
	// ErrSwitchRolledBack:切过去了但隧道起不来,**已经回滚**,现在仍在原来那台。
	ErrSwitchRolledBack = errors.New("switch outcome: rolled back")
	// ErrSwitchRollbackFailed:隧道起不来,而且回滚也失败了 —— 此刻可能正在断网。
	ErrSwitchRollbackFailed = errors.New("switch outcome: rollback failed")
	// ErrSwitchCommitFailed:**已经生效**,只是确认没做成,死手可能把它还原。
	ErrSwitchCommitFailed = errors.New("switch outcome: commit failed")
)

// taggedSwitchError 给一条错误贴上结局哨兵,**不改它一个字**。
//
// 为什么不是在 fmt.Errorf 里多写一个 %w:那会把哨兵的文字印进用户看见的那句话里。
// 要加的是一个机器读的标签,不是一句新的话。`Unwrap() []error` 让 errors.Is
// 同时认得哨兵与里面那条真错误(后者是 Core 报上来的原因,仍然只进日志)。
func taggedSwitchError(tag, err error) error { return switchOutcomeError{tag: tag, err: err} }

type switchOutcomeError struct {
	tag error
	err error
}

func (e switchOutcomeError) Error() string   { return e.err.Error() }
func (e switchOutcomeError) Unwrap() []error { return []error{e.tag, e.err} }

// LiveSwitchDeps 是接到真 Core 上的那四步。
func LiveSwitchDeps() SwitchDeps {
	return SwitchDeps{
		Arm: func(link, udp string) error {
			_, err := SetServerControl(SockPath, link, udp)
			return err
		},
		// **验证是「新隧道健康」,不是「命令没报错」。** 换过去之后隧道起不来
		// 才是这次切换真正的失败,而那要等健康检查跑一轮。
		Healthy: func() bool { return tunnelHealthyWithin(switchVerifyTimeout) },
		Commit: func() error {
			_, err := CommitControl(SockPath)
			return err
		},
		Rollback: func() error {
			_, err := RollbackControl(SockPath)
			return err
		},
	}
}

// switchVerifyTimeout 是等新隧道变健康的上限。
//
// 比死手的窗口短:必须在死手动手之前得出结论,否则我们的「确认」会撞上
// 它的「还原」,而两者谁先谁后是掷骰子。
const switchVerifyTimeout = 12 * time.Second

func tunnelHealthyWithin(limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for {
		if rep, err := FetchStatusReport(SockPath); err == nil && rep.TunnelHealthy {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}
