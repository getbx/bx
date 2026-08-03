package cli

import (
	"fmt"
	"strings"
)

// bundleRootFromExecutable 从「Bx.app 内可执行文件路径」反推出 Bx.app 包根路径。
// 纯函数,darwin/other 共用(app-install 的 --app-source 缺省从自身可执行路径推导)。
func bundleRootFromExecutable(executable string) (string, error) {
	const marker = "/Bx.app/Contents/Resources/"
	idx := strings.Index(executable, marker)
	if idx < 0 {
		return "", fmt.Errorf("executable %q is not inside a Bx.app bundle; pass --app-source", executable)
	}
	return executable[:idx+len("/Bx.app")], nil
}
