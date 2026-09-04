//go:build windows

package provision

// statfsFree 在 Windows 上暂不探测:返回「没问出来」,预检放行,写失败仍由
// describeWriteError 解释。小根盘/内存盘的形状在 Windows 客户端上没有出现过。
func statfsFree(string) (uint64, bool) { return 0, false }
