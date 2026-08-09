package supervisor

import (
	"errors"
	"fmt"
)

// composeMutations 把两对 (apply, undo) 串成一对,供 mutationEngine.Arm 使用。
//
// apply 按序执行,第二步失败即回滚第一步(不留半截 —— 半截状态在这里意味着
// 「路由已经改了但传输没换」或反过来,两种都不是任何人要的);
// undo 逆序执行,并把两边的错误都带出来。
func composeMutations(a1, u1, a2, u2 func() error) (apply, undo func() error) {
	apply = func() error {
		if err := a1(); err != nil {
			return err
		}
		if err := a2(); err != nil {
			if rerr := u1(); rerr != nil {
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
		if err := u1(); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	return apply, undo
}
