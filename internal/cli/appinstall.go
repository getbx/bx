package cli

import (
	"bufio"
	"fmt"
	"os"
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

// confirmOnTTY 就地问用户一句「继续吗」,返回 true 表示继续。
//
// stdin 不是终端时不问,直接继续并把问题连同这个事实打印出来(留在
// install.log / 菜单的 privileged 输出里)。这是有意的:此时没有人可问,而
// 「默认取消」会把菜单里的 Install/Repair 变成一个看起来成功、实际什么都没做
// 的按钮 —— 正是本期要消灭的那类失败(2026-08-08 的事故就是「照做也没用的
// 指引」)。真正不想被打断、又身处终端的调用方用 --yes 显式表态。
func confirmOnTTY(prompt string) bool {
	if !stdinIsTerminal() {
		fmt.Printf("%s(非交互环境,自动继续)\n", prompt)
		return true
	}
	fmt.Printf("%s [y/N] ", prompt)
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && answer == "" {
		// 读不到回答(EOF/中断)按取消处理:人在终端前却没答话,
		// 不能替他决定一次会断网的操作。
		fmt.Println()
		return false
	}
	return confirmationAccepted(answer)
}

// confirmationAccepted 只认明确的 y/yes;直接回车(默认)是取消。
func confirmationAccepted(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
