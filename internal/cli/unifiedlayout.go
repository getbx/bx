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

// unifiedLayoutDegraded:统一布局产物存在但健康检查不过(半途安装/损坏)。
// 此状态下 setup/update 绝不能回退 legacy 路径(SelfInstall/ReplaceBinary 会覆盖 bridge)。
func unifiedLayoutDegraded() bool {
	return unifiedLayoutDegradedWith(runtime.GOOS,
		unifiedTeardownNeeded(darwinRuntimeRootPath, darwinAppBundlePath),
		runtimedir.Installed(runtimedir.Root))
}

func unifiedLayoutDegradedWith(goos string, artifactsPresent, healthy bool) bool {
	return goos == "darwin" && artifactsPresent && !healthy
}

const unifiedRepairHint = "统一安装已损坏(runtime/current 不完整):请打开 /Applications/Bx.app 点 Install bx 修复,或运行 sudo /Applications/Bx.app/Contents/Resources/bx-cli app-install。为避免覆盖 CLI bridge,本命令不会回退 legacy 安装。"
