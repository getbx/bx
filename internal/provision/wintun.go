package provision

import (
	"os"
	"path/filepath"
)

// EnsureWintun 确保 exeDir/wintun.dll 与内嵌字节一致并返回其路径。wireguard-go 的 wintun 加载器
// 只搜 exe 目录 + System32(LOAD_LIBRARY_SEARCH_APPLICATION_DIR|SEARCH_SYSTEM32),故释放到 exe 目录。
// wintunBytes 为空(无内嵌 arch)→ 返回 ("", nil):调用方靠系统已安装的 wintun.dll。
// 版本键缓存(exeDir/.wintun-version = version+内容hash):一致且文件在则复用,否则原子写出。
func EnsureWintun(exeDir string, wintunBytes []byte, version string) (string, error) {
	if len(wintunBytes) == 0 {
		return "", nil
	}
	target := filepath.Join(exeDir, "wintun.dll")
	verFile := filepath.Join(exeDir, ".wintun-version")
	key := embedCacheKey(version, wintunBytes)
	if cur, err := os.ReadFile(verFile); err == nil && string(cur) == key {
		if _, err := os.Stat(target); err == nil {
			return target, nil
		}
	}
	if err := atomicWrite(target, wintunBytes, 0o644); err != nil {
		return "", err
	}
	_ = os.WriteFile(verFile, []byte(key), 0o644)
	return target, nil
}
