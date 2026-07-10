package provision

import (
	"runtime"
	"strings"
)

// execName 给可执行基名按平台补后缀:windows 加 .exe(已有则不重复),其余原样。
// 内嵌/下载的 sing-box、brook 释放到磁盘后要 exec,windows 需 .exe 才稳妥可执行。
func execName(base string) string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(base), ".exe") {
		return base + ".exe"
	}
	return base
}
