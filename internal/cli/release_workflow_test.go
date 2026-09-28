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

// **正式发布必须签 Developer ID 并公证;平时的 CI 打包必须不签。**
//
// 2026-09-28 起 the publisher's LLC 的证书与公证密钥住在仓库 secrets 里。打包脚本在凭据
// 不全时会安静跳过公证(本机开发打包要能跑),所以「release.yml 忘了把 secrets 递进
// 环境」在打包那一步一个字都不会报 —— 发出去的是一个「无法验证开发者」的包,而
// README 里写着「官方发布已公证」。这里钉的是接线:跑 package-macos-release.sh 的那个
// job,在 release.yml 里必须把证书与三样公证凭据都交出去;在 ci.yml 里必须一样都不交
// (fork 的 PR 拿不到 secrets,交了只会让 CI 在 secrets 缺席时红)。
func TestReleaseWorkflowSignsAndNotarizesTheMacPackageAndCIDoesNot(t *testing.T) {
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
		if err != nil {
			t.Fatalf("读不到 %s:%v —— 守卫已经失效,先修守卫", name, err)
		}
		return string(raw)
	}
	release := read("release.yml")
	for _, want := range []string{
		"secrets.MACOS_SIGNING_CERT_P12",
		"secrets.MACOS_SIGNING_CERT_PASSWORD",
		"secrets.APPLE_NOTARY_KEY_ID",
		"secrets.APPLE_NOTARY_ISSUER_ID",
		"secrets.APPLE_NOTARY_KEY_P8",
		"BX_CODESIGN_IDENTITY=",
		"BX_NOTARY_KEY_PATH=",
		"security delete-keychain",
	} {
		if !strings.Contains(release, want) {
			t.Errorf("release.yml 缺 %q —— 发出去的 macOS 包会是「无法验证开发者」", want)
		}
	}
	// 三样公证凭据必须在**同一步**里交给打包脚本(打包脚本按 BX_NOTARY_* 读)。
	pack := strings.Index(release, "scripts/package-macos-release.sh")
	if pack < 0 {
		t.Fatal("release.yml 里没有 job 跑 package-macos-release.sh —— 守卫的锚点漂了")
	}
	step := release[strings.LastIndex(release[:pack], "- name:"):pack]
	for _, want := range []string{"BX_NOTARY_KEY_ID:", "BX_NOTARY_ISSUER_ID:", "APPLE_NOTARY_KEY_P8:"} {
		if !strings.Contains(step, want) {
			t.Errorf("release.yml 打包那一步没有把 %s 交给脚本", want)
		}
	}
	ci := read("ci.yml")
	for _, forbidden := range []string{"secrets.MACOS_SIGNING", "secrets.APPLE_NOTARY", "BX_CODESIGN_IDENTITY"} {
		if strings.Contains(ci, forbidden) {
			t.Errorf("ci.yml 不该碰 %s:平时的 CI 打包是 ad-hoc 的,fork 的 PR 也拿不到 secrets", forbidden)
		}
	}
	// 验证脚本在身份是 Developer ID 时必须真的去问票据与 Gatekeeper —— 少了这一段,
	// 「签了、没公证」与「签了、公证了」在流水线上完全一样。
	verify, err := os.ReadFile(filepath.Join("..", "..", "scripts", "verify-macos-release.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stapler validate", "spctl --assess --type execute", "context:primary-signature", "(runtime"} {
		if !strings.Contains(string(verify), want) {
			t.Errorf("verify-macos-release.sh 缺 %q —— 没公证的 Developer ID 包会被放过", want)
		}
	}
}
