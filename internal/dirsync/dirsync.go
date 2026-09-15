// Package dirsync 把「让一次 rename 真的落到盘上」收成一处。
//
// **它是叶子包,不引本仓库任何东西**(理由同 internal/udpsource、
// internal/barriercidr):同一个平台事实此前在三个包里各写了一遍
// (guardian 的状态存储、启动标记、升级事务,以及 internal/update 的安装器),
// 而四份拷贝里只要有一份没跟上,失败方式是**静默的**——写盘照常成功,
// 只有掉电那一次才看得出来。
//
// **Windows 上目录同步做不到,而「做不到」不是「失败」。** `FlushFileBuffers`
// 对目录句柄一律 `Access is denied`;NTFS 的元数据持久性也不靠它。2026-09-15
// 实测:CI 的 windows 腿上 515 条失败里 **345 条**是这一个错误,而它既不是 bx
// 的 bug 也不是测试的 bug —— 是四处代码把一个平台事实当成了一次写盘失败。
// (那三个包今天都只在 darwin/linux 上真的跑,所以这从来不是用户可见的故障;
// 但它让 windows 那条 CI 腿**永远不可能绿**,于是整条腿停止了当闸门。)
package dirsync

import "os"

// Sync 让 path 这个目录的元数据落盘。在做不到这件事的平台上如实无操作。
func Sync(path string) error {
	if !supported {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// SyncRoot 是调用方已经持着一个 *os.Root 时的那一路(升级事务用)。
// **不走 Sync(path)**:那会把一个已经被 root 约束住的路径重新按字符串打开,
// 而 os.Root 存在的全部意义就是不让路径再被解析第二次。
func SyncRoot(root *os.Root) error {
	if !supported {
		return nil
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
