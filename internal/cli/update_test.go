package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssetName(t *testing.T) {
	if got := assetName("linux", "amd64"); got != "bx_linux_amd64.tar.gz" {
		t.Fatalf("assetName = %q", got)
	}
	if got := assetName("darwin", "arm64"); got != "bx_darwin_arm64.tar.gz" {
		t.Fatalf("assetName = %q", got)
	}
}

func TestUpdateDoesNotRestartProtection(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("update.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Contains(text, "install.Restart(") {
		t.Fatal("update must not restart the protection service: process restart can release the protection path")
	}
	if strings.Contains(text, "sudo bx down && sudo bx up") {
		t.Fatal("update must not recommend a down/up cycle")
	}
}

func TestUpdateCheckJSONIsAvailableForMenu(t *testing.T) {
	app := New()
	command := findAppCommand(app, "update")
	if command == nil || !commandHasFlag(command, "json") {
		t.Fatal("update should expose --json for the macOS menu")
	}
}

func TestParseReleaseTag(t *testing.T) {
	cases := map[string]string{
		"https://github.com/getbx/bx/releases/tag/v0.2.0":     "v0.2.0",
		"https://github.com/getbx/bx/releases/tag/v1.10.3/":   "v1.10.3",
		"https://github.com/getbx/bx/releases/tag/v2.0.0?x=1": "v2.0.0",
		"https://github.com/getbx/bx/releases":                "", // 无 tag(无 release)
	}
	for u, want := range cases {
		if got := parseReleaseTag(u); got != want {
			t.Errorf("parseReleaseTag(%q) = %q, want %q", u, got, want)
		}
	}
}

func TestNewerAvailable(t *testing.T) {
	if newerAvailable("v0.2.0", "v0.2.0") {
		t.Error("同版本不应提示更新")
	}
	if !newerAvailable("v0.1.0", "v0.2.0") {
		t.Error("低版本应可更新")
	}
	if !newerAvailable("dev", "v0.2.0") {
		t.Error("dev 构建应视为可更新")
	}
	if newerAvailable("v0.2.0", "") {
		t.Error("拿不到 latest(空)时不应误报可更新")
	}
}

func TestExpectedSum(t *testing.T) {
	sums := "abc123  bx_linux_amd64.tar.gz\ndef456  bx_darwin_arm64.tar.gz\n"
	if got := expectedSum(sums, "bx_linux_amd64.tar.gz"); got != "abc123" {
		t.Fatalf("expectedSum linux = %q", got)
	}
	if got := expectedSum(sums, "bx_darwin_arm64.tar.gz"); got != "def456" {
		t.Fatalf("expectedSum darwin = %q", got)
	}
	if got := expectedSum(sums, "bx_windows_amd64.tar.gz"); got != "" {
		t.Fatalf("缺失项应返回空,得 %q", got)
	}
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("hello bx")
	sum := sha256.Sum256(data)
	hexsum := hex.EncodeToString(sum[:])
	if err := verifyChecksum(data, hexsum); err != nil {
		t.Fatalf("正确校验和应通过: %v", err)
	}
	if err := verifyChecksum(data, "deadbeef"); err == nil {
		t.Fatal("错误校验和应报错")
	}
	if err := verifyChecksum(data, ""); err == nil {
		t.Fatal("空校验和应报错(拒绝未校验的下载)")
	}
}

func TestExtractBxFromTarGz(t *testing.T) {
	want := []byte("#!/bin/sh\necho bx\n")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// 放一个干扰文件 + 目标 bx
	_ = tw.WriteHeader(&tar.Header{Name: "README", Mode: 0o644, Size: 3})
	_, _ = tw.Write([]byte("hi\n"))
	_ = tw.WriteHeader(&tar.Header{Name: "bx", Mode: 0o755, Size: int64(len(want))})
	_, _ = tw.Write(want)
	_ = tw.Close()
	_ = gz.Close()

	got, err := extractBxFromTarGz(buf.Bytes())
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("提取内容不符: %q != %q", got, want)
	}
}

func TestExtractBxFromTarGzMissing(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "notbx", Mode: 0o755, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = gz.Close()
	if _, err := extractBxFromTarGz(buf.Bytes()); err == nil {
		t.Fatal("包里没有 bx 应报错")
	}
}
