package provision

import (
	"archive/zip"
	"bytes"
	"testing"
)

func TestDefaultSingboxURLWindows(t *testing.T) {
	got := defaultSingboxURL("1.13.14", "windows", "amd64")
	want := "https://github.com/SagerNet/sing-box/releases/download/v1.13.14/sing-box-1.13.14-windows-amd64.zip"
	if got != want {
		t.Errorf("windows sing-box URL 错(注意 tag 带 v、文件名不带):\n got=%q\nwant=%q", got, want)
	}
}

func TestDefaultSingboxURLWindowsArm64(t *testing.T) {
	got := defaultSingboxURL("1.13.14", "windows", "arm64")
	want := "https://github.com/SagerNet/sing-box/releases/download/v1.13.14/sing-box-1.13.14-windows-arm64.zip"
	if got != want {
		t.Errorf("windows arm64 URL 错: got=%q want=%q", got, want)
	}
}

// 容忍版本文件带不带 v 前缀:tag 恒需 v、文件名恒不带,故 "v1.13.14" 与 "1.13.14" 应同结果,
// 绝不能拼出 vv1.13.14(将来 CI 若把 SINGBOX_VERSION 写成带 v 就会 404)。
func TestDefaultSingboxURLToleratesVPrefix(t *testing.T) {
	want := "https://github.com/SagerNet/sing-box/releases/download/v1.13.14/sing-box-1.13.14-windows-amd64.zip"
	for _, ver := range []string{"1.13.14", "v1.13.14"} {
		if got := defaultSingboxURL(ver, "windows", "amd64"); got != want {
			t.Errorf("ver=%q 应产出规范 URL(不重复 v):\n got=%q\nwant=%q", ver, got, want)
		}
	}
}

// 仅 windows 需下载兜底;linux/darwin 已内嵌 → 空(逼走内嵌/报错,不拼 .tar.gz 半支持)。
func TestDefaultSingboxURLNonWindowsEmpty(t *testing.T) {
	if got := defaultSingboxURL("1.13.14", "linux", "amd64"); got != "" {
		t.Errorf("非 windows 应空, got=%q", got)
	}
	if got := defaultSingboxURL("", "windows", "amd64"); got != "" {
		t.Errorf("空版本应空, got=%q", got)
	}
}

// buildZip 造一个内存 zip(模拟官方包:可执行在带版本的子目录里 + 一个干扰文件)。
func buildZip(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range members {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractSingboxWindows(t *testing.T) {
	z := buildZip(t, map[string][]byte{
		"sing-box-1.13.14-windows-amd64/LICENSE":      []byte("license"),
		"sing-box-1.13.14-windows-amd64/sing-box.exe": []byte("MZFAKEBINARY"),
	})
	got, err := extractSingbox(z, "windows")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "MZFAKEBINARY" {
		t.Errorf("解压内容错: %q", got)
	}
}

func TestExtractSingboxNonWindowsLooksForBareName(t *testing.T) {
	z := buildZip(t, map[string][]byte{
		"sing-box-1.13.14-linux-amd64/sing-box": []byte("ELFFAKE"),
	})
	got, err := extractSingbox(z, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ELFFAKE" {
		t.Errorf("解压内容错: %q", got)
	}
}

func TestExtractSingboxMissingMemberErrors(t *testing.T) {
	z := buildZip(t, map[string][]byte{"junk/readme.txt": []byte("x")})
	if _, err := extractSingbox(z, "windows"); err == nil {
		t.Fatal("zip 内无 sing-box.exe 应报错")
	}
}
