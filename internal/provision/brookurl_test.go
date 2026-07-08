package provision

import "testing"

func TestDefaultBrookURLWindows(t *testing.T) {
	got := defaultBrookURL("v20260101.0", "windows", "amd64")
	want := "https://github.com/txthinking/brook/releases/download/v20260101.0/brook_windows_amd64.exe"
	if got != want {
		t.Errorf("windows brook URL 错:\n got=%q\nwant=%q", got, want)
	}
}

func TestDefaultBrookURLWindowsArm64(t *testing.T) {
	got := defaultBrookURL("v20260101.0", "windows", "arm64")
	want := "https://github.com/txthinking/brook/releases/download/v20260101.0/brook_windows_arm64.exe"
	if got != want {
		t.Errorf("windows arm64 brook URL 错: got=%q want=%q", got, want)
	}
}

// 非 windows 无 .exe 后缀(理论兜底,通常已内嵌不走此路)。
func TestDefaultBrookURLLinuxNoExe(t *testing.T) {
	got := defaultBrookURL("v20260101.0", "linux", "amd64")
	want := "https://github.com/txthinking/brook/releases/download/v20260101.0/brook_linux_amd64"
	if got != want {
		t.Errorf("linux brook URL 错: got=%q want=%q", got, want)
	}
}

// version 为空 → 空串(不拼坏 URL)。
func TestDefaultBrookURLEmptyVersion(t *testing.T) {
	if got := defaultBrookURL("", "windows", "amd64"); got != "" {
		t.Errorf("空 version 应返回空串, got=%q", got)
	}
}
