//go:build !(linux && amd64) && !(linux && arm64) && !(darwin && amd64) && !(darwin && arm64) && !(windows && amd64) && !(windows && arm64)

package embedded

// 无内嵌 sing-box 的平台(其他 arch):singbox 为 nil,
// reality 传输回落到 provision.EnsureSingbox 的下载/override 路径。
var singbox []byte
