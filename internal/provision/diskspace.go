package provision

import (
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
)

// diskFree 报告 dir 所在文件系统对本进程可用的字节数;第二个返回值为 false 表示
// **没问出来**(平台没实现、statfs 失败)。变量形式是为了让测试把它换成一块
// 假的小盘。
//
// 它存在的理由是一台真机:QNAP QTS 的 `/` 是几百 MB 的内存盘,默认 data_dir
// (`/var/lib/bx`)落在上面,内嵌的 29MB sing-box 一解就 ENOSPC。**写之前问,
// 不是写失败之后解释** —— 往一块 91% 的内存盘上灌 29MB,先被打死的是那台机器
// 上别的服务。
var diskFree = statfsFree

// ensureSpace 在写 data 之前确认 dir 放得下;放不下就拒绝,并把该改的那个配置键
// 直接写进错误里。探不出可用空间时放行:这道门是给小根盘设备加的保险,不是新的
// 启动前置,「没问到」不许变成「拒绝启动」。
func ensureSpace(dir string, need int) error {
	free, ok := diskFree(dir)
	if !ok || uint64(need) <= free {
		return nil
	}
	return fmt.Errorf("%s 所在分区放不下要写入的文件(需要 %s,可用 %s):%w",
		dir, humanBytes(uint64(need)), humanBytes(free), errDataDirTooSmall)
}

var errDataDirTooSmall = errors.New("小根盘/内存盘设备上请在配置里把 data_dir 指到有空间的分区(例如 NAS 的数据卷),再重启 bx")

// describeWriteError 给写盘失败补上可行动的话:ENOSPC 时点名 data_dir,其它错误
// 原样透传。原始 errno 保留在链上(errors.Is 仍能认出 ENOSPC)。
func describeWriteError(target string, err error) error {
	if errors.Is(err, syscall.ENOSPC) {
		return fmt.Errorf("写 %s: %w;%v", target, err, errDataDirTooSmall)
	}
	return err
}

func humanBytes(n uint64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// dataDirFor 取写入目标所在的目录,预检与错误提示都以它为准。
func dataDirFor(path string) string { return filepath.Dir(path) }
