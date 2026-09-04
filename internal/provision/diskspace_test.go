package provision

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// **data_dir 放不下内嵌二进制时,必须在写之前就说清楚该改什么。**
//
// 真机 2026-09-03(QNAP QTS):`/` 是几百 MB 的内存盘,默认 data_dir=/var/lib/bx,
// reality 要解 29MB 的 sing-box → `no space left on device`,bx 反复 rc=1 起不来,
// 报错里没有一个字提到 data_dir。更糟的一半:往一块 91% 的内存盘上灌 29MB,
// 先被打死的是 NAS 上别的服务,不是 bx。所以判据要在写之前,不是写失败之后。
func TestEnsureSingboxRefusesToFillATinyDataDir(t *testing.T) {
	dir := t.TempDir()
	restore := diskFree
	diskFree = func(string) (uint64, bool) { return 10, true }
	t.Cleanup(func() { diskFree = restore })

	_, err := EnsureSingbox(dir, "", []byte("SINGBOX-29MB-PRETEND"), "v1", "", "")
	if err == nil {
		t.Fatal("空间不够却写成功了")
	}
	if !strings.Contains(err.Error(), "data_dir") {
		t.Errorf("报错没提示改 data_dir:%v", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("报错没点名是哪个目录放不下:%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "sing-box")); !os.IsNotExist(statErr) {
		t.Errorf("空间不够还是把文件写出去了(或留了半截)")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("目录里留下了东西:%v", entries)
	}
}

// 探不出可用空间(平台没实现、statfs 失败)时**照常写** —— 这道门是为小根盘
// 设备加的保险,不是新的启动前置;探不出来就退回写失败那条路,别把「没问到」
// 变成拒绝启动。
func TestEnsureBrookStillWritesWhenFreeSpaceIsUnknown(t *testing.T) {
	dir := t.TempDir()
	restore := diskFree
	diskFree = func(string) (uint64, bool) { return 0, false }
	t.Cleanup(func() { diskFree = restore })

	if _, err := EnsureBrook(dir, "", []byte("BROOKv1"), "v1", "", ""); err != nil {
		t.Fatalf("探不出空间就不该拒绝:%v", err)
	}
}

// 第二道防线:预检放行了但内核仍报 ENOSPC(别的进程同时在写),错误里同样要
// 带上 data_dir 的提示 —— 两条路上用户看到的都是可行动的话。
func TestWriteErrorNamesDataDirOnENOSPC(t *testing.T) {
	err := describeWriteError("/var/lib/bx/sing-box", &os.PathError{Op: "write", Path: "/var/lib/bx/.tmp-1", Err: syscall.ENOSPC})
	if !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("包装后丢了原始 errno:%v", err)
	}
	if !strings.Contains(err.Error(), "data_dir") {
		t.Errorf("ENOSPC 没提示改 data_dir:%v", err)
	}
	other := describeWriteError("/x", errors.New("boom"))
	if strings.Contains(other.Error(), "data_dir") {
		t.Errorf("不是空间问题也在劝改 data_dir:%v", other)
	}
}

// 真实的探测在本机必须能跑通:临时目录所在分区可用空间不会是 0。
func TestDiskFreeProbesTheRealFilesystem(t *testing.T) {
	free, ok := diskFree(t.TempDir())
	if !ok || free == 0 {
		t.Fatalf("真实探测失败: free=%d ok=%v", free, ok)
	}
}
