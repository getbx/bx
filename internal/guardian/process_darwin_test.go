//go:build darwin

package guardian

import (
	"errors"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinProcessGenerationUsesStartTime(t *testing.T) {
	got, err := darwinProcessGeneration(unix.Timeval{Sec: 123, Usec: 456})
	if err != nil {
		t.Fatal(err)
	}
	if want := "darwin:123:456"; got != want {
		t.Fatalf("generation = %q, want %q", got, want)
	}
	if _, err := darwinProcessGeneration(unix.Timeval{}); err == nil {
		t.Fatal("zero Darwin start time accepted")
	}
}

// 进程已死时,inspectProcess 必须返回 ErrProcessNotRunning。
//
// macOS 上 SysctlKinfoProc 对不存在的 PID **不返回 ESRCH**:内核 sysctl 调用成功
// 但写回 0 字节,x/sys 因 n != SizeofKinfoProc 把它转成 **EIO**
// (x/sys@v0.45.0 syscall_darwin.go:513)。只认 ESRCH/ENOENT 会让
// ErrProcessNotRunning 在 macOS 上永远不可达,于是:
//   - Existing() 的「PID 已死就自愈」永不触发 → 陈旧记录永久卡死 bx up
//   - Start() 的「OS 确认已死才放行陈旧标记」永不触发
//   - bx down 停 Core 后确认拿到 EIO → 判 core_stop_failed → 误以为 Core 崩溃
//     → 重启 → 留下 launching 标记 → 此后每次 bx up 都 500
// 真机事故 2026-08-06:整条链在 /var/log/bx-guard.err.log 里逐行可见。
func TestInspectProcessReportsDeadPIDAsNotRunning(t *testing.T) {
	cmd := exec.Command("/usr/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("起探针进程: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("等探针进程退出: %v", err)
	}

	_, err := inspectProcess(pid)
	if !errors.Is(err, ErrProcessNotRunning) {
		t.Fatalf("已死 PID %d 的 inspectProcess = %v, want ErrProcessNotRunning——"+
			"否则 Guardian 无法分辨「进程没了」与「问不出来」,陈旧记录会永久卡死 bx up", pid, err)
	}
}

// 从未存在过的 PID 同样必须是 ErrProcessNotRunning,而不是一个不透明的 I/O 错误。
func TestInspectProcessReportsNeverExistedPIDAsNotRunning(t *testing.T) {
	_, err := inspectProcess(999999)
	if !errors.Is(err, ErrProcessNotRunning) {
		t.Fatalf("不存在的 PID 的 inspectProcess = %v, want ErrProcessNotRunning", err)
	}
}
