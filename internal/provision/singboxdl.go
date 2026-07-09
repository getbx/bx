package provision

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"strings"
)

// singboxdl.go:sing-box 无内嵌(windows)时的下载兜底纯逻辑——URL 派生 + zip 解压。
// 官方 windows release 是 `.zip`(内含 sing-box.exe),不像 brook 是裸 exe,故需解压。

// defaultSingboxURL 派生 SagerNet/sing-box 官方 windows release 的 zip 地址。
// 注意:release **tag 带 v 前缀**(v1.13.14),**资产文件名不带**(sing-box-1.13.14-…)。
// 仅 windows 需要(linux/darwin 已内嵌);其余 goos / 空版本返回 ""。
func defaultSingboxURL(version, goos, goarch string) string {
	if version == "" || goos != "windows" {
		return ""
	}
	// tag 恒带 v 前缀、资产文件名恒不带;先剥掉版本可能自带的 v 再统一拼,容忍版本文件带不带 v。
	v := strings.TrimPrefix(version, "v")
	asset := fmt.Sprintf("sing-box-%s-%s-%s", v, goos, goarch)
	return fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/v%s/%s.zip", v, asset)
}

// extractSingbox 从下载的 zip 字节里取出 sing-box 可执行(windows 找 sing-box.exe、其余 sing-box),
// 忽略其所在子目录(官方包形如 sing-box-<ver>-<os>-<arch>/sing-box.exe)。找不到则报错。
func extractSingbox(zipBytes []byte, goos string) ([]byte, error) {
	want := "sing-box"
	if goos == "windows" {
		want = "sing-box.exe"
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("读 zip: %w", err)
	}
	for _, f := range zr.File {
		if path.Base(f.Name) != want { // zip 用正斜杠,path.Base 正确
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, fmt.Errorf("zip 内未找到 %s", want)
}
