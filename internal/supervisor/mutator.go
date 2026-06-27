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
}

// nopMutator:不做任何真实改动(A2 生产挂载)。full commit-confirmed 回路仍真实跑,
// 只是 apply/undo 为空 —— brick-safe。真 mutator 接入是硬件刀。
type nopMutator struct{}

func nop() error { return nil }

func (nopMutator) SetTransport(string) (func() error, func() error, error) { return nop, nop, nil }
func (nopMutator) Rehijack() (func() error, func() error, error)           { return nop, nop, nil }
