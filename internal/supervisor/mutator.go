// mutator 把一次改动翻译成 commit-confirmed 引擎要的 (apply, undo)。
// fake 测、nopMutator 生产(A2)、真 impl 留硬件刀(run.go 捕获 tun0/teardown/plat/cfg)。
package supervisor

// mutator:改动类操作的执行器。apply 执行改动;undo 语义回滚(路由还原另有 9a 快照网兜底)。
// 约定:方法本身必须无副作用——只构造并返回 apply/undo 闭包,不做任何真实改动。
// 真实改动发生在 apply 内部(由 engine.Arm 在 armed 状态下持有,commit 时执行)。
// 原因:Arm 在已 armed 时直接返回 ErrAlreadyArmed 而不会运行 apply,
// 因此任何在方法体内执行的改动都会绕过快照/undo 机制,且在 already-armed 路径上无法回滚。
type mutator interface {
	SetTransport(link string) (apply func() error, undo func() error, err error)
	Rehijack() (apply func() error, undo func() error, err error)
	Reconnect() error
}

// nopMutator:不做任何真实改动(A2 生产挂载)。full commit-confirmed 回路仍真实跑,
// 只是 apply/undo 为空 —— brick-safe。真 mutator 接入是硬件刀。
type nopMutator struct{}

func nop() error { return nil }

func (nopMutator) SetTransport(string) (func() error, func() error, error) { return nop, nop, nil }
func (nopMutator) Rehijack() (func() error, func() error, error)           { return nop, nop, nil }
func (nopMutator) Reconnect() error                                         { return nil }

// rehijacker 是 liveMutator 对 platform 的窄依赖(只需路由-only 重落实)。
// platform 接口的方法集 ⊇ rehijacker,故 run.go 的 plat 可直接赋值;
// 单测的 fakePlatform 也只需实现这一个方法。
type rehijacker interface {
	RehijackRoutes(tun tunHandle, serverBypass, userBypass []string) error
}

// linkSwapper:把"换到某 link"抽象出来,使 liveMutator 的 commit-confirmed 逻辑可 fake 测;
// 真隧道操作(建/起/等健康/原子换/停旧)由 transportSwapper 实现、真机验。
type linkSwapper interface {
	currentLink() string
	swapTo(link string) error
}

// liveMutator:生产 mutator。Rehijack=路由-only 重落实(plat);SetTransport=换隧道(swap)。
// 两方法均指针接收者,必须以 &liveMutator{} 使用。
type liveMutator struct {
	plat         rehijacker
	swap         linkSwapper
	tunH         tunHandle
	serverBypass []string
	userBypass   []string
}

// SetTransport 返回真 apply:换到 newLink(建新+等健康+原子换+停旧)。
// 方法体无副作用(A2 契约):只读当前 link、构造闭包。undo 仅在确实换过时换回旧 link。
func (m *liveMutator) SetTransport(newLink string) (apply, undo func() error, err error) {
	oldLink := m.swap.currentLink()
	apply = func() error { return m.swap.swapTo(newLink) }
	undo = func() error {
		if m.swap.currentLink() == oldLink {
			return nil // apply 未换成(健康失败)→ 无需 undo
		}
		return m.swap.swapTo(oldLink)
	}
	return apply, undo, nil
}

// Reconnect 安全重建当前传输:swapTo 先让替代传输健康,再原子切换 dialer,
// 因而不碰 TUN、路由或 DNS,失败时旧传输保持原样。
func (m *liveMutator) Reconnect() error {
	return m.swap.swapTo(m.swap.currentLink())
}

// Rehijack 返回真 apply:在存活设备上重落实劫持路由(重探网关 + 拆旧路由 + 装新路由)。
// 方法体无副作用(A2 契约):只构造闭包。undo 为 nop —— 路由还原靠
// engine.Arm 的 snapshotter.Restore(9a 快照网);设备始终在,故快照可兜底。
func (m *liveMutator) Rehijack() (apply, undo func() error, err error) {
	apply = func() error { return m.plat.RehijackRoutes(m.tunH, m.serverBypass, m.userBypass) }
	undo = func() error { return nil }
	return apply, undo, nil
}
