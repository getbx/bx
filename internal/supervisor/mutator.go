// mutator 把一次改动翻译成 commit-confirmed 引擎要的 (apply, undo)。
// fake 测、nopMutator 生产(A2)、真 impl 留硬件刀(run.go 捕获 tun0/teardown/plat/cfg)。
package supervisor

import (
	"errors"
	"fmt"
	"sync"
)

// mutator:改动类操作的执行器。apply 执行改动;undo 语义回滚(路由还原另有 9a 快照网兜底)。
// 约定:方法本身必须无副作用——只构造并返回 apply/undo 闭包,不做任何真实改动。
// 真实改动发生在 apply 内部(由 engine.Arm 在 armed 状态下持有,commit 时执行)。
// 原因:Arm 在已 armed 时直接返回 ErrAlreadyArmed 而不会运行 apply,
// 因此任何在方法体内执行的改动都会绕过快照/undo 机制,且在 already-armed 路径上无法回滚。
type mutator interface {
	SetTransport(link string) (apply func() error, undo func() error, err error)
	SetServer(link, udp string) (apply func() error, undo func() error, err error)
	Rehijack() (apply func() error, undo func() error, err error)
	Reconnect() error
}

// nopMutator:不做任何真实改动(A2 生产挂载)。full commit-confirmed 回路仍真实跑,
// 只是 apply/undo 为空 —— brick-safe。真 mutator 接入是硬件刀。
type nopMutator struct{}

func nop() error { return nil }

func (nopMutator) SetTransport(string) (func() error, func() error, error) { return nop, nop, nil }
func (nopMutator) SetServer(string, string) (func() error, func() error, error) {
	return nop, nop, nil
}
func (nopMutator) Rehijack() (func() error, func() error, error) { return nop, nop, nil }
func (nopMutator) Reconnect() error                              { return nil }

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
	udpSwap      linkSwapper // nil = 该配置没有 UDP 专用槽
	tunH         tunHandle
	mu           sync.Mutex
	serverBypass []string
	// store 非空时**压过** serverBypass:那是全进程唯一那份「什么必须绕开隧道」,
	// 刷新写它、路径恢复读它、这里也读它。各自留一份冻结拷贝正是成环的来源。
	store      *bypassStore
	userBypass []string
	routes     *routeReadiness
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

// SetServerBypass 更新 bypass 集合。加**新**服务器时新 IP 不在启动时算好的集合里,
// 必须先更新再 Rehijack —— 顺序反了就等于没装,而没装就成环。
func (m *liveMutator) SetServerBypass(cidrs []string) {
	if m.store != nil {
		// 只换路由那一半,静态 DNS 保持原样(这条路径的调用方只知道 CIDR)。
		m.store.set(cidrs, m.store.staticEntries(), m.store.serverAddrs())
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.serverBypass = append([]string(nil), cidrs...)
}

func (m *liveMutator) currentServerBypass() []string {
	if m.store != nil {
		return m.store.cidrs()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.serverBypass...)
}

// SetServer 换一台服务器 = 换一对链接。两个槽必须一起换成功或一起留在原地:
// 半切状态(TCP 在 tokyo、UDP 还在 hk)是个合法配置,不报错、还能用,
// 但出口不是用户以为的那台。
//
// udp 为空表示目标服务器没有 UDP 专用传输 —— 此时 UDP 槽跟随主传输,
// 而不是留着上一台的(留着 = UDP 流量仍从上一台出去)。
func (m *liveMutator) SetServer(link, udp string) (apply, undo func() error, err error) {
	oldMain := m.swap.currentLink()
	var oldUDP string
	if m.udpSwap != nil {
		oldUDP = m.udpSwap.currentLink()
	}
	targetUDP := udp
	if targetUDP == "" {
		targetUDP = link
	}
	apply = func() error {
		if err := m.swap.swapTo(link); err != nil {
			return fmt.Errorf("换主传输: %w", err)
		}
		if m.udpSwap == nil {
			return nil
		}
		if err := m.udpSwap.swapTo(targetUDP); err != nil {
			// 主传输已经换过去了。留着就是半切状态,故立刻换回。
			if rerr := m.swap.swapTo(oldMain); rerr != nil {
				return fmt.Errorf("换 UDP 传输失败(%w),回退主传输也失败(%v)——两个槽可能不一致", err, rerr)
			}
			return fmt.Errorf("换 UDP 传输: %w(主传输已换回 %s)", err, transportLabel(oldMain))
		}
		return nil
	}
	undo = func() error {
		var errs []error
		if m.swap.currentLink() != oldMain {
			if err := m.swap.swapTo(oldMain); err != nil {
				errs = append(errs, err)
			}
		}
		if m.udpSwap != nil && m.udpSwap.currentLink() != oldUDP && oldUDP != "" {
			if err := m.udpSwap.swapTo(oldUDP); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	return apply, undo, nil
}

// Rehijack 返回真 apply:在存活设备上重落实劫持路由(重探网关 + 拆旧路由 + 装新路由)。
// 方法体无副作用(A2 契约):只构造闭包。undo 先悲观清 readiness;路由还原仍靠
// engine.Arm 的 snapshotter.Restore(9a 快照网),未验证的还原不会重新宣称路由就绪。
func (m *liveMutator) Rehijack() (apply, undo func() error, err error) {
	apply = func() error {
		m.setRoutesInstalled(false)
		if err := m.plat.RehijackRoutes(m.tunH, m.currentServerBypass(), m.userBypass); err != nil {
			return err
		}
		m.setRoutesInstalled(true)
		return nil
	}
	undo = func() error {
		m.setRoutesInstalled(false)
		return nil
	}
	return apply, undo, nil
}

func (m *liveMutator) setRoutesInstalled(installed bool) {
	if m.routes != nil {
		m.routes.set(installed)
	}
}
