package cli

import (
	"runtime"

	"github.com/getbx/bx/internal/runtimedir"
)

// unifiedLayoutActive 报告本机是否已装 macOS 统一 runtime 布局
// (`/Library/Application Support/bx/runtime` 下有 current 版本)。其余 GOOS 恒 false。
// 无 build tag:setup/update 等跨平台调用点需要在所有 GOOS 下编译。
func unifiedLayoutActive() bool {
	return runtime.GOOS == "darwin" && runtimedir.Installed(runtimedir.Root)
}
