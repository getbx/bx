package cli

import (
	"strings"
	"testing"
)

// 失败弹窗那句收尾「Run Doctor to collect diagnostics.」只许由一个人加。
//
// 2026-09-17 真机截图:更新失败的弹窗里那句话**连着出现了两次** ——
// `updateFailureMessage` 自己拼了一遍,而 `showFailure`(所有失败弹窗的唯一出口)
// 又补了一遍。它不是打错字,是「两层各自负责同一句收尾」这个形状:两边单独看都
// 对,合起来才露馅,而两边都没有测试盯着。
//
// 判据是**剥掉注释之后**那个字面量只出现一次 —— 上面这段解释里就写着它,而这个
// 仓库刚在 rehijack 那条守卫上因为"注释里恰好有锚点"吃过一次假绿。
func TestMacMenuAppendsTheDoctorSuffixInExactlyOnePlace(t *testing.T) {
	const suffix = "Run Doctor to collect diagnostics."
	code := stripSwiftComments(menuMainSwiftSource(t))
	if !strings.Contains(code, "showFailure") {
		t.Fatal("main.swift 里找不到 showFailure —— 守卫读不懂现在的代码了")
	}
	if n := strings.Count(code, suffix); n != 1 {
		t.Fatalf("「%s」在代码里出现了 %d 次,want 1 —— 多于一次就是同一句收尾被拼了两遍,"+
			"用户读到的是它连着出现;少于一次说明收尾没人加了", suffix, n)
	}
}
