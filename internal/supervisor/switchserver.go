package supervisor

import "fmt"

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
		return fmt.Errorf("切换到 %s 失败(未生效,仍在原来那台):%w", name, err)
	}
	if !deps.Healthy() {
		if rerr := deps.Rollback(); rerr != nil {
			return fmt.Errorf("切换到 %s 后隧道不健康,且回滚失败(%v)——"+
				"死手仍会在超时后还原,或直接 `sudo bx down && sudo bx up`", name, rerr)
		}
		return fmt.Errorf("切换到 %s 后隧道起不来,**已回滚**到原来那台", name)
	}
	if err := deps.Commit(); err != nil {
		return fmt.Errorf("切换到 %s 已生效但确认失败(%v)——死手可能在超时后把它还原,"+
			"请立刻 `sudo bx down && sudo bx up` 让配置里的选择落定", name, err)
	}
	return nil
}
