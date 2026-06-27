// mutator 把一次改动翻译成 commit-confirmed 引擎要的 (apply, undo)。
// fake 测、nopMutator 生产(A2)、真 impl 留硬件刀(run.go 捕获 tun0/teardown/plat/cfg)。
package supervisor

// mutator:改动类操作的执行器。apply 执行改动;undo 语义回滚(路由还原另有 9a 快照网兜底)。
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
