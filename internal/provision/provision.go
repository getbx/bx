// Package provision 把内嵌的 brook 与列表快照落盘到运行期数据目录(默认 /var/lib/bx)。
package provision

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// embedCacheKey 把版本 tag 与内嵌字节的内容 hash 拼成缓存键。同 tag 不同字节(如重嵌换了
// 构建 tag,比如 sing-box 从 with_utls 加到 with_utls,with_quic)也会失效旧缓存、强制重释放,
// 避免「同版本不同内容」用到已落盘的陈旧二进制。
func embedCacheKey(version string, b []byte) string {
	sum := sha256.Sum256(b)
	return version + ":" + hex.EncodeToString(sum[:])[:12]
}

// EnsureBrook 确保 brook 可执行存在并返回其路径。
// override 非空时直接用该路径(用户显式指定,需存在);否则把 brookBytes 解压到
// dataDir/brook,当 dataDir/.brook-version 与 version 不一致(或目标缺失)时重新解压。
// EnsureBrook 优先级:override(本地路径)> brookBytes(内嵌,linux/darwin,不联网)>
// 下载兜底(windows/其他 arch:url 空则按 version 派生官方 release 地址,sha256hex 非空强校验)。
func EnsureBrook(dataDir, override string, brookBytes []byte, version, url, sha256hex string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("指定的 brook 路径不可用 %q: %w", override, err)
		}
		return override, nil
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(dataDir, "brook")
	// 有内嵌(linux/darwin amd64/arm64):落盘,按版本缓存,零联网。
	if len(brookBytes) > 0 {
		verFile := filepath.Join(dataDir, ".brook-version")
		key := embedCacheKey(version, brookBytes)
		if cur, err := os.ReadFile(verFile); err == nil && string(cur) == key {
			if _, err := os.Stat(target); err == nil {
				return target, nil
			}
		}
		if err := atomicWrite(target, brookBytes, 0o755); err != nil {
			return "", err
		}
		_ = os.WriteFile(verFile, []byte(key), 0o644)
		return target, nil
	}
	// 无内嵌(windows/其他 arch):下载兜底。url 空则按版本派生官方 release 地址。
	if url == "" {
		url = defaultBrookURL(version, runtime.GOOS, runtime.GOARCH)
	}
	if url == "" {
		return "", fmt.Errorf("无内嵌 brook 且无下载地址(brook 版本未知);设 config brook_url 或 brook 本地路径")
	}
	// 缓存键:有 sha 用 sha(供应链 pin),否则用 url —— 至少避免每次 up 重下同一个二进制。
	cacheKey := sha256hex
	if cacheKey == "" {
		cacheKey = url
	}
	srcFile := filepath.Join(dataDir, ".brook-src")
	if cur, err := os.ReadFile(srcFile); err == nil && string(cur) == cacheKey {
		if _, err := os.Stat(target); err == nil {
			return target, nil
		}
	}
	data, err := downloadBinary(url, sha256hex, "brook")
	if err != nil {
		return "", err
	}
	if err := atomicWrite(target, data, 0o755); err != nil {
		return "", err
	}
	_ = os.WriteFile(srcFile, []byte(cacheKey), 0o644)
	return target, nil
}

// EnsureLists 确保 china 列表存在(缺失才从内嵌快照解压;已存在的可能是刷新过的新版,不覆盖)。
func EnsureLists(dataDir string, domainBytes, cidrBytes []byte) (domainPath, cidrPath string, err error) {
	if err = os.MkdirAll(dataDir, 0o755); err != nil {
		return "", "", err
	}
	domainPath = filepath.Join(dataDir, "china_domain.txt")
	cidrPath = filepath.Join(dataDir, "china_cidr4.txt")
	if _, e := os.Stat(domainPath); os.IsNotExist(e) {
		if err = atomicWrite(domainPath, domainBytes, 0o644); err != nil {
			return "", "", err
		}
	}
	if _, e := os.Stat(cidrPath); os.IsNotExist(e) {
		if err = atomicWrite(cidrPath, cidrBytes, 0o644); err != nil {
			return "", "", err
		}
	}
	return domainPath, cidrPath, nil
}

// atomicWrite 写临时文件后 rename,避免覆盖正在执行的文件触发 ETXTBSY/读到半截。
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
