package supervisor

import (
	"errors"
	"fmt"
	"sync/atomic"
)

// composeMutations 把两对 (apply, undo) 串成一对,供 mutationEngine.Arm 使用。
//
// apply 按序执行,第二步失败即回滚第一步(不留半截 —— 半截状态在这里意味着
// 「路由已经改了但传输没换」或反过来,两种都不是任何人要的);
// undo 逆序执行,并把两边的错误都带出来。
func composeMutations(a1, u1, a2, u2 func() error) (apply, undo func() error) {
	// u1Owed 记「第一步的回滚还欠着没做」,保证 u1 **至多执行一次**。
	//
	// 必须有它:apply 失败时 mutationEngine.Arm 紧接着还会调一次 guard.Rollback
	// → 我们的 undo,而组合后的 apply 自己已经回滚过第一步了。没有这个标记,
	// 真引擎里的序列是 [a1 a2 u1 u2 u1] —— 对「拆路由」这类操作,第二次是在
	// 已经拆掉的东西上再拆一遍。
	//
	// 注意记的是「还欠着」而不是「已 apply」:undo 在 apply 之前被调到时
	//(死手在 Arm 与 apply 之间到点),两步的回滚都仍要照做 —— 那时无法断言
	// a1 一定没留下任何痕迹,少做一次回滚比多做一次危险。
	u1Owed := new(atomic.Bool)
	u1Owed.Store(true)
	once := func() error {
		if !u1Owed.CompareAndSwap(true, false) {
			return nil
		}
		return u1()
	}
	apply = func() error {
		if err := a1(); err != nil {
			return err
		}
		if err := a2(); err != nil {
			if rerr := once(); rerr != nil {
				return fmt.Errorf("%w(回滚前一步也失败: %v)", err, rerr)
			}
			return err
		}
		return nil
	}
	undo = func() error {
		var errs []error
		if err := u2(); err != nil {
			errs = append(errs, err)
		}
		if err := once(); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	return apply, undo
}
