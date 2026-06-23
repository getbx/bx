package provision

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// EnsureSingbox 确保 sing-box 可执行存在并返回路径(REALITY 传输按需用)。
// override 非空:直接用该路径(需存在)。否则从 url 下载到 dataDir/sing-box,
// 当 dataDir/.singbox-sha 记录与 sha256hex 一致且文件在时复用(免重复下载,过固件靠 /usrdata)。
// sha256hex 非空时强校验,不匹配硬失败(供应链防护)。
func EnsureSingbox(dataDir, override, url, sha256hex string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("指定的 sing-box 路径不可用 %q: %w", override, err)
		}
		return override, nil
	}
	if url == "" {
		return "", fmt.Errorf("reality 传输需要 singbox_url 或 singbox_bin")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(dataDir, "sing-box")
	verFile := filepath.Join(dataDir, ".singbox-sha")
	if sha256hex != "" {
		if cur, err := os.ReadFile(verFile); err == nil && string(cur) == sha256hex {
			if _, err := os.Stat(target); err == nil {
				return target, nil
			}
		}
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("下载 sing-box: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 sing-box: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读 sing-box 响应: %w", err)
	}
	if sha256hex != "" {
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != sha256hex {
			return "", fmt.Errorf("sing-box SHA256 不匹配: 期望 %s 实得 %s", sha256hex, got)
		}
	}
	if err := atomicWrite(target, data, 0o755); err != nil {
		return "", err
	}
	if sha256hex != "" {
		_ = os.WriteFile(verFile, []byte(sha256hex), 0o644)
	}
	return target, nil
}
