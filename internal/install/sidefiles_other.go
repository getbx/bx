//go:build !windows

package install

// linux/darwin 无随行 DLL,SelfInstall 只装 bx 二进制本身。
func installPlatformSideFiles(srcExe, dstExe string) error { return nil }
