//go:build !(linux && amd64) && !(linux && arm64)

package embedded

// 无内嵌 sing-box 的平台(darwin/windows/其他 arch):singbox 为 nil,
// reality 传输回落到 provision.EnsureSingbox 的下载/override 路径。
var singbox []byte
