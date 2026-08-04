package supervisor

import (
	"path/filepath"
	"testing"
)

// TestCoreSocketLivesInOwnedRuntimeDir 钉住 Core 控制 socket/pid 在所有平台上都落在
// bx 自有的运行时子目录下,而非共享系统目录(linux /run、darwin /var/run 的属主/
// 权限不受 bx 掌控,对它们做 chmod/校验既越权又可能因属主不是 euid 而硬失败)。
func TestCoreSocketLivesInOwnedRuntimeDir(t *testing.T) {
	if filepath.Dir(SockPath) != RuntimeDir {
		t.Fatalf("SockPath must live under RuntimeDir %q: got %q", RuntimeDir, SockPath)
	}
	if filepath.Dir(PidPath) != RuntimeDir {
		t.Fatalf("PidPath must live under RuntimeDir %q: got %q", RuntimeDir, PidPath)
	}
	if len(SockPath) >= 104 {
		t.Fatalf("SockPath %q exceeds sun_path limit", SockPath)
	}
	if RuntimeDir == "/run" || RuntimeDir == "/var/run" {
		t.Fatalf("RuntimeDir %q must be a bx-owned subdirectory, never a shared system dir", RuntimeDir)
	}
}
