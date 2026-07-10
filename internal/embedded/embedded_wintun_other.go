//go:build !(windows && amd64) && !(windows && arm64)

package embedded

// 非 windows/amd64|arm64:无内嵌 wintun(wintun 仅 windows 用),wintun 为 nil。
var wintun []byte
