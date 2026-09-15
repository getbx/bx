//go:build windows

package dirsync

// supported 为假:Windows 对目录句柄的 FlushFileBuffers 一律
// `Access is denied` —— 不是权限不够,是那个操作对目录不存在。
const supported = false
