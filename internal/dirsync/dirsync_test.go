package dirsync

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 目录同步在**支持它的平台上必须真的做**:一次 rename 之后不同步目录,
// 掉电就可能留下「文件没了、新文件也没落盘」的中间态,而这个仓库的原子写
// (guardian 的状态、启动标记、升级事务)整套都押在 rename 的原子性上。
func TestSyncSucceedsOnARealDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Sync(dir); err != nil {
		t.Fatalf("同步一个真实目录失败了:%v", err)
	}
}

// **Windows 上目录同步做不到,而「做不到」不是「失败」。**
// `FlushFileBuffers` 对目录句柄一律 `Access is denied` —— 实测 CI 的 windows
// 腿上 515 条失败里有 345 条是这一个错误。它不是 bx 的 bug、也不是测试的 bug:
// 那个平台压根不提供这个操作,NTFS 的元数据持久性也不靠它。
// 所以这一层要**如实无操作**,而不是把一个平台事实报成一次写盘失败。
func TestSyncIsANoOpWhereTheOSCannotDoIt(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("只在 windows 上成立;别的平台由上面那条钉住它真的做了")
	}
	if err := Sync(t.TempDir()); err != nil {
		t.Errorf("windows 上不该把「这个平台做不到」报成错误:%v", err)
	}
}

// 路径不存在仍然要如实报错 —— 无操作只对「平台做不到」成立,
// 不许顺手把「你要同步的那个目录根本不在」也吞掉。
func TestSyncStillReportsAMissingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 上整个是无操作,这条性质在那儿不成立")
	}
	if err := Sync(filepath.Join(t.TempDir(), "没有这个目录")); err == nil {
		t.Error("同步一个不存在的目录居然成功了")
	}
}
