//go:build !darwin

package leakserve

// LiveFactDeps 在非 darwin 上一个能力都没有:本机事实的原语(route -n get /
// networksetup / scutil)目前只有 macOS 实现。
//
// **返回空 FactDeps 是诚实的**:CollectFacts 会产出一份「什么都不知道」的事实,
// Judge 于是只会输出 not checked。伪造几个返回零值的函数填进去才是撒谎 ——
// 那会让每一项都变成「查了,没有」,而 not checked 与 ok 的区别正是这个功能的
// 全部价值所在。
func LiveFactDeps() FactDeps { return FactDeps{} }
