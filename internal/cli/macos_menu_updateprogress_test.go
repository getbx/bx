package cli

import (
	"strings"
	"testing"
)

// 下载进度的前缀是一条**跨语言契约**:产地是 Go 的 formatDownloadProgress,
// 消费方是 Swift 的 lastDownloadProgressLine(它手抄了一份常量)。
//
// 漂掉的后果是**完全静默的**:菜单一行进度也找不到,于是永远退回那句只有秒数的
// 话 —— 而那正是这次改动要消灭的东西;两侧测试照样全绿。本仓库为同一个形状栽过
// 一次(leakcheck 页面的探针名),那次的修法也是双向钉住。
func TestMenuReadsTheSameDownloadProgressMarkerTheCLIWrites(t *testing.T) {
	src := stripSwiftComments(readMenuSwiftSource(t, "UpdatePresentation.swift"))
	marker, ok := swiftStringLiteralFor(src, "downloadProgressMarker")
	if !ok {
		t.Fatal("UpdatePresentation.swift 里找不到 downloadProgressMarker —— 守卫读不懂现在的代码了")
	}
	if marker == "" {
		t.Fatal("downloadProgressMarker 是空串:那样每一行都会被当成进度行")
	}
	// 正向:Go 打出来的每一种进度行都必须带这个前缀(有总量与没总量两条路都要)。
	for _, line := range []string{
		formatDownloadProgress(1<<20, 40<<20),
		formatDownloadProgress(1<<20, -1),
	} {
		if !strings.HasPrefix(line, marker) {
			t.Fatalf("Go 打的进度行 %q 不以菜单认的前缀 %q 开头 —— 菜单会一行都找不到", line, marker)
		}
	}
	// 反向:那个常量必须真的被用在解析上,而不只是定义着。
	if !strings.Contains(src, "line.hasPrefix(downloadProgressMarker)") {
		t.Fatal("downloadProgressMarker 定义了却没参与解析 —— 它守不住任何东西")
	}
}

// 接线:菜单那一行必须由**这一轮的真实输入**算出来。
//
// 判据刻意不是「updateStageText 被调用过」—— 那是本仓库列过的第七种失效写法
// (判据对了,而把真实输入递给它那根线没人守)。这里钉的是到达的**值**:
// 阶段来自 Guardian 的 phase(不是字面量),进度来自这一次的日志文件。
func TestMacMenuUpdateRowIsFedTheRealStageAndTheRealLog(t *testing.T) {
	src := stripSwiftComments(menuMainSwiftSource(t))
	for _, want := range []string{
		"installing: updateStageInstalling()",
		"progress: lastDownloadProgressLine(updateLogTail())",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("main.swift 里没有 %q —— 那一行没有接上真实输入", want)
		}
	}
	// 写死 true/false 就是没看答案先宣布在哪一段(与 leakcheck 那条
	// probeLanded(probe, true) 同形)。
	for _, forbidden := range []string{"installing: true", "installing: false"} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("main.swift 里有 %q:阶段被写死了", forbidden)
		}
	}
	// 合并那句文案只许住在 updateStageText 的「问不出来」那一支里,
	// 别处再拼一遍就等于绕过分段。
	if strings.Contains(src, "Downloading and installing") {
		t.Fatal("main.swift 又自己拼了一遍合并文案 —— 分段会被它绕过去")
	}
	// 日志路径要跟着这一次更新走:起的时候记上,收尾清掉。少了后者,下一次
	// 更新开始前那一瞬间会读到上一次的日志,把陈旧进度显示成当前进度。
	for _, want := range []string{"updateLogPath = logPath", "updateLogPath = nil"} {
		if !strings.Contains(src, want) {
			t.Fatalf("main.swift 里没有 %q", want)
		}
	}
}

// swiftStringLiteralFor 取 `let <name> = "…"` 里那个字面量。
func swiftStringLiteralFor(src, name string) (string, bool) {
	marker := "let " + name + " = \""
	i := strings.Index(src, marker)
	if i < 0 {
		return "", false
	}
	rest := src[i+len(marker):]
	j := strings.Index(rest, "\"")
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}
