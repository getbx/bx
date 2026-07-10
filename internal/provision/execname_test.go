package provision

import (
	"runtime"
	"testing"
)

func TestExecName(t *testing.T) {
	got := execName("sing-box")
	want := "sing-box"
	if runtime.GOOS == "windows" {
		want = "sing-box.exe"
	}
	if got != want {
		t.Fatalf("execName(sing-box) on %s = %q, want %q", runtime.GOOS, got, want)
	}
}

// 已带 .exe 不重复加(幂等)。
func TestExecNameIdempotent(t *testing.T) {
	if runtime.GOOS == "windows" && execName("sing-box.exe") != "sing-box.exe" {
		t.Fatalf("execName 应幂等,不重复加 .exe")
	}
}
