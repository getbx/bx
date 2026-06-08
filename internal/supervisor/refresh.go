package supervisor

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/dialer"
	"github.com/getbx/bx/internal/route"
)

const (
	listDomainURL = "https://txthinking.github.io/bypass/china_domain.txt"
	listCIDRURL   = "https://txthinking.github.io/bypass/china_cidr4.txt"
)

// proxyHTTPClient 构造经 socks5 代理拨号的 http.Client(绕过 github 直连封锁)。
func proxyHTTPClient(pd dialer.ContextDialer) *http.Client {
	return &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{DialContext: pd.DialContext},
	}
}

func httpGet(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func atomicWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // best-effort 清理临时文件
		return err
	}
	return nil
}

// fetchLists 经 client 拉两份列表并原子写入 dataDir。
func fetchLists(ctx context.Context, client *http.Client, dataDir string) error {
	for _, j := range []struct{ url, name string }{
		{listDomainURL, "china_domain.txt"},
		{listCIDRURL, "china_cidr4.txt"},
	} {
		body, err := httpGet(ctx, client, j.url)
		if err != nil {
			return fmt.Errorf("拉 %s: %w", j.name, err)
		}
		if err := atomicWriteFile(filepath.Join(dataDir, j.name), body); err != nil {
			return err
		}
	}
	return nil
}

func readListFile(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// rebuildRouterFromFiles 从落盘列表重建 Router(沿用 BuildRouter 优先级与内建私网直连)。
func rebuildRouterFromFiles(cfg *config.Config, domainPath, cidrPath string, global bool) (*route.Router, error) {
	r, err := BuildRouter(cfg, readListFile(domainPath), readListFile(cidrPath))
	if err != nil {
		return nil, err
	}
	r.GlobalProxy = global
	return r, nil
}

// refreshLoop 周期刷新:仅在 healthy() 为真时执行 doRefresh;失败非致命。ctx 取消即退出。
func refreshLoop(ctx context.Context, interval time.Duration, healthy func() bool, doRefresh func() error) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !healthy() {
				continue
			}
			if err := doRefresh(); err != nil {
				log.Printf("列表刷新失败(保留旧列表): %v", err)
			} else {
				log.Printf("china 列表已刷新并热重载")
			}
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
