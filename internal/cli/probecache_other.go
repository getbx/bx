//go:build !unix

package cli

// statDirOwner 在没有 POSIX 属主概念的平台上**恒说「拿不到」**。
//
// 返回 (0, true) 会让判据把每个目录都当成 root 所有,于是每次调用都吐一句
// 猜出来的所有权诊断 —— 而那比原本的 permission denied 更能把人带偏。
// 「本平台没有这回事」与「属主是 0」是两件事。
func statDirOwner(string) (int, bool) { return 0, false }
