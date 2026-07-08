package provision

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"
)

// downloadBinary 下载 url 的二进制,sha256hex 非空时强校验(不匹配硬失败,供应链防护)。
// brook / sing-box 的无内嵌兜底共用它。
func downloadBinary(url, sha256hex, name string) ([]byte, error) {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("下载 %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载 %s: HTTP %d", name, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读 %s 响应: %w", name, err)
	}
	if sha256hex != "" {
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != sha256hex {
			return nil, fmt.Errorf("%s SHA256 不匹配: 期望 %s 实得 %s", name, sha256hex, got)
		}
	}
	return data, nil
}
