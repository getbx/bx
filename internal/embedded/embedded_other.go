//go:build !(linux && amd64) && !(linux && arm64) && !(darwin && amd64) && !(darwin && arm64) && !(windows && amd64) && !(windows && arm64)

package embedded

// 无内嵌 brook 的平台(其他 GOOS/GOARCH 组合,如 windows/386):brook 为 nil,
// brook 传输回落到 provision.EnsureBrook 的下载/override 路径。
// china 列表与版本文件是平台无关的,仍在 embedded.go 内嵌。
var brook []byte
