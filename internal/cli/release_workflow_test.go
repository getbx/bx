package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// **release.yml 只在打 tag 时跑,平时的 CI 一次都覆盖不到它。**
//
// 2026-08-17 v0.3.0 的第一次发布就死在这上面:一次「dist → dist.noindex」的
// 全局替换把 build job 的产物路径改了,而同一段里的 `cd dist && zip` 没跟着改,
// 于是 `zip: Nothing to do`,build 失败、后面三个 job 全部 skipped。
// 而那个后缀的理由(本机 Spotlight 不索引构建产物)在 CI 上根本不成立。
//
// 这条守卫是那条路上唯一在平时跑得到的检查。
func TestReleaseBuildJobUsesOneDistPath(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("读不到 release.yml:%v —— 守卫已经失效,先修守卫", err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // 注释里可以谈论旧路径
		}
		if !strings.Contains(line, "dist.noindex") {
			continue
		}
		// **唯一合法的一处**:读 package-macos-release.sh 的产物,那个脚本确实
		// 写到 dist.noindex/release(它跑在开发者的 Mac 上,有 Spotlight 问题)。
		if strings.Contains(line, "dist.noindex/release/") {
			continue
		}
		t.Errorf("release.yml:%d 用了 dist.noindex,而 CI 上没有 Spotlight 问题;"+
			"混着用会让打包那几行找不到产物:%s", i+1, trimmed)
	}
}
