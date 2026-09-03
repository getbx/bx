//go:build !unix

package cli

import "testing"

// **「本平台没有属主这回事」与「属主是 0」是两件事。**
//
// 返回 (0, true) 会让判据把每个目录都当成 root 所有,于是每次调用都吐一句
// 猜出来的所有权诊断 —— 比原本的 permission denied 更能把人带偏。
// 这条只在非 unix 的 CI 腿上跑(darwin 上那个文件根本不编译)。
func TestStatDirOwnerAdmitsItCannotTellOnThisPlatform(t *testing.T) {
	if _, ok := statDirOwner(`C:\Windows`); ok {
		t.Error("本平台拿不到 POSIX 属主,却报告说拿到了")
	}
}
