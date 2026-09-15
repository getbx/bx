//go:build !windows

package dirsync

// supported:这个平台的目录句柄接受 fsync。
const supported = true
