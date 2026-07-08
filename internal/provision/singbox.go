package provision

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// EnsureSingbox 确保 sing-box 可执行存在并返回路径(REALITY 传输用)。
// 优先级:override(本地指定路径)> embedded(内嵌字节,默认路径,零外部依赖、根除自举悖论)> 下载(url+sha256 pin 兜底)。
//   - override 非空:直接用该路径(需存在)。
//   - embedded 非空:落盘 dataDir/sing-box,按 embeddedVersion 缓存(版本未变且文件在则复用),不联网。
//   - 否则(无内嵌 arch,如 windows)下载:url 空则按 embeddedVersion 派生官方 windows release
//     地址;url 以 .zip 结尾时下载后解压出 sing-box.exe(官方 windows 包形态);sha256hex 校验
//     下载物(.zip 本身),非空时不匹配硬失败(供应链防护)。
func EnsureSingbox(dataDir, override string, embedded []byte, embeddedVersion, url, sha256hex string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("指定的 sing-box 路径不可用 %q: %w", override, err)
		}
		return override, nil
	}
	target := filepath.Join(dataDir, "sing-box")
	if len(embedded) > 0 {
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return "", err
		}
		verFile := filepath.Join(dataDir, ".singbox-version")
		key := embedCacheKey(embeddedVersion, embedded)
		if cur, err := os.ReadFile(verFile); err == nil && string(cur) == key {
			if _, err := os.Stat(target); err == nil {
				return target, nil
			}
		}
		if err := atomicWrite(target, embedded, 0o755); err != nil {
			return "", err
		}
		_ = os.WriteFile(verFile, []byte(key), 0o644)
		return target, nil
	}
	if url == "" {
		url = defaultSingboxURL(embeddedVersion, runtime.GOOS, runtime.GOARCH)
	}
	if url == "" {
		return "", fmt.Errorf("reality 传输需要内嵌 sing-box、singbox_url 或 singbox_bin")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	// 缓存键:有 sha 用 sha(供应链 pin),否则用 url —— 避免每次 up 重下同一个包。
	cacheKey := sha256hex
	if cacheKey == "" {
		cacheKey = url
	}
	srcFile := filepath.Join(dataDir, ".singbox-src")
	if cur, err := os.ReadFile(srcFile); err == nil && string(cur) == cacheKey {
		if _, err := os.Stat(target); err == nil {
			return target, nil
		}
	}
	data, err := downloadBinary(url, sha256hex, "sing-box")
	if err != nil {
		return "", err
	}
	// 官方 windows release 是 .zip(内含 sing-box.exe);裸二进制 url 直接落盘。
	// sha256hex 校验的是下载物(zip 本身),校验通过再解压。
	if strings.HasSuffix(strings.ToLower(url), ".zip") {
		data, err = extractSingbox(data, runtime.GOOS)
		if err != nil {
			return "", fmt.Errorf("解压 sing-box: %w", err)
		}
	}
	if err := atomicWrite(target, data, 0o755); err != nil {
		return "", err
	}
	_ = os.WriteFile(srcFile, []byte(cacheKey), 0o644)
	return target, nil
}
