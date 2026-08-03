package cli

import (
	"runtime"
	"strings"
	"testing"
)

// unifiedLayoutActive 的文档承诺「其余 GOOS 恒 false」——darwin 上是否为 true 取决于
// 真实文件系统状态(runtimedir.Installed),不适合在通用单测里断言;非 darwin 平台
// 则应该在不碰任何文件系统的情况下恒为 false,这里钉死这条快速路径防回归。
func TestUnifiedLayoutActiveFalseOnNonDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin 上的真实值依赖本机 runtimedir 安装状态,由真机/集成验证覆盖")
	}
	if unifiedLayoutActive() {
		t.Fatal("unifiedLayoutActive 在非 darwin 平台必须恒为 false")
	}
}

func TestUnifiedLayoutDegradedWith(t *testing.T) {
	cases := []struct {
		goos               string
		artifacts, healthy bool
		want               bool
	}{
		{"darwin", true, false, true},   // 损坏:产物在、健康检查挂
		{"darwin", true, true, false},   // 健康统一布局
		{"darwin", false, false, false}, // 纯 legacy(无产物)
		{"linux", true, false, false},   // 非 darwin 恒 false
	}
	for _, tc := range cases {
		if got := unifiedLayoutDegradedWith(tc.goos, tc.artifacts, tc.healthy); got != tc.want {
			t.Fatalf("(%s,%v,%v)=%v want %v", tc.goos, tc.artifacts, tc.healthy, got, tc.want)
		}
	}
}

func TestUnifiedRepairHintMentionsRepairPath(t *testing.T) {
	for _, want := range []string{"Bx.app", "app-install", "bridge"} {
		if !strings.Contains(unifiedRepairHint, want) {
			t.Fatalf("hint missing %q", want)
		}
	}
}
