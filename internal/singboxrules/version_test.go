package singboxrules

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/embedded"
)

// 翻译器按 spec §4 对 sing-box 1.14 实测的语义写;sing-box 的 route/DNS schema 跨版本有过
// 破坏性变更(§4.3:1.14 把 1.12 之前的 DNS 格式直接 FATAL)。内嵌版本由 CI 跟着上游
// 自动升,升到一个语义变了的版本而翻译器一个字不改,两边都绿 —— 这条守卫就是那一刻
// 唯一会说话的东西:内嵌版本变了,先回 spec §4 重测,再把常量改过去。
func TestTranslatorPinsTheEmbeddedSingboxVersion(t *testing.T) {
	got := strings.TrimPrefix(embedded.SingboxVersion(), "v")
	if got == "" {
		t.Fatal("内嵌的 sing-box 版本是空的 —— 这条守卫失去意义,必须响亮失败")
	}
	if got != TargetSingboxVersion {
		t.Fatalf("内嵌 sing-box 是 %s,翻译器钉的是 %s —— 先按 spec §4 重测语义,再改 TargetSingboxVersion", got, TargetSingboxVersion)
	}
}
