//go:build darwin

package supervisor

import (
	"path/filepath"
	"testing"
)

// TestDarwinCoreSocketLivesInOwnedRuntimeDir 钉住 darwin 上 Core 控制 socket/pid
// 同迁进 bx 自有运行时目录(与 Guardian 的 RuntimeDir 同构),不再散落 /var/run 根下。
func TestDarwinCoreSocketLivesInOwnedRuntimeDir(t *testing.T) {
	if filepath.Dir(SockPath) != RuntimeDir || filepath.Dir(PidPath) != RuntimeDir {
		t.Fatalf("core socket/pid must live under %q: %q %q", RuntimeDir, SockPath, PidPath)
	}
	if len(SockPath) >= 104 {
		t.Fatalf("SockPath %q exceeds sun_path limit", SockPath)
	}
}
