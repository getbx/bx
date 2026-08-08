package cli

import (
	"bufio"
	"fmt"
	"io"
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
func confirmOnTTY(prompt string) bool {
	return confirmPrompt(os.Stdin, stdinIsTerminal(), os.Stdout, prompt)
}

// confirmPrompt 是 confirmOnTTY 的可注入内核(interactive 与 in/out 由调用方给)。
//
// **没有终端 = 取消**。没人可问的时候默认「继续」等于把一次会断网的操作强加给
// 每一个非终端调用方(脚本、CI、打包步骤、将来某个 shell 出去的 bx update),
// 而且无从退出 —— --yes 只是把本来就会发生的事写明,那不叫确认。要在非交互环境
// 里升级,调用方显式加 --yes 表态;菜单正是这么做的(它自己先弹了一个 NSAlert)。
func confirmPrompt(in io.Reader, interactive bool, out io.Writer, prompt string) bool {
	if !interactive {
		fmt.Fprintf(out, "%s\n已取消:当前不是交互式终端,无法确认。如需在脚本/自动化中升级,加 --yes 重试。\n", prompt)
		return false
	}
	fmt.Fprintf(out, "%s [y/N] ", prompt)
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && answer == "" {
		// 读不到回答(EOF/中断)按取消处理:人在终端前却没答话,
		// 不能替他决定一次会断网的操作。
		fmt.Fprintln(out)
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
