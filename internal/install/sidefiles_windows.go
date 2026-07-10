//go:build windows

package install

import (
	"fmt"
	"os"
	"path/filepath"
)

// installPlatformSideFiles 把 Windows 运行所需的随行文件(wintun.dll)从源 exe 同目录复制到
// 安装目录(BinPath 同目录)。wintun 的 tun.Device 用默认 DLL 搜索路径加载 "wintun.dll",
// 服务以 System32 为 CWD 跑 Program Files\bx\bx.exe 时,DLL 必须在 exe 同目录才找得到。
// 源目录缺 wintun.dll → 清晰报错(Windows 必需),让用户从 wintun.net 取 amd64 版放到 bx.exe 旁。
func installPlatformSideFiles(srcExe, dstExe string) error {
	src := filepath.Join(filepath.Dir(srcExe), "wintun.dll")
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("Windows 需要 wintun.dll 与 bx.exe 同目录(未找到 %s);从 https://www.wintun.net 下载对应架构版放到 bx.exe 旁再重试: %w", src, err)
	}
	dst := filepath.Join(filepath.Dir(dstExe), "wintun.dll")
	if err := copyExecutable(src, dst); err != nil {
		return fmt.Errorf("安装 wintun.dll 到 %s: %w", dst, err)
	}
	return nil
}
