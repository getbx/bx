package guardian

import (
	"log"
	"sync"
	"time"
)

// 本文件让维护挂起的一生在 **Guardian 日志**里留下痕迹。
//
// 为什么记在 Guardian 而不是武装它的那一侧:**武装挂起的是 CLI 进程**
// (internal/cli 的 recordStopIntent / forcedMacOSTeardown),而 internal/cli
// 几乎不打日志 —— 它的 log 输出是终端 stderr,一次 `bx app-install` 结束就没了,
// 而 Guardian 日志(/var/log/bx-guard.log)是真机上唯一留得住的那份。
//
// 更要紧的是**过期完全无声**:过期在读取时判(hold.go),没有定时器、没有事件,
// 没有任何一行代码在挂起失效的那一刻跑过。而「升级崩在半路、挂起自己过期了」
// 恰恰是排查时最需要看到的那个结局 —— 它是「用户要保护而机器上没有保护」这个
// 状态的唯一解释。
//
// 三条线因此都是**观测**而不是记账:Guardian 现问盘上那张挂起是什么样,把变化
// 记下来。与 internal/observe、以及 Core 所有权的进程扫描同一条原则。

// holdObserver 把「盘上那张挂起」的状态变化记进日志,**只在变化时记**。
//
// 存在的理由是它的反面:调谐环每 30 秒读一次挂起,recoverLocked、
// handleUnexpectedExit、以及每一次 /v1/status(菜单开着时 2 秒一次)也各读一次。
// 在读的地方直接打印,等于每台机器每天几万行「有一张挂起」/「没有挂起」——
// 那会把这个字段训练成用户和 agent 学会忽略的噪声,正好毁掉它唯一的价值
// (cli.go 的 observerForPlatform 那道门是同一个教训)。
//
// 去重的键是**挂起本身的身份**(reason + 到期时刻),不是「上一次记了什么」:
// 同一张挂起先被观测到武装、后被观测到过期,是两条要各记一次的事;而换了一张
// 新挂起(升级重试)则一切从头。
//
// **状态是进程级的**:Guardian 重启后会把它当时看到的那张挂起重新记一次。
// 这是有意的 —— 一个新进程如实报告它启动时发现了什么,比沉默有用得多,而升级
// 本来就会重启 Guardian,那一行正好标出「新 Guardian 起来时挂起还在」。
type holdObserver struct {
	mu            sync.Mutex
	seen          MaintenanceHold
	seenAny       bool
	armedLogged   bool
	expiredLogged bool
}

// observe 记下这一次读盘看到的挂起。**只接受读成功的那次观测**:调用方在
// err != nil 时不许调它 —— 「读不出来」不是「没有挂起」,把它当成后者正是
// hold.go 整个三返回值设计要消灭的谎。那一半由 intent_unreadable 负责。
func (o *holdObserver) observe(hold MaintenanceHold, armed bool, now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	// 零到期时刻 = 盘上没有挂起(ENOENT 那一支返回零值)。一张**合法**的挂起
	// 不可能是零 —— LoadMaintenanceHold 对零到期时刻直接报错,而报错的那次
	// 观测根本到不了这里。
	if hold.ExpiresAt.IsZero() {
		o.seen, o.seenAny = MaintenanceHold{}, false
		o.armedLogged, o.expiredLogged = false, false
		return
	}
	if !o.seenAny || !sameHold(o.seen, hold) {
		o.seen, o.seenAny = hold, true
		o.armedLogged, o.expiredLogged = false, false
	}
	switch {
	case armed && !o.armedLogged:
		o.armedLogged = true
		log.Printf("guardian_maintenance_hold_armed reason=%s expires_at=%s in=%s",
			hold.Reason, hold.ExpiresAt.Format(time.RFC3339), hold.ExpiresAt.Sub(now).Round(time.Second))
	case !armed && !o.expiredLogged:
		o.expiredLogged = true
		// **这是这一整个文件存在的理由。** 挂起过期不会恢复保护(设计取舍五),
		// 它买到的只是「不再压制」;于是一台升级崩在半路的机器就停在
		// 「desired=on 而没有保护」上,而在这行字之前,没有任何东西说过它为什么。
		log.Printf("guardian_maintenance_hold_expired reason=%s expires_at=%s ago=%s"+
			" note=protection_not_restored_by_expiry",
			hold.Reason, hold.ExpiresAt.Format(time.RFC3339), now.Sub(hold.ExpiresAt).Round(time.Second))
	}
}

// sameHold 判断两次观测看到的是不是同一张挂起。
//
// 到期时刻用 Equal 而不是 ==:后者比的是 time.Time 的内部表示(单调钟、时区
// 指针),两次分别从磁盘解出来的同一个时刻在那种比法下不保证相等,而一次误判
// 「换了张新挂起」就会让去重失效、每一轮重打一行 —— 正好变成本类型要防的噪声。
func sameHold(a, b MaintenanceHold) bool {
	return a.Reason == b.Reason && a.ExpiresAt.Equal(b.ExpiresAt)
}

// loadIntentSnapshot 是 Manager **唯一**读意图的入口,顺手把挂起的变化记进日志。
//
// 单点拥有是刻意的:观测挂起生命周期的机会只在读它的那一刻,而 Manager 里有四个
// 地方读(启动恢复、Core 意外退出、调谐环、发布 Status)。散着接线的话,新加一处
// 读盘就会悄悄少一双眼睛,而少掉的那双眼睛不会让任何测试变红。
func (m *Manager) loadIntentSnapshot(now time.Time) (IntentSnapshot, error) {
	intent, err := m.store.LoadIntentSnapshot(now)
	if err != nil {
		return intent, err
	}
	m.holdLog.observe(intent.Hold, intent.HoldArmed, now)
	return intent, nil
}
